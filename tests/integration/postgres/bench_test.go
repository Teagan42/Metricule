//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/teagan42/metricule/internal/triggers"
)

// Write-path overhead of a managed statement-level trigger (§9.4, §20.6).
//
// Runs one INSERT statement per iteration against two identical tables: one without
// a managed trigger and one with it. The difference is the trigger's cost, including
// pg_notify. Run with:
//
//	go test -tags=integration ./tests/integration/postgres -bench ManagedTrigger -benchtime=2s
func BenchmarkManagedTriggerWriteOverhead(b *testing.B) {
	dsn := sharedDSN(b, "bench_trigger_overhead")
	u := uniqueSuffix()
	schema := "bench_mt_" + u
	plain := "bench_plain_" + u
	watched := "bench_watched_" + u
	channel := "bench_ch_" + u

	adminExec(b, dsn,
		`DROP TABLE IF EXISTS public.`+plain+` CASCADE`,
		`DROP TABLE IF EXISTS public.`+watched+` CASCADE`,
		`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
		`CREATE TABLE public.`+plain+` (id bigserial PRIMARY KEY, state text NOT NULL)`,
		`CREATE TABLE public.`+watched+` (id bigserial PRIMARY KEY, state text NOT NULL)`,
	)
	b.Cleanup(func() {
		adminExec(b, dsn,
			`DROP TABLE IF EXISTS public.`+plain+` CASCADE`,
			`DROP TABLE IF EXISTS public.`+watched+` CASCADE`,
			`DROP SCHEMA IF EXISTS `+schema+` CASCADE`,
		)
	})

	m, err := triggers.Open(context.Background(), dsn)
	if err != nil {
		b.Fatalf("triggers.Open: %v", err)
	}
	defer func() { _ = m.Close(context.Background()) }()

	if _, err := m.Apply(context.Background(), triggers.Target{
		SourceID: "bench", Schema: schema, Manage: true, DSN: dsn,
		Subscriptions: []triggers.Subscription{{
			Table: "public." + watched, Channel: channel,
		}},
	}); err != nil {
		b.Fatalf("apply managed trigger: %v", err)
	}

	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())

	run := func(b *testing.B, sql string) {
		b.Helper()
		ctx := context.Background()
		b.ResetTimer()
		for range b.N {
			if _, err := conn.Exec(ctx, sql); err != nil {
				b.Fatalf("insert: %v", err)
			}
		}
	}

	for _, tc := range []struct {
		name  string
		table string
	}{
		{"without_trigger", plain},
		{"with_trigger", watched},
	} {
		b.Run("single_row/"+tc.name, func(b *testing.B) {
			run(b, `INSERT INTO public.`+tc.table+` (state) VALUES ('queued')`)
		})
		b.Run("bulk_100_rows/"+tc.name, func(b *testing.B) {
			run(b, `INSERT INTO public.`+tc.table+` (state) SELECT 'queued' FROM generate_series(1, 100)`)
		})
	}
}
