import { useMemo, useState, type ReactNode } from "react";
import type { Release, Service } from "../api";
import { OUTCOME_LABEL, relTime, shortSha, statusLine } from "../format";
import { StateIcon } from "./Icons";
import { Pipeline } from "./Pipeline";
import { StagePanel } from "./StagePanel";

interface Props {
  release: Release;
  service?: Service;
  now: number;
  /** The card's heading, for example "Now playing". */
  heading: string;
  /** Extra items for the line under the title, after the start time. */
  sub?: ReactNode;
  /** Extra controls in the card head, before the outcome pill. */
  actions?: ReactNode;
}

/**
 * The release player: pipeline, status line and stage detail panel. The
 * panel follows the active stage until a stage is clicked.
 */
export function Player({ release: r, service, now, heading, sub, actions }: Props) {
  const [pinned, setPinned] = useState<{ release: string; stage: string } | null>(null);
  const pinnedStage = pinned?.release === r.release_id ? pinned.stage : null;

  const focusKey = useMemo(() => {
    if (pinnedStage && r.stages.some((s) => s.key === pinnedStage)) return pinnedStage;
    const active = r.stages.find((s) => s.state === "running") ?? r.stages.find((s) => s.state === "failed");
    if (active) return active.key;
    const last = [...r.stages].reverse().find((s) => s.state !== "pending");
    return (last ?? r.stages[0])?.key ?? null;
  }, [r, pinnedStage]);
  const focusStage = r.stages.find((s) => s.key === focusKey);

  return (
    <section className="card" aria-label="Release player">
      <div className="card-head">
        <span>{heading}</span>
        {r.status === "running" && <span className="badge badge-live">LIVE</span>}
        <span className="spacer" />
        {actions}
        <OutcomePill release={r} />
      </div>
      <div className="card-body">
        <div className="pl-top">
          <div style={{ minWidth: 0 }}>
            <div className="pl-title">
              {r.service}
              {r.drill && <span className="tag tag-drill">DRILL</span>}
              {r.kind === "config" && <span className="tag tag-config">CONFIG</span>}
              {r.flag && (
                <span className="tag tag-flag" title={r.flag}>
                  HAND EDIT
                </span>
              )}
              <span className="mono" style={{ fontWeight: 400 }}>
                {shortSha(r.kind === "config" ? r.gitops_sha : r.source_sha)}
              </span>
            </div>
            <div className="pl-sub">
              {r.release_id} · started {relTime(r.created_at, now)}
              {sub}
            </div>
          </div>
        </div>
        <div className="canvas">
          <Pipeline
            release={r}
            now={now}
            focus={focusKey}
            pinned={pinnedStage !== null}
            onPick={(key) => setPinned(pinnedStage === key ? null : { release: r.release_id, stage: key })}
          />
        </div>
        <StatusLine release={r} />
        {focusStage && <StagePanel release={r} stage={focusStage} service={service} now={now} pinned={pinnedStage !== null} />}
      </div>
    </section>
  );
}

export function OutcomePill({ release: r }: { release: Release }) {
  const outcome = r.superseded ? "skipped" : r.status;
  return (
    <span className={`pill o-${outcome}`}>
      <StateIcon state={outcome} />
      {OUTCOME_LABEL[outcome]}
    </span>
  );
}

function StatusLine({ release }: { release: Release }) {
  const { text, state } = statusLine(release);
  return (
    <div className="pl-status" aria-live="polite">
      <span className={`sp-title s-${state}`}>
        <StateIcon state={state} />
      </span>
      <span className="msg">{text}</span>
    </div>
  );
}
