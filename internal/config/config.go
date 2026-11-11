// Package config reads the server configuration from the environment.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
)

// Config holds everything the server needs at startup.
type Config struct {
	// Listen is the address of the public HTTP server: UI, read API and health.
	Listen string
	// MetricsListen is the address of the cluster-internal /metrics server.
	MetricsListen string
	// DBPath is the SQLite database file. Its directory is created if missing.
	DBPath string
	// Fixtures inserts sample releases at startup, for UI work and screenshots.
	Fixtures bool
	// ReleaseID is this deployment's own release ID, returned on /healthz.
	ReleaseID string
	// LogLevel is the minimum level written to the log.
	LogLevel slog.Level
}

// FromEnv builds a Config from MYBUILDS_* variables, with defaults for local use.
func FromEnv() (Config, error) {
	cfg := Config{
		Listen:        envOr("MYBUILDS_LISTEN", ":8080"),
		MetricsListen: envOr("MYBUILDS_METRICS_LISTEN", ":9090"),
		DBPath:        envOr("MYBUILDS_DB_PATH", "data/mybuilds.db"),
		ReleaseID:     envOr("RELEASE_ID", "unknown"),
	}

	fixtures, err := envBool("MYBUILDS_FIXTURES", false)
	if err != nil {
		return cfg, err
	}
	cfg.Fixtures = fixtures

	if err := cfg.LogLevel.UnmarshalText([]byte(envOr("MYBUILDS_LOG_LEVEL", "info"))); err != nil {
		return cfg, fmt.Errorf("MYBUILDS_LOG_LEVEL: %w", err)
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback, fmt.Errorf("%s: %q is not a boolean", key, v)
	}
	return b, nil
}
