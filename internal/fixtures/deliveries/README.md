# Recorded deliveries

`personal-site-11/` is the real `personal-site` release
`personal-site/ce61154/production/11` (2026-11-08), one file per delivery in
live order. Each file holds the provider, the request headers and the body;
tests sign the body themselves.

- **GitHub** deliveries were built from the GitHub API's run and jobs JSON for
  run 42475748808, which has the same shape as the `workflow_run` and
  `workflow_job` webhook objects. The `requested` and `in_progress` copies
  differ from the final objects only in status, conclusion and step progress.
- **CI** events use the run's real digest and the real squash commit
  `f8c9de9` in `homelab-infra`. The workflow did not send them yet; their
  timestamps are the end of the `Resolve digest` and `Squash merge` steps.
- **Argo CD** notifications are synthesized for the same commit, including
  one from the root `homelab-apps` Application, which must not count as this
  release's sync.

Fields that ingestion must drop (runner names and IDs, log and HTML URLs)
are left in on purpose; `TestStoredPayloadsAreAllowlisted` checks they never
reach the database.
