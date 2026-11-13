package kube

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/releases"
	"github.com/spicyhek/mybuilds/internal/services"
)

// DefaultResync is the informers' resync interval. Resyncs replay the cache,
// not the API server, and unchanged objects store nothing; the long interval
// keeps reads off the k3s datastore on the contended control-plane disk.
const DefaultResync = 30 * time.Minute

// Store is what the watcher needs from storage.
type Store interface {
	InsertEvent(ctx context.Context, e *events.Event) (bool, error)
}

// Projector re-projects the releases an event touches.
type Projector interface {
	ProjectEvent(ctx context.Context, e events.Event) ([]releases.Outcome, error)
}

// Options configures a Watcher.
type Options struct {
	// Config reaches the API server, normally rest.InClusterConfig.
	Config    *rest.Config
	Services  services.Map
	Store     Store
	Projector Projector
	// Notify is called after an event is stored and projected.
	Notify     func(ctx context.Context, eventID int64, outcomes []releases.Outcome)
	Logger     *slog.Logger
	Registerer prometheus.Registerer
	// Resync defaults to DefaultResync.
	Resync time.Duration
	// Now is the clock for occurred_at; time.Now when nil.
	Now func() time.Time
}

// scheme holds only the two API groups the watch reads. The generated
// clientsets register every Kubernetes API group, which more than doubles
// the binary and its memory at startup.
var scheme = runtime.NewScheme()

func init() {
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			panic(err)
		}
	}
}

// restClient returns a client for one API group version.
func restClient(cfg *rest.Config, gv schema.GroupVersion, apiPath string) (*rest.RESTClient, error) {
	c := rest.CopyConfig(cfg)
	c.GroupVersion = &gv
	c.APIPath = apiPath
	c.NegotiatedSerializer = serializer.NewCodecFactory(scheme).WithoutConversion()
	if c.UserAgent == "" {
		c.UserAgent = rest.DefaultKubernetesUserAgent()
	}
	return rest.RESTClientFor(c)
}

// Watcher runs the informers and records what they see.
type Watcher struct {
	apps  rest.Interface
	core  rest.Interface
	opts  Options
	queue chan change

	observed *prometheus.CounterVec
	synced   prometheus.Gauge
	syncedAt atomic.Pointer[time.Time]
}

// SyncedAt returns when the informers completed their initial list.
func (w *Watcher) SyncedAt() (time.Time, bool) {
	if t := w.syncedAt.Load(); t != nil {
		return *t, true
	}
	return time.Time{}, false
}

type change struct {
	obj     any
	deleted bool
}

// New returns a Watcher for the namespaces of the service map's workloads.
func New(o Options) (*Watcher, error) {
	apps, err := restClient(o.Config, appsv1.SchemeGroupVersion, "/apis")
	if err != nil {
		return nil, fmt.Errorf("apps client: %w", err)
	}
	core, err := restClient(o.Config, corev1.SchemeGroupVersion, "/api")
	if err != nil {
		return nil, fmt.Errorf("core client: %w", err)
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Resync == 0 {
		o.Resync = DefaultResync
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	w := &Watcher{
		apps:  apps,
		core:  core,
		opts:  o,
		queue: make(chan change, 256),
		observed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mybuilds_runtime_events_total",
			Help: "Kubernetes objects observed by the runtime watch, by kind and result.",
		}, []string{"kind", "result"}),
		synced: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mybuilds_runtime_watch_synced",
			Help: "1 once the runtime watch's informers have completed their initial list.",
		}),
	}
	if o.Registerer != nil {
		o.Registerer.MustRegister(w.observed, w.synced)
	}
	return w, nil
}

