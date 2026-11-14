package kube

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
)

var t0 = time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)

func at(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

const image = "ghcr.io/spicyhek/homelab-hosted-personal-site"

func digestOf(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func ptr[T any](v T) *T { return &v }

func deployment(revision string, gen, observed int64, statusReplicas, updated, ready int32, progressReason string, progressAt int) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "personal-site", UID: "dep-uid", Generation: gen,
			Labels:      map[string]string{ManagedLabel: "true"},
			Annotations: map[string]string{revisionAnnotation: revision}},
		Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(2)), Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{releaseIDAnnotation: "personal-site/x/production/" + revision}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: image}}}}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: observed, Replicas: statusReplicas,
			UpdatedReplicas: updated, ReadyReplicas: ready, AvailableReplicas: ready},
	}
	if progressReason != "" {
		d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing,
			Status: corev1.ConditionTrue, Reason: progressReason, LastUpdateTime: metav1.NewTime(at(progressAt))}}
	}
	return d
}

func replicaSet(hash, revision string, created int) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "personal-site-" + hash, UID: types.UID("rs-" + hash),
			CreationTimestamp: metav1.NewTime(at(created)),
			Labels:            map[string]string{ManagedLabel: "true", podTemplateHash: hash},
			Annotations:       map[string]string{revisionAnnotation: revision},
			OwnerReferences:   []metav1.OwnerReference{{Kind: "Deployment", Name: "personal-site", Controller: ptr(true)}}},
		Spec: appsv1.ReplicaSetSpec{Replicas: ptr(int32(2)), Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: image}}}}},
	}
}

func pod(hash, suffix string, created int, readyAt int, digest string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "personal-site-" + hash + "-" + suffix,
			UID: types.UID("pod-" + hash + suffix), CreationTimestamp: metav1.NewTime(at(created)),
			Labels:          map[string]string{ManagedLabel: "true", podTemplateHash: hash},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "personal-site-" + hash, Controller: ptr(true)}}},
		Spec: corev1.PodSpec{NodeName: "homelab-worker2", Containers: []corev1.Container{{Name: "web", Image: image + "@" + digest}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, PodIP: "10.42.1.137", HostIP: "192.168.1.20",
			ContainerStatuses: []corev1.ContainerStatus{{Name: "web", Image: image,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}}},
	}
	if readyAt > 0 {
		p.Status.Phase = corev1.PodRunning
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(at(readyAt))}}
		p.Status.ContainerStatuses[0] = corev1.ContainerStatus{Name: "web", Image: image, Ready: true,
			ImageID: image + "@" + digest,
			State:   corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(at(readyAt - 1))}}}
	}
	return p
}

func TestPodEventDropsNodeAndAddresses(t *testing.T) {
	ev, err := PodEvent(pod("576fd9b569", "a", 10, 20, digestOf('a')), false, at(21))
	if err != nil || ev == nil {
		t.Fatalf("PodEvent: %v %v", ev, err)
	}
	if ev.Keys.Workload != "default/personal-site" || ev.Provider != events.ProviderKubernetes {
		t.Errorf("workload %q provider %q", ev.Keys.Workload, ev.Provider)
	}
	for _, leak := range []string{"homelab-worker2", "10.42.1.137", "192.168.1.20"} {
		if strings.Contains(string(ev.Payload), leak) {
			t.Errorf("payload contains %q: %s", leak, ev.Payload)
		}
	}
	var p events.Pod
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if !p.Ready || p.Containers[0].ImageDigest != digestOf('a') || !p.ReadyChangedAt.Equal(at(20)) {
		t.Errorf("pod payload %+v", p)
	}
}

func TestDeliveryIDChangesOnlyWithTheSnapshot(t *testing.T) {
	a, _ := DeploymentEvent(deployment("12", 58, 58, 2, 2, 2, "NewReplicaSetAvailable", 30), false, at(31))
	b, _ := DeploymentEvent(deployment("12", 58, 58, 2, 2, 2, "NewReplicaSetAvailable", 30), false, at(3000))
	c, _ := DeploymentEvent(deployment("12", 58, 58, 3, 2, 2, "NewReplicaSetAvailable", 30), false, at(31))
	if a.DeliveryID != b.DeliveryID {
		t.Error("an unchanged object seen later must be a duplicate")
	}
	if a.DeliveryID == c.DeliveryID {
		t.Error("a status change must be a new event")
	}
}

