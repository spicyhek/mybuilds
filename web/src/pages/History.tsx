import { useEffect, useMemo, useState } from "react";
import { fetchReleasePage, type Release, type ReleaseQuery } from "../api";
import { OUTCOME_LABEL } from "../format";
import { ReleaseTable } from "../components/ReleaseTable";
import { navigate, releasePath } from "../route";

const OUTCOMES = ["succeeded", "failed", "degraded", "unknown", "running", "pending", "skipped"] as const;

interface Props {
  query: URLSearchParams;
  /** Live copies from the stream; they replace stale rows. */
  live: Release[];
  services: string[];
  now: number;
}

/**
 * Every release, newest first, with filters kept in the URL so a filtered
 * view can be shared. Pages load with the API's cursor.
 */
export function History({ query, live, services, now }: Props) {
  const q: ReleaseQuery = useMemo(
    () => ({
      service: query.get("service") ?? undefined,
      kind: query.get("kind") ?? undefined,
      outcome: query.get("outcome") ?? undefined,
      drill: query.get("drill") ?? undefined,
    }),
    [query],
  );
  const key = JSON.stringify(q);

  const [rows, setRows] = useState<Release[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    fetchReleasePage(q).then(
      (page) => {
        if (cancelled) return;
        setRows(page.releases);
        setCursor(page.next_cursor);
        setLoading(false);
      },
      (e) => {
        if (cancelled) return;
        setError(e instanceof Error ? e.message : String(e));
        setLoading(false);
      },
    );
    return () => {
      cancelled = true;
    };
    // key is q serialized, so the effect runs once per distinct filter.
  }, [key]);

  const more = async () => {
    if (!cursor) return;
    setLoading(true);
    try {
      const page = await fetchReleasePage(q, cursor);
      setRows((prev) => [...prev, ...page.releases.filter((r) => !prev.some((p) => p.release_id === r.release_id))]);
      setCursor(page.next_cursor);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  const liveById = useMemo(() => new Map(live.map((r) => [r.release_id, r])), [live]);
  const shown = rows.map((r) => liveById.get(r.release_id) ?? r);

  const set = (k: string, v: string) => {
    const next = new URLSearchParams(query);
    if (v) next.set(k, v);
    else next.delete(k);
    const s = next.toString();
    navigate(s ? `/history?${s}` : "/history", { replace: true });
  };

  return (
    <section className="card" aria-label="Release history">
      <div className="card-head">
        <span>Release history</span>
        <span className="spacer" />
        <div className="filters">
          <select aria-label="Filter by service" value={q.service ?? ""} onChange={(e) => set("service", e.target.value)}>
            <option value="">All services</option>
            {services.map((s) => (
              <option key={s}>{s}</option>
            ))}
          </select>
          <select aria-label="Filter by kind" value={q.kind ?? ""} onChange={(e) => set("kind", e.target.value)}>
            <option value="">All kinds</option>
            <option value="application">Application</option>
            <option value="config">Config</option>
          </select>
          <select aria-label="Filter by outcome" value={q.outcome ?? ""} onChange={(e) => set("outcome", e.target.value)}>
            <option value="">All outcomes</option>
            {OUTCOMES.map((o) => (
              <option key={o} value={o}>
                {OUTCOME_LABEL[o]}
              </option>
            ))}
          </select>
          <label>
            <input type="checkbox" checked={q.drill !== "false"} onChange={(e) => set("drill", e.target.checked ? "" : "false")} /> Drills
          </label>
        </div>
      </div>
      {error && (
        <div className="banner" role="alert">
          {error}
        </div>
      )}
      <ReleaseTable
        label="Release history"
        releases={shown}
        now={now}
        empty={loading ? "Loading…" : "No releases match the filters."}
        onSelect={(id) => navigate(releasePath(id))}
      />
      {cursor && (
        <div className="more">
          <button type="button" className="btn" onClick={() => void more()} disabled={loading}>
            {loading ? "Loading…" : "Load more"}
          </button>
        </div>
      )}
    </section>
  );
}
