package releases

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/services"
)

// Projection is the projected state of one release, ready to be stored.
type Projection struct {
	// Release has its stages filled in; the stages' Steps are left empty.
	Release Release
	// Steps holds the steps of every attempt of every stage.
	Steps []Step
	// EventIDs are the events that belong to this release.
	EventIDs []int64
}

// Project reduces a release's events to its current state. It is a pure
// function of its input: events are ordered by provider timestamp and then by
// ID, never by arrival, so the same events always give the same result and
// replaying duplicates changes nothing (plan sections 5.4 and 13).
//
// It covers the application template from Source through Reconcile. Rollout
// and Verify stay pending until the runtime watch and the verifier exist.
func Project(releaseID string, evs []events.Event, svc services.Service) (Projection, error) {
	parts, err := ParseReleaseID(releaseID)
	if err != nil {
		return Projection{}, err
	}

	sorted := append([]events.Event(nil), evs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].OccurredAt.Equal(sorted[j].OccurredAt) {
			return sorted[i].OccurredAt.Before(sorted[j].OccurredAt)
		}
		return sorted[i].ID < sorted[j].ID
	})

	f := &fold{
		svc:  svc,
		runs: map[int]*runObs{},
		jobs: map[jobKey]*jobObs{},
	}
	f.release = Release{
		ReleaseID:   releaseID,
		Service:     parts.Service,
		Environment: parts.Environment,
		Kind:        KindApplication,
		Template:    TemplateApplication,
	}
	for _, e := range sorted {
		if err := f.add(e); err != nil {
			return Projection{}, fmt.Errorf("event %d (%s): %w", e.ID, e.Type, err)
		}
	}
	return f.result(), nil
}

type runObs struct {
	run     events.WorkflowRun
	attempt int
	eventID int64
}

type jobKey struct {
	attempt int
	name    string
}

type jobObs struct {
	job     events.WorkflowJob
	eventID int64
}

type ciObs[T any] struct {
	payload T
	attempt int
	eventID int64
}

type argoObs struct {
	n       events.ArgoCDNotification
	at      time.Time
	eventID int64
}

type fold struct {
	svc     services.Service
	release Release

	eventIDs []int64
	firstAt  time.Time

	runs       map[int]*runObs
	jobs       map[jobKey]*jobObs
	published  *ciObs[events.ReleasePublished]
	gitops     *ciObs[events.GitOpsUpdated]
	superseded *ciObs[events.GitOpsSuperseded]
	argo       []argoObs
}

// statusRank orders GitHub run and job statuses so that a late "in_progress"
// delivery never overwrites "completed".
func statusRank(status string) int {
	switch status {
	case "completed":
		return 2
	case "in_progress":
		return 1
	}
	return 0
}

