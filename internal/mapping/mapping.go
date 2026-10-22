// Package mapping converts normalized source results into validated metric families.
//
// Mapping is the only place that interprets a metric's declared type: a state_set
// lowers to gauges here, so nothing downstream needs to know the abstraction exists
// (§8.5). It holds no source-specific logic and sees only source.ResultSet (P7).
package mapping

import (
	"fmt"
	"slices"
	"strings"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// Style selects how mapping fields are read.
type Style string

const (
	// StyleColumns reads mapping fields as result column names. Used by SQL sources.
	StyleColumns Style = "columns"
	// StyleSelectors reads mapping fields as RFC 9535 selectors. Used by JSON sources.
	StyleSelectors Style = "selectors"
)

// StyleFor reports the mapping style a source type uses.
func StyleFor(t config.SourceType) Style {
	if t == config.SourceHTTP {
		return StyleSelectors
	}
	return StyleColumns
}

// Spec is the compile-time input for one metric family.
type Spec struct {
	// CollectionID identifies the owning collection group for telemetry and snapshots.
	CollectionID string
	SourceID     string
	Metric       config.Metric
	Style        Style
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
	m := &Mapper{spec: spec, wire: wire}
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
		s, err := m.sample(row)
		if err != nil {
			return snapshot.Family{}, err
		}
		fam.Samples = append(fam.Samples, s)
	}
	sortSamples(fam.Samples)
	return fam, nil
}

// Builds one sample from one row.
func (m *Mapper) sample(row source.Row) (snapshot.Sample, error) {
	mp := m.spec.Metric.Mapping

	raw, err := m.lookup(row, mp.Value, "value")
	if err != nil {
		return snapshot.Sample{}, err
	}
	v, err := toFloat(raw)
	if err != nil {
		return snapshot.Sample{}, m.errorf("value", telemetry.ReasonConversion, err.Error())
	}

	labels := make([]snapshot.Label, 0, len(m.labels)+len(mp.StaticLabels))
	for _, name := range m.labels {
		lv, err := m.lookup(row, mp.Labels[name], "labels."+name)
		if err != nil {
			return snapshot.Sample{}, err
		}
		s, err := toLabelValue(lv)
		if err != nil {
			return snapshot.Sample{}, m.errorf("labels."+name, telemetry.ReasonConversion, err.Error())
		}
		labels = append(labels, snapshot.Label{Name: name, Value: s})
	}
	for name, value := range mp.StaticLabels {
		labels = append(labels, snapshot.Label{Name: name, Value: value})
	}
	sortLabels(labels)

	return snapshot.Sample{Labels: labels, Value: v * mp.Scale}, nil
}

// Reads one mapping field from a row.
func (m *Mapper) lookup(row source.Row, field, mappingField string) (any, error) {
	v, ok := row[field]
	if !ok {
		return nil, m.errorf(mappingField, telemetry.ReasonMissingField,
			fmt.Sprintf("result has no column %q", field))
	}
	return v, nil
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
