package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/spicyhek/mybuilds/internal/releases"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

func TestOpenSetsWALMode(t *testing.T) {
	store := openTestStore(t)

	var mode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, err := store.Migrate(ctx)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if first == 0 {
		t.Fatal("first migrate applied nothing")
	}

	second, err := store.Migrate(ctx)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if second != 0 {
		t.Fatalf("second migrate applied %d migrations, want 0", second)
	}

	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version < 1 {
		t.Fatalf("schema version = %d, want >= 1", version)
	}

	for _, table := range []string{"events", "releases", "release_stages", "release_steps", "verification_samples"} {
		var name string
		err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestInsertListAndGetRelease(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	started := time.Date(2026, 11, 8, 11, 25, 50, 0, time.UTC)
	done := started.Add(6 * time.Minute)
	rel := releases.Release{
		UUID:           releases.NewUUID(),
		ReleaseID:      "personal-site/ce61154/production/11",
		Service:        "personal-site",
		Environment:    "production",
		Kind:           releases.KindApplication,
		Template:       releases.TemplateApplication,
		SourceSHA:      "ce61154e05756b0a7e40311de30dccaa0273f305",
		ArtifactDigest: "sha256:9785af09",
		Status:         releases.StateSucceeded,
		CreatedAt:      started,
		CompletedAt:    &done,
		Stages: []releases.Stage{
			{Key: "source", State: releases.StateSucceeded, StartedAt: &started, Summary: "Push to main"},
			{Key: "build", State: releases.StateRunning},
		},
	}

	inserted, err := store.InsertRelease(ctx, rel)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if !inserted {
		t.Fatal("first insert reported not inserted")
	}

	again, err := store.InsertRelease(ctx, rel)
	if err != nil {
		t.Fatalf("duplicate insert: %v", err)
	}
	if again {
		t.Fatal("duplicate insert reported inserted")
	}

	list, err := store.ListReleases(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list returned %d releases, want 1", len(list))
	}
	got := list[0]
	if got.ReleaseID != rel.ReleaseID || got.Kind != releases.KindApplication || got.Status != releases.StateSucceeded {
		t.Errorf("listed release = %+v", got)
	}
	if !got.CreatedAt.Equal(started) || got.CompletedAt == nil || !got.CompletedAt.Equal(done) || got.VerifiedAt != nil {
		t.Errorf("timestamps: created %v completed %v verified %v", got.CreatedAt, got.CompletedAt, got.VerifiedAt)
	}
	if len(got.Stages) != 2 || got.Stages[0].Key != "source" || got.Stages[0].Position != 1 || got.Stages[1].Position != 2 {
		t.Errorf("stages = %+v", got.Stages)
	}
	if got.Stages[0].Attempt != 1 || got.Stages[0].StartedAt == nil || got.Stages[1].StartedAt != nil {
		t.Errorf("stage defaults = %+v", got.Stages)
	}

	one, err := store.GetRelease(ctx, rel.ReleaseID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if one.UUID != rel.UUID || len(one.Stages) != 2 {
		t.Errorf("got %+v", one)
	}

	if _, err := store.GetRelease(ctx, "nope/0000000/production/1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing release: err = %v, want ErrNotFound", err)
	}
}

func TestInsertReleaseRejectsBadKind(t *testing.T) {
	store := openTestStore(t)
	_, err := store.InsertRelease(context.Background(), releases.Release{
		UUID: releases.NewUUID(), ReleaseID: "x/1/production/1", Service: "x", Environment: "production",
		Kind: "mystery", Template: "application", Status: releases.StatePending, CreatedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("insert with unknown kind succeeded, want CHECK constraint error")
	}
}