func (f *fold) add(e events.Event) error {
	switch e.Type {
	case events.TypeWorkflowRun:
		var p events.WorkflowRun
		if err := e.Decode(&p); err != nil {
			return err
		}
		attempt := max(p.RunAttempt, 1)
		if cur, ok := f.runs[attempt]; !ok || statusRank(p.Status) >= statusRank(cur.run.Status) {
			f.runs[attempt] = &runObs{run: p, attempt: attempt, eventID: e.ID}
		}

	case events.TypeWorkflowJob:
		var p events.WorkflowJob
		if err := e.Decode(&p); err != nil {
			return err
		}
		k := jobKey{attempt: max(p.RunAttempt, 1), name: p.JobName}
		if cur, ok := f.jobs[k]; !ok || statusRank(p.Status) >= statusRank(cur.job.Status) {
			f.jobs[k] = &jobObs{job: p, eventID: e.ID}
		}

	case events.TypeReleasePublished:
		var p events.ReleasePublished
		if err := e.Decode(&p); err != nil {
			return err
		}
		if a := max(p.RunAttempt, 1); f.published == nil || a >= f.published.attempt {
			f.published = &ciObs[events.ReleasePublished]{payload: p, attempt: a, eventID: e.ID}
		}

	case events.TypeGitOpsUpdated:
		var p events.GitOpsUpdated
		if err := e.Decode(&p); err != nil {
			return err
		}
		if a := max(p.RunAttempt, 1); f.gitops == nil || a >= f.gitops.attempt {
			f.gitops = &ciObs[events.GitOpsUpdated]{payload: p, attempt: a, eventID: e.ID}
		}

	case events.TypeGitOpsSuperseded:
		var p events.GitOpsSuperseded
		if err := e.Decode(&p); err != nil {
			return err
		}
		if a := max(p.RunAttempt, 1); f.superseded == nil || a >= f.superseded.attempt {
			f.superseded = &ciObs[events.GitOpsSuperseded]{payload: p, attempt: a, eventID: e.ID}
		}

	case events.TypeArgoCD:
		var p events.ArgoCDNotification
		if err := e.Decode(&p); err != nil {
			return err
		}
		// The root app-of-apps syncs the same revision; only the service's
		// own Application is evidence for this release.
		if f.svc.ArgoCDApplication == "" || p.Application != f.svc.ArgoCDApplication {
			return nil
		}
		f.argo = append(f.argo, argoObs{n: p, at: e.OccurredAt, eventID: e.ID})

	default:
		return nil
	}

	f.eventIDs = append(f.eventIDs, e.ID)
	if f.firstAt.IsZero() || e.OccurredAt.Before(f.firstAt) {
		f.firstAt = e.OccurredAt
	}
	return nil
}

// stageEvidence is what the workflow jobs of one attempt say about a stage.
type stageEvidence struct {
	attempt      int
	steps        []Step
	started      *time.Time
	completed    *time.Time
	running      bool
	failed       bool
	failedStep   string
	allSucceeded bool
}

