package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/fixtures"
	"github.com/spicyhek/mybuilds/internal/ingest"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
)

// TestReleaseEventsRecordAndReplay records a release through
// GET /api/v1/releases/{id}/events, writes it as a fixture file, loads it
// into an empty store and checks the release projects the same (plan
// section 7.5).
func TestReleaseEventsRecordAndReplay(t *testing.T) {
	const id = fixtures.RecordedRelease
	_, store := newTestServer(t)
	svcs, err := services.Load("")
	if err != nil {
		t.Fatal(err)
	}
	projector := releases.NewProjector(store, svcs, nil)
	secrets := ingest.Secrets{GitHubWebhook: "g", CIEvents: "c", ArgoCDToken: "a"}
	in := ingest.New(ingest.Options{Store: store, Projector: projector, Services: svcs, Secrets: secrets})
	ds, err := fixtures.Deliveries("personal-site-11")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if _, err := in.Ingest(t.Context(), d.Provider, fixtures.Sign(d, secrets), d.Body); err != nil {
			t.Fatal(err)
		}
	}
	h := New(Options{Store: store, Metrics: NewMetrics(prometheus.NewRegistry(), "test"), Projector: projector, Services: svcs})

	rec := get(t, h, "/api/v1/releases/"+id+"/events")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "body_sha256") {
		t.Error("the body hash is exported")
	}
	// The root Application's sync of the same commit is read by the
	// projection, which must keep ignoring it, so the fixture holds it.
	if !strings.Contains(rec.Body.String(), `"application":"homelab-apps"`) {
		t.Error("the root Application's sync is missing")
	}
	var body struct {
		ReleaseID string          `json:"release_id"`
		Events    []events.Record `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ReleaseID != id || len(body.Events) < 10 {
		t.Fatalf("release %s with %d events", body.ReleaseID, len(body.Events))
	}
	for i := 1; i < len(body.Events); i++ {
		if body.Events[i].ID <= body.Events[i-1].ID {
			t.Fatalf("events are not in ID order at %d", i)
		}
	}

	// Through the fixture file format and back.
	var file bytes.Buffer
	if err := events.WriteRecording(&file, events.Recording{ReleaseID: id, Events: body.Events}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(file.String(), "\n"); n != len(body.Events)+1 {
		t.Errorf("fixture has %d lines, want a header and one per event", n)
	}
	read, err := events.ReadRecording(&file)
	if err != nil {
		t.Fatal(err)
	}

	fresh, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fresh.Close() })
	if _, err := fresh.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, r := range read.Events {
		e := r.Event()
		e.ID = 0
		if _, err := fresh.InsertEvent(t.Context(), &e); err != nil {
			t.Fatal(err)
		}
	}
	fp := releases.NewProjector(fresh, svcs, nil)
	if _, err := fp.ProjectRelease(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	fh := New(Options{Store: fresh, Metrics: NewMetrics(prometheus.NewRegistry(), "test"), Projector: fp, Services: svcs})
	for _, path := range []string{"/api/v1/releases/" + id, "/api/v1/releases/" + id + "/timeline"} {
		if got, want := get(t, fh, path).Body.String(), get(t, h, path).Body.String(); got != want {
			t.Errorf("%s from the recording differs:\n got %s\nwant %s", path, got, want)
		}
	}
}

func TestReleaseEventsOfUnknownRelease(t *testing.T) {
	_, store := newTestServer(t)
	svcs, _ := services.Load("")
	h := New(Options{Store: store, Metrics: NewMetrics(prometheus.NewRegistry(), "test"), Projector: releases.NewProjector(store, svcs, nil)})
	if rec := get(t, h, "/api/v1/releases/personal-site/0000000/production/1/events"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	// Without a projector there is nothing to export.
	h = New(Options{Store: store, Metrics: NewMetrics(prometheus.NewRegistry(), "test")})
	if rec := get(t, h, "/api/v1/releases/drill-target/89abcde/production/4/events"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d without a projector, want 404", rec.Code)
	}
}
