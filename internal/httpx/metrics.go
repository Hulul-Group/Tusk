package httpx

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics bundles the Prometheus collectors used by the HTTP services.
// Use NewMetrics with a fresh registry per process (tests get their own).
type Metrics struct {
	Registry *prometheus.Registry

	requestDuration *prometheus.HistogramVec
	// RedirectCache counts hot-path lookups by result (hit|miss|notfound|error).
	// "hit" means served by the resolver without error; the resolver itself
	// (Agent A) distinguishes LRU/Redis/DB internally.
	RedirectCache *prometheus.CounterVec
	// Redirects counts redirect outcomes (target|banned|notfound|expired|protected|info|homepage).
	Redirects *prometheus.CounterVec
	// VisitsPublished / VisitsDropped: dropped is incremented by publishers
	// that expose a counter hook, or by the redirector when Publish is known
	// to have dropped (see DroppedFunc).
	VisitsPublished prometheus.Counter
	VisitsDropped   prometheus.Counter
	RateLimited     *prometheus.CounterVec
}

// NewMetrics registers collectors (incl. Go/process collectors) on a new registry.
func NewMetrics(service string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	constLabels := prometheus.Labels{"service": service}
	m := &Metrics{
		Registry: reg,
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace:   "tusk",
			Name:        "http_request_duration_seconds",
			Help:        "HTTP request latency by ServeMux route pattern.",
			ConstLabels: constLabels,
			Buckets:     []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		}, []string{"route", "method", "code"}),
		RedirectCache: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tusk", Name: "redirect_lookups_total", ConstLabels: constLabels,
			Help: "Redirect resolver lookups by result (hit, miss, error).",
		}, []string{"result"}),
		Redirects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tusk", Name: "redirects_total", ConstLabels: constLabels,
			Help: "Redirect handler outcomes.",
		}, []string{"outcome"}),
		VisitsPublished: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "tusk", Name: "visit_events_published_total", ConstLabels: constLabels,
			Help: "Visit events handed to the publisher.",
		}),
		VisitsDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "tusk", Name: "visit_events_dropped_total", ConstLabels: constLabels,
			Help: "Visit events dropped because the publisher buffer was full.",
		}),
		RateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tusk", Name: "rate_limited_total", ConstLabels: constLabels,
			Help: "Requests rejected by the rate limiter, by route.",
		}, []string{"route"}),
	}
	reg.MustRegister(m.requestDuration, m.RedirectCache, m.Redirects, m.VisitsPublished, m.VisitsDropped, m.RateLimited)
	// Pre-create hot label combos so the hot path never allocates a new child.
	for _, s := range []string{"hit", "miss", "error"} {
		m.RedirectCache.WithLabelValues(s)
	}
	return m
}

// Register adds extra collectors (e.g. from Agent A's cache/events packages).
func (m *Metrics) Register(cs ...prometheus.Collector) {
	for _, c := range cs {
		_ = m.Registry.Register(c)
	}
}

func (m *Metrics) observe(route, method string, code int, d time.Duration) {
	m.requestDuration.WithLabelValues(route, method, strconv.Itoa(code)).Observe(d.Seconds())
}

// Handler serves /metrics for this registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}
