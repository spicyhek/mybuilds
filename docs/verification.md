# Verifier and the Verify stage

After a release's Rollout succeeds, mybuilds checks the service's public
health URL through its public route, Cloudflare included, and stores each
result as an event. The Verify stage is projected from those events. See the
plan, section 8.5.

## Configuration

A service opts in with a `verify` entry in the service map:

```json
"verify": {
  "url": "https://brendanmanley.com/healthz",
  "max_latency_ms": 3000
}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `url` | required | An `https` URL, normally the public `/healthz` |
| `allowed_status` | `[200]` | Status codes that count as up |
| `max_latency_ms` | `2000` | Slowest acceptable response, body included |
| `window_seconds` | `600` | How long after Rollout completes the checks may run |
| `health_body` | none | `{"field": "status", "value": "ok"}`: a top-level JSON field that must equal the value |

Without `verify`, Verify is skipped once Rollout succeeds.

A config release's checks expect the `X-Release-ID` of the application
release its pods carry in their `release-id` annotation, not the config
release's own ID. See [`config-releases.md`](config-releases.md).
`MYBUILDS_VERIFIER=off` stops the checks; fixture mode never runs them.

## One check

Each check opens a new connection, so DNS and TLS are part of every check.
Redirects are not followed. It records:

- DNS and TLS: whether they succeeded, the TLS version and the certificate's
  expiry;
- status code and latency;
- the `X-Release-ID` header;
- the Cloudflare location from `cf-ray` (for example `LAX`) and
  `cf-cache-status`;
- with `health_body`, only whether the body matched. The body itself is not
  stored.

The check's result is decided when it runs:

| Result | When | Effect |
| --- | --- | --- |
| `pass` | Allowed status, matching release, under the latency limit, healthy body | Counts toward 5 in a row |
| `mismatch` | Another release's `X-Release-ID`, or none, such as from an old replica | Resets the count |
| `unhealthy` | The health body is not healthy or cannot be parsed | Resets the count |
| `fail` | DNS, TLS, connection, timeout (10 s), a status code that is not allowed, or the latency limit | Fails Verify |

The stored result is the evidence. Replaying a release gives the same
outcome even after the service map changes.

## Outcomes

Checks run 30 seconds apart, from Rollout's completion until the window
closes.

| Verify | When |
| --- | --- |
| succeeded | 5 consecutive passes. The release's `verified_at` is the fifth. |
| failed | A `fail` check, or the window closed after a release mismatch |
| degraded | The window closed and only the health body stood in the way |
| unknown | The window closed with no checks or with no failed check to blame, such as when mybuilds was down; or a newer release of the service rolled out first |
| running | Checks so far, none of the above yet |

The verifier keeps no state of its own. After a restart it reads which
releases are still open and when each was last checked, and carries on. A
window that closed while it was down is closed with what was checked.

Only a service's newest rollout is checked. An older release still open when
a newer one finishes rolling out ends `unknown`.

The AWS reliability plane stays the authority on outside availability. The
verifier answers only whether this release became reachable and stayed
healthy during its window.

## Steps

| Step | Evidence |
| --- | --- |
| DNS + TLS | From the first check: TLS version and certificate expiry |
| Check *n* | Status, latency, observed release, Cloudflare location, and the count of passes in a row. A check that reset the count shows as degraded. |

Each check is also written to `verification_samples`, which is rebuilt with
the rest of the projection.

## Metrics

| Metric | Meaning |
| --- | --- |
| `mybuilds_verifier_checks_total{service, result}` | Checks by result |
| `mybuilds_verifier_latency_seconds{service}` | Latency of checks that got a response |
