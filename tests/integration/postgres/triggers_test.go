//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/runtime"
	"github.com/teagan42/metricule/internal/triggers"
)

var triggerSeq atomic.Int64

// uniqueSuffix keeps every test's schema, table, and channel distinct on the
// shared server without relying on test ordering.
func uniqueSuffix() string {
	return fmt.Sprint(time.Now().UnixNano()%1_000_000_000 + triggerSeq.Add(1))
}

func openTriggers(t *testing.T, dsn string) *triggers.Manager {
	t.Helper()
	m, err := triggers.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("triggers.Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m
}

// listen opens a dedicated LISTEN session and returns it.
func listen(t *testing.T, dsn, channel string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("listen connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(context.Background(), "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		t.Fatalf("LISTEN: %v", err)
	}
	return conn
}

func awaitPayload(t *testing.T, conn *pgx.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("wait for notification: %v", err)
	}
	if n.Payload != want {
		t.Errorf("payload = %q, want %q", n.Payload, want)
	}
}

func awaitNoPayload(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	if n, err := conn.WaitForNotification(ctx); err == nil {
		t.Fatalf("unexpected notification on channel %q with payload %q", n.Channel, n.Payload)
	}
}

func objectExists(t *testing.T, dsn, sql string, args ...any) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("exists connect: %v", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx, sql, args...).Scan(&exists); err != nil {
		t.Fatalf("exists query: %v", err)
	}
	return exists
}

// End-to-end lifecycle: apply, verify notifications, status, apply again
// (idempotent), then remove.
func TestManagedTriggersLifecycle(t *testing.T) {
	dsn := sharedDSN(t, "triggers_lifecycle")
	u := uniqueSuffix()
	schema, table, channel := "mt_"+u, "jobs_"+u, "mt_ch_"+u
	qualified := "public." + table

	adminExec(t, dsn,
		`DROP TABLE IF EXISTS `+qualified+` CASCADE`,
		`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
		`CREATE TABLE `+qualified+` (id bigserial PRIMARY KEY, state text)`,
	)
	t.Cleanup(func() {
		adminExec(t, dsn,
			`DROP TABLE IF EXISTS `+qualified+` CASCADE`,
			`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
		)
	})

	target := triggers.Target{
		SourceID: "appdb", Schema: schema, Manage: true, DSN: dsn,
		Subscriptions: []triggers.Subscription{{Table: qualified, Channel: channel}},
	}

	m := openTriggers(t, dsn)
	report, err := m.Apply(context.Background(), target)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !report.Changed() {
		t.Fatal("first Apply reported no changes")
	}
	if len(report.Actions) != 3 {
		t.Errorf("actions = %d, want schema, function, trigger:\n%+v", len(report.Actions), report.Actions)
	}

	// Second apply is a no-op: no trigger churn on restart.
	again, err := m.Apply(context.Background(), target)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if again.Changed() {
		t.Errorf("second Apply changed objects: %+v", again.Actions)
	}
	if again.Unchanged != 2 {
		t.Errorf("unchanged = %d, want function and trigger", again.Unchanged)
	}

	conn := listen(t, dsn, channel)
	adminExec(t, dsn, `INSERT INTO `+qualified+` (state) VALUES ('queued')`)
	awaitPayload(t, conn, qualified)

	adminExec(t, dsn, `UPDATE `+qualified+` SET state = 'running'`)
	awaitPayload(t, conn, qualified)

	adminExec(t, dsn, `DELETE FROM `+qualified)
	awaitPayload(t, conn, qualified)

	status, err := m.Status(context.Background(), target)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.SchemaPresent || status.Function != triggers.StatePresent {
		t.Errorf("status = %+v, want schema and function present", status)
	}
	if len(status.Objects) != 1 || status.Objects[0].State != triggers.StatePresent {
		t.Errorf("objects = %+v, want the trigger present", status.Objects)
	}

	removed, err := m.Remove(context.Background(), target)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(removed.Actions) != 2 {
		t.Errorf("removed actions = %+v, want trigger and function", removed.Actions)
	}

	after, err := m.Status(context.Background(), target)
	if err != nil {
		t.Fatalf("Status after Remove: %v", err)
	}
	if after.Function != triggers.StateMissing {
		t.Errorf("function state = %q, want missing", after.Function)
	}
	if len(after.Objects) != 1 || after.Objects[0].State != triggers.StateMissing {
		t.Errorf("objects = %+v, want the trigger missing", after.Objects)
	}
}