func TestUnownedObjectsAreIgnored(t *testing.T) {
	p := pod("576fd9b569", "a", 10, 0, digestOf('a'))
	p.OwnerReferences = nil
	if ev, err := PodEvent(p, false, at(1)); ev != nil || err != nil {
		t.Errorf("bare pod: %v %v", ev, err)
	}
	rs := replicaSet("576fd9b569", "1", 1)
	rs.OwnerReferences = nil
	if ev, err := ReplicaSetEvent(rs, false, at(1)); ev != nil || err != nil {
		t.Errorf("bare replica set: %v %v", ev, err)
	}
}

func TestImageDigest(t *testing.T) {
	for in, want := range map[string]string{
		image + "@" + digestOf('a'):                        digestOf('a'),
		"docker-pullable://" + image + "@" + digestOf('b'): digestOf('b'),
		digestOf('c'): digestOf('c'),
		"":            "",
	} {
		if got := imageDigest(in); got != want {
			t.Errorf("imageDigest(%q) = %q, want %q", in, got, want)
		}
	}
}

// harness drives the watcher's record path with a fake clock.
type harness struct {
	t         *testing.T
	ctx       context.Context
	store     *storage.Store
	projector *releases.Projector
	w         *Watcher
	now       time.Time
	nextEvent int
	// serving is the release-id annotation of the pods rollout starts.
	serving string
}

func newHarness(t *testing.T) *harness {
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
	h := &harness{t: t, ctx: ctx, store: store, projector: releases.NewProjector(store, svcs, nil)}
	h.w = &Watcher{
		opts:     Options{Services: svcs, Store: store, Projector: h.projector, Now: func() time.Time { return h.now }},
		observed: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "x"}, []string{"kind", "result"}),
		synced:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "y"}),
	}
	h.w.opts.Logger = discardLogger()
	return h
}

func (h *harness) observe(s int, obj any) {
	h.now = at(s)
	h.w.handle(h.ctx, change{obj: obj})
}

// provider stores and projects one non-runtime event, as ingestion does.
func (h *harness) provider(provider, typ string, s int, keys events.Keys, payload any) {
	h.t.Helper()
	data, _ := json.Marshal(payload)
	h.nextEvent++
	ev := &events.Event{Provider: provider, DeliveryID: typ + "/" + string(rune('a'+h.nextEvent)), Type: typ,
		Attempt: 1, OccurredAt: at(s), ReceivedAt: at(s), Keys: keys, Payload: data}
	if _, err := h.store.InsertEvent(h.ctx, ev); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.projector.ProjectEvent(h.ctx, *ev); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) release(run int, sha, gitops string, digest string, s int) string {
	id := "personal-site/" + sha[:7] + "/production/" + itoa(run)
	keys := events.Keys{ReleaseID: id, RunID: int64(run)}
	h.provider(events.ProviderCI, events.TypeReleasePublished, s, keys, events.ReleasePublished{
		ReleaseID: id, Service: "personal-site", Environment: "production", SourceSHA: sha,
		Artifact: image, ArtifactDigest: digest, WorkflowRunID: int64(run), WorkflowRunNumber: run, RunAttempt: 1, OccurredAt: at(s)})
	keys.Revision = gitops
	h.provider(events.ProviderCI, events.TypeGitOpsUpdated, s+5, keys, events.GitOpsUpdated{
		ReleaseID: id, GitOpsRepository: "spicyhek/homelab-infra", GitOpsSHA: gitops, WorkflowRunID: int64(run),
		RunAttempt: 1, OccurredAt: at(s + 5)})
	return id
}

func (h *harness) sync(gitops string, started, finished int) {
	h.provider(events.ProviderArgoCD, events.TypeArgoCD, started, events.Keys{Revision: gitops}, events.ArgoCDNotification{
		Trigger: events.TriggerSyncRunning, Application: "personal-site", Revision: gitops, StartedAt: ptr(at(started))})
	h.provider(events.ProviderArgoCD, events.TypeArgoCD, finished, events.Keys{Revision: gitops}, events.ArgoCDNotification{
		Trigger: events.TriggerSyncSucceeded, Application: "personal-site", Revision: gitops,
		StartedAt: ptr(at(started)), FinishedAt: ptr(at(finished))})
}

