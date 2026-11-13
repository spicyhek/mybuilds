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
//   - A failing check (DNS, TLS, connection, status code, latency) fails.
//   - A release mismatch or an unhealthy health body resets the count.
//   - When the window closes without five passes, Verify is degraded if the
//     health body was the only thing in the way, and failed otherwise. With
//     no failed check to blame, such as when mybuilds was down for the
//     window, it is unknown.
func (f *fold) verify(st *Stage) ([]Step, []Sample) {
	if f.svc.Verify == nil && len(f.checks) == 0 {
		st.State = StateSkipped
		st.Summary = "No public check is configured for this service"
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

	streak := 0
	// lastBad is the latest check that reset the count; lastMismatch the
	// latest that served another release, which a failure names.
	var lastBad, lastMismatch *events.VerifyCheck
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
			streak++
			maxLatency = max(maxLatency, c.LatencyMS)
			step.State = StateSucceeded
			step.Detail = fmt.Sprintf("%s, %d of %d in a row", checkDetail(c), streak, RequiredPasses)
		case events.CheckMismatch, events.CheckUnhealthy:
			streak, maxLatency = 0, 0
			bad := c
			lastBad = &bad
			if c.Result == events.CheckMismatch {
				lastMismatch = &bad
			}
			step.State = StateDegraded
			step.Detail = fmt.Sprintf("%s, %s, count reset", checkDetail(c), c.Reason)
		default:
			step.State = StateFailed
			step.Detail = checkDetail(c)
			if c.Reason != "" {
				step.Detail = c.Reason + ", " + step.Detail
			}
		}
		add(step)

		switch {
		case step.State == StateFailed:
			st.State = StateFailed
			st.CompletedAt = step.CompletedAt
			st.FailureReason = orDefault(c.Reason, "Check failed")
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
		case lastBad == nil:
			st.State = StateUnknown
			st.FailureReason = fmt.Sprintf("Only %d consecutive checks passed before the window closed", streak)
		case lastMismatch == nil:
			st.State = StateDegraded
			st.Summary = "Reachable and on this release, but the health check never passed 5 times in a row"
			st.FailureReason = "Window closed: " + lastBad.Reason
		default:
			st.State = StateFailed
			st.FailureReason = "Window closed: " + lastMismatch.Reason
		}
		if st.StartedAt == nil {
			st.StartedAt = st.CompletedAt
		}
		return steps, samples
	}

	if len(checks) > 0 {
		st.State = StateRunning
		st.Summary = fmt.Sprintf("%d of %d consecutive checks passed", streak, RequiredPasses)
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
