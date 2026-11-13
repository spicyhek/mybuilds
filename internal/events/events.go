// Package events defines the normalized events that ingestion stores and the
// projector reads. See the plan, sections 5.4, 8 and 12.
//
// Payloads are built from an allowlist of provider fields. Raw request bodies
// are hashed, never stored, and log URLs, runner IDs and similar fields are
// dropped at translation time.
package events

import (
	"encoding/json"
	"time"
)

// Providers.
const (
	ProviderGitHub = "github"
	ProviderCI     = "ci"
	ProviderArgoCD = "argocd"
	// ProviderKubernetes is the runtime watch, not a webhook (plan section
	// 8.4).
	ProviderKubernetes = "kubernetes"
	// ProviderVerifier is mybuilds' own public check (plan section 8.5).
	ProviderVerifier = "verifier"
)

// Event types.
const (
	TypeWorkflowRun      = "github.workflow_run"
	TypeWorkflowJob      = "github.workflow_job"
	TypeReleasePublished = "ci.release.published"
	TypeGitOpsUpdated    = "ci.gitops.updated"
	TypeGitOpsSuperseded = "ci.gitops.superseded"
	TypeArgoCD           = "argocd.notification"
	TypeDeployment       = "k8s.deployment"
	TypeReplicaSet       = "k8s.replicaset"
	TypePod              = "k8s.pod"
	TypeVerifyCheck      = "verifier.check"
	TypeVerifyEnded      = "verifier.ended"
	TypeNoRollout        = "verifier.no_rollout"
)

// Event is one stored observation.
type Event struct {
	ID         int64
	Provider   string
	DeliveryID string
	Type       string
	Attempt    int
	OccurredAt time.Time
	ReceivedAt time.Time
	BodySHA256 string
	Keys       Keys
	// Payload is one of the payload types below, encoded as JSON.
	Payload json.RawMessage
}

// Keys are the correlation keys of an event. The projector finds a release's
// events through them (plan section 8, correlation table).
type Keys struct {
	// ReleaseID is set when the event names its release directly: CI events,
	// and workflow runs of a workflow in the service map.
	ReleaseID string
	// RunID is the GitHub Actions run ID. It links workflow_job events, which
	// carry no release ID, to the release of their run.
	RunID int64
	// Revision is a GitOps commit SHA. It links Argo CD syncs to the release
	// whose gitops.updated event reported that commit.
	Revision string
	// Workload is the Deployment a runtime event describes, as
	// namespace/name. The projector reads a workload's events between the
	// Argo CD sync of a release's GitOps commit and the next sync.
	Workload string
}

// Decode unmarshals the payload into v.
func (e Event) Decode(v any) error {
	return json.Unmarshal(e.Payload, v)
}

// WorkflowRun is a GitHub workflow_run delivery.
type WorkflowRun struct {
	Action       string    `json:"action"`
	RunID        int64     `json:"run_id"`
	RunNumber    int       `json:"run_number"`
	RunAttempt   int       `json:"run_attempt"`
	Repository   string    `json:"repository"`
	WorkflowPath string    `json:"workflow_path"`
	WorkflowName string    `json:"workflow_name"`
	HeadSHA      string    `json:"head_sha"`
	HeadBranch   string    `json:"head_branch"`
	Event        string    `json:"event"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	RunStartedAt time.Time `json:"run_started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	// Service and Environment come from the service map at ingestion time.
	Service     string `json:"service,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// WorkflowJob is a GitHub workflow_job delivery. One completed delivery
// carries every step of the job.
type WorkflowJob struct {
	Action       string     `json:"action"`
	RunID        int64      `json:"run_id"`
	RunAttempt   int        `json:"run_attempt"`
	JobID        int64      `json:"job_id"`
	JobName      string     `json:"job_name"`
	WorkflowName string     `json:"workflow_name"`
	Repository   string     `json:"repository"`
	HeadSHA      string     `json:"head_sha"`
	Status       string     `json:"status"`
	Conclusion   string     `json:"conclusion,omitempty"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	// Labels are the runner labels, such as ubuntu-latest. Runner IDs and
	// names are dropped.
	Labels []string  `json:"labels,omitempty"`
	Steps  []JobStep `json:"steps,omitempty"`
}

