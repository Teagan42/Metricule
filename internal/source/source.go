// Package source defines the adapter contract shared by every data source.
//
// Adapters live in subpackages and implement these interfaces. The collection
// scheduler and mapping engine depend on this package only, never on a concrete
// adapter (P7). Adapters MUST NOT import each other.
package source

import (
	"context"
	"time"
)

// Source queries a data source. Every adapter implements it.
//
// Implementations must be safe for concurrent use by multiple collectors.
type Source interface {
	// ID returns the configured source name, used as a bounded telemetry label.
	ID() string

	// Type returns the adapter type, such as "postgres".
	Type() string

	// Capabilities reports what this adapter supports. The scheduler reads
	// capabilities instead of type-asserting concrete adapter types.
	Capabilities() Capabilities

	// Open establishes pools and verifies reachability. Called once at startup.
	Open(ctx context.Context) error

	// CompileQuery validates a metric's query definition and returns an executable
	// spec. Called during configuration compilation, before any collection, so that
	// unsupported or malformed queries fail at startup rather than at scrape time.
	CompileQuery(def QueryDefinition) (QuerySpec, error)

	// Query executes a compiled spec. Must honor context cancellation and must
	// return an error rather than a truncated ResultSet when MaxRows is exceeded.
	Query(ctx context.Context, req QueryRequest) (ResultSet, error)

	// Health reports current source reachability for /readyz and metricule_source_up.
	Health(ctx context.Context) error

	// Close releases connections and terminates listeners.
	Close() error
}

// WatchableSource observes changes and emits invalidation signals.
//
// Optional: a source may support querying without change observation.
type WatchableSource interface {
	Source

	// Watch blocks until ctx is cancelled, sending change events as they are
	// observed. Implementations must reconnect internally with bounded backoff and
	// must emit a ChangeEvent of KindReconnect after re-establishing a subscription
	// so the scheduler can reconcile (§9.2).
	//
	// Implementations must not block indefinitely on a full events channel; drop and
	// count coalesced events instead of growing an unbounded queue (rule 10).
	Watch(ctx context.Context, spec WatchSpec, events chan<- ChangeEvent) error
}

// Capabilities describes adapter support. Unsupported requested capabilities must
// produce actionable validation errors during compilation.
type Capabilities struct {
	Query             bool
	Poll              bool
	Notifications     bool
	ChangeTokens      bool
	CDC               bool
	TriggerManagement bool
}

// ChangeKind classifies a change signal.
type ChangeKind string

const (
	// KindNotify is an adapter-delivered notification, such as PostgreSQL NOTIFY.
	KindNotify ChangeKind = "notify"
	// KindToken is an observed change-token advance, such as SQLite data_version.
	KindToken ChangeKind = "token"
	// KindReconnect follows a re-established subscription and forces reconciliation.
	// Notification delivery during a disconnect is not durable, so the scheduler
	// must treat prior state as unknown.
	KindReconnect ChangeKind = "reconnect"
	// KindReconcile is a scheduler-initiated periodic refresh.
	KindReconcile ChangeKind = "reconcile"
)

// ChangeEvent is an invalidation signal, not a durable business event (AD-005).
//
// Receipt means "something may have changed"; absence does not prove that nothing
// changed. Consumers must not derive counters from event arrival (AD-012).
type ChangeEvent struct {
	SourceID string
	// Scope identifies the affected watch scope, such as "public.jobs". Empty means
	// the change scope is unknown and all plans on this source are invalidated.
	Scope string
	Kind  ChangeKind
	At    time.Time
}

// ScopeUnknown marks an event whose affected scope cannot be determined.
const ScopeUnknown = ""

// WatchSpec requests change observation for one scope.
type WatchSpec struct {
	// Channel is the PostgreSQL NOTIFY channel. Empty for adapters without channels.
	Channel string
	// Tables are the qualified tables to observe. Advisory: an adapter that can only
	// detect database-wide change reports ScopeUnknown regardless.
	Tables []string
	// Events filters DML kinds where the adapter supports it.
	Events []string
}

// QueryDefinition is the source-agnostic query configuration handed to an adapter for
// compilation. Adapters read the fields they support and reject the rest.
type QueryDefinition struct {
	// MetricName scopes compilation errors to a configured metric.
	MetricName string

	// SQL is set for database adapters.
	SQL string

	// Method, Path, Params, and Headers are set for HTTP adapters.
	Method  string
	Path    string
	Params  map[string]string
	Headers map[string]string

	// Rows, Value, and Labels are selectors for adapters that read structured
	// documents rather than rows. A document adapter resolves them into a
	// normalized ResultSet so the mapping engine performs plain column lookups for
	// every source type (P7, §12).
	//
	// Column keys are the selector strings themselves, so `row[mapping.Value]`
	// resolves identically whether Value is a SQL column name or a JSONPath
	// expression. Renaming to canonical columns would force the mapper to know
	// which source it came from.
	//
	// Empty Rows means the document yields a single row.
	Rows   string
	Value  string
	Labels map[string]string
}

// QuerySpec is a compiled, adapter-specific query. Opaque outside its adapter.
type QuerySpec interface {
	// Describe returns a sanitized description for diagnostics and `metricule test`.
	// Must never include credentials or row data.
	Describe() string
}

// QueryRequest carries a compiled spec plus the per-collection resource bounds (P5).
type QueryRequest struct {
	Spec QuerySpec
	// MaxRows bounds the result. Adapters must error, not truncate (§8.7).
	MaxRows int
}

// ResultSet is a normalized query result. The mapping engine consumes only this.
type ResultSet struct {
	Columns []Column
	Rows    []Row
}

// Column describes one result field.
type Column struct {
	Name string
	// Type is an adapter-reported type name, used for diagnostics and online
	// validation. Not authoritative for conversion.
	Type string
}

// Row maps column name to value. Values are Go scalars, nil, or []byte.
//
// An absent key and a present nil value are different conditions and must stay
// distinguishable: a missing field is a mapping error, while a present null is
// governed by the configured null policy (§12.4). Use the two-value form to tell
// them apart; no sentinel value is required.
//
//	v, ok := row[name]  // ok reports presence, v == nil reports a null value
type Row map[string]any
