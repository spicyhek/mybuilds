import { useEffect, useState } from "react";
import { fetchRelease, HTTPError, type Release, type Service } from "../api";
import { Player } from "../components/Player";
import { ReplayPlayer } from "../components/ReplayPlayer";
import { navigate, onLinkClick, releasePath } from "../route";
import { sameShape } from "../useLiveReleases";

interface Props {
  id: string;
  /** Start in replay, from ?replay=1. */
  replay: boolean;
  /** Live copies from the stream; a change refetches the release. */
  live: Release[];
  services: Service[];
  now: number;
  onLoaded: (r: Release | null) => void;
}

/** One release at its shareable URL, /releases/{release_id}. */
export function ReleasePage({ id, replay, live, services, now, onLoaded }: Props) {
  const [rel, setRel] = useState<Release | null>(null);
  const [state, setState] = useState<"loading" | "ok" | "missing" | "error">("loading");
  const [copied, setCopied] = useState(false);

  const load = (showLoading: boolean) => {
    if (showLoading) setState("loading");
    fetchRelease(id).then(
      (r) => {
        setRel(r);
        setState("ok");
      },
      (e) => {
        if (e instanceof HTTPError && e.status === 404) setState("missing");
        else if (showLoading) setState("error");
      },
    );
  };

  useEffect(() => {
    setRel(null);
    load(true);
    // load depends only on id.
  }, [id]);

  // The stream carries every change; refetch for the steps when the
  // release changes, or appears after its link was opened.
  const liveCopy = live.find((r) => r.release_id === id);
  useEffect(() => {
    if (liveCopy && (!rel || !sameShape(liveCopy, rel))) load(false);
  }, [liveCopy]);

  useEffect(() => {
    onLoaded(rel && rel.release_id === id ? rel : null);
  }, [rel, id, onLoaded]);

  useEffect(() => {
    document.title = `${id} · mybuilds`;
    return () => {
      document.title = "mybuilds";
    };
  }, [id]);

  if (!rel || rel.release_id !== id) {
    return (
      <section className="card" aria-label="Release">
        <div className="empty" role={state === "missing" || state === "error" ? "alert" : undefined}>
          {state === "missing" ? (
            <>
              No release <span className="mono">{id}</span>.{" "}
              <a href="/history" onClick={onLinkClick}>
                Browse the history
              </a>
            </>
          ) : state === "error" ? (
            "The release could not be loaded."
          ) : (
            "Loading…"
          )}
        </div>
      </section>
    );
  }

  const service = services.find((s) => s.name === rel.service);
  const setReplay = (on: boolean) => navigate(on ? `${releasePath(id)}?replay=1` : releasePath(id), { replace: true });

  const copy = () => {
    void navigator.clipboard?.writeText(window.location.href).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    });
  };

  const copyButton = (
    <button type="button" className="linkish" onClick={copy}>
      {copied ? "Link copied" : "Copy link"}
    </button>
  );
  if (replay) {
    return <ReplayPlayer releaseId={id} service={service} onExit={() => setReplay(false)} />;
  }
  return (
    <Player
      release={rel}
      service={service}
      now={now}
      heading="Release"
      actions={
        <>
          {rel.status !== "running" && (
            <button type="button" className="btn replay-btn" onClick={() => setReplay(true)}>
              Replay
            </button>
          )}
          {copyButton}
        </>
      }
    />
  );
}