// configSync records Argo CD syncing a human commit, keyed the way
// ingestion keys a commit without a Release-Id trailer.
func (h *harness) configSync(gitops string, started, finished int) string {
	id := releases.ConfigReleaseID("personal-site", "production", gitops)
	keys := events.Keys{ReleaseID: id, Revision: gitops}
	for _, n := range []events.ArgoCDNotification{
		{Trigger: events.TriggerSyncRunning, StartedAt: ptr(at(started))},
		{Trigger: events.TriggerSyncSucceeded, StartedAt: ptr(at(started)), FinishedAt: ptr(at(finished))},
	} {
		n.Application, n.Revision = "personal-site", gitops
		n.CommitSubject, n.CommitAuthor = "Raise personal-site memory limit", "spicyhek"
		occurred := started
		if n.FinishedAt != nil {
			occurred = finished
		}
		h.provider(events.ProviderArgoCD, events.TypeArgoCD, occurred, keys, n)
	}
	return id
}

func (h *harness) rollout(hash, revision string, base int, digest string) {
	gen := int64(50 + base)
	// The Deployment's pod template matches the new ReplicaSet's.
	dep := func(d *appsv1.Deployment) *appsv1.Deployment {
		d.Spec.Template.Spec.Containers[0].Image = image + "@" + digest
		if h.serving != "" {
			d.Spec.Template.Annotations[releaseIDAnnotation] = h.serving
		}
		return d
	}
	h.observe(base+1, dep(deployment(revision, gen, gen, 3, 1, 2, "ReplicaSetUpdated", base+1)))
	rs := replicaSet(hash, revision, base+1)
	rs.Spec.Template.Spec.Containers[0].Image = image + "@" + digest
	if h.serving != "" {
		rs.Spec.Template.Annotations[releaseIDAnnotation] = h.serving
	}
	h.observe(base+1, rs)
	h.observe(base+2, pod(hash, "a", base+1, 0, digest))
	h.observe(base+8, pod(hash, "a", base+1, base+8, digest))
	h.observe(base+9, pod(hash, "b", base+8, 0, digest))
	h.observe(base+13, pod(hash, "b", base+8, base+13, digest))
	h.observe(base+15, dep(deployment(revision, gen, gen, 2, 2, 2, "NewReplicaSetAvailable", base+14)))
}

