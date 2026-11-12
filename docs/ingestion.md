# Ingestion contracts

mybuilds accepts three kinds of push event. Each endpoint authenticates the
request before parsing it, stores one normalized event per delivery, and
re-projects the releases the event touches. See the plan, sections 8, 13 and
14.

| Endpoint | Sender | Authentication | Idempotency key |
| --- | --- | --- | --- |
| `POST /hooks/github` | GitHub repository webhook | `X-Hub-Signature-256` HMAC | `X-GitHub-Delivery` |
| `POST /api/v1/ci-events` | Release workflow steps | `X-Mybuilds-Signature-256` HMAC | `<type>/<release_id>/<run_attempt>` |
| `POST /hooks/argocd` | Argo CD Notifications | `Authorization: Bearer <token>` | SHA-256 of the body |

Responses:

| Status | Meaning |
| --- | --- |
| `202` | Stored. The body names the event ID and the releases it touched. |
| `200` | Valid but not stored: a duplicate delivery, or one mybuilds does not track (`ping`, a pull request run, another workflow). The body gives the reason. |
| `401` | Missing or wrong signature or token. Nothing is parsed. |
| `413` | Body over 1 MiB. |
| `422` | Authenticated but invalid, such as a release ID that does not match its fields. |
| `503` | The endpoint's secret is not configured. |

A stored event is acknowledged even if re-projecting fails; the failure is
logged, counted in `mybuilds_projection_errors_total`, and repaired by the
next event of that release or by the rebuild at startup.

## GitHub

Repository webhook on each tracked source repository:

- Payload URL: `https://mybuilds.brendanmanley.com/hooks/github`
- Content type: `application/json`
- Secret: the value of `MYBUILDS_GITHUB_WEBHOOK_SECRET`
- Events: **Workflow runs** and **Workflow jobs** only

Only runs of the workflow named in the service map, triggered by a push to
its branch, become releases. Jobs are linked to their release by run ID.
Runner names and IDs, log URLs and HTML URLs are dropped before storage.

## CI-signed events

The release workflow sends one JSON body per event. Unknown fields are
rejected, so a typo fails the step instead of losing data. `release_id`
must match `service`, `environment`, the short source SHA and the run
number, and the service must be in the service map.

`release.published`, after the image is pushed and its digest resolved:

```json
{
  "type": "release.published",
  "release_id": "personal-site/ce61154/production/11",
  "service": "personal-site",
  "environment": "production",
  "source_repository": "spicyhek/homelab-hosted-personal-site",
  "source_sha": "ce61154e05756b0a7e40311de30dccaa0273f305",
  "artifact": "ghcr.io/spicyhek/homelab-hosted-personal-site",
  "artifact_digest": "sha256:9785af09091041a492b423d2aa88d98df1f980b5a056c055590d4b889bf092a5",
  "workflow_run_id": 42475748808,
  "workflow_run_number": 11,
  "run_attempt": 1,
  "drill": false,
  "occurred_at": "2026-11-08T11:27:02Z"
}
```

`drill_scenario` is optional and only meaningful with `"drill": true`.

`gitops.updated`, after the bump pull request is squash-merged.
`gitops_sha` is the squash commit on `main`, the revision Argo CD syncs:

```json
{
  "type": "gitops.updated",
  "release_id": "personal-site/ce61154/production/11",
  "service": "personal-site",
  "environment": "production",
  "gitops_repository": "spicyhek/homelab-infra",
  "gitops_sha": "f8c9de901999c2984662a926a18e31a531fd7821",
  "workflow_run_id": 42475748808,
  "run_attempt": 1,
  "occurred_at": "2026-11-08T11:27:28Z"
}
```

`gitops.superseded`, when the bump job skips the promotion because a newer
commit is on the source branch. `superseded_by` is optional:

```json
{
  "type": "gitops.superseded",
  "release_id": "personal-site/ce61154/production/11",
  "service": "personal-site",
  "environment": "production",
  "superseded_by": "0123456789abcdef0123456789abcdef01234567",
  "workflow_run_id": 42475748808,
  "run_attempt": 1,
  "occurred_at": "2026-11-08T11:27:18Z"
}
```

