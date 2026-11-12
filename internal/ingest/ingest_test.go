package ingest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/fixtures"
	"github.com/spicyhek/mybuilds/internal/ingest"
	"github.com/spicyhek/mybuilds/internal/ingest/verify"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
)

const (
	githubSecret = "test-github-secret"
	ciSecret     = "test-ci-secret"
	argoToken    = "test-argocd-token"

	releaseID = "personal-site/ce61154/production/11"
	gitopsSHA = "f8c9de901999c2984662a926a18e31a531fd7821"
)

type env struct {
	store     *storage.Store
	projector *releases.Projector
	ingester  *ingest.Ingester
}

func newEnv(t *testing.T, secrets ingest.Secrets) *env {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	svcs, err := services.Load("")
	if err != nil {
		t.Fatal(err)
	}
	projector := releases.NewProjector(store, svcs, nil)
	in := ingest.New(ingest.Options{
		Store:     store,
		Projector: projector,
		Services:  svcs,
		Secrets:   secrets,
		Now:       func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) },
	})
	return &env{store: store, projector: projector, ingester: in}
}

var allSecrets = ingest.Secrets{GitHubWebhook: githubSecret, CIEvents: ciSecret, ArgoCDToken: argoToken}

type delivery = fixtures.Delivery

func loadDeliveries(t *testing.T, name string) []delivery {
	t.Helper()
	ds, err := fixtures.Deliveries(name)
	if err != nil || len(ds) == 0 {
		t.Fatalf("recorded deliveries %s: %v", name, err)
	}
	return ds
}

var paths = map[string]string{
	events.ProviderGitHub: "/hooks/github",
	events.ProviderCI:     "/api/v1/ci-events",
	events.ProviderArgoCD: "/hooks/argocd",
}

