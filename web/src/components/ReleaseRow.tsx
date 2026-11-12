import type { Release } from "../api";
import { OUTCOME_LABEL, relTime, releaseDuration, shortSha } from "../format";
import { StateIcon } from "./Icons";
import { MiniPipeline } from "./Pipeline";

interface Props {
  release: Release;
  now: number;
  selected: boolean;
  onSelect: (id: string) => void;
}

export function ReleaseRow({ release: r, now, selected, onSelect }: Props) {
  const sha = r.kind === "config" ? r.gitops_sha : r.source_sha;
  const outcome = r.superseded ? "skipped" : r.status;
  return (
    <div
      role="row"
      tabIndex={0}
      className={`tr row${selected ? " sel" : ""}`}
      onClick={() => onSelect(r.release_id)}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onSelect(r.release_id);
        }
      }}
      aria-selected={selected}
    >
      <span className="c-rel" role="cell">
        <span className="rel-l1">
          {r.service}
          {r.drill && <span className="tag tag-drill">DRILL</span>}
          {r.kind === "config" && <span className="tag tag-config">CONFIG</span>}
          {r.status === "running" && <span className="tag tag-live">LIVE</span>}
          <span className="mono" style={{ fontWeight: 400 }}>
            {shortSha(sha)}
          </span>
        </span>
        <span className="rel-l2">{r.drill && r.drill_scenario ? `scenario: ${r.drill_scenario}` : r.release_id}</span>
      </span>
      <span className="c-pipe" role="cell">
        <MiniPipeline release={r} />
      </span>
      <span className="c-out" role="cell">
        <span className={`pill o-${outcome}`}>
          <StateIcon state={outcome} />
          {OUTCOME_LABEL[outcome]}
        </span>
      </span>
      <span className="c-start" role="cell">
        {relTime(r.created_at, now)}
      </span>
      <span className="c-dur" role="cell">
        {releaseDuration(r, now)}
      </span>
    </div>
  );
}
