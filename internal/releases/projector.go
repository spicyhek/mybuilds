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
	// AppSyncEvents returns an Application's notifications for a revision.
	AppSyncEvents(ctx context.Context, application, revision string) ([]events.Event, error)
	// VerifierEvents returns the verifier's events for a release.
	VerifierEvents(ctx context.Context, releaseID string) ([]events.Event, error)
	// LastDeploymentBefore returns a workload's latest Deployment snapshot
	// before t, or nil.
	LastDeploymentBefore(ctx context.Context, workload string, t time.Time) (*events.Event, error)
	// UnsyncedReleases returns a service's application releases created
	// since t whose reported GitOps commit was never synced.
	UnsyncedReleases(ctx context.Context, service string, since time.Time) ([]string, error)
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

// ProjectEvent re-projects every release a newly stored event can change,
// and the releases deployed together with them.
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
	for i := 0; i < len(ids); i++ {
		created, partner, err := p.projectRelease(ctx, ids[i])
		if err != nil {
			return out, err
		}
		out = append(out, Outcome{ReleaseID: ids[i], Created: created})
		if partner != "" && !slices.Contains(ids, partner) {
			ids = append(ids, partner)
		}
	}
	return out, nil
}

// ProjectRelease rebuilds one release from all of its events.
func (p *Projector) ProjectRelease(ctx context.Context, releaseID string) (created bool, err error) {
	created, _, err = p.projectRelease(ctx, releaseID)
	return created, err
}

func (p *Projector) projectRelease(ctx context.Context, releaseID string) (created bool, partner string, err error) {
	evs, err := p.store.ReleaseEvents(ctx, releaseID)
	if err != nil {
		return false, "", fmt.Errorf("load events of %s: %w", releaseID, err)
	}
	parts, err := ParseReleaseID(releaseID)
	if err != nil {
		return false, "", err
	}
	// An unknown service still projects; its jobs just map to no stage.
	svc, _ := p.services.ByName(parts.Service)
	proj, err := Project(releaseID, evs, svc)
	if err != nil {
		return false, "", fmt.Errorf("project %s: %w", releaseID, err)
	}
	// The runtime window depends on the GitOps commit, which only the first
	// pass knows.
	rt, extra, err := p.runtime(ctx, svc, proj.Release)
	if err != nil {
		return false, "", fmt.Errorf("runtime of %s: %w", releaseID, err)
	}
	if rt != nil {
		if proj, err = ProjectWithRuntime(releaseID, append(evs, extra...), svc, rt); err != nil {
			return false, "", fmt.Errorf("project %s: %w", releaseID, err)
		}
	}
	created, err = p.store.SaveProjection(ctx, proj)
	if err != nil {
		return false, "", fmt.Errorf("save %s: %w", releaseID, err)
	}
	return created, proj.Release.DeployedWith, nil
}

// adoptionGrace allows for Argo CD starting a sync of a commit that contains
// a digest bump just before the bump job's gitops.updated arrives. The job
// sends it right after the merge, so the margin is short; a longer one would
// let a sync from before the bump adopt it.
const adoptionGrace = 30 * time.Second

// unsyncedLookback bounds how far back runtime and sync events look for
// releases whose GitOps commit was never synced.
const unsyncedLookback = 24 * time.Hour

// runtime loads a release's runtime window: its workload's events from the
// sync of its GitOps commit until the next sync, plus any events it shares
// with a release deployed together with it. It is nil when the service has
// no workload or the release has no sync.
func (p *Projector) runtime(ctx context.Context, svc services.Service, r Release) (*Runtime, []events.Event, error) {
	workload := svc.WorkloadKey()
	if workload == "" || svc.ArgoCDApplication == "" || r.GitOpsSHA == "" || r.Superseded {
		return nil, nil, nil
	}
	syncs, err := p.store.AppSyncs(ctx, svc.ArgoCDApplication)
	if err != nil {
		return nil, nil, err
	}
	rt := &Runtime{Revision: r.GitOpsSHA}
	var extra []events.Event
	start, end, ok := RuntimeWindow(syncs, r.GitOpsSHA)
	switch {
	case ok:
	case r.Kind == KindApplication:
		// Rule 2 (plan section 8.7): the bump's own commit was never
		// synced, because Argo CD applied a newer commit that contains it.
		rev, found, err := p.adoption(ctx, svc, r, syncs)
		if err != nil || !found {
			return nil, nil, err
		}
		start, end, _ = RuntimeWindow(syncs, rev)
		rt.Revision = rev
		if extra, err = p.store.AppSyncEvents(ctx, svc.ArgoCDApplication, rev); err != nil {
			return nil, nil, err
		}
		ids, err := p.store.ReleasesWithRevision(ctx, rev)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range ids {
			if parts, err := ParseReleaseID(id); err == nil && parts.Kind == KindConfig {
				rt.Partner = id
			}
		}
	default:
		return nil, nil, nil
	}
	rt.Start, rt.End = start, end
	if rt.Events, err = p.store.WorkloadEvents(ctx, workload, start, end); err != nil {
		return nil, nil, err
	}
	prev, err := p.store.LastDeploymentBefore(ctx, workload, start)
	if err != nil {
		return nil, nil, err
	}
	if prev != nil {
		var d events.Deployment
		if err := prev.Decode(&d); err == nil {
			rt.PrevReleaseID, rt.PrevDigest = d.ReleaseID, templateDigest(d.Images, svc.Artifact)
		}
	}

	// A config release whose new pods carry an application release still
	// waiting at Reconcile was deployed together with it, and shares its
	// verification.
	if r.Kind == KindConfig {
		if served := newReplicaSetRelease(rt.Events, start); served != "" {
			x, err := p.firstPass(ctx, served, svc)
			if err != nil {
				return nil, nil, err
			}
			if x != nil {
				rev, found, err := p.adoption(ctx, svc, *x, syncs)
				if err != nil {
					return nil, nil, err
				}
				if found && rev == r.GitOpsSHA {
					rt.Partner = served
					checks, err := p.store.VerifierEvents(ctx, served)
					if err != nil {
						return nil, nil, err
					}
					extra = append(extra, checks...)
				}
			}
		}
	}
	return rt, extra, nil
}

