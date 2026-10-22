package httpjson_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/source"
)

// §20.4.1 — scalar JSON value.
func TestScalarValue(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"sessions":{"active":42}}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{Value: "$.sessions.active"})

	rs := query(t, s, spec, 10)
	want := source.ResultSet{
		Columns: []source.Column{{Name: "$.sessions.active", Type: "number"}},
		Rows:    []source.Row{{"$.sessions.active": int64(42)}},
	}
	if !reflect.DeepEqual(rs, want) {
		t.Fatalf("ResultSet = %+v, want %+v", rs, want)
	}
}

// §20.4.2 — array extraction with row-relative value and label selectors (§11.2).
func TestArrayExtraction(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"workers":[
		{"name":"worker-a","active_tasks":7},
		{"name":"worker-b","active_tasks":0}
	]}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{
		Rows:   "$.workers[*]",
		Value:  "$.active_tasks",
		Labels: map[string]string{"worker": "$.name"},
	})

	rs := query(t, s, spec, 10)
	want := source.ResultSet{
		Columns: []source.Column{
			{Name: "$.active_tasks", Type: "number"},
			{Name: "$.name", Type: "string"},
		},
		Rows: []source.Row{
			{"$.active_tasks": int64(7), "$.name": "worker-a"},
			{"$.active_tasks": int64(0), "$.name": "worker-b"},
		},
	}
	if !reflect.DeepEqual(rs, want) {
		t.Fatalf("ResultSet = %+v, want %+v", rs, want)
	}
}

// Nested arrays: a descendant selector reaches repeating elements at any depth, and
// row-relative selectors still resolve against each element.
func TestNestedArrayRows(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"regions":[
		{"id":"eu","pools":[{"name":"a","size":1},{"name":"b","size":2}]},
		{"id":"us","pools":[{"name":"c","size":3}]}
	]}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{
		Rows:   "$.regions[*].pools[*]",
		Value:  "$.size",
		Labels: map[string]string{"pool": "$.name"},
	})

	rs := query(t, s, spec, 10)
	if len(rs.Rows) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(rs.Rows), rs.Rows)
	}
	if got := rs.Rows[2]["$.name"]; got != "c" {
		t.Errorf("row 2 name = %v, want c", got)
	}
	if got := rs.Rows[2]["$.size"]; got != int64(3) {
		t.Errorf("row 2 size = %v, want 3", got)
	}
}

// A row selector that matches scalars yields rows whose "$" is the scalar itself.
func TestScalarRows(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"samples":[1,2,3]}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{Rows: "$.samples[*]", Value: "$"})

	rs := query(t, s, spec, 10)
	want := []source.Row{{"$": int64(1)}, {"$": int64(2)}, {"$": int64(3)}}
	if !reflect.DeepEqual(rs.Rows, want) {
		t.Fatalf("rows = %+v, want %+v", rs.Rows, want)
	}
}

// An empty array is a valid result with no series, not an error.
func TestEmptyRowSelection(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"workers":[]}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{Rows: "$.workers[*]", Value: "$.active_tasks"})

	rs := query(t, s, spec, 10)
	if len(rs.Rows) != 0 {
		t.Fatalf("rows = %+v, want none", rs.Rows)
	}
	if len(rs.Columns) != 1 || rs.Columns[0].Type != "unknown" {
		t.Fatalf("columns = %+v, want one column of unknown type", rs.Columns)
	}
}

// §20.4.4 — a missing field omits the key so the mapper reports a missing field.
func TestMissingFieldOmitsKey(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"workers":[{"name":"a","active_tasks":1},{"name":"b"}]}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{
		Rows:   "$.workers[*]",
		Value:  "$.active_tasks",
		Labels: map[string]string{"worker": "$.name"},
	})

	rs := query(t, s, spec, 10)
	if _, ok := rs.Rows[1]["$.active_tasks"]; ok {
		t.Fatalf("absent path produced a key: %+v", rs.Rows[1])
	}
	if v, ok := rs.Rows[0]["$.active_tasks"]; !ok || v != int64(1) {
		t.Fatalf("row 0 value = %v present=%v, want 1", v, ok)
	}
}