func (f *fold) result() Projection {
	r := f.release
	stages := make(map[string]*Stage, len(ApplicationStages))
	r.Stages = make([]Stage, len(ApplicationStages))
	for i, key := range ApplicationStages {
		r.Stages[i] = Stage{Key: key, Position: i + 1, State: StatePending, Attempt: 1}
		stages[key] = &r.Stages[i]
	}
	var steps []Step

	firstRun, latestRun := f.runBounds()

	// Identity.
	if firstRun != nil {
		r.SourceRepository = firstRun.run.Repository
		r.SourceSHA = firstRun.run.HeadSHA
	}
	if p := f.published; p != nil {
		r.SourceRepository = p.payload.SourceRepository
		r.SourceSHA = p.payload.SourceSHA
		r.Artifact = p.payload.Artifact
		r.ArtifactDigest = p.payload.ArtifactDigest
		r.Drill = p.payload.Drill
		r.DrillScenario = p.payload.DrillScenario
	}
	if g := f.gitops; g != nil {
		r.GitOpsRepository = g.payload.GitOpsRepository
		r.GitOpsSHA = g.payload.GitOpsSHA
	}
	r.CreatedAt = f.firstAt
	if firstRun != nil && !firstRun.run.CreatedAt.IsZero() && firstRun.run.CreatedAt.Before(r.CreatedAt) {
		r.CreatedAt = firstRun.run.CreatedAt
	}

	// Source: the workflow run proves the push triggered the release workflow.
	if firstRun != nil {
		run := firstRun.run
		src := stages["source"]
		src.State = StateSucceeded
		src.StartedAt = timePtr(run.CreatedAt)
		src.CompletedAt = timePtr(run.CreatedAt)
		src.Summary = fmt.Sprintf("Push to %s, %s", run.HeadBranch, short(run.HeadSHA, 7))
		steps = append(steps,
			Step{StageKey: "source", Attempt: 1, Name: "Push to " + run.HeadBranch, State: StateSucceeded,
				StartedAt: timePtr(run.CreatedAt), CompletedAt: timePtr(run.CreatedAt),
				Detail: short(run.HeadSHA, 7), SourceEventID: firstRun.eventID},
			Step{StageKey: "source", Attempt: 1, Name: "Workflow triggered", State: StateSucceeded,
				StartedAt: timePtr(run.RunStartedAt), CompletedAt: timePtr(run.RunStartedAt),
				Detail: fmt.Sprintf("Run #%d", run.RunNumber), SourceEventID: firstRun.eventID},
		)
	}

	evidence := f.jobEvidence()
	latest := func(stage string) *stageEvidence {
		var best *stageEvidence
		for _, e := range evidence[stage] {
			if best == nil || e.attempt > best.attempt {
				best = e
			}
		}
		return best
	}
	for _, key := range []string{"build", "publish", "gitops"} {
		for _, e := range evidence[key] {
			steps = append(steps, e.steps...)
		}
	}

	// Build. Its result can come from its steps (plan section 8.1).
	build := stages["build"]
	be := latest("build")
	if be != nil {
		build.Attempt = be.attempt
		build.StartedAt = be.started
	}
	if build.StartedAt == nil && latestRun != nil {
		build.StartedAt = timePtr(latestRun.run.RunStartedAt)
	}
	switch {
	case f.published != nil:
		build.State = StateSucceeded
		build.CompletedAt = timePtr(f.published.payload.OccurredAt)
		if be != nil && be.allSucceeded && be.completed != nil {
			build.CompletedAt = be.completed
		}
		build.Summary = "Image built"
	case be != nil && be.failed:
		build.State = StateFailed
		build.CompletedAt = be.completed
		build.FailureReason = fmt.Sprintf("Step %q failed", be.failedStep)
	case be != nil && be.allSucceeded:
		build.State = StateSucceeded
		build.CompletedAt = be.completed
		build.Summary = "Image built"
	case be != nil && be.running:
		build.State = StateRunning
	case latestRun != nil && latestRun.run.Status != "completed":
		build.State = StateRunning
	}

	// Publish completes only on the signed release manifest.
	publish := stages["publish"]
	pe := latest("publish")
	if pe != nil {
		publish.Attempt = pe.attempt
		publish.StartedAt = pe.started
	}
	switch {
	case f.published != nil:
		p := f.published
		publish.State = StateSucceeded
		publish.Attempt = p.attempt
		if publish.StartedAt == nil {
			publish.StartedAt = build.CompletedAt
		}
		publish.CompletedAt = timePtr(p.payload.OccurredAt)
		publish.Summary = short(p.payload.ArtifactDigest, 19)
		steps = append(steps, Step{StageKey: "publish", Attempt: p.attempt, Name: "Send release.published",
			State: StateSucceeded, StartedAt: timePtr(p.payload.OccurredAt), CompletedAt: timePtr(p.payload.OccurredAt),
			Detail: short(p.payload.ArtifactDigest, 19), SourceEventID: p.eventID})
	case pe != nil && pe.failed:
		publish.State = StateFailed
		publish.CompletedAt = pe.completed
		publish.FailureReason = fmt.Sprintf("Step %q failed", pe.failedStep)
	case pe != nil && pe.running:
		publish.State = StateRunning
	case pe != nil && pe.allSucceeded:
		publish.State = StateUnknown
		publish.FailureReason = "Publish steps succeeded but no release.published event was received"
	}

	// GitOps completes only on gitops.updated, or is skipped on
	// gitops.superseded.
	gitops := stages["gitops"]
	ge := latest("gitops")
	if ge != nil {
		gitops.Attempt = ge.attempt
		gitops.StartedAt = ge.started
	}
	updated, superseded := f.gitops, f.superseded
	if updated != nil && superseded != nil {
		// Keep only the newest attempt's word; within one attempt the two
		// contradict each other.
		switch {
		case updated.attempt > superseded.attempt:
			superseded = nil
		case superseded.attempt > updated.attempt:
			updated = nil
		}
	}
	switch {
	case updated != nil && superseded != nil:
		gitops.State = StateUnknown
		gitops.FailureReason = "Both gitops.updated and gitops.superseded were received"
	case superseded != nil:
		gitops.State = StateSkipped
		gitops.Attempt = superseded.attempt
		gitops.CompletedAt = timePtr(superseded.payload.OccurredAt)
		gitops.Summary = "Superseded by a newer commit"
		r.Superseded = true
		r.GitOpsRepository, r.GitOpsSHA = "", ""
	case updated != nil:
		gitops.State = StateSucceeded
		gitops.Attempt = updated.attempt
		if gitops.StartedAt == nil {
			gitops.StartedAt = publish.CompletedAt
		}
		gitops.CompletedAt = timePtr(updated.payload.OccurredAt)
		gitops.Summary = fmt.Sprintf("%s@%s", repoName(updated.payload.GitOpsRepository), short(updated.payload.GitOpsSHA, 7))
		steps = append(steps, Step{StageKey: "gitops", Attempt: updated.attempt, Name: "Send gitops.updated",
			State: StateSucceeded, StartedAt: timePtr(updated.payload.OccurredAt), CompletedAt: timePtr(updated.payload.OccurredAt),
			Detail: short(updated.payload.GitOpsSHA, 7), SourceEventID: updated.eventID})
	case ge != nil && ge.failed:
		gitops.State = StateFailed
		gitops.CompletedAt = ge.completed
		gitops.FailureReason = fmt.Sprintf("Step %q failed", ge.failedStep)
	case ge != nil && ge.running:
		gitops.State = StateRunning
	case ge != nil && ge.allSucceeded:
		gitops.State = StateUnknown
		gitops.FailureReason = "GitOps steps succeeded but no gitops.updated or gitops.superseded event was received"
	}

	// The workflow's own conclusion settles CI stages that are still open.
	if latestRun != nil && latestRun.run.Status == "completed" {
		run := latestRun.run
		ciStages := []*Stage{build, publish, gitops}
		if run.Conclusion == "success" {
			for _, st := range ciStages {
				if !st.State.Terminal() && st.State != StateUnknown {
					st.State = StateUnknown
					st.FailureReason = fmt.Sprintf("The workflow succeeded without %s evidence", st.Key)
				}
			}
		} else {
			// The first open stage takes the blame, unless an earlier stage
			// already failed on its own evidence; the rest are skipped.
			blamed := false
			for _, st := range ciStages {
				switch {
				case st.State == StateFailed:
					blamed = true
				case st.State.Terminal():
				case !blamed:
					st.State = StateFailed
					st.CompletedAt = timePtr(run.UpdatedAt)
					st.FailureReason = "Workflow run concluded " + orDefault(run.Conclusion, "without a conclusion")
					blamed = true
				default:
					st.State = StateSkipped
				}
			}
		}
	}

	// Reconcile: Argo CD syncs of the GitOps commit, by the service's own
	// Application. Synced completes Reconcile only.
	reconcile := stages["reconcile"]
	if r.Superseded {
		// Nothing was promoted, so nothing will reconcile, roll out or verify.
		for _, key := range []string{"reconcile", "rollout", "verify"} {
			stages[key].State = StateSkipped
		}
	} else if r.GitOpsSHA != "" {
		steps = append(steps, f.reconcile(reconcile, r.GitOpsSHA)...)
	}

	settle(r.Stages)
	r.Status, r.CompletedAt = releaseStatus(r)
	numberSteps(steps)

	return Projection{Release: r, Steps: steps, EventIDs: f.eventIDs}
}

