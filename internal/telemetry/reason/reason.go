// Package reason holds the closed vocabulary of failure labels.
//
// Separate from internal/telemetry, and deliberately dependency-free, so an adapter
// can name why it failed without taking on an exporter concern. internal/telemetry
// imports client_golang; homing this type there would make every adapter depend
// transitively on the Prometheus client.
//
// Values are label values on exporter-owned metrics, so the set is closed: free-text
// errors belong in logs (§13).
package reason

// Reason labels a failure.
type Reason string

// Failure reasons. Extend deliberately: each value is a label dimension.
//
// Consumers bound their own accepted subset. A reason outside what a consumer
// accepts collapses to Unknown rather than widening that consumer's label space.
const (
	Timeout         Reason = "timeout"
	Connection      Reason = "connection"
	Query           Reason = "query"
	Decode          Reason = "decode"
	Conversion      Reason = "conversion"
	NullValue       Reason = "null_value"
	MissingField    Reason = "missing_field"
	DuplicateSeries Reason = "duplicate_series"
	Cardinality     Reason = "cardinality"
	ResultTooLarge  Reason = "result_too_large"
	Unavailable     Reason = "unavailable"
	Unknown         Reason = "unknown"
)
