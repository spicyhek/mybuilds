package httpapi

import (
	"net/http"

	"github.com/spicyhek/mybuilds/internal/services"
)

// metaResponse is GET /api/v1/meta: what the UI needs to label itself and
// describe where each stage runs. Everything here is public already: the
// repositories, workflows and image names appear in release views.
type metaResponse struct {
	Version   string        `json:"version"`
	ReleaseID string        `json:"release_id"`
	Fixtures  bool          `json:"fixtures"`
	Services  []serviceView `json:"services"`
}

type serviceView struct {
	Name              string `json:"name"`
	Environment       string `json:"environment"`
	Repository        string `json:"repository"`
	Workflow          string `json:"workflow"`
	Branch            string `json:"branch"`
	Artifact          string `json:"artifact,omitempty"`
	GitOpsRepository  string `json:"gitops_repository,omitempty"`
	GitOpsPath        string `json:"gitops_path,omitempty"`
	ArgoCDApplication string `json:"argocd_application,omitempty"`
	// Workload is the Deployment the Rollout stage watches.
	Workload *workloadView `json:"workload,omitempty"`
	// Verify is the public check of the Verify stage.
	Verify *verifyView `json:"verify,omitempty"`
}

type verifyView struct {
	URL           string `json:"url"`
	AllowedStatus []int  `json:"allowed_status"`
	MaxLatencyMS  int64  `json:"max_latency_ms"`
	WindowSeconds int64  `json:"window_seconds"`
	HealthBody    string `json:"health_body,omitempty"`
}

type workloadView struct {
	Namespace  string `json:"namespace"`
	Deployment string `json:"deployment"`
}

func (s *server) meta(w http.ResponseWriter, _ *http.Request) {
	res := metaResponse{Version: s.version, ReleaseID: s.releaseID, Fixtures: s.fixtures, Services: []serviceView{}}
	for _, svc := range s.services.Services() {
		res.Services = append(res.Services, toServiceView(svc))
	}
	writeJSON(w, http.StatusOK, res)
}

func toServiceView(svc services.Service) serviceView {
	var workload *workloadView
	if w := svc.Workload; w != nil {
		workload = &workloadView{Namespace: w.Namespace, Deployment: w.Deployment}
	}
	var verify *verifyView
	if v := svc.Verify; v != nil {
		verify = &verifyView{URL: v.URL, AllowedStatus: v.Status(), MaxLatencyMS: v.MaxLatency().Milliseconds(),
			WindowSeconds: int64(v.Window().Seconds())}
		if h := v.HealthBody; h != nil {
			verify.HealthBody = h.Field + " = " + h.Value
		}
	}
	return serviceView{
		Workload:          workload,
		Verify:            verify,
		Name:              svc.Name,
		Environment:       svc.Environment,
		Repository:        svc.Repository,
		Workflow:          svc.Workflow,
		Branch:            svc.Branch,
		Artifact:          svc.Artifact,
		GitOpsRepository:  svc.GitOpsRepository,
		GitOpsPath:        svc.GitOpsPath,
		ArgoCDApplication: svc.ArgoCDApplication,
	}
}
