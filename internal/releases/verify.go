package releases

import (
	"fmt"
	"sort"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
)

// RequiredPasses is how many consecutive passing checks verify a release
// (plan section 8.5).
const RequiredPasses = 5

// FailuresInARow is how many consecutive failing checks fail Verify. One
// failing check alone is a blip on the path between mybuilds and the
// service, such as a dropped TLS handshake or the moment a Service swaps
// endpoints, so it resets the count instead.
const FailuresInARow = 2

// Sample is one verification check, stored in verification_samples.
type Sample struct {
	Endpoint          string
	OK                bool
	StatusCode        int
	LatencyMS         int64
	ObservedReleaseID string
	HealthOK          *bool
	CheckedAt         time.Time
	FailureReason     string
}

type checkObs struct {
	c       events.VerifyCheck
	eventID int64
}

type endObs struct {
	e       events.VerifyEnded
	eventID int64
}

// verify projects the Verify stage from the verifier's checks. Rollout has
// already succeeded.
//
//   - Five consecutive passes succeed.
//   - Two failing checks in a row (DNS, TLS, connection, status code,
//     latency) fail. A single failing check resets the count.
//   - A release mismatch or an unhealthy health body resets the count.
//   - When the window closes without five passes, Verify is failed after a
//     release mismatch or a failing check, and degraded if the health body
//     was the only thing in the way. With nothing to blame, such as when
//     mybuilds was down for the window, or when the only failing check was
//     the last one and never confirmed, it is unknown.
func (f *fold) verify(st *Stage, r Release) ([]Step, []Sample) {
	if f.svc.Verify == nil && len(f.checks) == 0 {
		st.State = StateSkipped
		st.Summary = "No public check is configured for this service"
		return nil, nil
	}
	if r.Kind == KindConfig && r.RuntimeReleaseID == "" && len(f.checks) == 0 {
		st.State = StateSkipped
		st.Summary = "The new pods carry no release-id annotation to check X-Release-ID against"
		return nil, nil
	}

	checks := append([]checkObs(nil), f.checks...)
	sort.SliceStable(checks, func(i, j int) bool {
		if !checks[i].c.CheckedAt.Equal(checks[j].c.CheckedAt) {
			return checks[i].c.CheckedAt.Before(checks[j].c.CheckedAt)
		}
		return checks[i].eventID < checks[j].eventID
	})

	var steps []Step
	var samples []Sample
	add := func(s Step) {
		s.StageKey, s.Attempt = "verify", 1
		steps = append(steps, s)
	}

	streak, failStreak, fails := 0, 0, 0
	// lastBad is the latest check that reset the count; lastMismatch the
	// latest that served another release, and lastFail the latest failing
	// check, which a failure names.
	var lastBad, lastMismatch, lastFail *events.VerifyCheck
	var maxLatency int64
	for i, obs := range checks {
		c := obs.c
		if i == 0 {
			st.StartedAt = timePtr(c.CheckedAt)
			add(connectionStep(c, obs.eventID))
		}
		samples = append(samples, Sample{
			Endpoint: c.Endpoint, OK: c.Result == events.CheckPass, StatusCode: c.StatusCode,
			LatencyMS: c.LatencyMS, ObservedReleaseID: c.ObservedReleaseID, HealthOK: c.HealthOK,
			CheckedAt: c.CheckedAt, FailureReason: c.Reason,
		})

		step := Step{Name: fmt.Sprintf("Check %d", i+1), StartedAt: timePtr(c.CheckedAt),
			CompletedAt:   timePtr(c.CheckedAt.Add(time.Duration(c.LatencyMS) * time.Millisecond)),
			SourceEventID: obs.eventID}
		switch c.Result {
		case events.CheckPass:
			failStreak = 0
			streak++
			maxLatency = max(maxLatency, c.LatencyMS)
			step.State = StateSucceeded
			step.Detail = fmt.Sprintf("%s, %d of %d in a row", checkDetail(c), streak, RequiredPasses)
		case events.CheckMismatch, events.CheckUnhealthy:
			streak, maxLatency, failStreak = 0, 0, 0
			bad := c
			lastBad = &bad
			if c.Result == events.CheckMismatch {
				lastMismatch = &bad
			}
			step.State = StateDegraded
			step.Detail = fmt.Sprintf("%s, %s, count reset", checkDetail(c), c.Reason)
		default:
			streak, maxLatency = 0, 0
			failStreak++
			fails++
			bad := c
			lastBad, lastFail = &bad, &bad
			step.Detail = checkDetail(c)
			if c.Reason != "" {
				step.Detail = c.Reason + ", " + step.Detail
			}
			if failStreak >= FailuresInARow {
				step.State = StateFailed
			} else {
				step.State = StateDegraded
				step.Detail += ", count reset"
			}
		}
		add(step)

		switch {
		case step.State == StateFailed:
			st.State = StateFailed
			st.CompletedAt = step.CompletedAt
			st.FailureReason = fmt.Sprintf("%s (%d checks in a row)", orDefault(c.Reason, "Check failed"), failStreak)
			return steps, samples
		case streak >= RequiredPasses:
			st.State = StateSucceeded
			st.CompletedAt = step.CompletedAt
			st.Summary = fmt.Sprintf("%d consecutive checks passed, slowest %d ms", RequiredPasses, maxLatency)
			return steps, samples
		}
	}

	if f.ended != nil {
		e := f.ended.e
		st.CompletedAt = timePtr(e.OccurredAt)
		switch {
		case e.Reason == events.EndSuperseded:
			st.State = StateUnknown
			st.FailureReason = "A newer release rolled out before verification finished"
		case len(checks) == 0:
			st.State = StateUnknown
			st.FailureReason = "No checks ran during the verification window"
		case lastMismatch != nil:
			st.State = StateFailed
			st.FailureReason = "Window closed: " + lastMismatch.Reason
		case failStreak > 0 && fails == 1:
			// The only failing check was the last one: nothing confirmed
			// it, so it is no evidence the release is down.
			st.State = StateUnknown
			st.FailureReason = "Window closed before the failed check was confirmed: " + lastFail.Reason
		case lastFail != nil:
			st.State = StateFailed
			st.FailureReason = fmt.Sprintf("Window closed after %d failed checks: %s", fails, lastFail.Reason)
		case lastBad == nil:
			st.State = StateUnknown
			st.FailureReason = fmt.Sprintf("Only %d consecutive checks passed before the window closed", streak)
		default:
			st.State = StateDegraded
			st.Summary = "Reachable and on this release, but the health check never passed 5 times in a row"
			st.FailureReason = "Window closed: " + lastBad.Reason
		}
		if st.StartedAt == nil {
			st.StartedAt = st.CompletedAt
		}
		return steps, samples
	}

	if len(checks) > 0 {
		st.State = StateRunning
		st.Summary = fmt.Sprintf("%d of %d consecutive checks passed", streak, RequiredPasses)
		if failStreak > 0 {
			st.Summary += ", last check failed: " + lastFail.Reason
		}
	}
	return steps, samples
}

