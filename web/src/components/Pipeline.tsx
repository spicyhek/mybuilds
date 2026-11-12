import type { Release, Stage } from "../api";
import { STATE_LABEL, stageLabel, stageTimer } from "../format";
import { StateIcon } from "./Icons";

interface Props {
  release: Release;
  now: number;
  /** Stage key the detail panel is showing. */
  focus: string | null;
  pinned: boolean;
  onPick: (key: string) => void;
}

/** The big pipeline on the player card: one node per stage with a timer. */
export function Pipeline({ release, now, focus, pinned, onPick }: Props) {
  return (
    <ol className="pipeline big" aria-label="Pipeline stages">
      {release.stages.map((st, i) => (
        <PipelineNode
          key={st.key}
          stage={st}
          prev={release.stages[i - 1]}
          now={now}
          first={i === 0}
          focused={focus === st.key}
          pinned={pinned && focus === st.key}
          onPick={onPick}
        />
      ))}
    </ol>
  );
}

function PipelineNode({
  stage: st,
  prev,
  now,
  first,
  focused,
  pinned,
  onPick,
}: {
  stage: Stage;
  prev?: Stage;
  now: number;
  first: boolean;
  focused: boolean;
  pinned: boolean;
  onPick: (key: string) => void;
}) {
  const timer = stageTimer(st, now);
  const lineClass = !prev ? "" : prev.state === "succeeded" || prev.state === "skipped" ? (st.state === "running" ? "active" : "done") : "";
  return (
    <>
      {!first && <li className={`line ${lineClass}`} aria-hidden="true" />}
      <li className={`node s-${st.state}${focused ? " focus" : ""}${pinned ? " pinned" : ""}`}>
        <button
          type="button"
          className="pn"
          onClick={() => onPick(st.key)}
          aria-pressed={pinned}
          aria-label={`${stageLabel(st.key)}: ${STATE_LABEL[st.state]}${timer ? `, ${timer}` : ""}`}
          title={st.summary ?? st.failure_reason ?? STATE_LABEL[st.state]}
        >
          <StateIcon state={st.state} />
          <span>{stageLabel(st.key)}</span>
          {st.attempt > 1 && <span className="attempt">#{st.attempt}</span>}
        </button>
        <span className="timer">{timer}</span>
      </li>
    </>
  );
}

/** Seven small squares for the release list. */
export function MiniPipeline({ release }: { release: Release }) {
  return (
    <div className="mini" aria-label={release.stages.map((s) => `${stageLabel(s.key)} ${STATE_LABEL[s.state]}`).join(", ")}>
      {release.stages.map((st) => (
        <span key={st.key} className={`seg s-${st.state}`} title={`${stageLabel(st.key)}: ${STATE_LABEL[st.state]}`}>
          <StateIcon state={st.state} />
        </span>
      ))}
    </div>
  );
}
