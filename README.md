# mybuilds

Self-hosted delivery tracking for the homelab K3s cluster. One release is
followed from commit to image digest to GitOps commit to running pod to a
verified public response, with each stage backed by its own evidence. A green
CI run, an Argo CD sync, a Ready pod and a passing public check are four
separate facts; mybuilds never treats one as proof of another.

**Status: M2, Rollout stage.** The server receives signed GitHub, CI and
Argo CD events, watches its workloads through read-only Kubernetes informers,
stores each delivery and observation once, projects releases from Source
through Rollout, streams changes over SSE and serves the React pipeline view.
It runs on the cluster it watches. Verify and config-change releases are
next. `demo/index.html` is the standalone UI prototype the view was built
from.

## Layout

```text
cmd/server             entry point: two listeners, graceful shutdown
internal/config        MYBUILDS_* environment variables
internal/storage       SQLite (WAL), versioned migrations, release queries
internal/events        normalized event types
internal/ingest        authenticate, translate, store, re-project
  github, ci, argocd   one translator per provider
  verify               HMAC and bearer-token checks
internal/kube          read-only informers: Deployments, ReplicaSets, Pods
internal/releases      release model, correlation and the projector
internal/services      service map: repositories, workflows, jobs, apps
internal/httpapi       health, read API, SSE stream, ingestion routes, static files, metrics
internal/fixtures      sample releases and the recorded release replayed in fixture mode
web/                   React + TypeScript frontend (Vite); dist/ is embedded into the binary
docs/ingestion.md      the three ingestion contracts
docs/runtime.md        the runtime watch and the Rollout stage
demo/                  standalone UI prototype
```

Planned, not yet present: `internal/ingest/queue` (SQS consumer),
`internal/verify`, `worker/` (Cloudflare Worker) and `drill/`.

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
`gitops.superseded`), Reconcile on the service's own Argo CD sync, Rollout on
the workload's pods running the release digest. A stage with no evidence
before one that has started shows `unknown`.

Rollout evidence is attributed through Argo CD, not through pod annotations:
a release owns its workload's runtime events from the sync of its GitOps
commit until the Application's next sync. See
[`docs/runtime.md`](docs/runtime.md).

## Run locally

Build the frontend once (Node 22), then run the server:

```bash
(cd web && npm ci && npm run build)
MYBUILDS_FIXTURES=true go run ./cmd/server
```

PowerShell:

```powershell
cd web; npm ci; npm run build; cd ..
$env:MYBUILDS_FIXTURES = 'true'; go run ./cmd/server
```

The UI and API are on <http://localhost:8080>, metrics on
<http://localhost:9090/metrics>. The database is created at `data/mybuilds.db`.
Without a frontend build the API still works and pages answer 404.

Fixture mode inserts two sample releases and then replays the recorded
`personal-site` release #11 through the real ingestion path, with its
timestamps shifted to now and the real gaps between deliveries (long waits
capped at 20 s), so the pipeline view moves live about five seconds after
startup. The replay is skipped when that release is already in the database;
delete the database file to see it again.

For frontend work, `npm run dev` in `web/` serves the app on
<http://localhost:5173> with hot reload and proxies `/api`, `/hooks` and the
health endpoints to the Go server on :8080.

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
| `MYBUILDS_RUNTIME_WATCH` | `auto` | `auto` watches Kubernetes when running in a cluster; `off` disables it |
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
| `GET /api/v1/stream` | Server-Sent Events: `hello`, then `release.created` and `release.updated` with the full release view. The SSE `id` is the event ID; a reconnect with `Last-Event-ID` replays what was missed, or sends `snapshot` when the cursor is too old |
| `GET /api/v1/meta` | Version, release ID, fixture mode and the public parts of the service map |
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

The Dockerfile builds the frontend in a Node stage, so no local `npm run
build` is needed for the image. The image is `distroless/static`, runs as
`nonroot` (uid 65532) and keeps its database in `/data`. When a volume is mounted there, set `fsGroup: 65532` on
the pod. SQLite comes from the pure-Go `modernc.org/sqlite`, so the build has
`CGO_ENABLED=0` and cross-compiles for the cluster's arm64 nodes.

## Tests

```bash
go test ./...
```

`internal/ingest` replays the real `personal-site` release #11 from recorded
deliveries: in live order, again as duplicates, and reversed and shuffled,
and checks that the release is the same each time.

CI (`.github/workflows/release.yml`) builds the frontend (which type-checks
it), then runs `gofmt`, `go vet` and the tests on every pull request. On `main` it also builds the image, runs a smoke test
against it, pushes `linux/amd64` and `linux/arm64` to
`ghcr.io/spicyhek/mybuilds` and records the digest.

The `bump-gitops` job then promotes that digest the same way personal-site
does: it pins it in `homelab-infra/k8s/apps/mybuilds/kustomization.yml`,
rewrites `release-patch.yml` with the release ID, opens a pull request and
squash-merges it as `Deploy mybuilds sha-...` with a `Release-Id:` trailer,
and Argo CD syncs it. It needs the `INFRA_REPO_TOKEN` Actions secret, a
fine-grained token limited to `homelab-infra` with `contents: write` and
`pull-requests: write`. A run whose commit is no longer the head of `main`
skips the promotion, and until the manifests exist on `homelab-infra` main the
job only warns, so pushes before the first deploy publish an image without
promoting it.
