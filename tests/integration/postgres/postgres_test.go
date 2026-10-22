//go:build integration

// Integration coverage for the PostgreSQL adapter (§20.2).
//
// Runs against a real PostgreSQL container: rule 9 forbids substituting mocks
// where actual datastore behavior is under test. Snapshot isolation, server-side
// cancellation, read-only transaction rejection, and pool recovery across a
// restart have no faithful in-process equivalent.
package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/teagan42/metricule/internal/source"
	pgadapter "github.com/teagan42/metricule/internal/source/postgres"
	"github.com/teagan42/metricule/internal/telemetry"
)

const (
	pgImage       = "postgres:17-alpine"
	pgStartupWait = 2 * time.Minute
	// Budget for an outage to clear. Exceeds the adapter's 10s backoff cap so a
	// deferred probe is not mistaken for a failure to reconnect.
	pgRecoveryBudget = 90 * time.Second
)

// Starts a PostgreSQL container and returns its DSN.
func startPostgres(t *testing.T) (*tcpostgres.PostgresContainer, string) {
	t.Helper()
	ctx := context.Background()

	startCtx, cancel := context.WithTimeout(ctx, pgStartupWait)
	defer cancel()

	ctr, err := tcpostgres.Run(startCtx, pgImage,
		tcpostgres.WithDatabase("app"),
		tcpostgres.WithUsername("owner"),
		tcpostgres.WithPassword("owner-secret"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return ctr, dsn
}

// Forwards a stable local address to the container's current endpoint.
//
// Docker republishes a container on a fresh ephemeral port when it restarts, but a
// source's DSN is fixed. Proxying keeps the restart test honest — the database
// really stops and really restarts — without pinning a host port.
func startProxy(t *testing.T, backend func() (string, error)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			client, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go forward(client, backend)
		}
	}()
	return ln.Addr().String()
}

// Splices one client connection onto the current backend address.
func forward(client net.Conn, backend func() (string, error)) {
	defer client.Close()

	addr, err := backend()
	if err != nil {
		return
	}
	server, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return
	}
	defer server.Close()

	go func() {
		defer server.Close()
		io.Copy(server, client)
	}()
	io.Copy(client, server)
}

// Reserves an ephemeral port and releases it for Docker to bind.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("split port: %v", err)
	}
	return port
}

// Opens an adapter source against dsn, closing it at test end.
func openSource(t *testing.T, dsn string, tune func(*source.PostgresSettings)) *pgadapter.Source {
	t.Helper()
	settings := source.PostgresSettings{
		DSN:          dsn,
		MaxOpen:      4,
		MaxIdle:      1,
		QueryTimeout: 10 * time.Second,
	}
	if tune != nil {
		tune(&settings)
	}
	s, err := pgadapter.NewSource("appdb", settings)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if err := s.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// Runs admin SQL on a direct connection, outside the adapter's read-only pool.
func adminExec(t *testing.T, dsn string, statements ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer conn.Close(ctx)

	for _, sql := range statements {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("admin exec %q: %v", sql, err)
		}
	}
}

// Compiles sql and runs it through the adapter.
func query(ctx context.Context, t *testing.T, s *pgadapter.Source, sql string, maxRows int) (source.ResultSet, error) {
	t.Helper()
	spec := compile(t, s, sql)
	return s.Query(ctx, source.QueryRequest{Spec: spec, MaxRows: maxRows})
}

func compile(t *testing.T, s *pgadapter.Source, sql string) source.QuerySpec {
	t.Helper()
	spec, err := s.CompileQuery(source.QueryDefinition{MetricName: "test_metric", SQL: sql})
	if err != nil {
		t.Fatalf("CompileQuery(%q): %v", sql, err)
	}
	return spec
}

func scalar(t *testing.T, rs source.ResultSet) any {
	t.Helper()
	if len(rs.Rows) != 1 {
		t.Fatalf("result has %d rows, want 1", len(rs.Rows))
	}
	v, ok := rs.Rows[0]["value"]
	if !ok {
		t.Fatalf("result has no \"value\" column: %+v", rs.Columns)
	}
	return v
}

func seedJobs(t *testing.T, dsn string) {
	t.Helper()
	adminExec(t, dsn,
		`CREATE TABLE jobs (id bigserial PRIMARY KEY, state text NOT NULL)`,
		`INSERT INTO jobs (state) VALUES ('queued'), ('queued'), ('running')`,
	)
}