// request builds a correctly authenticated request for a delivery.
func request(d delivery) *http.Request {
	req := httptest.NewRequest(http.MethodPost, paths[d.Provider], bytes.NewReader(d.Body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range d.Headers {
		req.Header.Set(k, v)
	}
	switch d.Provider {
	case events.ProviderGitHub:
		req.Header.Set("X-Hub-Signature-256", verify.Sign(githubSecret, d.Body))
	case events.ProviderCI:
		req.Header.Set("X-Mybuilds-Signature-256", verify.Sign(ciSecret, d.Body))
	case events.ProviderArgoCD:
		req.Header.Set("Authorization", "Bearer "+argoToken)
	}
	return req
}

func (e *env) send(t *testing.T, d delivery) (int, ingest.Result) {
	t.Helper()
	rec := httptest.NewRecorder()
	e.ingester.Handler(d.Provider).ServeHTTP(rec, request(d))
	var res ingest.Result
	if rec.Code < 300 {
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("%s: decode response %q: %v", d.Name, rec.Body.String(), err)
		}
	}
	return rec.Code, res
}

func (e *env) release(t *testing.T) *releases.Release {
	t.Helper()
	r, err := e.store.GetRelease(context.Background(), releaseID)
	if err != nil {
		t.Fatalf("get release: %v", err)
	}
	return r
}

func stageStates(r *releases.Release) map[string]releases.State {
	out := map[string]releases.State{}
	for _, st := range r.Stages {
		out[st.Key] = st.State
	}
	return out
}

func stage(r *releases.Release, key string) releases.Stage {
	for _, st := range r.Stages {
		if st.Key == key {
			return st
		}
	}
	return releases.Stage{}
}

// snapshot is the release as the API would see it, minus the random UUID.
func snapshot(t *testing.T, r *releases.Release) string {
	t.Helper()
	c := *r
	c.UUID = ""
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestRecordedReleaseMovesLive replays personal-site run #11 in the order a
// live release sends it and checks the pipeline after each step of the way
// (plan section 8.1, live order of events).
func TestRecordedReleaseMovesLive(t *testing.T) {
	e := newEnv(t, allSecrets)
	ds := loadDeliveries(t, "personal-site-11")

	type want struct {
		status releases.State
		stages map[string]releases.State
	}
	P, R, S := releases.StatePending, releases.StateRunning, releases.StateSucceeded
	checkpoints := map[string]want{
		"02-workflow-run-in-progress":    {R, map[string]releases.State{"source": S, "build": R, "publish": P}},
		"04-ci-release-published":        {R, map[string]releases.State{"build": S, "publish": S, "gitops": P}},
		"06-bump-gitops-job-in-progress": {R, map[string]releases.State{"gitops": R, "reconcile": P}},
		"07-ci-gitops-updated":           {R, map[string]releases.State{"gitops": S, "reconcile": P}},
		"10-argocd-root-sync-succeeded":  {R, map[string]releases.State{"reconcile": P}},
		"11-argocd-sync-running":         {R, map[string]releases.State{"reconcile": R}},
		"12-argocd-sync-succeeded":       {R, map[string]releases.State{"reconcile": S, "rollout": P, "verify": P}},
	}

	for _, d := range ds {
		code, res := e.send(t, d)
		if code != http.StatusAccepted || res.Outcome != ingest.OutcomeStored {
			t.Fatalf("%s: status %d outcome %q", d.Name, code, res.Outcome)
		}
		w, ok := checkpoints[d.Name]
		if !ok {
			continue
		}
		r := e.release(t)
		if r.Status != w.status {
			t.Errorf("after %s: release status = %s, want %s", d.Name, r.Status, w.status)
		}
		got := stageStates(r)
		for k, v := range w.stages {
			if got[k] != v {
				t.Errorf("after %s: stage %s = %s, want %s", d.Name, k, got[k], v)
			}
		}
	}

	r := e.release(t)
	if r.SourceSHA != "ce61154e05756b0a7e40311de30dccaa0273f305" ||
		r.ArtifactDigest != "sha256:9785af09091041a492b423d2aa88d98df1f980b5a056c055590d4b889bf092a5" ||
		r.GitOpsSHA != gitopsSHA || r.GitOpsRepository != "spicyhek/homelab-infra" {
		t.Errorf("identity = %+v", r)
	}
	if want := time.Date(2026, 11, 8, 11, 26, 25, 0, time.UTC); !r.CreatedAt.Equal(want) {
		t.Errorf("created_at = %v, want %v", r.CreatedAt, want)
	}

	wantSteps := map[string]int{"source": 2, "build": 8, "publish": 10, "gitops": 11, "reconcile": 2, "rollout": 0}
	for key, n := range wantSteps {
		if got := len(stage(r, key).Steps); got != n {
			t.Errorf("stage %s has %d steps, want %d: %+v", key, got, n, stage(r, key).Steps)
		}
	}
	if s := stage(r, "publish").Steps; len(s) > 0 && (s[0].Name != "Push image" || s[len(s)-1].Name != "Send release.published") {
		t.Errorf("publish steps run from %q to %q", s[0].Name, s[len(s)-1].Name)
	}
	if st := stage(r, "build"); st.CompletedAt == nil || !st.CompletedAt.Equal(time.Date(2026, 11, 8, 11, 26, 50, 0, time.UTC)) {
		t.Errorf("build completed_at = %v, want the smoke test's end", st.CompletedAt)
	}
	if st := stage(r, "reconcile"); st.Steps[0].Detail != "personal-site at f8c9de9" || st.Steps[1].Detail != "Sync succeeded, health progressing" {
		t.Errorf("reconcile steps = %+v", st.Steps)
	}

	// The root app-of-apps sync of the same commit is not this release's.
	total, uncorrelated, err := e.store.CountEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if total != len(ds) || uncorrelated != 1 {
		t.Errorf("events: total %d uncorrelated %d, want %d and 1", total, uncorrelated, len(ds))
	}
}

// TestReplayChangesNothing is the M1 exit criterion: sending the same
// webhooks again, or rebuilding from stored events, leaves the release as it
// was (plan section 18).
func TestReplayChangesNothing(t *testing.T) {
	e := newEnv(t, allSecrets)
	ds := loadDeliveries(t, "personal-site-11")
	for _, d := range ds {
		e.send(t, d)
	}
	before := snapshot(t, e.release(t))
	uuid := e.release(t).UUID

	for _, d := range ds {
		code, res := e.send(t, d)
		if code != http.StatusOK || res.Outcome != ingest.OutcomeDuplicate {
			t.Fatalf("%s resent: status %d outcome %q, want 200 duplicate", d.Name, code, res.Outcome)
		}
	}
	if after := snapshot(t, e.release(t)); after != before {
		t.Fatalf("release changed after resending every webhook:\n%s\n---\n%s", before, after)
	}

	if n, err := e.projector.RebuildAll(context.Background()); err != nil || n != 1 {
		t.Fatalf("rebuild: %d releases, %v", n, err)
	}
	r := e.release(t)
	if after := snapshot(t, r); after != before {
		t.Fatalf("release changed after a rebuild:\n%s\n---\n%s", before, after)
	}
	if r.UUID != uuid {
		t.Errorf("rebuild changed the release UUID")
	}
}

// TestArrivalOrderDoesNotMatter delivers the same events reversed and
// shuffled: the projection depends on provider timestamps, not arrival.
func TestArrivalOrderDoesNotMatter(t *testing.T) {
	ds := loadDeliveries(t, "personal-site-11")

	inOrder := newEnv(t, allSecrets)
	for _, d := range ds {
		inOrder.send(t, d)
	}
	want := snapshot(t, inOrder.release(t))

	reversed := slices.Clone(ds)
	slices.Reverse(reversed)
	orders := map[string][]delivery{"reversed": reversed}
	rng := rand.New(rand.NewSource(11))
	for i := range 3 {
		shuffled := slices.Clone(ds)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		orders["shuffle-"+string(rune('a'+i))] = shuffled
	}

	for name, order := range orders {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, allSecrets)
			for _, d := range order {
				if code, _ := e.send(t, d); code != http.StatusAccepted {
					t.Fatalf("%s: status %d", d.Name, code)
				}
			}
			if got := snapshot(t, e.release(t)); got != want {
				t.Fatalf("projection differs from in-order delivery:\n%s\n---\n%s", want, got)
			}
		})
	}
}

