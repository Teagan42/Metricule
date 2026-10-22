package config

import (
	"fmt"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Duration is a time.Duration that accepts Go duration strings in YAML.
//
// A distinct type is required because YAML has no duration scalar and decoding an
// integer into time.Duration would silently mean nanoseconds.
type Duration time.Duration

// UnmarshalYAML parses a duration string such as "30s" or "5m".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("line %d: duration must be a string such as \"30s\": %w", n.Line, err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q: %w", n.Line, s, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML renders the duration in its string form.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// Duration converts to the standard library type.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

// IsZero reports whether the field was unset.
func (d Duration) IsZero() bool { return d == 0 }
