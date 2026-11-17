import { describe, expect, it } from "vitest";
import { handle, MAX_BYTES, type Env, type Envelope } from "./index";

const env: Env = {
  SQS_QUEUE_URL: "https://sqs.us-west-1.amazonaws.com/123456789012/mybuilds-ingest",
  AWS_REGION: "us-west-1",
  AWS_ACCESS_KEY_ID: "key",
  AWS_SECRET_ACCESS_KEY: "secret",
  MYBUILDS_GITHUB_WEBHOOK_SECRET: "github-secret",
  MYBUILDS_CI_EVENT_SECRET: "ci-secret",
  MYBUILDS_ARGOCD_TOKEN: "argocd-token",
};

async function sign(secret: string, body: string): Promise<string> {
  const key = await crypto.subtle.importKey("raw", new TextEncoder().encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const sig = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(body));
  return "sha256=" + [...new Uint8Array(sig)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function post(path: string, body: string, headers: Record<string, string>, method = "POST"): Request {
  return new Request(`https://mybuilds.brendanmanley.com${path}`, { method, body: method === "GET" ? undefined : body, headers });
}

function queue() {
  const sent: Envelope[] = [];
  const send = async (_env: Env, envelope: Envelope) => {
    sent.push(envelope);
    return "msg-1";
  };
  return { sent, send };
}

const body = JSON.stringify({ action: "completed", workflow_run: { id: 1 } });

describe("handle", () => {
  it("queues a signed GitHub delivery with the headers mybuilds reads", async () => {
    const { sent, send } = queue();
    const sig = await sign("github-secret", body);
    const res = await handle(
      post("/hooks/github", body, {
        "content-type": "application/json",
        "x-github-delivery": "d-1",
        "x-github-event": "workflow_run",
        "x-hub-signature-256": sig,
        "x-forwarded-for": "203.0.113.1",
      }),
      env,
      send,
    );
    expect(res.status).toBe(202);
    expect(await res.json()).toEqual({ outcome: "queued", message_id: "msg-1" });
    expect(sent).toHaveLength(1);
    const e = sent[0]!;
    expect(e.provider).toBe("github");
    expect(atob(e.body_base64)).toBe(body);
    expect(e.headers).toEqual({
      "content-type": "application/json",
      "x-github-delivery": "d-1",
      "x-github-event": "workflow_run",
      "x-hub-signature-256": sig,
    });
    expect(Date.parse(e.received_at)).not.toBeNaN();
  });

  it("rejects a wrong or missing signature before queueing", async () => {
    const { sent, send } = queue();
    const wrong = await handle(post("/hooks/github", body, { "x-hub-signature-256": await sign("other", body) }), env, send);
    expect(wrong.status).toBe(401);
    expect(await wrong.json()).toEqual({ error: "signature: signature does not match" });
    const missing = await handle(post("/hooks/github", body, {}), env, send);
    expect(missing.status).toBe(401);
    const malformed = await handle(post("/hooks/github", body, { "x-hub-signature-256": "sha1=abc" }), env, send);
    expect(malformed.status).toBe(401);
    expect(sent).toHaveLength(0);
  });

  it("checks CI events with their own secret", async () => {
    const { sent, send } = queue();
    const ok = await handle(post("/api/v1/ci-events", body, { "x-mybuilds-signature-256": await sign("ci-secret", body) }), env, send);
    expect(ok.status).toBe(202);
    const wrong = await handle(post("/api/v1/ci-events", body, { "x-mybuilds-signature-256": await sign("github-secret", body) }), env, send);
    expect(wrong.status).toBe(401);
    expect(sent).toHaveLength(1);
    expect(sent[0]!.provider).toBe("ci");
  });

  it("checks the Argo CD bearer token", async () => {
    const { sent, send } = queue();
    const ok = await handle(post("/hooks/argocd", body, { authorization: "Bearer argocd-token" }), env, send);
    expect(ok.status).toBe(202);
    expect(sent[0]!.headers.authorization).toBe("Bearer argocd-token");
    for (const authorization of ["Bearer nope", "Basic argocd-token", "Bearer "]) {
      const res = await handle(post("/hooks/argocd", body, { authorization }), env, send);
      expect(res.status, authorization).toBe(401);
    }
    expect(sent).toHaveLength(1);
  });

  it("answers 503 when a provider's secret is not configured", async () => {
    const { sent, send } = queue();
    const res = await handle(post("/api/v1/ci-events", body, { "x-mybuilds-signature-256": await sign("ci-secret", body) }), { ...env, MYBUILDS_CI_EVENT_SECRET: undefined }, send);
    expect(res.status).toBe(503);
    expect(sent).toHaveLength(0);
  });

  it("rejects bodies over 256 KiB", async () => {
    const { sent, send } = queue();
    const big = "x".repeat(MAX_BYTES + 1);
    const res = await handle(post("/hooks/argocd", big, { authorization: "Bearer argocd-token" }), env, send);
    expect(res.status).toBe(413);
    const declared = await handle(post("/hooks/argocd", body, { authorization: "Bearer argocd-token", "content-length": String(MAX_BYTES + 1) }), env, send);
    expect(declared.status).toBe(413);
    expect(sent).toHaveLength(0);
  });

  it("accepts only POST on the ingestion paths", async () => {
    const { sent, send } = queue();
    const get = await handle(post("/hooks/github", "", {}, "GET"), env, send);
    expect(get.status).toBe(405);
    expect(get.headers.get("allow")).toBe("POST");
    const other = await handle(post("/api/v1/releases", body, {}), env, send);
    expect(other.status).toBe(404);
    expect(sent).toHaveLength(0);
  });

  it("answers 502 when the queue does not take the delivery", async () => {
    const send = async () => {
      throw new Error("SQS SendMessage: HTTP 500");
    };
    const res = await handle(post("/hooks/argocd", body, { authorization: "Bearer argocd-token" }), env, send);
    expect(res.status).toBe(502);
    expect(res.headers.get("cache-control")).toBe("no-store");
  });
});
