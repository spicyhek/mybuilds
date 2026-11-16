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
