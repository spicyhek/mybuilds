# Recorded releases

Each `<name>.jsonl` is a real release's events, recorded from production
with `cmd/record-release`, and `<name>.golden.json` is what it must keep
projecting to. `TestRecordedReleases` replays every pair. How to record one
and how to update a snapshot is in [`docs/replay.md`](../../../../docs/replay.md),
"Recorded releases as fixtures".

The events are the normalized, allowlisted ones mybuilds stores, the same
data the public API serves; no raw provider bodies or secrets are recorded.
