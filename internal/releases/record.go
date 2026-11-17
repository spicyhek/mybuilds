package releases

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
)

// RecordedEvents returns every event the projection of a release reads,
// ordered by ID: its own events, its runtime window's workload events and
// the Deployment snapshot before it, the events of the releases it was
// compared with to find what it was deployed together with, and the Argo CD
// syncs from the earliest of those events to the end of its window. Loaded into an empty store, they project the same
// release and the same timeline. This is the export behind recorded test
// fixtures (plan section 7.5).
func (p *Projector) RecordedEvents(ctx context.Context, releaseID string) ([]events.Event, error) {
	rec := &recordingStore{ProjectionStore: p.store, seen: map[int64]events.Event{}, before: map[int64]bool{}}
	rp := &Projector{store: rec, services: p.services, logger: p.logger}
	in, err := rp.inputs(ctx, releaseID)
	if err != nil {
		return nil, err
	}

	// The window ends where the next sync starts, and adoption walks every
	// sync after a release's bump was merged, for this release and for any
	// release it was compared with. AppSyncs returns no events, so the syncs
	// from the earliest event read until the window's end are added here.
	// The Deployment snapshot from before the window can be much older and
	// does not move the start.
	if rt := in.runtime; rt != nil && in.svc.ArgoCDApplication != "" {
		from := rt.Start
		for id, e := range rec.seen {
			if !rec.before[id] && e.OccurredAt.Before(from) {
				from = e.OccurredAt
			}
		}
		syncs, err := p.store.AppSyncs(ctx, in.svc.ArgoCDApplication)
		if err != nil {
			return nil, err
		}
		for _, s := range syncs {
			if s.Start.Before(from.Add(-adoptionGrace)) || (rt.End != nil && s.Start.After(*rt.End)) {
				continue
			}
			if _, err := rec.AppSyncEvents(ctx, in.svc.ArgoCDApplication, s.Revision); err != nil {
				return nil, fmt.Errorf("syncs of %s: %w", releaseID, err)
			}
		}
	}

	out := make([]events.Event, 0, len(rec.seen))
	for _, e := range rec.seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// recordingStore remembers every event the projector reads through it.
type recordingStore struct {
	ProjectionStore
	seen map[int64]events.Event
	// before marks the Deployment snapshots from before a window.
	before map[int64]bool
}

func (r *recordingStore) keep(evs []events.Event, err error) ([]events.Event, error) {
	for _, e := range evs {
		r.seen[e.ID] = e
	}
	return evs, err
}

func (r *recordingStore) ReleaseEvents(ctx context.Context, releaseID string) ([]events.Event, error) {
	return r.keep(r.ProjectionStore.ReleaseEvents(ctx, releaseID))
}

func (r *recordingStore) WorkloadEvents(ctx context.Context, workload string, from time.Time, to *time.Time) ([]events.Event, error) {
	return r.keep(r.ProjectionStore.WorkloadEvents(ctx, workload, from, to))
}

func (r *recordingStore) AppSyncEvents(ctx context.Context, application, revision string) ([]events.Event, error) {
	return r.keep(r.ProjectionStore.AppSyncEvents(ctx, application, revision))
}

func (r *recordingStore) VerifierEvents(ctx context.Context, releaseID string) ([]events.Event, error) {
	return r.keep(r.ProjectionStore.VerifierEvents(ctx, releaseID))
}

func (r *recordingStore) LastDeploymentBefore(ctx context.Context, workload string, t time.Time) (*events.Event, error) {
	e, err := r.ProjectionStore.LastDeploymentBefore(ctx, workload, t)
	if e != nil {
		r.seen[e.ID] = *e
		r.before[e.ID] = true
	}
	return e, err
}
