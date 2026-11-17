package releases

import (
	"testing"

	"github.com/spicyhek/mybuilds/internal/events"
)

// TestReconstructedSteps: a rollout the watch only saw once it was over, as
// when mybuilds' own release replaces its pod, takes its times from the
// objects, and its steps are marked reconstructed (plan section 8.8).
func TestReconstructedSteps(t *testing.T) {
	b := &builder{t: t}
	throughReconcile(b)
	rb := runtimeBuilder{b}
	// The old pod saw the Deployment's new generation at the sync, then
	// stopped.
	rb.dep(101, events.Deployment{Generation: 58, ObservedGeneration: 57, Replicas: 1, StatusReplicas: 1,
		UpdatedReplicas: 0, ReadyReplicas: 1, AvailableReplicas: 1, Conditions: progressing("ReplicaSetUpdated", 101)})
	// The new pod listed everything at t=140: the ReplicaSet created at
	// 105 and its own pod, created at 106 and not Ready yet.
	rb.rs(140, "personal-site-"+newHash, "12", newHash, 105)
	name := "personal-site-" + newHash + "-a"
	b.add(events.TypePod, at(140), 1, events.Keys{Workload: testWorkload}, events.Pod{
		Namespace: "default", Name: name, UID: name, ReplicaSet: "personal-site-" + newHash,
		PodTemplateHash: newHash, CreatedAt: at(106), Phase: "Running",
		Containers: []events.ContainerStatus{{Name: "web", Image: testImage + "@" + testDigest, ImageDigest: testDigest, State: "running"}},
	})
	rb.dep(140, events.Deployment{Generation: 58, ObservedGeneration: 58, Replicas: 1, StatusReplicas: 1,
		UpdatedReplicas: 1, ReadyReplicas: 0, AvailableReplicas: 0, Conditions: progressing("ReplicaSetUpdated", 105)})
	// Then it saw itself become Ready, live.
	rb.pod(145, name, newHash, 106, true, testDigest, 0, "")
	rb.dep(146, events.Deployment{Generation: 58, ObservedGeneration: 58, Replicas: 1, StatusReplicas: 1,
		UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, Conditions: progressing("NewReplicaSetAvailable", 145)})

	own, rt := window(b, nil)
	p := projectRuntime(t, own, rt)
	expect(t, p.Release, R, S, S, S, S, S, S, P)
	ro := p.Release.Stages[5]
	if !ro.StartedAt.Equal(at(105)) || !ro.CompletedAt.Equal(at(145)) {
		t.Errorf("rollout ran %v to %v, want 105s to 145s", ro.StartedAt, ro.CompletedAt)
	}

	want := map[string]bool{"New ReplicaSet": true, "Pod " + name: true, "Old pods scaled down": true, "Digest check": false}
	seen := map[string]bool{}
	for _, s := range p.Steps {
		if s.StageKey != "rollout" {
			continue
		}
		seen[s.Name] = true
		if w, ok := want[s.Name]; !ok || s.Reconstructed != w {
			t.Errorf("%s reconstructed = %v, want %v", s.Name, s.Reconstructed, w)
		}
		if s.Name == "Pod "+name && (!s.StartedAt.Equal(at(106)) || !s.CompletedAt.Equal(at(145))) {
			t.Errorf("pod step ran %v to %v, want 106s to 145s", s.StartedAt, s.CompletedAt)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("step %s missing", name)
		}
	}

	// A rollout watched throughout has no reconstructed step.
	live := &builder{t: t}
	throughReconcile(live)
	rollingUpdate(runtimeBuilder{live}, testDigest)
	own, rt = window(live, nil)
	for _, s := range projectRuntime(t, own, rt).Steps {
		if s.StageKey == "rollout" && s.Reconstructed {
			t.Errorf("%s is reconstructed in a rollout that was watched", s.Name)
		}
	}
}
