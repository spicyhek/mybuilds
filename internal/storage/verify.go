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
}

// VerifyCandidates returns the releases the verifier still has to check.
func (s *Store) VerifyCandidates(ctx context.Context) ([]VerifyCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.release_id, r.service, ro.completed_at,
			(SELECT MAX(occurred_at) FROM events e WHERE e.release_key = r.release_id AND e.type = ?1),
			(SELECT COUNT(*) FROM events e WHERE e.release_key = r.release_id AND e.type = ?1)
		FROM releases r
		JOIN release_stages ro ON ro.release_uuid = r.uuid AND ro.stage_key = 'rollout'
		JOIN release_stages v  ON v.release_uuid = r.uuid AND v.stage_key = 'verify'
		WHERE ro.state = 'succeeded' AND ro.completed_at IS NOT NULL
			AND v.state IN ('pending', 'running')
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
		if err := rows.Scan(&c.ReleaseID, &c.Service, &completed, &last, &c.Checks); err != nil {
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
