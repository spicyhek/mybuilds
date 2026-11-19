// Package jobpoller follows the steps of running GitHub Actions jobs.
//
// GitHub sends a workflow_job webhook when a job is queued, starts and
// completes, never when a step does. The in_progress webhook carries the
// steps as they stood the moment the job started, so without more evidence
// a release shows that job's second step running until the job ends. While
// a release's job runs, the poller reads it from the REST API and stores
// each change in its steps as a workflow_job event with action "polled",
// which the projection folds like any other (docs/ingestion.md).
package jobpoller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/ingest/github"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/storage"
)

// Defaults.
const (
	// DefaultInterval is the time between polls of one job.
	DefaultInterval = 10 * time.Second
	// DefaultMaxAge is how long after its last webhook a job is still
	// polled: GitHub cancels a job after 6 hours.
	DefaultMaxAge = 6 * time.Hour
	// DefaultBaseURL is GitHub's REST API.
	DefaultBaseURL = "https://api.github.com"
)

// maxBody bounds a job response; a job with every step is a few KiB.
const maxBody = 1 << 20

// maxBackoff is the longest wait after repeated errors polling one job.
const maxBackoff = 2 * time.Minute

// Store is what the poller needs from storage.
type Store interface {
	InsertEvent(ctx context.Context, e *events.Event) (bool, error)
	RunningJobs(ctx context.Context, since time.Time) ([]storage.RunningJob, error)
}

// Projector re-projects the releases an event touches.
type Projector interface {
	ProjectEvent(ctx context.Context, e events.Event) ([]releases.Outcome, error)
}

// Options configures a Poller.
type Options struct {
	Store     Store
	Projector Projector
	Notify    func(ctx context.Context, eventID int64, outcomes []releases.Outcome)
	Logger    *slog.Logger
	// Token is a GitHub token that can read Actions on the services'
	// repositories. It is sent only to BaseURL and never logged.
	Token      string
	BaseURL    string
	Client     *http.Client
	UserAgent  string
	Registerer prometheus.Registerer
	Interval   time.Duration
	MaxAge     time.Duration
	Now        func() time.Time
}

type jobState struct {
	etag     string
	nextAt   time.Time
	failures int
	// gone is set once GitHub answered 404: the run was deleted.
	gone bool
}

// Poller polls running jobs. Its only state is what makes polling cheap,
// ETags and backoff; what it learns is stored as events, so a restart loses
// nothing.
type Poller struct {
	opts        Options
	jobs        map[int64]*jobState
	pausedUntil time.Time
	lowWarned   bool
	polls       *prometheus.CounterVec
}

// New returns a Poller.
func New(o Options) *Poller {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if o.UserAgent == "" {
		o.UserAgent = "mybuilds"
	}
	if o.Interval == 0 {
		o.Interval = DefaultInterval
	}
	if o.MaxAge == 0 {
		o.MaxAge = DefaultMaxAge
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	p := &Poller{
		opts: o,
		jobs: map[int64]*jobState{},
		polls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mybuilds_github_job_polls_total",
			Help: "GitHub job polls by result: changed, unchanged, gone, rate_limited or error.",
		}, []string{"result"}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(p.polls)
	}
	return p
}

