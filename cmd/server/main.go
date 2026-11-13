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
	"k8s.io/client-go/rest"

	"github.com/spicyhek/mybuilds/internal/config"
	"github.com/spicyhek/mybuilds/internal/fixtures"
	"github.com/spicyhek/mybuilds/internal/httpapi"
	"github.com/spicyhek/mybuilds/internal/ingest"
	"github.com/spicyhek/mybuilds/internal/kube"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
	"github.com/spicyhek/mybuilds/internal/verifier"
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

	watcher := startRuntimeWatch(ctx, cfg, logger, svcs, store, projector, hub)

	// The verifier checks public URLs for real releases; fixture mode has
	// none.
	if cfg.Verifier == "off" || cfg.Fixtures {
		logger.Info("verifier disabled", "setting", cfg.Verifier, "fixtures", cfg.Fixtures)
	} else {
		opts := verifier.Options{
			Store:      store,
			Projector:  projector,
			Services:   svcs,
			Notify:     hub.Notify,
			Logger:     logger,
			Registerer: prometheus.DefaultRegisterer,
			Checker:    verifier.Checker{UserAgent: "mybuilds-verifier/" + version},
		}
		if watcher != nil {
			opts.WatchSyncedAt = watcher.SyncedAt
		}
		v := verifier.New(opts)
		go v.Run(ctx)
	}

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

// startRuntimeWatch starts the Kubernetes watch for the Rollout stage when
// running in a cluster (plan section 8.4). Outside one, or in fixture mode,
// releases stop at Reconcile.
func startRuntimeWatch(ctx context.Context, cfg config.Config, logger *slog.Logger, svcs services.Map,
	store *storage.Store, projector *releases.Projector, hub *httpapi.Hub) *kube.Watcher {
	if cfg.RuntimeWatch == "off" || cfg.Fixtures {
		logger.Info("runtime watch disabled", "setting", cfg.RuntimeWatch, "fixtures", cfg.Fixtures)
		return nil
	}
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		logger.Info("runtime watch disabled: not running in a cluster", "reason", err.Error())
		return nil
	}
	restConfig.UserAgent = "mybuilds/" + version
	watcher, err := kube.New(kube.Options{
		Config:     restConfig,
		Services:   svcs,
		Store:      store,
		Projector:  projector,
		Notify:     hub.Notify,
		Logger:     logger,
		Registerer: prometheus.DefaultRegisterer,
	})
	if err != nil {
		logger.Error("runtime watch disabled", "error", err)
		return nil
	}
	go func() {
		if err := watcher.Run(ctx); err != nil {
			logger.Error("runtime watch stopped", "error", err)
		}
	}()
	return watcher
}

func randomSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b[:])
}
