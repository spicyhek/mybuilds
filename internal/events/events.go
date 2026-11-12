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
)

// Event types.
const (
	TypeWorkflowRun      = "github.workflow_run"
	TypeWorkflowJob      = "github.workflow_job"
	TypeReleasePublished = "ci.release.published"
	TypeGitOpsUpdated    = "ci.gitops.updated"
	TypeGitOpsSuperseded = "ci.gitops.superseded"
	TypeArgoCD           = "argocd.notification"
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
