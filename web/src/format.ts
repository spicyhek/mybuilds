import type { Release, Stage, State, Step } from "./api";

export const STAGE_LABEL: Record<string, string> = {
  source: "Source",
  build: "Build",
  publish: "Publish",
  gitops: "GitOps",
  gitops_commit: "GitOps commit",
  reconcile: "Reconcile",
  rollout: "Rollout",
  verify: "Verify",
};

export const STATE_LABEL: Record<State, string> = {
  pending: "Pending",
  running: "Running",
  succeeded: "Succeeded",
  failed: "Failed",
  degraded: "Degraded",
  skipped: "Skipped",
  unknown: "Unknown",
};

export const stageLabel = (key: string) => STAGE_LABEL[key] ?? key;

export const shortSha = (sha?: string) => (sha ? sha.slice(0, 7) : "");

export const shortDigest = (d?: string) => (d && d.length > 20 ? `${d.slice(0, 13)}…${d.slice(-4)}` : d ?? "");

export const repoName = (repo?: string) => (repo ? repo.split("/").pop() ?? repo : "");

/** Seconds between two instants, the second defaulting to now. */
export function seconds(from?: string, to?: string, now = Date.now()): number | null {
  if (!from) return null;
  const a = Date.parse(from);
  const b = to ? Date.parse(to) : now;
  if (Number.isNaN(a) || Number.isNaN(b)) return null;
  return Math.max(0, (b - a) / 1000);
}

/** 0.4s, 42s, 3m 05s, 1h 12m. */
export function duration(sec: number | null): string {
  if (sec === null) return "";
  if (sec < 1) return `${sec.toFixed(1)}s`;
  if (sec < 60) return `${Math.round(sec)}s`;
  const m = Math.floor(sec / 60);
  const s = Math.round(sec % 60);
  if (m < 60) return `${m}m ${String(s).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${String(m % 60).padStart(2, "0")}m`;
}

/** Live timer text for a stage: elapsed while running, total when done. */
export function stageTimer(st: Stage, now: number): string {
  switch (st.state) {
    case "running":
      return duration(seconds(st.started_at, undefined, now));
    case "pending":
      return "";
    case "skipped":
      return "skipped";
    default:
      return duration(seconds(st.started_at, st.completed_at, now));
  }
}

export function stepTimer(s: Step, now: number): string {
  if (s.state === "running") return duration(seconds(s.started_at, undefined, now));
  if (s.state === "pending" || s.state === "skipped") return "";
  return duration(seconds(s.started_at, s.completed_at, now));
}

export function relTime(iso: string, now = Date.now()): string {
  const s = (now - Date.parse(iso)) / 1000;
  if (s < 45) return "just now";
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  const d = new Date(iso);
  return d.toLocaleDateString([], { month: "short", day: "numeric" });
}

export function releaseDuration(r: Release, now: number): string {
  const end = r.completed_at ?? (r.status === "running" ? undefined : lastCompleted(r));
  if (r.status === "running") return duration(seconds(r.created_at, undefined, now));
  return duration(seconds(r.created_at, end, now));
}

function lastCompleted(r: Release): string | undefined {
  let last: string | undefined;
  for (const st of r.stages) {
    if (st.completed_at && (!last || st.completed_at > last)) last = st.completed_at;
  }
  return last;
}

export const OUTCOME_LABEL: Record<State, string> = {
  succeeded: "Succeeded",
  failed: "Failed",
  degraded: "Degraded",
  running: "In progress",
  unknown: "Unknown",
  pending: "Pending",
  skipped: "Superseded",
};

/** The stage and step of the latest activity, e.g. "Build › Smoke test". */
export function statusLine(r: Release): { text: string; state: State } {
  const running = r.stages.find((s) => s.state === "running");
  if (running) {
    const step = running.steps?.find((s) => s.state === "running");
    if (step) return { text: `${stageLabel(running.key)} › ${step.name}`, state: "running" };
    const waiting = waitingOn(r, running);
    return { text: waiting ?? `${stageLabel(running.key)} running`, state: "running" };
  }
  const failed = r.stages.find((s) => s.state === "failed");
  if (failed) {
    const step = failed.steps?.find((s) => s.state === "failed");
    return { text: `${stageLabel(failed.key)} › ${step ? step.name : failed.failure_reason ?? "failed"}`, state: "failed" };
  }
  const unknown = r.stages.find((s) => s.state === "unknown");
  if (unknown) return { text: `${stageLabel(unknown.key)} › ${unknown.failure_reason ?? "no evidence"}`, state: "unknown" };

  // Nothing active: the next stage is waiting on another system.
  const next = r.stages.find((s) => s.state === "pending");
  if (next) {
    const prev = r.stages[next.position - 2];
    const waiting = waitingOn(r, next);
    if (waiting) return { text: waiting, state: "pending" };
    if (prev && prev.state === "succeeded") return { text: `${stageLabel(prev.key)} done, waiting for ${stageLabel(next.key)}`, state: "pending" };
  }
  if (r.status === "succeeded") return { text: "Verified", state: "succeeded" };
  if (r.superseded) return { text: "Superseded by a newer commit", state: "skipped" };
  return { text: OUTCOME_LABEL[r.status], state: r.status };
}

function waitingOn(r: Release, st: Stage): string | null {
  switch (st.key) {
    case "reconcile":
      return r.gitops_sha ? `Waiting for Argo CD to pick up ${repoName(r.gitops_repository)}@${shortSha(r.gitops_sha)}` : null;
    case "rollout":
      return r.stages.find((s) => s.key === "reconcile")?.state === "succeeded"
        ? `Waiting for ${r.service} pods to roll out`
        : null;
    case "verify":
      return "Verification arrives with the verifier (M2)";
    case "build":
      return st.state === "running" && !st.steps?.length ? "Waiting for the build job to start" : null;
    default:
      return null;
  }
}
