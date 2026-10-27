package triggers

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// TriggerName derives a stable, collision-resistant name for a table/channel pair.
//
// Hashed because channels may contain characters an identifier cannot carry and
// because the name must stay under PostgreSQL's 63-byte limit. Stable so repeated
// applies and restarts find the object instead of creating a new one.
func TriggerName(table, channel string) string {
	sum := sha256.Sum256([]byte(table + "\x00" + channel))
	return triggerPrefix + hex.EncodeToString(sum[:8])
}

// subscriptionHash changes when anything that affects the trigger definition
// changes, so apply rewrites the object only when it must.
func subscriptionHash(s Subscription) string {
	clause, _ := eventClause(s.Events)
	sum := sha256.Sum256([]byte(s.Table + "\x00" + s.Channel + "\x00" + clause))
	return hex.EncodeToString(sum[:8])
}

// splitTable splits a schema-qualified table and rejects ambiguous spellings.
func splitTable(qualified string) (schema, name string, err error) {
	parts := strings.Split(qualified, ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("watch table %q must be schema-qualified, such as public.jobs", qualified)
	}
	schema, name = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if schema == "" || name == "" {
		return "", "", fmt.Errorf("watch table %q must be schema-qualified, such as public.jobs", qualified)
	}
	return schema, name, nil
}

// eventClause builds the trigger event list, defaulting to all DML kinds.
//
// Fixed order keeps the generated SQL and the subscription hash deterministic.
func eventClause(events []string) (string, error) {
	if len(events) == 0 {
		return "INSERT OR UPDATE OR DELETE", nil
	}
	seen := map[string]bool{}
	for _, e := range events {
		kind := strings.ToLower(strings.TrimSpace(e))
		switch kind {
		case "insert", "update", "delete":
			seen[kind] = true
		default:
			return "", fmt.Errorf("unsupported trigger event %q (supported: insert, update, delete)", e)
		}
	}
	ordered := make([]string, 0, 3)
	for _, kind := range []string{"insert", "update", "delete"} {
		if seen[kind] {
			ordered = append(ordered, strings.ToUpper(kind))
		}
	}
	return strings.Join(ordered, " OR "), nil
}

func ident(parts ...string) string {
	return pgx.Identifier(parts).Sanitize()
}

// literal quotes a string for use as a SQL literal.
func literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func createSchemaSQL(schema string) string {
	return "CREATE SCHEMA IF NOT EXISTS " + ident(schema)
}

func createFunctionSQL(schema string) string {
	return "CREATE OR REPLACE FUNCTION " + ident(schema, FunctionName) + "() RETURNS trigger " +
		"LANGUAGE plpgsql AS $metricule$ BEGIN " +
		"PERFORM pg_notify(TG_ARGV[0], TG_TABLE_SCHEMA || '.' || TG_TABLE_NAME); " +
		"RETURN NULL; END; $metricule$"
}

func commentFunctionSQL(schema string) string {
	return "COMMENT ON FUNCTION " + ident(schema, FunctionName) + "() IS " + literal(functionMarker)
}

func createTriggerSQL(schema, tableSchema, table, name, events, channel string) string {
	return "CREATE TRIGGER " + ident(name) +
		" AFTER " + events + " ON " + ident(tableSchema, table) +
		" FOR EACH STATEMENT EXECUTE FUNCTION " + ident(schema, FunctionName) +
		"(" + literal(channel) + ")"
}

func commentTriggerSQL(tableSchema, table, name, hash string) string {
	return "COMMENT ON TRIGGER " + ident(name) + " ON " + ident(tableSchema, table) +
		" IS " + literal(triggerMarker(hash))
}

func dropTriggerSQL(tableSchema, table, name string) string {
	return "DROP TRIGGER " + ident(name) + " ON " + ident(tableSchema, table)
}

func dropFunctionSQL(schema string) string {
	return "DROP FUNCTION " + ident(schema, FunctionName) + "()"
}

func triggerMarker(hash string) string {
	return functionMarker + " hash=" + hash
}

// owned reports whether a comment carries this package's marker.
func owned(comment string) bool {
	return strings.HasPrefix(comment, functionMarker)
}

// markerHash extracts the subscription hash from a trigger comment.
func markerHash(comment string) string {
	const key = "hash="
	i := strings.Index(comment, key)
	if i < 0 {
		return ""
	}
	return comment[i+len(key):]
}
