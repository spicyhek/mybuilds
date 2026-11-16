package kube

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/storage"
)

// comparable renders a release for comparison, without the internal UUID
// the projection does not assign.
func comparable(t *testing.T, r releases.Release) string {
	t.Helper()
	r.UUID = ""
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (h *harness) timeline(id string) []releases.Frame {
	h.t.Helper()
	frames, err := h.projector.Timeline(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return frames
}

// TestReplayMatchesLive records the release after every event, as the UI
// saw it live, and checks that the timeline passes through exactly those
// states (plan section 18, M3 exit: replaying any release matches what was
// shown live). Replay orders events by the time they occurred, so this holds
// when they arrive in that order; a late notification only changes when the
// live view caught up, never the final state.
func TestReplayMatchesLive(t *testing.T) {
	h := newHarness(t)
	g1 := strings.Repeat("1", 40)
	id := "personal-site/aaaaaaa/production/11"

	var live []string
	h.after = func() {
		r, err := h.store.GetRelease(h.ctx, id)
		if errors.Is(err, storage.ErrNotFound) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if s := comparable(t, *r); len(live) == 0 || live[len(live)-1] != s {
			live = append(live, s)
		}
	}

	h.release(11, strings.Repeat("a", 40), g1, digestOf('a'), 0)
	h.serving = id
	// Every event arrives in time order: Argo CD reports the sync running,
	// the pods start, then Argo CD reports the sync succeeded.
	h.provider(events.ProviderArgoCD, events.TypeArgoCD, 100, events.Keys{Revision: g1}, events.ArgoCDNotification{
		Trigger: events.TriggerSyncRunning, Application: "personal-site", Revision: g1, StartedAt: ptr(at(100))})
	h.rolloutWith("aaaaaaaaaa", "11", 100, digestOf('a'), func() {
		h.provider(events.ProviderArgoCD, events.TypeArgoCD, 104, events.Keys{Revision: g1}, events.ArgoCDNotification{
			Trigger: events.TriggerSyncSucceeded, Application: "personal-site", Revision: g1,
			StartedAt: ptr(at(100)), FinishedAt: ptr(at(104))})
	})
	for i := range releases.RequiredPasses {
		s := 130 + 30*i
		h.provider(events.ProviderVerifier, events.TypeVerifyCheck, s, events.Keys{ReleaseID: id}, events.VerifyCheck{
			ReleaseID: id, CheckedAt: at(s), Result: events.CheckPass, DNSOK: true, TLSOK: true,
			StatusCode: 200, LatencyMS: 90, ObservedReleaseID: id, ExpectedReleaseID: id})
	}
	if v := stageOf(h.get(id), "verify"); v.State != releases.StateSucceeded {
		t.Fatalf("verify %s (%s)", v.State, v.FailureReason)
	}

	frames := h.timeline(id)
	for i, f := range frames {
		if i >= len(live) {
			t.Fatalf("frame %d at %v (%s) was never shown live", i, f.At, f.EventType)
		}
		if got := comparable(t, f.Release); got != live[i] {
			t.Fatalf("frame %d at %v (%s) differs from live:\nreplay %s\nlive   %s", i, f.At, f.EventType, got, live[i])
		}
		if i > 0 && f.At.Before(frames[i-1].At) {
			t.Fatalf("frame %d goes back in time", i)
		}
	}
	if len(frames) != len(live) {
		t.Fatalf("timeline has %d frames, live showed %d states; first missing:\n%s", len(frames), len(live), live[len(frames)])
	}
	// Rollout runs before it succeeds, and Verify counts its checks.
	var sawRolloutRunning, sawThreePasses bool
	for _, f := range frames {
		r := f.Release
		if stageOf(&r, "rollout").State == releases.StateRunning {
			sawRolloutRunning = true
		}
		if stageOf(&r, "verify").Summary == "3 of 5 consecutive checks passed" {
			sawThreePasses = true
		}
	}
	if !sawRolloutRunning || !sawThreePasses {
		t.Errorf("rollout running seen %v, 3 passes seen %v", sawRolloutRunning, sawThreePasses)
	}
}

// TestReplayEndsAtTheStoredState covers a release deployed together with a
// config release: the runtime window comes from another commit's sync, and
// the verifier events from the partner.
func TestReplayEndsAtTheStoredState(t *testing.T) {
	h := newHarness(t)
	g1 := strings.Repeat("1", 40)
	g2 := strings.Repeat("2", 40)
	g3 := strings.Repeat("e", 40)
	r11 := h.release(11, strings.Repeat("a", 40), g1, digestOf('a'), 0)
	h.serving = r11
	h.sync(g1, 100, 104)
	h.rollout("aaaaaaaaaa", "11", 100, digestOf('a'))
	r12 := h.release(12, strings.Repeat("b", 40), g2, digestOf('b'), 150)
	h.serving = r12
	cfg := h.configSync(g3, 200, 203)
	h.rollout("bbbbbbbbbb", "12", 200, digestOf('b'))
	h.provider(events.ProviderVerifier, events.TypeVerifyCheck, 230, events.Keys{ReleaseID: r12}, events.VerifyCheck{
		ReleaseID: r12, CheckedAt: at(230), Result: events.CheckPass, DNSOK: true, TLSOK: true,
		StatusCode: 200, LatencyMS: 90, ObservedReleaseID: r12})

	for _, id := range []string{r11, r12, cfg} {
		frames := h.timeline(id)
		if len(frames) == 0 {
			t.Fatalf("%s: no frames", id)
		}
		if got, want := comparable(t, frames[len(frames)-1].Release), comparable(t, *h.get(id)); got != want {
			t.Errorf("%s: last frame differs from the stored release:\nreplay %s\nstored %s", id, got, want)
		}
	}
	// Release 11's window closed when the config commit synced; before
	// that, its replay never showed it closed.
	for _, f := range h.timeline(r11) {
		r := f.Release
		if st := stageOf(&r, "rollout"); st.State == releases.StateUnknown {
			t.Errorf("release 11 rollout unknown at %v", f.At)
		}
	}
}
