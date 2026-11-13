package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/storage"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// releaseView is the public shape of a release. It is an explicit allowlist:
// nothing reaches the API without a field here (plan section 14).
type releaseView struct {
	ReleaseID        string      `json:"release_id"`
	Service          string      `json:"service"`
	Environment      string      `json:"environment"`
	Kind             string      `json:"kind"`
	Template         string      `json:"template"`
	Drill            bool        `json:"drill"`
	DrillScenario    string      `json:"drill_scenario,omitempty"`
	Superseded       bool        `json:"superseded"`
	SourceRepository string      `json:"source_repository,omitempty"`
	SourceSHA        string      `json:"source_sha,omitempty"`
	Artifact         string      `json:"artifact,omitempty"`
	ArtifactDigest   string      `json:"artifact_digest,omitempty"`
	GitOpsRepository string      `json:"gitops_repository,omitempty"`
	GitOpsSHA        string      `json:"gitops_sha,omitempty"`
	GitOpsSubject    string      `json:"gitops_subject,omitempty"`
	GitOpsAuthor     string      `json:"gitops_author,omitempty"`
	RuntimeReleaseID string      `json:"runtime_release_id,omitempty"`
	Status           string      `json:"status"`
	CreatedAt        time.Time   `json:"created_at"`
	CompletedAt      *time.Time  `json:"completed_at,omitempty"`
	VerifiedAt       *time.Time  `json:"verified_at,omitempty"`
	Stages           []stageView `json:"stages"`
}

type stageView struct {
	Key           string     `json:"key"`
	Position      int        `json:"position"`
	State         string     `json:"state"`
	Attempt       int        `json:"attempt"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	FailureReason string     `json:"failure_reason,omitempty"`
	// Steps are present on the single-release endpoint only.
	Steps []stepView `json:"steps,omitempty"`
}

type stepView struct {
	Position      int        `json:"position"`
	Name          string     `json:"name"`
	State         string     `json:"state"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Detail        string     `json:"detail,omitempty"`
	Reconstructed bool       `json:"reconstructed,omitempty"`
}

type listReleasesResponse struct {
	Releases []releaseView `json:"releases"`
	// NextCursor is always null until cursor pagination lands with the
	// history screen (plan section 7.2).
	NextCursor *string `json:"next_cursor"`
}

func toView(r releases.Release) releaseView {
	stages := make([]stageView, 0, len(r.Stages))
	for _, st := range r.Stages {
		var steps []stepView
		for _, sp := range st.Steps {
			steps = append(steps, stepView{
				Position:      sp.Position,
				Name:          sp.Name,
				State:         string(sp.State),
				StartedAt:     sp.StartedAt,
				CompletedAt:   sp.CompletedAt,
				Detail:        sp.Detail,
				Reconstructed: sp.Reconstructed,
			})
		}
		stages = append(stages, stageView{
			Key:           st.Key,
			Position:      st.Position,
			State:         string(st.State),
			Attempt:       st.Attempt,
			StartedAt:     st.StartedAt,
			CompletedAt:   st.CompletedAt,
			Summary:       st.Summary,
			FailureReason: st.FailureReason,
			Steps:         steps,
		})
	}
	return releaseView{
		ReleaseID:        r.ReleaseID,
		Service:          r.Service,
		Environment:      r.Environment,
		Kind:             string(r.Kind),
		Template:         r.Template,
		Drill:            r.Drill,
		DrillScenario:    r.DrillScenario,
		Superseded:       r.Superseded,
		SourceRepository: r.SourceRepository,
		SourceSHA:        r.SourceSHA,
		Artifact:         r.Artifact,
		ArtifactDigest:   r.ArtifactDigest,
		GitOpsRepository: r.GitOpsRepository,
		GitOpsSHA:        r.GitOpsSHA,
		GitOpsSubject:    r.GitOpsSubject,
		GitOpsAuthor:     r.GitOpsAuthor,
		RuntimeReleaseID: r.RuntimeReleaseID,
		Status:           string(r.Status),
		CreatedAt:        r.CreatedAt,
		CompletedAt:      r.CompletedAt,
		VerifiedAt:       r.VerifiedAt,
		Stages:           stages,
	}
}

func (s *server) listReleases(w http.ResponseWriter, r *http.Request) {
	limit := defaultListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxListLimit {
			writeError(w, http.StatusBadRequest, "limit must be an integer between 1 and 200")
			return
		}
		limit = n
	}

	list, err := s.store.ListReleases(r.Context(), limit)
	if err != nil {
		s.logger.Error("list releases", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	views := make([]releaseView, 0, len(list))
	for _, rel := range list {
		views = append(views, toView(rel))
	}
	writeJSON(w, http.StatusOK, listReleasesResponse{Releases: views})
}

func (s *server) getRelease(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusNotFound, "release not found")
		return
	}

	rel, err := s.store.GetRelease(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "release not found")
		return
	}
	if err != nil {
		s.logger.Error("get release", "release_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, toView(*rel))
}
