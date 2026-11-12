import type { Release, Service, Stage } from "../api";
import { STATE_LABEL, repoName, shortDigest, shortSha, stageLabel, stepTimer } from "../format";
import { StateIcon } from "./Icons";

interface Props {
  release: Release;
  stage: Stage;
  service?: Service;
  now: number;
  pinned: boolean;
}

const PROVIDER: Record<string, string> = {
  source: "GitHub",
  build: "GitHub Actions",
  publish: "GitHub Actions",
  gitops: "GitHub Actions",
  gitops_commit: "GitHub",
  reconcile: "Argo CD",
  rollout: "Kubernetes",
  verify: "Verifier",
};

/** Where a stage runs and what it did: the "where" rows and the steps. */
export function StagePanel({ release, stage, service, now, pinned }: Props) {
  const steps = stage.steps ?? [];
  return (
    <div className="stage-panel" aria-label="Stage detail">
      <div className="sp-head">
        <span className={`sp-title s-${stage.state}`}>
          <StateIcon state={stage.state} />
          {stageLabel(stage.key)}
        </span>
        <span className="sp-state">{STATE_LABEL[stage.state]}</span>
        {stage.attempt > 1 && <span className="sp-state">attempt {stage.attempt}</span>}
        <span className="prov">{PROVIDER[stage.key] ?? "mybuilds"}</span>
        <span className="spacer" />
        <span className="sp-mode">{pinned ? "Pinned. Click the stage again to follow." : "Following the active stage"}</span>
      </div>
      <div className="sp-grid">
        <dl className="where">
          {whereRows(release, stage, service).map(([k, v, mono]) => (
            <WhereRow key={k} k={k} v={v} mono={mono} />
          ))}
        </dl>
        {steps.length > 0 ? (
          <ol className="steps">
            {steps.map((s) => (
              <li key={`${s.position}-${s.name}`} className={`step st-${s.state}`}>
                <span className="ic">
                  <StateIcon state={s.state} />
                </span>
                <span className="nm">
                  {s.name}
                  {s.reconstructed && <span className="recb">reconstructed</span>}
                </span>
                <span className="tm">{stepTimer(s, now)}</span>
                {s.detail && <span className="dt">{s.detail}</span>}
              </li>
            ))}
          </ol>
        ) : (
          <div className="sp-note">{emptyNote(stage)}</div>
        )}
      </div>
    </div>
  );
}

function WhereRow({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <>
      <dt>{k}</dt>
      <dd className={mono ? "mono" : undefined}>{v}</dd>
    </>
  );
}

type Row = [string, string, boolean?];

function whereRows(r: Release, st: Stage, svc?: Service): Row[] {
  const rows: Row[] = [];
  const push = (k: string, v?: string, mono = false) => {
    if (v) rows.push([k, v, mono]);
  };
  switch (st.key) {
    case "source":
      push("Repository", r.source_repository ?? svc?.repository);
      push("Branch", svc?.branch);
      push("Commit", r.source_sha, true);
      push("Evidence", "workflow_run webhook");
      break;
    case "build":
      push("Workflow", svc?.workflow, true);
      push("Job", "build", true);
      push("Image", r.artifact ?? svc?.artifact, true);
      push("Evidence", "workflow_job steps, release.published");
      break;
    case "publish":
      push("Workflow", svc?.workflow, true);
      push("Job", "build", true);
      push("Image", r.artifact ?? svc?.artifact, true);
      push("Digest", r.artifact_digest ? shortDigest(r.artifact_digest) : undefined, true);
      push("Evidence", "CI-signed release.published");
      break;
    case "gitops":
    case "gitops_commit":
      push("Repository", r.gitops_repository ?? svc?.gitops_repository);
      push("Path", svc?.gitops_path, true);
      push("Job", st.key === "gitops" ? "bump-gitops" : undefined, true);
      push("Commit", r.gitops_sha ? shortSha(r.gitops_sha) : undefined, true);
      push("Evidence", st.key === "gitops" ? "CI-signed gitops.updated" : "GitOps commit");
      break;
    case "reconcile":
      push("Application", svc?.argocd_application ?? r.service, true);
      push("Revision", r.gitops_sha ? `${repoName(r.gitops_repository)}@${shortSha(r.gitops_sha)}` : undefined, true);
      push("Evidence", "Argo CD Notifications");
      break;
    case "rollout":
      push("Namespace", svc?.workload?.namespace, true);
      push("Deployment", svc?.workload?.deployment, true);
      push("Digest", r.artifact_digest ? shortDigest(r.artifact_digest) : undefined, true);
      push("Evidence", "Read-only watch of the Deployment, ReplicaSets and pods");
      break;
    case "verify":
      push("Check", "GET /healthz, X-Release-ID", true);
      push("Evidence", "Verifier samples (M2)");
      break;
  }
  if (st.failure_reason) rows.push(["Reason", st.failure_reason]);
  return rows;
}

function emptyNote(st: Stage): string {
  switch (st.state) {
    case "pending":
      return `${stageLabel(st.key)} has not started. Steps appear as evidence arrives.`;
    case "skipped":
      return st.summary ?? `${stageLabel(st.key)} was skipped.`;
    case "unknown":
      return st.failure_reason ?? "No evidence was received for this stage.";
    case "running":
      return "Running. Step detail arrives when the job reports.";
    default:
      return st.summary ?? "No step detail was recorded for this stage.";
  }
}
