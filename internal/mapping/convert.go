package mapping

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Conversion failures never quote the offending value: a result field may hold
// sensitive application data (§12.3).
var (
	errNotNumeric  = errors.New("value is not numeric")
	errNotFinite   = errors.New("numeric value is not finite")
	errNotScalar   = errors.New("value is not a scalar")
	errNumString   = errors.New("string value is not a valid number")
	errNullValue   = errors.New("value is null")
	errUnsupported = errors.New("unsupported value type")
)

// Converts a result field into a sample value (§12.2).
func toFloat(v any) (float64, error) {
	switch t := v.(type) {
	case nil:
		return 0, errNullValue
	case float64:
		return finite(t)
	case float32:
		return finite(float64(t))
	case int:
		return float64(t), nil
	case int8:
		return float64(t), nil
	case int16:
		return float64(t), nil
	case int32:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case uint:
		return float64(t), nil
	case uint8:
		return float64(t), nil
	case uint16:
		return float64(t), nil
	case uint32:
		return float64(t), nil
	case uint64:
		return float64(t), nil
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	case string:
		return parseNumber(t)
	case []byte:
		// Database drivers return NUMERIC and TEXT alike as bytes.
		return parseNumber(string(t))
	case json.Number:
		return parseNumber(t.String())
	default:
		return 0, fmt.Errorf("%w: %T", errUnsupported, v)
	}
}

// Coerces a numeric string. Explicit per §12.2: the string form is accepted, its
// content is not guessed at.
func parseNumber(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, errNumString
	}
	return finite(f)
}

// Rejects NaN and infinities, which have no valid exposition form (§11.3).
func finite(f float64) (float64, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errNotFinite
	}
	return f, nil
}

// Converts a result field into a label value.
func toLabelValue(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", errNullValue
	case string:
		return t, nil
	case []byte:
		return string(t), nil
	case bool:
		return strconv.FormatBool(t), nil
	case int:
		return strconv.FormatInt(int64(t), 10), nil
	case int8:
		return strconv.FormatInt(int64(t), 10), nil
	case int16:
		return strconv.FormatInt(int64(t), 10), nil
	case int32:
		return strconv.FormatInt(int64(t), 10), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case uint:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(t), 10), nil
	case uint64:
		return strconv.FormatUint(t, 10), nil
	case float32:
		return formatFloat(float64(t))
	case float64:
		return formatFloat(t)
	case json.Number:
		return t.String(), nil
	default:
		return "", fmt.Errorf("%w: %T", errNotScalar, v)
	}
}

func formatFloat(f float64) (string, error) {
	if _, err := finite(f); err != nil {
		return "", err
	}
	return strconv.FormatFloat(f, 'g', -1, 64), nil
}
