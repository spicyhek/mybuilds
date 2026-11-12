package releases

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/services"
)

const (
	testRelease = "personal-site/0123456/production/57"
	testSHA     = "0123456789abcdef0123456789abcdef01234567"
	testGitOps  = "89abcdef0123456789abcdef0123456789abcdef"
	testRunID   = 9001
)

var t0 = time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return t0.Add(time.Duration(seconds) * time.Second) }

func tp(seconds int) *time.Time { t := at(seconds); return &t }

func personalSite(t *testing.T) services.Service {
	t.Helper()
	m, err := services.Load("")
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := m.ByName("personal-site")
	if !ok {
		t.Fatal("personal-site missing from the built-in service map")
	}
	return svc
}

type builder struct {
	t      *testing.T
	nextID int64
	evs    []events.Event
}

func (b *builder) add(typ string, occurred time.Time, attempt int, keys events.Keys, payload any) {
	b.t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		b.t.Fatal(err)
	}
	b.nextID++
	b.evs = append(b.evs, events.Event{
		ID: b.nextID, Type: typ, Attempt: attempt, OccurredAt: occurred, Keys: keys, Payload: data,
	})
}

func (b *builder) run(attempt int, status, conclusion string, updated int) {
	b.add(events.TypeWorkflowRun, at(updated), attempt, events.Keys{ReleaseID: testRelease, RunID: testRunID},
		events.WorkflowRun{RunID: testRunID, RunNumber: 57, RunAttempt: attempt, HeadSHA: testSHA, HeadBranch: "main",
			Event: "push", Status: status, Conclusion: conclusion, CreatedAt: at(0), RunStartedAt: at(0), UpdatedAt: at(updated),
			Repository: "spicyhek/homelab-hosted-personal-site"})
}

// job adds a workflow_job delivery. Each step is name=conclusion; an empty
// conclusion means the step is still running.
func (b *builder) job(attempt int, name, status, conclusion string, start int, steps ...[2]string) {
	var js []events.JobStep
	for i, s := range steps {
		step := events.JobStep{Number: i + 1, Name: s[0], Status: "completed", Conclusion: s[1]}
		if s[1] == "" {
			step.Status = "in_progress"
		}
		if s[1] != "skipped" {
			step.StartedAt = tp(start + i)
			if s[1] != "" {
				step.CompletedAt = tp(start + i + 1)
			}
		}
		js = append(js, step)
	}
	occurred := at(start)
	var completed *time.Time
	if status == "completed" {
		completed = tp(start + len(steps))
		occurred = *completed
	}
	b.add(events.TypeWorkflowJob, occurred, attempt, events.Keys{RunID: testRunID},
		events.WorkflowJob{RunID: testRunID, RunAttempt: attempt, JobID: int64(attempt*100 + len(name)), JobName: name,
			Status: status, Conclusion: conclusion, StartedAt: tp(start), CompletedAt: completed, Steps: js})
}

func (b *builder) published(attempt, occurred int) {
	b.add(events.TypeReleasePublished, at(occurred), attempt, events.Keys{ReleaseID: testRelease, RunID: testRunID},
		events.ReleasePublished{ReleaseID: testRelease, Service: "personal-site", Environment: "production",
			SourceSHA: testSHA, Artifact: "ghcr.io/spicyhek/homelab-hosted-personal-site",
			ArtifactDigest: "sha256:" + testSHA + "aaaaaaaaaaaaaaaaaaaaaaaa", WorkflowRunID: testRunID,
			WorkflowRunNumber: 57, RunAttempt: attempt, OccurredAt: at(occurred)})
}

func (b *builder) gitopsUpdated(attempt, occurred int) {
	b.add(events.TypeGitOpsUpdated, at(occurred), attempt,
		events.Keys{ReleaseID: testRelease, RunID: testRunID, Revision: testGitOps},
		events.GitOpsUpdated{ReleaseID: testRelease, GitOpsRepository: "spicyhek/homelab-infra", GitOpsSHA: testGitOps,
			WorkflowRunID: testRunID, RunAttempt: attempt, OccurredAt: at(occurred)})
}

