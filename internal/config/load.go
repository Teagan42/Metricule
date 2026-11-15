package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	yaml "go.yaml.in/yaml/v3"

	"github.com/teagan42/metricule/internal/source"
)

// Load reads, defaults, and validates configuration from files or directories.
//
// Several files merge into one document; see resolveFiles and merge for the rules.
// Returns diagnostics even when err is nil, so callers can surface warnings.
func Load(paths ...string) (*Config, Diagnostics, error) {
	files, err := resolveFiles(paths)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 1 {
		data, readErr := readConfigFile(files[0])
		if readErr != nil {
			return nil, nil, readErr
		}
		return Parse(data, source.CapabilitiesFor)
	}

	frags := make([]fragment, 0, len(files))
	for _, f := range files {
		data, readErr := readConfigFile(f)
		if readErr != nil {
			return nil, nil, readErr
		}
		cfg, decodeErr := decode(data)
		if decodeErr != nil {
			return nil, nil, fmt.Errorf("config %q: %w", f, decodeErr)
		}
		frags = append(frags, fragment{file: f, cfg: cfg})
	}
	cfg, ds := merge(frags)
	if mergeErr := ds.Err(); mergeErr != nil {
		return nil, ds, mergeErr
	}
	return finish(cfg, source.CapabilitiesFor)
}

func readConfigFile(path string) ([]byte, error) {
	// The operator chooses this path on the command line; reading it is the point.
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return data, nil
}

// Parse decodes and validates configuration bytes.
//
// Pass nil caps to skip adapter capability checks.
func Parse(data []byte, caps CapabilityFunc) (*Config, Diagnostics, error) {
	cfg, err := decode(data)
	if err != nil {
		return nil, nil, err
	}
	return finish(cfg, caps)
}

// Defaults and validates a decoded document.
func finish(cfg *Config, caps CapabilityFunc) (*Config, Diagnostics, error) {
	ApplyDefaults(cfg)
	ds := Validate(cfg, caps)
	if err := ds.Err(); err != nil {
		return nil, ds, err
	}
	return cfg, ds, nil
}

// Decodes YAML with unknown-key rejection (§5).
func decode(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("configuration is empty")
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// A second pass records which paths the document stated. yaml.Node.Decode
	// ignores KnownFields, so the strict decode above cannot also yield the node
	// tree; parsing twice at startup is cheaper than losing unknown-key rejection.
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.present = collectPresent(&root)

	// A second document would be silently ignored, hiding half the configuration.
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("configuration contains more than one YAML document")
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// Redacted returns the configuration as YAML with secrets replaced by their
// reference form. Safe to log or print (§14.3).
func Redacted(c *Config) ([]byte, error) {
	return yaml.Marshal(c)
}
