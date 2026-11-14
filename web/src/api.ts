// Types mirror the allowlisted views in internal/httpapi/releases.go.

export type State =
  | "pending"
  | "running"
  | "succeeded"
  | "failed"
  | "degraded"
  | "skipped"
  | "unknown";

export const TERMINAL = new Set<State>(["succeeded", "failed", "degraded", "skipped"]);

export interface Step {
  position: number;
  name: string;
  state: State;
  started_at?: string;
  completed_at?: string;
  detail?: string;
  reconstructed?: boolean;
}

export interface Stage {
  key: string;
  position: number;
  state: State;
  attempt: number;
  started_at?: string;
  completed_at?: string;
  summary?: string;
  failure_reason?: string;
  steps?: Step[];
}

export interface Release {
  release_id: string;
  service: string;
  environment: string;
  kind: "application" | "config";
  template: string;
  drill: boolean;
  drill_scenario?: string;
  superseded: boolean;
  source_repository?: string;
  source_sha?: string;
  artifact?: string;
  artifact_digest?: string;
  gitops_repository?: string;
  gitops_sha?: string;
  gitops_subject?: string;
  gitops_author?: string;
  runtime_release_id?: string;
  deployed_with?: string;
  flag?: string;
  status: State;
  created_at: string;
  completed_at?: string;
  verified_at?: string;
  stages: Stage[];
}

export interface Service {
  name: string;
  environment: string;
  repository: string;
  workflow: string;
  branch: string;
  artifact?: string;
  gitops_repository?: string;
  gitops_path?: string;
  argocd_application?: string;
  workload?: { namespace: string; deployment: string };
  verify?: {
    url: string;
    allowed_status: number[];
    max_latency_ms: number;
    window_seconds: number;
    health_body?: string;
  };
}

export interface Meta {
  version: string;
  release_id: string;
  fixtures: boolean;
  services: Service[];
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    throw new Error(`${path}: HTTP ${res.status}`);
  }
  return (await res.json()) as T;
}

export const fetchReleases = (limit = 50) =>
  getJSON<{ releases: Release[] }>(`/api/v1/releases?limit=${limit}`).then((r) => r.releases);

export const fetchRelease = (id: string) => getJSON<Release>(`/api/v1/releases/${id}`);

export const fetchMeta = () => getJSON<Meta>("/api/v1/meta");
