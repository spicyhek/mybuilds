package events

import (
	"encoding/json"
	"fmt"
	"time"
)

// Record is the exported form of a stored event: what
// GET /api/v1/releases/{id}/events returns and what a recorded fixture
// holds, one per line (plan section 7.5). Ingestion already keeps only
// allowlisted fields; NewRecord re-encodes the payload through its type as
// well, so a field stored by an older version cannot reach the export. The
// body hash is left out.
type Record struct {
	ID         int64           `json:"id"`
	Provider   string          `json:"provider"`
	DeliveryID string          `json:"delivery_id"`
	Type       string          `json:"type"`
	Attempt    int             `json:"attempt"`
	OccurredAt time.Time       `json:"occurred_at"`
	ReceivedAt time.Time       `json:"received_at"`
	Keys       RecordKeys      `json:"keys"`
	Payload    json.RawMessage `json:"payload"`
}

// RecordKeys are an event's correlation keys.
type RecordKeys struct {
	ReleaseID string `json:"release_id,omitempty"`
	RunID     int64  `json:"run_id,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Workload  string `json:"workload,omitempty"`
}

// payloadTypes maps each event type to a new value of its payload type.
var payloadTypes = map[string]func() any{
	TypeWorkflowRun:      func() any { return new(WorkflowRun) },
	TypeWorkflowJob:      func() any { return new(WorkflowJob) },
	TypeReleasePublished: func() any { return new(ReleasePublished) },
	TypeGitOpsUpdated:    func() any { return new(GitOpsUpdated) },
	TypeGitOpsSuperseded: func() any { return new(GitOpsSuperseded) },
	TypeArgoCD:           func() any { return new(ArgoCDNotification) },
	TypeDeployment:       func() any { return new(Deployment) },
	TypeReplicaSet:       func() any { return new(ReplicaSet) },
	TypePod:              func() any { return new(Pod) },
	TypeVerifyCheck:      func() any { return new(VerifyCheck) },
	TypeVerifyEnded:      func() any { return new(VerifyEnded) },
	TypeNoRollout:        func() any { return new(NoRollout) },
}

// NewRecord exports an event. An event of an unknown type is an error
// rather than an unfiltered payload.
func NewRecord(e Event) (Record, error) {
	newPayload, ok := payloadTypes[e.Type]
	if !ok {
		return Record{}, fmt.Errorf("event %d: unknown type %q", e.ID, e.Type)
	}
	v := newPayload()
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return Record{}, fmt.Errorf("event %d: %w", e.ID, err)
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return Record{}, fmt.Errorf("event %d: %w", e.ID, err)
	}
	return Record{
		ID:         e.ID,
		Provider:   e.Provider,
		DeliveryID: e.DeliveryID,
		Type:       e.Type,
		Attempt:    e.Attempt,
		OccurredAt: e.OccurredAt,
		ReceivedAt: e.ReceivedAt,
		Keys: RecordKeys{
			ReleaseID: e.Keys.ReleaseID,
			RunID:     e.Keys.RunID,
			Revision:  e.Keys.Revision,
			Workload:  e.Keys.Workload,
		},
		Payload: payload,
	}, nil
}

// Event turns a record back into an event, for loading a fixture into an
// empty store. The body hash is not recorded, so it is empty.
func (r Record) Event() Event {
	return Event{
		ID:         r.ID,
		Provider:   r.Provider,
		DeliveryID: r.DeliveryID,
		Type:       r.Type,
		Attempt:    r.Attempt,
		OccurredAt: r.OccurredAt,
		ReceivedAt: r.ReceivedAt,
		Keys: Keys{
			ReleaseID: r.Keys.ReleaseID,
			RunID:     r.Keys.RunID,
			Revision:  r.Keys.Revision,
			Workload:  r.Keys.Workload,
		},
		Payload: r.Payload,
	}
}
