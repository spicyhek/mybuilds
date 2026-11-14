package storage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
)

// WorkloadEvents returns a workload's runtime events that occurred at or
// after from and, when to is set, before it, oldest first.
func (s *Store) WorkloadEvents(ctx context.Context, workload string, from time.Time, to *time.Time) ([]events.Event, error) {
	q := `SELECT ` + eventColumns + ` FROM events WHERE workload = ? AND occurred_at >= ?`
	args := []any{workload, formatTime(from)}
	if to != nil {
		q += ` AND occurred_at < ?`
		args = append(args, formatTime(*to))
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY occurred_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("events of workload %s: %w", workload, err)
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AppSyncs returns every revision an Argo CD Application has synced, each
// with the start of its first sync, oldest first. Health notifications are
// not syncs and are left out.
func (s *Store) AppSyncs(ctx context.Context, application string) ([]releases.Sync, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+eventColumns+` FROM events
		WHERE provider = ? AND revision IS NOT NULL
			AND json_extract(payload, '$.application') = ?
			AND json_extract(payload, '$.trigger') <> ?`,
		events.ProviderArgoCD, application, events.TriggerHealthDegraded)
	if err != nil {
		return nil, fmt.Errorf("syncs of %s: %w", application, err)
	}
	defer rows.Close()

	starts := map[string]time.Time{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		var n events.ArgoCDNotification
		if err := e.Decode(&n); err != nil {
			return nil, fmt.Errorf("event %d: %w", e.ID, err)
		}
		start := e.OccurredAt
		if n.StartedAt != nil {
			start = *n.StartedAt
		}
		if cur, ok := starts[e.Keys.Revision]; !ok || start.Before(cur) {
			starts[e.Keys.Revision] = start
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]releases.Sync, 0, len(starts))
	for rev, start := range starts {
		out = append(out, releases.Sync{Revision: rev, Start: start})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return out[i].Revision < out[j].Revision
	})
	return out, nil
}

// ReleasesWithRevision returns the releases whose gitops.updated event
// reported revision.
func (s *Store) ReleasesWithRevision(ctx context.Context, revision string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT release_key FROM events WHERE revision = ? AND release_key IS NOT NULL ORDER BY release_key`, revision)
	if err != nil {
		return nil, fmt.Errorf("releases of revision %s: %w", revision, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AppSyncEvents returns an Argo CD Application's notifications for one
// revision, oldest first. A release adopts them when its own commit was
// applied as part of that newer one.
func (s *Store) AppSyncEvents(ctx context.Context, application, revision string) ([]events.Event, error) {
	return s.queryEvents(ctx, `
		SELECT `+eventColumns+` FROM events
		WHERE provider = ? AND revision = ? AND json_extract(payload, '$.application') = ?
		ORDER BY occurred_at, id`, events.ProviderArgoCD, revision, application)
}

// VerifierEvents returns the verifier's events for a release, oldest first.
func (s *Store) VerifierEvents(ctx context.Context, releaseID string) ([]events.Event, error) {
	return s.queryEvents(ctx, `
		SELECT `+eventColumns+` FROM events WHERE release_key = ? AND provider = ?
		ORDER BY occurred_at, id`, releaseID, events.ProviderVerifier)
}

// LastDeploymentBefore returns a workload's latest Deployment snapshot that
// occurred before t, or nil.
func (s *Store) LastDeploymentBefore(ctx context.Context, workload string, t time.Time) (*events.Event, error) {
	evs, err := s.queryEvents(ctx, `
		SELECT `+eventColumns+` FROM events
		WHERE workload = ? AND type = ? AND occurred_at < ?
		ORDER BY occurred_at DESC, id DESC LIMIT 1`, workload, events.TypeDeployment, formatTime(t))
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	return &evs[0], nil
}

// UnsyncedReleases returns a service's application releases created since
// t whose reported GitOps commit has no Argo CD sync. It stays true after a
// newer commit that carried the bump is adopted, so runtime events keep
// reaching the release.
func (s *Store) UnsyncedReleases(ctx context.Context, service string, since time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.release_id FROM releases r
		WHERE r.service = ? AND r.kind = 'application' AND r.created_at >= ?
			AND r.gitops_sha IS NOT NULL AND r.superseded = 0
			AND NOT EXISTS (SELECT 1 FROM events e WHERE e.provider = ? AND e.revision = r.gitops_sha)
		ORDER BY r.created_at`, service, formatTime(since), events.ProviderArgoCD)
	if err != nil {
		return nil, fmt.Errorf("unsynced releases of %s: %w", service, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) queryEvents(ctx context.Context, q string, args ...any) ([]events.Event, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
