package releases

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
)

// Sync is one revision an Argo CD Application synced, with the start of its
// first sync.
type Sync struct {
	Revision string
	Start    time.Time
}

// Runtime is a release's runtime evidence: its workload's events from the
// Argo CD sync of the release's GitOps commit until the Application's next
// sync of another revision (plan sections 8.4 and 8.7, rule 1). The pod
// template's release-id annotation is never used to attribute a rollout.
type Runtime struct {
	Start time.Time
	// End is the start of the next sync, or nil while this is the latest.
	End    *time.Time
	Events []events.Event
}

// RuntimeWindow returns the runtime window of revision among an
// Application's syncs, which must be ordered by start.
func RuntimeWindow(syncs []Sync, revision string) (start time.Time, end *time.Time, ok bool) {
	for i, s := range syncs {
		if s.Revision != revision {
			continue
		}
		for _, next := range syncs[i+1:] {
			if next.Start.After(s.Start) {
				t := next.Start
				return s.Start, &t, true
			}
		}
		return s.Start, nil, true
	}
	return time.Time{}, nil, false
}

// SyncAt returns the revision whose sync window contains t: the latest sync
// that started at or before t.
func SyncAt(syncs []Sync, t time.Time) (Sync, bool) {
	var found Sync
	ok := false
	for _, s := range syncs {
		if s.Start.After(t) {
			break
		}
		found, ok = s, true
	}
	return found, ok
}

// PreviousSync returns the sync before revision's, whose window revision's
// sync closes.
func PreviousSync(syncs []Sync, revision string) (Sync, bool) {
	for i, s := range syncs {
		if s.Revision == revision {
			if i == 0 {
				return Sync{}, false
			}
			return syncs[i-1], true
		}
	}
	return Sync{}, false
}

// restartLimit is how many restarts of one container fail a rollout.
const restartLimit = 3

// createdSlack allows for clock skew between the Argo CD controller, which
// stamps the sync start, and the API server, which stamps creation times.
const createdSlack = 5 * time.Second

type rsObs struct {
	rs      events.ReplicaSet
	eventID int64
}

type podObs struct {
	pod     events.Pod
	eventID int64
}

// rolloutView is the workload as last observed.
type rolloutView struct {
	dep      *events.Deployment
	depEvent int64
	sets     map[string]rsObs
	pods     map[string]podObs
}

type rolloutResult struct {
	state     State
	reason    string
	completed *time.Time

	newRS   *rsObs
	newPods []podObs
	ready   int
	// mismatch names a Ready pod running another digest.
	mismatch string
	// digest is the one the pods must run: the release's, or for a config
	// release, the new ReplicaSet's template image.
	digest string
}

func (v *rolloutView) apply(e events.Event) (bool, error) {
	switch e.Type {
	case events.TypeDeployment:
		var d events.Deployment
		if err := e.Decode(&d); err != nil {
			return false, err
		}
		v.dep, v.depEvent = &d, e.ID
	case events.TypeReplicaSet:
		var rs events.ReplicaSet
		if err := e.Decode(&rs); err != nil {
			return false, err
		}
		v.sets[rs.Name] = rsObs{rs: rs, eventID: e.ID}
	case events.TypePod:
		var p events.Pod
		if err := e.Decode(&p); err != nil {
			return false, err
		}
		v.pods[p.Name] = podObs{pod: p, eventID: e.ID}
	default:
		return false, nil
	}
	return true, nil
}

