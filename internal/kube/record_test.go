package kube

import (
	"os"
	"path/filepath"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
)

// everyReleaseSelfContained runs at the end of every harness test: each
// release's exported events must be all its projection needs (plan section
// 7.5). Loaded into an empty store, they project the same release and
// replay the same timeline, so a recorded fixture tests what was live.
func (h *harness) everyReleaseSelfContained() {
	if h.t.Failed() {
		return
	}
	list, err := h.store.ListReleases(h.ctx, 200)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, r := range list {
		h.selfContained(r.ReleaseID)
	}
}

func (h *harness) selfContained(id string) {
	t := h.t
	evs, err := h.projector.RecordedEvents(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// t.TempDir cannot be called from a cleanup.
	dir, err := os.MkdirTemp("", "mybuilds-fresh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	fresh, err := storage.Open(h.ctx, filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.Migrate(h.ctx); err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		rec, err := events.NewRecord(e)
		if err != nil {
			t.Fatal(err)
		}
		ev := rec.Event()
		ev.ID = 0
		if _, err := fresh.InsertEvent(h.ctx, &ev); err != nil {
			t.Fatal(err)
		}
	}
	svcs, err := services.Load("")
	if err != nil {
		t.Fatal(err)
	}
	p := releases.NewProjector(fresh, svcs, nil)
	if _, err := p.ProjectRelease(h.ctx, id); err != nil {
		t.Fatal(err)
	}
	got, err := fresh.GetRelease(h.ctx, id)
	if err != nil {
		t.Fatalf("%s from its %d exported events: %v", id, len(evs), err)
	}
	if g, w := comparable(t, *got), comparable(t, *h.get(id)); g != w {
		t.Errorf("%s from its %d exported events:\n got %s\nwant %s", id, len(evs), g, w)
	}
	frames, err := p.Timeline(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := h.timeline(id)
	if len(want) > 0 {
		if l, s := comparable(t, want[len(want)-1].Release), comparable(t, *h.get(id)); l != s {
			t.Errorf("%s: the timeline's last frame is not the stored release:\n frame  %s\nstored %s", id, l, s)
		}
	}
	if len(frames) != len(want) {
		t.Errorf("%s from its exported events: %d frames, want %d", id, len(frames), len(want))
		return
	}
	for i := range frames {
		if !frames[i].At.Equal(want[i].At) || frames[i].EventType != want[i].EventType ||
			comparable(t, frames[i].Release) != comparable(t, want[i].Release) {
			t.Errorf("%s from its exported events: frame %d (%s at %v) differs:\n got %s\nwant %s", id, i,
				want[i].EventType, want[i].At, comparable(t, frames[i].Release), comparable(t, want[i].Release))
			return
		}
	}
}
