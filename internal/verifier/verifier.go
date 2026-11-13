package verifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
)

// Defaults.
const (
	// DefaultInterval is the time between checks of one release.
	DefaultInterval = 30 * time.Second
	// DefaultTick is how often the verifier looks for work.
	DefaultTick = 5 * time.Second
)

// Store is what the verifier needs from storage.
type Store interface {
	InsertEvent(ctx context.Context, e *events.Event) (bool, error)
	VerifyCandidates(ctx context.Context) ([]storage.VerifyCandidate, error)
}

// Projector re-projects the releases an event touches.
type Projector interface {
	ProjectEvent(ctx context.Context, e events.Event) ([]releases.Outcome, error)
}

// Options configures a Verifier.
type Options struct {
	Store      Store
	Projector  Projector
	Services   services.Map
	Notify     func(ctx context.Context, eventID int64, outcomes []releases.Outcome)
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	Checker    Checker
	Interval   time.Duration
	Tick       time.Duration
	Now        func() time.Time
}

// Verifier checks releases whose Rollout succeeded until their Verify stage
// is settled. All of its state is in the database, so a restart resumes
// where it stopped; a window that closed while mybuilds was down ends with
// what was checked.
type Verifier struct {
	opts    Options
	checks  *prometheus.CounterVec
	latency *prometheus.HistogramVec
}

// New returns a Verifier.
func New(o Options) *Verifier {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval == 0 {
		o.Interval = DefaultInterval
	}
	if o.Tick == 0 {
		o.Tick = DefaultTick
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	v := &Verifier{
		opts: o,
		checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mybuilds_verifier_checks_total",
			Help: "Public verification checks by service and result.",
		}, []string{"service", "result"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mybuilds_verifier_latency_seconds",
			Help:    "Latency of verification checks that got a response.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2, 3, 5, 10},
		}, []string{"service"}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(v.checks, v.latency)
	}
	return v
}

// Run checks releases until ctx is done.
func (v *Verifier) Run(ctx context.Context) {
	v.opts.Logger.Info("verifier starting", "interval", v.opts.Interval.String(), "required_passes", releases.RequiredPasses)
	t := time.NewTicker(v.opts.Tick)
	defer t.Stop()
	for {
		if err := v.Step(ctx); err != nil && ctx.Err() == nil {
			v.opts.Logger.Error("verifier", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Step does whatever is due now: closes stale windows and runs the checks
// that are due.
func (v *Verifier) Step(ctx context.Context) error {
	cands, err := v.opts.Store.VerifyCandidates(ctx)
	if err != nil {
		return err
	}
	byService := map[string][]storage.VerifyCandidate{}
	for _, c := range cands {
		byService[c.Service] = append(byService[c.Service], c)
	}
	names := make([]string, 0, len(byService))
	for name := range byService {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		cs := byService[name]
		sort.Slice(cs, func(i, j int) bool { return cs[i].RolloutCompletedAt.After(cs[j].RolloutCompletedAt) })
		// Only the newest rollout of a service is serving; older releases
		// still open can no longer be verified.
		for _, old := range cs[1:] {
			if err := v.end(ctx, old.ReleaseID, events.EndSuperseded, cs[0].RolloutCompletedAt); err != nil {
				return err
			}
		}
		c := cs[0]
		svc, ok := v.opts.Services.ByName(name)
		if !ok || svc.Verify == nil {
			continue
		}
		now := v.opts.Now()
		deadline := c.RolloutCompletedAt.Add(svc.Verify.Window())
		switch {
		case !now.Before(deadline):
			if err := v.end(ctx, c.ReleaseID, events.EndExpired, deadline); err != nil {
				return err
			}
		case c.LastCheckAt == nil || now.Sub(*c.LastCheckAt) >= v.opts.Interval:
			if err := v.check(ctx, svc, c.ReleaseID, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *Verifier) check(ctx context.Context, svc services.Service, releaseID string, now time.Time) error {
	res := v.opts.Checker.Check(ctx, *svc.Verify, releaseID, now)
	if ctx.Err() != nil {
		return nil
	}
	v.checks.WithLabelValues(svc.Name, res.Result).Inc()
	if res.StatusCode != 0 {
		v.latency.WithLabelValues(svc.Name).Observe(float64(res.LatencyMS) / 1000)
	}
	log := v.opts.Logger.Info
	if res.Result != events.CheckPass {
		log = v.opts.Logger.Warn
	}
	log("verification check", "release_id", releaseID, "result", res.Result, "status", res.StatusCode,
		"latency_ms", res.LatencyMS, "observed_release_id", res.ObservedReleaseID, "colo", res.Colo, "reason", res.Reason)
	return v.store(ctx, events.TypeVerifyCheck,
		fmt.Sprintf("check/%s/%d", releaseID, res.CheckedAt.UnixNano()), releaseID, res.CheckedAt, res)
}

func (v *Verifier) end(ctx context.Context, releaseID, reason string, at time.Time) error {
	v.opts.Logger.Info("verification window ended", "release_id", releaseID, "reason", reason)
	return v.store(ctx, events.TypeVerifyEnded, "ended/"+releaseID, releaseID, at,
		events.VerifyEnded{ReleaseID: releaseID, Reason: reason, OccurredAt: at.UTC()})
}

func (v *Verifier) store(ctx context.Context, typ, deliveryID, releaseID string, at time.Time, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := v.opts.Now().UTC()
	sum := sha256.Sum256(data)
	ev := &events.Event{
		Provider: events.ProviderVerifier, DeliveryID: deliveryID, Type: typ, Attempt: 1,
		OccurredAt: at.UTC(), ReceivedAt: now, BodySHA256: hex.EncodeToString(sum[:]),
		Keys: events.Keys{ReleaseID: releaseID}, Payload: data,
	}
	inserted, err := v.opts.Store.InsertEvent(ctx, ev)
	if err != nil || !inserted {
		return err
	}
	outcomes, err := v.opts.Projector.ProjectEvent(ctx, *ev)
	if err != nil {
		return fmt.Errorf("project %s: %w", releaseID, err)
	}
	if v.opts.Notify != nil && len(outcomes) > 0 {
		v.opts.Notify(ctx, ev.ID, outcomes)
	}
	return nil
}
