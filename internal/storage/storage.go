// Package storage owns the SQLite database: opening it with the right
// pragmas, versioned migrations, and the queries the rest of the server uses.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Store is a handle on the database.
type Store struct {
	db *sql.DB
}

// Open opens or creates the SQLite database at path.
//
// WAL mode and synchronous=NORMAL are what the Litestream sidecar expects;
// the application never forces checkpoints itself (plan section 15.2).
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	// Every transaction here writes, so each begins IMMEDIATE and takes the
	// write lock up front, waiting up to busy_timeout for it. A deferred
	// transaction would start on a read snapshot and fail at its first write
	// with SQLITE_BUSY_SNAPSHOT (517) if another connection committed in
	// between; the busy handler never runs for that, so the ingester, the
	// runtime watch and the verifier lost projections when they wrote at
	// once (seen draining the queue after an outage).
	dsn := "file:" + filepath.ToSlash(path) + "?" + strings.Join([]string{
		"_txlock=immediate",
		"_pragma=busy_timeout(5000)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=foreign_keys(ON)",
	}, "&")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One process is the only writer. A few connections let readers run
	// alongside a write; busy_timeout covers write contention between the
	// connections that write.
	db.SetMaxOpenConns(4)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Ping runs a trivial query, for the readiness probe.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	if err := s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}
