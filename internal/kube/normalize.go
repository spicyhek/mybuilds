// Package kube is the read-only runtime watch (plan section 8.4): informers on
// the Deployments, ReplicaSets and Pods labeled
// mybuilds.brendanmanley.com/managed=true, whose changes become stored events
// for the Rollout stage.
package kube

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/spicyhek/mybuilds/internal/events"
)

// Labels and annotations.
const (
	ManagedLabel        = "mybuilds.brendanmanley.com/managed"
	releaseIDAnnotation = "mybuilds.brendanmanley.com/release-id"
	revisionAnnotation  = "deployment.kubernetes.io/revision"
	podTemplateHash     = "pod-template-hash"
)

// DeploymentEvent normalizes a Deployment.
func DeploymentEvent(d *appsv1.Deployment, deleted bool, now time.Time) (*events.Event, error) {
	p := events.Deployment{
		Namespace:           d.Namespace,
		Name:                d.Name,
		UID:                 string(d.UID),
		Revision:            d.Annotations[revisionAnnotation],
		Generation:          d.Generation,
		ObservedGeneration:  d.Status.ObservedGeneration,
		Replicas:            replicas(d.Spec.Replicas),
		StatusReplicas:      d.Status.Replicas,
		UpdatedReplicas:     d.Status.UpdatedReplicas,
		ReadyReplicas:       d.Status.ReadyReplicas,
		AvailableReplicas:   d.Status.AvailableReplicas,
		UnavailableReplicas: d.Status.UnavailableReplicas,
		Images:              images(d.Spec.Template.Spec.Containers),
		ReleaseID:           d.Spec.Template.Annotations[releaseIDAnnotation],
		Deleted:             deleted,
	}
	for _, c := range d.Status.Conditions {
		p.Conditions = append(p.Conditions, events.Condition{
			Type:               string(c.Type),
			Status:             string(c.Status),
			Reason:             c.Reason,
			LastUpdateTime:     metaTime(c.LastUpdateTime),
			LastTransitionTime: metaTime(c.LastTransitionTime),
		})
	}
	sort.Slice(p.Conditions, func(i, j int) bool { return p.Conditions[i].Type < p.Conditions[j].Type })
	return newEvent(events.TypeDeployment, p.UID, d.Namespace+"/"+d.Name, p, now)
}

// ReplicaSetEvent normalizes a ReplicaSet. It returns nil for one that no
// Deployment owns.
func ReplicaSetEvent(rs *appsv1.ReplicaSet, deleted bool, now time.Time) (*events.Event, error) {
	owner := ownerName(rs.OwnerReferences, "Deployment")
	if owner == "" {
		return nil, nil
	}
	p := events.ReplicaSet{
		Namespace:         rs.Namespace,
		Name:              rs.Name,
		UID:               string(rs.UID),
		Deployment:        owner,
		Revision:          rs.Annotations[revisionAnnotation],
		PodTemplateHash:   rs.Labels[podTemplateHash],
		CreatedAt:         rs.CreationTimestamp.UTC(),
		Replicas:          replicas(rs.Spec.Replicas),
		StatusReplicas:    rs.Status.Replicas,
		ReadyReplicas:     rs.Status.ReadyReplicas,
		AvailableReplicas: rs.Status.AvailableReplicas,
		Images:            images(rs.Spec.Template.Spec.Containers),
		ReleaseID:         rs.Spec.Template.Annotations[releaseIDAnnotation],
		Deleted:           deleted,
	}
	return newEvent(events.TypeReplicaSet, p.UID, rs.Namespace+"/"+owner, p, now)
}

// PodEvent normalizes a pod. It returns nil for a pod that no ReplicaSet of
// a Deployment owns. Node names and IPs are not copied.
func PodEvent(pod *corev1.Pod, deleted bool, now time.Time) (*events.Event, error) {
	rs := ownerName(pod.OwnerReferences, "ReplicaSet")
	hash := pod.Labels[podTemplateHash]
	if rs == "" || hash == "" || !strings.HasSuffix(rs, "-"+hash) {
		return nil, nil
	}
	deployment := strings.TrimSuffix(rs, "-"+hash)

	p := events.Pod{
		Namespace:       pod.Namespace,
		Name:            pod.Name,
		UID:             string(pod.UID),
		ReplicaSet:      rs,
		PodTemplateHash: hash,
		CreatedAt:       pod.CreationTimestamp.UTC(),
		Phase:           string(pod.Status.Phase),
		Deleted:         deleted || pod.DeletionTimestamp != nil,
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			p.Ready = c.Status == corev1.ConditionTrue
			p.ReadyChangedAt = metaTime(c.LastTransitionTime)
		}
	}
	specImages := map[string]string{}
	for _, c := range pod.Spec.Containers {
		specImages[c.Name] = c.Image
	}
	for _, cs := range pod.Status.ContainerStatuses {
		c := events.ContainerStatus{
			Name:         cs.Name,
			Image:        specImages[cs.Name],
			ImageDigest:  imageDigest(cs.ImageID),
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
		}
		if c.Image == "" {
			c.Image = cs.Image
		}
		switch s := cs.State; {
		case s.Waiting != nil:
			c.State, c.Reason = "waiting", s.Waiting.Reason
		case s.Running != nil:
			c.State, c.StartedAt = "running", metaTime(s.Running.StartedAt)
		case s.Terminated != nil:
			c.State, c.Reason = "terminated", s.Terminated.Reason
		}
		p.Containers = append(p.Containers, c)
	}
	sort.Slice(p.Containers, func(i, j int) bool { return p.Containers[i].Name < p.Containers[j].Name })
	return newEvent(events.TypePod, p.UID, pod.Namespace+"/"+deployment, p, now)
}

// newEvent wraps a payload. The delivery ID is the object's UID and a hash of
// the payload, so an unchanged object seen again (a resync, or the initial
// list after a restart) is a duplicate and stores nothing.
func newEvent(typ, uid, workload string, payload any, now time.Time) (*events.Event, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", typ, err)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	return &events.Event{
		Provider:   events.ProviderKubernetes,
		DeliveryID: fmt.Sprintf("%s/%s/%s", typ, uid, hash[:32]),
		Type:       typ,
		Attempt:    1,
		OccurredAt: now.UTC(),
		ReceivedAt: now.UTC(),
		BodySHA256: hash,
		Keys:       events.Keys{Workload: workload},
		Payload:    data,
	}, nil
}

// imageDigest extracts sha256:<hex> from a kubelet imageID, such as
// ghcr.io/owner/app@sha256:... or docker-pullable://...@sha256:....
func imageDigest(imageID string) string {
	if i := strings.LastIndex(imageID, "sha256:"); i >= 0 {
		return imageID[i:]
	}
	return ""
}

func images(cs []corev1.Container) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Image)
	}
	return out
}

func ownerName(refs []metav1.OwnerReference, kind string) string {
	for _, r := range refs {
		if r.Kind == kind && r.Controller != nil && *r.Controller {
			return r.Name
		}
	}
	return ""
}

func replicas(n *int32) int32 {
	if n == nil {
		return 1
	}
	return *n
}

func metaTime(t metav1.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