// evaluate applies the Rollout rules of plan section 8.4 to the view.
func (v *rolloutView) evaluate(digest, artifact string, at time.Time) rolloutResult {
	res := rolloutResult{state: StateRunning}
	dep := v.dep

	// The new ReplicaSet is the one at the Deployment's revision, as kubectl
	// finds it; before the Deployment is seen, the newest one.
	names := make([]string, 0, len(v.sets))
	for name := range v.sets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		obs := v.sets[name]
		switch {
		case obs.rs.Deleted:
		case dep != nil && dep.Revision != "":
			if obs.rs.Revision == dep.Revision {
				res.newRS = &obs
			}
		case res.newRS == nil || obs.rs.CreatedAt.After(res.newRS.rs.CreatedAt):
			res.newRS = &obs
		}
	}
	if res.newRS != nil {
		for _, obs := range v.pods {
			if obs.pod.PodTemplateHash == res.newRS.rs.PodTemplateHash {
				res.newPods = append(res.newPods, obs)
			}
		}
		sort.Slice(res.newPods, func(i, j int) bool {
			a, b := res.newPods[i].pod, res.newPods[j].pod
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
			return a.Name < b.Name
		})
		if digest == "" {
			digest = templateDigest(res.newRS.rs.Images, artifact)
		}
	}
	res.digest = digest

	if dep != nil {
		for _, c := range dep.Conditions {
			if c.Type == "Progressing" && c.Status == "False" && c.Reason == "ProgressDeadlineExceeded" {
				res.state = StateFailed
				res.reason = "Progress deadline exceeded"
				res.completed = firstTime(c.LastUpdateTime, timePtr(at))
				return res
			}
		}
	}

	for _, obs := range res.newPods {
		p := obs.pod
		if p.Deleted {
			continue
		}
		for _, c := range p.Containers {
			if c.RestartCount >= restartLimit {
				res.state = StateFailed
				res.reason = fmt.Sprintf("Container %s in pod %s restarted %d times", c.Name, p.Name, c.RestartCount)
				if c.Reason != "" {
					res.reason += " (" + c.Reason + ")"
				}
				res.completed = timePtr(at)
				return res
			}
		}
		if !p.Ready {
			continue
		}
		res.ready++
		for _, c := range releaseContainers(p, artifact) {
			if c.ImageDigest != digest && res.mismatch == "" {
				res.mismatch = fmt.Sprintf("%s runs %s", p.Name, orDefault(short(c.ImageDigest, 19), "an unknown digest"))
			}
		}
	}
	if res.mismatch != "" {
		res.state = StateFailed
		res.reason = "Pod " + res.mismatch + ", not the release digest"
		res.completed = timePtr(at)
		return res
	}

	if dep != nil && !dep.Deleted && dep.Replicas > 0 && digest != "" &&
		dep.ObservedGeneration >= dep.Generation &&
		dep.UpdatedReplicas == dep.Replicas &&
		dep.AvailableReplicas == dep.Replicas &&
		dep.StatusReplicas == dep.Replicas &&
		res.ready >= int(dep.Replicas) {
		res.state = StateSucceeded
		res.completed = timePtr(at)
		for _, c := range dep.Conditions {
			if c.Type == "Progressing" && c.Reason == "NewReplicaSetAvailable" && c.LastUpdateTime != nil {
				res.completed = c.LastUpdateTime
			}
		}
	}
	return res
}

// rollout projects the Rollout stage from the release's runtime window. The
// first terminal outcome is final: later events in the window, such as a
// rollout of a newer release whose sync notification has not arrived yet,
// never reopen it (plan section 8.7, "History never changes").
func (f *fold) rollout(st *Stage, r Release, rt *Runtime) []Step {
	evs := append([]events.Event(nil), rt.Events...)
	sort.SliceStable(evs, func(i, j int) bool {
		if !evs[i].OccurredAt.Equal(evs[j].OccurredAt) {
			return evs[i].OccurredAt.Before(evs[j].OccurredAt)
		}
		return evs[i].ID < evs[j].ID
	})

	artifact := r.Artifact
	if artifact == "" {
		artifact = f.svc.Artifact
	}
	v := &rolloutView{sets: map[string]rsObs{}, pods: map[string]podObs{}}
	var res rolloutResult
	var firstAt, oldScaledAt *time.Time
	seen := false
	for _, e := range evs {
		ok, err := v.apply(e)
		if err != nil || !ok {
			continue
		}
		seen = true
		f.eventIDs = append(f.eventIDs, e.ID)
		if firstAt == nil {
			firstAt = timePtr(e.OccurredAt)
		}
		res = v.evaluate(r.ArtifactDigest, artifact, e.OccurredAt)
		if d := v.dep; oldScaledAt == nil && d != nil && res.newRS != nil &&
			d.UpdatedReplicas == d.Replicas && d.StatusReplicas == d.UpdatedReplicas {
			oldScaledAt = timePtr(e.OccurredAt)
		}
		if res.state.Terminal() {
			break
		}
	}

	if !seen {
		if rt.End != nil {
			st.State = StateUnknown
			st.FailureReason = "No runtime evidence was received before the next sync"
		}
		return nil
	}

	// Started: the new ReplicaSet's creation when it belongs to this window;
	// a sync that changed no pod template starts at its first observation.
	st.Attempt = 1
	st.StartedAt = firstAt
	if res.newRS != nil && !res.newRS.rs.CreatedAt.Before(rt.Start.Add(-createdSlack)) {
		st.StartedAt = timePtr(res.newRS.rs.CreatedAt)
	}
	st.State = res.state
	switch res.state {
	case StateSucceeded:
		st.CompletedAt = latest(res.completed, st.StartedAt)
		st.Summary = fmt.Sprintf("%d/%d pods ready on %s", res.ready, v.dep.Replicas, short(res.digest, 19))
	case StateFailed:
		st.CompletedAt = latest(res.completed, st.StartedAt)
		st.FailureReason = res.reason
	default:
		if rt.End != nil {
			st.State = StateUnknown
			st.FailureReason = "A newer sync started before this rollout finished"
		} else if v.dep != nil {
			st.Summary = fmt.Sprintf("%d/%d pods ready", res.ready, v.dep.Replicas)
		}
	}
	if res.newRS != nil {
		f.runtimeReleaseID = res.newRS.rs.ReleaseID
	}
	f.runtimeDigest = res.digest
	return rolloutSteps(v, res, res.digest, artifact, oldScaledAt)
}

