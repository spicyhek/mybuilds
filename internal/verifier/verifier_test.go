package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
)

var t0 = time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)

func at(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

const (
	image  = "ghcr.io/spicyhek/homelab-hosted-personal-site"
	digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	gitops = "1111111111111111111111111111111111111111"
)

type env struct {
	t         *testing.T
	ctx       context.Context
	store     *storage.Store
	projector *releases.Projector
	svcs      services.Map
	n         int
}

func newEnv(t *testing.T, verifyURL string) *env {
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
	svcs, err := services.Parse([]byte(fmt.Sprintf(`{"services": [{
		"name": "personal-site", "environment": "production",
		"repository": "spicyhek/homelab-hosted-personal-site", "workflow": ".github/workflows/release.yml",
		"branch": "main", "argocd_application": "personal-site", "artifact": %q,
		"workload": {"namespace": "default", "deployment": "personal-site"},
		"verify": {"url": %q, "max_latency_ms": 2000}
	}]}`, image, verifyURL)))
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, ctx: ctx, store: store, projector: releases.NewProjector(store, svcs, nil), svcs: svcs}
}

func (e *env) event(provider, typ string, s int, keys events.Keys, payload any) {
	e.t.Helper()
	data, _ := json.Marshal(payload)
	e.n++
	ev := &events.Event{Provider: provider, DeliveryID: fmt.Sprintf("%s/%d", typ, e.n), Type: typ, Attempt: 1,
		OccurredAt: at(s), ReceivedAt: at(s), BodySHA256: "x", Keys: keys, Payload: data}
	if _, err := e.store.InsertEvent(e.ctx, ev); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.projector.ProjectEvent(e.ctx, *ev); err != nil {
		e.t.Fatal(err)
	}
}

// rolledOut stores a release whose Rollout succeeded at t=114.
func (e *env) rolledOut() string {
	id := "personal-site/aaaaaaa/production/57"
	keys := events.Keys{ReleaseID: id, RunID: 57}
	e.event(events.ProviderCI, events.TypeReleasePublished, 0, keys, events.ReleasePublished{
		ReleaseID: id, Service: "personal-site", Environment: "production", Artifact: image,
		ArtifactDigest: digest, WorkflowRunID: 57, RunAttempt: 1, OccurredAt: at(0)})
	keys.Revision = gitops
	e.event(events.ProviderCI, events.TypeGitOpsUpdated, 5, keys, events.GitOpsUpdated{
		ReleaseID: id, GitOpsRepository: "spicyhek/homelab-infra", GitOpsSHA: gitops, WorkflowRunID: 57,
		RunAttempt: 1, OccurredAt: at(5)})
	argo := events.Keys{Revision: gitops}
	started := at(100)
	e.event(events.ProviderArgoCD, events.TypeArgoCD, 104, argo, events.ArgoCDNotification{
		Trigger: events.TriggerSyncSucceeded, Application: "personal-site", Revision: gitops,
		StartedAt: &started, FinishedAt: func() *time.Time { t := at(104); return &t }()})

	w := events.Keys{Workload: "default/personal-site"}
	e.event(events.ProviderKubernetes, events.TypeReplicaSet, 101, w, events.ReplicaSet{
		Namespace: "default", Name: "personal-site-abc", UID: "rs", Deployment: "personal-site",
		Revision: "2", PodTemplateHash: "abc", CreatedAt: at(101), Replicas: 1})
	ready := at(110)
	e.event(events.ProviderKubernetes, events.TypePod, 110, w, events.Pod{
		Namespace: "default", Name: "personal-site-abc-x", UID: "pod", PodTemplateHash: "abc",
		CreatedAt: at(101), Phase: "Running", Ready: true, ReadyChangedAt: &ready,
		Containers: []events.ContainerStatus{{Name: "web", Image: image + "@" + digest, ImageDigest: digest,
			Ready: true, State: "running"}}})
	done := at(114)
	e.event(events.ProviderKubernetes, events.TypeDeployment, 115, w, events.Deployment{
		Namespace: "default", Name: "personal-site", UID: "dep", Revision: "2", Generation: 3,
		ObservedGeneration: 3, Replicas: 1, StatusReplicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1,
		AvailableReplicas: 1, Conditions: []events.Condition{{Type: "Progressing", Status: "True",
			Reason: "NewReplicaSetAvailable", LastUpdateTime: &done}}})
	return id
}

