package releases

import (
	"strings"
	"testing"

	"github.com/spicyhek/mybuilds/internal/events"
)

// rolledOut records a release whose Rollout succeeded at t=114.
func rolledOut(t *testing.T) *builder {
	b := &builder{t: t}
	throughReconcile(b)
	rollingUpdate(runtimeBuilder{b}, testDigest)
	return b
}

func (b *builder) check(at0 int, result, observed string, latency int64) {
	c := events.VerifyCheck{ReleaseID: testRelease, Endpoint: "https://example.test/healthz", CheckedAt: at(at0),
		Result: result, DNSOK: true, TLSOK: true, TLSVersion: "TLS 1.3", StatusCode: 200, LatencyMS: latency,
		ObservedReleaseID: observed, Colo: "LAX"}
	switch result {
	case events.CheckMismatch:
		c.Reason = "release mismatch: served " + observed
	case events.CheckUnhealthy:
		ok := false
		c.HealthOK = &ok
		c.Reason = `health body status is not "ok"`
	case events.CheckFail:
		c.StatusCode = 500
		c.Reason = "HTTP 500 is not an allowed status"
	}
	b.add(events.TypeVerifyCheck, at(at0), 1, events.Keys{ReleaseID: testRelease}, c)
}

func (b *builder) ended(at0 int, reason string) {
	b.add(events.TypeVerifyEnded, at(at0), 1, events.Keys{ReleaseID: testRelease},
		events.VerifyEnded{ReleaseID: testRelease, Reason: reason, OccurredAt: at(at0)})
}

const oldRelease = "personal-site/fffffff/production/56"

func project7(t *testing.T, b *builder) Projection {
	t.Helper()
	own, rt := window(b, nil)
	return projectRuntime(t, own, rt)
}

func verifyStage(p Projection) Stage { return p.Release.Stages[6] }

func TestVerifySucceedsOnFiveConsecutivePasses(t *testing.T) {
	b := rolledOut(t)
	for i := range 5 {
		b.check(120+30*i, events.CheckPass, testRelease, int64(80+i))
	}
	p := project7(t, b)
	expect(t, p.Release, S, S, S, S, S, S, S, S)
	v := verifyStage(p)
	if !v.StartedAt.Equal(at(120)) || p.Release.VerifiedAt == nil || !p.Release.VerifiedAt.Equal(*v.CompletedAt) {
		t.Errorf("verify %v to %v, verified_at %v", v.StartedAt, v.CompletedAt, p.Release.VerifiedAt)
	}
	if v.Summary != "5 consecutive checks passed, slowest 84 ms" {
		t.Errorf("summary %q", v.Summary)
	}
	names := stepNames(p, "verify")
	if len(names) != 6 || names[0] != "DNS + TLS=succeeded" || names[5] != "Check 5=succeeded" {
		t.Errorf("steps %v", names)
	}
	if len(p.Samples) != 5 || !p.Samples[0].OK {
		t.Errorf("samples %+v", p.Samples)
	}
}

func TestVerifyMismatchResetsTheCount(t *testing.T) {
	b := rolledOut(t)
	b.check(120, events.CheckPass, testRelease, 80)
	b.check(150, events.CheckPass, testRelease, 80)
	b.check(180, events.CheckMismatch, oldRelease, 80)
	for i := range 4 {
		b.check(210+30*i, events.CheckPass, testRelease, 80)
	}
	p := project7(t, b)
	expect(t, p.Release, R, S, S, S, S, S, S, R)
	if got := verifyStage(p).Summary; got != "4 of 5 consecutive checks passed" {
		t.Errorf("summary %q", got)
	}
	if got := stepNames(p, "verify")[3]; got != "Check 3=degraded" {
		t.Errorf("mismatch step %q", got)
	}

	b.check(330, events.CheckPass, testRelease, 80)
	expect(t, project7(t, b).Release, S, S, S, S, S, S, S, S)
}

func TestVerifyFailingCheckFails(t *testing.T) {
	b := rolledOut(t)
	b.check(120, events.CheckPass, testRelease, 80)
	b.check(150, events.CheckFail, testRelease, 80)
	p := project7(t, b)
	expect(t, p.Release, F, S, S, S, S, S, S, F)
	if got := verifyStage(p).FailureReason; got != "HTTP 500 is not an allowed status" {
		t.Errorf("reason %q", got)
	}
}

func TestVerifyWindowEnds(t *testing.T) {
	cases := []struct {
		name   string
		record func(*builder)
		status State
		verify State
		reason string
	}{
		{"stale release header", func(b *builder) {
			b.check(120, events.CheckPass, testRelease, 80)
			b.check(150, events.CheckMismatch, oldRelease, 80)
			b.check(180, events.CheckUnhealthy, testRelease, 80)
			b.ended(714, events.EndExpired)
		}, F, F, "release mismatch"},
		{"health body only", func(b *builder) {
			b.check(120, events.CheckPass, testRelease, 80)
			b.check(150, events.CheckUnhealthy, testRelease, 80)
			b.ended(714, events.EndExpired)
		}, StateDegraded, StateDegraded, "health body"},
		{"no checks", func(b *builder) {
			b.ended(714, events.EndExpired)
		}, U, U, "No checks ran"},
		{"too few passes", func(b *builder) {
			b.check(120, events.CheckPass, testRelease, 80)
			b.ended(714, events.EndExpired)
		}, U, U, "Only 1 consecutive"},
		{"newer release", func(b *builder) {
			b.check(120, events.CheckPass, testRelease, 80)
			b.ended(200, events.EndSuperseded)
		}, U, U, "newer release"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := rolledOut(t)
			tc.record(b)
			p := project7(t, b)
			expect(t, p.Release, tc.status, S, S, S, S, S, S, tc.verify)
			if got := verifyStage(p).FailureReason; !strings.Contains(got, tc.reason) {
				t.Errorf("reason %q, want it to contain %q", got, tc.reason)
			}
		})
	}
}

func TestVerifyWaitsForRollout(t *testing.T) {
	b := &builder{t: t}
	throughReconcile(b)
	b.check(120, events.CheckPass, testRelease, 80)
	own, rt := window(b, nil)
	expect(t, projectRuntime(t, own, rt).Release, R, S, S, S, S, S, P, P)
}

func TestVerifySkippedWithoutACheck(t *testing.T) {
	b := rolledOut(t)
	own, rt := window(b, nil)
	svc := personalSite(t)
	svc.Verify = nil
	p, err := ProjectWithRuntime(testRelease, own.evs, svc, rt)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, p.Release, S, S, S, S, S, S, S, K)
}
