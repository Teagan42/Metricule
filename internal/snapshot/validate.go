package snapshot

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Publication rejections. A collection fails atomically rather than publishing a
// partial or truncated result (§8.3, §8.7).
var (
	// ErrNoCollectionID rejects a snapshot that owns no collection group, which would
	// make its series impossible to replace or expire.
	ErrNoCollectionID = errors.New("snapshot has no collection id")
	// ErrDuplicateFamily rejects two families with the same name in one snapshot.
	ErrDuplicateFamily = errors.New("duplicate metric family")
	// ErrDuplicateSeries rejects repeated labelsets instead of letting one overwrite
	// the other (§8.3).
	ErrDuplicateSeries = errors.New("duplicate labelset")
	// ErrInvalidSample rejects NaN and infinite values, which cannot be exposed as a
	// meaningful measurement.
	ErrInvalidSample = errors.New("invalid sample value")
	// ErrUnsortedLabels rejects labels that are not strictly increasing by name.
	// Sorted labels are what make duplicate detection and exposition deterministic.
	ErrUnsortedLabels = errors.New("labels not sorted by name")
	// ErrTotalSeriesLimit rejects a publication that would exceed the global series
	// bound (§8.7).
	ErrTotalSeriesLimit = errors.New("total published series limit exceeded")
)

// Validates a snapshot against the §8.3 series-lifecycle rules.
func validate(snap *Snapshot) error {
	if snap == nil {
		return ErrNoCollectionID
	}
	if snap.CollectionID == "" {
		return ErrNoCollectionID
	}

	families := make(map[string]struct{}, len(snap.Families))
	for i := range snap.Families {
		f := &snap.Families[i]
		if _, dup := families[f.Name]; dup {
			return fmt.Errorf("collection %q: family %q: %w", snap.CollectionID, f.Name, ErrDuplicateFamily)
		}
		families[f.Name] = struct{}{}
		if err := validateFamily(f); err != nil {
			return fmt.Errorf("collection %q: family %q: %w", snap.CollectionID, f.Name, err)
		}
	}
	return nil
}

func validateFamily(f *Family) error {
	if f.Type == TypeHistogram {
		return validateHistograms(f)
	}
	if len(f.Histograms) > 0 {
		return fmt.Errorf("histograms present on a %q family", f.Type)
	}
	seen := make(map[string]struct{}, len(f.Samples))
	for i := range f.Samples {
		s := &f.Samples[i]
		if err := validateLabels(s.Labels); err != nil {
			return err
		}
		if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			return fmt.Errorf("value %v: %w", s.Value, ErrInvalidSample)
		}
		key := seriesKey(s.Labels)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("labels %s: %w", key, ErrDuplicateSeries)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// validateHistograms enforces the invariants Prometheus exposition assumes.
//
// The source owns bucket arithmetic, so this rejects a result that is not already
// cumulative rather than silently rewriting it (P3).
func validateHistograms(f *Family) error {
	if len(f.Samples) > 0 {
		return fmt.Errorf("samples present on a histogram family")
	}
	seen := make(map[string]struct{}, len(f.Histograms))
	var labelNames []string

	for i := range f.Histograms {
		h := &f.Histograms[i]
		if err := validateLabels(h.Labels); err != nil {
			return err
		}
		names := make([]string, len(h.Labels))
		for j, l := range h.Labels {
			names[j] = l.Name
		}
		if labelNames == nil {
			labelNames = names
		} else if !slices.Equal(labelNames, names) {
			return fmt.Errorf("histogram label names differ across series")
		}

		key := seriesKey(h.Labels)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("labels %s: %w", key, ErrDuplicateSeries)
		}
		seen[key] = struct{}{}

		if math.IsNaN(h.Sum) || math.IsInf(h.Sum, 0) {
			return fmt.Errorf("sum %v: %w", h.Sum, ErrInvalidSample)
		}
		if len(h.Buckets) == 0 {
			return fmt.Errorf("histogram has no buckets")
		}

		inf, ok := h.Buckets[math.Inf(1)]
		if !ok {
			return fmt.Errorf("histogram has no +Inf bucket")
		}
		if inf != h.Count {
			return fmt.Errorf("+Inf bucket %d does not equal count %d", inf, h.Count)
		}

		bounds := make([]float64, 0, len(h.Buckets))
		for bound := range h.Buckets {
			if math.IsNaN(bound) || math.IsInf(bound, -1) {
				return fmt.Errorf("bucket bound %v: %w", bound, ErrInvalidSample)
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
	}
	return nil
}

// Requires strictly increasing names, which also rejects a repeated label name.
func validateLabels(labels []Label) error {
	for i := 1; i < len(labels); i++ {
		if labels[i-1].Name >= labels[i].Name {
			return fmt.Errorf("%q then %q: %w", labels[i-1].Name, labels[i].Name, ErrUnsortedLabels)
		}
	}
	return nil
}

// Builds a comparison key for a labelset.
//
// Length-prefixed because a label value may contain any delimiter: {a="b,c=d"} and
// {a="b",c="d"} would otherwise collide and hide a real duplicate.
func seriesKey(labels []Label) string {
	var b strings.Builder
	for _, l := range labels {
		writeField(&b, l.Name)
		writeField(&b, l.Value)
	}
	return b.String()
}

func writeField(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}
