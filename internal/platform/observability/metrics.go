package observability

import (
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxSeries bounds the number of different label sets, so a bug or an attack cannot make the memory grow.
const maxSeries = 2000

// Buckets are the upper bounds of the latency histogram, in seconds.
var Buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type reqKey struct {
	method, route string
	status        int
}

type hist struct {
	counts []uint64 // per bucket, not cumulative
	inf    uint64   // observations above the last bucket
	sum    float64
}

type durKey struct{ method, route string }

// Registry collects the metrics of one process. Safe for concurrent use.
type Registry struct {
	version  string
	inflight atomic.Int64

	mu       sync.Mutex
	requests map[reqKey]uint64
	dur      map[durKey]*hist
	dropped  uint64

	poolMu sync.Mutex
	pools  map[string]func() sql.DBStats
}

// NewRegistry creates a registry; version is shown by the build info metric.
func NewRegistry(version string) *Registry {
	return &Registry{
		version:  version,
		requests: make(map[reqKey]uint64),
		dur:      make(map[durKey]*hist),
		pools:    make(map[string]func() sql.DBStats),
	}
}

// Observe records one finished request. route is the route pattern ("unmatched" for a 404), never the raw URL.
func (r *Registry) Observe(method, route string, status int, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rk := reqKey{method, route, status}
	dk := durKey{method, route}
	if _, ok := r.requests[rk]; !ok && len(r.requests) >= maxSeries {
		r.dropped++
		return
	}
	r.requests[rk]++
	h := r.dur[dk]
	if h == nil {
		if len(r.dur) >= maxSeries {
			return
		}
		h = &hist{counts: make([]uint64, len(Buckets))}
		r.dur[dk] = h
	}
	s := d.Seconds()
	h.sum += s
	placed := false
	for i, b := range Buckets {
		if s <= b {
			h.counts[i]++
			placed = true
			break
		}
	}
	if !placed {
		h.inf++
	}
}

// InFlight adds delta to the number of requests being served.
func (r *Registry) InFlight(delta int64) { r.inflight.Add(delta) }

// AddPool registers a database connection pool under a name ("shared", or the reference of a dedicated one).
func (r *Registry) AddPool(name string, stats func() sql.DBStats) {
	r.poolMu.Lock()
	defer r.poolMu.Unlock()
	r.pools[name] = stats
}

// Write renders every metric in the Prometheus text format (version 0.0.4).
func (r *Registry) Write(w io.Writer) {
	var b strings.Builder

	b.WriteString("# HELP app_build_info Version of the running binary.\n# TYPE app_build_info gauge\n")
	fmt.Fprintf(&b, "app_build_info{version=%s} 1\n", quote(r.version))

	b.WriteString("# HELP http_requests_in_flight Requests being served now.\n# TYPE http_requests_in_flight gauge\n")
	fmt.Fprintf(&b, "http_requests_in_flight %d\n", r.inflight.Load())

	r.mu.Lock()
	reqs := make([]reqKey, 0, len(r.requests))
	for k := range r.requests {
		reqs = append(reqs, k)
	}
	sort.Slice(reqs, func(i, j int) bool {
		a, c := reqs[i], reqs[j]
		if a.route != c.route {
			return a.route < c.route
		}
		if a.method != c.method {
			return a.method < c.method
		}
		return a.status < c.status
	})
	b.WriteString("# HELP http_requests_total Requests by method, route pattern and status.\n# TYPE http_requests_total counter\n")
	for _, k := range reqs {
		fmt.Fprintf(&b, "http_requests_total{method=%s,route=%s,status=\"%d\"} %d\n", quote(k.method), quote(k.route), k.status, r.requests[k])
	}
	fmt.Fprintf(&b, "# HELP http_metrics_dropped_total Observations dropped because of too many label sets.\n# TYPE http_metrics_dropped_total counter\nhttp_metrics_dropped_total %d\n", r.dropped)

	durs := make([]durKey, 0, len(r.dur))
	for k := range r.dur {
		durs = append(durs, k)
	}
	sort.Slice(durs, func(i, j int) bool {
		if durs[i].route != durs[j].route {
			return durs[i].route < durs[j].route
		}
		return durs[i].method < durs[j].method
	})
	b.WriteString("# HELP http_request_duration_seconds Request latency by method and route pattern.\n# TYPE http_request_duration_seconds histogram\n")
	for _, k := range durs {
		h := r.dur[k]
		var cum uint64
		for i, ub := range Buckets {
			cum += h.counts[i]
			fmt.Fprintf(&b, "http_request_duration_seconds_bucket{method=%s,route=%s,le=\"%s\"} %d\n", quote(k.method), quote(k.route), strconv.FormatFloat(ub, 'g', -1, 64), cum)
		}
		cum += h.inf
		fmt.Fprintf(&b, "http_request_duration_seconds_bucket{method=%s,route=%s,le=\"+Inf\"} %d\n", quote(k.method), quote(k.route), cum)
		fmt.Fprintf(&b, "http_request_duration_seconds_sum{method=%s,route=%s} %s\n", quote(k.method), quote(k.route), strconv.FormatFloat(h.sum, 'g', -1, 64))
		fmt.Fprintf(&b, "http_request_duration_seconds_count{method=%s,route=%s} %d\n", quote(k.method), quote(k.route), cum)
	}
	r.mu.Unlock()

	r.poolMu.Lock()
	names := make([]string, 0, len(r.pools))
	for n := range r.pools {
		names = append(names, n)
	}
	sort.Strings(names)
	stats := make(map[string]sql.DBStats, len(names))
	for _, n := range names {
		stats[n] = r.pools[n]()
	}
	r.poolMu.Unlock()
	gauge := func(name, help string, get func(sql.DBStats) float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		for _, n := range names {
			fmt.Fprintf(&b, "%s{db=%s} %s\n", name, quote(n), strconv.FormatFloat(get(stats[n]), 'g', -1, 64))
		}
	}
	gauge("db_pool_open_connections", "Open connections (in use + idle).", func(s sql.DBStats) float64 { return float64(s.OpenConnections) })
	gauge("db_pool_in_use_connections", "Connections in use.", func(s sql.DBStats) float64 { return float64(s.InUse) })
	gauge("db_pool_idle_connections", "Idle connections.", func(s sql.DBStats) float64 { return float64(s.Idle) })
	gauge("db_pool_wait_count_total", "Times a request had to wait for a connection.", func(s sql.DBStats) float64 { return float64(s.WaitCount) })
	gauge("db_pool_wait_seconds_total", "Time spent waiting for a connection.", func(s sql.DBStats) float64 { return s.WaitDuration.Seconds() })

	_, _ = io.WriteString(w, b.String())
}

// quote renders a label value: double quotes, with backslash, quote and newline escaped.
func quote(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
	return `"` + s + `"`
}