func reasonOf(t *testing.T, err error) telemetry.Reason {
	t.Helper()
	var reporter interface{ Reason() telemetry.Reason }
	if !errors.As(err, &reporter) {
		t.Fatalf("error %v (%T) reports no telemetry reason", err, err)
	}
	return reporter.Reason()
}

// §20.2 1-3: committed inserts, updates, and deletes change the aggregate.
func TestPostgresQueryReflectsCommittedWrites(t *testing.T) {
	t.Parallel()
	_, dsn := startPostgres(t)
	seedJobs(t, dsn)
	s := openSource(t, dsn, nil)

	const sql = `SELECT count(*) AS value FROM jobs WHERE state = 'queued'`
	assertCount := func(want int64) {
		t.Helper()
		rs, err := query(context.Background(), t, s, sql, 10)
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if got := scalar(t, rs); got != want {
			t.Fatalf("count = %v (%T), want %d", got, got, want)
		}
	}

	assertCount(2)
	adminExec(t, dsn, `INSERT INTO jobs (state) VALUES ('queued')`)
	assertCount(3)
	adminExec(t, dsn, `UPDATE jobs SET state = 'done' WHERE state = 'queued'`)
	assertCount(0)
	adminExec(t, dsn, `INSERT INTO jobs (state) VALUES ('queued'), ('queued')`)
	assertCount(2)
	adminExec(t, dsn, `DELETE FROM jobs WHERE state = 'queued'`)
	assertCount(0)
}

// Columns and values normalize into the ResultSet contract.
func TestPostgresMapsResultTypes(t *testing.T) {
	t.Parallel()
	_, dsn := startPostgres(t)
	s := openSource(t, dsn, nil)

	rs, err := query(context.Background(), t, s, `
		SELECT 42::bigint          AS as_int8,
		       7::int              AS as_int4,
		       1.5::float8         AS as_float8,
		       12.34::numeric      AS as_numeric,
		       true                AS as_bool,
		       'queued'::text      AS as_text,
		       NULL::text          AS as_null,
		       '\x0102'::bytea     AS as_bytea,
		       'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid AS as_uuid,
		       '2026-10-21T09:00:00Z'::timestamptz AS as_timestamptz
	`, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rs.Rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rs.Rows))
	}
	row := rs.Rows[0]

	for name, expected := range map[string]any{
		"as_int8":    int64(42),
		"as_int4":    int64(7),
		"as_float8":  1.5,
		"as_numeric": 12.34,
		"as_bool":    true,
		"as_text":    "queued",
		"as_null":    nil,
		"as_uuid":    "ffffffff-ffff-ffff-ffff-ffffffffffff",
	} {
		if got := row[name]; got != expected {
			t.Errorf("%s = %#v (%T), want %#v", name, got, got, expected)
		}
	}
	if b, ok := row["as_bytea"].([]byte); !ok || len(b) != 2 || b[0] != 1 || b[1] != 2 {
		t.Errorf("as_bytea = %#v, want []byte{1,2}", row["as_bytea"])
	}
	if ts, ok := row["as_timestamptz"].(time.Time); !ok || ts.UTC().Hour() != 9 {
		t.Errorf("as_timestamptz = %#v, want a time.Time at 09:00 UTC", row["as_timestamptz"])
	}

	types := map[string]string{}
	for _, c := range rs.Columns {
		types[c.Name] = c.Type
	}
	for name, wantType := range map[string]string{
		"as_int8": "int8", "as_numeric": "numeric", "as_bool": "bool",
		"as_text": "text", "as_uuid": "uuid", "as_timestamptz": "timestamptz",
	} {
		if types[name] != wantType {
			t.Errorf("column %s type = %q, want %q", name, types[name], wantType)
		}
	}
}

// §8.7: exceeding max_rows errors instead of truncating.
func TestPostgresRejectsOversizedResults(t *testing.T) {
	t.Parallel()
	_, dsn := startPostgres(t)
	s := openSource(t, dsn, nil)

	rs, err := query(context.Background(), t, s,
		`SELECT i AS value FROM generate_series(1, 50) AS i`, 10)
	if err == nil {
		t.Fatalf("Query returned %d rows, want an error", len(rs.Rows))
	}
	if len(rs.Rows) != 0 {
		t.Errorf("failed query returned %d rows, want none", len(rs.Rows))
	}
	if got := reasonOf(t, err); got != telemetry.ReasonResultTooLarge {
		t.Errorf("reason = %q, want %q", got, telemetry.ReasonResultTooLarge)
	}

	// A rejected result must not poison the pool.
	if _, err := query(context.Background(), t, s, `SELECT 1 AS value`, 10); err != nil {
		t.Errorf("Query after rejection: %v", err)
	}
}

