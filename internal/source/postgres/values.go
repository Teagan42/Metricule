package postgres

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/teagan42/metricule/internal/source"
)

// columnsOf names result columns and resolves their PostgreSQL type names.
//
// The type name is diagnostic only; conversion is driven by the decoded Go value.
func columnsOf(conn *pgx.Conn, fields []pgconn.FieldDescription) []source.Column {
	types := pgtype.NewMap()
	if conn != nil {
		types = conn.TypeMap()
	}
	out := make([]source.Column, len(fields))
	for i, f := range fields {
		out[i] = source.Column{Name: f.Name, Type: typeName(types, f.DataTypeOID)}
	}
	return out
}

func typeName(types *pgtype.Map, oid uint32) string {
	if t, ok := types.TypeForOID(oid); ok {
		return t.Name
	}
	return "oid:" + strconv.FormatUint(uint64(oid), 10)
}

// buildRow pairs column names with normalized values.
//
// Rejects duplicate column names: a map would keep one value and drop the other,
// making the sample depend on column order.
func buildRow(columns []source.Column, values []any) (source.Row, error) {
	if len(columns) != len(values) {
		return nil, fmt.Errorf("result has %d columns but %d values", len(columns), len(values))
	}
	row := make(source.Row, len(columns))
	for i, c := range columns {
		if _, dup := row[c.Name]; dup {
			return nil, fmt.Errorf("column %q appears more than once; alias it in the query", c.Name)
		}
		v, err := normalizeValue(values[i])
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", c.Name, err)
		}
		row[c.Name] = v
	}
	return row, nil
}

// normalizeValue maps a pgx value onto the ResultSet value set.
//
// Integers widen to int64 and all floats to float64 because a Prometheus sample is
// a float64 regardless; numeric decodes to float64 for the same reason, with NaN and
// infinity preserved rather than flattened.
//
// Types with no scalar equivalent — jsonb documents, arrays, intervals — pass
// through as pgx decoded them. Guessing a scalar for them would be lossy, and a
// jsonb document in decoded form is exactly what a JSON selector consumes.
func normalizeValue(v any) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case bool, string, []byte, int64, float64, time.Time:
		return t, nil
	case int:
		return int64(t), nil
	case int8:
		return int64(t), nil
	case int16:
		return int64(t), nil
	case int32:
		return int64(t), nil
	case uint8:
		return int64(t), nil
	case uint16:
		return int64(t), nil
	case uint32:
		return int64(t), nil
	case uint:
		return unsignedToNumber(uint64(t)), nil
	case uint64:
		return unsignedToNumber(t), nil
	case float32:
		return float64(t), nil
	case pgtype.Numeric:
		return numericValue(t)
	case [16]byte:
		return formatUUID(t), nil
	default:
		return v, nil
	}
}

// Keeps magnitude when a uint64 cannot be an int64.
func unsignedToNumber(v uint64) any {
	if v > math.MaxInt64 {
		return float64(v)
	}
	return int64(v)
}

func numericValue(n pgtype.Numeric) (any, error) {
	if !n.Valid {
		return nil, nil
	}
	if n.NaN {
		return math.NaN(), nil
	}
	switch n.InfinityModifier {
	case pgtype.Infinity:
		return math.Inf(1), nil
	case pgtype.NegativeInfinity:
		return math.Inf(-1), nil
	case pgtype.Finite:
	}
	f, err := n.Float64Value()
	if err != nil {
		return nil, fmt.Errorf("numeric is not representable as a float: %w", err)
	}
	if !f.Valid {
		return nil, nil
	}
	return f.Float64, nil
}

// formatUUID renders pgx's decoded uuid bytes in canonical form.
func formatUUID(b [16]byte) string {
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