func (h *harness) rolloutStage(id string) releases.Stage {
	h.t.Helper()
	r, err := h.store.GetRelease(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, st := range r.Stages {
		if st.Key == "rollout" {
			return st
		}
	}
	h.t.Fatalf("%s has no rollout stage", id)
	return releases.Stage{}
}

func TestWatcherAttributesRolloutsBySyncWindow(t *testing.T) {
	h := newHarness(t)
	g1 := strings.Repeat("1", 40)
	g2 := strings.Repeat("2", 40)

	r1 := h.release(11, strings.Repeat("a", 40), g1, digestOf('a'), 0)
	h.sync(g1, 100, 104)
	h.rollout("aaaaaaaaaa", "11", 100, digestOf('a'))
	if st := h.rolloutStage(r1); st.State != releases.StateSucceeded || !st.CompletedAt.Equal(at(114)) {
		t.Fatalf("release 1 rollout %s at %v (%s)", st.State, st.CompletedAt, st.FailureReason)
	}

	// Release 2 rolls out before its sync notification arrives.
	r2 := h.release(12, strings.Repeat("b", 40), g2, digestOf('b'), 150)
	h.rollout("bbbbbbbbbb", "12", 200, digestOf('b'))
	if st := h.rolloutStage(r1); st.State != releases.StateSucceeded {
		t.Fatalf("release 1 reopened: %s (%s)", st.State, st.FailureReason)
	}
	if st := h.rolloutStage(r2); st.State != releases.StatePending {
		t.Fatalf("release 2 rollout before its sync: %s", st.State)
	}

	h.sync(g2, 199, 203)
	st := h.rolloutStage(r2)
	if st.State != releases.StateSucceeded || !st.StartedAt.Equal(at(201)) || !st.CompletedAt.Equal(at(214)) {
		t.Fatalf("release 2 rollout %s from %v to %v (%s)", st.State, st.StartedAt, st.CompletedAt, st.FailureReason)
	}
	var names []string
	for _, s := range st.Steps {
		names = append(names, s.Name)
	}
	if strings.Contains(strings.Join(names, ","), "aaaaaaaaaa") {
		t.Errorf("release 2 shows release 1's pods: %v", names)
	}

	// Seeing the same objects again, as after a restart, stores nothing,
	// and a rebuild gives the same result.
	total, _, _ := h.store.CountEvents(h.ctx)
	same := deployment("12", 250, 250, 2, 2, 2, "NewReplicaSetAvailable", 214)
	same.Spec.Template.Spec.Containers[0].Image = image + "@" + digestOf('b')
	h.observe(5000, same)
	if again, _, _ := h.store.CountEvents(h.ctx); again != total {
		t.Errorf("an unchanged object stored a new event: %d -> %d", total, again)
	}
	if _, err := h.projector.RebuildAll(h.ctx); err != nil {
		t.Fatal(err)
	}
	if st := h.rolloutStage(r1); st.State != releases.StateSucceeded {
		t.Errorf("release 1 after rebuild: %s", st.State)
	}
	if st := h.rolloutStage(r2); st.State != releases.StateSucceeded {
		t.Errorf("release 2 after rebuild: %s", st.State)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestConfigChangeLeavesTheApplicationReleaseUnchanged(t *testing.T) {
	h := newHarness(t)
	g1 := strings.Repeat("1", 40)
	g2 := strings.Repeat("c", 40)

	r1 := h.release(11, strings.Repeat("a", 40), g1, digestOf('a'), 0)
	h.serving = r1
	h.sync(g1, 100, 104)
	h.rollout("aaaaaaaaaa", "11", 100, digestOf('a'))
	before, err := h.store.GetRelease(h.ctx, r1)
	if err != nil || before.Status != releases.StateRunning || before.Stages[5].State != releases.StateSucceeded {
		t.Fatalf("release 1 before the config change: %v %v", before, err)
	}

	// A human edit to the pod template: new pods, same digest and
	// release-id annotation as release 1.
	cfg := h.configSync(g2, 300, 303)
	h.rollout("cccccccccc", "12", 300, digestOf('a'))

	after, err := h.store.GetRelease(h.ctx, r1)
	if err != nil {
		t.Fatal(err)
	}
	if after.Stages[5].State != releases.StateSucceeded || !after.Stages[5].CompletedAt.Equal(*before.Stages[5].CompletedAt) {
		t.Errorf("release 1 rollout changed: %s at %v", after.Stages[5].State, after.Stages[5].CompletedAt)
	}
	for _, s := range h.rolloutStage(r1).Steps {
		if strings.Contains(s.Name, "cccccccccc") {
			t.Errorf("release 1 shows the config rollout's pod %s", s.Name)
		}
	}

	c, err := h.store.GetRelease(h.ctx, cfg)
	if err != nil {
		t.Fatalf("config release: %v", err)
	}
	if c.Kind != releases.KindConfig || c.GitOpsSHA != g2 || c.GitOpsSubject != "Raise personal-site memory limit" {
		t.Errorf("config release %s %s %q", c.Kind, c.GitOpsSHA, c.GitOpsSubject)
	}
	ro := h.rolloutStage(cfg)
	if ro.State != releases.StateSucceeded || c.RuntimeReleaseID != r1 || c.ArtifactDigest != digestOf('a') {
		t.Errorf("config rollout %s (%s), serving %q on %q", ro.State, ro.FailureReason, c.RuntimeReleaseID, c.ArtifactDigest)
	}

	// The verifier checks the config release for release 1's X-Release-ID.
	cands, err := h.store.VerifyCandidates(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cands {
		if c.ReleaseID == cfg {
			found = c.Expected == r1
		}
	}
	if !found {
		t.Errorf("config release not a candidate expecting %s: %+v", r1, cands)
	}

	// A later config commit that changes no pods waits for the no-rollout
	// timer.
	g3 := strings.Repeat("d", 40)
	quiet := h.configSync(g3, 600, 603)
	idle, err := h.store.IdleConfigReleases(h.ctx)
	if err != nil || len(idle) != 1 || idle[0].ReleaseID != quiet || !idle[0].ReconcileStartedAt.Equal(at(600)) ||
		!idle[0].ReconcileDoneAt.Equal(at(603)) {
		t.Errorf("idle config releases %+v %v", idle, err)
	}

	// The same after a rebuild from events.
	if _, err := h.projector.RebuildAll(h.ctx); err != nil {
		t.Fatal(err)
	}
	if st := h.rolloutStage(r1); !st.CompletedAt.Equal(*before.Stages[5].CompletedAt) {
		t.Errorf("release 1 after rebuild: %v", st.CompletedAt)
	}
	if st := h.rolloutStage(cfg); st.State != releases.StateSucceeded {
		t.Errorf("config release after rebuild: %s", st.State)
	}
}

func (h *harness) get(id string) *releases.Release {
	h.t.Helper()
	r, err := h.store.GetRelease(h.ctx, id)
	if err != nil {
		h.t.Fatalf("release %s: %v", id, err)
	}
	return r
}

func stageOf(r *releases.Release, key string) releases.Stage {
	for _, st := range r.Stages {
		if st.Key == key {
			return st
		}
	}
	return releases.Stage{}
}

// TestDeployedTogether covers rule 2 of plan section 8.7: a human commit
// lands on top of a digest bump before Argo CD syncs, so the bump's own
// commit is never synced.
func TestDeployedTogether(t *testing.T) {
	g1 := strings.Repeat("1", 40)
	g2 := strings.Repeat("2", 40) // the bump for release 12
	g3 := strings.Repeat("e", 40) // the human commit on top

	for _, order := range []string{"sync first", "pods first"} {
		t.Run(order, func(t *testing.T) {
			h := newHarness(t)
			r11 := h.release(11, strings.Repeat("a", 40), g1, digestOf('a'), 0)
			h.serving = r11
			h.sync(g1, 100, 104)
			h.rollout("aaaaaaaaaa", "11", 100, digestOf('a'))

			r12 := h.release(12, strings.Repeat("b", 40), g2, digestOf('b'), 150)
			h.serving = r12
			cfg := releases.ConfigReleaseID("personal-site", "production", g3)
			if order == "sync first" {
				h.configSync(g3, 200, 203)
				h.rollout("bbbbbbbbbb", "12", 200, digestOf('b'))
			} else {
				// The runtime watch sees the pods before Argo CD's
				// notification arrives.
				h.rollout("bbbbbbbbbb", "12", 200, digestOf('b'))
				h.configSync(g3, 200, 203)
			}

			app, c := h.get(r12), h.get(cfg)
			rc := stageOf(app, "reconcile")
			if rc.State != releases.StateSucceeded || rc.Summary != "Synced eeeeeee, which includes 2222222" {
				t.Errorf("release 12 reconcile %s %q", rc.State, rc.Summary)
			}
			if ro := stageOf(app, "rollout"); ro.State != releases.StateSucceeded || !ro.StartedAt.Equal(at(201)) {
				t.Errorf("release 12 rollout %s from %v (%s)", ro.State, ro.StartedAt, ro.FailureReason)
			}
			if app.DeployedWith != cfg || c.DeployedWith != r12 {
				t.Errorf("deployed with: release 12 -> %q, config -> %q", app.DeployedWith, c.DeployedWith)
			}
			if ro := stageOf(c, "rollout"); ro.State != releases.StateSucceeded || c.RuntimeReleaseID != r12 || c.Flag != "" {
				t.Errorf("config rollout %s serving %q flag %q", ro.State, c.RuntimeReleaseID, c.Flag)
			}
			if st := stageOf(h.get(r11), "rollout"); st.State != releases.StateSucceeded || !st.CompletedAt.Equal(at(114)) {
				t.Errorf("release 11 changed: %s at %v", st.State, st.CompletedAt)
			}

			// Only the application release is checked; the config release
			// shares its checks.
			cands, err := h.store.VerifyCandidates(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, c := range cands {
				ids = append(ids, c.ReleaseID)
			}
			if slices.Contains(ids, cfg) || !slices.Contains(ids, r12) {
				t.Errorf("verify candidates %v", ids)
			}
			h.provider(events.ProviderVerifier, events.TypeVerifyCheck, 230, events.Keys{ReleaseID: r12}, events.VerifyCheck{
				ReleaseID: r12, CheckedAt: at(230), Result: events.CheckPass, DNSOK: true, TLSOK: true,
				StatusCode: 200, LatencyMS: 90, ObservedReleaseID: r12})
			for _, id := range []string{r12, cfg} {
				if v := stageOf(h.get(id), "verify"); v.State != releases.StateRunning || v.Summary != "1 of 5 consecutive checks passed" {
					t.Errorf("%s verify %s %q", id, v.State, v.Summary)
				}
			}

			if _, err := h.projector.RebuildAll(h.ctx); err != nil {
				t.Fatal(err)
			}
			if a := h.get(r12); a.DeployedWith != cfg || stageOf(a, "rollout").State != releases.StateSucceeded {
				t.Errorf("after rebuild: release 12 deployed with %q, rollout %s", a.DeployedWith, stageOf(a, "rollout").State)
			}
		})
	}
}

// TestBumpCarriedByAnotherServicesCommit: the commit on top is another
// service's deploy commit, which has a Release-Id trailer and so names no
// config release. The bump's release still takes the sync and the rollout.
func TestBumpCarriedByAnotherServicesCommit(t *testing.T) {
	h := newHarness(t)
	g2 := strings.Repeat("2", 40)
	g3 := strings.Repeat("f", 40)
	r12 := h.release(12, strings.Repeat("b", 40), g2, digestOf('b'), 150)
	h.serving = r12
	h.sync(g3, 200, 203)
	h.rollout("bbbbbbbbbb", "12", 200, digestOf('b'))

	app := h.get(r12)
	if st := stageOf(app, "rollout"); st.State != releases.StateSucceeded || app.DeployedWith != "" {
		t.Errorf("release 12 rollout %s, deployed with %q", st.State, app.DeployedWith)
	}
	if st := stageOf(app, "reconcile"); st.Summary != "Synced fffffff, which includes 2222222" {
		t.Errorf("reconcile %q", st.Summary)
	}
}

// TestOlderBumpIsNotAdopted: two bumps land before Argo CD syncs the newer
// one, whose pods carry release 13. Release 12 never ran, so it keeps
// waiting at Reconcile, even when a later config change still carries 13.
func TestOlderBumpIsNotAdopted(t *testing.T) {
	h := newHarness(t)
	g2 := strings.Repeat("2", 40)
	g3 := strings.Repeat("3", 40)
	r12 := h.release(12, strings.Repeat("b", 40), g2, digestOf('b'), 150)
	r13 := h.release(13, strings.Repeat("c", 40), g3, digestOf('c'), 160)
	h.serving = r13
	h.sync(g3, 200, 203)
	h.rollout("cccccccccc", "13", 200, digestOf('c'))
	h.configSync(strings.Repeat("d", 40), 400, 403)
	h.rollout("dddddddddd", "14", 400, digestOf('c'))

	if st := stageOf(h.get(r12), "reconcile"); st.State != releases.StatePending {
		t.Errorf("release 12 reconcile %s (%q)", st.State, st.Summary)
	}
	if a := h.get(r13); stageOf(a, "rollout").State != releases.StateSucceeded || a.DeployedWith != "" {
		t.Errorf("release 13 rollout %s deployed with %q", stageOf(a, "rollout").State, a.DeployedWith)
	}
}

func TestHandEditIsFlagged(t *testing.T) {
	h := newHarness(t)
	g1 := strings.Repeat("1", 40)
	g2 := strings.Repeat("2", 40)
	r11 := h.release(11, strings.Repeat("a", 40), g1, digestOf('a'), 0)
	r12 := h.release(12, strings.Repeat("b", 40), g2, digestOf('b'), 50)
	h.serving = r12
	h.sync(g2, 100, 104)
	h.rollout("bbbbbbbbbb", "12", 100, digestOf('b'))

	// An env var edit: same release and digest, not flagged.
	env := h.configSync(strings.Repeat("c", 40), 300, 303)
	h.rollout("cccccccccc", "13", 300, digestOf('b'))
	if f := h.get(env).Flag; f != "" {
		t.Errorf("env var edit flagged: %q", f)
	}

	// A hand rollback to release 11's digest and annotation.
	h.serving = r11
	back := h.configSync(strings.Repeat("d", 40), 500, 503)
	h.rollout("dddddddddd", "14", 500, digestOf('a'))
	want := "A commit without a CI release moved the pods from " + r12 + " to " + r11
	if f := h.get(back).Flag; f != want {
		t.Errorf("flag %q, want %q", f, want)
	}
	// Release 11 never synced its own commit and does not take the rollback.
	if st := stageOf(h.get(r11), "rollout"); st.State != releases.StatePending {
		t.Errorf("release 11 rollout %s", st.State)
	}
}