// Run starts the informers and records changes until ctx is done. List and
// watch errors, such as missing RBAC, are retried by the informers with
// backoff and logged; they never stop the server.
func (w *Watcher) Run(ctx context.Context) error {
	namespaces := w.opts.Services.Namespaces()
	if len(namespaces) == 0 {
		w.opts.Logger.Info("runtime watch idle: no service has a workload")
		return nil
	}
	selector := func(o *metav1.ListOptions) { o.LabelSelector = ManagedLabel + "=true" }

	var informers []cache.SharedIndexInformer
	for _, ns := range namespaces {
		sources := []struct {
			client   cache.Getter
			resource string
			obj      runtime.Object
		}{
			{w.apps, "deployments", &appsv1.Deployment{}},
			{w.apps, "replicasets", &appsv1.ReplicaSet{}},
			{w.core, "pods", &corev1.Pod{}},
		}
		for _, src := range sources {
			lw := cache.NewFilteredListWatchFromClient(src.client, src.resource, ns, selector)
			inf := cache.NewSharedIndexInformer(lw, src.obj, w.opts.Resync, cache.Indexers{})
			// Managed fields are the bulk of an object and never read.
			if err := inf.SetTransform(func(obj any) (any, error) {
				if m, ok := obj.(metav1.Object); ok {
					m.SetManagedFields(nil)
				}
				return obj, nil
			}); err != nil {
				return err
			}
			if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc:    func(obj any) { w.enqueue(ctx, obj, false) },
				UpdateFunc: func(_, obj any) { w.enqueue(ctx, obj, false) },
				DeleteFunc: func(obj any) {
					if tomb, ok := obj.(cache.DeletedFinalStateUnknown); ok {
						obj = tomb.Obj
					}
					w.enqueue(ctx, obj, true)
				},
			}); err != nil {
				return err
			}
			informers = append(informers, inf)
		}
	}

	go w.record(ctx)
	for _, inf := range informers {
		go inf.Run(ctx.Done())
	}
	w.opts.Logger.Info("runtime watch starting", "namespaces", namespaces, "selector", ManagedLabel+"=true",
		"resync", w.opts.Resync.String())

	synced := make([]cache.InformerSynced, len(informers))
	for i, inf := range informers {
		synced[i] = inf.HasSynced
	}
	if cache.WaitForCacheSync(ctx.Done(), synced...) {
		now := w.opts.Now()
		w.syncedAt.Store(&now)
		w.synced.Set(1)
		w.opts.Logger.Info("runtime watch synced")
	}
	<-ctx.Done()
	return nil
}

func (w *Watcher) enqueue(ctx context.Context, obj any, deleted bool) {
	select {
	case w.queue <- change{obj: obj, deleted: deleted}:
	case <-ctx.Done():
	}
}

// record stores changes one at a time, so events are numbered and projected
// in the order they were observed.
func (w *Watcher) record(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-w.queue:
			w.handle(ctx, c)
		}
	}
}

func (w *Watcher) handle(ctx context.Context, c change) {
	now := w.opts.Now()
	var ev *events.Event
	var err error
	kind := "unknown"
	switch obj := c.obj.(type) {
	case *appsv1.Deployment:
		kind = "deployment"
		ev, err = DeploymentEvent(obj, c.deleted, now)
	case *appsv1.ReplicaSet:
		kind = "replicaset"
		ev, err = ReplicaSetEvent(obj, c.deleted, now)
	case *corev1.Pod:
		kind = "pod"
		ev, err = PodEvent(obj, c.deleted, now)
	}
	switch {
	case err != nil:
		w.observed.WithLabelValues(kind, "error").Inc()
		w.opts.Logger.Error("normalize runtime object", "kind", kind, "error", err)
		return
	case ev == nil:
		w.observed.WithLabelValues(kind, "ignored").Inc()
		return
	}
	if _, ok := w.opts.Services.ByWorkload(ev.Keys.Workload); !ok {
		// Labeled, but not in the service map.
		w.observed.WithLabelValues(kind, "ignored").Inc()
		return
	}

	inserted, err := w.opts.Store.InsertEvent(ctx, ev)
	if err != nil {
		w.observed.WithLabelValues(kind, "error").Inc()
		w.opts.Logger.Error("store runtime event", "kind", kind, "workload", ev.Keys.Workload, "error", err)
		return
	}
	if !inserted {
		w.observed.WithLabelValues(kind, "duplicate").Inc()
		return
	}
	w.observed.WithLabelValues(kind, "stored").Inc()

	outcomes, err := w.opts.Projector.ProjectEvent(ctx, *ev)
	if err != nil {
		w.opts.Logger.Error("projection failed", "event_id", ev.ID, "type", ev.Type, "error", err)
	}
	if w.opts.Notify != nil && len(outcomes) > 0 {
		w.opts.Notify(ctx, ev.ID, outcomes)
	}
	ids := make([]string, len(outcomes))
	for i, o := range outcomes {
		ids[i] = o.ReleaseID
	}
	w.opts.Logger.Debug("runtime event stored", "type", ev.Type, "event_id", ev.ID,
		"workload", ev.Keys.Workload, "releases", ids)
}