// JobStep is one step of a workflow job.
type JobStep struct {
	Number      int        `json:"number"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// ReleasePublished is the CI-signed release manifest, sent after the image is
// pushed and its digest resolved (plan section 8.2).
type ReleasePublished struct {
	Type              string    `json:"type"`
	ReleaseID         string    `json:"release_id"`
	Service           string    `json:"service"`
	Environment       string    `json:"environment"`
	SourceRepository  string    `json:"source_repository"`
	SourceSHA         string    `json:"source_sha"`
	Artifact          string    `json:"artifact"`
	ArtifactDigest    string    `json:"artifact_digest"`
	WorkflowRunID     int64     `json:"workflow_run_id"`
	WorkflowRunNumber int       `json:"workflow_run_number"`
	RunAttempt        int       `json:"run_attempt"`
	Drill             bool      `json:"drill"`
	DrillScenario     string    `json:"drill_scenario,omitempty"`
	OccurredAt        time.Time `json:"occurred_at"`
}

// GitOpsUpdated is sent by the bump job after the digest bump is merged.
// GitOpsSHA is the squash commit on the GitOps repository's main branch.
type GitOpsUpdated struct {
	Type             string    `json:"type"`
	ReleaseID        string    `json:"release_id"`
	Service          string    `json:"service"`
	Environment      string    `json:"environment"`
	GitOpsRepository string    `json:"gitops_repository"`
	GitOpsSHA        string    `json:"gitops_sha"`
	WorkflowRunID    int64     `json:"workflow_run_id"`
	RunAttempt       int       `json:"run_attempt"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// GitOpsSuperseded is sent by the bump job when it skips the promotion
// because a newer commit is already on the source branch.
type GitOpsSuperseded struct {
	Type          string    `json:"type"`
	ReleaseID     string    `json:"release_id"`
	Service       string    `json:"service"`
	Environment   string    `json:"environment"`
	SupersededBy  string    `json:"superseded_by,omitempty"`
	WorkflowRunID int64     `json:"workflow_run_id"`
	RunAttempt    int       `json:"run_attempt"`
	OccurredAt    time.Time `json:"occurred_at"`
}

// Argo CD notification triggers.
const (
	TriggerSyncRunning    = "on-sync-running"
	TriggerSyncSucceeded  = "on-sync-succeeded"
	TriggerSyncFailed     = "on-sync-failed"
	TriggerHealthDegraded = "on-health-degraded"
)

// ArgoCDNotification is one Argo CD Notifications webhook.
type ArgoCDNotification struct {
	Trigger        string     `json:"trigger"`
	Application    string     `json:"application"`
	Revision       string     `json:"revision"`
	SyncStatus     string     `json:"sync_status,omitempty"`
	HealthStatus   string     `json:"health_status,omitempty"`
	OperationPhase string     `json:"operation_phase,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	// From the synced commit's metadata (plan section 8.7). Only the subject
	// line and the Release-Id trailer are kept, never the full message.
	CommitSubject   string `json:"commit_subject,omitempty"`
	CommitReleaseID string `json:"commit_release_id,omitempty"`
	CommitAuthor    string `json:"commit_author,omitempty"`
}

// Kubernetes runtime payloads (plan section 8.4). Each is a snapshot of the
// fields the Rollout stage needs. Node names, pod IPs, environment, volumes
// and container messages are never copied.

// Deployment is a snapshot of a managed Deployment.
type Deployment struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	// Revision is the deployment.kubernetes.io/revision annotation: the
	// revision of its newest ReplicaSet.
	Revision           string `json:"revision,omitempty"`
	Generation         int64  `json:"generation"`
	ObservedGeneration int64  `json:"observed_generation"`
	// Replicas is the desired count; the others are from status.
	Replicas            int32       `json:"replicas"`
	StatusReplicas      int32       `json:"status_replicas"`
	UpdatedReplicas     int32       `json:"updated_replicas"`
	ReadyReplicas       int32       `json:"ready_replicas"`
	AvailableReplicas   int32       `json:"available_replicas"`
	UnavailableReplicas int32       `json:"unavailable_replicas"`
	Images              []string    `json:"images,omitempty"`
	ReleaseID           string      `json:"release_id,omitempty"`
	Conditions          []Condition `json:"conditions,omitempty"`
	Deleted             bool        `json:"deleted,omitempty"`
}

// Condition is a Deployment condition without its free-text message.
type Condition struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	LastUpdateTime     *time.Time `json:"last_update_time,omitempty"`
	LastTransitionTime *time.Time `json:"last_transition_time,omitempty"`
}

// ReplicaSet is a snapshot of a ReplicaSet owned by a managed Deployment.
type ReplicaSet struct {
	Namespace         string    `json:"namespace"`
	Name              string    `json:"name"`
	UID               string    `json:"uid"`
	Deployment        string    `json:"deployment"`
	Revision          string    `json:"revision,omitempty"`
	PodTemplateHash   string    `json:"pod_template_hash,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	Replicas          int32     `json:"replicas"`
	StatusReplicas    int32     `json:"status_replicas"`
	ReadyReplicas     int32     `json:"ready_replicas"`
	AvailableReplicas int32     `json:"available_replicas"`
	Images            []string  `json:"images,omitempty"`
	ReleaseID         string    `json:"release_id,omitempty"`
	Deleted           bool      `json:"deleted,omitempty"`
}

// Pod is a snapshot of a pod of a managed Deployment.
type Pod struct {
	Namespace       string    `json:"namespace"`
	Name            string    `json:"name"`
	UID             string    `json:"uid"`
	ReplicaSet      string    `json:"replica_set,omitempty"`
	PodTemplateHash string    `json:"pod_template_hash,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	Phase           string    `json:"phase,omitempty"`
	Ready           bool      `json:"ready"`
	// ReadyChangedAt is the last transition of the Ready condition.
	ReadyChangedAt *time.Time        `json:"ready_changed_at,omitempty"`
	Containers     []ContainerStatus `json:"containers,omitempty"`
	Deleted        bool              `json:"deleted,omitempty"`
}

// ContainerStatus is one container of a pod.
type ContainerStatus struct {
	Name string `json:"name"`
	// Image is the image reference from the pod spec; ImageDigest is the
	// sha256 digest the kubelet reports in imageID.
	Image        string `json:"image"`
	ImageDigest  string `json:"image_digest,omitempty"`
	Ready        bool   `json:"ready"`
	RestartCount int32  `json:"restart_count"`
	// State is waiting, running or terminated; Reason is its reason, such as
	// ContainerCreating, ImagePullBackOff or CrashLoopBackOff.
	State     string     `json:"state,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// Verification check results.
const (
	// CheckPass counts toward the five consecutive passes.
	CheckPass = "pass"
	// CheckMismatch is a response with another release's X-Release-ID, such
	// as from an old replica. It resets the count.
	CheckMismatch = "mismatch"
	// CheckUnhealthy is a health body that is not healthy or cannot be
	// parsed. It resets the count.
	CheckUnhealthy = "unhealthy"
	// CheckFail is any other failure: DNS, TLS, connection, timeout, status
	// code or latency. It fails Verify.
	CheckFail = "fail"
)

// VerifyCheck is one public check of a release (plan section 8.5). The
// result is decided when the check runs, against the service map of that
// time, so replaying it gives the same Verify outcome.
type VerifyCheck struct {
	ReleaseID string `json:"release_id"`
	// ExpectedReleaseID is the X-Release-ID the check expected when it is
	// not ReleaseID: a config release's pods serve an application release.
	ExpectedReleaseID string    `json:"expected_release_id,omitempty"`
	Endpoint          string    `json:"endpoint"`
	CheckedAt         time.Time `json:"checked_at"`
	Result            string    `json:"result"`
	// Reason explains a result other than pass. It is built from the fields
	// below, never copied from an error message.
	Reason string `json:"reason,omitempty"`

	DNSOK        bool       `json:"dns_ok"`
	TLSOK        bool       `json:"tls_ok"`
	TLSVersion   string     `json:"tls_version,omitempty"`
	CertNotAfter *time.Time `json:"cert_not_after,omitempty"`
	StatusCode   int        `json:"status_code,omitempty"`
	LatencyMS    int64      `json:"latency_ms,omitempty"`
	// ObservedReleaseID is the response's X-Release-ID header.
	ObservedReleaseID string `json:"observed_release_id,omitempty"`
	// Colo is the Cloudflare location from the cf-ray header, such as LAX.
	Colo        string `json:"colo,omitempty"`
	CacheStatus string `json:"cache_status,omitempty"`
	// HealthOK is the health-body check's result, nil when the service has
	// none. The body itself is not stored.
	HealthOK *bool `json:"health_ok,omitempty"`
}

// Reasons a verification window ends without five consecutive passes.
const (
	EndExpired    = "expired"
	EndSuperseded = "superseded"
)

// VerifyEnded closes a release's verification window.
type VerifyEnded struct {
	ReleaseID  string    `json:"release_id"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
}

// NoRollout records that a config release's sync replaced no pods: the
// runtime watch was running throughout and saw no change to the workload
// within the grace period after the sync finished.
type NoRollout struct {
	ReleaseID    string    `json:"release_id"`
	GraceSeconds int       `json:"grace_seconds"`
	OccurredAt   time.Time `json:"occurred_at"`
}