func TestStoredPayloadsAreAllowlisted(t *testing.T) {
	e := newEnv(t, allSecrets)
	for _, d := range loadDeliveries(t, "personal-site-11") {
		e.send(t, d)
	}
	evs, err := e.store.ReleaseEvents(context.Background(), releaseID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		for _, banned := range []string{"runner_name", "runner_id", "html_url", "logs_url", "check_run_url", "Release-Id: personal"} {
			if strings.Contains(string(ev.Payload), banned) {
				t.Errorf("event %d (%s) stored %q: %s", ev.ID, ev.Type, banned, ev.Payload)
			}
		}
		if len(ev.BodySHA256) != 64 {
			t.Errorf("event %d: body hash %q", ev.ID, ev.BodySHA256)
		}
	}
}

func TestAuthentication(t *testing.T) {
	ds := loadDeliveries(t, "personal-site-11")
	gh, ci, argo := ds[0], ds[3], ds[11]

	t.Run("bad signatures are rejected before parsing", func(t *testing.T) {
		e := newEnv(t, allSecrets)
		for _, tc := range []struct {
			d      delivery
			header string
			value  string
		}{
			{gh, "X-Hub-Signature-256", verify.Sign("wrong", gh.Body)},
			{gh, "X-Hub-Signature-256", ""},
			{gh, "X-Hub-Signature-256", "sha1=abc"},
			{ci, "X-Mybuilds-Signature-256", verify.Sign("wrong", ci.Body)},
			{argo, "Authorization", "Bearer wrong"},
			{argo, "Authorization", argoToken},
		} {
			req := request(tc.d)
			req.Header.Set(tc.header, tc.value)
			rec := httptest.NewRecorder()
			e.ingester.Handler(tc.d.Provider).ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s with %s=%q: status %d, want 401", tc.d.Name, tc.header, tc.value, rec.Code)
			}
		}
		if total, _, _ := e.store.CountEvents(context.Background()); total != 0 {
			t.Errorf("%d events stored from rejected requests", total)
		}
	})

	t.Run("a tampered body fails the signature", func(t *testing.T) {
		e := newEnv(t, allSecrets)
		tampered := bytes.Replace(ci.Body, []byte(`"drill":false`), []byte(`"drill":true`), 1)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ci-events", bytes.NewReader(tampered))
		req.Header.Set("X-Mybuilds-Signature-256", verify.Sign(ciSecret, ci.Body))
		rec := httptest.NewRecorder()
		e.ingester.Handler(ci.Provider).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", rec.Code)
		}
	})

	t.Run("an unconfigured provider is unavailable", func(t *testing.T) {
		e := newEnv(t, ingest.Secrets{GitHubWebhook: githubSecret})
		if code, _ := e.send(t, ci); code != http.StatusServiceUnavailable {
			t.Errorf("ci without a secret: status %d, want 503", code)
		}
		if code, _ := e.send(t, argo); code != http.StatusServiceUnavailable {
			t.Errorf("argocd without a token: status %d, want 503", code)
		}
	})

	t.Run("bodies over 1 MiB are refused", func(t *testing.T) {
		e := newEnv(t, allSecrets)
		big := bytes.Repeat([]byte(" "), ingest.MaxBodyBytes+1)
		req := httptest.NewRequest(http.MethodPost, "/hooks/github", bytes.NewReader(big))
		rec := httptest.NewRecorder()
		e.ingester.Handler(events.ProviderGitHub).ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status %d, want 413", rec.Code)
		}
	})
}

