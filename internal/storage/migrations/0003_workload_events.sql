-- Kubernetes runtime events (plan section 8.4). An event's workload is the
-- Deployment it describes, as namespace/name; the projector reads a
-- workload's events between one Argo CD sync and the next.
ALTER TABLE events ADD COLUMN workload TEXT;

CREATE INDEX events_workload ON events (workload, occurred_at) WHERE workload IS NOT NULL;
