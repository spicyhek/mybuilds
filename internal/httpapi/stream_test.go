package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spicyhek/mybuilds/internal/fixtures"
	"github.com/spicyhek/mybuilds/internal/ingest"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
)

type sseFrame struct {
	ID    string
	Event string
	Data  string
}

// readFrames parses SSE frames from a live response until n frames arrive
// or the deadline passes.
func readFrames(t *testing.T, r *bufio.Reader, n int, timeout time.Duration) []sseFrame {
	t.Helper()
	var out []sseFrame
	done := make(chan struct{})
	go func() {
		defer close(done)
		var cur sseFrame
		for len(out) < n {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if cur.Event != "" {
					out = append(out, cur)
				}
				cur = sseFrame{}
			case strings.HasPrefix(line, "id: "):
				cur.ID = line[4:]
			case strings.HasPrefix(line, "event: "):
				cur.Event = line[7:]
			case strings.HasPrefix(line, "data: "):
				cur.Data = line[6:]
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("timed out after %d of %d frames: %+v", len(out), n, out)
	}
	return out
}

type streamEnv struct {
	srv     *httptest.Server
	hub     *Hub
	in      *ingest.Ingester
	secrets ingest.Secrets
}

func newStreamServer(t *testing.T) streamEnv {
	t.Helper()
	_, store := newTestServer(t)
	svcs, err := services.Load("")
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub(store, nil)
	secrets := ingest.Secrets{GitHubWebhook: "g", CIEvents: "c", ArgoCDToken: "a"}
	in := ingest.New(ingest.Options{
		Store:     store,
		Projector: releases.NewProjector(store, svcs, nil),
		Services:  svcs,
		Secrets:   secrets,
		Notify:    hub.Notify,
	})
	h := New(Options{Store: store, Metrics: NewMetrics(prometheus.NewRegistry(), "test"), Hub: hub, Services: svcs})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return streamEnv{srv: srv, hub: hub, in: in, secrets: secrets}
}

// ingestAll sends recorded deliveries straight through the ingester.
func (e streamEnv) ingestAll(t *testing.T, ds []fixtures.Delivery) {
	t.Helper()
	for _, d := range ds {
		if _, err := e.in.Ingest(context.Background(), d.Provider, fixtures.Sign(d, e.secrets), d.Body); err != nil {
			t.Fatal(err)
		}
	}
}

// open connects to the stream and returns a frame reader.
func (e streamEnv) open(t *testing.T, lastID string) *bufio.Reader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/api/v1/stream", nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	return bufio.NewReader(res.Body)
}

func TestStreamDeliversProjectedReleases(t *testing.T) {
	e := newStreamServer(t)
	ds, err := fixtures.Deliveries("personal-site-11")
	if err != nil {
		t.Fatal(err)
	}

	r := e.open(t, "")
	hello := readFrames(t, r, 1, 5*time.Second)[0]
	if hello.Event != msgHello || !strings.Contains(hello.Data, `"last_event_id":0`) {
		t.Fatalf("first frame = %+v", hello)
	}
	// The hello frame is written after subscribing, so the client is
	// registered by now.
	if n := e.hub.Subscribers(); n != 1 {
		t.Fatalf("subscribers = %d", n)
	}

	e.ingestAll(t, ds[:2])
	frames := readFrames(t, r, 2, 5*time.Second)
	if frames[0].Event != msgReleaseCreated || frames[0].ID != "1" {
		t.Errorf("first release frame = %+v, want release.created id 1", frames[0])
	}
	if frames[1].Event != msgReleaseUpdated || frames[1].ID != "2" {
		t.Errorf("second release frame = %+v, want release.updated id 2", frames[1])
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(frames[1].Data), &view); err != nil {
		t.Fatal(err)
	}
	if view["release_id"] != fixtures.RecordedRelease || view["status"] != "running" {
		t.Errorf("frame data = %v", view)
	}
	if _, leaked := view["uuid"]; leaked {
		t.Error("uuid leaked into the stream")
	}
}

func TestStreamReplaysAfterLastEventID(t *testing.T) {
	e := newStreamServer(t)
	ds, err := fixtures.Deliveries("personal-site-11")
	if err != nil {
		t.Fatal(err)
	}
	e.ingestAll(t, ds)

	connect := func(lastID string) []sseFrame {
		t.Helper()
		n := 1
		if lastID != "" {
			n = 2
		}
		return readFrames(t, e.open(t, lastID), n, 5*time.Second)
	}

	// A client that saw everything gets only hello.
	fresh := connect("")
	if fresh[0].Event != msgHello || !strings.Contains(fresh[0].Data, `"last_event_id":`+strconv.Itoa(len(ds))) {
		t.Fatalf("hello = %+v", fresh[0])
	}

	// A client that missed the last three events gets the release once,
	// stamped with the newest event it covers.
	frames := connect(strconv.Itoa(len(ds) - 3))
	if frames[1].Event != msgReleaseUpdated || frames[1].ID != strconv.Itoa(len(ds)) {
		t.Fatalf("replay frame = %+v", frames[1])
	}
	if !strings.Contains(frames[1].Data, `"release_id":"`+fixtures.RecordedRelease+`"`) {
		t.Errorf("replay data = %s", frames[1].Data)
	}
}

func TestStreamSendsSnapshotWhenCursorIsTooOld(t *testing.T) {
	// Replay is bounded; this checks the limit path with the limit lowered.
	e := newStreamServer(t)
	ds, _ := fixtures.Deliveries("personal-site-11")
	e.ingestAll(t, ds)
	e.hub.ReplayLimit = 2

	frames := readFrames(t, e.open(t, "0"), 2, 5*time.Second)
	if frames[1].Event != msgSnapshot {
		t.Fatalf("frame = %+v, want snapshot", frames[1])
	}
}

func TestMeta(t *testing.T) {
	h, _ := newTestServer(t)
	rec := get(t, h, "/api/v1/meta")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "test" || body["release_id"] != testReleaseID {
		t.Errorf("meta = %v", body)
	}
	if _, ok := body["services"].([]any); !ok {
		t.Errorf("services missing: %v", body)
	}
}
