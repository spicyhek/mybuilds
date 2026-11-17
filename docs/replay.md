# Replay

Every observation is stored once as a timestamped event, and the projector
is a pure function of a release's events, so any past release can be played
back as it happened. See the plan, section 7.3.

## Timeline

`GET /api/v1/releases/{release_id}/timeline` returns every state the release
passed through, oldest first:

```json
{
  "release_id": "personal-site/c2cfca1/production/13",
  "frames": [
    { "at": "2026-11-13T10:00:38Z", "event_type": "github.workflow_run", "release": { "...": "the release view" } }
  ]
}
```

The server builds it with the same projector that keeps the releases table
current (`Projector.Timeline`). It loads the release's inputs once: its own
events, the workload events of its runtime window and any events it shares
with a release deployed together with it. Then, for each event in order, it
projects only what had occurred up to that event:

- events are ordered by provider time, then by event ID, as everywhere else;
- the runtime window does not exist before its sync started, and stays open
  until the next sync started, so a frame never shows a window that was
  closed later;
- a frame that looks the same as the one before it is left out.

The last frame equals the stored release. Each `release` uses the same
allowlisted view as `GET /api/v1/releases/{release_id}`, with each stage's
steps. The browser never projects anything: it renders the frames with the
components it uses for live releases.

### Replay and live

When events arrive in the order they occurred, replay passes through exactly
the states the live view showed (`TestReplayMatchesLive`). A notification
that arrives late, for example Argo CD reporting a sync after the pods have
started, only changes when the live view caught up. Replay shows each event
at the time it occurred, and the final state is the same either way.

A release stored without events, such as a fixture inserted directly, has an
empty timeline.

## Playback

- A replay loads paused at its first event and starts when Play is pressed.
- Play and pause, 1× (the default), 4× and 16× speeds, a scrubber, and
  previous and next event.
- Gaps longer than 3 seconds play as 1.5 seconds and show "⋯ 6m 12s later".
  Real time runs faster through a compressed gap, so stage and step timers
  show the recorded durations.
- A `REPLAY · recorded <date> · 1×` badge takes the place of `LIVE`.
- The stage detail panel follows playback. Steps that playback has not
  reached yet show dimmed.
- Nothing plays on its own. On the homepage, clicking a finished release
  replays it in the player; clicking a running one shows it live. In the
  history screen, each finished release has a Replay button, and on a
  release's page, `?replay=1` starts the replay, so a replay can be shared.
- With `prefers-reduced-motion`, timers jump from frame to frame instead of
  counting through gaps.

## Recorded releases as fixtures

Real releases become regression tests (plan section 7.5). Each one is a
file in `internal/releases/testdata/releases/`, and `TestRecordedReleases`
loads it into an empty database, projects it, and checks that:

- the release and its timeline match the stored snapshot next to it
  (`<name>.golden.json`): the release in full, one line per frame, and a
  hash of every frame;
- replaying it twice gives the same timeline, and the last frame is the
  stored release;
- exporting it again gives back every event it holds.

### The export

`GET /api/v1/releases/{release_id}/events` returns every event the release's
projection reads, in ID order:

```json
{
  "release_id": "personal-site/472d44a/production/14",
  "events": [
    { "id": 2017, "provider": "github", "delivery_id": "…", "type": "github.workflow_run", "attempt": 1,
      "occurred_at": "…", "received_at": "…", "keys": { "release_id": "…", "run_id": 4402… }, "payload": { "…": "…" } }
  ]
}
```

That is more than the release's own events. The projector runs the
release's projection through a store that remembers what it returned:

- the release's own events, the jobs of its runs and the Argo CD
  notifications for its GitOps commit, including the root Application's,
  which the projector must keep ignoring;
- its runtime window's workload events, and the Deployment snapshot from
  before the window;
- the events of any release it was compared with to find what it was
  deployed together with, and the verifier's events of a partner.

The list of syncs comes back as revisions and times, not events. So the
export adds the Argo CD notifications of every sync from the earliest event
read until the end of the window. The window ends where the next sync
starts, and adoption walks every sync after a bump was merged, for this
release and for the releases it was compared with. The kube tests check
this for every release in every scenario: loaded into an empty store, its
export projects the same release and the same timeline.

Payloads are the normalized ones ingestion stored, already built from an
allowlist. The export decodes each into its type and encodes it again, so a
field an older version stored cannot leave, and an event of an unknown type
is an error. The body hash is left out. Raw provider bodies are never stored,
so they cannot be exported.

### Recording a release

```bash
go run ./cmd/record-release -o internal/releases/testdata/releases/personal-site-14.jsonl \
    personal-site/472d44a/production/14
go test ./internal/releases -run TestRecordedReleases -update
```

`-from` points at another instance, such as a local server
(`-from http://localhost:8080`). The file is JSON Lines: a header with the
release ID, where and when it was recorded, then one event per line, so a
re-recording diffs line by line. Review the new snapshot before committing
it. It should show the release as the UI did.

### When a snapshot changes

A projector change that alters a recorded release fails the test with the
first differing line. If the change is intended, rerun with `-update` and
review the snapshot's diff: the frame lines show which states moved, and
the release shows the final steps and summaries. The service map is the
built-in one, so a change to it can change a snapshot too.