func (b *builder) superseded(occurred int) {
	b.add(events.TypeGitOpsSuperseded, at(occurred), 1, events.Keys{ReleaseID: testRelease, RunID: testRunID},
		events.GitOpsSuperseded{ReleaseID: testRelease, WorkflowRunID: testRunID, RunAttempt: 1, OccurredAt: at(occurred)})
}

func (b *builder) argo(trigger string, started int, finished int) {
	n := events.ArgoCDNotification{Trigger: trigger, Application: "personal-site", Revision: testGitOps, StartedAt: tp(started)}
	occurred := at(started)
	if finished > 0 {
		n.FinishedAt = tp(finished)
		occurred = at(finished)
	}
	b.add(events.TypeArgoCD, occurred, 1, events.Keys{Revision: testGitOps}, n)
}

var buildSteps = [][2]string{{"Set up job", "success"}, {"Build image", "success"}, {"Smoke test", "success"},
	{"Push image", "success"}, {"Resolve digest", "success"}}

func project(t *testing.T, b *builder) Release {
	t.Helper()
	p, err := Project(testRelease, b.evs, personalSite(t))
	if err != nil {
		t.Fatal(err)
	}
	return p.Release
}

func states(r Release) []State {
	var out []State
	for _, st := range r.Stages {
		out = append(out, st.State)
	}
	return out
}

func expect(t *testing.T, r Release, status State, want ...State) {
	t.Helper()
	if got := states(r); !slices.Equal(got, want) || r.Status != status {
		t.Errorf("status %s stages %v, want status %s stages %v", r.Status, got, status, want)
		for _, st := range r.Stages {
			if st.FailureReason != "" {
				t.Logf("  %s: %s", st.Key, st.FailureReason)
			}
		}
	}
}

const (
	P = StatePending
	R = StateRunning
	S = StateSucceeded
	F = StateFailed
	K = StateSkipped
	U = StateUnknown
)

func TestBuildStepFailureStopsThePipeline(t *testing.T) {
	b := &builder{t: t}
	b.run(1, "in_progress", "", 1)
	b.job(1, "build", "completed", "failure", 2,
		[2]string{"Set up job", "success"}, [2]string{"Build image", "success"}, [2]string{"Smoke test", "failure"},
		[2]string{"Push image", "skipped"}, [2]string{"Resolve digest", "skipped"})
	b.run(1, "completed", "failure", 20)

	r := project(t, b)
	expect(t, r, F, S, F, K, K, K, K, K)
	if got := r.Stages[1].FailureReason; got != `Step "Smoke test" failed` {
		t.Errorf("build failure reason = %q", got)
	}
}

func TestSupersededReleaseIsSkippedNotFailed(t *testing.T) {
	b := &builder{t: t}
	b.run(1, "in_progress", "", 1)
	b.job(1, "build", "completed", "success", 2, buildSteps...)
	b.published(1, 7)
	b.job(1, "bump-gitops", "completed", "success", 10,
		[2]string{"Check for newer commit", "success"}, [2]string{"Pin image digest", "skipped"}, [2]string{"Squash merge", "skipped"})
	b.superseded(11)
	b.run(1, "completed", "success", 20)

	r := project(t, b)
	expect(t, r, K, S, S, S, K, K, K, K)
	if !r.Superseded || r.GitOpsSHA != "" {
		t.Errorf("superseded = %v, gitops_sha = %q", r.Superseded, r.GitOpsSHA)
	}
}

func TestMissingEvidenceIsUnknownNotSuccess(t *testing.T) {
	b := &builder{t: t}
	b.run(1, "in_progress", "", 1)
	b.job(1, "build", "completed", "success", 2, buildSteps...)
	b.published(1, 7)
	b.job(1, "bump-gitops", "completed", "success", 10,
		[2]string{"Pin image digest", "success"}, [2]string{"Squash merge", "success"})
	b.run(1, "completed", "success", 20)

	r := project(t, b)
	expect(t, r, U, S, S, S, U, P, P, P)

	// A job that reported every Publish step green still needs the manifest.
	b2 := &builder{t: t}
	b2.run(1, "in_progress", "", 1)
	b2.job(1, "build", "completed", "success", 2, buildSteps...)
	expect(t, project(t, b2), U, S, S, U, P, P, P, P)
}

