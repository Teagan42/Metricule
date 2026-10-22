// Package telemetry defines Metricule's own metric families (§13).
//
// Family names, types, and label sets are a stable contract: renaming one breaks
// every dashboard and alert built on it. Change them only deliberately.
//
// Every label value comes from a closed enum in this package, so no caller can turn
// an error string into a label and blow up cardinality.
package telemetry

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/teagan42/metricule/internal/telemetry/reason"
	"github.com/teagan42/metricule/internal/version"
)

// Namespace prefixes every exporter-owned family and is reserved against user
// metrics by configuration validation.
const Namespace = "metricule"

// Reason labels a failure. Closed set: free-text errors belong in logs (§13).
//
// An alias, not a new type: the vocabulary lives in a dependency-free leaf package
// so adapters can classify their errors without importing the Prometheus client.
// Aliasing keeps telemetry.Reason and reason.Reason the same type, so callers may
// use either spelling.
type Reason = reason.Reason

// Failure reasons. Extend deliberately: each value is a label dimension.
const (
	ReasonTimeout         = reason.Timeout
	ReasonConnection      = reason.Connection
	ReasonQuery           = reason.Query
	ReasonDecode          = reason.Decode
	ReasonConversion      = reason.Conversion
	ReasonNullValue       = reason.NullValue
	ReasonMissingField    = reason.MissingField
	ReasonDuplicateSeries = reason.DuplicateSeries
	ReasonCardinality     = reason.Cardinality
	ReasonResultTooLarge  = reason.ResultTooLarge
	ReasonUnavailable     = reason.Unavailable
	ReasonUnknown         = reason.Unknown
)

// Trigger labels why a refresh attempt is running — not what originally wanted the
// data. The two differ only for a retry, which is attributed to TriggerRetry rather
// than inheriting the trigger it descended from.
//
// That makes refresh rate readable during an outage, at the cost of not being able to
// recover a retry's causal ancestor from this family alone. Do not add a second
// "origin" label to recover it: it would multiply this family's cardinality by the
// trigger count (§8.7), and counters cannot express per-event lineage anyway. A retry
// only ever follows a failure, so collection_errors_total already identifies which
// collections were retrying and why.
type Trigger string

// Refresh triggers.
const (
	TriggerStartup   Trigger = "startup"
	TriggerSchedule  Trigger = "schedule"
	TriggerNotify    Trigger = "notify"
	TriggerToken     Trigger = "token"
	TriggerReconnect Trigger = "reconnect"
	TriggerReconcile Trigger = "reconcile"
	// TriggerRetry marks a re-attempt after a failure. Distinct from the trigger
	// that originally failed, so refresh rate stays readable during an outage —
	// which is exactly when someone is looking at it.
	TriggerRetry Trigger = "retry"
)

// Collection outcomes, used as the result label on duration observations.
const (
	resultSuccess = "success"
	resultFailure = "failure"
)

// Metrics holds every exporter-owned family.
type Metrics struct {
	buildInfo *prometheus.GaugeVec

	sourceUp        *prometheus.GaugeVec
	sourceReconnect *prometheus.CounterVec

	collectionSuccess     *prometheus.GaugeVec
	collectionDuration    *prometheus.HistogramVec
	collectionErrors      *prometheus.CounterVec
	collectionRefresh     *prometheus.CounterVec
	collectionLastSuccess *prometheus.GaugeVec
	collectionSnapshotAge *prometheus.GaugeVec
	collectionSamples     *prometheus.GaugeVec
	triggerEvents         *prometheus.CounterVec
	coalescedEvents       *prometheus.CounterVec

	mappingErrors         *prometheus.CounterVec
	cardinalityRejections *prometheus.CounterVec

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
}

// Collection latency spans a sub-millisecond cache read to a slow aggregate query,
// so buckets cover four orders of magnitude rather than the client default.
var collectionBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}

// Scrape handling must stay fast because it only reads a snapshot (P2); buckets are
// tight enough to show regressions into upstream I/O.
var scrapeBuckets = []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25, 1}

// New registers every family and records build information.
//
// Go and process collectors are deliberately not registered here: this registry
// holds the exporter's own contract, and the serving layer decides what else to
// expose.
func New(reg prometheus.Registerer, info version.Info) *Metrics {
	f := promauto(reg)
	m := &Metrics{
		buildInfo: f.gauge("build_info",
			"Build information for the running Metricule binary. Always 1.",
			"version", "commit", "go"),

		sourceUp: f.gauge("source_up",
			"Whether the source was reachable at its last health check.", "source"),
		sourceReconnect: f.counter("source_reconnects_total",
			"Total source reconnections. Each one forces reconciliation.", "source"),

		collectionSuccess: f.gauge("collection_success",
			"Whether the last collection attempt succeeded. Absent until the first attempt.",
			"collection", "source"),
		collectionDuration: f.histogram("collection_duration_seconds",
			"Collection duration, including query execution and mapping.",
			collectionBuckets, "collection", "source", "result"),
		collectionErrors: f.counter("collection_errors_total",
			"Total failed collections by reason.", "collection", "source", "reason"),
		collectionRefresh: f.counter("collection_refresh_total",
			"Total refresh attempts, by why each attempt ran. A retry is attributed to \"retry\", not to the trigger it descended from.",
			"collection", "reason"),
		collectionLastSuccess: f.gauge("collection_last_success_timestamp_seconds",
			"Unix timestamp of the last successful collection.", "collection"),
		collectionSnapshotAge: f.gauge("collection_snapshot_age_seconds",
			"Age of the currently published snapshot.", "collection"),
		collectionSamples: f.gauge("collection_samples",
			"Number of samples in the currently published snapshot.", "collection"),
		triggerEvents: f.counter("collection_trigger_events_total",
			"Total change notifications received from a source.", "source", "collection"),
		coalescedEvents: f.counter("collection_coalesced_events_total",
			"Total change notifications collapsed into an existing pending refresh.", "collection"),

		mappingErrors: f.counter("mapping_errors_total",
			"Total mapping failures by reason.", "collection", "reason"),
		cardinalityRejections: f.counter("cardinality_rejections_total",
			"Total collections rejected for exceeding a cardinality limit.", "collection"),

		httpRequests: f.counter("http_requests_total",
			"Total HTTP requests served by the exporter.", "path", "code"),
		httpDuration: f.histogram("http_request_duration_seconds",
			"Exporter HTTP request duration.", scrapeBuckets, "path"),
	}

	m.buildInfo.WithLabelValues(info.Version, info.Commit, info.GoVersion).Set(1)
	return m
}

