// Package ingest receives provider events: it authenticates each request,
// translates it into a normalized event, stores it once and re-projects the
// releases it touches (plan sections 8, 11, 13 and 14).
//
// The work is split from HTTP so that the SQS consumer (plan section 8.6) can
// run the same checks on queued requests.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/ingest/argocd"
	"github.com/spicyhek/mybuilds/internal/ingest/ci"
	"github.com/spicyhek/mybuilds/internal/ingest/github"
	"github.com/spicyhek/mybuilds/internal/ingest/verify"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
)

// MaxBodyBytes is the direct endpoints' body limit, checked before parsing.
const MaxBodyBytes = 1 << 20

// Secrets authenticate each provider. An empty secret disables its endpoint.
type Secrets struct {
	GitHubWebhook string
	CIEvents      string
	ArgoCDToken   string
}

// Store is what ingestion needs from storage.
type Store interface {
	InsertEvent(ctx context.Context, e *events.Event) (bool, error)
}

// Projector re-projects the releases an event touches.
type Projector interface {
	ProjectEvent(ctx context.Context, e events.Event) ([]releases.Outcome, error)
}

// Options configures an Ingester.
type Options struct {
	Store     Store
	Projector Projector
	Services  services.Map
	Secrets   Secrets
	Logger    *slog.Logger
	// Registerer receives the ingestion metrics. Nil skips them.
	Registerer prometheus.Registerer
	// Now is the clock for received_at; time.Now when nil.
	Now func() time.Time
	// Notify is called after an event is stored and its releases
	// re-projected. The SSE stream hangs off it.
	Notify func(ctx context.Context, eventID int64, outcomes []releases.Outcome)
}

// Ingester authenticates, translates and stores provider events.
type Ingester struct {
	store     Store
	projector Projector
	services  services.Map
	secrets   Secrets
	logger    *slog.Logger
	now       func() time.Time
	notify    func(context.Context, int64, []releases.Outcome)

	results          *prometheus.CounterVec
	projectionErrors prometheus.Counter
}

// New returns an Ingester.
func New(o Options) *Ingester {
	in := &Ingester{
		store:     o.Store,
		projector: o.Projector,
		services:  o.Services,
		secrets:   o.Secrets,
		logger:    o.Logger,
		now:       o.Now,
		notify:    o.Notify,
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mybuilds_ingest_requests_total",
			Help: "Ingestion requests by provider and result.",
		}, []string{"provider", "result"}),
		projectionErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mybuilds_projection_errors_total",
			Help: "Stored events whose releases could not be re-projected.",
		}),
	}
	if in.logger == nil {
		in.logger = slog.Default()
	}
	if in.now == nil {
		in.now = time.Now
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(in.results, in.projectionErrors)
	}
	return in
}

// Outcome of an accepted request.
const (
	OutcomeStored    = "stored"
	OutcomeDuplicate = "duplicate"
	OutcomeIgnored   = "ignored"
)

