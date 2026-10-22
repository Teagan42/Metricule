// Package secrets resolves indirect credential references from the environment or disk.
//
// Resolved values are never returned by String, MarshalYAML, or MarshalJSON. Config
// dumps and error messages therefore cannot leak credentials by accident.
package secrets

import (
	"fmt"
	"os"
	"strings"
)

// Ref is an indirect reference to a secret value.
//
// Exactly one of Value, Env, or File must be set. Value is intended for development
// only; production deployments should use Env or File.
type Ref struct {
	Value string `yaml:"value,omitempty" json:"value,omitempty"`
	Env   string `yaml:"env,omitempty" json:"env,omitempty"`
	File  string `yaml:"file,omitempty" json:"file,omitempty"`
}

// Redacted replaces any secret in logs, errors, and config dumps.
const Redacted = "[REDACTED]"

// String reports the reference kind, never the secret.
func (r Ref) String() string {
	switch {
	case r.Env != "":
		return "env:" + r.Env
	case r.File != "":
		return "file:" + r.File
	case r.Value != "":
		return "literal:" + Redacted
	default:
		return "unset"
	}
}

// MarshalYAML prevents credential disclosure through config serialization.
func (r Ref) MarshalYAML() (any, error) { return r.String(), nil }

// MarshalJSON prevents credential disclosure through API and diagnostic output.
func (r Ref) MarshalJSON() ([]byte, error) { return []byte(`"` + r.String() + `"`), nil }

// IsZero reports whether no source is configured.
func (r Ref) IsZero() bool { return r.Value == "" && r.Env == "" && r.File == "" }

// Validate checks that exactly one source is configured.
//
// Does not resolve, so offline validation stays side-effect free.
func (r Ref) Validate() error {
	n := 0
	for _, set := range []bool{r.Value != "", r.Env != "", r.File != ""} {
		if set {
			n++
		}
	}
	switch n {
	case 1:
		return nil
	case 0:
		return fmt.Errorf("secret reference is empty: set one of value, env, or file")
	default:
		return fmt.Errorf("secret reference is ambiguous: set only one of value, env, or file")
	}
}

// Resolve reads the referenced secret.
//
// Error messages name the reference, never its contents.
func (r Ref) Resolve() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	switch {
	case r.Value != "":
		return r.Value, nil
	case r.Env != "":
		v, ok := os.LookupEnv(r.Env)
		if !ok {
			return "", fmt.Errorf("environment variable %q is not set", r.Env)
		}
		if v == "" {
			return "", fmt.Errorf("environment variable %q is empty", r.Env)
		}
		return v, nil
	default:
		b, err := os.ReadFile(r.File)
		if err != nil {
			return "", fmt.Errorf("read secret file %q: %w", r.File, err)
		}
		// Docker secrets and kubectl both append a trailing newline.
		v := strings.TrimRight(string(b), "\r\n")
		if v == "" {
			return "", fmt.Errorf("secret file %q is empty", r.File)
		}
		return v, nil
	}
}
