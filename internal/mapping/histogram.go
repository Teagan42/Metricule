package mapping

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// mapHistograms groups bucket rows into one histogram per labelset.
//
// The source owns bucket arithmetic. This validates that the result is already
// cumulative and consistent, and never rewrites counts into a different shape (P3).
func (m *Mapper) mapHistograms(rs source.ResultSet) (snapshot.Family, error) {
	mp := m.spec.Metric.Mapping
	fam := snapshot.Family{
		Name: m.spec.Metric.Name,
		Help: m.spec.Metric.Help,
		Type: snapshot.TypeHistogram,
	}

	type group struct {
		labels  []snapshot.Label
		buckets map[float64]uint64
		count   uint64
		sum     float64
		rows    int
		omit    bool
	}
	groups := map[string]*group{}

	for _, row := range rs.Rows {
		labels, err := m.rowLabels(row)
		if err != nil {
			if errors.Is(err, errOmitSample) {
				return snapshot.Family{}, m.errorf("labels", telemetry.ReasonNullValue,
					"histogram labels must not be null")
			}
			return snapshot.Family{}, err
		}
		key := labelKey(labels)
		g, ok := groups[key]
		if !ok {
			g = &group{labels: labels, buckets: map[float64]uint64{}}
			groups[key] = g
		}
		if g.omit {
			continue
		}

		rawBound, err := m.lookup(row, mp.Buckets, "buckets")
		if err != nil {
			return snapshot.Family{}, err
		}
		bound, err := toBucketBound(rawBound)
		if err != nil {
			if errors.Is(err, errNullValue) && mp.Null == config.NullOmit {
				g.omit = true
				continue
			}
			return snapshot.Family{}, m.conversionError("buckets", err)
		}
		if !math.IsInf(bound, 1) {
			bound *= m.scale
		}

		rawCount, err := m.lookup(row, mp.Value, fieldValue)
		if err != nil {
			return snapshot.Family{}, err
		}
		bucketCount, err := toCount(rawCount)
		if err != nil {
			if errors.Is(err, errNullValue) && mp.Null == config.NullOmit {
				g.omit = true
				continue
			}
			return snapshot.Family{}, m.conversionError(fieldValue, err)
		}

		rawTotal, err := m.lookup(row, mp.Count, "count")
		if err != nil {
			return snapshot.Family{}, err
		}
		total, err := toCount(rawTotal)
		if err != nil {
			if errors.Is(err, errNullValue) && mp.Null == config.NullOmit {
				g.omit = true
				continue
			}
			return snapshot.Family{}, m.conversionError("count", err)
		}

		rawSum, err := m.lookup(row, mp.Sum, "sum")
		if err != nil {
			return snapshot.Family{}, err
		}
		sum, err := toFloat(rawSum)
		if err != nil {
			if errors.Is(err, errNullValue) && mp.Null == config.NullOmit {
				g.omit = true
				continue
			}
			return snapshot.Family{}, m.conversionError("sum", err)
		}
		sum *= m.scale

		if g.rows > 0 {
			if g.count != total {
				return snapshot.Family{}, m.errorf("count", telemetry.ReasonConversion,
					"histogram count is not constant across bucket rows")
			}
			if g.sum != sum {
				return snapshot.Family{}, m.errorf("sum", telemetry.ReasonConversion,
					"histogram sum is not constant across bucket rows")
			}
		}
		if _, dup := g.buckets[bound]; dup {
			return snapshot.Family{}, m.errorf("buckets", telemetry.ReasonDuplicateSeries,
				fmt.Sprintf("bucket bound %v appears more than once", bound))
		}
		g.buckets[bound] = bucketCount
		g.count, g.sum = total, sum
		g.rows++
	}

	series := 0
	for _, g := range groups {
		if g.omit || g.rows == 0 {
			continue
		}
		h := snapshot.Histogram{Labels: g.labels, Buckets: g.buckets, Count: g.count, Sum: g.sum}
		if err := validateHistogram(h); err != nil {
			return snapshot.Family{}, m.histogramError(err)
		}
		series += len(g.buckets) + 2
		fam.Histograms = append(fam.Histograms, h)
	}
	slices.SortFunc(fam.Histograms, func(a, b snapshot.Histogram) int {
		return strings.Compare(labelKey(a.Labels), labelKey(b.Labels))
	})
	if err := m.validateHistogramSet(fam.Histograms, series); err != nil {
		return snapshot.Family{}, err
	}
	return fam, nil
}

