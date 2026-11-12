import { useEffect, useMemo, useState } from "react";
import { fetchMeta, type Meta, type Release } from "./api";
import { OUTCOME_LABEL, relTime, shortSha, statusLine } from "./format";
import { GitHubIcon, MoonIcon, StateIcon, SunIcon } from "./components/Icons";
import { Pipeline } from "./components/Pipeline";
import { ReleaseDetails } from "./components/ReleaseDetails";
import { ReleaseRow } from "./components/ReleaseRow";
import { StagePanel } from "./components/StagePanel";
import { useLiveReleases } from "./useLiveReleases";

type Theme = "light" | "dark" | null;

export default function App() {
  const { releases, connection, error, lastTouched, loadDetail } = useLiveReleases();
  const [meta, setMeta] = useState<Meta | null>(null);
  const [picked, setPicked] = useState<string | null>(null);
  const [pinnedStage, setPinnedStage] = useState<string | null>(null);
  const [showDrills, setShowDrills] = useState(true);
  const [service, setService] = useState("");
  const [theme, setTheme] = useState<Theme>(() => {
    try {
      const t = localStorage.getItem("mybuilds-theme");
      return t === "light" || t === "dark" ? t : null;
    } catch {
      return null;
    }
  });

  useEffect(() => {
    fetchMeta().then(setMeta, () => undefined);
  }, []);

  useEffect(() => {
    try {
      if (theme) {
        document.documentElement.setAttribute("data-theme", theme);
        localStorage.setItem("mybuilds-theme", theme);
      } else {
        document.documentElement.removeAttribute("data-theme");
        localStorage.removeItem("mybuilds-theme");
      }
    } catch {
      // Storage is a convenience only.
    }
  }, [theme]);

  const visible = useMemo(
    () => releases.filter((r) => (showDrills || !r.drill) && (!service || r.service === service)),
    [releases, showDrills, service],
  );

  // The player follows the most recently touched release until the user
  // picks one; a running release wins over an idle one.
  const playing: Release | undefined = useMemo(() => {
    if (picked) {
      const p = visible.find((r) => r.release_id === picked);
      if (p) return p;
    }
    const touched = lastTouched ? visible.find((r) => r.release_id === lastTouched) : undefined;
    if (touched) return touched;
    return visible.find((r) => r.status === "running") ?? visible[0];
  }, [picked, lastTouched, visible]);

  // Steps come with the single-release endpoint; load them once per release.
  useEffect(() => {
    if (playing && !playing.stages.some((s) => s.steps)) void loadDetail(playing.release_id);
  }, [playing, loadDetail]);

  const now = useNow(releases.some((r) => r.status === "running"));

  const focusKey = useMemo(() => {
    if (!playing) return null;
    if (pinnedStage && playing.stages.some((s) => s.key === pinnedStage)) return pinnedStage;
    const active = playing.stages.find((s) => s.state === "running") ?? playing.stages.find((s) => s.state === "failed");
    if (active) return active.key;
    const last = [...playing.stages].reverse().find((s) => s.state !== "pending");
    return (last ?? playing.stages[0])?.key ?? null;
  }, [playing, pinnedStage]);
  const focusStage = playing?.stages.find((s) => s.key === focusKey);
  const svc = meta?.services.find((s) => s.name === playing?.service);
  const services = useMemo(() => Array.from(new Set(releases.map((r) => r.service))).sort(), [releases]);

  return (
    <>
      <header className="topbar">
        <div className="crumbs">
          <span className="brand">mybuilds</span>
          <span className="crumb-sep" aria-hidden="true">
            ›
          </span>
          <span className="crumb-cur">Pipelines</span>
        </div>
        <div className="top-actions">
          {meta?.fixtures && (
            <span className="fixture" title="Fixture mode: sample releases and a replayed event stream">
              Fixture data
            </span>
          )}
          <ConnectionBadge connection={connection} />
          <a className="top-link" href="https://github.com/spicyhek/mybuilds" target="_blank" rel="noopener">
            <GitHubIcon />
            <span>Source</span>
          </a>
          <div className="seg-ctl" role="radiogroup" aria-label="Color theme">
            <button type="button" role="radio" aria-checked={theme === "light"} aria-label="Light theme" title="Light" onClick={() => setTheme(theme === "light" ? null : "light")}>
              <SunIcon />
            </button>
            <button type="button" role="radio" aria-checked={theme === "dark"} aria-label="Dark theme" title="Dark" onClick={() => setTheme(theme === "dark" ? null : "dark")}>
              <MoonIcon />
            </button>
          </div>
        </div>
      </header>

      <main>
        {error && (
          <div className="banner" role="alert">
            {error}
          </div>
        )}
        <section className="layout">
          <div className="col">
            <section className="card" aria-label="Release player">
              <div className="card-head">
                <span>{playing?.status === "running" ? "Now playing" : "Latest release"}</span>
                {playing?.status === "running" && <span className="badge badge-live">LIVE</span>}
                <span className="spacer" />
                {playing && <OutcomePill release={playing} />}
              </div>
              <div className="card-body">
                {playing ? (
                  <>
                    <div className="pl-top">
                      <div style={{ minWidth: 0 }}>
                        <div className="pl-title">
                          {playing.service}
                          {playing.drill && <span className="tag tag-drill">DRILL</span>}
                          {playing.kind === "config" && <span className="tag tag-config">CONFIG</span>}
                          <span className="mono" style={{ fontWeight: 400 }}>
                            {shortSha(playing.kind === "config" ? playing.gitops_sha : playing.source_sha)}
                          </span>
                        </div>
                        <div className="pl-sub">
                          {playing.release_id} · started {relTime(playing.created_at, now)}
                          {picked && (
                            <>
                              {" · "}
                              <button type="button" className="linkish" onClick={() => setPicked(null)}>
                                follow latest
                              </button>
                            </>
                          )}
                        </div>
                      </div>
                    </div>
                    <div className="canvas">
                      <Pipeline
                        release={playing}
                        now={now}
                        focus={focusKey}
                        pinned={pinnedStage !== null}
                        onPick={(key) => setPinnedStage(pinnedStage === key ? null : key)}
                      />
                    </div>
                    <StatusLine release={playing} />
                    {focusStage && <StagePanel release={playing} stage={focusStage} service={svc} now={now} pinned={pinnedStage !== null} />}
                  </>
                ) : (
                  <div className="empty">{releases.length === 0 && !error ? "Waiting for the first release…" : "No releases match the filter."}</div>
                )}
              </div>
            </section>

            <section className="card" aria-label="Recent releases">
              <div className="card-head">
                <span>Recent releases</span>
                <span className="spacer" />
                <div className="filters">
                  <select aria-label="Filter by service" value={service} onChange={(e) => setService(e.target.value)}>
                    <option value="">All services</option>
                    {services.map((s) => (
                      <option key={s}>{s}</option>
                    ))}
                  </select>
                  <label>
                    <input type="checkbox" checked={showDrills} onChange={(e) => setShowDrills(e.target.checked)} /> Drills
                  </label>
                </div>
              </div>
              <div role="table" aria-label="Recent releases">
                <div className="tr th" role="row">
                  <span role="columnheader">Release</span>
                  <span role="columnheader">Pipeline</span>
                  <span role="columnheader">Outcome</span>
                  <span role="columnheader">Started</span>
                  <span role="columnheader">Duration</span>
                </div>
                {visible.length === 0 ? (
                  <div className="empty">No releases yet.</div>
                ) : (
                  visible.map((r) => (
                    <ReleaseRow key={r.release_id} release={r} now={now} selected={playing?.release_id === r.release_id} onSelect={(id) => setPicked(id === picked ? null : id)} />
                  ))
                )}
              </div>
            </section>
          </div>

          <aside className="col sidebar">
            <section className="side" aria-label="Release details">
              <h2>Release details</h2>
              {playing ? <ReleaseDetails release={playing} /> : <p className="await">Select a release.</p>}
            </section>
            <section className="side" aria-label="About">
              <h2>About</h2>
              <p className="about">
                Each stage completes only on its own evidence: a green CI run, an Argo CD sync, a Ready pod and a passing public check are four separate facts. Missing evidence shows as <em>unknown</em>, never as success.
              </p>
              {meta && (
                <p className="about mono">
                  {meta.release_id} · {meta.version}
                </p>
              )}
            </section>
          </aside>
        </section>
      </main>
    </>
  );
}

function ConnectionBadge({ connection }: { connection: "connecting" | "live" | "reconnecting" }) {
  const label = connection === "live" ? "Live" : connection === "connecting" ? "Connecting" : "Reconnecting";
  const title = connection === "live" ? "Event stream connected" : "Event stream not connected; the browser retries automatically";
  return (
    <span className={`conn conn-${connection}`} title={title} role="status">
      <span className="conn-dot" aria-hidden="true" />
      {label}
    </span>
  );
}

function OutcomePill({ release: r }: { release: Release }) {
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

/** Ticks once a second while something is running, so timers count. */
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    setNow(Date.now());
    if (!active) return;
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [active]);
  return now;
}
