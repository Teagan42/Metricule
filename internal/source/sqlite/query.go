package sqlite

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/teagan42/metricule/internal/source"
)

// spec is a compiled SQLite statement, opaque outside this package.
type spec struct {
	metric string
	sql    string
}

// Describe returns a diagnostic description. Carries no credentials or row data.
func (s *spec) Describe() string {
	return fmt.Sprintf("sqlite metric %q: %s", s.metric, s.sql)
}

// CompileQuery validates a metric's query at configuration time.
//
// The statement is not inspected for mutation: §14.5 states that cannot be done
// securely by string matching. mode=ro enforces it instead.
func (s *Source) CompileQuery(def source.QueryDefinition) (source.QuerySpec, error) {
	if def.Method != "" || def.Path != "" || len(def.Params) > 0 || len(def.Headers) > 0 {
		return nil, fmt.Errorf("source %q: metric %q: method, path, params and headers are HTTP fields and are not valid on a sqlite source", s.id, def.MetricName)
	}
	stmt := strings.TrimSpace(def.SQL)
	if stmt == "" {
		return nil, fmt.Errorf("source %q: metric %q: sql is required", s.id, def.MetricName)
	}
	return &spec{metric: def.MetricName, sql: stmt}, nil
}

// Normalizes rows into a ResultSet, failing rather than truncating at maxRows.
//
// maxRows of zero or less means no bound was configured.
func scanRows(rows *sql.Rows, maxRows int) (source.ResultSet, error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		return source.ResultSet{}, fmt.Errorf("describe columns: %w", err)
	}

	cols := make([]source.Column, len(types))
	seen := make(map[string]struct{}, len(types))
	for i, ct := range types {
		name := ct.Name()
		// Row is a map, so a repeated name would drop a column silently.
		if _, dup := seen[name]; dup {
			return source.ResultSet{}, fmt.Errorf("duplicate result column %q: give it an alias", name)
		}
		seen[name] = struct{}{}
		cols[i] = source.Column{Name: name, Type: ct.DatabaseTypeName()}
	}

	out := source.ResultSet{Columns: cols}
	values := make([]any, len(cols))
	targets := make([]any, len(cols))
	for i := range values {
		targets[i] = &values[i]
	}

	for rows.Next() {
		if maxRows > 0 && len(out.Rows) >= maxRows {
			return source.ResultSet{}, fmt.Errorf("%w of %d", ErrTooManyRows, maxRows)
		}
		if err := rows.Scan(targets...); err != nil {
			return source.ResultSet{}, fmt.Errorf("scan row %d: %w", len(out.Rows)+1, err)
		}
		row := make(source.Row, len(cols))
		for i, c := range cols {
			row[c.Name] = values[i]
		}
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return source.ResultSet{}, fmt.Errorf("iterate rows: %w", err)
	}
	return out, nil
}
