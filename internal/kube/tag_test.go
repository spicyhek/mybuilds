package kube

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/spicyhek/mybuilds/internal/releases"
)

// tagRollout is a config release of a Deployment whose image is still a
// tag, as mynids was before its first digest-pinned release (2026-11-18):
// the template carries no digest to expect, so the pods' must agree.
func tagRollout(t *testing.T, digests ...string) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	const hash = "eeeeeeeeee"
	tagged := image + ":sha-" + strings.Repeat("9", 40)
	cfg := h.configSync(strings.Repeat("c", 40), 100, 101)
	dep := func(ready int32, reason string, at int) *appsv1.Deployment {
		d := deployment("7", 60, 60, int32(len(digests)), int32(len(digests)), ready, reason, at)
		d.Spec.Replicas = ptr(int32(len(digests)))
		d.Spec.Template.Spec.Containers[0].Image = tagged
		delete(d.Spec.Template.Annotations, releaseIDAnnotation)
		return d
	}
	h.observe(102, dep(0, "ReplicaSetUpdated", 102))
	rs := replicaSet(hash, "7", 102)
	rs.Spec.Template.Spec.Containers[0].Image = tagged
	h.observe(102, rs)
	for i, d := range digests {
		suffix := string(rune('a' + i))
		created := 103 + 10*i
		h.observe(created, pod(hash, suffix, created, 0, d))
		h.observe(created+8, pod(hash, suffix, created, created+8, d))
	}
	end := 103 + 10*len(digests)
	h.observe(end, dep(int32(len(digests)), "NewReplicaSetAvailable", end))
	return h, cfg
}

func TestTagImageRolloutTakesTheDigestFromItsPods(t *testing.T) {
	h, cfg := tagRollout(t, digestOf('b'))
	st := h.rolloutStage(cfg)
	if st.State != releases.StateSucceeded {
		t.Fatalf("rollout %s (%s), want succeeded", st.State, st.FailureReason)
	}
	if !strings.Contains(st.Summary, "1/1 pods ready on sha256:bbbbbbbbbbbb") {
		t.Errorf("summary %q", st.Summary)
	}
	r := h.get(cfg)
	if r.ArtifactDigest != digestOf('b') {
		t.Errorf("artifact digest %q, want the pods'", r.ArtifactDigest)
	}
	if r.Flag != "" {
		t.Errorf("flagged as a hand edit: %q", r.Flag)
	}
	var check *releases.Step
	for i, s := range st.Steps {
		if s.Name == "Digest check" {
			check = &st.Steps[i]
		}
	}
	if check == nil || check.State != releases.StateSucceeded || !strings.Contains(check.Detail, "tag") {
		t.Errorf("digest check %+v, want succeeded and naming the tag", check)
	}
}

// Pods of one tag that run different digests, such as a tag re-pushed
// during the rollout, are not one release.
func TestTagImageRolloutFailsWhenItsPodsDisagree(t *testing.T) {
	h, cfg := tagRollout(t, digestOf('b'), digestOf('c'))
	st := h.rolloutStage(cfg)
	if st.State != releases.StateFailed || !strings.Contains(st.FailureReason, "personal-site-eeeeeeeeee-b") {
		t.Errorf("rollout %s (%s), want failed naming the second pod", st.State, st.FailureReason)
	}
}
