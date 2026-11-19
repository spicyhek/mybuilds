-- The GitHub job poller (docs/ingestion.md) looks for recent workflow_job
-- events every few seconds.
CREATE INDEX events_type_received ON events (type, received_at);
