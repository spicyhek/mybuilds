import type { Release } from "../api";
import { shortDigest, shortSha } from "../format";
import { CopyIcon } from "./Icons";

/** The identity sidebar: commit, artifact, GitOps commit, Argo CD revision. */
export function ReleaseDetails({ release: r }: { release: Release }) {
  const reconcile = r.stages.find((s) => s.key === "reconcile");
  const rollout = r.stages.find((s) => s.key === "rollout");
  const verify = r.stages.find((s) => s.key === "verify");
  return (
    <>
      <Field k="Release" v={r.release_id} mono copy />
      <Field k="Service" v={`${r.service} · ${r.environment}`} />
      {r.kind === "application" ? (
        <>
          <Field
            k="Source commit"
            v={r.source_sha ? shortSha(r.source_sha) : undefined}
            mono
            copy={r.source_sha}
            href={r.source_repository && r.source_sha ? `https://github.com/${r.source_repository}/commit/${r.source_sha}` : undefined}
            awaiting="awaiting workflow run"
          />
          <Field k="Artifact" v={r.artifact} mono awaiting="awaiting release.published" />
          <Field k="Digest" v={r.artifact_digest ? shortDigest(r.artifact_digest) : undefined} mono copy={r.artifact_digest} awaiting="awaiting release.published" />
        </>
      ) : null}
      <Field
        k="GitOps commit"
        v={r.gitops_sha ? shortSha(r.gitops_sha) : undefined}
        mono
        copy={r.gitops_sha}
        href={r.gitops_repository && r.gitops_sha ? `https://github.com/${r.gitops_repository}/commit/${r.gitops_sha}` : undefined}
        awaiting={r.superseded ? "superseded, no promotion" : r.kind === "config" ? "awaiting sync" : "awaiting gitops.updated"}
      />
      {r.kind === "config" ? (
        <>
          <Field k="Commit" v={r.gitops_subject} awaiting="awaiting commit metadata" />
          <Field k="Author" v={r.gitops_author} awaiting="awaiting commit metadata" />
          <Field k="Serving" v={r.runtime_release_id} mono copy awaiting="awaiting rollout" />
          <Field k="Digest" v={r.artifact_digest ? shortDigest(r.artifact_digest) : undefined} mono copy={r.artifact_digest} awaiting="awaiting rollout" />
        </>
      ) : null}
      <Field
        k="Argo CD sync"
        v={reconcile && reconcile.state !== "pending" ? reconcile.summary ?? reconcile.failure_reason ?? reconcile.state : undefined}
        awaiting="awaiting sync"
      />
      <Field
        k="Rollout"
        v={rollout && rollout.state !== "pending" ? rollout.summary ?? rollout.failure_reason ?? rollout.state : undefined}
        awaiting="awaiting rollout"
      />
      <Field
        k="Verification"
        v={verify && verify.state !== "pending" ? verify.summary ?? verify.failure_reason ?? verify.state : undefined}
        awaiting="awaiting checks"
      />
    </>
  );
}

function Field({
  k,
  v,
  mono,
  copy,
  href,
  awaiting,
}: {
  k: string;
  v?: string;
  mono?: boolean;
  copy?: boolean | string;
  href?: string;
  awaiting?: string;
}) {
  const copyText = typeof copy === "string" ? copy : copy ? v : undefined;
  return (
    <div className="field">
      <div className="k">{k}</div>
      <div className={`v${mono && v ? " mono" : ""}`}>
        {v ? (
          href ? (
            <a href={href} target="_blank" rel="noopener">
              {v}
            </a>
          ) : (
            <span>{v}</span>
          )
        ) : (
          <span className="await">{awaiting ?? "—"}</span>
        )}
        {copyText && (
          <button type="button" className="copy" aria-label={`Copy ${k}`} title={`Copy ${k}`} onClick={() => void navigator.clipboard?.writeText(copyText)}>
            <CopyIcon />
          </button>
        )}
      </div>
    </div>
  );
}
