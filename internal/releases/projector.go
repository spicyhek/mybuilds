package releases

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
	// SaveProjection replaces a release's projected state.
	SaveProjection(ctx context.Context, p Projection) error
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

// ProjectEvent re-projects every release a newly stored event can change and
// returns their IDs.
func (p *Projector) ProjectEvent(ctx context.Context, e events.Event) ([]string, error) {
	ids, err := p.store.AffectedReleaseIDs(ctx, e)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := p.ProjectRelease(ctx, id); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

// ProjectRelease rebuilds one release from all of its events.
func (p *Projector) ProjectRelease(ctx context.Context, releaseID string) error {
	evs, err := p.store.ReleaseEvents(ctx, releaseID)
	if err != nil {
		return fmt.Errorf("load events of %s: %w", releaseID, err)
	}
	parts, err := ParseReleaseID(releaseID)
	if err != nil {
		return err
	}
	// An unknown service still projects; its jobs just map to no stage.
	svc, _ := p.services.ByName(parts.Service)
	proj, err := Project(releaseID, evs, svc)
	if err != nil {
		return fmt.Errorf("project %s: %w", releaseID, err)
	}
	if err := p.store.SaveProjection(ctx, proj); err != nil {
		return fmt.Errorf("save %s: %w", releaseID, err)
	}
	return nil
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
		if err := p.ProjectRelease(ctx, id); err != nil {
			errs = append(errs, err)
			continue
		}
		done++
	}
	return done, errors.Join(errs...)
}
