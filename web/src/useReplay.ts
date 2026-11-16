import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Frame } from "./api";
import { duration } from "./format";

// Plan section 7.3: idle gaps longer than 3 seconds are compressed, so a slow
// Argo CD sync does not stall the demo, while stage timers keep showing real
// elapsed time.
const GAP_LIMIT_MS = 3000;
const GAP_SHOWN_MS = 1500;

export const SPEEDS = [1, 4, 16] as const;
export type Speed = (typeof SPEEDS)[number];

export interface Schedule {
  /** Real provider time of each frame, in ms. */
  times: number[];
  /** Playback position of each frame, in ms of compressed time. */
  offsets: number[];
  total: number;
}

export function schedule(frames: Frame[]): Schedule {
  const times = frames.map((f) => Date.parse(f.at));
  const offsets: number[] = [];
  let prevTime = times[0] ?? 0;
  let off = 0;
  for (const t of times) {
    const gap = Math.max(0, t - prevTime);
    off += gap > GAP_LIMIT_MS ? GAP_SHOWN_MS : gap;
    offsets.push(off);
    prevTime = t;
  }
  return { times, offsets, total: off };
}

/** The index of the frame showing at playback position pos. */
export function frameAt(s: Schedule, pos: number): number {
  let lo = 0;
  let hi = s.offsets.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (s.offsets[mid]! <= pos) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

/**
 * The real time at playback position pos: inside a compressed gap, real time
 * runs faster, so stage timers show the recorded durations.
 */
export function realTime(s: Schedule, pos: number): number {
  const i = frameAt(s, pos);
  if (i >= s.times.length - 1) return s.times[s.times.length - 1] ?? 0;
  const span = s.offsets[i + 1]! - s.offsets[i]!;
  const frac = span > 0 ? Math.min(1, Math.max(0, (pos - s.offsets[i]!) / span)) : 0;
  return s.times[i]! + frac * (s.times[i + 1]! - s.times[i]!);
}

/** "⋯ 6m 12s later" while playback crosses a compressed gap. */
export function gapLabel(s: Schedule, pos: number): string | null {
  const i = frameAt(s, pos);
  if (i >= s.times.length - 1 || pos <= s.offsets[i]!) return null;
  const gap = s.times[i + 1]! - s.times[i]!;
  return gap > GAP_LIMIT_MS ? `⋯ ${duration(Math.round(gap / 1000))} later` : null;
}

export function prefersReducedMotion(): boolean {
  try {
    return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  } catch {
    return false;
  }
}

export interface Replay {
  schedule: Schedule;
  index: number;
  pos: number;
  /** The real time to render timers at. */
  now: number;
  playing: boolean;
  speed: Speed;
  ended: boolean;
  gap: string | null;
  play: () => void;
  pause: () => void;
  seek: (pos: number) => void;
  next: () => void;
  prev: () => void;
  setSpeed: (s: Speed) => void;
}

/**
 * Drives playback of a timeline. With reduced motion, timers jump from frame
 * to frame instead of counting through gaps.
 */
export function useReplay(frames: Frame[], autoplay: boolean, initialSpeed: Speed = 4): Replay {
  const sched = useMemo(() => schedule(frames), [frames]);
  const [pos, setPosState] = useState(0);
  const posRef = useRef(0);
  const setPos = useCallback((p: number) => {
    posRef.current = p;
    setPosState(p);
  }, []);
  const [playing, setPlaying] = useState(autoplay);
  const [speed, setSpeed] = useState<Speed>(initialSpeed);
  const reduced = useMemo(prefersReducedMotion, []);
  const last = useRef<number | null>(null);

  useEffect(() => {
    setPos(0);
    setPlaying(autoplay && frames.length > 0);
  }, [frames, autoplay, setPos]);

  useEffect(() => {
    if (!playing) {
      last.current = null;
      return;
    }
    let raf = 0;
    const tick = (ts: number) => {
      const dt = last.current === null ? 0 : ts - last.current;
      last.current = ts;
      const n = Math.min(sched.total, posRef.current + dt * speed);
      setPos(n);
      if (n >= sched.total) {
        setPlaying(false);
        return;
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [playing, speed, sched, setPos]);

  const index = frameAt(sched, pos);
  const ended = frames.length > 0 && pos >= sched.total && !playing;

  const play = useCallback(() => {
    if (posRef.current >= sched.total) setPos(0);
    setPlaying(true);
  }, [sched, setPos]);
  const pause = useCallback(() => setPlaying(false), []);
  const seek = useCallback((p: number) => setPos(Math.max(0, Math.min(sched.total, p))), [sched, setPos]);
  const next = useCallback(() => {
    const i = frameAt(sched, pos);
    setPos(sched.offsets[Math.min(i + 1, sched.offsets.length - 1)] ?? 0);
  }, [sched, pos, setPos]);
  const prev = useCallback(() => {
    const i = frameAt(sched, pos);
    const target = pos > sched.offsets[i]! ? i : Math.max(0, i - 1);
    setPos(sched.offsets[target] ?? 0);
  }, [sched, pos, setPos]);

  return {
    schedule: sched,
    index,
    pos,
    now: reduced ? (sched.times[index] ?? 0) : realTime(sched, pos),
    playing,
    speed,
    ended,
    gap: reduced ? null : gapLabel(sched, pos),
    play,
    pause,
    seek,
    next,
    prev,
    setSpeed,
  };
}
