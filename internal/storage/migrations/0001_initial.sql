-- Initial schema. See the plan, section 12.
-- Timestamps are RFC 3339 text in UTC. Booleans are 0 or 1.

CREATE TABLE releases (
    uuid              TEXT    PRIMARY KEY,
    release_id        TEXT    NOT NULL UNIQUE,
    service           TEXT    NOT NULL,
    environment       TEXT    NOT NULL,
    kind              TEXT    NOT NULL CHECK (kind IN ('application', 'config')),
    template          TEXT    NOT NULL,
    deployed_with     TEXT    REFERENCES releases (uuid),
    drill             INTEGER NOT NULL DEFAULT 0 CHECK (drill IN (0, 1)),
    drill_scenario    TEXT,
    source_repository TEXT,
    source_sha        TEXT,
    artifact          TEXT,
    artifact_digest   TEXT,
    gitops_repository TEXT,
    gitops_sha        TEXT,
    status            TEXT    NOT NULL,
    created_at        TEXT    NOT NULL,
    completed_at      TEXT,
    verified_at       TEXT
);

CREATE INDEX releases_created_at ON releases (created_at DESC);
CREATE INDEX releases_service    ON releases (service, created_at DESC);
CREATE INDEX releases_gitops_sha ON releases (gitops_sha);

-- Every observation, immutable. The raw body is hashed, never stored.
CREATE TABLE events (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    provider             TEXT    NOT NULL,
    provider_delivery_id TEXT    NOT NULL,
    type                 TEXT    NOT NULL,
    release_uuid         TEXT    REFERENCES releases (uuid),
    stage_key            TEXT,
    attempt              INTEGER NOT NULL DEFAULT 1,
    occurred_at          TEXT    NOT NULL,
    received_at          TEXT    NOT NULL,
    payload              TEXT    NOT NULL CHECK (json_valid(payload)),
    body_sha256          TEXT    NOT NULL,
    UNIQUE (provider, provider_delivery_id)
);

CREATE INDEX events_release      ON events (release_uuid, id);
CREATE INDEX events_uncorrelated ON events (received_at) WHERE release_uuid IS NULL;

-- Projection of events onto stages. Rebuilt from events.
CREATE TABLE release_stages (
    release_uuid   TEXT    NOT NULL REFERENCES releases (uuid) ON DELETE CASCADE,
    stage_key      TEXT    NOT NULL,
    position       INTEGER NOT NULL,
    state          TEXT    NOT NULL CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'degraded', 'skipped', 'unknown')),
    attempt        INTEGER NOT NULL DEFAULT 1,
    started_at     TEXT,
    completed_at   TEXT,
    summary        TEXT,
    failure_reason TEXT,
    PRIMARY KEY (release_uuid, stage_key)
);

-- Projection of events onto steps within a stage. Rebuilt from events.
CREATE TABLE release_steps (
    release_uuid    TEXT    NOT NULL REFERENCES releases (uuid) ON DELETE CASCADE,
    stage_key       TEXT    NOT NULL,
    attempt         INTEGER NOT NULL,
    position        INTEGER NOT NULL,
    name            TEXT    NOT NULL,
    state           TEXT    NOT NULL CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'degraded', 'skipped', 'unknown')),
    started_at      TEXT,
    completed_at    TEXT,
    detail          TEXT,
    reconstructed   INTEGER NOT NULL DEFAULT 0 CHECK (reconstructed IN (0, 1)),
    source_event_id INTEGER REFERENCES events (id),
    PRIMARY KEY (release_uuid, stage_key, attempt, position)
);

CREATE TABLE verification_samples (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    release_uuid        TEXT    NOT NULL REFERENCES releases (uuid) ON DELETE CASCADE,
    endpoint            TEXT    NOT NULL,
    ok                  INTEGER NOT NULL CHECK (ok IN (0, 1)),
    status_code         INTEGER,
    latency_ms          INTEGER,
    observed_release_id TEXT,
    health_ok           INTEGER CHECK (health_ok IN (0, 1)),
    checked_at          TEXT    NOT NULL,
    failure_reason      TEXT
);

CREATE INDEX verification_samples_release ON verification_samples (release_uuid, checked_at);
