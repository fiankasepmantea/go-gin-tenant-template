// Package observability holds the metrics of the service in the Prometheus text format, without a dependency:
// request counters and latency histograms per route pattern, requests in flight, and the connection pools of the
// databases. OpenTelemetry tracing is not part of the template; add it per project when needed.
package observability
