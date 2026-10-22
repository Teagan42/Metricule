// Package mapping converts normalized source results into validated metric families.
//
// Mapping is the only place that interprets a metric's declared type: a state_set
// lowers to gauges here, so nothing downstream needs to know the abstraction exists
// (§8.5).
//
// Every mapping field is a plain key into source.Row, for every source type. Document
// adapters resolve their selectors and key the resolved columns by the selector string
// (ADR 0008), so this package branches on no source type and imports no adapter (P7).
package mapping

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// stateLabel is generated for state_set metrics. Configuration rejects a
// user-declared label of the same name.
const stateLabel = "state"

// fieldValue names the value mapping field in diagnostics (§12.3).
const fieldValue = "value"

// Spec is the compile-time input for one metric family.
type Spec struct {
	// CollectionID identifies the owning collection group for telemetry and snapshots.
	CollectionID string
	SourceID     string
	Metric       config.Metric
	// Limits are the metric's effective cardinality bounds (§8.7).
	Limits config.Limits
}

// Mapper converts result sets into one metric family.
//
// Compiled once at startup and safe for concurrent use: Map holds no state.
type Mapper struct {
	spec   Spec
	wire   snapshot.Type
	labels []string
	scale  float64
}

// Compile validates a mapping and returns a reusable mapper.
//
// Fails at startup rather than at collection time, so a malformed mapping never
// reaches a scrape.
func Compile(spec Spec) (*Mapper, error) {
	wire, err := wireType(spec.Metric.Type)
	if err != nil {
		return nil, fmt.Errorf("metric %q: %w", spec.Metric.Name, err)
	}
	m := &Mapper{spec: spec, wire: wire, scale: spec.Metric.Mapping.Scale}
	if m.scale == 0 {
		// An unset scale means 1. Multiplying by the zero value would publish a
		// fabricated zero (AD-010).
		m.scale = 1
	}
	for name := range spec.Metric.Mapping.Labels {
		m.labels = append(m.labels, name)
	}
	slices.Sort(m.labels)
	return m, nil
}

// Name returns the metric family name.
func (m *Mapper) Name() string { return m.spec.Metric.Name }

// Map converts a result set into one family.
//
// Returns an error rather than a partial family: a caller must never publish half a
// collection (§8.2).
func (m *Mapper) Map(rs source.ResultSet) (snapshot.Family, error) {
	fam := snapshot.Family{
		Name: m.spec.Metric.Name,
		Help: m.spec.Metric.Help,
		Type: m.wire,
	}
	for _, row := range rs.Rows {
		samples, err := m.rowSamples(row)
		switch {
		case errors.Is(err, errOmitSample):
			continue
		case err != nil:
			return snapshot.Family{}, err
		}
		fam.Samples = append(fam.Samples, samples...)
	}
	sortSamples(fam.Samples)
	if err := m.validate(fam.Samples); err != nil {
		return snapshot.Family{}, err
	}
	return fam, nil
}

// Enforces the publication invariants: unique labelsets, bounded cardinality, and
// valid sample values (§8.3, §8.7).
//
// Runs on the complete sample set because a cap breach must fail the collection
// atomically rather than truncate it.
func (m *Mapper) validate(samples []snapshot.Sample) error {
	if limit := m.spec.Limits.MaxSeriesPerMetric; limit > 0 && len(samples) > limit {
		return m.errorf(fieldValue, telemetry.ReasonCardinality,
			fmt.Sprintf("%d series exceeds max_series_per_metric %d", len(samples), limit))
	}

	maxBytes := m.spec.Limits.MaxLabelValueBytes
	seen := make(map[string]struct{}, len(samples))
	for _, s := range samples {
		for _, l := range s.Labels {
			if maxBytes > 0 && len(l.Value) > maxBytes {
				return m.errorf("labels."+l.Name, telemetry.ReasonCardinality,
					fmt.Sprintf("label value of %d bytes exceeds max_label_value_bytes %d",
						len(l.Value), maxBytes))
			}
		}
		key := labelKey(s.Labels)
		if _, dup := seen[key]; dup {
			// Overwriting would hide a query that does not group as its author
			// believed (§8.3).
			return m.errorf("labels", telemetry.ReasonDuplicateSeries,
				"two rows produced the same labelset")
		}
		seen[key] = struct{}{}

		if m.wire == snapshot.TypeCounter && s.Value < 0 {
			return m.errorf(fieldValue, telemetry.ReasonConversion,
				"counter sample is negative, which is not a cumulative value")
		}
	}
	return nil
}

// Builds every sample one row contributes. A state_set row yields one per state.
func (m *Mapper) rowSamples(row source.Row) ([]snapshot.Sample, error) {
	labels, err := m.rowLabels(row)
	if err != nil {
		return nil, err
	}
	if m.spec.Metric.Type == config.MetricStateSet {
		return m.stateSamples(row, labels)
	}

	raw, err := m.lookup(row, m.spec.Metric.Mapping.Value, fieldValue)
	if err != nil {
		return nil, err
	}
	v, err := toFloat(raw)
	if err != nil {
		return nil, m.conversionError(fieldValue, err)
	}
	return []snapshot.Sample{{Labels: labels, Value: v * m.scale}}, nil
}

