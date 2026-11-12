// Package ci translates the HMAC-signed events sent by release workflows:
// release.published, gitops.updated and gitops.superseded (plan section 8.2).
package ci

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
)

// SignatureHeader carries "sha256=<hex>", the HMAC-SHA256 of the body.
const SignatureHeader = "X-Mybuilds-Signature-256"

// Event type names on the wire.
const (
	TypeReleasePublished = "release.published"
	TypeGitOpsUpdated    = "gitops.updated"
	TypeGitOpsSuperseded = "gitops.superseded"
)

var (
	fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digest  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Translate validates a CI event and turns it into a normalized event. The
// delivery ID is derived from the event itself, so a step retried with
// curl --retry stores once.
func Translate(body []byte, svcs services.Map) (*events.Event, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return nil, fmt.Errorf("decode CI event: %w", err)
	}

	switch head.Type {
	case TypeReleasePublished:
		var p events.ReleasePublished
		if err := strictDecode(body, &p); err != nil {
			return nil, err
		}
		svc, parts, err := checkIdentity(p.ReleaseID, p.Service, p.Environment, svcs)
		if err != nil {
			return nil, err
		}
		switch {
		case !strings.EqualFold(p.SourceRepository, svc.Repository):
			return nil, fmt.Errorf("source_repository %q is not %s's repository", p.SourceRepository, svc.Name)
		case !fullSHA.MatchString(p.SourceSHA) || !strings.HasPrefix(p.SourceSHA, parts.ShortSHA):
			return nil, errors.New("source_sha must be the full commit SHA named by release_id")
		case p.Artifact == "" || strings.ContainsAny(p.Artifact, "@ "):
			return nil, errors.New("artifact must be an image name without a tag or digest")
		case !digest.MatchString(p.ArtifactDigest):
			return nil, errors.New("artifact_digest must be sha256:<64 hex>")
		case p.WorkflowRunNumber != parts.RunNumber:
			return nil, errors.New("workflow_run_number does not match release_id")
		}
		return build(events.TypeReleasePublished, p.ReleaseID, p.WorkflowRunID, p.RunAttempt, p.OccurredAt, "", p)

	case TypeGitOpsUpdated:
		var p events.GitOpsUpdated
		if err := strictDecode(body, &p); err != nil {
			return nil, err
		}
		if _, _, err := checkIdentity(p.ReleaseID, p.Service, p.Environment, svcs); err != nil {
			return nil, err
		}
		switch {
		case p.GitOpsRepository == "":
			return nil, errors.New("gitops_repository is required")
		case !fullSHA.MatchString(p.GitOpsSHA):
			return nil, errors.New("gitops_sha must be a full commit SHA")
		}
		return build(events.TypeGitOpsUpdated, p.ReleaseID, p.WorkflowRunID, p.RunAttempt, p.OccurredAt, p.GitOpsSHA, p)

	case TypeGitOpsSuperseded:
		var p events.GitOpsSuperseded
		if err := strictDecode(body, &p); err != nil {
			return nil, err
		}
		if _, _, err := checkIdentity(p.ReleaseID, p.Service, p.Environment, svcs); err != nil {
			return nil, err
		}
		if p.SupersededBy != "" && !fullSHA.MatchString(p.SupersededBy) {
			return nil, errors.New("superseded_by must be a full commit SHA")
		}
		return build(events.TypeGitOpsSuperseded, p.ReleaseID, p.WorkflowRunID, p.RunAttempt, p.OccurredAt, "", p)

	case "":
		return nil, errors.New("type is required")
	default:
		return nil, fmt.Errorf("unknown CI event type %q", head.Type)
	}
}

// strictDecode rejects unknown fields: the CI contract is ours, so a typo in
// a workflow should fail the step instead of silently dropping a field.
func strictDecode(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode CI event: %w", err)
	}
	return nil
}

func checkIdentity(releaseID, service, environment string, svcs services.Map) (services.Service, releases.ReleaseIDParts, error) {
	parts, err := releases.ParseReleaseID(releaseID)
	if err != nil {
		return services.Service{}, parts, err
	}
	svc, ok := svcs.ByName(service)
	switch {
	case !ok:
		return svc, parts, fmt.Errorf("service %q is not in the service map", service)
	case parts.Service != service || parts.Environment != environment:
		return svc, parts, errors.New("service and environment do not match release_id")
	case environment != svc.Environment:
		return svc, parts, fmt.Errorf("service %s is not tracked in environment %q", service, environment)
	}
	return svc, parts, nil
}

func build(typ, releaseID string, runID int64, attempt int, occurred time.Time, revision string, payload any) (*events.Event, error) {
	switch {
	case runID <= 0:
		return nil, errors.New("workflow_run_id is required")
	case attempt < 1:
		return nil, errors.New("run_attempt must be at least 1")
	case occurred.IsZero():
		return nil, errors.New("occurred_at is required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &events.Event{
		DeliveryID: fmt.Sprintf("%s/%s/%d", strings.TrimPrefix(typ, "ci."), releaseID, attempt),
		Type:       typ,
		Attempt:    attempt,
		OccurredAt: occurred.UTC(),
		Keys:       events.Keys{ReleaseID: releaseID, RunID: runID, Revision: revision},
		Payload:    data,
	}, nil
}
