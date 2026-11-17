package kube

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/spicyhek/mybuilds/internal/releases"
)

// TestScaleToZeroAndBack is mybuilds' own outage test (2026-11-17): a
// commit scales the Deployment to zero, a later one scales it back. Both
// reuse the ReplicaSet, so after the scale-up it looks exactly as it did
// after the first rollout. That snapshot must still be stored, or the
// second config release never sees its pods come back.
func TestScaleToZeroAndBack(t *testing.T) {
	h := newHarness(t)
	g2 := strings.Repeat("2", 40)
	r12 := h.release(12, strings.Repeat("b", 40), g2, digestOf('b'), 50)
	h.serving = r12
	h.sync(g2, 100, 104)
	const hash = "bbbbbbbbbb"
	// The ReplicaSet's own generation counts its spec changes.
	rs := func(gen int64, replicas, ready int32) *appsv1.ReplicaSet {
		r := replicaSet(hash, "12", 101)
		r.Generation = gen
		r.Spec.Replicas = ptr(replicas)
		r.Spec.Template.Spec.Containers[0].Image = image + "@" + digestOf('b')
		r.Spec.Template.Annotations[releaseIDAnnotation] = r12
		r.Status = appsv1.ReplicaSetStatus{Replicas: ready, ReadyReplicas: ready, AvailableReplicas: ready, ObservedGeneration: gen}
		return r
	}
	dep := func(gen int64, replicas int32, at int) *appsv1.Deployment {
		d := deployment("12", gen, gen, replicas, replicas, replicas, "NewReplicaSetAvailable", at)
		d.Spec.Replicas = ptr(replicas)
		d.Spec.Template.Spec.Containers[0].Image = image + "@" + digestOf('b')
		d.Spec.Template.Annotations[releaseIDAnnotation] = r12
		return d
	}

	// Release 12 rolls out two pods.
	h.observe(101, dep(150, 2, 101))
	h.observe(101, rs(1, 2, 0))
	h.observe(102, pod(hash, "a", 101, 0, digestOf('b')))
	h.observe(108, pod(hash, "a", 101, 108, digestOf('b')))
	h.observe(109, pod(hash, "b", 108, 0, digestOf('b')))
	h.observe(113, pod(hash, "b", 108, 113, digestOf('b')))
	h.observe(114, rs(1, 2, 2))
	h.observe(115, dep(150, 2, 114))
	if st := h.rolloutStage(r12); st.State != releases.StateSucceeded {
		t.Fatalf("release 12 rollout %s (%s)", st.State, st.FailureReason)
	}

	// Scaled to zero.
	down := h.configSync(strings.Repeat("c", 40), 300, 301)
	h.observe(302, dep(151, 0, 114))
	h.observe(302, rs(2, 0, 0))
	h.w.handle(h.ctx, change{obj: pod(hash, "a", 101, 108, digestOf('b')), deleted: true})
	h.w.handle(h.ctx, change{obj: pod(hash, "b", 108, 113, digestOf('b')), deleted: true})

	// Scaled back: same ReplicaSet, two new pods, and the ReplicaSet ends
	// with the replica counts it had after release 12.
	up := h.configSync(strings.Repeat("d", 40), 500, 501)
	h.observe(502, dep(152, 2, 114))
	h.observe(502, rs(3, 2, 0))
	h.observe(503, pod(hash, "c", 502, 0, digestOf('b')))
	h.observe(509, pod(hash, "c", 502, 509, digestOf('b')))
	h.observe(510, pod(hash, "d", 509, 0, digestOf('b')))
	h.observe(514, pod(hash, "d", 509, 514, digestOf('b')))
	h.observe(515, rs(3, 2, 2))
	h.observe(516, dep(152, 2, 114))

	if st := h.rolloutStage(up); st.State != releases.StateSucceeded {
		t.Errorf("scale-up rollout %s (%s), want succeeded", st.State, st.FailureReason)
	}
	// The scale-down replaced no pods with new ones; it is not a failure.
	if st := h.rolloutStage(down); st.State == releases.StateFailed {
		t.Errorf("scale-down rollout failed: %s", st.FailureReason)
	}
}