// Run polls until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	p.opts.Logger.Info("github job poller starting", "interval", p.opts.Interval.String())
	t := time.NewTicker(min(p.opts.Interval, 5*time.Second))
	defer t.Stop()
	for {
		if err := p.Step(ctx); err != nil && ctx.Err() == nil {
			p.opts.Logger.Error("github job poller", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Step polls every running job that is due.
func (p *Poller) Step(ctx context.Context) error {
	now := p.opts.Now()
	if now.Before(p.pausedUntil) {
		return nil
	}
	jobs, err := p.opts.Store.RunningJobs(ctx, now.Add(-p.opts.MaxAge))
	if err != nil {
		return err
	}
	running := map[int64]bool{}
	for _, j := range jobs {
		running[j.JobID] = true
	}
	for id := range p.jobs {
		if !running[id] {
			delete(p.jobs, id)
		}
	}
	for _, j := range jobs {
		st := p.jobs[j.JobID]
		if st == nil {
			st = &jobState{}
			p.jobs[j.JobID] = st
		}
		if st.gone || now.Before(st.nextAt) {
			continue
		}
		if err := p.poll(ctx, j, st, now); err != nil {
			return err
		}
		if now.Before(p.pausedUntil) || ctx.Err() != nil {
			return nil
		}
	}
	return nil
}

// poll reads one job. Only a failure to store is returned; GitHub's errors
// are logged and back off.
func (p *Poller) poll(ctx context.Context, j storage.RunningJob, st *jobState, now time.Time) error {
	log := p.opts.Logger.With("repository", j.Repository, "job_id", j.JobID, "job", j.JobName)
	url := fmt.Sprintf("%s/repos/%s/actions/jobs/%d", p.opts.BaseURL, j.Repository, j.JobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", p.opts.UserAgent)
	if p.opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.opts.Token)
	}
	if st.etag != "" {
		req.Header.Set("If-None-Match", st.etag)
	}

	resp, err := p.opts.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		p.backoff(st, now)
		p.polls.WithLabelValues("error").Inc()
		log.Warn("github job poll failed", "error", err.Error(), "failures", st.failures)
		return nil
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	p.watchRateLimit(resp.Header, log)

	switch {
	case resp.StatusCode == http.StatusNotModified:
		// GitHub does not count a 304 against an authenticated rate limit.
		st.failures, st.nextAt = 0, now.Add(p.opts.Interval)
		p.polls.WithLabelValues("unchanged").Inc()
		return nil
	case resp.StatusCode == http.StatusOK && readErr == nil:
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		st.gone = true
		p.polls.WithLabelValues("gone").Inc()
		log.Warn("github job not found; it is no longer polled", "status", resp.StatusCode)
		return nil
	case p.rateLimited(resp, now):
		p.polls.WithLabelValues("rate_limited").Inc()
		log.Warn("github rate limit reached; polling paused", "until", p.pausedUntil.UTC().Format(time.RFC3339))
		return nil
	default:
		p.backoff(st, now)
		if resp.StatusCode == http.StatusUnauthorized {
			// A bad token fails every job alike; wait longer.
			st.nextAt = now.Add(5 * time.Minute)
		}
		p.polls.WithLabelValues("error").Inc()
		log.Warn("github job poll failed", "status", resp.StatusCode, "failures", st.failures)
		return nil
	}

	var job github.Job
	if err := json.Unmarshal(body, &job); err != nil || job.ID != j.JobID || job.RunID != j.RunID {
		p.backoff(st, now)
		p.polls.WithLabelValues("error").Inc()
		log.Warn("github job response is not this job", "decode_error", err != nil)
		return nil
	}
	st.etag = resp.Header.Get("ETag")
	st.failures, st.nextAt = 0, now.Add(p.opts.Interval)

	ev, err := github.JobEvent(job, github.ActionPolled, j.Repository)
	if err != nil {
		p.polls.WithLabelValues("error").Inc()
		log.Warn("github job response is incomplete", "error", err.Error())
		return nil
	}
	// One event per distinct state of the job: polling an unchanged job
	// stores nothing new, even after a restart forgets the ETag.
	sum := sha256.Sum256(ev.Payload)
	ev.Provider = events.ProviderGitHub
	ev.DeliveryID = fmt.Sprintf("poll/%d/%s", j.JobID, hex.EncodeToString(sum[:8]))
	ev.ReceivedAt = now.UTC()
	raw := sha256.Sum256(body)
	ev.BodySHA256 = hex.EncodeToString(raw[:])
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = now.UTC()
	}

	inserted, err := p.opts.Store.InsertEvent(ctx, ev)
	if err != nil {
		return err
	}
	if !inserted {
		p.polls.WithLabelValues("unchanged").Inc()
		return nil
	}
	p.polls.WithLabelValues("changed").Inc()
	log.Debug("github job steps changed", "event_id", ev.ID, "status", job.Status)
	outcomes, err := p.opts.Projector.ProjectEvent(ctx, *ev)
	if err != nil {
		return fmt.Errorf("project job %d: %w", j.JobID, err)
	}
	if p.opts.Notify != nil && len(outcomes) > 0 {
		p.opts.Notify(ctx, ev.ID, outcomes)
	}
	return nil
}

func (p *Poller) backoff(st *jobState, now time.Time) {
	st.failures++
	wait := p.opts.Interval << min(st.failures, 6)
	st.nextAt = now.Add(min(wait, maxBackoff))
}

// rateLimited pauses all polling when GitHub says the token's quota is
// spent: a primary limit names its reset, a secondary one a Retry-After.
func (p *Poller) rateLimited(resp *http.Response, now time.Time) bool {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil {
			p.pausedUntil = now.Add(time.Duration(secs) * time.Second)
			return true
		}
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		p.pausedUntil = now.Add(time.Minute)
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			p.pausedUntil = time.Unix(reset, 0)
		}
		return true
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		p.pausedUntil = now.Add(time.Minute)
		return true
	}
	return false
}

// watchRateLimit warns once when the token's remaining quota runs low.
func (p *Poller) watchRateLimit(h http.Header, log *slog.Logger) {
	remaining, err := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	if err != nil {
		return
	}
	switch {
	case remaining < 100 && !p.lowWarned:
		p.lowWarned = true
		log.Warn("github rate limit running low", "remaining", remaining, "limit", h.Get("X-RateLimit-Limit"))
	case remaining >= 100:
		p.lowWarned = false
	}
}
