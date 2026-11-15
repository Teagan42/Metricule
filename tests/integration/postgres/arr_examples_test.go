//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/runtime"
	"github.com/teagan42/metricule/internal/triggers"
)

// Fixture DDL follows the Sonarr v4 and Radarr v5 PostgreSQL schemas, reduced to
// the columns the examples read. The quoted PascalCase identifiers are the point:
// they must survive both the SQL and managed trigger provisioning.
const sonarrSchema = `
CREATE TABLE "QualityDefinitions" ("Id" serial PRIMARY KEY, "Quality" integer NOT NULL, "Title" text NOT NULL);
CREATE TABLE "Series" ("Id" serial PRIMARY KEY, "Title" text NOT NULL, "Status" integer NOT NULL, "Monitored" boolean NOT NULL);
CREATE TABLE "Episodes" (
	"Id" serial PRIMARY KEY, "SeriesId" integer NOT NULL, "SeasonNumber" integer NOT NULL,
	"EpisodeNumber" integer NOT NULL, "AirDateUtc" timestamptz, "Monitored" boolean NOT NULL,
	"EpisodeFileId" integer NOT NULL DEFAULT 0
);
CREATE TABLE "EpisodeFiles" ("Id" serial PRIMARY KEY, "SeriesId" integer NOT NULL, "Size" bigint NOT NULL, "Quality" text NOT NULL);
CREATE TABLE "History" ("Id" serial PRIMARY KEY, "EpisodeId" integer NOT NULL, "EventType" integer, "Date" timestamptz NOT NULL);
CREATE TABLE "Blocklist" ("Id" serial PRIMARY KEY, "SeriesId" integer NOT NULL, "SourceTitle" text NOT NULL);
CREATE TABLE "PendingReleases" ("Id" serial PRIMARY KEY, "SeriesId" integer NOT NULL, "Title" text NOT NULL);

INSERT INTO "QualityDefinitions" ("Quality", "Title") VALUES (4, 'HDTV-720p'), (9, 'HDTV-1080p');
INSERT INTO "Series" ("Id", "Title", "Status", "Monitored") VALUES
	(1, 'Andor', 0, true), (2, 'The Expanse', 1, true), (3, 'Firefly', 1, false);
INSERT INTO "Episodes" ("SeriesId", "SeasonNumber", "EpisodeNumber", "AirDateUtc", "Monitored", "EpisodeFileId") VALUES
	(1, 1, 1, now() - interval '30 days', true, 1),
	(1, 1, 2, now() - interval '7 days', true, 0),
	(1, 1, 3, now() + interval '7 days', true, 0),
	(1, 0, 1, now() - interval '7 days', true, 0),
	(2, 2, 1, now() - interval '400 days', true, 0),
	(2, 2, 2, now() - interval '400 days', false, 0),
	(3, 1, 1, now() - interval '9000 days', true, 0);
INSERT INTO "EpisodeFiles" ("SeriesId", "Size", "Quality") VALUES
	(1, 1073741824, '{"quality": 9, "revision": {"version": 1, "real": 0, "isRepack": false}}'),
	(2, 2147483648, '{"quality": 9, "revision": {"version": 2, "real": 0, "isRepack": true}}'),
	(2, 536870912,  '{"quality": 4, "revision": {"version": 1, "real": 0, "isRepack": false}}'),
	(2, 100,        '{"quality": 99, "revision": {"version": 1, "real": 0, "isRepack": false}}');
INSERT INTO "History" ("EpisodeId", "EventType", "Date") VALUES
	(1, 1, now() - interval '1 hour'),
	(2, 1, now() - interval '2 hours'),
	(1, 3, now() - interval '1 hour'),
	(5, 4, now() - interval '48 hours');
INSERT INTO "Blocklist" ("SeriesId", "SourceTitle") VALUES (1, 'Andor.S01E02.CAM'), (2, 'The.Expanse.S02E01.FAKE');
INSERT INTO "PendingReleases" ("SeriesId", "Title") VALUES (1, 'Andor.S01E02.1080p.WEB');
`

