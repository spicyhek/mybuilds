package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spicyhek/mybuilds/internal/fixtures"
	"github.com/spicyhek/mybuilds/internal/ingest"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
)

func TestTimelineReplaysRecordedDeliveries(t *testing.T) {
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

	rec := get(t, h, "/api/v1/releases/personal-site/ce61154/production/11/timeline")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ReleaseID string `json:"release_id"`
		Frames    []struct {
			At        time.Time      `json:"at"`
			EventType string         `json:"event_type"`
			Release   map[string]any `json:"release"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ReleaseID != "personal-site/ce61154/production/11" || len(body.Frames) < 3 {
		t.Fatalf("timeline = %s", rec.Body.String())
	}
	for i, f := range body.Frames {
		if i > 0 && f.At.Before(body.Frames[i-1].At) {
			t.Errorf("frame %d goes back in time", i)
		}
		if _, leaked := f.Release["uuid"]; leaked {
			t.Error("internal uuid leaked into a frame")
		}
	}

	// The last frame is the release as the API serves it now.
	var now map[string]any
	if err := json.Unmarshal(get(t, h, "/api/v1/releases/personal-site/ce61154/production/11").Body.Bytes(), &now); err != nil {
		t.Fatal(err)
	}
	last, _ := json.Marshal(body.Frames[len(body.Frames)-1].Release)
	want, _ := json.Marshal(now)
	if string(last) != string(want) {
		t.Errorf("last frame differs from the release:\nframe   %s\nrelease %s", last, want)
	}
}

func TestTimelineOfUnknownRelease(t *testing.T) {
	_, store := newTestServer(t)
	svcs, _ := services.Load("")
	h := New(Options{Store: store, Metrics: NewMetrics(prometheus.NewRegistry(), "test"), Projector: releases.NewProjector(store, svcs, nil)})
	if rec := get(t, h, "/api/v1/releases/personal-site/0000000/production/1/timeline"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	// A release inserted without events replays as no frames, not an error.
	rec := get(t, h, "/api/v1/releases/drill-target/89abcde/production/4/timeline")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Frames []any `json:"frames"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Frames == nil || len(body.Frames) != 0 {
		t.Errorf("frames = %v, want []", body.Frames)
	}
}