func rolloutSteps(v *rolloutView, res rolloutResult, digest, artifact string, oldScaledAt *time.Time) []Step {
	var steps []Step
	add := func(s Step) {
		s.StageKey, s.Attempt = "rollout", 1
		steps = append(steps, s)
	}
	if res.newRS == nil {
		return nil
	}
	rs := res.newRS.rs
	detail := rs.Name
	if rs.Revision != "" {
		detail += ", revision " + rs.Revision
	}
	add(Step{Name: "New ReplicaSet", State: StateSucceeded, StartedAt: timePtr(rs.CreatedAt),
		CompletedAt: timePtr(rs.CreatedAt), Detail: detail, SourceEventID: res.newRS.eventID})

	for _, obs := range res.newPods {
		state, completed, detail := podStep(obs.pod, artifact)
		add(Step{Name: "Pod " + obs.pod.Name, State: state, StartedAt: timePtr(obs.pod.CreatedAt),
			CompletedAt: completed, Detail: detail, SourceEventID: obs.eventID})
	}

	if d := v.dep; d != nil {
		old := Step{Name: "Old pods scaled down", StartedAt: timePtr(rs.CreatedAt), SourceEventID: v.depEvent}
		if remaining := d.StatusReplicas - d.UpdatedReplicas; oldScaledAt != nil && remaining <= 0 {
			old.State, old.CompletedAt, old.Detail = StateSucceeded, oldScaledAt, "No old pods remain"
		} else {
			old.State = StateRunning
			if res.state == StateFailed {
				old.State = StateSkipped
			}
			old.Detail = fmt.Sprintf("%d old pods remaining", max(remaining, 0))
		}
		add(old)
	}

	if res.ready > 0 || res.mismatch != "" {
		check := Step{Name: "Digest check", SourceEventID: v.depEvent}
		if res.mismatch != "" {
			check.State, check.Detail = StateFailed, "Pod "+res.mismatch
		} else {
			check.State = StateSucceeded
			check.Detail = fmt.Sprintf("%d ready pods on %s", res.ready, short(digest, 19))
			if res.state != StateSucceeded {
				check.State = StateRunning
			}
		}
		if check.State.Terminal() {
			check.CompletedAt = res.completed
		}
		add(check)
	}
	return steps
}

// podStep describes one pod of the new ReplicaSet. Pod names are shown;
// node names and IPs are never recorded.
func podStep(p events.Pod, artifact string) (State, *time.Time, string) {
	if p.Deleted {
		return StateSkipped, nil, "Deleted before the rollout finished"
	}
	restarts := int32(0)
	reason := ""
	for _, c := range p.Containers {
		restarts = max(restarts, c.RestartCount)
		if reason == "" && c.State != "running" && c.Reason != "" {
			reason = c.Reason
		}
	}
	if restarts >= restartLimit {
		return StateFailed, nil, fmt.Sprintf("%s, %d restarts", orDefault(reason, "Restarting"), restarts)
	}
	if p.Ready {
		digest := ""
		for _, c := range releaseContainers(p, artifact) {
			digest = c.ImageDigest
			break
		}
		return StateSucceeded, p.ReadyChangedAt,
			fmt.Sprintf("Ready on %s, %d restarts", orDefault(short(digest, 19), "unknown digest"), restarts)
	}
	switch {
	case reason != "":
		return StateRunning, nil, reason
	case p.Phase != "":
		return StateRunning, nil, p.Phase
	}
	return StateRunning, nil, "Pending"
}

// releaseContainers returns the containers running the release's image, or
// every container when the artifact is unknown.
func releaseContainers(p events.Pod, artifact string) []events.ContainerStatus {
	if artifact == "" {
		return p.Containers
	}
	var out []events.ContainerStatus
	for _, c := range p.Containers {
		if imageRepository(c.Image) == artifact {
			out = append(out, c)
		}
	}
	return out
}

// imageRepository strips the digest or tag from an image reference.
func imageRepository(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image
}

func firstTime(ts ...*time.Time) *time.Time {
	for _, t := range ts {
		if t != nil {
			return t
		}
	}
	return nil
}

func latest(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	}
	return a
}

// templateDigest is the digest of the artifact's image in a pod template.
func templateDigest(images []string, artifact string) string {
	for _, img := range images {
		repo, digest, ok := strings.Cut(img, "@")
		if ok && (artifact == "" || imageRepository(repo) == artifact) {
			return digest
		}
	}
	return ""
}
