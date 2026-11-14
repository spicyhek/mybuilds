package releases

import (
	"fmt"
	"strings"
	"time"
)

// resultConfig projects a config release (plan section 8.7): a GitOps commit
// that no CI run made, followed from its Argo CD sync through Rollout and
// Verify. Its events are the service Application's notifications for the
// commit, the workload's runtime window and the verifier's checks.
func (f *fold) resultConfig(shortSHA string) Projection {
	r := f.release
	stages := make(map[string]*Stage, len(ConfigStages))
	r.Stages = make([]Stage, len(ConfigStages))
	for i, key := range ConfigStages {
		r.Stages[i] = Stage{Key: key, Position: i + 1, State: StatePending, Attempt: 1}
		stages[key] = &r.Stages[i]
	}
	r.CreatedAt = f.firstAt
	r.GitOpsRepository = f.svc.GitOpsRepository
	var steps []Step

	// GitOps commit: what Argo CD reported about the synced commit.
	var first *argoObs
	for i := range f.argo {
		a := &f.argo[i]
		if !strings.HasPrefix(a.n.Revision, shortSHA) {
			continue
		}
		if first == nil || syncStart(*a).Before(syncStart(*first)) {
			first = a
		}
		if a.n.CommitSubject != "" {
			r.GitOpsSubject, r.GitOpsAuthor = a.n.CommitSubject, a.n.CommitAuthor
		}
	}
	if first != nil {
		r.GitOpsSHA = first.n.Revision
		start := syncStart(*first)
		gc := stages["gitops_commit"]
		gc.State = StateSucceeded
		gc.StartedAt, gc.CompletedAt = timePtr(start), timePtr(start)
		gc.Summary = orDefault(r.GitOpsSubject, "Commit "+short(r.GitOpsSHA, 7))
		detail := short(r.GitOpsSHA, 7)
		if r.GitOpsAuthor != "" {
			detail += " by " + r.GitOpsAuthor
		}
		steps = append(steps, Step{StageKey: "gitops_commit", Attempt: 1, Name: "Commit without a Release-Id trailer",
			State: StateSucceeded, StartedAt: timePtr(start), CompletedAt: timePtr(start),
			Detail: detail, SourceEventID: first.eventID})
		steps = append(steps, f.reconcile(stages["reconcile"], r.GitOpsSHA)...)
	}

	rsteps, samples := f.runtimeStages(&r, stages)
	steps = append(steps, rsteps...)
	if rs := stages["rollout"]; rs.State == StateSucceeded && r.RuntimeReleaseID != "" {
		rs.Summary += fmt.Sprintf(", serving %s", r.RuntimeReleaseID)
	}

	settle(r.Stages)
	r.Status, r.CompletedAt = releaseStatus(r)
	numberSteps(steps)
	return Projection{Release: r, Steps: steps, EventIDs: f.eventIDs, Samples: samples}
}

// syncStart is when a sync notification's operation started.
func syncStart(a argoObs) time.Time {
	if a.n.StartedAt != nil {
		return *a.n.StartedAt
	}
	return a.at
}

// handEditFlag explains why a config release bypassed CI: its new pods carry
// another release ID or digest than the pod template did before the sync,
// so a hand edit changed what runs (plan section 8.7). A config release
// deployed together with an application release is not flagged: the change
// came from CI's own bump.
func handEditFlag(r Release, rt *Runtime) string {
	if r.Kind != KindConfig || rt == nil || rt.Partner != "" {
		return ""
	}
	switch {
	case r.RuntimeReleaseID != "" && rt.PrevReleaseID != "" && r.RuntimeReleaseID != rt.PrevReleaseID:
		return fmt.Sprintf("A commit without a CI release moved the pods from %s to %s", rt.PrevReleaseID, r.RuntimeReleaseID)
	case r.ArtifactDigest != "" && rt.PrevDigest != "" && r.ArtifactDigest != rt.PrevDigest:
		return fmt.Sprintf("A commit without a CI release moved the pods from %s to %s", short(rt.PrevDigest, 19), short(r.ArtifactDigest, 19))
	}
	return ""
}
