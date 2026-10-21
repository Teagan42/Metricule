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

// Load reads, defaults, and validates a configuration file.
//
// Returns diagnostics even when err is nil, so callers can surface warnings.
func Load(path string) (*Config, Diagnostics, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return Parse(data, source.CapabilitiesFor)
}

// Parse decodes and validates configuration bytes.
//
// Pass nil caps to skip adapter capability checks.
func Parse(data []byte, caps CapabilityFunc) (*Config, Diagnostics, error) {
	cfg, err := decode(data)
	if err != nil {
		return nil, nil, err
	}
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
