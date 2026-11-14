import { useEffect, useState, type MouseEvent } from "react";

// A tiny client-side router: three screens do not need a library. The Go
// server answers every non-API path with index.html (internal/httpapi/static.go).

export type Route =
  | { name: "pipelines" }
  | { name: "history"; query: URLSearchParams }
  | { name: "release"; id: string }
  | { name: "not-found" };

export function parseRoute(path: string, search: string): Route {
  if (path === "/" || path === "") return { name: "pipelines" };
  if (path === "/history" || path === "/history/") return { name: "history", query: new URLSearchParams(search) };
  if (path.startsWith("/releases/")) {
    const id = path
      .slice("/releases/".length)
      .split("/")
      .filter(Boolean)
      .map((seg) => {
        try {
          return decodeURIComponent(seg);
        } catch {
          return seg;
        }
      })
      .join("/");
    if (id) return { name: "release", id };
  }
  return { name: "not-found" };
}

/** The shareable URL of a release. Release IDs contain slashes. */
export function releasePath(id: string): string {
  return "/releases/" + id.split("/").map(encodeURIComponent).join("/");
}

const NAV_EVENT = "mybuilds:navigate";

export function navigate(to: string, opts: { replace?: boolean } = {}) {
  if (to === window.location.pathname + window.location.search) return;
  if (opts.replace) window.history.replaceState(null, "", to);
  else window.history.pushState(null, "", to);
  window.dispatchEvent(new Event(NAV_EVENT));
  if (!opts.replace) window.scrollTo(0, 0);
}

export function useRoute(): Route {
  const read = () => parseRoute(window.location.pathname, window.location.search);
  const [route, setRoute] = useState<Route>(read);
  useEffect(() => {
    const on = () => setRoute(read());
    window.addEventListener("popstate", on);
    window.addEventListener(NAV_EVENT, on);
    return () => {
      window.removeEventListener("popstate", on);
      window.removeEventListener(NAV_EVENT, on);
    };
  }, []);
  return route;
}

/**
 * onLinkClick turns a plain left click on an <a> into client-side
 * navigation, leaving modified clicks (new tab, new window) to the browser.
 */
export function onLinkClick(e: MouseEvent<HTMLAnchorElement>) {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  const href = e.currentTarget.getAttribute("href");
  if (!href || !href.startsWith("/")) return;
  e.preventDefault();
  navigate(href);
}
