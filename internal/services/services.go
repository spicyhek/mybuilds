// Package services is the service map: which repository, workflow, jobs and
// Argo CD application belong to each tracked service (plan sections 8.1, 8.8
// and 8.9).
package services

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed services.json
var defaultMap []byte

// Service is one tracked service.
type Service struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
	// Repository is the source repository, owner/name.
	Repository string `json:"repository"`
	// Workflow is the release workflow's path in the repository.
	Workflow string `json:"workflow"`
	// Branch is the branch whose pushes are releases.
	Branch            string `json:"branch"`
	ArgoCDApplication string `json:"argocd_application"`
	// GitOpsRepository and GitOpsPath are where the digest bump lands, and
	// Artifact is the image the workflow publishes. All three are shown in
	// the UI's stage detail panel.
	GitOpsRepository string         `json:"gitops_repository"`
	GitOpsPath       string         `json:"gitops_path"`
	Artifact         string         `json:"artifact"`
	Jobs             map[string]Job `json:"jobs"`
	// Workload is the Deployment the Rollout stage watches. Without it the
	// service's releases stop at Reconcile.
	Workload *Workload `json:"workload,omitempty"`
	// Verify is the public check of the Verify stage. Without it, Verify
	// is skipped once Rollout succeeds.
	Verify *Verify `json:"verify,omitempty"`
}

// Verify configures the public check (plan section 8.5).
type Verify struct {
	// URL is checked through the public route, normally Cloudflare.
	URL string `json:"url"`
	// AllowedStatus defaults to [200].
	AllowedStatus []int `json:"allowed_status,omitempty"`
	// MaxLatencyMS defaults to 2000.
	MaxLatencyMS int `json:"max_latency_ms,omitempty"`
	// WindowSeconds, from Rollout's completion, defaults to 600.
	WindowSeconds int `json:"window_seconds,omitempty"`
	// HealthBody, when set, requires a JSON body field to equal a value.
	HealthBody *HealthBody `json:"health_body,omitempty"`
}

// HealthBody is a required JSON field value, such as status = ok.
type HealthBody struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// Status returns the allowed status codes.
func (v Verify) Status() []int {
	if len(v.AllowedStatus) == 0 {
		return []int{200}
	}
	return v.AllowedStatus
}

// MaxLatency returns the latency threshold.
func (v Verify) MaxLatency() time.Duration {
	if v.MaxLatencyMS <= 0 {
		return 2 * time.Second
	}
	return time.Duration(v.MaxLatencyMS) * time.Millisecond
}

// Window returns how long after Rollout the checks may run.
func (v Verify) Window() time.Duration {
	if v.WindowSeconds <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(v.WindowSeconds) * time.Second
}

// Workload names a Deployment. The runtime watch only sees objects labeled
// mybuilds.brendanmanley.com/managed=true.
type Workload struct {
	Namespace  string `json:"namespace"`
	Deployment string `json:"deployment"`
}

// Key is the workload as namespace/name, the events table's workload key.
func (w Workload) Key() string {
	return w.Namespace + "/" + w.Deployment
}

// WorkloadKey returns the service's workload key, or "" without a workload.
func (s Service) WorkloadKey() string {
	if s.Workload == nil {
		return ""
	}
	return s.Workload.Key()
}

// Job maps one workflow job's steps to pipeline stages.
type Job struct {
	// Stage is the job's default stage. Steps that match no FromStep rule
	// land here, so renaming a step never hides it.
	Stage string `json:"stage"`
	// FromStep moves the named step and every step after it to another
	// stage. Rules apply in order.
	FromStep []StepStage `json:"from_step,omitempty"`
}

// StepStage starts a stage at a named step.
type StepStage struct {
	Step  string `json:"step"`
	Stage string `json:"stage"`
}

// Map is the set of tracked services.
type Map struct {
	services []Service
}

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Load reads the service map from path, or the built-in map when path is
// empty.
func Load(path string) (Map, error) {
	data := defaultMap
	if path != "" {
		var err error
		if data, err = os.ReadFile(path); err != nil {
			return Map{}, fmt.Errorf("read service map: %w", err)
		}
	}
	return Parse(data)
}

