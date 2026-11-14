package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	DeployedWith     string      `json:"deployed_with,omitempty"`
	Flag             string      `json:"flag,omitempty"`
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
	// NextCursor continues the list; null on the last page.
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
		DeployedWith:     r.DeployedWith,
		Flag:             r.Flag,
		Status:           string(r.Status),
		CreatedAt:        r.CreatedAt,
		CompletedAt:      r.CompletedAt,
		VerifiedAt:       r.VerifiedAt,
		Stages:           stages,
	}
}

func (s *server) listReleases(w http.ResponseWriter, r *http.Request) {
	f, msg := parseListQuery(r.URL.Query())
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	list, next, err := s.store.ListReleasesPage(r.Context(), f)
	if err != nil {
		s.logger.Error("list releases", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	views := make([]releaseView, 0, len(list))
	for _, rel := range list {
		views = append(views, toView(rel))
	}
	resp := listReleasesResponse{Releases: views}
	if next != nil {
		c := encodeCursor(*next)
		resp.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, resp)
}

var outcomes = map[string]bool{
	string(releases.StatePending):   true,
	string(releases.StateRunning):   true,
	string(releases.StateSucceeded): true,
	string(releases.StateFailed):    true,
	string(releases.StateDegraded):  true,
	string(releases.StateSkipped):   true,
	string(releases.StateUnknown):   true,
}

// parseListQuery reads the filters of GET /api/v1/releases. A non-empty
// message means the query is invalid.
func parseListQuery(q url.Values) (storage.ReleaseFilter, string) {
	f := storage.ReleaseFilter{Limit: defaultListLimit, Service: q.Get("service")}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxListLimit {
			return f, "limit must be an integer between 1 and 200"
		}
		f.Limit = n
	}
	switch k := q.Get("kind"); k {
	case "", string(releases.KindApplication), string(releases.KindConfig):
		f.Kind = k
	default:
		return f, "kind must be application or config"
	}
	if o := q.Get("outcome"); o != "" {
		if !outcomes[o] {
			return f, "outcome must be a release state"
		}
		f.Outcome = o
	}
	if v := q.Get("drill"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return f, "drill must be true or false"
		}
		f.Drill = &b
	}
	if v := q.Get("cursor"); v != "" {
		c, err := decodeCursor(v)
		if err != nil {
			return f, "invalid cursor"
		}
		f.After = &c
	}
	return f, ""
}

// A cursor is opaque to clients: the last release's creation time and ID,
// base64url-encoded.
func encodeCursor(c storage.ReleaseCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.CreatedAt.UTC().Format(time.RFC3339Nano) + "\n" + c.ReleaseID))
}

func decodeCursor(v string) (storage.ReleaseCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return storage.ReleaseCursor{}, err
	}
	at, id, ok := strings.Cut(string(b), "\n")
	if !ok || id == "" {
		return storage.ReleaseCursor{}, errors.New("malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return storage.ReleaseCursor{}, err
	}
	return storage.ReleaseCursor{CreatedAt: t, ReleaseID: id}, nil
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
