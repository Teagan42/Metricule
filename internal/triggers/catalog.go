package triggers

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// querier is satisfied by *pgx.Conn and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type functionInfo struct {
	exists  bool
	owned   bool
	comment string
	oid     uint32
}

type triggerInfo struct {
	exists  bool
	owned   bool
	comment string
	hash    string
}

type managedTrigger struct {
	schema string
	table  string
	name   string
	owned  bool
}

func (m managedTrigger) qualified() string { return m.schema + "." + m.table }

func schemaPresent(ctx context.Context, q querier, schema string) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, schema).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("inspect schema %q: %w", schema, err)
	}
	return exists, nil
}

func relationKind(ctx context.Context, q querier, schema, table string) (string, error) {
	var kind string
	err := q.QueryRow(ctx, `
		SELECT c.relkind::text
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`, schema, table).Scan(&kind)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("inspect relation %s.%s: %w", schema, table, err)
	}
	return kind, nil
}

func inspectFunction(ctx context.Context, q querier, schema string) (functionInfo, error) {
	var (
		info    functionInfo
		comment *string
	)
	err := q.QueryRow(ctx, `
		SELECT p.oid, obj_description(p.oid, 'pg_proc')
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND p.proname = $2 AND p.pronargs = 0
		LIMIT 1`, schema, FunctionName).Scan(&info.oid, &comment)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return info, nil
	case err != nil:
		return info, fmt.Errorf("inspect function %s.%s(): %w", schema, FunctionName, err)
	}
	info.exists = true
	if comment != nil {
		info.comment = *comment
	}
	info.owned = owned(info.comment)
	return info, nil
}

func inspectTrigger(ctx context.Context, q querier, schema, table, name string) (triggerInfo, error) {
	var (
		info    triggerInfo
		comment *string
	)
	err := q.QueryRow(ctx, `
		SELECT obj_description(t.oid, 'pg_trigger')
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND t.tgname = $3 AND NOT t.tgisinternal
		LIMIT 1`, schema, table, name).Scan(&comment)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return info, nil
	case err != nil:
		return info, fmt.Errorf("inspect trigger %s on %s.%s: %w", name, schema, table, err)
	}
	info.exists = true
	if comment != nil {
		info.comment = *comment
	}
	info.owned = owned(info.comment)
	info.hash = markerHash(info.comment)
	return info, nil
}

// managedTriggers lists every trigger attached to the managed function.
func managedTriggers(ctx context.Context, q querier, functionOID uint32) ([]managedTrigger, error) {
	rows, err := q.Query(ctx, `
		SELECT n.nspname, c.relname, t.tgname, obj_description(t.oid, 'pg_trigger')
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE t.tgfoid = $1 AND NOT t.tgisinternal
		ORDER BY n.nspname, c.relname, t.tgname`, functionOID)
	if err != nil {
		return nil, fmt.Errorf("list managed triggers: %w", err)
	}
	defer rows.Close()

	var out []managedTrigger
	for rows.Next() {
		var (
			tr      managedTrigger
			comment *string
		)
		if err := rows.Scan(&tr.schema, &tr.table, &tr.name, &comment); err != nil {
			return nil, fmt.Errorf("list managed triggers: %w", err)
		}
		if comment != nil {
			tr.owned = owned(*comment)
		}
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list managed triggers: %w", err)
	}
	return out, nil
}
