import type { State } from "../api";

const paths: Record<State, string> = {
  succeeded: '<path d="M3.5 8.5l3 3 6-7"/>',
  failed: '<path d="M4 4l8 8M12 4l-8 8"/>',
  degraded: '<path d="M8 2.5l6 11H2z"/><path d="M8 7v3M8 12v.5"/>',
  running: '<path d="M8 2.5a5.5 5.5 0 1 0 5.5 5.5"/>',
  unknown: '<path d="M6 6a2 2 0 1 1 3 1.7c-.7.4-1 .9-1 1.6"/><path d="M8 12v.5"/>',
  pending: '<circle cx="8" cy="8" r="5"/>',
  skipped: '<path d="M4 8h8"/>',
};

export function StateIcon({ state, className = "" }: { state: State; className?: string }) {
  const spin = state === "running" ? " spin" : "";
  return (
    <svg
      className={`i${spin} ${className}`.trim()}
      viewBox="0 0 16 16"
      aria-hidden="true"
      dangerouslySetInnerHTML={{ __html: paths[state] }}
    />
  );
}

export const CopyIcon = () => (
  <svg className="i" viewBox="0 0 16 16" aria-hidden="true">
    <rect x="5.5" y="5.5" width="8" height="8" rx="1.5" />
    <path d="M10.5 5.5V3.8c0-.7-.6-1.3-1.3-1.3H3.8c-.7 0-1.3.6-1.3 1.3v5.4c0 .7.6 1.3 1.3 1.3h1.7" />
  </svg>
);

export const SunIcon = () => (
  <svg className="i" viewBox="0 0 16 16" aria-hidden="true">
    <circle cx="8" cy="8" r="3" />
    <path d="M8 1.5v1.3M8 13.2v1.3M1.5 8h1.3M13.2 8h1.3M3.4 3.4l.9.9M11.7 11.7l.9.9M3.4 12.6l.9-.9M11.7 4.3l.9-.9" />
  </svg>
);

export const MoonIcon = () => (
  <svg className="i" viewBox="0 0 16 16" aria-hidden="true">
    <path d="M13.5 9.6A5.8 5.8 0 0 1 6.4 2.5a5.8 5.8 0 1 0 7.1 7.1z" />
  </svg>
);

export const GitHubIcon = () => (
  <svg className="i" viewBox="0 0 16 16" aria-hidden="true">
    <path d="M6 13.5c-3 .9-3-1.5-4.2-1.8M10 15v-2.3c0-.7.1-1.2-.3-1.6 1.8-.2 3.6-.9 3.6-4a3.1 3.1 0 0 0-.8-2.1 2.9 2.9 0 0 0-.1-2.1s-.7-.2-2.2.8a7.6 7.6 0 0 0-4 0C4.7 2.7 4 2.9 4 2.9a2.9 2.9 0 0 0 0 2.1 3.1 3.1 0 0 0-.8 2.1c0 3.1 1.8 3.8 3.6 4-.4.4-.4.9-.3 1.6V15" />
  </svg>
);
