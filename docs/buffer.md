# Off-cluster ingestion buffer

mybuilds runs inside the cluster it watches, as one pod. A webhook sent
straight to it while it restarts, for example during its own deploy, would
be lost. So each push-style source sends to a small receiver outside the
cluster, which queues the request for mybuilds to drain. See the plan,
section 8.6.

```text
GitHub / CI / Argo CD
  -> https://mybuilds.brendanmanley.com/hooks/*  and  /api/v1/ci-events
  -> Cloudflare Worker (worker/): size and signature check, SendMessage
  -> SQS mybuilds-ingest (14-day retention) --5 failed receives--> mybuilds-ingest-dlq
  -> mybuilds consumer (internal/ingest/queue): same signature check, events table
```

The provider-facing URLs are the same with or without the buffer. Nothing in
GitHub, the release workflows or Argo CD changes when the Worker route is
added or removed.

## Receiver

The Worker runs on a route for the three ingestion paths of the hostname;
every other path still goes through the Cloudflare Tunnel. For each request
it:

1. accepts `POST` only, and bodies up to 256 KiB;
2. checks the GitHub or CI HMAC, or the Argo CD bearer token, against the
   same secrets mybuilds holds, so unsigned traffic never reaches the queue.
   The error messages match mybuilds' own (`401`, `413`, `503`);
3. queues one SQS message, the **envelope** below, signing the AWS request
   with `aws4fetch`;
4. answers `202 {"outcome":"queued","message_id":"..."}` only once SQS has
   the message, and `502` when it does not. GitHub shows that on its
   deliveries page, and the CI step retries.

```json
{
  "provider": "github",
  "headers": { "content-type": "application/json", "x-github-delivery": "...", "x-github-event": "workflow_run", "x-hub-signature-256": "sha256=..." },
  "body_base64": "...",
  "received_at": "2026-11-16T10:00:00.000Z"
}
```

Only the headers mybuilds reads are forwarded. The body is base64 so that
mybuilds verifies the bytes the sender signed.

Argo CD posts to the public URL as well. It sends from inside the cluster, so
the tunnel is not on that path, and a notification sent while mybuilds is
restarting waits in the queue instead of being dropped.

## Queue

A standard queue, not FIFO: the projector orders events by their own
timestamps, and the unique `(provider, delivery_id)` drops a duplicate, so
neither ordering nor exactly-once delivery is needed. Messages are encrypted
at rest and kept for 14 days. A message received five times without being
deleted moves to the dead-letter queue, where it stays until redriven or
expired. Both queues and both IAM users are in homelab-infra's
`terraform/reliability-control-plane/mybuilds-ingest.tf`.

## Consumer

`internal/ingest/queue` long-polls the queue (20-second wait, up to 10
messages, 60-second visibility timeout). Each message goes through the same
`Ingester` as the direct endpoints, so the signature is checked a second
time. The message is deleted when the delivery was stored, was a duplicate,
was ignored, or was rejected with a `4xx` (a changed secret, or an invalid
body: final either way). It is left when storing failed, and comes back
after the visibility timeout.

The consumer runs when `MYBUILDS_SQS_QUEUE_URL`, `MYBUILDS_SQS_ACCESS_KEY_ID`
and `MYBUILDS_SQS_SECRET_ACCESS_KEY` are set (the `mybuilds-sqs` Secret in
homelab-infra). The region comes from the queue URL, or
`MYBUILDS_SQS_REGION`.

| Metric | Meaning |
| --- | --- |
| `mybuilds_queue_messages_total{result}` | `stored`, `duplicate`, `ignored`, `rejected`, `malformed` or `error` (left in the queue) |
| `mybuilds_queue_receive_errors_total` | Failed receive calls; the consumer retries with backoff |
| `mybuilds_queue_last_receive_timestamp_seconds` | The last successful poll; stale means the consumer is stuck or its key is wrong |
| `mybuilds_queue_delay_seconds` | From the Worker receiving a delivery to mybuilds processing it |

## Direct endpoints stay

The `POST` handlers in mybuilds remain for local development, fixture mode
and as the fallback: a request that the Worker does not answer, such as when
the route is removed or Workers' free-plan limit is reached with the route
set to fail open, goes through the tunnel to mybuilds as before.

## What it does not cover

- If the Worker cannot enqueue, it answers `502`. GitHub does not retry on
  its own; the delivery can be resent from the webhook's page. The CI step
  retries. Argo CD retries on its next reconcile.
- Argo CD runs in the cluster, so when the whole cluster is down it sends
  nothing for the buffer to hold.
- Those cases still show `unknown`. Querying providers to repair them stays
  deferred.

## Operating it

The Worker deploys from the release workflow's `deploy-worker` job whenever
`worker/` changes on `main`, with `CLOUDFLARE_API_TOKEN` and
`CLOUDFLARE_ACCOUNT_ID` from the repository's secrets. Its own secrets are
set once with `wrangler secret put` (see `worker/wrangler.toml`). The
cluster side is in homelab-infra's `k8s/apps/mybuilds/README.md`.

A delivery parked in the dead-letter queue is examined with the AWS CLI, and
redriven to the queue from the console once the cause is fixed.