// §20.4.5 — a present null keeps the key with a nil value, which the null policy
// then governs. Absent and null must stay distinguishable (§12.4, ADR 0008).
func TestNullFieldKeepsKey(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"workers":[{"name":"a","active_tasks":null},{"name":"b"}]}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{
		Rows:   "$.workers[*]",
		Value:  "$.active_tasks",
		Labels: map[string]string{"worker": "$.name"},
	})

	rs := query(t, s, spec, 10)
	v, ok := rs.Rows[0]["$.active_tasks"]
	if !ok {
		t.Fatal("null value must be present as a key")
	}
	if v != nil {
		t.Fatalf("null value = %#v, want nil", v)
	}
	if _, ok := rs.Rows[1]["$.active_tasks"]; ok {
		t.Fatal("absent value must not be present as a key")
	}
}

// §20.4.6 — invalid numbers never reach the mapper as a fabricated sample (AD-010).
func TestInvalidNumberRejected(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ body, want string }{
		"overflow to +Inf": {`{"v":1e400}`, "non-finite"},
		"overflow to -Inf": {`{"v":-1e400}`, "non-finite"},
		"object":           {`{"v":{"a":1}}`, "scalar"},
		"array":            {`{"v":[1]}`, "scalar"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := jsonServer(t, tc.body)
			s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
			spec := compile(t, s, source.QueryDefinition{Value: "$.v"})

			err := queryErr(t, s, spec, 10)
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "app_metric") || !strings.Contains(err.Error(), "appapi") {
				t.Errorf("error %v must name the metric and source (§12.3)", err)
			}
		})
	}
}

// Large integers stay exact; a fractional number becomes a float.
func TestNumericConversion(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"i":9007199254740993,"f":1.5,"b":true,"s":"running"}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})

	for sel, want := range map[string]any{
		"$.i": int64(9007199254740993),
		"$.f": 1.5,
		"$.b": true,
		"$.s": "running",
	} {
		spec := compile(t, s, source.QueryDefinition{Value: sel})
		rs := query(t, s, spec, 10)
		if got := rs.Rows[0][sel]; got != want {
			t.Errorf("%s = %#v, want %#v", sel, got, want)
		}
	}
}

// An ambiguous scalar selector is an error: picking a match would make the sample
// depend on JSON member ordering.
func TestAmbiguousValueRejected(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"workers":[{"n":1},{"n":2}]}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{Value: "$.workers[*].n"})

	if err := queryErr(t, s, spec, 10); !strings.Contains(err.Error(), "multiple values") {
		t.Fatalf("error = %v, want an ambiguity report", err)
	}
}

// §8.7 — MaxRows errors rather than publishing a truncated series set.
func TestMaxRowsErrorsNeverTruncates(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"workers":[{"v":1},{"v":2},{"v":3}]}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{Rows: "$.workers[*]", Value: "$.v"})

	err := queryErr(t, s, spec, 2)
	if !strings.Contains(err.Error(), "exceeding max_rows_per_query 2") {
		t.Fatalf("error = %v, want a max_rows report", err)
	}
	if rs := query(t, s, spec, 3); len(rs.Rows) != 3 {
		t.Fatalf("rows at the limit = %d, want 3", len(rs.Rows))
	}
}

// Two labels sharing a selector collapse into one column.
func TestSharedSelectorYieldsOneColumn(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"name":"a","v":1}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{
		Value:  "$.v",
		Labels: map[string]string{"worker": "$.name", "host": "$.name"},
	})

	rs := query(t, s, spec, 10)
	want := []source.Column{{Name: "$.v", Type: "number"}, {Name: "$.name", Type: "string"}}
	if !reflect.DeepEqual(rs.Columns, want) {
		t.Fatalf("columns = %+v, want %+v", rs.Columns, want)
	}
}

// A column whose rows disagree reports "mixed"; Column.Type is diagnostic only.
func TestMixedColumnType(t *testing.T) {
	t.Parallel()

	srv := jsonServer(t, `{"r":[{"v":1},{"v":"two"},{"v":null}]}`)
	s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
	spec := compile(t, s, source.QueryDefinition{Rows: "$.r[*]", Value: "$.v"})

	rs := query(t, s, spec, 10)
	if rs.Columns[0].Type != "mixed" {
		t.Fatalf("type = %q, want mixed", rs.Columns[0].Type)
	}
}

func TestMalformedJSONRejected(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"truncated":     `{"v":`,
		"two documents": `{"v":1}{"v":2}`,
		"not json":      `<html>nope</html>`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := jsonServer(t, body)
			s := newSource(t, source.HTTPSettings{BaseURL: srv.URL})
			spec := compile(t, s, source.QueryDefinition{Value: "$.v"})
			_ = queryErr(t, s, spec, 10)
		})
	}
}