// connectionStep is the first step: DNS and TLS of the first check.
func connectionStep(c events.VerifyCheck, eventID int64) Step {
	s := Step{Name: "DNS + TLS", StartedAt: timePtr(c.CheckedAt), CompletedAt: timePtr(c.CheckedAt),
		SourceEventID: eventID}
	switch {
	case !c.DNSOK:
		s.State, s.Detail = StateFailed, "DNS lookup failed"
	case !c.TLSOK:
		s.State, s.Detail = StateFailed, "TLS handshake failed"
	default:
		s.State = StateSucceeded
		s.Detail = orDefault(c.TLSVersion, "TLS")
		if c.CertNotAfter != nil {
			s.Detail += ", certificate valid until " + c.CertNotAfter.UTC().Format("2006-01-02")
		}
	}
	return s
}

// checkDetail is a check's status, latency, release and location.
func checkDetail(c events.VerifyCheck) string {
	if c.StatusCode == 0 {
		return "no response"
	}
	d := fmt.Sprintf("HTTP %d in %d ms", c.StatusCode, c.LatencyMS)
	if c.ObservedReleaseID != "" {
		d += ", " + c.ObservedReleaseID
	}
	if c.Colo != "" {
		d += ", " + c.Colo
	}
	if c.CacheStatus != "" && c.CacheStatus != "DYNAMIC" {
		d += ", cache " + c.CacheStatus
	}
	return d
}
