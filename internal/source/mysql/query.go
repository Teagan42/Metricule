package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

const (
	describeLimit = 200
	initialRows   = 64
)

// Compiled query. Opaque outside this package.
type spec struct {
	metric string
	sql    string
}

// Describe renders a credential-free, row-free summary for diagnostics.
func (s *spec) Describe() string {
	query := strings.Join(strings.Fields(s.sql), " ")
	if len(query) > describeLimit {
		query = query[:describeLimit] + "..."
	}
	return fmt.Sprintf("mysql read-only query for %s: %s", s.metric, query)
}

// CompileQuery validates a query definition at configuration time.
func (s *Source) CompileQuery(def source.QueryDefinition) (source.QuerySpec, error) {
	query := strings.TrimSpace(def.SQL)
	if query == "" {
		return nil, newError("compile", telemetry.ReasonQuery,
			fmt.Errorf("metric %q on source %q: query.sql is required for a mysql source", def.MetricName, s.id))
	}
	if unsupported := httpOnlyFields(def); len(unsupported) > 0 {
		return nil, newError("compile", telemetry.ReasonQuery,
			fmt.Errorf("metric %q on source %q: mysql sources accept query.sql only, not %s",
				def.MetricName, s.id, strings.Join(unsupported, ", ")))
	}
	return &spec{metric: def.MetricName, sql: query}, nil
}

func httpOnlyFields(def source.QueryDefinition) []string {
	var out []string
	if def.Method != "" {
		out = append(out, "query.method")
	}
	if def.Path != "" {
		out = append(out, "query.path")
	}
	if len(def.Params) > 0 {
		out = append(out, "query.params")
	}
	if len(def.Headers) > 0 {
		out = append(out, "query.headers")
	}
	return out
}

// Query executes a compiled spec inside a read-only transaction.
func (s *Source) Query(ctx context.Context, req source.QueryRequest) (source.ResultSet, error) {
	compiled, ok := req.Spec.(*spec)
	if !ok {
		return source.ResultSet{}, newError("query", telemetry.ReasonQuery,
			fmt.Errorf("source %q: query spec %T was not compiled by the mysql adapter", s.id, req.Spec))
	}
	if req.MaxRows <= 0 {
		return source.ResultSet{}, newError("query", telemetry.ReasonQuery,
			fmt.Errorf("metric %q on source %q: max_rows must be greater than zero, got %d",
				compiled.metric, s.id, req.MaxRows))
	}
	db, err := s.handle()
	if err != nil {
		return source.ResultSet{}, newError("query", telemetry.ReasonUnavailable, err)
	}

	ctx, cancel := context.WithTimeout(ctx, s.settings.QueryTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return source.ResultSet{}, s.queryError(compiled, "begin read-only transaction", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			s.log.Debug("mysql rollback failed", "target", s.target, "error", rbErr)
		}
	}()

	rows, err := tx.QueryContext(ctx, compiled.sql)
	if err != nil {
		return source.ResultSet{}, s.queryError(compiled, "execute query", err)
	}
	defer func() { _ = rows.Close() }()

	columns, err := columnsOf(rows)
	if err != nil {
		return source.ResultSet{}, newError("query", telemetry.ReasonDecode,
			fmt.Errorf("metric %q on source %q: read columns: %w", compiled.metric, s.id, err))
	}

	result := source.ResultSet{Columns: columns, Rows: make([]source.Row, 0, min(req.MaxRows, initialRows))}
	for rows.Next() {
		if len(result.Rows) >= req.MaxRows {
			// Cancel before Close so the remaining rows are not drained.
			cancel()
			return source.ResultSet{}, newError("query", telemetry.ReasonResultTooLarge,
				fmt.Errorf("metric %q on source %q: more than %d rows: %w",
					compiled.metric, s.id, req.MaxRows, ErrResultTooLarge))
		}
		values, err := scanRow(rows, len(columns))
		if err != nil {
			return source.ResultSet{}, newError("query", telemetry.ReasonDecode,
				fmt.Errorf("metric %q on source %q: decode row %d: %w",
					compiled.metric, s.id, len(result.Rows)+1, err))
		}
		row, err := buildRow(columns, values)
		if err != nil {
			return source.ResultSet{}, newError("query", telemetry.ReasonDecode,
				fmt.Errorf("metric %q on source %q: row %d: %w", compiled.metric, s.id, len(result.Rows)+1, err))
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return source.ResultSet{}, s.queryError(compiled, "read result", err)
	}
	return result, nil
}

func columnsOf(rows *sql.Rows) ([]source.Column, error) {
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	out := make([]source.Column, len(names))
	for i, name := range names {
		typ := ""
		if i < len(types) {
			typ = types[i].DatabaseTypeName()
		}
		out[i] = source.Column{Name: name, Type: typ}
	}
	return out, nil
}

func scanRow(rows *sql.Rows, n int) ([]any, error) {
	dest := make([]any, n)
	for i := range dest {
		dest[i] = new(any)
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	values := make([]any, n)
	for i := range dest {
		values[i] = *dest[i].(*any)
	}
	return values, nil
}

// Wraps a driver failure with its classification and operation context.
func (s *Source) queryError(compiled *spec, op string, err error) error {
	if isReadOnly(err) {
		err = fmt.Errorf("%w: %w", ErrReadOnly, err)
	}
	return newError("query", classify(err),
		fmt.Errorf("metric %q on source %q: %s: %w", compiled.metric, s.id, op, err))
}
