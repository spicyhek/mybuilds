package releases

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
)

const (
	testWorkload = "default/personal-site"
	testDigest   = "sha256:" + testSHA + "aaaaaaaaaaaaaaaaaaaaaaaa"
	otherDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testImage    = "ghcr.io/spicyhek/homelab-hosted-personal-site"
	newHash      = "576fd9b569"
)

// throughReconcile records a release that has synced at t=100.
func throughReconcile(b *builder) {
	b.run(1, "in_progress", "", 1)
	b.job(1, "build", "completed", "success", 2, buildSteps...)
	b.published(1, 40)
	b.job(1, "bump-gitops", "completed", "success", 45, [2]string{"Pin image digest", "success"})
	b.gitopsUpdated(1, 60)
	b.run(1, "completed", "success", 62)
	b.argo(events.TriggerSyncRunning, 100, 0)
	b.argo(events.TriggerSyncSucceeded, 100, 104)
}

type runtimeBuilder struct {
	*builder
}

func (b runtimeBuilder) dep(occurred int, d events.Deployment) {
	d.Namespace, d.Name, d.UID = "default", "personal-site", "dep-uid"
	if d.Revision == "" {
		d.Revision = "12"
	}
	b.add(events.TypeDeployment, at(occurred), 1, events.Keys{Workload: testWorkload}, d)
}

func (b runtimeBuilder) rs(occurred int, name, revision, hash string, created int) {
	b.add(events.TypeReplicaSet, at(occurred), 1, events.Keys{Workload: testWorkload}, events.ReplicaSet{
		Namespace: "default", Name: name, UID: name, Deployment: "personal-site", Revision: revision,
		PodTemplateHash: hash, CreatedAt: at(created), Replicas: 2,
	})
}

func (b runtimeBuilder) pod(occurred int, name, hash string, created int, ready bool, digest string, restarts int32, reason string) {
	p := events.Pod{Namespace: "default", Name: name, UID: name, ReplicaSet: "personal-site-" + hash,
		PodTemplateHash: hash, CreatedAt: at(created), Phase: "Running", Ready: ready}
	c := events.ContainerStatus{Name: "web", Image: testImage + "@" + digest, ImageDigest: digest,
		Ready: ready, RestartCount: restarts, State: "running"}
	if reason != "" {
		c.State, c.Reason = "waiting", reason
		p.Phase = "Pending"
	}
	if ready {
		p.ReadyChangedAt = tp(occurred)
	}
	p.Containers = []events.ContainerStatus{c}
	b.add(events.TypePod, at(occurred), 1, events.Keys{Workload: testWorkload}, p)
}

func progressing(reason string, updated int) []events.Condition {
	status := "True"
	if reason == "ProgressDeadlineExceeded" {
		status = "False"
	}
	return []events.Condition{{Type: "Available", Status: "True"},
		{Type: "Progressing", Status: status, Reason: reason, LastUpdateTime: tp(updated)}}
}

// rollingUpdate records a two-replica rollout that starts at t=101 and
// finishes at t=115, with pods on digest.
func rollingUpdate(b runtimeBuilder, digest string) {
	b.dep(101, events.Deployment{Generation: 58, ObservedGeneration: 58, Replicas: 2, StatusReplicas: 3,
		UpdatedReplicas: 1, ReadyReplicas: 2, AvailableReplicas: 2, Conditions: progressing("ReplicaSetUpdated", 101)})
	b.rs(101, "personal-site-"+newHash, "12", newHash, 101)
	b.pod(102, "personal-site-"+newHash+"-a", newHash, 101, false, "", 0, "ContainerCreating")
	b.pod(108, "personal-site-"+newHash+"-a", newHash, 101, true, digest, 0, "")
	b.pod(109, "personal-site-"+newHash+"-b", newHash, 108, false, "", 0, "ContainerCreating")
	b.pod(113, "personal-site-"+newHash+"-b", newHash, 108, true, digest, 0, "")
	b.dep(115, events.Deployment{Generation: 58, ObservedGeneration: 58, Replicas: 2, StatusReplicas: 2,
		UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2, Conditions: progressing("NewReplicaSetAvailable", 114)})
}