const radarrSchema = `
CREATE TABLE "QualityDefinitions" ("Id" serial PRIMARY KEY, "Quality" integer NOT NULL, "Title" text NOT NULL);
CREATE TABLE "MovieMetadata" ("Id" serial PRIMARY KEY, "Title" text NOT NULL, "Year" integer NOT NULL, "Status" integer NOT NULL);
CREATE TABLE "Movies" (
	"Id" serial PRIMARY KEY, "MovieMetadataId" integer NOT NULL, "Monitored" boolean NOT NULL,
	"MovieFileId" integer NOT NULL DEFAULT 0, "MinimumAvailability" integer NOT NULL
);
CREATE TABLE "MovieFiles" ("Id" serial PRIMARY KEY, "MovieId" integer NOT NULL, "Size" bigint NOT NULL, "Quality" text NOT NULL);
CREATE TABLE "History" ("Id" serial PRIMARY KEY, "MovieId" integer NOT NULL, "EventType" integer, "Date" timestamptz NOT NULL);
CREATE TABLE "Blocklist" ("Id" serial PRIMARY KEY, "MovieId" integer NOT NULL, "SourceTitle" text NOT NULL);
CREATE TABLE "PendingReleases" ("Id" serial PRIMARY KEY, "MovieId" integer NOT NULL, "Title" text NOT NULL);

INSERT INTO "QualityDefinitions" ("Quality", "Title") VALUES (3, 'WEBDL-1080p'), (7, 'Bluray-1080p');
INSERT INTO "MovieMetadata" ("Id", "Title", "Year", "Status") VALUES
	(1, 'Dune: Part Two', 2024, 3), (2, 'Mickey 17', 2025, 2), (3, 'Sinners', 2025, 3),
	(4, 'Untitled Sequel', 2027, 1), (5, 'Alien', 1979, 3);
INSERT INTO "Movies" ("MovieMetadataId", "Monitored", "MovieFileId", "MinimumAvailability") VALUES
	(1, true, 1, 3),
	(2, true, 0, 3),
	(3, true, 0, 3),
	(4, true, 0, 1),
	(5, false, 0, 3);
INSERT INTO "MovieFiles" ("MovieId", "Size", "Quality") VALUES
	(1, 8589934592, '{"quality": 7, "revision": {"version": 1, "real": 0, "isRepack": false}}'),
	(3, 1073741824, '{"quality": 3, "revision": {"version": 1, "real": 0, "isRepack": false}}');
INSERT INTO "History" ("MovieId", "EventType", "Date") VALUES
	(1, 1, now() - interval '1 hour'),
	(1, 6, now() - interval '3 hours'),
	(2, 9, now() - interval '30 hours');
INSERT INTO "Blocklist" ("MovieId", "SourceTitle") VALUES (2, 'Mickey.17.2025.TS');
`

// Creates a dedicated database so each app's same-named tables do not collide.
func createArrDatabase(t *testing.T, appName, schema string) string {
	t.Helper()
	adminDSN := sharedDSN(t, appName)
	db := appName + "_" + uniqueSuffix()
	adminExec(t, adminDSN, `CREATE DATABASE `+pgx.Identifier{db}.Sanitize())
	t.Cleanup(func() {
		adminExec(t, adminDSN, `DROP DATABASE IF EXISTS `+pgx.Identifier{db}.Sanitize()+` WITH (FORCE)`)
	})

	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.Path = "/" + db
	dsn := u.String()
	adminExec(t, dsn, schema)
	return dsn
}

