package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
)

// RunningJob is a workflow job of a release run that a webhook reported
// started and none reported completed.
type RunningJob struct {
	Repository string
	JobID      int64
	JobName    string
	RunID      int64
	RunAttempt int
}

// RunningJobs returns the jobs of release runs whose workflow_job events
// received since since include an in_progress and no completed one. Jobs
// of runs that are not releases, such as pull request runs, are left out.
func (s *Store) RunningJobs(ctx context.Context, since time.Time) ([]RunningJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH jobs AS (
			SELECT json_extract(payload, '$.job_id') AS job_id,
				json_extract(payload, '$.repository') AS repository,
				json_extract(payload, '$.job_name') AS job_name,
				json_extract(payload, '$.run_attempt') AS run_attempt,
				run_id,
				MAX(CASE json_extract(payload, '$.status')
					WHEN 'completed' THEN 2 WHEN 'in_progress' THEN 1 ELSE 0 END) AS rank
			FROM events
			WHERE type = ?1 AND received_at >= ?2 AND run_id IS NOT NULL
			GROUP BY job_id
		)
		SELECT repository, job_id, job_name, run_id, COALESCE(run_attempt, 1)
		FROM jobs
		WHERE rank = 1 AND COALESCE(repository, '') != '' AND job_id IS NOT NULL
			AND EXISTS (SELECT 1 FROM events r WHERE r.run_id = jobs.run_id AND r.release_key IS NOT NULL)
		ORDER BY job_id`, events.TypeWorkflowJob, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("running jobs: %w", err)
	}
	defer rows.Close()
	var out []RunningJob
	for rows.Next() {
		var j RunningJob
		if err := rows.Scan(&j.Repository, &j.JobID, &j.JobName, &j.RunID, &j.RunAttempt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
