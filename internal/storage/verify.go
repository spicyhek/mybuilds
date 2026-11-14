package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
)

// VerifyCandidate is a release whose Rollout succeeded and whose Verify is
// still open.
type VerifyCandidate struct {
	ReleaseID          string
	Service            string
	RolloutCompletedAt time.Time
	// LastCheckAt is the time of its latest check, nil before the first.
	LastCheckAt *time.Time
	Checks      int
	// Expected is the X-Release-ID the checks expect: the release's own ID,
	// or for a config release, the release its pods carry.
	Expected string
}

// VerifyCandidates returns the releases the verifier still has to check.
func (s *Store) VerifyCandidates(ctx context.Context) ([]VerifyCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.release_id, r.service, ro.completed_at,
			CASE WHEN r.kind = 'config' THEN COALESCE(r.runtime_release_id, '') ELSE r.release_id END,
			(SELECT MAX(occurred_at) FROM events e WHERE e.release_key = r.release_id AND e.type = ?1),
			(SELECT COUNT(*) FROM events e WHERE e.release_key = r.release_id AND e.type = ?1)
		FROM releases r
		JOIN release_stages ro ON ro.release_uuid = r.uuid AND ro.stage_key = 'rollout'
		JOIN release_stages v  ON v.release_uuid = r.uuid AND v.stage_key = 'verify'
		WHERE ro.state = 'succeeded' AND ro.completed_at IS NOT NULL
			AND v.state IN ('pending', 'running')
			-- A config release deployed together with an application release
			-- shares that release's checks.
			AND NOT (r.kind = 'config' AND r.deployed_with_release IS NOT NULL)
		ORDER BY ro.completed_at, r.release_id`, events.TypeVerifyCheck)
	if err != nil {
		return nil, fmt.Errorf("verify candidates: %w", err)
	}
	defer rows.Close()
	var out []VerifyCandidate
	for rows.Next() {
		var c VerifyCandidate
		var completed string
		var last sql.NullString
		if err := rows.Scan(&c.ReleaseID, &c.Service, &completed, &c.Expected, &last, &c.Checks); err != nil {
			return nil, err
		}
		if c.RolloutCompletedAt, err = parseTime(completed); err != nil {
			return nil, err
		}
		if last.Valid {
			t, err := parseTime(last.String)
			if err != nil {
				return nil, err
			}
			c.LastCheckAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountSamples returns how many verification samples are stored, and how
// many of them passed.
func (s *Store) CountSamples(ctx context.Context) (total, passed int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE ok = 1) FROM verification_samples`).Scan(&total, &passed)
	return total, passed, err
}

// IdleCandidate is a config release whose sync succeeded while Rollout has
// no evidence yet.
type IdleCandidate struct {
	ReleaseID          string
	Service            string
	ReconcileStartedAt time.Time
	ReconcileDoneAt    time.Time
}

// IdleConfigReleases returns config releases that synced but whose Rollout
// is still pending.
func (s *Store) IdleConfigReleases(ctx context.Context) ([]IdleCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.release_id, r.service, COALESCE(rc.started_at, rc.completed_at), rc.completed_at
		FROM releases r
		JOIN release_stages rc ON rc.release_uuid = r.uuid AND rc.stage_key = 'reconcile'
		JOIN release_stages ro ON ro.release_uuid = r.uuid AND ro.stage_key = 'rollout'
		WHERE r.kind = 'config' AND rc.state = 'succeeded' AND rc.completed_at IS NOT NULL
			AND ro.state = 'pending'
		ORDER BY rc.completed_at`)
	if err != nil {
		return nil, fmt.Errorf("idle config releases: %w", err)
	}
	defer rows.Close()
	var out []IdleCandidate
	for rows.Next() {
		var c IdleCandidate
		var started, done string
		if err := rows.Scan(&c.ReleaseID, &c.Service, &started, &done); err != nil {
			return nil, err
		}
		if c.ReconcileStartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		if c.ReconcileDoneAt, err = parseTime(done); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