func (f *fold) runBounds() (first, latest *runObs) {
	for _, obs := range f.runs {
		if first == nil || obs.attempt < first.attempt {
			first = obs
		}
		if latest == nil || obs.attempt > latest.attempt {
			latest = obs
		}
	}
	return first, latest
}

// jobEvidence groups the steps of every known job by stage and attempt.
func (f *fold) jobEvidence() map[string][]*stageEvidence {
	keys := make([]jobKey, 0, len(f.jobs))
	for k := range f.jobs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := f.jobs[keys[i]].job, f.jobs[keys[j]].job
		if keys[i].attempt != keys[j].attempt {
			return keys[i].attempt < keys[j].attempt
		}
		if ta, tb := timeOrZero(a.StartedAt), timeOrZero(b.StartedAt); !ta.Equal(tb) {
			return ta.Before(tb)
		}
		return a.JobID < b.JobID
	})

	byStage := map[string][]*stageEvidence{}
	get := func(stage string, attempt int) *stageEvidence {
		for _, e := range byStage[stage] {
			if e.attempt == attempt {
				return e
			}
		}
		e := &stageEvidence{attempt: attempt}
		byStage[stage] = append(byStage[stage], e)
		return e
	}

	for _, k := range keys {
		obs := f.jobs[k]
		job := obs.job
		defaultStage, known := f.svc.JobStage(job.JobName)
		if !known {
			continue
		}
		steps := append([]events.JobStep(nil), job.Steps...)
		sort.SliceStable(steps, func(i, j int) bool { return steps[i].Number < steps[j].Number })
		names := make([]string, len(steps))
		for i, s := range steps {
			names[i] = s.Name
		}

		touched := map[string]bool{}
		current := defaultStage
		for i, s := range steps {
			stage, _ := f.svc.StageForStep(job.JobName, names, i)
			e := get(stage, k.attempt)
			touched[stage] = true
			state := stepState(s.Status, s.Conclusion)
			e.steps = append(e.steps, Step{
				StageKey: stage, Attempt: k.attempt, Name: s.Name, State: state,
				StartedAt: s.StartedAt, CompletedAt: s.CompletedAt,
				Detail: stepDetail(s), SourceEventID: obs.eventID,
			})
			if s.StartedAt != nil {
				current = stage
				if e.started == nil || s.StartedAt.Before(*e.started) {
					e.started = s.StartedAt
				}
			}
			if s.CompletedAt != nil && (e.completed == nil || s.CompletedAt.After(*e.completed)) {
				e.completed = s.CompletedAt
			}
			if state == StateFailed && !e.failed {
				e.failed = true
				e.failedStep = s.Name
			}
			if state == StateRunning {
				e.running = true
			}
		}

		switch job.Status {
		case "completed":
			if len(steps) == 0 && job.Conclusion != "success" && job.Conclusion != "skipped" {
				e := get(defaultStage, k.attempt)
				e.failed = true
				e.failedStep = job.JobName
				e.completed = job.CompletedAt
			}
			for stage := range touched {
				e := get(stage, k.attempt)
				e.allSucceeded = !e.failed && allSucceeded(e.steps)
			}
		default:
			e := get(current, k.attempt)
			e.running = true
			if e.started == nil {
				e.started = job.StartedAt
			}
		}
	}
	return byStage
}

