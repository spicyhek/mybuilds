// Package releases holds the release model shared by storage, the projector
// and the API. See the plan, sections 5 and 12.
package releases

import (
	"crypto/rand"
	"fmt"
	"time"
)

// Kind tells an application release (a new artifact built by CI) from a
// config release (a human commit to the GitOps repository).
type Kind string

const (
	KindApplication Kind = "application"
	KindConfig      Kind = "config"
)

// State is the state of a release, a stage or a step.
type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateDegraded  State = "degraded"
	StateSkipped   State = "skipped"
	StateUnknown   State = "unknown"
)

// Pipeline templates and their stage keys, in order.
const (
	TemplateApplication = "application"
	TemplateConfig      = "config"
)

var (
	ApplicationStages = []string{"source", "build", "publish", "gitops", "reconcile", "rollout", "verify"}
	ConfigStages      = []string{"gitops_commit", "reconcile", "rollout", "verify"}
)

// Release is one attempt to move an immutable artifact into an environment.
// Empty strings mean "not known"; they are stored as NULL.
type Release struct {
	UUID        string `json:"uuid,omitempty"`
	ReleaseID   string `json:"release_id"`
	Service     string `json:"service"`
	Environment string `json:"environment"`
	Kind        Kind   `json:"kind"`
	Template    string `json:"template"`

	// DeployedWith is the UUID of a release that shares this release's
	// Rollout and Verify evidence (plan section 8.7).
	DeployedWith  string `json:"deployed_with,omitempty"`
	Drill         bool   `json:"drill"`
	DrillScenario string `json:"drill_scenario,omitempty"`
	// Superseded marks a release whose promotion was skipped because a newer
	// commit was already on the source branch. It is not a failure.
	Superseded bool `json:"superseded"`

	SourceRepository string `json:"source_repository,omitempty"`
	SourceSHA        string `json:"source_sha,omitempty"`
	Artifact         string `json:"artifact,omitempty"`
	ArtifactDigest   string `json:"artifact_digest,omitempty"`
	GitOpsRepository string `json:"gitops_repository,omitempty"`
	GitOpsSHA        string `json:"gitops_sha,omitempty"`
	// GitOpsSubject and GitOpsAuthor describe a config release's commit,
	// from Argo CD's commit metadata. Only the subject line is kept.
	GitOpsSubject string `json:"gitops_subject,omitempty"`
	GitOpsAuthor  string `json:"gitops_author,omitempty"`
	// RuntimeReleaseID is the release-id annotation of the pods the rollout
	// started. A config release's pods carry the application release they
	// run, and its verification expects that X-Release-ID.
	RuntimeReleaseID string `json:"runtime_release_id,omitempty"`

	Status      State      `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	VerifiedAt  *time.Time `json:"verified_at,omitempty"`

	Stages []Stage `json:"stages"`
}

// Stage is one stage of a release's pipeline.
type Stage struct {
	Key           string     `json:"key"`
	Position      int        `json:"position"`
	State         State      `json:"state"`
	Attempt       int        `json:"attempt"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	FailureReason string     `json:"failure_reason,omitempty"`
	// Steps of the stage's current attempt. Loaded for a single release only.
	Steps []Step `json:"steps,omitempty"`
}

// Step is one step inside a stage (plan section 5.5).
type Step struct {
	StageKey    string     `json:"-"`
	Attempt     int        `json:"attempt"`
	Position    int        `json:"position"`
	Name        string     `json:"name"`
	State       State      `json:"state"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// Detail is built from structured fields, never copied from logs.
	Detail string `json:"detail,omitempty"`
	// Reconstructed steps were rebuilt from object timestamps (section 8.8).
	Reconstructed bool  `json:"reconstructed"`
	SourceEventID int64 `json:"-"`
}

// Terminal reports whether a state is final for its attempt.
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateDegraded, StateSkipped:
		return true
	}
	return false
}

// NewUUID returns a random version 4 UUID.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