// Expands one row into a gauge per configured state: 1 for the active state, 0 for
// the rest (§5.1, §8.5).
func (m *Mapper) stateSamples(row source.Row, labels []snapshot.Label) ([]snapshot.Sample, error) {
	mp := m.spec.Metric.Mapping

	raw, err := m.lookup(row, mp.Value, fieldValue)
	if err != nil {
		return nil, err
	}
	active, err := toLabelValue(raw)
	if err != nil {
		return nil, m.conversionError(fieldValue, err)
	}
	if !slices.Contains(mp.States, active) {
		// The value itself is source data and stays out of the message (§12.3).
		return nil, m.errorf(fieldValue, telemetry.ReasonConversion,
			"value is not one of the configured states")
	}

	out := make([]snapshot.Sample, 0, len(mp.States))
	for _, state := range mp.States {
		stateLabels := make([]snapshot.Label, 0, len(labels)+1)
		stateLabels = append(stateLabels, labels...)
		stateLabels = append(stateLabels, snapshot.Label{Name: stateLabel, Value: state})
		sortLabels(stateLabels)

		value := 0.0
		if state == active {
			value = 1
		}
		out = append(out, snapshot.Sample{Labels: stateLabels, Value: value})
	}
	return out, nil
}

// Builds the labelset shared by every sample from one row.
func (m *Mapper) rowLabels(row source.Row) ([]snapshot.Label, error) {
	mp := m.spec.Metric.Mapping
	labels := make([]snapshot.Label, 0, len(m.labels)+len(mp.StaticLabels))
	for _, name := range m.labels {
		field := "labels." + name
		raw, err := m.lookup(row, mp.Labels[name], field)
		if err != nil {
			return nil, err
		}
		value, err := toLabelValue(raw)
		if err != nil {
			return nil, m.conversionError(field, err)
		}
		labels = append(labels, snapshot.Label{Name: name, Value: value})
	}
	for name, value := range mp.StaticLabels {
		labels = append(labels, snapshot.Label{Name: name, Value: value})
	}
	sortLabels(labels)
	return labels, nil
}

// Reads one mapping field from a row.
//
// Absence and a present null are different conditions: absence is a mapping error,
// while a null follows the configured policy (ADR 0008, §12.4).
func (m *Mapper) lookup(row source.Row, field, mappingField string) (any, error) {
	v, ok := row[field]
	if !ok {
		return nil, m.errorf(mappingField, telemetry.ReasonMissingField,
			fmt.Sprintf("result has no field %q", field))
	}
	return v, nil
}

// Classifies a conversion failure, applying the configured null policy (§12.4).
func (m *Mapper) conversionError(field string, err error) error {
	if errors.Is(err, errNullValue) {
		if m.spec.Metric.Mapping.Null == config.NullOmit {
			return errOmitSample
		}
		return m.errorf(field, telemetry.ReasonNullValue, err.Error())
	}
	return m.errorf(field, telemetry.ReasonConversion, err.Error())
}

func (m *Mapper) errorf(field string, reason telemetry.Reason, detail string) error {
	return &Error{
		Metric: m.spec.Metric.Name,
		Source: m.spec.SourceID,
		Field:  field,
		Reason: reason,
		Detail: detail,
	}
}

func wireType(t config.MetricType) (snapshot.Type, error) {
	switch t {
	case config.MetricGauge:
		return snapshot.TypeGauge, nil
	case config.MetricCounter:
		// Monotonicity is a property of the source column across collections, which a
		// single result set cannot show. Mapping rejects only what it can see: a
		// negative cumulative value (§8.5).
		return snapshot.TypeCounter, nil
	case config.MetricStateSet:
		// A state_set is a mapping abstraction over gauges, not a wire type (§5.1).
		return snapshot.TypeGauge, nil
	default:
		return "", fmt.Errorf("unsupported metric type %q", t)
	}
}

func sortLabels(labels []snapshot.Label) {
	slices.SortFunc(labels, func(a, b snapshot.Label) int {
		return strings.Compare(a.Name, b.Name)
	})
}

// Orders samples by labelset so exposition and duplicate detection are deterministic.
func sortSamples(samples []snapshot.Sample) {
	slices.SortFunc(samples, func(a, b snapshot.Sample) int {
		return strings.Compare(labelKey(a.Labels), labelKey(b.Labels))
	})
}

func labelKey(labels []snapshot.Label) string {
	var b strings.Builder
	for _, l := range labels {
		b.WriteString(l.Name)
		// Separators cannot appear in a label name and keep distinct label sets from
		// colliding on concatenation.
		b.WriteByte(0)
		b.WriteString(l.Value)
		b.WriteByte(0)
	}
	return b.String()
}