// SetSourceUp records source reachability.
func (m *Metrics) SetSourceUp(source string, up bool) {
	m.sourceUp.WithLabelValues(source).Set(boolValue(up))
}

// SourceReconnected counts a reconnection.
func (m *Metrics) SourceReconnected(source string) {
	m.sourceReconnect.WithLabelValues(source).Inc()
}

// CollectionSucceeded records a successful collection and its published sample count.
func (m *Metrics) CollectionSucceeded(collection, source string, d time.Duration, samples int, at time.Time) {
	m.collectionSuccess.WithLabelValues(collection, source).Set(1)
	m.collectionDuration.WithLabelValues(collection, source, resultSuccess).Observe(d.Seconds())
	m.collectionLastSuccess.WithLabelValues(collection).Set(float64(at.Unix()))
	m.collectionSamples.WithLabelValues(collection).Set(float64(samples))
}

// CollectionFailed records a failed collection.
//
// Does not touch the sample or last-success gauges: a failure must not imply that
// zero samples are current (P3). Error counters are not pre-initialized to zero
// either, because that would create a series for every collection and reason pair.
func (m *Metrics) CollectionFailed(collection, source string, d time.Duration, reason Reason) {
	m.collectionSuccess.WithLabelValues(collection, source).Set(0)
	m.collectionDuration.WithLabelValues(collection, source, resultFailure).Observe(d.Seconds())
	m.collectionErrors.WithLabelValues(collection, source, string(reason)).Inc()
}

// RefreshRequested counts a refresh request and what triggered it.
func (m *Metrics) RefreshRequested(collection string, trigger Trigger) {
	m.collectionRefresh.WithLabelValues(collection, string(trigger)).Inc()
}

// EventsCoalesced counts notifications folded into a pending refresh.
//
// The ratio against collection_refresh_total shows whether debouncing is working.
func (m *Metrics) EventsCoalesced(collection string, n int) {
	if n <= 0 {
		return
	}
	m.coalescedEvents.WithLabelValues(collection).Add(float64(n))
}

// TriggerEventReceived counts a change notification delivered by a source.
func (m *Metrics) TriggerEventReceived(source, collection string) {
	m.triggerEvents.WithLabelValues(source, collection).Inc()
}

// MappingFailed counts a mapping failure.
func (m *Metrics) MappingFailed(collection string, reason Reason) {
	m.mappingErrors.WithLabelValues(collection, string(reason)).Inc()
}

// CardinalityRejected counts a collection rejected for exceeding a series cap.
func (m *Metrics) CardinalityRejected(collection string) {
	m.cardinalityRejections.WithLabelValues(collection).Inc()
}

// SetSnapshotAge publishes the age of the current snapshot.
//
// Paired with the configured staleness policy, this is how an operator tells cached
// data from current data (P6).
func (m *Metrics) SetSnapshotAge(collection string, age time.Duration) {
	m.collectionSnapshotAge.WithLabelValues(collection).Set(age.Seconds())
}

// ObserveScrape records one exporter HTTP request.
func (m *Metrics) ObserveScrape(path string, statusCode int, d time.Duration) {
	m.httpRequests.WithLabelValues(path, strconv.Itoa(statusCode)).Inc()
	m.httpDuration.WithLabelValues(path).Observe(d.Seconds())
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Builds and registers families under the exporter namespace.
type factory struct{ reg prometheus.Registerer }

func promauto(reg prometheus.Registerer) factory { return factory{reg} }

func (f factory) gauge(name, help string, labels ...string) *prometheus.GaugeVec {
	v := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: Namespace, Name: name, Help: help,
	}, labels)
	f.reg.MustRegister(v)
	return v
}

func (f factory) counter(name, help string, labels ...string) *prometheus.CounterVec {
	v := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: Namespace, Name: name, Help: help,
	}, labels)
	f.reg.MustRegister(v)
	return v
}

func (f factory) histogram(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	v := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: Namespace, Name: name, Help: help, Buckets: buckets,
	}, labels)
	f.reg.MustRegister(v)
	return v
}
