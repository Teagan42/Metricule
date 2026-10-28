package config

import (
	"bytes"
	"fmt"

	yaml "go.yaml.in/yaml/v3"
)

// Decodes a node with unknown-key rejection.
//
// yaml.Node.Decode ignores the parent decoder's KnownFields setting, so the node is
// re-encoded and decoded through a strict decoder.
func strictDecodeNode(n *yaml.Node, out any) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(out)
}

// UnmarshalYAML decodes a source against the field set for its declared type.
//
// Two passes are required: the discriminator must be read before the type-specific
// fields can be validated, so that a postgres source rejects sqlite keys.
func (s *SourceConfig) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: source must be a mapping", n.Line)
	}

	var disc struct {
		Type SourceType `yaml:"type"`
	}
	if err := n.Decode(&disc); err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	if disc.Type == "" {
		return fmt.Errorf("line %d: source is missing required field \"type\"", n.Line)
	}
	s.Type = disc.Type

	switch disc.Type {
	case SourcePostgres:
		var doc struct {
			Type           SourceType `yaml:"type"`
			PostgresSource `yaml:",inline"`
		}
		if err := strictDecodeNode(n, &doc); err != nil {
			return fmt.Errorf("postgres source: %w", err)
		}
		s.Postgres = &doc.PostgresSource
	case SourceSQLite:
		var doc struct {
			Type         SourceType `yaml:"type"`
			SQLiteSource `yaml:",inline"`
		}
		if err := strictDecodeNode(n, &doc); err != nil {
			return fmt.Errorf("sqlite source: %w", err)
		}
		s.SQLite = &doc.SQLiteSource
	case SourceHTTP:
		var doc struct {
			Type       SourceType `yaml:"type"`
			HTTPSource `yaml:",inline"`
		}
		if err := strictDecodeNode(n, &doc); err != nil {
			return fmt.Errorf("http source: %w", err)
		}
		s.HTTP = &doc.HTTPSource
	case SourceMySQL:
		var doc struct {
			Type        SourceType `yaml:"type"`
			MySQLSource `yaml:",inline"`
		}
		if err := strictDecodeNode(n, &doc); err != nil {
			return fmt.Errorf("mysql source: %w", err)
		}
		s.MySQL = &doc.MySQLSource
	default:
		return fmt.Errorf("line %d: unsupported source type %q (supported: postgres, sqlite, http, mysql)", n.Line, disc.Type)
	}
	return nil
}

// MarshalYAML flattens the union back into its wire form.
func (s SourceConfig) MarshalYAML() (any, error) {
	switch s.Type {
	case SourcePostgres:
		return struct {
			Type SourceType `yaml:"type"`
			*PostgresSource
		}{s.Type, s.Postgres}, nil
	case SourceSQLite:
		return struct {
			Type SourceType `yaml:"type"`
			*SQLiteSource
		}{s.Type, s.SQLite}, nil
	case SourceHTTP:
		return struct {
			Type SourceType `yaml:"type"`
			*HTTPSource
		}{s.Type, s.HTTP}, nil
	case SourceMySQL:
		return struct {
			Type SourceType `yaml:"type"`
			*MySQLSource
		}{s.Type, s.MySQL}, nil
	}
	return nil, fmt.Errorf("unsupported source type %q", s.Type)
}

// UnmarshalYAML decodes a mapping block.
//
// Hand-rolled because the §5.1 contract spells the null-policy key as `null`, which
// YAML resolves to the null scalar instead of the string "null". Struct tags cannot
// express that key.
func (m *Mapping) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: mapping must be a mapping", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]

		key := k.Value
		if k.Tag == "!!null" {
			key = "null"
		}

		var err error
		switch key {
		case "rows":
			err = v.Decode(&m.Rows)
		case "value":
			err = v.Decode(&m.Value)
		case "buckets":
			err = v.Decode(&m.Buckets)
		case "count":
			err = v.Decode(&m.Count)
		case "sum":
			err = v.Decode(&m.Sum)
		case "labels":
			err = v.Decode(&m.Labels)
		case "static_labels":
			err = v.Decode(&m.StaticLabels)
		case "states":
			err = v.Decode(&m.States)
		case "scale":
			err = v.Decode(&m.Scale)
		case "null":
			err = v.Decode(&m.Null)
		default:
			return fmt.Errorf("line %d: unknown mapping field %q", k.Line, k.Value)
		}
		if err != nil {
			return fmt.Errorf("line %d: mapping field %q: %w", v.Line, key, err)
		}
	}
	return nil
}

// MarshalYAML renders the mapping, emitting the null policy under its `null` key.
func (m Mapping) MarshalYAML() (any, error) {
	out := map[string]any{}
	if m.Rows != "" {
		out["rows"] = m.Rows
	}
	if m.Value != "" {
		out["value"] = m.Value
	}
	if m.Buckets != "" {
		out["buckets"] = m.Buckets
	}
	if m.Count != "" {
		out["count"] = m.Count
	}
	if m.Sum != "" {
		out["sum"] = m.Sum
	}
	if len(m.Labels) > 0 {
		out["labels"] = m.Labels
	}
	if len(m.StaticLabels) > 0 {
		out["static_labels"] = m.StaticLabels
	}
	if len(m.States) > 0 {
		out["states"] = m.States
	}
	if m.Scale != 0 {
		out["scale"] = m.Scale
	}
	if m.Null != "" {
		out["null"] = string(m.Null)
	}
	return out, nil
}
