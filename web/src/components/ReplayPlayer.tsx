import { useEffect, useState } from "react";
import { fetchTimeline, type Frame, type Service } from "../api";
import { SPEEDS, useReplay, type Speed } from "../useReplay";
import { Player } from "./Player";

interface Props {
  releaseId: string;
  service?: Service;
  autoplay: boolean;
  speed?: Speed;
  heading?: string;
  /** Shown as an "Exit replay" control; omit to hide it. */
  onExit?: () => void;
  /** Called once when playback reaches the end. */
  onEnded?: () => void;
  /** Called instead of showing a message when there is nothing to replay. */
  onUnavailable?: () => void;
}

const recorded = (iso: string) =>
  new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });

/**
 * Replays a release from its server-side timeline with the same Player the
 * live view uses (plan section 7.3). It never projects anything itself.
 */
export function ReplayPlayer({ releaseId, service, autoplay, speed, heading = "Replay", onExit, onEnded, onUnavailable }: Props) {
  const [frames, setFrames] = useState<Frame[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setFrames(null);
    setError(null);
    fetchTimeline(releaseId).then(
      (t) => !cancelled && setFrames(t.frames),
      (e) => !cancelled && setError(e instanceof Error ? e.message : String(e)),
    );
    return () => {
      cancelled = true;
    };
  }, [releaseId]);

  const unavailable = !!error || (frames !== null && frames.length === 0);
  useEffect(() => {
    if (unavailable) onUnavailable?.();
  }, [unavailable, onUnavailable]);

  if (unavailable) {
    if (onUnavailable) return null;
    return (
      <section className="card" aria-label="Release replay">
        <div className="empty">
          {error ? "The replay could not be loaded." : "This release has no recorded events to replay."}
          {onExit && (
            <>
              {" "}
              <button type="button" className="linkish" onClick={onExit}>
                Back
              </button>
            </>
          )}
        </div>
      </section>
    );
  }
  if (!frames) {
    return (
      <section className="card" aria-label="Release replay">
        <div className="empty">Loading the replay…</div>
      </section>
    );
  }
  return <Loaded frames={frames} service={service} autoplay={autoplay} speed={speed} heading={heading} onExit={onExit} onEnded={onEnded} />;
}

function Loaded({
  frames,
  service,
  autoplay,
  speed,
  heading,
  onExit,
  onEnded,
}: { frames: Frame[] } & Omit<Props, "releaseId"> & { heading: string }) {
  const rp = useReplay(frames, autoplay, speed);
  const frame = frames[rp.index] ?? frames[0]!;
  const final = frames[frames.length - 1]!.release;

  useEffect(() => {
    if (rp.ended) onEnded?.();
  }, [rp.ended, onEnded]);

  const badge = `REPLAY · recorded ${recorded(frames[0]!.at)} · ${rp.speed}×`;
  const controls = (
    <div className="replay-bar" role="group" aria-label="Replay controls">
      <button type="button" className="btn btn-icon" onClick={rp.prev} aria-label="Previous event" title="Previous event" disabled={rp.pos <= 0}>
        ⏮
      </button>
      {rp.playing ? (
        <button type="button" className="btn btn-icon" onClick={rp.pause} aria-label="Pause" title="Pause">
          ⏸
        </button>
      ) : (
        <button type="button" className="btn btn-icon" onClick={rp.play} aria-label={rp.ended ? "Replay again" : "Play"} title={rp.ended ? "Replay again" : "Play"}>
          {rp.ended ? "↻" : "▶"}
        </button>
      )}
      <button
        type="button"
        className="btn btn-icon"
        onClick={rp.next}
        aria-label="Next event"
        title="Next event"
        disabled={rp.index >= frames.length - 1}
      >
        ⏭
      </button>
      <input
        className="scrub"
        type="range"
        min={0}
        max={Math.max(1, Math.round(rp.schedule.total))}
        step={1}
        value={Math.round(rp.pos)}
        onChange={(e) => rp.seek(Number(e.target.value))}
        aria-label="Replay position"
        aria-valuetext={`Event ${rp.index + 1} of ${frames.length}`}
      />
      <span className="replay-pos" aria-live="off">
        {rp.gap ?? `${rp.index + 1} / ${frames.length}`}
      </span>
      <div className="seg-ctl" role="radiogroup" aria-label="Replay speed">
        {SPEEDS.map((s) => (
          <button key={s} type="button" role="radio" aria-checked={rp.speed === s} onClick={() => rp.setSpeed(s)}>
            {s}×
          </button>
        ))}
      </div>
      {onExit && (
        <button type="button" className="linkish" onClick={onExit}>
          Exit replay
        </button>
      )}
    </div>
  );

  return (
    <Player
      release={frame.release}
      service={service}
      now={rp.now}
      heading={heading}
      replay={{ badge, final }}
      controls={controls}
    />
  );
}
