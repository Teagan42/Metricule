package httpjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/teagan42/metricule/internal/selector"
	"github.com/teagan42/metricule/internal/source"
)

// extractor holds one metric's compiled selectors. Semantics are in the package
// doc comment.
type extractor struct {
	rows  *selector.Selector
	value *selector.Selector
	// labels maps label name to its selector. Several labels may share a selector,
	// which yields one shared column.
	labels map[string]*selector.Selector
	// columns is the deterministic column order: value first, then labels by name.
	columns []string
}

func compileExtractor(def source.QueryDefinition) (*extractor, error) {
	ex := &extractor{}

	if def.Value == "" {
		return nil, errors.New("mapping.value: required field is missing")
	}
	var err error
	if ex.value, err = selector.Parse(def.Value); err != nil {
		return nil, fmt.Errorf("mapping.value: %w", err)
	}
	if def.Rows != "" {
		if ex.rows, err = selector.Parse(def.Rows); err != nil {
			return nil, fmt.Errorf("mapping.rows: %w", err)
		}
	}

	ex.columns = []string{def.Value}
	seen := map[string]struct{}{def.Value: {}}
	ex.labels = make(map[string]*selector.Selector, len(def.Labels))
	for _, name := range sortedLabelNames(def.Labels) {
		raw := def.Labels[name]
		sel, perr := selector.Parse(raw)
		if perr != nil {
			return nil, fmt.Errorf("mapping.labels.%s: %w", name, perr)
		}
		ex.labels[name] = sel
		if _, dup := seen[raw]; !dup {
			seen[raw] = struct{}{}
			ex.columns = append(ex.columns, raw)
		}
	}
	return ex, nil
}

func (e *extractor) labelNames() []string { return sortedLabelNames(e.labels) }

// Converts a decoded document into a normalized ResultSet.
//
// maxRows errors rather than truncating: a truncated result would publish a
// plausible but wrong series set (§8.7).
func (e *extractor) resultSet(doc any, maxRows int) (source.ResultSet, error) {
	nodes := []any{doc}
	if e.rows != nil {
		nodes = e.rows.SelectAll(doc)
	}
	if maxRows > 0 && len(nodes) > maxRows {
		return source.ResultSet{}, fmt.Errorf("mapping.rows %s selected %d rows, exceeding max_rows_per_query %d",
			e.rows, len(nodes), maxRows)
	}

	rows := make([]source.Row, 0, len(nodes))
	types := make(map[string]string, len(e.columns))
	for i, node := range nodes {
		row := make(source.Row, len(e.columns))
		if err := e.fill(row, types, node); err != nil {
			return source.ResultSet{}, fmt.Errorf("row %d: %w", i, err)
		}
		rows = append(rows, row)
	}

	columns := make([]source.Column, 0, len(e.columns))
	for _, name := range e.columns {
		t, ok := types[name]
		if !ok {
			t = "unknown"
		}
		columns = append(columns, source.Column{Name: name, Type: t})
	}
	return source.ResultSet{Columns: columns, Rows: rows}, nil
}

// Evaluates every selector against one row node.
func (e *extractor) fill(row source.Row, types map[string]string, node any) error {
	if err := e.set(row, types, e.value.String(), e.value, node, "mapping.value"); err != nil {
		return err
	}
	for _, name := range e.labelNames() {
		sel := e.labels[name]
		if err := e.set(row, types, sel.String(), sel, node, "mapping.labels."+name); err != nil {
			return err
		}
	}
	return nil
}

func (e *extractor) set(row source.Row, types map[string]string, column string, sel *selector.Selector, node any, field string) error {
	raw, err := sel.SelectOne(node)
	switch {
	case errors.Is(err, selector.ErrNotFound):
		return nil // Absent: leave the key out so the mapper reports a missing field.
	case err != nil:
		return fmt.Errorf("%s: %w", field, err)
	}
	v, t, err := convert(raw)
	if err != nil {
		return fmt.Errorf("%s %s: %w", field, sel, err)
	}
	row[column] = v
	recordType(types, column, t)
	return nil
}

// Narrows a decoded JSON node to a mappable scalar.
func convert(raw any) (any, string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, "null", nil
	case string:
		return v, "string", nil
	case bool:
		return v, "boolean", nil
	case json.Number:
		return convertNumber(v)
	case float64:
		// Reachable only if a caller decoded without UseNumber.
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, "", fmt.Errorf("%w: %v", errNonFinite, v)
		}
		return v, "number", nil
	case map[string]any:
		return nil, "", fmt.Errorf("%w: matched a JSON object", errNotScalar)
	case []any:
		return nil, "", fmt.Errorf("%w: matched a JSON array", errNotScalar)
	default:
		return nil, "", fmt.Errorf("%w: matched %T", errNotScalar, raw)
	}
}

// Keeps integers exact and rejects values Prometheus cannot represent.
func convertNumber(n json.Number) (any, string, error) {
	if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
		return i, "number", nil
	}
	f, err := strconv.ParseFloat(n.String(), 64)
	// Overflow such as 1e400 reports ErrRange and yields ±Inf. Clamping to MaxFloat64
	// would invent a sample, which AD-010 forbids.
	if errors.Is(err, strconv.ErrRange) || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, "", fmt.Errorf("%w: %s", errNonFinite, n.String())
	}
	if err != nil {
		return nil, "", fmt.Errorf("invalid number %s: %w", n.String(), err)
	}
	return f, "number", nil
}

var (
	errNonFinite = errors.New("non-finite number is not a valid sample")
	errNotScalar = errors.New("selector did not match a scalar")
)

// Reports "mixed" when rows disagree, since Column.Type describes the column.
func recordType(types map[string]string, column, t string) {
	prev, ok := types[column]
	switch {
	case !ok, prev == "null":
		types[column] = t
	case t == "null", prev == t:
	default:
		types[column] = "mixed"
	}
}

func sortedLabelNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