func TestSourceWithoutWorkflowRunIsUnknown(t *testing.T) {
	b := &builder{t: t}
	b.published(1, 7)
	expect(t, project(t, b), R, U, S, S, P, P, P, P)
}

func TestReRunOfFailedJobContinuesTheRelease(t *testing.T) {
	b := &builder{t: t}
	b.run(1, "in_progress", "", 1)
	b.job(1, "build", "completed", "success", 2, buildSteps...)
	b.published(1, 7)
	b.job(1, "bump-gitops", "completed", "failure", 10,
		[2]string{"Pin image digest", "success"}, [2]string{"Squash merge", "failure"})
	b.run(1, "completed", "failure", 20)
	expect(t, project(t, b), F, S, S, S, F, K, K, K)

	// "Re-run failed jobs": attempt 2 runs only bump-gitops.
	b.run(2, "in_progress", "", 100)
	b.job(2, "bump-gitops", "in_progress", "", 101, [2]string{"Pin image digest", ""})
	r := project(t, b)
	expect(t, r, R, S, S, S, R, P, P, P)
	if r.Stages[1].Attempt != 1 || r.Stages[3].Attempt != 2 {
		t.Errorf("attempts: build %d gitops %d, want 1 and 2", r.Stages[1].Attempt, r.Stages[3].Attempt)
	}

	b.gitopsUpdated(2, 105)
	b.argo(events.TriggerSyncSucceeded, 120, 125)
	expect(t, project(t, b), R, S, S, S, S, S, P, P)
}

func TestReconcileUsesProviderTimesNotArrival(t *testing.T) {
	base := func() *builder {
		b := &builder{t: t}
		b.run(1, "completed", "success", 20)
		b.job(1, "build", "completed", "success", 2, buildSteps...)
		b.published(1, 7)
		b.gitopsUpdated(1, 15)
		return b
	}

	// A failed sync retried successfully ends succeeded, and a late
	// sync-running delivery cannot reopen it.
	b := base()
	b.argo(events.TriggerSyncFailed, 30, 35)
	b.argo(events.TriggerSyncSucceeded, 40, 45)
	b.argo(events.TriggerSyncRunning, 40, 0)
	r := project(t, b)
	expect(t, r, R, S, S, S, S, S, P, P)

	reversed := base()
	reversed.argo(events.TriggerSyncRunning, 40, 0)
	reversed.argo(events.TriggerSyncSucceeded, 40, 45)
	reversed.argo(events.TriggerSyncFailed, 30, 35)
	r2 := project(t, reversed)
	if a, b := mustJSON(t, r.Stages[4]), mustJSON(t, r2.Stages[4]); a != b {
		t.Errorf("reconcile depends on arrival order: %s vs %s", a, b)
	}
	if st := r.Stages[4]; !st.CompletedAt.Equal(at(45)) || !st.StartedAt.Equal(at(30)) {
		t.Errorf("reconcile times %v-%v", st.StartedAt, st.CompletedAt)
	}

	// Health degradation is Rollout evidence; it does not touch Reconcile.
	d := base()
	d.argo(events.TriggerHealthDegraded, 50, 0)
	expect(t, project(t, d), R, S, S, S, S, P, P, P)
}

func TestParseReleaseID(t *testing.T) {
	p, err := ParseReleaseID("personal-site/ce61154/production/11")
	if err != nil || p.Service != "personal-site" || p.ShortSHA != "ce61154" || p.Environment != "production" || p.RunNumber != 11 {
		t.Errorf("got %+v, %v", p, err)
	}
	for _, bad := range []string{"", "personal-site/ce61154/production", "Personal/ce61154/production/1",
		"personal-site/CE61154/production/1", "personal-site/ce61154/production/0", "a/ce61154/b/1/extra",
		"personal-site/config/ce61154/production"} {
		if _, err := ParseReleaseID(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if got := ApplicationReleaseID("personal-site", testSHA, "production", 57); got != testRelease {
		t.Errorf("ApplicationReleaseID = %q", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
