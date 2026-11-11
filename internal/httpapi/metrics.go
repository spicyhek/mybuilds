package httpapi

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds the HTTP metrics for the public listener. They are scraped
// from the separate metrics listener, which is never exposed publicly.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics registers the HTTP metrics and a build_info gauge with reg.
func NewMetrics(reg prometheus.Registerer, version string) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "mybuilds",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "HTTP requests to the public listener by route pattern and status code.",
		}, []string{"route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "mybuilds",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration on the public listener by route pattern.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"route"}),
	}

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "mybuilds",
		Name:      "build_info",
		Help:      "Always 1, labeled with the running version.",
	}, []string{"version"})
	buildInfo.WithLabelValues(version).Set(1)

	reg.MustRegister(m.requests, m.duration, buildInfo)
	return m
}

func (m *Metrics) record(route string, status int, d time.Duration) {
	m.requests.WithLabelValues(route, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(route).Observe(d.Seconds())
}