// Result describes an accepted request.
type Result struct {
	Outcome  string   `json:"outcome"`
	Type     string   `json:"type,omitempty"`
	EventID  int64    `json:"event_id,omitempty"`
	Releases []string `json:"releases,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// Error is a rejected request.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func reject(status int, format string, args ...any) error {
	return &Error{Status: status, Message: fmt.Sprintf(format, args...)}
}

// Ingest handles one request body from provider. It returns after the event
// row has committed; a failed re-projection is logged and counted, not
// returned, because the event is already stored and the next event or
// restart re-projects the release.
func (in *Ingester) Ingest(ctx context.Context, provider string, header http.Header, body []byte) (res Result, err error) {
	defer func() {
		outcome := res.Outcome
		if err != nil {
			outcome = "rejected"
			var ie *Error
			if !errors.As(err, &ie) || ie.Status >= 500 {
				outcome = "error"
			}
		}
		in.results.WithLabelValues(provider, outcome).Inc()
	}()

	now := in.now().UTC()
	var ev *events.Event
	var reason string

	switch provider {
	case events.ProviderGitHub:
		if in.secrets.GitHubWebhook == "" {
			return Result{}, reject(http.StatusServiceUnavailable, "github ingestion is not configured")
		}
		if err := verify.HMACSHA256(in.secrets.GitHubWebhook, body, header.Get(github.SignatureHeader)); err != nil {
			return Result{}, reject(http.StatusUnauthorized, "signature: %v", err)
		}
		ev, reason, err = github.Translate(header, body, in.services)

	case events.ProviderCI:
		if in.secrets.CIEvents == "" {
			return Result{}, reject(http.StatusServiceUnavailable, "ci ingestion is not configured")
		}
		if err := verify.HMACSHA256(in.secrets.CIEvents, body, header.Get(ci.SignatureHeader)); err != nil {
			return Result{}, reject(http.StatusUnauthorized, "signature: %v", err)
		}
		ev, err = ci.Translate(body, in.services)

	case events.ProviderArgoCD:
		if in.secrets.ArgoCDToken == "" {
			return Result{}, reject(http.StatusServiceUnavailable, "argocd ingestion is not configured")
		}
		if err := verify.BearerToken(in.secrets.ArgoCDToken, header.Get("Authorization")); err != nil {
			return Result{}, reject(http.StatusUnauthorized, "authorization: %v", err)
		}
		ev, err = argocd.Translate(body, bodyHash(body))

	default:
		return Result{}, reject(http.StatusNotFound, "unknown provider %q", provider)
	}
	if err != nil {
		return Result{}, reject(http.StatusUnprocessableEntity, "%v", err)
	}
	if ev == nil {
		return Result{Outcome: OutcomeIgnored, Reason: reason}, nil
	}

	ev.Provider = provider
	ev.ReceivedAt = now
	ev.BodySHA256 = bodyHash(body)
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = now
	}

	inserted, err := in.store.InsertEvent(ctx, ev)
	if err != nil {
		return Result{}, fmt.Errorf("store event: %w", err)
	}
	if !inserted {
		return Result{Outcome: OutcomeDuplicate, Type: ev.Type}, nil
	}

	res = Result{Outcome: OutcomeStored, Type: ev.Type, EventID: ev.ID}
	if in.projector != nil {
		outcomes, perr := in.projector.ProjectEvent(ctx, *ev)
		for _, o := range outcomes {
			res.Releases = append(res.Releases, o.ReleaseID)
		}
		if perr != nil {
			in.projectionErrors.Inc()
			in.logger.Error("projection failed", "event_id", ev.ID, "type", ev.Type, "error", perr)
		}
		if in.notify != nil && len(outcomes) > 0 {
			in.notify(ctx, ev.ID, outcomes)
		}
	}
	in.logger.Info("event stored", "provider", provider, "type", ev.Type, "event_id", ev.ID,
		"release_id", ev.Keys.ReleaseID, "releases", res.Releases)
	return res, nil
}

// Handler returns the HTTP endpoint for one provider.
func (in *Ingester) Handler(provider string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				in.results.WithLabelValues(provider, "rejected").Inc()
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body exceeds 1 MiB"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read body"})
			return
		}

		res, err := in.Ingest(r.Context(), provider, r.Header, body)
		if err != nil {
			var ie *Error
			if errors.As(err, &ie) {
				if ie.Status == http.StatusUnauthorized {
					in.logger.Warn("rejected unauthenticated request", "provider", provider, "error", ie.Message)
				}
				writeJSON(w, ie.Status, map[string]string{"error": ie.Message})
				return
			}
			in.logger.Error("ingest failed", "provider", provider, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}

		status := http.StatusOK
		if res.Outcome == OutcomeStored {
			status = http.StatusAccepted
		}
		writeJSON(w, status, res)
	})
}

func bodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
