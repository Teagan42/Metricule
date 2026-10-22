package postgres

import (
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/teagan42/metricule/internal/source"
)

func TestNormalizeValueWidensNumbers(t *testing.T) {
	cases := map[string]struct {
		in   any
		want any
	}{
		"nil":          {nil, nil},
		"bool":         {true, true},
		"int2":         {int16(7), int64(7)},
		"int4":         {int32(-7), int64(-7)},
		"int8":         {int64(1 << 40), int64(1 << 40)},
		"int":          {42, int64(42)},
		"oid":          {uint32(16), int64(16)},
		"float4":       {float32(1.5), float64(1.5)},
		"float8":       {2.5, 2.5},
		"text":         {"queued", "queued"},
		"bytea":        {[]byte{1, 2}, []byte{1, 2}},
		"huge uint":    {uint64(math.MaxUint64), float64(math.MaxUint64)},
		"uint in int":  {uint64(5), int64(5)},
		"jsonb object": {map[string]any{"a": 1.0}, map[string]any{"a": 1.0}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeValue(tc.in)
			if err != nil {
				t.Fatalf("normalizeValue(%v): %v", tc.in, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("normalizeValue(%#v) = %#v (%T), want %#v (%T)", tc.in, got, got, tc.want, tc.want)
			}
		})
	}
}

func TestNormalizeValueKeepsTimestamps(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	got, err := normalizeValue(at)
	if err != nil {
		t.Fatalf("normalizeValue: %v", err)
	}
	if !reflect.DeepEqual(got, at) {
		t.Errorf("normalizeValue(time) = %#v, want the same time", got)
	}
}

func TestNormalizeNumeric(t *testing.T) {
	cases := map[string]struct {
		in   pgtype.Numeric
		want any
	}{
		"null":     {pgtype.Numeric{}, nil},
		"integral": {pgtype.Numeric{Int: big.NewInt(1234), Exp: 0, Valid: true}, 1234.0},
		"scaled":   {pgtype.Numeric{Int: big.NewInt(1234), Exp: -2, Valid: true}, 12.34},
		"infinity": {pgtype.Numeric{InfinityModifier: pgtype.Infinity, Valid: true}, math.Inf(1)},
		"-infinity": {
			pgtype.Numeric{InfinityModifier: pgtype.NegativeInfinity, Valid: true}, math.Inf(-1),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := normalizeValue(tc.in)
			if err != nil {
				t.Fatalf("normalizeValue: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("normalizeValue(%+v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeNumericNaN(t *testing.T) {
	got, err := normalizeValue(pgtype.Numeric{NaN: true, Valid: true})
	if err != nil {
		t.Fatalf("normalizeValue: %v", err)
	}
	f, ok := got.(float64)
	if !ok || !math.IsNaN(f) {
		t.Errorf("normalizeValue(NaN) = %#v, want NaN", got)
	}
}

func TestNormalizeUUID(t *testing.T) {
	var raw [16]byte
	copy(raw[:], []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef})
	got, err := normalizeValue(raw)
	if err != nil {
		t.Fatalf("normalizeValue: %v", err)
	}
	const want = "01234567-89ab-cdef-0123-456789abcdef"
	if got != want {
		t.Errorf("normalizeValue(uuid) = %v, want %s", got, want)
	}
}

func TestBuildRow(t *testing.T) {
	columns := []source.Column{{Name: "value", Type: "int8"}, {Name: "state", Type: "text"}}
	row, err := buildRow(columns, []any{int64(3), "queued"})
	if err != nil {
		t.Fatalf("buildRow: %v", err)
	}
	want := source.Row{"value": int64(3), "state": "queued"}
	if !reflect.DeepEqual(row, want) {
		t.Errorf("buildRow = %#v, want %#v", row, want)
	}
}

// A map keeps one value and drops the other, so the sample would depend on order.
func TestBuildRowRejectsDuplicateColumns(t *testing.T) {
	columns := []source.Column{{Name: "value"}, {Name: "value"}}
	_, err := buildRow(columns, []any{int64(1), int64(2)})
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("buildRow with duplicate columns = %v", err)
	}
}

func TestBuildRowRejectsArityMismatch(t *testing.T) {
	_, err := buildRow([]source.Column{{Name: "value"}}, []any{int64(1), int64(2)})
	if err == nil || !strings.Contains(err.Error(), "columns") {
		t.Fatalf("buildRow with mismatched arity = %v", err)
	}
}

func TestColumnsOfResolvesTypeNames(t *testing.T) {
	got := columnsOf(nil, []pgconn.FieldDescription{
		{Name: "value", DataTypeOID: pgtype.Int8OID},
		{Name: "label", DataTypeOID: pgtype.TextOID},
		{Name: "odd", DataTypeOID: 1 << 30},
	})
	want := []source.Column{
		{Name: "value", Type: "int8"},
		{Name: "label", Type: "text"},
		{Name: "odd", Type: "oid:1073741824"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("columnsOf = %#v, want %#v", got, want)
	}
}
