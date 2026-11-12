package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/spicyhek/mybuilds/internal/releases"
)

const releaseColumns = `uuid, release_id, service, environment, kind, template,
	deployed_with, drill, drill_scenario, superseded,
	source_repository, source_sha, artifact, artifact_digest, gitops_repository, gitops_sha,
	status, created_at, completed_at, verified_at`

// releaseValues returns every column of releaseColumns except uuid, in order.
func releaseValues(r releases.Release) []any {
	return []any{
		r.ReleaseID, r.Service, r.Environment, string(r.Kind), r.Template,
		nullString(r.DeployedWith), boolInt(r.Drill), nullString(r.DrillScenario), boolInt(r.Superseded),
		nullString(r.SourceRepository), nullString(r.SourceSHA), nullString(r.Artifact), nullString(r.ArtifactDigest),
		nullString(r.GitOpsRepository), nullString(r.GitOpsSHA),
		string(r.Status), formatTime(r.CreatedAt), nullTime(r.CompletedAt), nullTime(r.VerifiedAt),
	}
}

// InsertRelease stores a release and its stages. A release whose release_id
// already exists is left untouched and false is returned. Fixtures use it;
// real releases are written by the projector through SaveProjection.
func (s *Store) InsertRelease(ctx context.Context, r releases.Release) (inserted bool, err error) {
	if r.UUID == "" || r.ReleaseID == "" {
		return false, errors.New("release uuid and release_id are required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	// Rollback after a successful Commit is a no-op; this also ends the
	// transaction on the duplicate path, so no connection is left open.
	defer tx.Rollback()

	// ON CONFLICT DO NOTHING only covers the uniqueness of release_id.
	// Unlike INSERT OR IGNORE, a CHECK violation still fails loudly.
	res, err := tx.ExecContext(ctx, `
		INSERT INTO releases (`+releaseColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (release_id) DO NOTHING`,
		append([]any{r.UUID}, releaseValues(r)...)...,
	)
	if err != nil {
		return false, fmt.Errorf("insert release %s: %w", r.ReleaseID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := insertStages(ctx, tx, r.UUID, r); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func insertStages(ctx context.Context, tx *sql.Tx, uuid string, r releases.Release) error {
	for i, st := range r.Stages {
		position := st.Position
		if position == 0 {
			position = i + 1
		}
		attempt := st.Attempt
		if attempt == 0 {
			attempt = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO release_stages
				(release_uuid, stage_key, position, state, attempt, started_at, completed_at, summary, failure_reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			uuid, st.Key, position, string(st.State), attempt,
			nullTime(st.StartedAt), nullTime(st.CompletedAt), nullString(st.Summary), nullString(st.FailureReason),
		); err != nil {
			return fmt.Errorf("insert stage %s/%s: %w", r.ReleaseID, st.Key, err)
		}
	}
	return nil
}

// ListReleases returns the newest releases first, with their stages.
func (s *Store) ListReleases(ctx context.Context, limit int) ([]releases.Release, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+releaseColumns+` FROM releases
		ORDER BY created_at DESC, release_id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	defer rows.Close()

	var out []releases.Release
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	rows.Close()

	for i := range out {
		if out[i].Stages, err = s.loadStages(ctx, out[i].UUID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GetRelease returns one release by its external release ID, with its stages
// and the steps of each stage's current attempt.
func (s *Store) GetRelease(ctx context.Context, releaseID string) (*releases.Release, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+releaseColumns+` FROM releases WHERE release_id = ?`, releaseID)
	r, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if r.Stages, err = s.loadStages(ctx, r.UUID); err != nil {
		return nil, err
	}
	steps, err := s.loadSteps(ctx, r.UUID)
	if err != nil {
		return nil, err
	}
	for i := range r.Stages {
		for _, st := range steps {
			if st.StageKey == r.Stages[i].Key && st.Attempt == r.Stages[i].Attempt {
				r.Stages[i].Steps = append(r.Stages[i].Steps, st)
			}
		}
	}
	return &r, nil
}

func (s *Store) loadStages(ctx context.Context, uuid string) ([]releases.Stage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stage_key, position, state, attempt, started_at, completed_at, summary, failure_reason
		FROM release_stages WHERE release_uuid = ? ORDER BY position`, uuid)
	if err != nil {
		return nil, fmt.Errorf("load stages: %w", err)
	}
	defer rows.Close()

	stages := []releases.Stage{}
	for rows.Next() {
		var st releases.Stage
		var state string
		var startedAt, completedAt, summary, failureReason sql.NullString
		if err := rows.Scan(&st.Key, &st.Position, &state, &st.Attempt, &startedAt, &completedAt, &summary, &failureReason); err != nil {
			return nil, fmt.Errorf("scan stage: %w", err)
		}
		st.State = releases.State(state)
		st.Summary = summary.String
		st.FailureReason = failureReason.String
		if st.StartedAt, err = parseNullTime(startedAt); err != nil {
			return nil, err
		}
		if st.CompletedAt, err = parseNullTime(completedAt); err != nil {
			return nil, err
		}
		stages = append(stages, st)
	}
	return stages, rows.Err()
}

func (s *Store) loadSteps(ctx context.Context, uuid string) ([]releases.Step, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT stage_key, attempt, position, name, state, started_at, completed_at, detail, reconstructed, source_event_id
		FROM release_steps WHERE release_uuid = ? ORDER BY stage_key, attempt, position`, uuid)
	if err != nil {
		return nil, fmt.Errorf("load steps: %w", err)
	}
	defer rows.Close()

	var steps []releases.Step
	for rows.Next() {
		var st releases.Step
		var state string
		var startedAt, completedAt, detail sql.NullString
		var reconstructed int
		var sourceEvent sql.NullInt64
		if err := rows.Scan(&st.StageKey, &st.Attempt, &st.Position, &st.Name, &state,
			&startedAt, &completedAt, &detail, &reconstructed, &sourceEvent); err != nil {
			return nil, fmt.Errorf("scan step: %w", err)
		}
		st.State = releases.State(state)
		st.Detail = detail.String
		st.Reconstructed = reconstructed == 1
		st.SourceEventID = sourceEvent.Int64
		if st.StartedAt, err = parseNullTime(startedAt); err != nil {
			return nil, err
		}
		if st.CompletedAt, err = parseNullTime(completedAt); err != nil {
			return nil, err
		}
		steps = append(steps, st)
	}
	return steps, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRelease(sc rowScanner) (releases.Release, error) {
	var r releases.Release
	var kind, status, createdAt string
	var drill, superseded int
	var deployedWith, drillScenario, sourceRepo, sourceSHA, artifact, digest, gitopsRepo, gitopsSHA, completedAt, verifiedAt sql.NullString

	if err := sc.Scan(
		&r.UUID, &r.ReleaseID, &r.Service, &r.Environment, &kind, &r.Template,
		&deployedWith, &drill, &drillScenario, &superseded,
		&sourceRepo, &sourceSHA, &artifact, &digest, &gitopsRepo, &gitopsSHA,
		&status, &createdAt, &completedAt, &verifiedAt,
	); err != nil {
		return r, err
	}

	r.Kind = releases.Kind(kind)
	r.Status = releases.State(status)
	r.Drill = drill == 1
	r.Superseded = superseded == 1
	r.DeployedWith = deployedWith.String
	r.DrillScenario = drillScenario.String
	r.SourceRepository = sourceRepo.String
	r.SourceSHA = sourceSHA.String
	r.Artifact = artifact.String
	r.ArtifactDigest = digest.String
	r.GitOpsRepository = gitopsRepo.String
	r.GitOpsSHA = gitopsSHA.String

	var err error
	if r.CreatedAt, err = parseTime(createdAt); err != nil {
		return r, err
	}
	if r.CompletedAt, err = parseNullTime(completedAt); err != nil {
		return r, err
	}
	if r.VerifiedAt, err = parseNullTime(verifiedAt); err != nil {
		return r, err
	}
	return r, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// timeLayout is RFC 3339 with a fixed-width fraction, so that timestamps
// stored as text sort in time order (RFC3339Nano trims trailing zeros).
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", s, err)
	}
	return t, nil
}

func parseNullTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