// validateHistogram checks one series without publishing a partial result.
func validateHistogram(h snapshot.Histogram) error {
	if len(h.Buckets) == 0 {
		return errors.New("histogram has no buckets")
	}
	inf, ok := h.Buckets[math.Inf(1)]
	if !ok {
		return errors.New("histogram has no +Inf bucket")
	}
	if inf != h.Count {
		return fmt.Errorf("+Inf bucket %d does not equal count %d", inf, h.Count)
	}
	bounds := make([]float64, 0, len(h.Buckets))
	for bound := range h.Buckets {
		if math.IsNaN(bound) || math.IsInf(bound, -1) {
			return fmt.Errorf("invalid bucket bound %v", bound)
		}
		bounds = append(bounds, bound)
	}
	slices.Sort(bounds)
	var previous uint64
	for _, bound := range bounds {
		count := h.Buckets[bound]
		if count < previous {
			return fmt.Errorf("bucket %v count %d is smaller than the previous bucket %d: buckets must be cumulative",
				bound, count, previous)
		}
		previous = count
	}
	return nil
}

func (m *Mapper) histogramError(err error) error {
	return m.errorf("buckets", telemetry.ReasonConversion, err.Error())
}

// validateHistogramSet enforces cardinality and label bounds across histograms.
func (m *Mapper) validateHistogramSet(histograms []snapshot.Histogram, series int) error {
	if limit := m.spec.Limits.MaxSeriesPerMetric; limit > 0 && series > limit {
		return m.errorf(fieldValue, telemetry.ReasonCardinality,
			fmt.Sprintf("%d series exceeds max_series_per_metric %d", series, limit))
	}
	maxBytes := m.spec.Limits.MaxLabelValueBytes
	var names []string
	for _, h := range histograms {
		for _, l := range h.Labels {
			if maxBytes > 0 && len(l.Value) > maxBytes {
				return m.errorf("labels."+l.Name, telemetry.ReasonCardinality,
					fmt.Sprintf("label value of %d bytes exceeds max_label_value_bytes %d",
						len(l.Value), maxBytes))
			}
		}
		current := make([]string, len(h.Labels))
		for i, l := range h.Labels {
			current[i] = l.Name
		}
		if names == nil {
			names = current
		} else if !slices.Equal(names, current) {
			return m.errorf("labels", telemetry.ReasonConversion,
				"histogram label names differ across series")
		}
	}
	return nil
}

// toBucketBound parses a cumulative bucket bound, including +Inf.
func toBucketBound(v any) (float64, error) {
	switch t := v.(type) {
	case nil:
		return 0, errNullValue
	case string:
		return parseBound(t)
	case []byte:
		return parseBound(string(t))
	case float64:
		if math.IsNaN(t) || math.IsInf(t, -1) {
			return 0, errNumString
		}
		return t, nil
	case float32:
		f := float64(t)
		if math.IsNaN(f) || math.IsInf(f, -1) {
			return 0, errNumString
		}
		return f, nil
	default:
		return toFloat(v)
	}
}

func parseBound(s string) (float64, error) {
	trimmed := strings.TrimSpace(s)
	switch strings.ToLower(trimmed) {
	case "+inf", "inf":
		return math.Inf(1), nil
	}
	f, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, -1) {
		return 0, errNumString
	}
	return f, nil
}

// toCount parses a non-negative integral observation count.
func toCount(v any) (uint64, error) {
	f, err := toFloat(v)
	if err != nil {
		return 0, err
	}
	if f < 0 || math.Trunc(f) != f || f > math.MaxUint64 {
		return 0, errCount
	}
	return uint64(f), nil
}

var errCount = errors.New("value is not a non-negative integer")
