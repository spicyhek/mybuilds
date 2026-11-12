// Package httpapi serves the public UI, the read API and the health endpoints.
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/ingest"
	"github.com/spicyhek/mybuilds/internal/storage"
)

// Options configures the handler returned by New.
type Options struct {
	Store     *storage.Store
	ReleaseID string
	Version   string
	Metrics   *Metrics
	Logger    *slog.Logger
	// Ingest receives provider events. Nil leaves the ingestion routes out.
	Ingest *ingest.Ingester
	// Web is the built frontend. Unknown paths outside /api fall back to its
	// index.html so the client-side router can take over.
	Web fs.FS
}

type server struct {
	store     *storage.Store
	releaseID string
	version   string
	metrics   *Metrics
	logger    *slog.Logger
}

// New builds the public HTTP handler.
func New(o Options) http.Handler {
	s := &server{
		store:     o.Store,
		releaseID: o.ReleaseID,
		version:   o.Version,
		metrics:   o.Metrics,
		logger:    o.Logger,
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /api/v1/releases", s.listReleases)
	// Release IDs contain slashes (service/sha/environment/run), so the
	// wildcard has to take the rest of the path.
	mux.HandleFunc("GET /api/v1/releases/{id...}", s.getRelease)
	if o.Ingest != nil {
		mux.Handle("POST /hooks/github", o.Ingest.Handler(events.ProviderGitHub))
		mux.Handle("POST /hooks/argocd", o.Ingest.Handler(events.ProviderArgoCD))
		mux.Handle("POST /api/v1/ci-events", o.Ingest.Handler(events.ProviderCI))
	}
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("/hooks/", notFound)
	if o.Web != nil {
		mux.Handle("/", spaHandler(o.Web))
	}

	return s.observe(mux)
}

// observe wraps the mux with request IDs, access logging and metrics.
func (s *server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := newRequestID()
		w.Header().Set("X-Request-ID", requestID)

		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		duration := time.Since(start)
		if s.metrics != nil {
			s.metrics.record(route, sw.status, duration)
		}

		level := slog.LevelDebug
		if sw.status >= 500 {
			level = slog.LevelWarn
		}
		s.logger.LogAttrs(r.Context(), level, "request",
			slog.String("request_id", requestID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("route", route),
			slog.Int("status", sw.status),
			slog.Duration("duration", duration),
		)
	})
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// statusWriter records the status code written through it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush, which SSE will need.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