// Counts Metricule-managed triggers on the given public tables.
func managedTriggersOn(t *testing.T, dsn, schema string, tables []string) int {
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
		SELECT count(DISTINCT c.relname)
		FROM pg_trigger t
		JOIN pg_class c ON c.oid = t.tgrelid
		JOIN pg_proc p ON p.oid = t.tgfoid
		JOIN pg_namespace fn ON fn.oid = p.pronamespace
		WHERE fn.nspname = $1 AND p.proname = $2 AND c.relname = ANY($3) AND NOT t.tgisinternal`,
		schema, triggers.FunctionName, tables).Scan(&n)
	if err != nil {
		t.Fatalf("count managed triggers: %v", err)
	}
	return n
}

// Proves the PostgreSQL-backed Sonarr and Radarr examples run against their real
// quoted-identifier schemas, and that managed triggers notify for those tables.
func TestArrPostgresExamples(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		source   string
		schema   string
		readEnv  string
		adminEnv string
		channel  string
		tables   []string
		touch    string
		payload  string
		want     map[string][]string
	}{
		{
			name:     "sonarr",
			path:     "../../../examples/sonarr-postgres/metricule.yaml",
			source:   "sonarr",
			schema:   sonarrSchema,
			readEnv:  "SONARR_DATABASE_URL",
			adminEnv: "SONARR_ADMIN_DATABASE_URL",
			channel:  "metricule_sonarr_changed",
			tables:   []string{"Series", "Episodes", "EpisodeFiles", "History", "Blocklist", "PendingReleases"},
			touch:    `INSERT INTO "Blocklist" ("SeriesId", "SourceTitle") VALUES (3, 'Firefly.S01E01.CAM')`,
			payload:  "public.Blocklist",
			want: map[string][]string{
				"sonarr_series": {
					`sonarr_series{monitored="true",status="continuing"} 1`,
					`sonarr_series{monitored="true",status="ended"} 1`,
					`sonarr_series{monitored="false",status="ended"} 1`,
				},
				"sonarr_wanted_missing_episodes": {"sonarr_wanted_missing_episodes 2"},
				"sonarr_episode_files": {
					`sonarr_episode_files{quality="HDTV-1080p"} 2`,
					`sonarr_episode_files{quality="HDTV-720p"} 1`,
					`sonarr_episode_files{quality="unknown"} 1`,
				},
				"sonarr_episode_file_bytes": {
					`sonarr_episode_file_bytes{quality="HDTV-1080p"} 3.221225472e+09`,
					`sonarr_episode_file_bytes{quality="HDTV-720p"} 5.36870912e+08`,
				},
				"sonarr_history_events_24h": {
					`sonarr_history_events_24h{event="grabbed"} 2`,
					`sonarr_history_events_24h{event="download_folder_imported"} 1`,
				},
				"sonarr_blocklist_items":  {"sonarr_blocklist_items 2"},
				"sonarr_pending_releases": {"sonarr_pending_releases 1"},
			},
		},
		{
			name:     "radarr",
			path:     "../../../examples/radarr-postgres/metricule.yaml",
			source:   "radarr",
			schema:   radarrSchema,
			readEnv:  "RADARR_DATABASE_URL",
			adminEnv: "RADARR_ADMIN_DATABASE_URL",
			channel:  "metricule_radarr_changed",
			tables:   []string{"Movies", "MovieMetadata", "MovieFiles", "History", "Blocklist", "PendingReleases"},
			touch:    `UPDATE "Movies" SET "MovieFileId" = 2 WHERE "MovieMetadataId" = 3`,
			payload:  "public.Movies",
			want: map[string][]string{
				"radarr_movies": {
					`radarr_movies{monitored="true",status="released"} 2`,
					`radarr_movies{monitored="true",status="in_cinemas"} 1`,
					`radarr_movies{monitored="true",status="announced"} 1`,
					`radarr_movies{monitored="false",status="released"} 1`,
				},
				"radarr_wanted_missing_movies": {"radarr_wanted_missing_movies 2"},
				"radarr_movie_files": {
					`radarr_movie_files{quality="Bluray-1080p"} 1`,
					`radarr_movie_files{quality="WEBDL-1080p"} 1`,
				},
				"radarr_movie_file_bytes": {
					`radarr_movie_file_bytes{quality="Bluray-1080p"} 8.589934592e+09`,
				},
				"radarr_history_events_24h": {
					`radarr_history_events_24h{event="grabbed"} 1`,
					`radarr_history_events_24h{event="movie_file_deleted"} 1`,
				},
				"radarr_blocklist_items":  {"radarr_blocklist_items 1"},
				"radarr_pending_releases": {"radarr_pending_releases 0"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := createArrDatabase(t, tc.name+"_main", tc.schema)
			t.Setenv(tc.readEnv, dsn)
			t.Setenv(tc.adminEnv, dsn)

			cfg, diags, err := config.Load(tc.path)
			if err != nil {
				t.Fatalf("load %s: %v; diagnostics: %v", tc.path, err, diags)
			}
			if len(cfg.Metrics) != len(tc.want) {
				t.Errorf("example defines %d metrics but the test asserts %d", len(cfg.Metrics), len(tc.want))
			}

			for name, wants := range tc.want {
				report, err := runtime.CollectOnce(context.Background(), cfg, name, nil)
				if err != nil {
					t.Errorf("collect %s: %v", name, err)
					continue
				}
				for _, w := range wants {
					if !strings.Contains(report.Exposition, w) {
						t.Errorf("%s exposition missing %q, got:\n%s", name, w, report.Exposition)
					}
				}
			}

			// The example's port may be taken; the trigger path is what is under test.
			cfg.Server.Listen = "127.0.0.1:0"
			rt, err := runtime.New(cfg, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatalf("runtime.New: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- rt.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(30 * time.Second):
					t.Error("Runtime.Run did not return after cancellation")
				}
			})

			schema := cfg.Sources[tc.source].Postgres.Triggers.Schema
			if err := waitFor(30*time.Second, func() error {
				if got := managedTriggersOn(t, dsn, schema, tc.tables); got != len(tc.tables) {
					return fmt.Errorf("%d of %d watched tables have a managed trigger", got, len(tc.tables))
				}
				return nil
			}); err != nil {
				t.Fatalf("startup did not provision triggers: %v", err)
			}

			conn := listen(t, dsn, tc.channel)
			adminExec(t, dsn, tc.touch)
			awaitPayload(t, conn, tc.payload)
		})
	}
}
