//go:build integration

// Integration coverage for the MySQL adapter.
//
// Runs against a real MySQL container: read-only enforcement, cancellation, row
// limits, and driver decoding have no faithful in-process equivalent.
package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/teagan42/metricule/internal/source"
	mysqladapter "github.com/teagan42/metricule/internal/source/mysql"
	"github.com/teagan42/metricule/internal/telemetry"
)

const mysqlImage = "mysql:8.0.36"

var sharedMySQL = sync.OnceValues(func() (*tcmysql.MySQLContainer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return tcmysql.Run(ctx, mysqlImage,
		tcmysql.WithDatabase("app"),
		tcmysql.WithUsername("metricule"),
		tcmysql.WithPassword("metricule-secret"),
	)
})

func sharedDSN(t *testing.T) string {
	t.Helper()
	ctr, err := sharedMySQL()
	if err != nil {
		t.Fatalf("start mysql: %v", err)
	}
	dsn, err := ctr.ConnectionString(context.Background())
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

func openSource(t *testing.T, dsn string, tune func(*source.MySQLSettings)) *mysqladapter.Source {
	t.Helper()
	settings := source.MySQLSettings{
		DSN:          dsn,
		MaxOpen:      4,
		MaxIdle:      1,
		QueryTimeout: 5 * time.Second,
	}
	if tune != nil {
		tune(&settings)
	}
	s, err := mysqladapter.NewSource("appdb", settings)
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

func adminExec(t *testing.T, dsn string, statements ...string) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	defer db.Close()
	for _, stmt := range statements {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

var tableSeq atomic.Int64

func tableName(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, tableSeq.Add(1))
}

func compile(t *testing.T, s *mysqladapter.Source, query string) source.QuerySpec {
	t.Helper()
	spec, err := s.CompileQuery(source.QueryDefinition{MetricName: "m", SQL: query})
	if err != nil {
		t.Fatalf("CompileQuery(%q): %v", query, err)
	}
	return spec
}

func query(t *testing.T, s *mysqladapter.Source, spec source.QuerySpec, maxRows int) (source.ResultSet, error) {
	t.Helper()
	return s.Query(context.Background(), source.QueryRequest{Spec: spec, MaxRows: maxRows})
}

func reasonOf(t *testing.T, err error) telemetry.Reason {
	t.Helper()
	var classified source.Classified
	if !errors.As(err, &classified) {
		t.Fatalf("error %v is not classified", err)
	}
	return classified.Reason()
}

func TestMySQLReadsCommittedWrites(t *testing.T) {
	dsn := sharedDSN(t)
	table := tableName("jobs")
	adminExec(t, dsn,
		`DROP TABLE IF EXISTS `+table,
		`CREATE TABLE `+table+` (id bigint PRIMARY KEY AUTO_INCREMENT, state varchar(32) NOT NULL)`,
		`INSERT INTO `+table+` (state) VALUES ('queued'), ('queued'), ('running')`,
	)
	t.Cleanup(func() { adminExec(t, dsn, `DROP TABLE IF EXISTS `+table) })

	s := openSource(t, dsn, nil)
	rs, err := query(t, s, compile(t, s, `SELECT COUNT(*) AS value FROM `+table), 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rs.Rows) != 1 || len(rs.Columns) != 1 || rs.Columns[0].Name != "value" {
		t.Fatalf("result = %+v", rs)
	}
	if got := fmt.Sprint(rs.Rows[0]["value"]); got != "3" {
		t.Errorf("count = %v, want 3", rs.Rows[0]["value"])
	}

	adminExec(t, dsn, `INSERT INTO `+table+` (state) VALUES ('queued')`)
	rs, err = query(t, s, compile(t, s, `SELECT COUNT(*) AS value FROM `+table), 10)
	if err != nil {
		t.Fatalf("Query after write: %v", err)
	}
	if got := fmt.Sprint(rs.Rows[0]["value"]); got != "4" {
		t.Errorf("count after write = %v, want 4", rs.Rows[0]["value"])
	}
}

func TestMySQLDecodesValuesAndNulls(t *testing.T) {
	dsn := sharedDSN(t)
	table := tableName("values")
	adminExec(t, dsn,
		`DROP TABLE IF EXISTS `+table,
		`CREATE TABLE `+table+` (id bigint PRIMARY KEY, amount decimal(10,2) NULL, ratio double NOT NULL, name varchar(32) NULL)`,
		`INSERT INTO `+table+` VALUES (1, 12.50, 1.5, 'alpha'), (2, NULL, 2.5, NULL)`,
	)
	t.Cleanup(func() { adminExec(t, dsn, `DROP TABLE IF EXISTS `+table) })

	s := openSource(t, dsn, nil)
	rs, err := query(t, s, compile(t, s, `SELECT id, amount, ratio, name FROM `+table+` ORDER BY id`), 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rs.Rows) != 2 {
		t.Fatalf("rows = %d", len(rs.Rows))
	}
	if got := string(rs.Rows[0]["name"].([]byte)); got != "alpha" {
		t.Errorf("name = %q, want alpha", got)
	}
	if rs.Rows[1]["name"] != nil {
		t.Errorf("null name = %#v, want nil", rs.Rows[1]["name"])
	}
	if rs.Rows[1]["amount"] != nil {
		t.Errorf("null amount = %#v, want nil", rs.Rows[1]["amount"])
	}
}

func TestMySQLReadOnlyTransactionRejectsWrites(t *testing.T) {
	dsn := sharedDSN(t)
	table := tableName("readonly")
	adminExec(t, dsn,
		`DROP TABLE IF EXISTS `+table,
		`CREATE TABLE `+table+` (id bigint PRIMARY KEY, state varchar(32))`,
		`INSERT INTO `+table+` VALUES (1, 'queued')`,
	)
	t.Cleanup(func() { adminExec(t, dsn, `DROP TABLE IF EXISTS `+table) })

	s := openSource(t, dsn, nil)
	_, err := query(t, s, compile(t, s, `UPDATE `+table+` SET state = 'running'`), 10)
	if err == nil {
		t.Fatal("write succeeded through the adapter")
	}
	if got := reasonOf(t, err); got != telemetry.ReasonQuery {
		t.Errorf("reason = %q, want query", got)
	}

	// The row must be unchanged.
	rs, err := query(t, s, compile(t, s, `SELECT state FROM `+table), 10)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got := string(rs.Rows[0]["state"].([]byte)); got != "queued" {
		t.Errorf("state = %q, want queued", got)
	}
}

func TestMySQLRejectsMultiStatements(t *testing.T) {
	dsn := sharedDSN(t) + "?multiStatements=true"
	if _, err := mysqladapter.NewSource("appdb", source.MySQLSettings{
		DSN: dsn, MaxOpen: 1, MaxIdle: 0, QueryTimeout: time.Second,
	}); err == nil {
		t.Fatal("multiStatements DSN accepted")
	}
}

func TestMySQLRowLimitFailsRatherThanTruncate(t *testing.T) {
	dsn := sharedDSN(t)
	table := tableName("rows")
	adminExec(t, dsn,
		`DROP TABLE IF EXISTS `+table,
		`CREATE TABLE `+table+` (id bigint PRIMARY KEY)`,
		`INSERT INTO `+table+` VALUES (1), (2), (3)`,
	)
	t.Cleanup(func() { adminExec(t, dsn, `DROP TABLE IF EXISTS `+table) })

	s := openSource(t, dsn, nil)
	_, err := query(t, s, compile(t, s, `SELECT id FROM `+table), 2)
	if err == nil {
		t.Fatal("oversized result accepted")
	}
	if !errors.Is(err, mysqladapter.ErrResultTooLarge) {
		t.Errorf("err = %v, want ErrResultTooLarge", err)
	}
}

func TestMySQLQueryTimeout(t *testing.T) {
	dsn := sharedDSN(t)
	s := openSource(t, dsn, func(settings *source.MySQLSettings) {
		settings.QueryTimeout = 300 * time.Millisecond
	})

	started := time.Now()
	_, err := query(t, s, compile(t, s, `SELECT SLEEP(3) AS value`), 1)
	if err == nil {
		t.Fatal("slow query succeeded")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("timeout took %s, want under 2s", elapsed)
	}
	if got := reasonOf(t, err); got != telemetry.ReasonTimeout {
		t.Errorf("reason = %q, want timeout", got)
	}
}

func TestMySQLDuplicateColumnsRejected(t *testing.T) {
	dsn := sharedDSN(t)
	s := openSource(t, dsn, nil)
	_, err := query(t, s, compile(t, s, `SELECT 1 AS value, 2 AS value`), 10)
	if err == nil {
		t.Fatal("duplicate columns accepted")
	}
}

func TestMySQLConcurrentQueries(t *testing.T) {
	dsn := sharedDSN(t)
	table := tableName("concurrent")
	adminExec(t, dsn,
		`DROP TABLE IF EXISTS `+table,
		`CREATE TABLE `+table+` (id bigint PRIMARY KEY)`,
	)
	for i := 1; i <= 50; i++ {
		adminExec(t, dsn, fmt.Sprintf(`INSERT INTO %s VALUES (%d)`, table, i))
	}
	t.Cleanup(func() { adminExec(t, dsn, `DROP TABLE IF EXISTS `+table) })

	s := openSource(t, dsn, nil)
	spec := compile(t, s, `SELECT COUNT(*) AS value FROM `+table)

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs, err := query(t, s, spec, 1)
			if err != nil {
				errs <- err
				return
			}
			if got := fmt.Sprint(rs.Rows[0]["value"]); got != "50" {
				errs <- fmt.Errorf("count = %s", got)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent query: %v", err)
	}
}

func TestMySQLHealth(t *testing.T) {
	dsn := sharedDSN(t)
	s := openSource(t, dsn, nil)
	if err := s.Health(context.Background()); err != nil {
		t.Errorf("Health: %v", err)
	}
}
