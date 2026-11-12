// Package github translates GitHub workflow_run and workflow_job webhooks
// into normalized events (plan section 8.1).
package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
)

// Request headers set by GitHub.
const (
	SignatureHeader = "X-Hub-Signature-256"
	EventHeader     = "X-GitHub-Event"
	DeliveryHeader  = "X-GitHub-Delivery"
)

type repository struct {
	FullName string `json:"full_name"`
}

type workflowRunPayload struct {
	Action      string     `json:"action"`
	Repository  repository `json:"repository"`
	WorkflowRun *struct {
		ID           int64      `json:"id"`
		Name         string     `json:"name"`
		Path         string     `json:"path"`
		RunNumber    int        `json:"run_number"`
		RunAttempt   int        `json:"run_attempt"`
		Event        string     `json:"event"`
		Status       string     `json:"status"`
		Conclusion   *string    `json:"conclusion"`
		HeadSHA      string     `json:"head_sha"`
		HeadBranch   string     `json:"head_branch"`
		CreatedAt    time.Time  `json:"created_at"`
		RunStartedAt *time.Time `json:"run_started_at"`
		UpdatedAt    time.Time  `json:"updated_at"`
	} `json:"workflow_run"`
}

type workflowJobPayload struct {
	Action      string     `json:"action"`
	Repository  repository `json:"repository"`
	WorkflowJob *struct {
		ID           int64      `json:"id"`
		RunID        int64      `json:"run_id"`
		RunAttempt   int        `json:"run_attempt"`
		Name         string     `json:"name"`
		WorkflowName string     `json:"workflow_name"`
		HeadSHA      string     `json:"head_sha"`
		Status       string     `json:"status"`
		Conclusion   *string    `json:"conclusion"`
		CreatedAt    *time.Time `json:"created_at"`
		StartedAt    *time.Time `json:"started_at"`
		CompletedAt  *time.Time `json:"completed_at"`
		Labels       []string   `json:"labels"`
		Steps        []struct {
			Name        string     `json:"name"`
			Number      int        `json:"number"`
			Status      string     `json:"status"`
			Conclusion  *string    `json:"conclusion"`
			StartedAt   *time.Time `json:"started_at"`
			CompletedAt *time.Time `json:"completed_at"`
		} `json:"steps"`
	} `json:"workflow_job"`
}

// Translate turns an authenticated webhook into an event. A nil event with a
// reason means the delivery is valid but not tracked, such as a ping or a
// pull request run.
func Translate(header http.Header, body []byte, svcs services.Map) (*events.Event, string, error) {
	delivery := header.Get(DeliveryHeader)
	if delivery == "" {
		return nil, "", errors.New("missing " + DeliveryHeader)
	}

	switch kind := header.Get(EventHeader); kind {
	case "ping":
		return nil, "ping", nil
	case "workflow_run":
		return translateRun(delivery, body, svcs)
	case "workflow_job":
		ev, err := translateJob(delivery, body)
		return ev, "", err
	case "":
		return nil, "", errors.New("missing " + EventHeader)
	default:
		return nil, fmt.Sprintf("event %q is not tracked", kind), nil
	}
}

func translateRun(delivery string, body []byte, svcs services.Map) (*events.Event, string, error) {
	var p workflowRunPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, "", fmt.Errorf("decode workflow_run: %w", err)
	}
	run := p.WorkflowRun
	if run == nil || run.ID == 0 || run.HeadSHA == "" || run.RunNumber == 0 {
		return nil, "", errors.New("workflow_run payload is missing id, head_sha or run_number")
	}

	svc, ok := svcs.ByWorkflow(p.Repository.FullName, run.Path)
	if !ok {
		return nil, fmt.Sprintf("workflow %s in %s is not in the service map", run.Path, p.Repository.FullName), nil
	}
	if run.Event != "push" || run.HeadBranch != svc.Branch {
		return nil, fmt.Sprintf("%s run on %s is not a release", run.Event, run.HeadBranch), nil
	}

	out := events.WorkflowRun{
		Action:       p.Action,
		RunID:        run.ID,
		RunNumber:    run.RunNumber,
		RunAttempt:   max(run.RunAttempt, 1),
		Repository:   p.Repository.FullName,
		WorkflowPath: run.Path,
		WorkflowName: run.Name,
		HeadSHA:      run.HeadSHA,
		HeadBranch:   run.HeadBranch,
		Event:        run.Event,
		Status:       run.Status,
		Conclusion:   deref(run.Conclusion),
		CreatedAt:    run.CreatedAt.UTC(),
		UpdatedAt:    run.UpdatedAt.UTC(),
		Service:      svc.Name,
		Environment:  svc.Environment,
	}
	if run.RunStartedAt != nil {
		out.RunStartedAt = run.RunStartedAt.UTC()
	}
	payload, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}

	occurred := out.UpdatedAt
	if occurred.IsZero() {
		occurred = out.CreatedAt
	}
	return &events.Event{
		DeliveryID: delivery,
		Type:       events.TypeWorkflowRun,
		Attempt:    out.RunAttempt,
		OccurredAt: occurred,
		Keys: events.Keys{
			ReleaseID: releases.ApplicationReleaseID(svc.Name, run.HeadSHA, svc.Environment, run.RunNumber),
			RunID:     run.ID,
		},
		Payload: payload,
	}, "", nil
}

func translateJob(delivery string, body []byte) (*events.Event, error) {
	var p workflowJobPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("decode workflow_job: %w", err)
	}
	job := p.WorkflowJob
	if job == nil || job.ID == 0 || job.RunID == 0 || job.Name == "" {
		return nil, errors.New("workflow_job payload is missing id, run_id or name")
	}

	// Allowlist: runner IDs and names, log and HTML URLs and check-run
	// links are dropped here (plan section 8.1).
	out := events.WorkflowJob{
		Action:       p.Action,
		RunID:        job.RunID,
		RunAttempt:   max(job.RunAttempt, 1),
		JobID:        job.ID,
		JobName:      job.Name,
		WorkflowName: job.WorkflowName,
		Repository:   p.Repository.FullName,
		HeadSHA:      job.HeadSHA,
		Status:       job.Status,
		Conclusion:   deref(job.Conclusion),
		CreatedAt:    utc(job.CreatedAt),
		StartedAt:    utc(job.StartedAt),
		CompletedAt:  utc(job.CompletedAt),
		Labels:       job.Labels,
	}
	for _, s := range job.Steps {
		out.Steps = append(out.Steps, events.JobStep{
			Number:      s.Number,
			Name:        s.Name,
			Status:      s.Status,
			Conclusion:  deref(s.Conclusion),
			StartedAt:   utc(s.StartedAt),
			CompletedAt: utc(s.CompletedAt),
		})
	}
	payload, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}

	var occurred time.Time
	switch {
	case job.Status == "completed" && out.CompletedAt != nil:
		occurred = *out.CompletedAt
	case job.Status == "in_progress" && out.StartedAt != nil:
		occurred = *out.StartedAt
	case out.CreatedAt != nil:
		occurred = *out.CreatedAt
	}
	return &events.Event{
		DeliveryID: delivery,
		Type:       events.TypeWorkflowJob,
		Attempt:    out.RunAttempt,
		OccurredAt: occurred,
		Keys:       events.Keys{RunID: job.RunID},
		Payload:    payload,
	}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func utc(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