func (e *env) stage(id, key string) releases.Stage {
	e.t.Helper()
	r, err := e.store.GetRelease(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, st := range r.Stages {
		if st.Key == key {
			return st
		}
	}
	e.t.Fatalf("no %s stage", key)
	return releases.Stage{}
}

func (e *env) verifier(c Checker, now *time.Time) *Verifier {
	return New(Options{Store: e.store, Projector: e.projector, Services: e.svcs, Checker: c, Logger: discard,
		Now: func() time.Time { return *now }})
}

func TestVerifierChecksUntilVerified(t *testing.T) {
	srv, c := newServer(t, &response{status: 200, release: "personal-site/aaaaaaa/production/57", body: "ok\n"})
	e := newEnv(t, srv.URL+"/healthz")
	id := e.rolledOut()
	if st := e.stage(id, "rollout"); st.State != releases.StateSucceeded {
		t.Fatalf("rollout %s (%s)", st.State, st.FailureReason)
	}

	now := at(120)
	v := e.verifier(c, &now)
	step := func() {
		t.Helper()
		if err := v.Step(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	step()
	step() // not due again yet
	if st := e.stage(id, "verify"); st.State != releases.StateRunning || st.Summary != "1 of 5 consecutive checks passed" {
		t.Fatalf("after one check: %s %q", st.State, st.Summary)
	}
	for range 4 {
		now = now.Add(30 * time.Second)
		step()
	}
	st := e.stage(id, "verify")
	if st.State != releases.StateSucceeded {
		t.Fatalf("verify %s (%s)", st.State, st.FailureReason)
	}
	r, _ := e.store.GetRelease(e.ctx, id)
	// The test release has no CI events, so only Verify's own fields count.
	if r.VerifiedAt == nil || !r.VerifiedAt.Equal(*st.CompletedAt) {
		t.Errorf("verified_at %v, verify completed %v", r.VerifiedAt, st.CompletedAt)
	}
	if total, passed, err := e.store.CountSamples(e.ctx); err != nil || total != 5 || passed != 5 {
		t.Errorf("samples %d (%d passed), want 5: %v", total, passed, err)
	}

	// Settled: nothing more to do, and a rebuild gives the same answer.
	now = now.Add(time.Hour)
	step()
	if cands, _ := e.store.VerifyCandidates(e.ctx); len(cands) != 0 {
		t.Errorf("candidates after verification: %+v", cands)
	}
	if _, err := e.projector.RebuildAll(e.ctx); err != nil {
		t.Fatal(err)
	}
	if st := e.stage(id, "verify"); st.State != releases.StateSucceeded {
		t.Errorf("after rebuild: %s", st.State)
	}
}

func TestVerifierClosesAWindowThatPassedUnchecked(t *testing.T) {
	e := newEnv(t, "https://example.test/healthz")
	id := e.rolledOut()
	now := at(114).Add(11 * time.Minute) // mybuilds was down for the window
	v := e.verifier(Checker{}, &now)
	if err := v.Step(e.ctx); err != nil {
		t.Fatal(err)
	}
	st := e.stage(id, "verify")
	if st.State != releases.StateUnknown || !strings.Contains(st.FailureReason, "No checks ran") {
		t.Errorf("verify %s (%s)", st.State, st.FailureReason)
	}
	if !st.CompletedAt.Equal(at(114).Add(10 * time.Minute)) {
		t.Errorf("window closed at %v, want the deadline", st.CompletedAt)
	}
}

// fakeStore records what the verifier stores for given candidates.
type fakeStore struct {
	cands  []storage.VerifyCandidate
	idle   []storage.IdleCandidate
	stored []events.Event
}

func (f *fakeStore) InsertEvent(_ context.Context, e *events.Event) (bool, error) {
	f.stored = append(f.stored, *e)
	return true, nil
}

func (f *fakeStore) VerifyCandidates(context.Context) ([]storage.VerifyCandidate, error) {
	return f.cands, nil
}

func (f *fakeStore) IdleConfigReleases(context.Context) ([]storage.IdleCandidate, error) {
	return f.idle, nil
}

type nopProjector struct{}

func (nopProjector) ProjectEvent(context.Context, events.Event) ([]releases.Outcome, error) {
	return nil, nil
}

func TestVerifierEndsOlderReleases(t *testing.T) {
	srv, c := newServer(t, &response{status: 200, release: "personal-site/bbbbbbb/production/58"})
	e := newEnv(t, srv.URL+"/healthz")
	fs := &fakeStore{cands: []storage.VerifyCandidate{
		{ReleaseID: "personal-site/aaaaaaa/production/57", Service: "personal-site", RolloutCompletedAt: at(0), Expected: "personal-site/aaaaaaa/production/57"},
		{ReleaseID: "personal-site/bbbbbbb/production/58", Service: "personal-site", RolloutCompletedAt: at(200), Expected: "personal-site/bbbbbbb/production/58"},
	}}
	now := at(210)
	v := New(Options{Store: fs, Projector: nopProjector{}, Services: e.svcs, Checker: c, Logger: discard,
		Now: func() time.Time { return now }})
	if err := v.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.stored) != 2 {
		t.Fatalf("stored %d events, want an end and a check", len(fs.stored))
	}
	var end events.VerifyEnded
	json.Unmarshal(fs.stored[0].Payload, &end)
	if fs.stored[0].Keys.ReleaseID != "personal-site/aaaaaaa/production/57" || end.Reason != events.EndSuperseded {
		t.Errorf("first event %s %+v", fs.stored[0].Type, end)
	}
	var check events.VerifyCheck
	json.Unmarshal(fs.stored[1].Payload, &check)
	if check.ReleaseID != "personal-site/bbbbbbb/production/58" || check.Result != events.CheckPass {
		t.Errorf("check %+v", check)
	}
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestVerifierChecksAConfigReleaseAgainstItsPods(t *testing.T) {
	served := "personal-site/aaaaaaa/production/57"
	cfg := "personal-site/config-ccccccc/production"
	srv, c := newServer(t, &response{status: 200, release: served})
	e := newEnv(t, srv.URL+"/healthz")
	fs := &fakeStore{cands: []storage.VerifyCandidate{
		{ReleaseID: cfg, Service: "personal-site", RolloutCompletedAt: at(0), Expected: served},
	}}
	now := at(10)
	v := New(Options{Store: fs, Projector: nopProjector{}, Services: e.svcs, Checker: c, Logger: discard,
		Now: func() time.Time { return now }})
	if err := v.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs.stored) != 1 || fs.stored[0].Keys.ReleaseID != cfg {
		t.Fatalf("stored %+v", fs.stored)
	}
	var check events.VerifyCheck
	json.Unmarshal(fs.stored[0].Payload, &check)
	if check.Result != events.CheckPass || check.ReleaseID != cfg || check.ExpectedReleaseID != served {
		t.Errorf("check %+v", check)
	}
}

func TestVerifierRecordsSyncsThatReplacedNoPods(t *testing.T) {
	cfg := "personal-site/config-ccccccc/production"
	idle := []storage.IdleCandidate{{ReleaseID: cfg, Service: "personal-site",
		ReconcileStartedAt: at(100), ReconcileDoneAt: at(103)}}
	cases := []struct {
		name     string
		synced   *time.Time
		now      time.Time
		recorded bool
	}{
		{"watch running throughout", ptrTime(at(0)), at(163), true},
		{"within the grace period", ptrTime(at(0)), at(162), false},
		{"watch started after the sync", ptrTime(at(101)), at(500), false},
		{"no watch", nil, at(500), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{idle: idle}
			o := Options{Store: fs, Projector: nopProjector{}, Logger: discard, Now: func() time.Time { return tc.now }}
			if tc.synced != nil {
				o.WatchSyncedAt = func() (time.Time, bool) { return *tc.synced, true }
			}
			if err := New(o).Step(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := len(fs.stored) == 1; got != tc.recorded {
				t.Fatalf("recorded %t (%d events), want %t", got, len(fs.stored), tc.recorded)
			}
			if tc.recorded {
				ev := fs.stored[0]
				if ev.Type != events.TypeNoRollout || ev.Keys.ReleaseID != cfg || !ev.OccurredAt.Equal(at(163)) {
					t.Errorf("event %s %s at %v", ev.Type, ev.Keys.ReleaseID, ev.OccurredAt)
				}
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
