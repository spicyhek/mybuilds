package releases

import (
	"context"
	"reflect"
	"sort"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
)

// Frame is a release as it stood at one moment of its history (plan section
// 7.3).
type Frame struct {
	// At is the provider time of the events that produced this state.
	At time.Time
	// EventID and EventType name the last of those events.
	EventID   int64
	EventType string
	// Release has each stage's current steps filled in, as GetRelease does.
	Release Release
}

// Timeline replays a release: it runs the projector over the release's
// events in provider-time order and returns every state the release passed
// through, oldest first. The browser animates the frames with the same
// components it uses for live releases and never projects anything itself.
//
// Each frame is the projection of the events that had occurred by its time,
// with the runtime window cut at that time, so replay shows what the
// pipeline showed when the events arrived in order. Times at which nothing
// visible changed are left out. The last frame equals the stored projection.
func (p *Projector) Timeline(ctx context.Context, releaseID string) ([]Frame, error) {
	in, err := p.inputs(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	all := append([]events.Event(nil), in.events...)
	if in.runtime != nil {
		rt := *in.runtime
		rt.Events = append([]events.Event(nil), rt.Events...)
		sortEvents(rt.Events)
		in.runtime = &rt
		all = append(all, rt.Events...)
		// The next sync closes the window, which can change the release,
		// as a rollout cut off there becomes unknown. That sync is not one
		// of the release's events, so it gets a frame of its own and the
		// last frame still equals the stored release.
		if rt.End != nil {
			all = append(all, events.Event{Type: events.TypeArgoCD, OccurredAt: *rt.End})
		}
	}
	sortEvents(all)
	evs := append([]events.Event(nil), in.events...)
	sortEvents(evs)

	var frames []Frame
	var prev Release
	for _, e := range all {
		// Each event is applied on its own, ordered by time and then ID, as
		// the live projection applied them on arrival.
		t := e.OccurredAt
		proj, err := ProjectWithRuntime(releaseID, upTo(evs, e), in.svc, runtimeAt(in.runtime, e))
		if err != nil {
			return nil, err
		}
		rel := withSteps(proj)
		if len(frames) > 0 && reflect.DeepEqual(prev, rel) {
			continue
		}
		frames = append(frames, Frame{At: t, EventID: e.ID, EventType: e.Type, Release: rel})
		prev = rel
	}
	return frames, nil
}

func sortEvents(evs []events.Event) {
	sort.SliceStable(evs, func(i, j int) bool { return before(evs[i], evs[j]) })
}

// before reports whether a sorts before b: by time, then by ID.
func before(a, b events.Event) bool {
	if !a.OccurredAt.Equal(b.OccurredAt) {
		return a.OccurredAt.Before(b.OccurredAt)
	}
	return a.ID < b.ID
}

// upTo returns the prefix of sorted events up to and including e's place.
func upTo(sorted []events.Event, e events.Event) []events.Event {
	n := sort.Search(len(sorted), func(i int) bool { return before(e, sorted[i]) })
	return sorted[:n]
}

// runtimeAt is the runtime window as it stood once e was applied: nothing
// before its sync started, still open until the next sync started, holding
// only the workload events up to e. rt.Events must be sorted.
func runtimeAt(rt *Runtime, e events.Event) *Runtime {
	t := e.OccurredAt
	if rt == nil || t.Before(rt.Start) {
		return nil
	}
	cut := *rt
	if cut.End != nil && t.Before(*cut.End) {
		cut.End = nil
	}
	cut.Events = upTo(rt.Events, e)
	return &cut
}

// withSteps attaches each stage's current attempt's steps.
func withSteps(p Projection) Release {
	r := p.Release
	r.Stages = append([]Stage(nil), r.Stages...)
	for i := range r.Stages {
		r.Stages[i].Steps = nil
		for _, st := range p.Steps {
			if st.StageKey == r.Stages[i].Key && st.Attempt == r.Stages[i].Attempt {
				r.Stages[i].Steps = append(r.Stages[i].Steps, st)
			}
		}
	}
	return r
}
