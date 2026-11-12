import type { Release } from "../api";
import { shortDigest, shortSha } from "../format";
import { CopyIcon } from "./Icons";

/** The identity sidebar: commit, artifact, GitOps commit, Argo CD revision. */
export function ReleaseDetails({ release: r }: { release: Release }) {
  const reconcile = r.stages.find((s) => s.key === "reconcile");
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
        awaiting={r.superseded ? "superseded, no promotion" : "awaiting gitops.updated"}
      />
      <Field
        k="Argo CD sync"
        v={reconcile && reconcile.state !== "pending" ? reconcile.summary ?? reconcile.failure_reason ?? reconcile.state : undefined}
        awaiting="awaiting sync"
      />
      <Field k="Rollout" v={undefined} awaiting="runtime watch arrives in M2" />
      <Field k="Verification" v={undefined} awaiting="verifier arrives in M2" />
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