// firstPass projects an application release of svc from its own events,
// without runtime evidence. It is nil when the release has no events.
func (p *Projector) firstPass(ctx context.Context, releaseID string, svc services.Service) (*Release, error) {
	parts, err := ParseReleaseID(releaseID)
	if err != nil || parts.Kind != KindApplication || parts.Service != svc.Name {
		return nil, nil
	}
	evs, err := p.store.ReleaseEvents(ctx, releaseID)
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	proj, err := Project(releaseID, evs, svc)
	if err != nil {
		return nil, err
	}
	return &proj.Release, nil
}

// adoption finds the sync that applied an application release whose own
// GitOps commit was never synced: the first sync after its gitops.updated
// that started new pods, provided those pods carry its release ID. A later
// rollout that still carries the ID, such as a config change, is not it.
func (p *Projector) adoption(ctx context.Context, svc services.Service, x Release, syncs []Sync) (string, bool, error) {
	if x.Kind != KindApplication || x.GitOpsSHA == "" || x.Superseded {
		return "", false, nil
	}
	for _, s := range syncs {
		if s.Revision == x.GitOpsSHA {
			return "", false, nil
		}
	}
	var gitopsAt *time.Time
	for _, st := range x.Stages {
		if st.Key == "gitops" && st.State == StateSucceeded {
			gitopsAt = st.CompletedAt
		}
	}
	if gitopsAt == nil {
		return "", false, nil
	}
	for i, s := range syncs {
		if s.Start.Before(gitopsAt.Add(-adoptionGrace)) {
			continue
		}
		var end *time.Time
		if i+1 < len(syncs) {
			t := syncs[i+1].Start
			end = &t
		}
		evs, err := p.store.WorkloadEvents(ctx, svc.WorkloadKey(), s.Start, end)
		if err != nil {
			return "", false, err
		}
		switch served := newReplicaSetRelease(evs, s.Start); served {
		case "":
			// This sync replaced no pods; the bump may be in a later one.
		case x.ReleaseID:
			return s.Revision, true, nil
		default:
			return "", false, nil
		}
	}
	return "", false, nil
}

// newReplicaSetRelease is the release-id annotation of the newest
// ReplicaSet created in a window that starts at start.
func newReplicaSetRelease(evs []events.Event, start time.Time) string {
	var newest *events.ReplicaSet
	for _, e := range evs {
		if e.Type != events.TypeReplicaSet {
			continue
		}
		var rs events.ReplicaSet
		if err := e.Decode(&rs); err != nil || rs.Deleted || rs.CreatedAt.Before(start.Add(-createdSlack)) {
			continue
		}
		if newest == nil || rs.CreatedAt.After(newest.CreatedAt) {
			newest = &rs
		}
	}
	if newest == nil {
		return ""
	}
	return newest.ReleaseID
}

// windowReleases returns the releases whose runtime window an event changes:
// for a runtime event, the release whose sync window contains it; for an Argo
// CD sync, the release of the previous sync, whose window it closes. Both
// can also change a release whose own GitOps commit was never synced, which
// may have been applied as part of a newer one (rule 2).
func (p *Projector) windowReleases(ctx context.Context, e events.Event) ([]string, error) {
	var svc services.Service
	var ok bool
	switch e.Provider {
	case events.ProviderKubernetes:
		svc, ok = p.services.ByWorkload(e.Keys.Workload)
	case events.ProviderArgoCD:
		var n events.ArgoCDNotification
		if err := e.Decode(&n); err != nil {
			return nil, err
		}
		if n.Trigger == events.TriggerHealthDegraded {
			return nil, nil
		}
		svc, ok = p.services.ByApplication(n.Application)
	}
	if !ok || svc.ArgoCDApplication == "" {
		return nil, nil
	}
	syncs, err := p.store.AppSyncs(ctx, svc.ArgoCDApplication)
	if err != nil {
		return nil, err
	}
	var out []string
	var s Sync
	if e.Provider == events.ProviderKubernetes {
		s, ok = SyncAt(syncs, e.OccurredAt)
	} else {
		s, ok = PreviousSync(syncs, e.Keys.Revision)
	}
	if ok {
		if out, err = p.store.ReleasesWithRevision(ctx, s.Revision); err != nil {
			return nil, err
		}
	}
	unsynced, err := p.store.UnsyncedReleases(ctx, svc.Name, e.OccurredAt.Add(-unsyncedLookback))
	if err != nil {
		return nil, err
	}
	for _, id := range unsynced {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
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
