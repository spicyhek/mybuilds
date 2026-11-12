// Command server runs the mybuilds HTTP server: the public UI and read API on
// one listener and Prometheus metrics on a second, cluster-internal one.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/spicyhek/mybuilds/internal/config"
	"github.com/spicyhek/mybuilds/internal/fixtures"
	"github.com/spicyhek/mybuilds/internal/httpapi"
	"github.com/spicyhek/mybuilds/internal/ingest"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
	"github.com/spicyhek/mybuilds/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	applied, err := store.Migrate(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	logger.Info("database ready", "path", cfg.DBPath, "migrations_applied", applied)

	if cfg.Fixtures {
		n, err := fixtures.Load(ctx, store)
		if err != nil {
			return fmt.Errorf("load fixtures: %w", err)
		}
		logger.Info("fixture mode", "releases_inserted", n)
	}

	svcs, err := services.Load(cfg.ServicesFile)
	if err != nil {
		return err
	}
	projector := releases.NewProjector(store, svcs, logger)
	// Rebuild every release from its events, so a projector change applies
	// to history. A release that fails to project is logged, not fatal: its
	// events are safe and the next deploy can fix the projector.
	rebuilt, err := projector.RebuildAll(ctx)
	if err != nil {
		logger.Error("rebuild projections", "error", err)
	}
	logger.Info("projections rebuilt", "releases", rebuilt, "services", len(svcs.Services()))

	secrets := ingest.Secrets{
		GitHubWebhook: cfg.GitHubWebhookSecret,
		CIEvents:      cfg.CIEventSecret,
		ArgoCDToken:   cfg.ArgoCDToken,
	}
	if cfg.Fixtures {
		// The simulated stream goes through the same signed path as real
		// events. Endpoints left unconfigured get a random secret that
		// nothing else knows, so they stay closed to outside callers.
		for _, s := range []*string{&secrets.GitHubWebhook, &secrets.CIEvents, &secrets.ArgoCDToken} {
			if *s == "" {
				*s = randomSecret()
			}
		}
	}

	hub := httpapi.NewHub(store, logger)
	ingester := ingest.New(ingest.Options{
		Store:      store,
		Projector:  projector,
		Services:   svcs,
		Secrets:    secrets,
		Logger:     logger,
		Registerer: prometheus.DefaultRegisterer,
		Notify:     hub.Notify,
	})
	logger.Info("ingestion",
		"github", cfg.GitHubWebhookSecret != "",
		"ci", cfg.CIEventSecret != "",
		"argocd", cfg.ArgoCDToken != "")

	if cfg.Fixtures {
		go func() {
			if _, err := store.GetRelease(ctx, fixtures.RecordedRelease); err == nil {
				logger.Info("fixture replay skipped: recorded release already present", "release_id", fixtures.RecordedRelease)
				return
			}
			err := fixtures.Replay(ctx, ingester, "personal-site-11", fixtures.ReplayOptions{
				Secrets: secrets, Lead: 5 * time.Second, MaxGap: 20 * time.Second, Logger: logger,
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("fixture replay", "error", err)
				return
			}
			logger.Info("fixture replay finished", "release_id", fixtures.RecordedRelease)
		}()
	}

	dist, err := web.Dist()
	if err != nil {
		return err
	}

	api := httpapi.New(httpapi.Options{
		Store:     store,
		ReleaseID: cfg.ReleaseID,
		Version:   version,
		Metrics:   httpapi.NewMetrics(prometheus.DefaultRegisterer, version),
		Logger:    logger,
		Ingest:    ingester,
		Hub:       hub,
		Services:  svcs,
		Fixtures:  cfg.Fixtures,
		Web:       dist,
	})

	appServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:              cfg.MetricsListen,
		Handler:           metricsMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 2)
	go func() {
		logger.Info("listening", "addr", cfg.Listen, "release_id", cfg.ReleaseID, "version", version)
		errs <- appServer.ListenAndServe()
	}()
	go func() {
		logger.Info("metrics listening", "addr", cfg.MetricsListen)
		errs <- metricsServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var shutdownErr error
	for _, srv := range []*http.Server{appServer, metricsServer} {
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	return shutdownErr
}

func randomSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b[:])
}