func projectRuntime(t *testing.T, b *builder, rt *Runtime) Projection {
	t.Helper()
	p, err := ProjectWithRuntime(testRelease, b.evs, personalSite(t), rt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// split separates runtime events from the release's own, as storage does.
func split(b *builder) (own, runtime []events.Event) {
	for _, e := range b.evs {
		if e.Keys.Workload != "" {
			runtime = append(runtime, e)
		} else {
			own = append(own, e)
		}
	}
	return own, runtime
}

func window(b *builder, end *time.Time) (*builder, *Runtime) {
	own, rt := split(b)
	return &builder{t: b.t, evs: own}, &Runtime{Start: at(100), End: end, Events: rt}
}

func stepNames(p Projection, stage string) []string {
	var out []string
	for _, s := range p.Steps {
		if s.StageKey == stage {
			out = append(out, s.Name+"="+string(s.State))
		}
	}
	return out
}

func TestRolloutSucceedsWhenEveryPodRunsTheDigest(t *testing.T) {
	b := &builder{t: t}
	throughReconcile(b)
	rollingUpdate(runtimeBuilder{b}, testDigest)

	own, rt := window(b, nil)
	p := projectRuntime(t, own, rt)
	expect(t, p.Release, R, S, S, S, S, S, S, P)

	ro := p.Release.Stages[5]
	if !ro.StartedAt.Equal(at(101)) || !ro.CompletedAt.Equal(at(114)) {
		t.Errorf("rollout ran %v to %v, want 101s to 114s", ro.StartedAt, ro.CompletedAt)
	}
	if want := "2/2 pods ready on " + testDigest[:19]; ro.Summary != want {
		t.Errorf("summary %q, want %q", ro.Summary, want)
	}
	want := []string{"New ReplicaSet=succeeded", "Pod personal-site-576fd9b569-a=succeeded",
		"Pod personal-site-576fd9b569-b=succeeded", "Old pods scaled down=succeeded", "Digest check=succeeded"}
	if got := stepNames(p, "rollout"); !slices.Equal(got, want) {
		t.Errorf("rollout steps\n got %v\nwant %v", got, want)
	}
}

func TestRolloutRunningShowsReadyCount(t *testing.T) {
	b := &builder{t: t}
	throughReconcile(b)
	rb := runtimeBuilder{b}
	rb.dep(101, events.Deployment{Generation: 58, ObservedGeneration: 58, Replicas: 2, StatusReplicas: 3,
		UpdatedReplicas: 1, ReadyReplicas: 2, AvailableReplicas: 2})
	rb.rs(101, "personal-site-"+newHash, "12", newHash, 101)
	rb.pod(108, "personal-site-"+newHash+"-a", newHash, 101, true, testDigest, 0, "")

	own, rt := window(b, nil)
	p := projectRuntime(t, own, rt)
	expect(t, p.Release, R, S, S, S, S, S, R, P)
	if got := p.Release.Stages[5].Summary; got != "1/2 pods ready" {
		t.Errorf("summary %q", got)
	}
}

func TestRolloutFailures(t *testing.T) {
	cases := []struct {
		name   string
		record func(runtimeBuilder)
		reason string
	}{
		{"progress deadline", func(b runtimeBuilder) {
			b.rs(101, "personal-site-"+newHash, "12", newHash, 101)
			b.dep(700, events.Deployment{Generation: 58, ObservedGeneration: 58, Replicas: 2, StatusReplicas: 3,
				UpdatedReplicas: 1, ReadyReplicas: 2, AvailableReplicas: 2, Conditions: progressing("ProgressDeadlineExceeded", 700)})
		}, "Progress deadline exceeded"},
		{"crash loop", func(b runtimeBuilder) {
			b.dep(101, events.Deployment{Generation: 58, ObservedGeneration: 58, Replicas: 2, StatusReplicas: 3, UpdatedReplicas: 1})
			b.rs(101, "personal-site-"+newHash, "12", newHash, 101)
			b.pod(160, "personal-site-"+newHash+"-a", newHash, 101, false, testDigest, 3, "CrashLoopBackOff")
		}, "restarted 3 times (CrashLoopBackOff)"},
		{"wrong digest", func(b runtimeBuilder) {
			rollingUpdate(b, otherDigest)
		}, "runs " + otherDigest[:19] + ", not the release digest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &builder{t: t}
			throughReconcile(b)
			tc.record(runtimeBuilder{b})
			own, rt := window(b, nil)
			p := projectRuntime(t, own, rt)
			expect(t, p.Release, F, S, S, S, S, S, F, K)
			if got := p.Release.Stages[5].FailureReason; !strings.Contains(got, tc.reason) {
				t.Errorf("reason %q, want it to contain %q", got, tc.reason)
			}
		})
	}
}

func TestRolloutClosedWindow(t *testing.T) {
	next := at(300)

	b := &builder{t: t}
	throughReconcile(b)
	own, rt := window(b, &next)
	expect(t, projectRuntime(t, own, rt).Release, U, S, S, S, S, S, U, P)

	own, rt = window(b, nil)
	expect(t, projectRuntime(t, own, rt).Release, R, S, S, S, S, S, P, P)

	// Still rolling out when a newer sync started.
	b = &builder{t: t}
	throughReconcile(b)
	rb := runtimeBuilder{b}
	rb.rs(101, "personal-site-"+newHash, "12", newHash, 101)
	rb.pod(108, "personal-site-"+newHash+"-a", newHash, 101, false, "", 0, "ImagePullBackOff")
	own, rt = window(b, &next)
	p := projectRuntime(t, own, rt)
	expect(t, p.Release, U, S, S, S, S, S, U, P)
	if got := stepNames(p, "rollout"); !slices.Contains(got, "Pod personal-site-576fd9b569-a=running") {
		t.Errorf("steps %v", got)
	}
}

func TestRolloutOutcomeIsFinal(t *testing.T) {
	b := &builder{t: t}
	throughReconcile(b)
	rb := runtimeBuilder{b}
	rollingUpdate(rb, testDigest)
	// The next release starts rolling out before its sync notification
	// arrives; the finished rollout must not reopen.
	rb.dep(200, events.Deployment{Revision: "13", Generation: 59, ObservedGeneration: 59, Replicas: 2,
		StatusReplicas: 3, UpdatedReplicas: 1})
	rb.rs(200, "personal-site-77d5d9689d", "13", "77d5d9689d", 200)
	rb.pod(210, "personal-site-77d5d9689d-a", "77d5d9689d", 200, true, otherDigest, 0, "")

	own, rt := window(b, nil)
	p := projectRuntime(t, own, rt)
	expect(t, p.Release, R, S, S, S, S, S, S, P)
	if got := p.Release.Stages[5].CompletedAt; !got.Equal(at(114)) {
		t.Errorf("completed %v", got)
	}
}

func TestRolloutWaitsForFailedSync(t *testing.T) {
	b := &builder{t: t}
	b.run(1, "in_progress", "", 1)
	b.job(1, "build", "completed", "success", 2, buildSteps...)
	b.published(1, 40)
	b.gitopsUpdated(1, 60)
	b.run(1, "completed", "success", 62)
	b.argo(events.TriggerSyncFailed, 100, 103)
	rollingUpdate(runtimeBuilder{b}, testDigest)

	own, rt := window(b, nil)
	expect(t, projectRuntime(t, own, rt).Release, F, S, S, S, S, F, K, K)
}

func TestRuntimeWindow(t *testing.T) {
	syncs := []Sync{{"aaa", at(0)}, {"bbb", at(100)}, {"ccc", at(200)}}

	start, end, ok := RuntimeWindow(syncs, "bbb")
	if !ok || !start.Equal(at(100)) || end == nil || !end.Equal(at(200)) {
		t.Errorf("bbb window %v %v %v", start, end, ok)
	}
	if _, end, ok := RuntimeWindow(syncs, "ccc"); !ok || end != nil {
		t.Errorf("latest window should be open: %v %v", end, ok)
	}
	if _, _, ok := RuntimeWindow(syncs, "zzz"); ok {
		t.Error("unknown revision has no window")
	}
	if s, ok := SyncAt(syncs, at(150)); !ok || s.Revision != "bbb" {
		t.Errorf("SyncAt(150) = %v %v", s, ok)
	}
	if _, ok := SyncAt(syncs, at(-1)); ok {
		t.Error("no sync before the first")
	}
	if s, ok := PreviousSync(syncs, "ccc"); !ok || s.Revision != "bbb" {
		t.Errorf("PreviousSync(ccc) = %v %v", s, ok)
	}
	if _, ok := PreviousSync(syncs, "aaa"); ok {
		t.Error("the first sync has no previous")
	}
}