// Apply and remove must leave user-created objects untouched.
func TestManagedTriggersPreserveUnrelatedObjects(t *testing.T) {
	dsn := sharedDSN(t, "triggers_preserve")
	u := uniqueSuffix()
	schema, table, channel := "mt_"+u, "jobs_"+u, "mt_ch_"+u
	qualified := "public." + table
	other := "other_fn_" + u

	adminExec(t, dsn,
		`DROP TABLE IF EXISTS `+qualified+` CASCADE`,
		`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
		`DROP FUNCTION IF EXISTS public.`+other+`() CASCADE`,
		`CREATE TABLE `+qualified+` (id bigserial PRIMARY KEY)`,
		`CREATE FUNCTION public.`+other+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		`CREATE TRIGGER user_trigger BEFORE INSERT ON `+qualified+` FOR EACH ROW EXECUTE FUNCTION public.`+other+`()`,
	)
	t.Cleanup(func() {
		adminExec(t, dsn,
			`DROP TABLE IF EXISTS `+qualified+` CASCADE`,
			`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
			`DROP FUNCTION IF EXISTS public.`+other+`() CASCADE`,
		)
	})

	target := triggers.Target{
		SourceID: "appdb", Schema: schema, Manage: true, DSN: dsn,
		Subscriptions: []triggers.Subscription{{Table: qualified, Channel: channel}},
	}
	m := openTriggers(t, dsn)
	if _, err := m.Apply(context.Background(), target); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := m.Remove(context.Background(), target); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if !objectExists(t, dsn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_trigger t
			JOIN pg_class c ON c.oid = t.tgrelid
			WHERE c.relname = $1 AND t.tgname = 'user_trigger' AND NOT t.tgisinternal)`, table) {
		t.Error("unrelated trigger was removed")
	}
	if !objectExists(t, dsn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = 'public' AND p.proname = $1)`, other) {
		t.Error("unrelated function was removed")
	}
	if !objectExists(t, dsn, `SELECT to_regclass($1) IS NOT NULL`, qualified) {
		t.Error("the monitored table was removed")
	}
}

// A user object occupying the generated name must be reported, never replaced.
func TestManagedTriggersRefuseToOverwriteAForeignObject(t *testing.T) {
	dsn := sharedDSN(t, "triggers_collision")
	u := uniqueSuffix()
	schema, table, channel := "mt_"+u, "jobs_"+u, "mt_ch_"+u
	qualified := "public." + table
	other := "other_fn_" + u
	name := triggers.TriggerName(qualified, channel)

	adminExec(t, dsn,
		`DROP TABLE IF EXISTS `+qualified+` CASCADE`,
		`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
		`DROP FUNCTION IF EXISTS public.`+other+`() CASCADE`,
		`CREATE TABLE `+qualified+` (id bigserial PRIMARY KEY)`,
		`CREATE FUNCTION public.`+other+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`,
		`CREATE TRIGGER `+name+` BEFORE INSERT ON `+qualified+` FOR EACH ROW EXECUTE FUNCTION public.`+other+`()`,
	)
	t.Cleanup(func() {
		adminExec(t, dsn,
			`DROP TABLE IF EXISTS `+qualified+` CASCADE`,
			`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
			`DROP FUNCTION IF EXISTS public.`+other+`() CASCADE`,
		)
	})

	target := triggers.Target{
		SourceID: "appdb", Schema: schema, Manage: true, DSN: dsn,
		Subscriptions: []triggers.Subscription{{Table: qualified, Channel: channel}},
	}
	m := openTriggers(t, dsn)
	if _, err := m.Apply(context.Background(), target); err == nil {
		t.Fatal("Apply overwrote a foreign trigger")
	} else if !strings.Contains(err.Error(), "not owned by Metricule") {
		t.Errorf("error = %v, want an ownership refusal", err)
	}

	if !objectExists(t, dsn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_trigger t
			JOIN pg_class c ON c.oid = t.tgrelid
			WHERE c.relname = $1 AND t.tgname = $2 AND NOT t.tgisinternal)`, table, name) {
		t.Error("the foreign trigger was removed")
	}
}

// Changing the event scope replaces the trigger in place instead of adding a
// second one, and the new events are observed.
func TestManagedTriggersConvergeOnScopeChange(t *testing.T) {
	dsn := sharedDSN(t, "triggers_drift")
	u := uniqueSuffix()
	schema, table, channel := "mt_"+u, "jobs_"+u, "mt_ch_"+u
	qualified := "public." + table

	adminExec(t, dsn,
		`DROP TABLE IF EXISTS `+qualified+` CASCADE`,
		`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
		`CREATE TABLE `+qualified+` (id bigserial PRIMARY KEY, state text)`,
	)
	t.Cleanup(func() {
		adminExec(t, dsn,
			`DROP TABLE IF EXISTS `+qualified+` CASCADE`,
			`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
		)
	})

	insertOnly := triggers.Target{
		SourceID: "appdb", Schema: schema, Manage: true, DSN: dsn,
		Subscriptions: []triggers.Subscription{{Table: qualified, Channel: channel, Events: []string{"insert"}}},
	}
	m := openTriggers(t, dsn)
	if _, err := m.Apply(context.Background(), insertOnly); err != nil {
		t.Fatalf("Apply insert-only: %v", err)
	}

	conn := listen(t, dsn, channel)
	adminExec(t, dsn, `UPDATE `+qualified+` SET state = 'ignored'`)
	awaitNoPayload(t, conn)

	insertAndUpdate := insertOnly
	insertAndUpdate.Subscriptions = []triggers.Subscription{{
		Table: qualified, Channel: channel, Events: []string{"insert", "update"},
	}}
	report, err := m.Apply(context.Background(), insertAndUpdate)
	if err != nil {
		t.Fatalf("Apply insert+update: %v", err)
	}
	if !report.Changed() {
		t.Fatal("scope change produced no action")
	}

	if got := countManagedTriggers(t, dsn, schema); got != 1 {
		t.Errorf("managed triggers = %d, want exactly one after convergence", got)
	}
	adminExec(t, dsn, `UPDATE `+qualified+` SET state = 'running'`)
	awaitPayload(t, conn, qualified)

	// Removing a subscription converges too: the old managed trigger is dropped.
	if _, err := m.Apply(context.Background(), triggers.Target{
		SourceID: "appdb", Schema: schema, Manage: true, DSN: dsn,
	}); err != nil {
		t.Fatalf("Apply empty scope: %v", err)
	}
	if got := countManagedTriggers(t, dsn, schema); got != 0 {
		t.Errorf("managed triggers = %d after clearing the scope, want 0", got)
	}
}