func TestUntrackedDeliveriesAreIgnored(t *testing.T) {
	e := newEnv(t, allSecrets)
	ds := loadDeliveries(t, "personal-site-11")

	ping := delivery{Name: "ping", Provider: events.ProviderGitHub,
		Headers: map[string]string{"X-GitHub-Event": "ping", "X-GitHub-Delivery": "ping-1"},
		Body:    []byte(`{"zen":"Keep it logically awesome.","hook_id":1}`)}
	push := delivery{Name: "push", Provider: events.ProviderGitHub,
		Headers: map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "push-1"},
		Body:    []byte(`{"ref":"refs/heads/main"}`)}
	pr := ds[0]
	pr.Name = "pull-request-run"
	pr.Headers = map[string]string{"X-GitHub-Event": "workflow_run", "X-GitHub-Delivery": "pr-1"}
	pr.Body = bytes.Replace(pr.Body, []byte(`"event":"push"`), []byte(`"event":"pull_request"`), 1)
	other := ds[0]
	other.Name = "other-workflow"
	other.Headers = map[string]string{"X-GitHub-Event": "workflow_run", "X-GitHub-Delivery": "other-1"}
	other.Body = bytes.Replace(other.Body, []byte(`.github/workflows/release.yml`), []byte(`.github/workflows/lint.yml`), 1)

	for _, d := range []delivery{ping, push, pr, other} {
		code, res := e.send(t, d)
		if code != http.StatusOK || res.Outcome != ingest.OutcomeIgnored || res.Reason == "" {
			t.Errorf("%s: status %d outcome %q reason %q, want 200 ignored with a reason", d.Name, code, res.Outcome, res.Reason)
		}
	}
	if total, _, _ := e.store.CountEvents(context.Background()); total != 0 {
		t.Errorf("%d events stored from ignored deliveries", total)
	}
}

func TestCIEventValidation(t *testing.T) {
	e := newEnv(t, allSecrets)
	published := loadDeliveries(t, "personal-site-11")[3]

	for _, tc := range []struct{ name, from, to string }{
		{"release ID of another service", `"release_id":"personal-site/`, `"release_id":"other/`},
		{"run number mismatch", `"workflow_run_number":11`, `"workflow_run_number":12`},
		{"short source sha", `"source_sha":"ce61154e05756b0a7e40311de30dccaa0273f305"`, `"source_sha":"ce61154"`},
		{"tag instead of digest", `"artifact_digest":"sha256:9785af09091041a492b423d2aa88d98df1f980b5a056c055590d4b889bf092a5"`, `"artifact_digest":"latest"`},
		{"unknown field", `"drill":false`, `"drill":false,"dril":true`},
		{"unknown type", `"type":"release.published"`, `"type":"release.deleted"`},
		{"wrong repository", `"source_repository":"spicyhek/homelab-hosted-personal-site"`, `"source_repository":"someone/else"`},
	} {
		d := published
		d.Body = bytes.Replace(published.Body, []byte(tc.from), []byte(tc.to), 1)
		if bytes.Equal(d.Body, published.Body) {
			t.Fatalf("%s: replacement did not apply", tc.name)
		}
		if code, _ := e.send(t, d); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", tc.name, code)
		}
	}
}
