package verifier

import (
	"strings"
	"testing"
	"time"

	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
)

// degradedBody is what mynids' /healthz returns when the operational model
// is unavailable: HTTP 200, so a status-code check alone would pass it.
const degradedBody = `{"status":"degraded","generated_at":"2026-11-18T00:13:55.413264Z","model_loaded":true,` +
	`"ingest_enabled":true,"warnings":["The operational v2 model is unavailable; flows are stored as unvalidated until it recovers."]}`

// TestMynidsDegradedHealthEndsVerifyDegraded uses mynids' own verify entry
// from the built-in service map (plan section 8.9, M5): every check of a
// degraded body resets the count, and when the window closes Verify is
// degraded rather than succeeded or failed. The warnings are not stored.
func TestMynidsDegradedHealthEndsVerifyDegraded(t *testing.T) {
	m, err := services.Load("")
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := m.ByName("mynids")
	if !ok || svc.Verify == nil || svc.Verify.HealthBody == nil {
		t.Fatalf("mynids has no health-body check: %+v", svc.Verify)
	}
	if hb := svc.Verify.HealthBody; hb.Field != "status" || hb.Value != "ok" {
		t.Fatalf("mynids health body %s = %q, want status = ok", hb.Field, hb.Value)
	}

	srv, c := newServer(t, &response{status: 200, release: "personal-site/aaaaaaa/production/57", body: degradedBody})
	verify := *svc.Verify
	verify.URL = srv.URL + "/healthz"
	e := newEnvVerify(t, verify)
	id := e.rolledOut()

	now := at(120)
	v := e.verifier(c, &now)
	deadline := at(114).Add(verify.Window())
	for now.Before(deadline.Add(time.Minute)) {
		if err := v.Step(e.ctx); err != nil {
			t.Fatal(err)
		}
		now = now.Add(30 * time.Second)
	}

	st := e.stage(id, "verify")
	if st.State != releases.StateDegraded {
		t.Fatalf("verify %s (%s), want degraded", st.State, st.FailureReason)
	}
	r, err := e.store.GetRelease(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != releases.StateDegraded {
		t.Errorf("release %s, want degraded", r.Status)
	}
	var checks int
	for _, s := range st.Steps {
		if strings.HasPrefix(s.Name, "Check ") {
			checks++
			if s.State != releases.StateDegraded {
				t.Errorf("%s is %s, want degraded", s.Name, s.State)
			}
		}
		if strings.Contains(s.Detail, "operational v2 model") {
			t.Errorf("%s shows the health body's warning: %q", s.Name, s.Detail)
		}
	}
	if checks == 0 {
		t.Error("no check steps")
	}
	if total, passed, err := e.store.CountSamples(e.ctx); err != nil || total == 0 || passed != 0 {
		t.Errorf("samples %d (%d passed): %v", total, passed, err)
	}
}
