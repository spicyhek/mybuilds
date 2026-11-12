package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
)

const eventColumns = `id, provider, provider_delivery_id, type, attempt, occurred_at, received_at,
	body_sha256, release_key, run_id, revision, payload`

// InsertEvent stores an event. A delivery already stored for the same
// provider is dropped and false is returned (plan section 13). On success
// e.ID is set.
func (s *Store) InsertEvent(ctx context.Context, e *events.Event) (inserted bool, err error) {
	attempt := e.Attempt
	if attempt < 1 {
		attempt = 1
	}
	var runID any
	if e.Keys.RunID != 0 {
		runID = e.Keys.RunID
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO events (provider, provider_delivery_id, type, attempt, occurred_at, received_at,
			body_sha256, release_key, run_id, revision, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, provider_delivery_id) DO NOTHING`,
		e.Provider, e.DeliveryID, e.Type, attempt, formatTime(e.OccurredAt), formatTime(e.ReceivedAt),
		e.BodySHA256, nullString(e.Keys.ReleaseID), runID, nullString(e.Keys.Revision), string(e.Payload),
	)
	if err != nil {
		return false, fmt.Errorf("insert event %s/%s: %w", e.Provider, e.DeliveryID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if e.ID, err = res.LastInsertId(); err != nil {
		return true, err
	}
	return true, nil
}

// AffectedReleaseIDs returns the releases an event can change: the one it
// names, the release of its workflow run, or the release that reported its
// GitOps commit.
func (s *Store) AffectedReleaseIDs(ctx context.Context, e events.Event) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	add(e.Keys.ReleaseID)

	query := func(q string, arg any) error {
		rows, err := s.db.QueryContext(ctx, q, arg)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			add(id)
		}
		return rows.Err()
	}
	if e.Keys.RunID != 0 {
		if err := query(`SELECT DISTINCT release_key FROM events WHERE run_id = ? AND release_key IS NOT NULL`, e.Keys.RunID); err != nil {
			return nil, fmt.Errorf("releases of run %d: %w", e.Keys.RunID, err)
		}
	}
	if e.Keys.Revision != "" {
		if err := query(`SELECT DISTINCT release_key FROM events WHERE revision = ? AND release_key IS NOT NULL`, e.Keys.Revision); err != nil {
			return nil, fmt.Errorf("releases of revision %s: %w", e.Keys.Revision, err)
		}
	}
	return out, nil
}

// ReleaseEvents returns every event of a release, oldest first: events that
// name the release, workflow_job events of its runs and Argo CD notifications
// for its GitOps commits.
func (s *Store) ReleaseEvents(ctx context.Context, releaseID string) ([]events.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH own AS (SELECT id, run_id, revision FROM events WHERE release_key = ?1)
		SELECT `+eventColumns+` FROM events WHERE id IN (
			SELECT id FROM own
			UNION
			SELECT e.id FROM events e
			WHERE e.type = ?2 AND e.run_id IN (SELECT run_id FROM own WHERE run_id IS NOT NULL)
			UNION
			SELECT e.id FROM events e
			WHERE e.provider = ?3 AND e.revision IN (SELECT revision FROM own WHERE revision IS NOT NULL)
		)
		ORDER BY occurred_at, id`,
		releaseID, events.TypeWorkflowJob, events.ProviderArgoCD)
	if err != nil {
		return nil, fmt.Errorf("events of %s: %w", releaseID, err)
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

// ProjectedReleaseIDs returns every release ID that an event names.
func (s *Store) ProjectedReleaseIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT release_key FROM events WHERE release_key IS NOT NULL ORDER BY release_key`)
	if err != nil {
		return nil, fmt.Errorf("list release keys: %w", err)
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

// CountEvents returns how many events are stored, and how many of them belong
// to no release yet.
func (s *Store) CountEvents(ctx context.Context) (total, uncorrelated int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE release_uuid IS NULL) FROM events`,
	).Scan(&total, &uncorrelated)
	return total, uncorrelated, err
}

func scanEvent(sc rowScanner) (events.Event, error) {
	var e events.Event
	var occurredAt, receivedAt, payload string
	var releaseKey, revision sql.NullString
	var runID sql.NullInt64
	if err := sc.Scan(&e.ID, &e.Provider, &e.DeliveryID, &e.Type, &e.Attempt, &occurredAt, &receivedAt,
		&e.BodySHA256, &releaseKey, &runID, &revision, &payload); err != nil {
		return e, fmt.Errorf("scan event: %w", err)
	}
	var err error
	if e.OccurredAt, err = parseTime(occurredAt); err != nil {
		return e, err
	}
	if e.ReceivedAt, err = parseTime(receivedAt); err != nil {
		return e, err
	}
	e.Keys = events.Keys{ReleaseID: releaseKey.String, RunID: runID.Int64, Revision: revision.String}
	e.Payload = []byte(payload)
	return e, nil
}

// SaveProjection replaces a release's projected row, stages and steps in one
// transaction, and links its events to it. The release keeps its UUID.
func (s *Store) SaveProjection(ctx context.Context, p releases.Projection) error {
	r := p.Release
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var uuid string
	err = tx.QueryRowContext(ctx, `SELECT uuid FROM releases WHERE release_id = ?`, r.ReleaseID).Scan(&uuid)
	switch {
	case err == sql.ErrNoRows:
		uuid = releases.NewUUID()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO releases (`+releaseColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			append([]any{uuid}, releaseValues(r)...)...); err != nil {
			return fmt.Errorf("insert release: %w", err)
		}
	case err != nil:
		return err
	default:
		if _, err := tx.ExecContext(ctx, `
			UPDATE releases SET release_id = ?, service = ?, environment = ?, kind = ?, template = ?,
				deployed_with = ?, drill = ?, drill_scenario = ?, superseded = ?,
				source_repository = ?, source_sha = ?, artifact = ?, artifact_digest = ?,
				gitops_repository = ?, gitops_sha = ?, status = ?, created_at = ?, completed_at = ?, verified_at = ?
			WHERE uuid = ?`,
			append(releaseValues(r), uuid)...); err != nil {
			return fmt.Errorf("update release: %w", err)
		}
	}

	for _, table := range []string{"release_steps", "release_stages"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE release_uuid = ?`, uuid); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	if err := insertStages(ctx, tx, uuid, r); err != nil {
		return err
	}
	for _, st := range p.Steps {
		var sourceEvent any
		if st.SourceEventID != 0 {
			sourceEvent = st.SourceEventID
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO release_steps (release_uuid, stage_key, attempt, position, name, state,
				started_at, completed_at, detail, reconstructed, source_event_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid, st.StageKey, st.Attempt, st.Position, st.Name, string(st.State),
			nullTime(st.StartedAt), nullTime(st.CompletedAt), nullString(st.Detail), boolInt(st.Reconstructed), sourceEvent,
		); err != nil {
			return fmt.Errorf("insert step %s/%d/%d: %w", st.StageKey, st.Attempt, st.Position, err)
		}
	}

	if len(p.EventIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(p.EventIDs)), ",")
		args := []any{uuid}
		for _, id := range p.EventIDs {
			args = append(args, id)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE events SET release_uuid = ? WHERE id IN (`+placeholders+`)`, args...); err != nil {
			return fmt.Errorf("link events: %w", err)
		}
	}
	return tx.Commit()
}
