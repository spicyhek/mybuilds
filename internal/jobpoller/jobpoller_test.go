package jobpoller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
)

const (
	release35 = "mynids/09f36c4/production/35"
	token     = "test-token"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	t         *testing.T
	ctx       context.Context
	store     *storage.Store
	projector *releases.Projector
	// webhooks are release 35's recorded GitHub events in ID order.
	webhooks []events.Event
}

func newEnv(t *testing.T) *env {
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
	f, err := os.Open("../releases/testdata/releases/mynids-35.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rec, err := events.ReadRecording(f)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, store: store, projector: releases.NewProjector(store, svcs, nil)}
	for _, r := range rec.Events {
		if ev := r.Event(); ev.Provider == events.ProviderGitHub {
			e.webhooks = append(e.webhooks, ev)
		}
	}
	return e
}

// deliver stores the recorded webhooks received up to t, as the ingester
// would have.
func (e *env) deliver(t time.Time) {
	e.t.Helper()
	for _, ev := range e.webhooks {
		if ev.ReceivedAt.After(t) {
			continue
		}
		ev.ID = 0
		inserted, err := e.store.InsertEvent(e.ctx, &ev)
		if err != nil {
			e.t.Fatal(err)
		}
		if inserted {
			if _, err := e.projector.ProjectEvent(e.ctx, ev); err != nil {
				e.t.Fatal(err)
			}
		}
	}
}

// job is the recorded build job of release 35 with the given status.
func (e *env) job(status string) events.WorkflowJob {
	e.t.Helper()
	for _, ev := range e.webhooks {
		var j events.WorkflowJob
		if ev.Type == events.TypeWorkflowJob && json.Unmarshal(ev.Payload, &j) == nil &&
			j.JobName == "build" && j.Status == status {
			return j
		}
	}
	e.t.Fatalf("no %s build job recorded", status)
	return events.WorkflowJob{}
}

func (e *env) stage(key string) releases.Stage {
	e.t.Helper()
	r, err := e.store.GetRelease(e.ctx, release35)
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

func stepState(st releases.Stage, name string) releases.State {
	for _, s := range st.Steps {
		if s.Name == name {
			return s.State
		}
	}
	return ""
}

func (e *env) count() int {
	e.t.Helper()
	evs, err := e.store.ReleaseEvents(e.ctx, release35)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(evs)
}

// fakeGitHub serves one job as GitHub's REST API does, with ETags.
type fakeGitHub struct {
	mu       sync.Mutex
	job      events.WorkflowJob
	status   int
	header   http.Header
	requests int
	auth     []string
}

func (g *fakeGitHub) set(job events.WorkflowJob) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.job = job
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests++
	g.auth = append(g.auth, r.Header.Get("Authorization"))
	for k, v := range g.header {
		w.Header()[k] = v
	}
	if g.status != 0 {
		w.WriteHeader(g.status)
		return
	}
	if r.URL.Path != fmt.Sprintf("/repos/%s/actions/jobs/%d", g.job.Repository, g.job.JobID) {
		http.NotFound(w, r)
		return
	}
	body := apiJob(g.job)
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// apiJob is a job as the REST API returns it, with the fields mybuilds
// drops included.
func apiJob(j events.WorkflowJob) []byte {
	type step struct {
		Name        string     `json:"name"`
		Number      int        `json:"number"`
		Status      string     `json:"status"`
		Conclusion  *string    `json:"conclusion"`
		StartedAt   *time.Time `json:"started_at"`
		CompletedAt *time.Time `json:"completed_at"`
	}
	str := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	var steps []step
	for _, s := range j.Steps {
		steps = append(steps, step{s.Name, s.Number, s.Status, str(s.Conclusion), s.StartedAt, s.CompletedAt})
	}
	body, _ := json.Marshal(map[string]any{
		"id": j.JobID, "run_id": j.RunID, "run_attempt": j.RunAttempt, "name": j.JobName,
		"workflow_name": j.WorkflowName, "head_sha": j.HeadSHA, "status": j.Status,
		"conclusion": str(j.Conclusion), "created_at": j.CreatedAt, "started_at": j.StartedAt,
		"completed_at": j.CompletedAt, "labels": j.Labels, "steps": steps,
		"runner_name": "homelab-runner-1", "html_url": "https://github.com/x/y/actions/runs/1/job/2",
	})
	return body
}

// building is the build job as it stood at 05:01, building the image: the
// recorded in_progress job with its steps advanced.
func building(j events.WorkflowJob) events.WorkflowJob {
	at := time.Date(2026, 11, 19, 5, 0, 0, 0, time.UTC)
	steps := append([]events.JobStep(nil), j.Steps...)
	for i := range steps {
		s := &steps[i]
		switch s.Name {
		case "Set up Node for dashboard tests", "Run regression tests before publishing", "Set up QEMU",
			"Set up Docker Buildx", "Log in to GitHub Container Registry":
			if s.StartedAt == nil {
				s.StartedAt = &at
			}
			done := at.Add(10 * time.Second)
			s.Status, s.Conclusion, s.CompletedAt = "completed", "success", &done
			at = done
		case "Build image":
			s.Status, s.StartedAt = "in_progress", &at
		}
	}
	j.Steps = steps
	return j
}

func (e *env) poller(srv *httptest.Server, now *time.Time) *Poller {
	return New(Options{Store: e.store, Projector: e.projector, Logger: discard, Token: token,
		BaseURL: srv.URL, Client: srv.Client(), Now: func() time.Time { return *now }})
}

func TestPollerFollowsTheStepsOfARunningJob(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 19, 5, 1, 0, 0, time.UTC)
	e.deliver(now)
	if got := stepState(e.stage("build"), "Set up Node for dashboard tests"); got != releases.StateRunning {
		t.Fatalf("before polling, the webhook's step is %q, want running", got)
	}

	gh := &fakeGitHub{}
	gh.set(building(e.job("in_progress")))
	srv := httptest.NewServer(gh)
	defer srv.Close()
	p := e.poller(srv, &now)
	step := func() {
		t.Helper()
		if err := p.Step(e.ctx); err != nil {
			t.Fatal(err)
		}
	}

	step()
	build := e.stage("build")
	if got := stepState(build, "Build image"); got != releases.StateRunning {
		t.Errorf("Build image %q, want running", got)
	}
	if got := stepState(build, "Run regression tests before publishing"); got != releases.StateSucceeded {
		t.Errorf("regression tests %q, want succeeded", got)
	}
	if gh.auth[0] != "Bearer "+token {
		t.Errorf("authorization %q", gh.auth[0])
	}

	// Not due yet, then due but unchanged: a 304 stores nothing.
	before := e.count()
	step()
	now = now.Add(DefaultInterval)
	step()
	if gh.requests != 2 || e.count() != before {
		t.Errorf("%d requests and %d new events, want 2 and none", gh.requests, e.count()-before)
	}

	// A redelivered in_progress webhook, such as from the buffer, does not
	// take the release back.
	for _, ev := range e.webhooks {
		var j events.WorkflowJob
		if ev.Type == events.TypeWorkflowJob && json.Unmarshal(ev.Payload, &j) == nil &&
			j.JobName == "build" && j.Status == "in_progress" {
			ev.ID, ev.DeliveryID, ev.ReceivedAt = 0, ev.DeliveryID+"-late", now
			if _, err := e.store.InsertEvent(e.ctx, &ev); err != nil {
				t.Fatal(err)
			}
			if _, err := e.projector.ProjectEvent(e.ctx, ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := stepState(e.stage("build"), "Build image"); got != releases.StateRunning {
		t.Errorf("after a late webhook, Build image %q", got)
	}

	// The job finishes before its webhook arrives: the poll records it, and
	// the job is no longer polled.
	gh.set(e.job("completed"))
	now = now.Add(DefaultInterval)
	step()
	if got := e.stage("build").State; got != releases.StateSucceeded {
		t.Errorf("build %s after the completed poll, want succeeded", got)
	}
	now = now.Add(DefaultInterval)
	step()
	if gh.requests != 3 {
		t.Errorf("%d requests, want no poll of a completed job", gh.requests)
	}

	// The timeline replays the polled steps in order.
	evs, err := e.store.ReleaseEvents(e.ctx, release35)
	if err != nil {
		t.Fatal(err)
	}
	var polled int
	for _, ev := range evs {
		var j events.WorkflowJob
		if ev.Type == events.TypeWorkflowJob && json.Unmarshal(ev.Payload, &j) == nil && j.Action == "polled" {
			polled++
			if ev.OccurredAt.Before(*j.StartedAt) {
				t.Errorf("polled event at %v, before the job started", ev.OccurredAt)
			}
		}
	}
	if polled != 2 {
		t.Errorf("%d polled events, want 2", polled)
	}
}

func TestPollerSkipsRunsThatAreNotReleases(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 19, 5, 1, 0, 0, time.UTC)
	for _, ev := range e.webhooks {
		if ev.Type == events.TypeWorkflowJob && !ev.ReceivedAt.After(now) {
			ev.ID = 0
			if _, err := e.store.InsertEvent(e.ctx, &ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	jobs, err := e.store.RunningJobs(e.ctx, now.Add(-DefaultMaxAge))
	if err != nil || len(jobs) != 0 {
		t.Errorf("running jobs of a run with no release: %+v %v", jobs, err)
	}
	e.deliver(now)
	if jobs, _ := e.store.RunningJobs(e.ctx, now.Add(-DefaultMaxAge)); len(jobs) != 1 || jobs[0].JobName != "build" {
		t.Errorf("running jobs %+v, want the build job", jobs)
	}
	if jobs, _ := e.store.RunningJobs(e.ctx, now.Add(DefaultMaxAge)); len(jobs) != 0 {
		t.Errorf("jobs older than the window: %+v", jobs)
	}
}

func TestPollerPausesWhenRateLimited(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 19, 5, 1, 0, 0, time.UTC)
	e.deliver(now)
	reset := now.Add(20 * time.Minute)
	gh := &fakeGitHub{status: http.StatusForbidden, header: http.Header{
		"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)}}}
	gh.set(e.job("in_progress"))
	srv := httptest.NewServer(gh)
	defer srv.Close()
	p := e.poller(srv, &now)
	for range 3 {
		if err := p.Step(e.ctx); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if gh.requests != 1 {
		t.Errorf("%d requests while rate limited, want 1", gh.requests)
	}
	gh.mu.Lock()
	gh.status, gh.header = 0, nil
	gh.mu.Unlock()
	now = reset
	if err := p.Step(e.ctx); err != nil {
		t.Fatal(err)
	}
	if gh.requests != 2 {
		t.Errorf("%d requests after the reset, want 2", gh.requests)
	}
}

func TestPollerStopsPollingADeletedJob(t *testing.T) {
	e := newEnv(t)
	now := time.Date(2026, 11, 19, 5, 1, 0, 0, time.UTC)
	e.deliver(now)
	gh := &fakeGitHub{status: http.StatusNotFound}
	gh.set(e.job("in_progress"))
	srv := httptest.NewServer(gh)
	defer srv.Close()
	p := e.poller(srv, &now)
	for range 3 {
		if err := p.Step(e.ctx); err != nil {
			t.Fatal(err)
		}
		now = now.Add(DefaultInterval)
	}
	if gh.requests != 1 {
		t.Errorf("%d requests for a deleted job, want 1", gh.requests)
	}
}