func countManagedTriggers(t *testing.T, dsn, schema string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("count connect: %v", err)
	}
	defer conn.Close(ctx)

	var n int
	err = conn.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_trigger t
		JOIN pg_proc p ON p.oid = t.tgfoid
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND p.proname = $2 AND NOT t.tgisinternal`,
		schema, triggers.FunctionName).Scan(&n)
	if err != nil {
		t.Fatalf("count managed triggers: %v", err)
	}
	return n
}

// Runtime wiring: serve applies managed triggers before collecting, and removes
// them at shutdown only when cleanup_on_shutdown is enabled.
func TestRuntimeProvisionsAndCleansUpManagedTriggers(t *testing.T) {
	dsn := sharedDSN(t, "triggers_runtime")
	u := uniqueSuffix()
	schema, table, channel := "mt_"+u, "jobs_"+u, "mt_ch_"+u
	qualified := "public." + table

	adminExec(t, dsn,
		`DROP TABLE IF EXISTS `+qualified+` CASCADE`,
		`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
		`CREATE TABLE `+qualified+` (id bigserial PRIMARY KEY)`,
	)
	t.Cleanup(func() {
		adminExec(t, dsn,
			`DROP TABLE IF EXISTS `+qualified+` CASCADE`,
			`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
		)
	})

	cfgPath := writeTriggerConfig(t, fmt.Sprintf(`
apiVersion: metricule.io/v1alpha1
kind: Exporter
server:
  listen: 127.0.0.1:0
sources:
  appdb:
    type: postgres
    dsn:
      value: %q
    triggers:
      manage: true
      schema: %s
      cleanup_on_shutdown: true
metrics:
  - name: app_jobs
    help: Queued jobs
    type: gauge
    source: appdb
    query:
      sql: SELECT COUNT(*) AS value FROM %s
    mapping:
      value: value
    collect:
      mode: notify
      channel: %s
      fallback_interval: 60s
      watch:
        tables:
          - %s
`, dsn, schema, qualified, channel, qualified))

	cfg, ds, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v; warnings: %v", err, ds)
	}
	rt, err := runtime.New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := waitFor(30*time.Second, func() error {
		if !objectExists(t, dsn, `
			SELECT EXISTS (
				SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = $1 AND p.proname = $2)`, schema, triggers.FunctionName) {
			return fmt.Errorf("managed function not created yet")
		}
		return nil
	}); err != nil {
		cancel()
		<-done
		t.Fatalf("startup did not provision triggers: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Runtime.Run did not return after cancellation")
	}

	if err := waitFor(10*time.Second, func() error {
		if objectExists(t, dsn, `
			SELECT EXISTS (
				SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = $1 AND p.proname = $2)`, schema, triggers.FunctionName) {
			return fmt.Errorf("managed function still present after shutdown")
		}
		return nil
	}); err != nil {
		t.Errorf("cleanup_on_shutdown did not remove managed objects: %v", err)
	}
}

func writeTriggerConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metricule.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
