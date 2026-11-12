package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
)

// fakeAPIServer serves Deployments, ReplicaSets and Pods for the default
// namespace. With streaming, it answers informers' watch-list requests
// (sendInitialEvents=true) the way a current API server does; without it, it
// rejects them, as an older server would, and serves plain LIST requests.
type fakeAPIServer struct {
	t         *testing.T
	streaming bool
	objects   map[string][]runtime.Object // by resource
	// watched receives each resource once its watch is open; a ReplicaSet is
	// then added over the replicasets watch.
	watched chan string
	added   runtime.Object

	mu        sync.Mutex
	selectors []string
}

func (f *fakeAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.selectors = append(f.selectors, q.Get("labelSelector"))
	f.mu.Unlock()

	parts := strings.Split(r.URL.Path, "/")
	resource := parts[len(parts)-1]
	items, ok := f.objects[resource]
	if !ok || !strings.Contains(r.URL.Path, "/namespaces/default/") {
		f.t.Errorf("unexpected request %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)

	if q.Get("watch") != "true" {
		enc.Encode(map[string]any{"kind": kindOf(resource) + "List", "apiVersion": apiVersionOf(resource),
			"metadata": map[string]string{"resourceVersion": "7"}, "items": items})
		return
	}
	if q.Get("sendInitialEvents") == "true" {
		if !f.streaming {
			w.WriteHeader(http.StatusUnprocessableEntity)
			enc.Encode(metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
				Status: metav1.StatusFailure, Code: http.StatusUnprocessableEntity, Reason: metav1.StatusReasonInvalid,
				Message: "sendInitialEvents is not supported"})
			return
		}
		for _, obj := range items {
			enc.Encode(map[string]any{"type": "ADDED", "object": obj})
		}
		enc.Encode(map[string]any{"type": "BOOKMARK", "object": map[string]any{
			"kind": kindOf(resource), "apiVersion": apiVersionOf(resource),
			"metadata": map[string]any{"resourceVersion": "7",
				"annotations": map[string]string{metav1.InitialEventsAnnotationKey: "true"}},
		}})
	}
	w.(http.Flusher).Flush()
	f.watched <- resource
	if resource == "replicasets" && f.added != nil {
		enc.Encode(map[string]any{"type": "ADDED", "object": f.added})
		w.(http.Flusher).Flush()
	}
	<-r.Context().Done()
}

func kindOf(resource string) string {
	return map[string]string{"deployments": "Deployment", "replicasets": "ReplicaSet", "pods": "Pod"}[resource]
}

func apiVersionOf(resource string) string {
	if resource == "pods" {
		return "v1"
	}
	return "apps/v1"
}

// TestWatcherThroughTheAPI runs the real informers against a fake API
// server: request paths, the label selector, decoding, and changes that
// arrive over the watch.
func TestWatcherThroughTheAPI(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		name := "list fallback"
		if streaming {
			name = "watch-list"
		}
		t.Run(name, func(t *testing.T) {
			dep := deployment("12", 58, 58, 2, 2, 2, "NewReplicaSetAvailable", 30)
			dep.TypeMeta = metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"}
			p := pod("576fd9b569", "a", 10, 20, digestOf('a'))
			p.TypeMeta = metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"}
			rs := replicaSet("576fd9b569", "12", 9)
			rs.TypeMeta = metav1.TypeMeta{Kind: "ReplicaSet", APIVersion: "apps/v1"}
			rs.ResourceVersion = "8"

			fake := &fakeAPIServer{t: t, streaming: streaming, watched: make(chan string, 8), added: rs,
				objects: map[string][]runtime.Object{
					"deployments": {dep}, "replicasets": {}, "pods": {p},
				}}
			srv := httptest.NewServer(fake)
			defer srv.Close()

			h := newHarness(t)
			w, err := New(Options{Config: &rest.Config{Host: srv.URL}, Services: h.w.opts.Services, Store: h.store,
				Projector: h.projector, Logger: discardLogger(), Now: func() time.Time { return at(0) }})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { w.Run(ctx); close(done) }()
			defer func() { cancel(); srv.CloseClientConnections(); <-done }()

			deadline := time.After(10 * time.Second)
			for seen := 0; seen < 3; seen++ {
				select {
				case <-fake.watched:
				case <-deadline:
					t.Fatal("informers did not start watching")
				}
			}
			for {
				total, _, err := h.store.CountEvents(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if total == 3 {
					break
				}
				select {
				case <-deadline:
					t.Fatalf("stored %d events, want 3 (deployment, pod, and the replica set from the watch)", total)
				case <-time.After(20 * time.Millisecond):
				}
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for _, s := range fake.selectors {
				if s != ManagedLabel+"=true" {
					t.Errorf("request without the managed selector: %q", s)
				}
			}
		})
	}
}
