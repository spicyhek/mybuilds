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
