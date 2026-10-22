package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/telemetry"
)

// Caps a compiled query's sanitized description so diagnostics stay readable.
const describeLimit = 200

// Bounds the initial row allocation so a large max_rows does not preallocate it.
const initialRowCapacity = 64

// Time allowed for ROLLBACK after the query context has already ended.
const rollbackGrace = 5 * time.Second

// readOnlySQLTransaction is SQLSTATE 25006, raised when a write reaches the
// read-only transaction that wraps every collection.
const readOnlySQLTransaction = "25006"

// Compiled query. Opaque outside this package.
type spec struct {
	metric string
	sql    string
}

// Describe renders a credential-free, row-free summary for diagnostics.
func (s *spec) Describe() string {
	sql := strings.Join(strings.Fields(s.sql), " ")
	if len(sql) > describeLimit {
		sql = sql[:describeLimit] + "..."
	}
	return fmt.Sprintf("postgres read-only query for %s: %s", s.metric, sql)
}

// CompileQuery validates a query definition at configuration time.
//
// Rejects fields belonging to other adapters so a mistyped source type fails at
// startup rather than at the first scrape.
func (s *Source) CompileQuery(def source.QueryDefinition) (source.QuerySpec, error) {
	sql := strings.TrimSpace(def.SQL)
	if sql == "" {
		return nil, newError("compile", telemetry.ReasonQuery,
			fmt.Errorf("metric %q on source %q: query.sql is required for a postgres source", def.MetricName, s.id))
	}
	if unsupported := httpOnlyFields(def); len(unsupported) > 0 {
		return nil, newError("compile", telemetry.ReasonQuery,
			fmt.Errorf("metric %q on source %q: postgres sources accept query.sql only, not %s",
				def.MetricName, s.id, strings.Join(unsupported, ", ")))
	}
	return &spec{metric: def.MetricName, sql: sql}, nil
}

// Lists populated query fields that only an HTTP source understands.
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
//
// Read-only enforcement is the transaction and the connection role, never SQL text
// inspection, which proves nothing (§14.5). Isolation is repeatable read so a
// multi-statement aggregate reads one snapshot.
func (s *Source) Query(ctx context.Context, req source.QueryRequest) (source.ResultSet, error) {
	compiled, ok := req.Spec.(*spec)
	if !ok {
		return source.ResultSet{}, newError("query", telemetry.ReasonQuery,
			fmt.Errorf("source %q: query spec %T was not compiled by the postgres adapter", s.id, req.Spec))
	}
	if req.MaxRows <= 0 {
		return source.ResultSet{}, newError("query", telemetry.ReasonQuery,
			fmt.Errorf("metric %q on source %q: max_rows must be greater than zero, got %d",
				compiled.metric, s.id, req.MaxRows))
	}
	pool, err := s.acquirePool()
	if err != nil {
		return source.ResultSet{}, newError("query", telemetry.ReasonUnavailable, err)
	}

	ctx, cancel := context.WithTimeout(ctx, s.settings.QueryTimeout)
	defer cancel()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return source.ResultSet{}, s.queryError(compiled, "begin read-only transaction", err)
	}
	defer func() {
		// The query context may already be dead; ROLLBACK still has to reach the
		// server or the pooled connection is discarded.
		rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackGrace)
		defer rollbackCancel()
		if rbErr := tx.Rollback(rollbackCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			s.log.Debug("postgres rollback failed", "target", s.target, "error", rbErr)
		}
	}()

	rows, err := tx.Query(ctx, compiled.sql)
	if err != nil {
		return source.ResultSet{}, s.queryError(compiled, "execute query", err)
	}
	defer rows.Close()

	result := source.ResultSet{Columns: columnsOf(tx.Conn(), rows.FieldDescriptions())}
	result.Rows = make([]source.Row, 0, min(req.MaxRows, initialRowCapacity))

	for rows.Next() {
		if len(result.Rows) >= req.MaxRows {
			// Cancel before Close: closing a live result set drains the remaining
			// rows off the connection, which is the cost this limit exists to avoid.
			cancel()
			return source.ResultSet{}, newError("query", telemetry.ReasonResultTooLarge,
				fmt.Errorf("metric %q on source %q: more than %d rows: %w",
					compiled.metric, s.id, req.MaxRows, ErrResultTooLarge))
		}
		values, valErr := rows.Values()
		if valErr != nil {
			return source.ResultSet{}, newError("query", telemetry.ReasonDecode,
				fmt.Errorf("metric %q on source %q: decode row %d: %w",
					compiled.metric, s.id, len(result.Rows)+1, valErr))
		}
		row, rowErr := buildRow(result.Columns, values)
		if rowErr != nil {
			return source.ResultSet{}, newError("query", telemetry.ReasonDecode,
				fmt.Errorf("metric %q on source %q: row %d: %w", compiled.metric, s.id, len(result.Rows)+1, rowErr))
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return source.ResultSet{}, s.queryError(compiled, "read result", err)
	}
	return result, nil
}

// Wraps a driver failure with its classification and operation context.
func (s *Source) queryError(compiled *spec, op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == readOnlySQLTransaction {
		err = fmt.Errorf("%w: %w", ErrReadOnly, err)
	}
	return newError("query", classify(err),
		fmt.Errorf("metric %q on source %q: %s: %w", compiled.metric, s.id, op, err))
}
