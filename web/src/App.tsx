import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { fetchMeta, type Meta, type Release } from "./api";
import { GitHubIcon, MoonIcon, SunIcon } from "./components/Icons";
import { Player } from "./components/Player";
import { ReleaseDetails } from "./components/ReleaseDetails";
import { ReleaseTable } from "./components/ReleaseTable";
import { History } from "./pages/History";
import { ReleasePage } from "./pages/ReleasePage";
import { onLinkClick, releasePath, useRoute } from "./route";
import { useLiveReleases } from "./useLiveReleases";

type Theme = "light" | "dark" | null;

export default function App() {
  const route = useRoute();
  const live = useLiveReleases();
  const { releases, connection, error } = live;
  const [meta, setMeta] = useState<Meta | null>(null);
  const [routed, setRouted] = useState<Release | null>(null);
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

  useEffect(() => {
    if (route.name === "history") document.title = "History · mybuilds";
    else if (route.name !== "release") document.title = "mybuilds";
  }, [route]);

  const now = useNow(releases.some((r) => r.status === "running") || routed?.status === "running");
  const services = useMemo(
    () => Array.from(new Set([...releases.map((r) => r.service), ...(meta?.services.map((s) => s.name) ?? [])])).sort(),
    [releases, meta],
  );
  const onRouted = useCallback((r: Release | null) => setRouted(r), []);

  let content: ReactNode;
  let detail: Release | undefined;
  let crumb = "Pipelines";
  switch (route.name) {
    case "pipelines":
      content = <Pipelines live={live} meta={meta} services={services} now={now} onPlaying={onRouted} />;
      detail = routed ?? undefined;
      break;
    case "history":
      crumb = "History";
      content = <History query={route.query} live={releases} services={services} now={now} />;
      break;
    case "release":
      crumb = route.id;
      content = <ReleasePage id={route.id} live={releases} services={meta?.services ?? []} now={now} onLoaded={onRouted} />;
      detail = routed ?? undefined;
      break;
    default:
      crumb = "Not found";
      content = (
        <section className="card">
          <div className="empty">
            Nothing here.{" "}
            <a href="/" onClick={onLinkClick}>
              Back to the pipelines
            </a>
          </div>
        </section>
      );
  }

  return (
    <>
      <header className="topbar">
        <div className="crumbs">
          <a className="brand" href="/" onClick={onLinkClick}>
            mybuilds
          </a>
          <span className="crumb-sep" aria-hidden="true">
            ›
          </span>
          {route.name === "release" && (
            <>
              <a className="crumb-link" href="/history" onClick={onLinkClick}>
                History
              </a>
              <span className="crumb-sep" aria-hidden="true">
                ›
              </span>
            </>
          )}
          <span className="crumb-cur" title={crumb}>
            {crumb}
          </span>
        </div>
        <div className="top-actions">
          <nav className="nav" aria-label="Screens">
            <a href="/" onClick={onLinkClick} aria-current={route.name === "pipelines" ? "page" : undefined}>
              Pipelines
            </a>
            <a href="/history" onClick={onLinkClick} aria-current={route.name === "history" ? "page" : undefined}>
              History
            </a>
          </nav>
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
          <div className="col">{content}</div>
          <aside className="col sidebar">
            {detail && (
              <section className="side" aria-label="Release details">
                <h2>Release details</h2>
                <ReleaseDetails release={detail} />
              </section>
            )}
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

interface PipelinesProps {
  live: ReturnType<typeof useLiveReleases>;
  meta: Meta | null;
  services: string[];
  now: number;
  onPlaying: (r: Release | null) => void;
}

/** The homepage: the player over the most recent releases. */
function Pipelines({ live, meta, services, now, onPlaying }: PipelinesProps) {
  const { releases, error, lastTouched, loadDetail } = live;
  const [picked, setPicked] = useState<string | null>(null);
  const [showDrills, setShowDrills] = useState(true);
  const [service, setService] = useState("");

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

  useEffect(() => {
    onPlaying(playing ?? null);
  }, [playing, onPlaying]);

  // Steps come with the single-release endpoint; load them once per release.
  useEffect(() => {
    if (playing && !playing.stages.some((s) => s.steps)) void loadDetail(playing.release_id);
  }, [playing, loadDetail]);

  return (
    <>
      {playing ? (
        <Player
          release={playing}
          service={meta?.services.find((s) => s.name === playing.service)}
          now={now}
          heading={playing.status === "running" ? "Now playing" : "Latest release"}
          sub={
            <>
              {" · "}
              <a href={releasePath(playing.release_id)} onClick={onLinkClick}>
                link
              </a>
              {picked && (
                <>
                  {" · "}
                  <button type="button" className="linkish" onClick={() => setPicked(null)}>
                    follow latest
                  </button>
                </>
              )}
            </>
          }
        />
      ) : (
        <section className="card" aria-label="Release player">
          <div className="empty">{releases.length === 0 && !error ? "Waiting for the first release…" : "No releases match the filter."}</div>
        </section>
      )}

      <section className="card" aria-label="Recent releases">
        <div className="card-head">
          <span>Recent releases</span>
          <a className="head-link" href="/history" onClick={onLinkClick}>
            All releases
          </a>
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
        <ReleaseTable
          label="Recent releases"
          releases={visible}
          now={now}
          selected={playing?.release_id}
          empty="No releases yet."
          onSelect={(id) => setPicked(id === picked ? null : id)}
        />
      </section>
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
