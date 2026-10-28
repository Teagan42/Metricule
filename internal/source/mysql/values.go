package mysql

import (
	"fmt"
	"math"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

// buildRow pairs column names with driver values.
//
// Rejects duplicate column names: a map would keep one and drop the other, making
// the sample depend on column order.
func buildRow(columns []source.Column, values []any) (source.Row, error) {
	if len(columns) != len(values) {
		return nil, fmt.Errorf("result has %d columns but %d values", len(columns), len(values))
	}
	row := make(source.Row, len(columns))
	for i, c := range columns {
		if _, dup := row[c.Name]; dup {
			return nil, fmt.Errorf("column %q appears more than once; alias it in the query", c.Name)
		}
		row[c.Name] = normalizeValue(values[i])
	}
	return row, nil
}

// normalizeValue maps a driver value onto the ResultSet value set.
//
// The text protocol returns []byte for most columns; that is preserved so a
// DECIMAL keeps its exact text until the mapping engine parses it for a sample or
// renders it for a label.
func normalizeValue(v any) any {
	switch t := v.(type) {
	case nil, bool, string, []byte, int64, float64, time.Time:
		return t
	case int:
		return int64(t)
	case int8:
		return int64(t)
	case int16:
		return int64(t)
	case int32:
		return int64(t)
	case uint8:
		return int64(t)
	case uint16:
		return int64(t)
	case uint32:
		return int64(t)
	case uint:
		return unsignedToNumber(uint64(t))
	case uint64:
		return unsignedToNumber(t)
	case float32:
		return float64(t)
	default:
		return v
	}
}

func unsignedToNumber(v uint64) any {
	if v > math.MaxInt64 {
		return float64(v)
	}
	return int64(v)
}
