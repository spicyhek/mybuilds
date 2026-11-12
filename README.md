# mybuilds

Self-hosted delivery tracking for the homelab K3s cluster. One release is
followed from commit to image digest to GitOps commit to running pod to a
verified public response, with each stage backed by its own evidence. A green
CI run, an Argo CD sync, a Ready pod and a passing public check are four
separate facts; mybuilds never treats one as proof of another.

**Status: M1, ingestion.** The server receives signed GitHub, CI and Argo CD
events, stores each delivery once, and projects releases from Source through
Reconcile. The SSE stream, the pipeline UI and deployment are next.
`demo/index.html` is the standalone UI prototype with fixture data.

## Layout

```text
cmd/server             entry point: two listeners, graceful shutdown
internal/config        MYBUILDS_* environment variables
internal/storage       SQLite (WAL), versioned migrations, release queries
internal/events        normalized event types
internal/ingest        authenticate, translate, store, re-project
  github, ci, argocd   one translator per provider
  verify               HMAC and bearer-token checks
internal/releases      release model, correlation and the projector
internal/services      service map: repositories, workflows, jobs, apps
internal/httpapi       health, read API, ingestion routes, static files, metrics
internal/fixtures      sample releases for fixture mode
docs/ingestion.md      the three ingestion contracts
web/                   frontend embedded into the binary (placeholder for now)
demo/                  standalone UI prototype
```

Planned, not yet present: `internal/ingest/queue` (SQS consumer),
`internal/runtime` (Kubernetes informers), `internal/verify`,
`worker/` (Cloudflare Worker) and `drill/`.

## How events become releases

Every delivery is authenticated, translated into an allowlisted event and
stored once; `(provider, delivery ID)` is unique, so redeliveries are dropped.
The projector then rebuilds each affected release from all of its events,
ordered by provider timestamp rather than arrival, so out-of-order and
duplicate deliveries give the same result. Events find their release through
three keys: the release ID (CI events and workflow runs), the GitHub run ID
(workflow jobs) and the GitOps commit (Argo CD syncs). At startup every
release is rebuilt from its events, so a projector change applies to history.

Each stage completes only on its own evidence (plan section 5.5): Source on
the workflow run, Build on the signed release manifest or its job's steps,
Publish on `release.published`, GitOps on `gitops.updated` (or is skipped on
`gitops.superseded`), Reconcile on the service's own Argo CD sync. A stage
with no evidence before one that has started shows `unknown`.

## Run locally

```bash
MYBUILDS_FIXTURES=true go run ./cmd/server
```

PowerShell:

```powershell
$env:MYBUILDS_FIXTURES = 'true'; go run ./cmd/server
```

The UI and API are on <http://localhost:8080>, metrics on
<http://localhost:9090/metrics>. The database is created at `data/mybuilds.db`.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `MYBUILDS_LISTEN` | `:8080` | Public listener: UI, read API, `/healthz`, `/readyz` |
| `MYBUILDS_METRICS_LISTEN` | `:9090` | Cluster-internal `/metrics` |
| `MYBUILDS_DB_PATH` | `data/mybuilds.db` | SQLite file; the directory is created |
| `MYBUILDS_FIXTURES` | `false` | Insert sample releases at startup |
| `MYBUILDS_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `RELEASE_ID` | `unknown` | This deployment's release ID, returned as `X-Release-ID` on `/healthz` |
| `MYBUILDS_GITHUB_WEBHOOK_SECRET` | unset | Enables `POST /hooks/github` |
| `MYBUILDS_CI_EVENT_SECRET` | unset | Enables `POST /api/v1/ci-events` |
| `MYBUILDS_ARGOCD_TOKEN` | unset | Enables `POST /hooks/argocd` |
| `MYBUILDS_SERVICES_FILE` | built-in | Service map JSON; the built-in one is `internal/services/services.json` |

An ingestion endpoint whose secret is unset answers `503`. Secrets are never
logged.

Fixture mode is a runtime switch rather than a build flag so that one image
serves production, screenshots and the CI smoke test.

## Endpoints

| Method and path | Purpose |
| --- | --- |
| `GET /healthz` | Liveness. Returns `X-Release-ID` and `Cache-Control: no-store` |
| `GET /readyz` | Readiness. 503 when the database does not answer |
| `GET /api/v1/releases?limit=N` | Newest releases first, with stages. `next_cursor` is null until cursor pagination lands |
| `GET /api/v1/releases/{release_id}` | One release with each stage's steps. IDs contain slashes, e.g. `personal-site/ce61154/production/11` |
| `POST /hooks/github` | GitHub `workflow_run` and `workflow_job` webhooks |
| `POST /api/v1/ci-events` | CI-signed `release.published`, `gitops.updated`, `gitops.superseded` |
| `POST /hooks/argocd` | Argo CD Notifications |
| `GET /metrics` | Prometheus metrics, on the metrics listener only |
| `GET /*` | Frontend, with `index.html` fallback for client-side routes |

API responses are built from an explicit allowlist of fields; internal IDs
and raw provider payloads are never returned. The ingestion payloads,
signatures and status codes are in [docs/ingestion.md](docs/ingestion.md).

## Container

```bash
docker build -t mybuilds .
docker run --rm -p 8080:8080 -p 9090:9090 -e MYBUILDS_FIXTURES=true mybuilds
```

The image is `distroless/static`, runs as `nonroot` (uid 65532) and keeps its
database in `/data`. When a volume is mounted there, set `fsGroup: 65532` on
the pod. SQLite comes from the pure-Go `modernc.org/sqlite`, so the build has
`CGO_ENABLED=0` and cross-compiles for the cluster's arm64 nodes.

## Tests

```bash
go test ./...
```

`internal/ingest` replays the real `personal-site` release #11 from recorded
deliveries: in live order, again as duplicates, and reversed and shuffled,
and checks that the release is the same each time.

CI (`.github/workflows/release.yml`) runs `gofmt`, `go vet` and the tests on
every pull request. On `main` it also builds the image, runs a smoke test
against it, pushes `linux/amd64` and `linux/arm64` to
`ghcr.io/spicyhek/mybuilds` and records the digest.
