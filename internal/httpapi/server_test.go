package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/spicyhek/mybuilds/internal/fixtures"
	"github.com/spicyhek/mybuilds/internal/ingest"
	"github.com/spicyhek/mybuilds/internal/services"
	"github.com/spicyhek/mybuilds/internal/storage"
)

const testReleaseID = "mybuilds/0123456/production/7"

func newTestServer(t *testing.T) (http.Handler, *storage.Store) {
	t.Helper()
	ctx := context.Background()

	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtures.Load(ctx, store); err != nil {
		t.Fatal(err)
	}

	dist := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>mybuilds</title><div id=root></div>")},
		"assets/app.js": {Data: []byte("console.log('app')")},
	}

	h := New(Options{
		Store:     store,
		ReleaseID: testReleaseID,
		Version:   "test",
		Metrics:   NewMetrics(prometheus.NewRegistry(), "test"),
		Web:       dist,
	})
	return h, store
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	h, _ := newTestServer(t)
	rec := get(t, h, "/healthz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("X-Release-ID"); got != testReleaseID {
		t.Errorf("X-Release-ID = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("missing X-Request-ID")
	}
}

func TestReadyz(t *testing.T) {
	h, store := newTestServer(t)

	if rec := get(t, h, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}

	store.Close()
	if rec := get(t, h, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status after close = %d, want 503", rec.Code)
	}
}

func TestListReleases(t *testing.T) {
	h, _ := newTestServer(t)
	rec := get(t, h, "/api/v1/releases")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}

	var body struct {
		Releases   []map[string]any `json:"releases"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want, _ := fixtures.Releases()
	if len(body.Releases) != len(want) {
		t.Fatalf("got %d releases, want %d", len(body.Releases), len(want))
	}
	if body.NextCursor != nil {
		t.Errorf("next_cursor = %v, want null", *body.NextCursor)
	}

	first := body.Releases[0]
	if first["release_id"] != "drill-target/89abcde/production/4" {
		t.Errorf("newest first: got %v", first["release_id"])
	}
	if first["drill"] != true {
		t.Errorf("drill flag missing: %v", first)
	}
	if _, leaked := first["uuid"]; leaked {
		t.Error("internal uuid leaked into the public view")
	}
	if stages, ok := first["stages"].([]any); !ok || len(stages) != 7 {
		t.Errorf("stages = %v", first["stages"])
	}
}

func TestListReleasesRejectsBadLimit(t *testing.T) {
	h, _ := newTestServer(t)
	for _, limit := range []string{"0", "201", "abc"} {
		if rec := get(t, h, "/api/v1/releases?limit="+limit); rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%s: status = %d, want 400", limit, rec.Code)
		}
	}
	if rec := get(t, h, "/api/v1/releases?limit=1"); rec.Code != http.StatusOK {
		t.Errorf("limit=1: status = %d", rec.Code)
	}
}

func TestListReleasesRejectsBadFilters(t *testing.T) {
	h, _ := newTestServer(t)
	for _, q := range []string{"kind=other", "outcome=green", "drill=maybe", "cursor=!!", "cursor=bm90LWEtY3Vyc29y"} {
		if rec := get(t, h, "/api/v1/releases?"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

func TestListReleasesPaginatesAndFilters(t *testing.T) {
	h, _ := newTestServer(t)
	type page struct {
		Releases []struct {
			ReleaseID string `json:"release_id"`
		} `json:"releases"`
		NextCursor *string `json:"next_cursor"`
	}
	list := func(q string) page {
		t.Helper()
		rec := get(t, h, "/api/v1/releases?"+q)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %s", q, rec.Code, rec.Body.String())
		}
		var p page
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	first := list("limit=1")
	if len(first.Releases) != 1 || first.NextCursor == nil {
		t.Fatalf("first page = %+v, want one release and a cursor", first)
	}
	second := list("limit=1&cursor=" + url.QueryEscape(*first.NextCursor))
	if len(second.Releases) != 1 || second.Releases[0].ReleaseID == first.Releases[0].ReleaseID {
		t.Fatalf("second page = %+v after %+v", second, first)
	}

	drills := list("drill=true")
	for _, r := range drills.Releases {
		if !strings.HasPrefix(r.ReleaseID, "drill-target/") {
			t.Errorf("drill=true returned %s", r.ReleaseID)
		}
	}
	if len(drills.Releases) == 0 {
		t.Error("drill=true returned nothing")
	}
	if cfg := list("kind=config&drill=false"); len(cfg.Releases) != 1 || cfg.Releases[0].ReleaseID != "personal-site/config/5bb4095/production" {
		t.Errorf("kind=config = %+v", cfg)
	}
}

func TestGetReleaseWithSlashesInID(t *testing.T) {
	h, _ := newTestServer(t)

	rec := get(t, h, "/api/v1/releases/personal-site/config/5bb4095/production")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["release_id"] != "personal-site/config/5bb4095/production" || got["kind"] != "config" {
		t.Errorf("got %v", got)
	}

	rec = get(t, h, "/api/v1/releases/personal-site/0000000/production/1")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing release: status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("404 body is not JSON: %s", rec.Body.String())
	}

	if rec := get(t, h, "/api/v1/nope"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("unknown API path: status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestStaticAndSPAFallback(t *testing.T) {
	h, _ := newTestServer(t)

	for _, path := range []string{"/", "/releases/personal-site/ce61154/production/11"} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q", path, ct)
		}
		if !strings.Contains(rec.Body.String(), "<title>mybuilds</title>") {
			t.Errorf("%s: did not serve index.html", path)
		}
	}
}

func TestIngestionRoutes(t *testing.T) {
	_, store := newTestServer(t)
	svcs, err := services.Load("")
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{
		Store:   store,
		Metrics: NewMetrics(prometheus.NewRegistry(), "test"),
		Ingest: ingest.New(ingest.Options{
			Store:    store,
			Services: svcs,
			Secrets:  ingest.Secrets{GitHubWebhook: "s", CIEvents: "s", ArgoCDToken: "s"},
		}),
	})

	for _, path := range []string{"/hooks/github", "/hooks/argocd", "/api/v1/ci-events"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s unsigned: status %d, want 401", path, rec.Code)
		}
	}
	for _, path := range []string{"/hooks/github", "/hooks/nope"} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("GET %s: status %d body %s, want a JSON 404", path, rec.Code, rec.Body.String())
		}
	}
}
