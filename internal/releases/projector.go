package releases

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/services"
)

// ProjectionStore is what the projector needs from storage.
type ProjectionStore interface {
	// AffectedReleaseIDs returns the releases an event can change.
	AffectedReleaseIDs(ctx context.Context, e events.Event) ([]string, error)
	// ReleaseEvents returns every event that belongs to a release: the ones
	// that name it, the jobs of its workflow runs and the syncs of its
	// GitOps commits.
	ReleaseEvents(ctx context.Context, releaseID string) ([]events.Event, error)
	// ProjectedReleaseIDs returns every release ID named by an event.
	ProjectedReleaseIDs(ctx context.Context) ([]string, error)
	// SaveProjection replaces a release's projected state and reports
	// whether the release row was created by this call.
	SaveProjection(ctx context.Context, p Projection) (created bool, err error)
	// AppSyncs returns the revisions an Argo CD Application synced, oldest
	// first.
	AppSyncs(ctx context.Context, application string) ([]Sync, error)
	// WorkloadEvents returns a workload's runtime events in [from, to).
	WorkloadEvents(ctx context.Context, workload string, from time.Time, to *time.Time) ([]events.Event, error)
	// ReleasesWithRevision returns the releases that reported a GitOps
	// commit.
	ReleasesWithRevision(ctx context.Context, revision string) ([]string, error)
}

// Outcome is one release touched by a projection.
type Outcome struct {
	ReleaseID string
	// Created is true when the release did not exist before.
	Created bool
}

// Projector keeps the releases, release_stages and release_steps tables in
// step with the events table.
type Projector struct {
	store    ProjectionStore
	services services.Map
	logger   *slog.Logger
}

// NewProjector returns a projector over store.
func NewProjector(store ProjectionStore, svcs services.Map, logger *slog.Logger) *Projector {
	if logger == nil {
		logger = slog.Default()
	}
	return &Projector{store: store, services: svcs, logger: logger}
}

// ProjectEvent re-projects every release a newly stored event can change.
func (p *Projector) ProjectEvent(ctx context.Context, e events.Event) ([]Outcome, error) {
	ids, err := p.store.AffectedReleaseIDs(ctx, e)
	if err != nil {
		return nil, err
	}
	more, err := p.windowReleases(ctx, e)
	if err != nil {
		return nil, err
	}
	for _, id := range more {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	var out []Outcome
	for _, id := range ids {
		created, err := p.ProjectRelease(ctx, id)
		if err != nil {
			return out, err
		}
		out = append(out, Outcome{ReleaseID: id, Created: created})
	}
	return out, nil
}

// ProjectRelease rebuilds one release from all of its events.
func (p *Projector) ProjectRelease(ctx context.Context, releaseID string) (created bool, err error) {
	evs, err := p.store.ReleaseEvents(ctx, releaseID)
	if err != nil {
		return false, fmt.Errorf("load events of %s: %w", releaseID, err)
	}
	parts, err := ParseReleaseID(releaseID)
	if err != nil {
		return false, err
	}
	// An unknown service still projects; its jobs just map to no stage.
	svc, _ := p.services.ByName(parts.Service)
	proj, err := Project(releaseID, evs, svc)
	if err != nil {
		return false, fmt.Errorf("project %s: %w", releaseID, err)
	}
	// The runtime window depends on the GitOps commit, which only the first
	// pass knows.
	rt, err := p.runtime(ctx, svc, proj.Release)
	if err != nil {
		return false, fmt.Errorf("runtime of %s: %w", releaseID, err)
	}
	if rt != nil {
		if proj, err = ProjectWithRuntime(releaseID, evs, svc, rt); err != nil {
			return false, fmt.Errorf("project %s: %w", releaseID, err)
		}
	}
	created, err = p.store.SaveProjection(ctx, proj)
	if err != nil {
		return false, fmt.Errorf("save %s: %w", releaseID, err)
	}
	return created, nil
}

// runtime loads a release's runtime window: its workload's events from the
// sync of its GitOps commit until the next sync. It is nil when the service
// has no workload or the commit has not been synced.
func (p *Projector) runtime(ctx context.Context, svc services.Service, r Release) (*Runtime, error) {
	workload := svc.WorkloadKey()
	if workload == "" || svc.ArgoCDApplication == "" || r.GitOpsSHA == "" || r.Superseded {
		return nil, nil
	}
	syncs, err := p.store.AppSyncs(ctx, svc.ArgoCDApplication)
	if err != nil {
		return nil, err
	}
	start, end, ok := RuntimeWindow(syncs, r.GitOpsSHA)
	if !ok {
		return nil, nil
	}
	evs, err := p.store.WorkloadEvents(ctx, workload, start, end)
	if err != nil {
		return nil, err
	}
	return &Runtime{Start: start, End: end, Events: evs}, nil
}

// windowReleases returns the releases whose runtime window an event changes:
// for a runtime event, the release whose sync window contains it; for an Argo
// CD sync, the release of the previous sync, whose window it closes.
func (p *Projector) windowReleases(ctx context.Context, e events.Event) ([]string, error) {
	var app string
	switch e.Provider {
	case events.ProviderKubernetes:
		svc, ok := p.services.ByWorkload(e.Keys.Workload)
		if !ok {
			return nil, nil
		}
		app = svc.ArgoCDApplication
	case events.ProviderArgoCD:
		var n events.ArgoCDNotification
		if err := e.Decode(&n); err != nil {
			return nil, err
		}
		if n.Trigger == events.TriggerHealthDegraded {
			return nil, nil
		}
		app = n.Application
	default:
		return nil, nil
	}
	if app == "" {
		return nil, nil
	}
	syncs, err := p.store.AppSyncs(ctx, app)
	if err != nil {
		return nil, err
	}
	var s Sync
	var ok bool
	if e.Provider == events.ProviderKubernetes {
		s, ok = SyncAt(syncs, e.OccurredAt)
	} else {
		s, ok = PreviousSync(syncs, e.Keys.Revision)
	}
	if !ok {
		return nil, nil
	}
	return p.store.ReleasesWithRevision(ctx, s.Revision)
}

// RebuildAll re-projects every release from its events, so a projector
// change applies to history after a restart (plan section 5.4).
func (p *Projector) RebuildAll(ctx context.Context) (int, error) {
	ids, err := p.store.ProjectedReleaseIDs(ctx)
	if err != nil {
		return 0, err
	}
	// One bad release must not hide the others: keep going and report every
	// failure together.
	var errs []error
	done := 0
	for _, id := range ids {
		if _, err := p.ProjectRelease(ctx, id); err != nil {
			errs = append(errs, err)
			continue
		}
		done++
	}
	return done, errors.Join(errs...)
}
