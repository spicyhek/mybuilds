package httpapi

import (
	"context"
	"net/http"
	"time"
)

// healthz reports that the process is up. It carries X-Release-ID so the
// verifier can confirm which release answered (plan section 8.5), and
// Cache-Control: no-store so Cloudflare never serves a stale header.
func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	s.releaseHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// readyz reports whether the database answers.
func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	s.releaseHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.logger.Warn("readiness check failed", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("database unavailable\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

func (s *server) releaseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Release-ID", s.releaseID)
}