// §20.2 11: a query beyond query_timeout fails as a timeout and frees the pool.
func TestPostgresCollectionTimeout(t *testing.T) {
	t.Parallel()
	_, dsn := startPostgres(t)
	s := openSource(t, dsn, func(set *source.PostgresSettings) {
		set.QueryTimeout = 750 * time.Millisecond
	})

	started := time.Now()
	_, err := query(context.Background(), t, s, `SELECT pg_sleep(30) AS value`, 10)
	if err == nil {
		t.Fatal("Query returned no error, want a timeout")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("Query took %s, want the configured timeout to apply", elapsed)
	}
	if got := reasonOf(t, err); got != telemetry.ReasonTimeout {
		t.Errorf("reason = %q, want %q", got, telemetry.ReasonTimeout)
	}

	assertNoSleepingBackends(t, dsn)

	if _, err := query(context.Background(), t, s, `SELECT 1 AS value`, 10); err != nil {
		t.Errorf("Query after timeout: %v", err)
	}
}

// §18 Agent 4: cancelling the collection context stops the server-side query.
func TestPostgresQueryCancellation(t *testing.T) {
	t.Parallel()
	_, dsn := startPostgres(t)
	s := openSource(t, dsn, func(set *source.PostgresSettings) {
		set.QueryTimeout = 60 * time.Second
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	_, err := query(ctx, t, s, `SELECT pg_sleep(60) AS value`, 10)
	if err == nil {
		t.Fatal("Query returned no error, want cancellation")
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Errorf("cancellation took %s, want a prompt return", elapsed)
	}
	if got := reasonOf(t, err); got != telemetry.ReasonTimeout {
		t.Errorf("reason = %q, want %q", got, telemetry.ReasonTimeout)
	}

	assertNoSleepingBackends(t, dsn)

	if _, err := query(context.Background(), t, s, `SELECT 1 AS value`, 10); err != nil {
		t.Errorf("Query after cancellation: %v", err)
	}
}

// Fails unless every abandoned pg_sleep backend has gone.
func assertNoSleepingBackends(t *testing.T, dsn string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer conn.Close(ctx)

	deadline := time.Now().Add(20 * time.Second)
	var remaining int64
	for time.Now().Before(deadline) {
		err = conn.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE query LIKE '%pg\_sleep%' AND pid <> pg_backend_pid() AND state = 'active'
		`).Scan(&remaining)
		if err != nil {
			t.Fatalf("inspect pg_stat_activity: %v", err)
		}
		if remaining == 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Errorf("%d abandoned pg_sleep backends still running", remaining)
}

// §20.2 7: a database restart must not require a Metricule restart.
func TestPostgresSurvivesDatabaseRestart(t *testing.T) {
	ctr, dsn := startPostgres(t)
	seedJobs(t, dsn)

	// The source addresses the proxy, so its DSN survives the republish.
	proxy := startProxy(t, func() (string, error) {
		return ctr.PortEndpoint(context.Background(), "5432/tcp", "")
	})
	proxiedDSN := fmt.Sprintf("postgres://owner:owner-secret@%s/app?sslmode=disable", proxy)
	s := openSource(t, proxiedDSN, func(set *source.PostgresSettings) {
		set.QueryTimeout = 3 * time.Second
	})

	const sql = `SELECT count(*) AS value FROM jobs`
	if _, err := query(context.Background(), t, s, sql, 10); err != nil {
		t.Fatalf("Query before restart: %v", err)
	}

	ctx := context.Background()
	stopTimeout := 30 * time.Second
	if err := ctr.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("stop container: %v", err)
	}

	if err := s.Health(ctx); err == nil {
		t.Error("Health reported up while the database was stopped")
	} else if got := reasonOf(t, err); got != telemetry.ReasonConnection && got != telemetry.ReasonTimeout {
		t.Errorf("reason while down = %q, want connection or timeout", got)
	}
	if _, err := query(ctx, t, s, sql, 10); err == nil {
		t.Error("Query succeeded while the database was stopped")
	}

	if err := ctr.Start(ctx); err != nil {
		t.Fatalf("start container: %v", err)
	}

	// The same Source must recover with no further Open or Close.
	if err := waitFor(pgRecoveryBudget, func() error { return s.Health(ctx) }); err != nil {
		t.Fatalf("Health did not recover: %v", err)
	}
	if _, err := query(ctx, t, s, sql, 10); err != nil {
		t.Fatalf("Query after restart: %v", err)
	}

	d := s.Diagnostics()
	if !d.Up {
		t.Error("Diagnostics report the source down after recovery")
	}
	if d.Reconnects < 1 {
		t.Errorf("Reconnects = %d, want at least 1 so the scheduler can reconcile", d.Reconnects)
	}
	if d.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d after recovery, want 0", d.ConsecutiveFailures)
	}
	if strings.Contains(d.Target, "owner-secret") {
		t.Errorf("diagnostics leaked credentials: %q", d.Target)
	}
}

func waitFor(budget time.Duration, fn func() error) error {
	deadline := time.Now().Add(budget)
	var err error
	for time.Now().Before(deadline) {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return err
}

// §14.5 and §20.2 10: writes are rejected by the transaction and by privileges.
func TestPostgresReadOnlyEnforcement(t *testing.T) {
	t.Parallel()
	_, ownerDSN := startPostgres(t)
	seedJobs(t, ownerDSN)
	adminExec(t, ownerDSN,
		`CREATE ROLE reader LOGIN PASSWORD 'reader-secret'`,
		`GRANT CONNECT ON DATABASE app TO reader`,
		`GRANT USAGE ON SCHEMA public TO reader`,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO reader`,
	)
	readerDSN := strings.Replace(ownerDSN, "owner:owner-secret@", "reader:reader-secret@", 1)
	if readerDSN == ownerDSN {
		t.Fatal("could not derive a reader DSN")
	}

	// A privileged account still cannot write: the read-only transaction is an
	// enforcement point independent of the account's grants.
	t.Run("owner credentials cannot write", func(t *testing.T) {
		s := openSource(t, ownerDSN, nil)
		_, err := query(context.Background(), t, s, `INSERT INTO jobs (state) VALUES ('injected')`, 10)
		if err == nil {
			t.Fatal("INSERT succeeded inside a read-only transaction")
		}
		if !errors.Is(err, pgadapter.ErrReadOnly) {
			t.Errorf("error = %v, want ErrReadOnly", err)
		}
		if got := reasonOf(t, err); got != telemetry.ReasonQuery {
			t.Errorf("reason = %q, want %q", got, telemetry.ReasonQuery)
		}
	})

	t.Run("read-only credentials select", func(t *testing.T) {
		s := openSource(t, readerDSN, nil)
		rs, err := query(context.Background(), t, s, `SELECT count(*) AS value FROM jobs`, 10)
		if err != nil {
			t.Fatalf("Query as reader: %v", err)
		}
		if got := scalar(t, rs); got != int64(3) {
			t.Errorf("count = %v, want 3", got)
		}
	})

	t.Run("read-only credentials cannot write", func(t *testing.T) {
		s := openSource(t, readerDSN, nil)
		_, err := query(context.Background(), t, s, `INSERT INTO jobs (state) VALUES ('injected')`, 10)
		if err == nil {
			t.Fatal("INSERT as reader succeeded")
		}
		if got := reasonOf(t, err); got != telemetry.ReasonQuery {
			t.Errorf("reason = %q, want %q", got, telemetry.ReasonQuery)
		}
	})

	// Nothing above may have reached the table.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	defer conn.Close(ctx)
	var injected int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE state = 'injected'`).Scan(&injected); err != nil {
		t.Fatalf("verify table: %v", err)
	}
	if injected != 0 {
		t.Errorf("%d rows were written through a read-only source", injected)
	}
}

// §20.2 12: rapid commits during a slow query neither block the writer nor
// leak into the reader's snapshot.
func TestPostgresRapidWritesDuringSlowQuery(t *testing.T) {
	t.Parallel()
	_, dsn := startPostgres(t)
	adminExec(t, dsn,
		`CREATE TABLE events (id bigserial PRIMARY KEY, at timestamptz DEFAULT now())`,
		`INSERT INTO events DEFAULT VALUES`,
		`INSERT INTO events DEFAULT VALUES`,
		`INSERT INTO events DEFAULT VALUES`,
	)
	const seeded = 3
	s := openSource(t, dsn, func(set *source.PostgresSettings) {
		set.QueryTimeout = 30 * time.Second
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	writerDone := make(chan int, 1)
	go func() {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			writerDone <- -1
			return
		}
		defer conn.Close(ctx)

		committed := 0
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := conn.Exec(ctx, `INSERT INTO events DEFAULT VALUES`); err != nil {
				break
			}
			committed++
		}
		writerDone <- committed
	}()

	// The statement's snapshot is fixed before pg_sleep returns, so commits that
	// land during the sleep must stay invisible to this collection.
	rs, err := query(ctx, t, s,
		`SELECT pg_sleep(4) IS NULL AS slept, (SELECT count(*) FROM events) AS value`, 1000)
	if err != nil {
		t.Fatalf("slow Query: %v", err)
	}
	committed := <-writerDone
	if committed <= 0 {
		t.Fatalf("writer committed %d rows, want concurrent writes to proceed", committed)
	}

	if got := scalar(t, rs); got != int64(seeded) {
		t.Errorf("snapshot count = %v, want %d: the read saw uncommitted-at-start writes", got, seeded)
	}

	// A later collection sees everything the writer committed.
	after, err := query(ctx, t, s, `SELECT count(*) AS value FROM events`, 1_000_000)
	if err != nil {
		t.Fatalf("Query after writes: %v", err)
	}
	if got := scalar(t, after); got != int64(seeded+committed) {
		t.Errorf("count after writes = %v, want %d", got, seeded+committed)
	}
}

// P5: concurrent collections never exceed the configured pool bound.
func TestPostgresPoolIsBounded(t *testing.T) {
	t.Parallel()
	_, dsn := startPostgres(t)
	const maxOpen = 3
	s := openSource(t, dsn, func(set *source.PostgresSettings) {
		set.MaxOpen = maxOpen
		set.MaxIdle = 1
		set.QueryTimeout = 30 * time.Second
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	spec := compile(t, s, `SELECT pg_sleep(0.3), 1 AS value`)
	observed := make(chan int64, 256)
	stop := make(chan struct{})

	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return
		}
		defer conn.Close(ctx)
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			var backends int64
			if err := conn.QueryRow(ctx, `
				SELECT count(*) FROM pg_stat_activity WHERE application_name = 'metricule'
			`).Scan(&backends); err != nil {
				return
			}
			observed <- backends
		}
	}()

	var queries sync.WaitGroup
	for range maxOpen * 4 {
		queries.Add(1)
		go func() {
			defer queries.Done()
			if _, err := s.Query(ctx, source.QueryRequest{Spec: spec, MaxRows: 10}); err != nil {
				t.Errorf("concurrent Query: %v", err)
			}
		}()
	}
	queries.Wait()
	close(stop)
	watcher.Wait()
	close(observed)

	var peak int64
	for n := range observed {
		peak = max(peak, n)
	}
	if peak > maxOpen {
		t.Errorf("peak connections = %d, want at most max_open %d", peak, maxOpen)
	}
	if peak == 0 {
		t.Error("observed no adapter connections; the pool probe did not work")
	}
}

// Health must report an unreachable target rather than blocking indefinitely.
func TestPostgresHealthReportsUnreachableTarget(t *testing.T) {
	t.Parallel()
	settings := source.PostgresSettings{
		DSN:          fmt.Sprintf("postgres://u:p@127.0.0.1:%s/app?sslmode=disable", freePort(t)),
		MaxOpen:      1,
		MaxIdle:      0,
		QueryTimeout: time.Second,
	}
	s, err := pgadapter.NewSource("down", settings)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Open(ctx); err == nil {
		t.Fatal("Open succeeded against a closed port")
	} else if got := reasonOf(t, err); got != telemetry.ReasonConnection && got != telemetry.ReasonTimeout {
		t.Errorf("reason = %q, want connection or timeout", got)
	}

	d := s.Diagnostics()
	if d.Up || d.ConsecutiveFailures == 0 {
		t.Errorf("Diagnostics = %+v, want a recorded outage", d)
	}
	if d.LastError == nil {
		t.Error("Diagnostics carry no last error")
	}
	if strings.Contains(fmt.Sprint(d.LastError), "password") {
		t.Errorf("diagnostics leaked credentials: %v", d.LastError)
	}

	// Health keeps reporting the outage instead of wedging.
	if err := s.Health(ctx); err == nil {
		t.Error("Health reported up for a closed port")
	}
}
