// The off-cluster ingestion buffer's receiver (plan section 8.6): a Worker
// on the ingestion paths of mybuilds.brendanmanley.com. It checks the size
// and the signature of each delivery, queues it in SQS, and answers 202 only
// once SQS has it. mybuilds drains the queue (internal/ingest/queue) and
// verifies the signature again. The same paths on mybuilds itself stay as
// the direct endpoints, which the route falls back to.
import { AwsClient } from "aws4fetch";

export interface Env {
  SQS_QUEUE_URL: string;
  AWS_REGION: string;
  AWS_ACCESS_KEY_ID: string;
  AWS_SECRET_ACCESS_KEY: string;
  MYBUILDS_GITHUB_WEBHOOK_SECRET?: string;
  MYBUILDS_CI_EVENT_SECRET?: string;
  MYBUILDS_ARGOCD_TOKEN?: string;
}

/** The largest body accepted, which is also SQS's message limit. */
export const MAX_BYTES = 256 * 1024;

/** One queued delivery, as mybuilds reads it. */
export interface Envelope {
  provider: "github" | "ci" | "argocd";
  /** The request headers mybuilds reads, lower-case. */
  headers: Record<string, string>;
  /** The raw body, base64, so mybuilds verifies the same bytes. */
  body_base64: string;
  received_at: string;
}

/** Puts an envelope on the queue and returns the message ID. */
export type Send = (env: Env, envelope: Envelope) => Promise<string>;

interface Route {
  provider: Envelope["provider"];
  headers: string[];
}

const ROUTES: Record<string, Route> = {
  "/hooks/github": {
    provider: "github",
    headers: ["content-type", "x-github-delivery", "x-github-event", "x-hub-signature-256"],
  },
  "/api/v1/ci-events": { provider: "ci", headers: ["content-type", "x-mybuilds-signature-256"] },
  "/hooks/argocd": { provider: "argocd", headers: ["content-type", "authorization"] },
};

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    return handle(request, env, sendToSQS);
  },
} satisfies ExportedHandler<Env>;

export async function handle(request: Request, env: Env, send: Send): Promise<Response> {
  const route = ROUTES[new URL(request.url).pathname];
  if (!route) return json(404, { error: "not found" });
  if (request.method !== "POST") return json(405, { error: "method not allowed" }, { Allow: "POST" });
  if (Number(request.headers.get("content-length") ?? 0) > MAX_BYTES) {
    return json(413, { error: "body exceeds 256 KiB" });
  }
  const body = new Uint8Array(await request.arrayBuffer());
  if (body.byteLength > MAX_BYTES) return json(413, { error: "body exceeds 256 KiB" });

  const denied = await authenticate(route.provider, request.headers, body, env);
  if (denied) return json(denied.status, { error: denied.error });

  const headers: Record<string, string> = {};
  for (const name of route.headers) {
    const value = request.headers.get(name);
    if (value !== null) headers[name] = value;
  }
  const envelope: Envelope = {
    provider: route.provider,
    headers,
    body_base64: base64(body),
    received_at: new Date().toISOString(),
  };
  if (new TextEncoder().encode(JSON.stringify(envelope)).byteLength > MAX_BYTES) {
    return json(413, { error: "delivery exceeds the queue's 256 KiB message limit" });
  }

  try {
    const id = await send(env, envelope);
    return json(202, { outcome: "queued", message_id: id });
  } catch (e) {
    console.error(JSON.stringify({ provider: route.provider, error: String(e) }));
    return json(502, { error: "could not queue the delivery; retry" });
  }
}

interface Denied {
  status: number;
  error: string;
}

async function authenticate(
  provider: Envelope["provider"],
  headers: Headers,
  body: Uint8Array,
  env: Env,
): Promise<Denied | null> {
  switch (provider) {
    case "github":
      return hmacDenied(env.MYBUILDS_GITHUB_WEBHOOK_SECRET, "github", headers.get("x-hub-signature-256"), body);
    case "ci":
      return hmacDenied(env.MYBUILDS_CI_EVENT_SECRET, "ci", headers.get("x-mybuilds-signature-256"), body);
    case "argocd": {
      if (!env.MYBUILDS_ARGOCD_TOKEN) return { status: 503, error: "argocd ingestion is not configured" };
      const got = headers.get("authorization") ?? "";
      if (!got.startsWith("Bearer ") || got.length <= "Bearer ".length) {
        return { status: 401, error: "authorization: missing bearer token" };
      }
      if (!(await sameSecret(got.slice("Bearer ".length), env.MYBUILDS_ARGOCD_TOKEN))) {
        return { status: 401, error: "authorization: token does not match" };
      }
      return null;
    }
  }
}

// The messages match mybuilds' own, so a sender sees the same answer with
// or without the Worker in front.
async function hmacDenied(
  secret: string | undefined,
  provider: string,
  header: string | null,
  body: Uint8Array,
): Promise<Denied | null> {
  if (!secret) return { status: 503, error: `${provider} ingestion is not configured` };
  if (!header) return { status: 401, error: "signature: missing signature" };
  if (!/^sha256=[0-9a-fA-F]{64}$/.test(header)) {
    return { status: 401, error: "signature: signature is not sha256=<hex>" };
  }
  const want = await hmacHex(secret, body);
  if (!equalHex(header.slice("sha256=".length).toLowerCase(), want)) {
    return { status: 401, error: "signature: signature does not match" };
  }
  return null;
}

async function hmacHex(secret: string, body: Uint8Array): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return hex(await crypto.subtle.sign("HMAC", key, body));
}

/** Compares two secrets by digest, in constant time. */
async function sameSecret(a: string, b: string): Promise<boolean> {
  const encode = (s: string) => new TextEncoder().encode(s);
  const [da, db] = await Promise.all([crypto.subtle.digest("SHA-256", encode(a)), crypto.subtle.digest("SHA-256", encode(b))]);
  return equalHex(hex(da), hex(db));
}

function equalHex(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

function hex(buf: ArrayBuffer): string {
  return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function base64(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(s);
}

function json(status: number, body: unknown, extra: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", "cache-control": "no-store", ...extra },
  });
}

/** SendMessage through SQS's JSON protocol, signed with SigV4. */
export async function sendToSQS(env: Env, envelope: Envelope): Promise<string> {
  const aws = new AwsClient({
    accessKeyId: env.AWS_ACCESS_KEY_ID,
    secretAccessKey: env.AWS_SECRET_ACCESS_KEY,
    region: env.AWS_REGION,
    service: "sqs",
  });
  const res = await aws.fetch(`https://sqs.${env.AWS_REGION}.amazonaws.com/`, {
    method: "POST",
    headers: { "content-type": "application/x-amz-json-1.0", "x-amz-target": "AmazonSQS.SendMessage" },
    body: JSON.stringify({ QueueUrl: env.SQS_QUEUE_URL, MessageBody: JSON.stringify(envelope) }),
  });
  if (!res.ok) {
    throw new Error(`SQS SendMessage: HTTP ${res.status} ${(await res.text()).slice(0, 200)}`);
  }
  const out = (await res.json()) as { MessageId?: string };
  return out.MessageId ?? "";
}
