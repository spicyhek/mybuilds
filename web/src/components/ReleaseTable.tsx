import type { Release } from "../api";
import { ReleaseRow } from "./ReleaseRow";

interface Props {
  label: string;
  releases: Release[];
  now: number;
  selected?: string;
  empty: string;
  onSelect: (id: string) => void;
}

export function ReleaseTable({ label, releases, now, selected, empty, onSelect }: Props) {
  return (
    <div role="table" aria-label={label}>
      <div className="tr th" role="row">
        <span role="columnheader">Release</span>
        <span role="columnheader">Pipeline</span>
        <span role="columnheader">Outcome</span>
        <span role="columnheader">Started</span>
        <span role="columnheader">Duration</span>
      </div>
      {releases.length === 0 ? (
        <div className="empty">{empty}</div>
      ) : (
        releases.map((r) => <ReleaseRow key={r.release_id} release={r} now={now} selected={selected === r.release_id} onSelect={onSelect} />)
      )}
    </div>
  );
}