func (f *fold) reconcile(st *Stage, revision string) []Step {
	var syncs []argoObs
	for _, a := range f.argo {
		if a.n.Revision == revision {
			syncs = append(syncs, a)
		}
	}
	if len(syncs) == 0 {
		return nil
	}

	var finishedAt *time.Time
	var last *argoObs
	for i := range syncs {
		a := syncs[i]
		started := a.n.StartedAt
		if started == nil {
			started = timePtr(a.at)
		}
		switch a.n.Trigger {
		case events.TriggerSyncRunning:
			if !st.State.Terminal() {
				st.State = StateRunning
				st.StartedAt = earliest(st.StartedAt, started)
			}
		case events.TriggerSyncSucceeded, events.TriggerSyncFailed:
			// A later sync result supersedes an earlier one (Argo CD retries).
			finished := a.n.FinishedAt
			if finished == nil {
				finished = timePtr(a.at)
			}
			if finishedAt != nil && finished.Before(*finishedAt) {
				continue
			}
			finishedAt = finished
			st.StartedAt = earliest(st.StartedAt, started)
			st.CompletedAt = finished
			if a.n.Trigger == events.TriggerSyncSucceeded {
				st.State = StateSucceeded
				st.FailureReason = ""
				st.Summary = "Synced " + short(revision, 7)
			} else {
				st.State = StateFailed
				st.Summary = ""
				st.FailureReason = "Sync " + strings.ToLower(orDefault(a.n.OperationPhase, "failed"))
			}
		default:
			// on-health-degraded is Rollout evidence, not Reconcile.
			continue
		}
		last = &syncs[i]
	}
	if last == nil {
		return nil
	}

	first := syncs[0]
	firstAt := first.n.StartedAt
	if firstAt == nil {
		firstAt = timePtr(first.at)
	}
	syncDetail := "Sync " + strings.ToLower(orDefault(last.n.OperationPhase, string(st.State)))
	if last.n.HealthStatus != "" {
		syncDetail += ", health " + strings.ToLower(last.n.HealthStatus)
	}
	return []Step{
		{StageKey: "reconcile", Attempt: 1, Name: "Revision detected", State: StateSucceeded,
			StartedAt: firstAt, CompletedAt: firstAt,
			Detail: fmt.Sprintf("%s at %s", first.n.Application, short(revision, 7)), SourceEventID: first.eventID},
		{StageKey: "reconcile", Attempt: 1, Name: "Sync", State: st.State,
			StartedAt: st.StartedAt, CompletedAt: st.CompletedAt, Detail: syncDetail, SourceEventID: last.eventID},
	}
}

