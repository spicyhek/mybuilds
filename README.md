# mybuilds

Self-hosted delivery tracking for the homelab K3s cluster. One release is
followed from commit to image digest to GitOps commit to running pod to a
verified public response, with each stage backed by its own evidence. A green
CI run, an Argo CD sync, a Ready pod and a passing public check are four
separate facts; mybuilds never treats one as proof of another.

**Status: M1 skeleton.** The server starts, migrates its SQLite schema, serves
health and metrics endpoints and a read API, and can load fixture releases.
Webhook ingestion, the projector, the SSE stream and the pipeline UI are next.
`demo/index.html` is the standalone UI prototype with fixture data.

## Layout

```text
cmd/server             entry point: two listeners, graceful shutdown
internal/config        MYBUILDS_* environment variables
internal/storage       SQLite (WAL), versioned migrations, release queries
internal/releases      release, stage and state model
internal/httpapi       health, read API, static files, request metrics
internal/fixtures      sample releases for fixture mode
web/                   frontend embedded into the binary (placeholder for now)
demo/                  standalone UI prototype
```

Planned, not yet present: `internal/ingest/{github,argocd,ci,queue}`,
`internal/runtime` (Kubernetes informers), `internal/verify`,
`internal/events`, `worker/` (Cloudflare Worker) and `drill/`.

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

Fixture mode is a runtime switch rather than a build flag so that one image
serves production, screenshots and the CI smoke test.

## Endpoints

| Method and path | Purpose |
| --- | --- |
| `GET /healthz` | Liveness. Returns `X-Release-ID` and `Cache-Control: no-store` |
| `GET /readyz` | Readiness. 503 when the database does not answer |
| `GET /api/v1/releases?limit=N` | Newest releases first, with stages. `next_cursor` is null until cursor pagination lands |
| `GET /api/v1/releases/{release_id}` | One release. IDs contain slashes, e.g. `personal-site/ce61154/production/11` |
| `GET /metrics` | Prometheus metrics, on the metrics listener only |
| `GET /*` | Frontend, with `index.html` fallback for client-side routes |

API responses are built from an explicit allowlist of fields; internal IDs
and raw provider payloads are never returned.

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

CI (`.github/workflows/release.yml`) runs `gofmt`, `go vet` and the tests on
every pull request. On `main` it also builds the image, runs a smoke test
against it, pushes `linux/amd64` and `linux/arm64` to
`ghcr.io/spicyhek/mybuilds` and records the digest.
