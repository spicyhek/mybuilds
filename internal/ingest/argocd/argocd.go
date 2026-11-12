// Package argocd translates Argo CD Notifications webhooks (plan section
// 8.3). The payload is produced by the webhook template documented in
// docs/ingestion.md.
package argocd

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type payload struct {
	Trigger        string `json:"trigger"`
	Application    string `json:"application"`
	Revision       string `json:"revision"`
	SyncStatus     string `json:"sync_status"`
	HealthStatus   string `json:"health_status"`
	OperationPhase string `json:"operation_phase"`
	// Argo CD templates render missing times as empty strings.
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at"`
	CommitMessage string `json:"commit_message"`
	CommitAuthor  string `json:"commit_author"`
}

// Translate validates a notification and turns it into an event. Argo CD
// sends no delivery ID, so the body hash is used: an identical notification
// is the same observation.
func Translate(body []byte, bodySHA256 string) (*events.Event, error) {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("decode Argo CD notification: %w", err)
	}
	switch p.Trigger {
	case events.TriggerSyncRunning, events.TriggerSyncSucceeded, events.TriggerSyncFailed, events.TriggerHealthDegraded:
	default:
		return nil, fmt.Errorf("unknown trigger %q", p.Trigger)
	}
	if p.Application == "" {
		return nil, errors.New("application is required")
	}
	if !fullSHA.MatchString(p.Revision) {
		return nil, errors.New("revision must be a full commit SHA")
	}
	started, err := optionalTime("started_at", p.StartedAt)
	if err != nil {
		return nil, err
	}
	finished, err := optionalTime("finished_at", p.FinishedAt)
	if err != nil {
		return nil, err
	}

	subject, releaseID := commitFields(p.CommitMessage)
	out := events.ArgoCDNotification{
		Trigger:         p.Trigger,
		Application:     p.Application,
		Revision:        p.Revision,
		SyncStatus:      p.SyncStatus,
		HealthStatus:    p.HealthStatus,
		OperationPhase:  p.OperationPhase,
		StartedAt:       started,
		FinishedAt:      finished,
		CommitSubject:   subject,
		CommitReleaseID: releaseID,
		CommitAuthor:    p.CommitAuthor,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}

	var occurred time.Time
	switch {
	case finished != nil && p.Trigger != events.TriggerSyncRunning:
		occurred = *finished
	case started != nil:
		occurred = *started
	}
	return &events.Event{
		DeliveryID: bodySHA256,
		Type:       events.TypeArgoCD,
		Attempt:    1,
		OccurredAt: occurred,
		Keys:       events.Keys{Revision: p.Revision},
		Payload:    data,
	}, nil
}

func optionalTime(field, s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	// Templates render a missing time as "", "<nil>" or "<no value>".
	if s == "" || s == "<nil>" || s == "<no value>" || strings.HasPrefix(s, "0001-01-01") {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	t = t.UTC()
	return &t, nil
}

// commitFields keeps the subject line and the Release-Id trailer of a commit
// message. The rest of the message is never stored.
func commitFields(message string) (subject, releaseID string) {
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	subject = strings.TrimSpace(lines[0])
	if len(subject) > 200 {
		subject = subject[:200]
	}
	for _, line := range lines[1:] {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Release-Id:"); ok {
			releaseID = strings.TrimSpace(v)
		}
	}
	return subject, releaseID
}