// settle applies the rules that span stages: after a failure the pipeline
// stops, and a stage with no evidence before one that has started is
// unknown, never success (plan section 5.3).
func settle(stages []Stage) {
	for i := range stages {
		if stages[i].State != StateFailed {
			continue
		}
		for j := i + 1; j < len(stages); j++ {
			if stages[j].State == StatePending {
				stages[j].State = StateSkipped
			}
		}
		break
	}
	for i := range stages {
		if stages[i].State != StatePending {
			continue
		}
		for j := i + 1; j < len(stages); j++ {
			if s := stages[j].State; s != StatePending && s != StateSkipped {
				stages[i].State = StateUnknown
				stages[i].FailureReason = "No evidence was received for this stage"
				break
			}
		}
	}
}

func releaseStatus(r Release) (State, *time.Time) {
	anyFailed, allDone := false, true
	frontier := -1 // the last stage with any evidence
	var completed *time.Time
	for i, st := range r.Stages {
		if st.State == StateFailed {
			anyFailed = true
		}
		if st.State != StatePending {
			frontier = i
		}
		if st.State != StateSucceeded && st.State != StateSkipped {
			allDone = false
		}
		if st.CompletedAt != nil && (completed == nil || st.CompletedAt.After(*completed)) {
			completed = st.CompletedAt
		}
	}
	switch {
	case anyFailed:
		return StateFailed, completed
	case r.Superseded:
		return StateSkipped, completed
	case allDone:
		return StateSucceeded, completed
	case frontier < 0:
		return StatePending, nil
	case r.Stages[frontier].State == StateUnknown:
		// The pipeline stalled on a stage without evidence. An unknown stage
		// that later stages moved past does not stall the release.
		return StateUnknown, nil
	}
	return StateRunning, nil
}

// numberSteps gives steps their positions within each stage and attempt, in
// the order they were appended.
func numberSteps(steps []Step) {
	type key struct {
		stage   string
		attempt int
	}
	next := map[key]int{}
	for i := range steps {
		k := key{steps[i].StageKey, steps[i].Attempt}
		next[k]++
		steps[i].Position = next[k]
	}
}

func stepState(status, conclusion string) State {
	switch status {
	case "completed":
		switch conclusion {
		case "success", "neutral":
			return StateSucceeded
		case "skipped":
			return StateSkipped
		case "failure", "cancelled", "timed_out", "startup_failure":
			return StateFailed
		}
		return StateUnknown
	case "in_progress":
		return StateRunning
	}
	return StatePending
}

func allSucceeded(steps []Step) bool {
	ok := false
	for _, s := range steps {
		switch s.State {
		case StateSucceeded:
			ok = true
		case StateSkipped:
		default:
			return false
		}
	}
	return ok
}

// stepDetail is the step's duration, the only structured detail a job step
// carries.
func stepDetail(s events.JobStep) string {
	if s.StartedAt == nil || s.CompletedAt == nil || s.Conclusion == "skipped" {
		return ""
	}
	return s.CompletedAt.Sub(*s.StartedAt).Round(time.Second).String()
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func repoName(repository string) string {
	if _, name, ok := strings.Cut(repository, "/"); ok {
		return name
	}
	return repository
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func earliest(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.Before(*a):
		return b
	}
	return a
}
