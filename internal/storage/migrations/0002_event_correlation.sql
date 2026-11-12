-- Correlation keys on events, so the projector can find every event of a
-- release (plan section 8, correlation table), and the superseded label.

-- The release ID an event names directly: CI events, and workflow runs of a
-- workflow in the service map. Unlike release_uuid, it is known at ingestion.
ALTER TABLE events ADD COLUMN release_key TEXT;
-- GitHub Actions run ID. Links workflow_job events to their release.
ALTER TABLE events ADD COLUMN run_id INTEGER;
-- GitOps commit SHA. Links Argo CD syncs to the release that made the commit.
ALTER TABLE events ADD COLUMN revision TEXT;

CREATE INDEX events_release_key ON events (release_key) WHERE release_key IS NOT NULL;
CREATE INDEX events_run_id      ON events (run_id)      WHERE run_id IS NOT NULL;
CREATE INDEX events_revision    ON events (revision)    WHERE revision IS NOT NULL;

-- A release whose promotion was skipped because a newer commit was already on
-- the source branch (plan section 8.1). Not a failure.
ALTER TABLE releases ADD COLUMN superseded INTEGER NOT NULL DEFAULT 0 CHECK (superseded IN (0, 1));