// Parse decodes and validates a service map.
func Parse(data []byte) (Map, error) {
	var doc struct {
		Services []Service `json:"services"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return Map{}, fmt.Errorf("parse service map: %w", err)
	}
	seen := map[string]bool{}
	for i, s := range doc.Services {
		switch {
		case !namePattern.MatchString(s.Name):
			return Map{}, fmt.Errorf("service %d: invalid name %q", i, s.Name)
		case seen[s.Name]:
			return Map{}, fmt.Errorf("service %q listed twice", s.Name)
		case s.Environment == "" || s.Repository == "" || s.Workflow == "" || s.Branch == "":
			return Map{}, fmt.Errorf("service %q: environment, repository, workflow and branch are required", s.Name)
		}
		if w := s.Workload; w != nil && (!namePattern.MatchString(w.Namespace) || !namePattern.MatchString(w.Deployment)) {
			return Map{}, fmt.Errorf("service %q: workload needs a valid namespace and deployment", s.Name)
		}
		if v := s.Verify; v != nil {
			if u, err := url.Parse(v.URL); err != nil || u.Scheme != "https" || u.Host == "" {
				return Map{}, fmt.Errorf("service %q: verify url must be an https URL", s.Name)
			}
			if h := v.HealthBody; h != nil && (h.Field == "" || h.Value == "") {
				return Map{}, fmt.Errorf("service %q: health_body needs a field and a value", s.Name)
			}
		}
		seen[s.Name] = true
	}
	return Map{services: doc.Services}, nil
}

// Services returns every service in the map.
func (m Map) Services() []Service {
	return append([]Service(nil), m.services...)
}

// ByName finds a service by name.
func (m Map) ByName(name string) (Service, bool) {
	for _, s := range m.services {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// ByApplication finds the service whose own Argo CD Application is app.
func (m Map) ByApplication(app string) (Service, bool) {
	for _, s := range m.services {
		if app != "" && s.ArgoCDApplication == app {
			return s, true
		}
	}
	return Service{}, false
}

// ByWorkload finds the service whose workload key is key.
func (m Map) ByWorkload(key string) (Service, bool) {
	for _, s := range m.services {
		if key != "" && s.WorkloadKey() == key {
			return s, true
		}
	}
	return Service{}, false
}

// Namespaces returns the namespaces of every service's workload, sorted.
func (m Map) Namespaces() []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range m.services {
		if s.Workload != nil && !seen[s.Workload.Namespace] {
			seen[s.Workload.Namespace] = true
			out = append(out, s.Workload.Namespace)
		}
	}
	sort.Strings(out)
	return out
}

// ByWorkflow finds the service released by a repository's workflow file.
// Repository names compare case-insensitively, as on GitHub.
func (m Map) ByWorkflow(repository, workflowPath string) (Service, bool) {
	// Reusable and dynamic workflows can carry an @ref suffix.
	workflowPath, _, _ = strings.Cut(workflowPath, "@")
	for _, s := range m.services {
		if strings.EqualFold(s.Repository, repository) && s.Workflow == workflowPath {
			return s, true
		}
	}
	return Service{}, false
}

// StageForStep returns the stage a job's step belongs to. ok is false for
// jobs the service map does not know, such as a test job.
func (s Service) StageForStep(jobName string, steps []string, index int) (stage string, ok bool) {
	job, ok := s.Jobs[jobName]
	if !ok {
		return "", false
	}
	stage = job.Stage
	for _, rule := range job.FromStep {
		for i := 0; i <= index && i < len(steps); i++ {
			if steps[i] == rule.Step {
				stage = rule.Stage
				break
			}
		}
	}
	return stage, true
}

// JobStage returns a job's default stage.
func (s Service) JobStage(jobName string) (string, bool) {
	job, ok := s.Jobs[jobName]
	return job.Stage, ok
}
