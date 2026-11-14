import { useCallback, useEffect, useRef, useState } from "react";
import { fetchRelease, fetchReleases, type Release } from "./api";

export type Connection = "connecting" | "live" | "reconnecting";

export interface LiveReleases {
  releases: Release[];
  connection: Connection;
  error: string | null;
  /** The release ID of the most recent stream message, for the status line. */
  lastTouched: string | null;
  /** Loads a release with its steps into the list. */
  loadDetail: (id: string) => Promise<void>;
}

const byNewest = (a: Release, b: Release) => (a.created_at < b.created_at ? 1 : a.created_at > b.created_at ? -1 : 0);

/**
 * Keeps the release list current: a REST snapshot first, then
 * release.created / release.updated messages from /api/v1/stream. The
 * browser resumes with Last-Event-ID; the server replays what was missed or
 * sends `snapshot`, in which case the list is fetched again.
 */
export function useLiveReleases(): LiveReleases {
  const [releases, setReleases] = useState<Release[]>([]);
  const [connection, setConnection] = useState<Connection>("connecting");
  const [error, setError] = useState<string | null>(null);
  const [lastTouched, setLastTouched] = useState<string | null>(null);
  const hadError = useRef(false);

  const upsert = useCallback((rel: Release) => {
    setReleases((prev) => {
      const next = prev.filter((r) => r.release_id !== rel.release_id);
      next.push(rel);
      next.sort(byNewest);
      return next;
    });
  }, []);

  const snapshot = useCallback(async () => {
    try {
      const list = await fetchReleases();
      setReleases((prev) => {
        // Keep steps already loaded; the list endpoint does not carry them.
        const steps = new Map(prev.filter((r) => r.stages.some((s) => s.steps)).map((r) => [r.release_id, r]));
        return list
          .map((r) => {
            const known = steps.get(r.release_id);
            return known && known.created_at === r.created_at && sameShape(known, r) ? known : r;
          })
          .sort(byNewest);
      });
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  const loadDetail = useCallback(
    async (id: string) => {
      try {
        upsert(await fetchRelease(id));
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
    },
    [upsert],
  );

  useEffect(() => {
    void snapshot();
    const es = new EventSource("/api/v1/stream");

    es.addEventListener("hello", () => {
      setConnection("live");
      if (hadError.current) {
        // Reconnected: the server replays missed events, but a snapshot
        // costs one request and covers a replay that was cut short.
        hadError.current = false;
        void snapshot();
      }
    });
    const onRelease = (ev: MessageEvent<string>) => {
      const rel = JSON.parse(ev.data) as Release;
      upsert(rel);
      setLastTouched(rel.release_id);
    };
    es.addEventListener("release.created", onRelease);
    es.addEventListener("release.updated", onRelease);
    es.addEventListener("snapshot", () => void snapshot());
    es.onerror = () => {
      hadError.current = true;
      setConnection("reconnecting");
    };
    return () => es.close();
  }, [snapshot, upsert]);

  return { releases, connection, error, lastTouched, loadDetail };
}

// sameShape is true when the stage states and timestamps match, so a detailed
// copy is still current.
export function sameShape(a: Release, b: Release): boolean {
  if (a.status !== b.status || a.stages.length !== b.stages.length) return false;
  return a.stages.every((s, i) => {
    const o = b.stages[i];
    return !!o && s.state === o.state && s.started_at === o.started_at && s.completed_at === o.completed_at && s.attempt === o.attempt;
  });
}