Sending one from a workflow step. The body is signed exactly as sent, and
`--retry` is safe because a resend of the same event is a duplicate:

```bash
set -euo pipefail
body="$(jq -cn \
  --arg release_id "${RELEASE_ID}" \
  --arg sha "${GITHUB_SHA}" \
  --arg digest "${DIGEST}" \
  --argjson run_id "${GITHUB_RUN_ID}" \
  --argjson run_number "${GITHUB_RUN_NUMBER}" \
  --argjson attempt "${GITHUB_RUN_ATTEMPT}" \
  --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{type: "release.published", release_id: $release_id,
    service: "personal-site", environment: "production",
    source_repository: "spicyhek/homelab-hosted-personal-site", source_sha: $sha,
    artifact: "ghcr.io/spicyhek/homelab-hosted-personal-site", artifact_digest: $digest,
    workflow_run_id: $run_id, workflow_run_number: $run_number, run_attempt: $attempt,
    drill: false, occurred_at: $now}')"
sig="sha256=$(printf '%s' "${body}" | openssl dgst -sha256 -hmac "${MYBUILDS_CI_EVENT_SECRET}" -r | cut -d' ' -f1)"
curl --fail-with-body --silent --show-error \
  --retry 5 --retry-all-errors --retry-delay 2 \
  -H "Content-Type: application/json" \
  -H "X-Mybuilds-Signature-256: ${sig}" \
  --data-binary "${body}" \
  https://mybuilds.brendanmanley.com/api/v1/ci-events
```

## Argo CD Notifications

Draft configuration for `argocd-notifications-cm`, to be verified when the
notifications are wired up. Argo CD templates do not know which trigger fired,
so each trigger gets its own template that names it. Only the service's own
Application counts as evidence; syncs of the same commit by the root
`homelab-apps` Application are stored but ignored.

```yaml
service.webhook.mybuilds: |
  url: https://mybuilds.brendanmanley.com/hooks/argocd
  headers:
    - name: Content-Type
      value: application/json
    - name: Authorization
      value: Bearer $mybuilds-argocd-token

template.mybuilds-on-sync-succeeded: |
  webhook:
    mybuilds:
      method: POST
      body: |
        {
          "trigger": "on-sync-succeeded",
          "application": "{{.app.metadata.name}}",
          "revision": "{{.app.status.sync.revision}}",
          "sync_status": "{{.app.status.sync.status}}",
          "health_status": "{{.app.status.health.status}}",
          "operation_phase": "{{.app.status.operationState.phase}}",
          "started_at": "{{.app.status.operationState.startedAt}}",
          "finished_at": "{{.app.status.operationState.finishedAt}}",
          "commit_message": {{toJson (call .repo.GetCommitMetadata .app.status.sync.revision).Message}},
          "commit_author": {{toJson (call .repo.GetCommitMetadata .app.status.sync.revision).Author}}
        }
```

`template.mybuilds-on-sync-running`, `-on-sync-failed` and
`-on-health-degraded` are the same with their trigger name. The Application
subscribes with annotations such as
`notifications.argoproj.io/subscribe.on-sync-succeeded.mybuilds: ""`, with
triggers whose `send` lists the matching template.

The payload fields:

| Field | Required | Notes |
| --- | --- | --- |
| `trigger` | yes | `on-sync-running`, `on-sync-succeeded`, `on-sync-failed` or `on-health-degraded` |
| `application` | yes | Argo CD Application name; compared with the service map |
| `revision` | yes | Full GitOps commit SHA |
| `sync_status`, `health_status`, `operation_phase` | no | Shown in the Sync step |
| `started_at`, `finished_at` | no | RFC 3339; empty, `<nil>` or `<no value>` mean absent |
| `commit_message` | no | Only the subject line and a `Release-Id:` trailer are kept |
| `commit_author` | no | For display of config releases (plan section 8.7) |

`on-health-degraded` is stored for the Rollout stage (M2) and does not change
Reconcile.
