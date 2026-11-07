package runtime_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/runtime"
)

// Fixtures follow the Sonarr and Radarr v3 OpenAPI schemas. Only the fields the
// example selectors read are populated, plus the shapes that break them: an
// unknown-series queue item with a null series, and a nested quality object.
var sonarrFixtures = map[string]string{
	"/api/v3/queue/status": `{
		"totalCount": 7, "count": 6, "unknownCount": 1,
		"errors": true, "warnings": false,
		"unknownErrors": false, "unknownWarnings": true
	}`,
	"/api/v3/wanted/missing": `{"page":1,"pageSize":1,"totalRecords":142,"records":[]}`,
	"/api/v3/wanted/cutoff":  `{"page":1,"pageSize":1,"totalRecords":9,"records":[]}`,
	// Matches includeUnknownSeriesItems=false: every record has a series.
	"/api/v3/queue": `{
		"page": 1, "pageSize": 200, "totalRecords": 2,
		"records": [
			{
				"id": 1, "title": "Severance.S04E07.1080p.HDTV", "size": 2147483648, "sizeleft": 1073741824,
				"seasonNumber": 4, "protocol": "torrent", "status": "downloading",
				"trackedDownloadStatus": "ok", "trackedDownloadState": "downloading",
				"quality": {"quality": {"id": 9, "name": "HDTV-1080p"}},
				"series": {"id": 11, "title": "Severance"}
			},
			{
				"id": 2, "title": "The.Expanse.S02.COMPLETE.720p.WEBDL", "size": 5368709120, "sizeleft": 0,
				"seasonNumber": 2, "protocol": "usenet", "status": "completed",
				"trackedDownloadStatus": "warning", "trackedDownloadState": "importPending",
				"quality": {"quality": {"id": 3, "name": "WEBDL-720p"}},
				"series": {"id": 13, "title": "The Expanse"}
			}
		]
	}`,
	"/api/v3/calendar": `[
		{
			"id": 31, "title": "Pilot", "seasonNumber": 1, "episodeNumber": 1,
			"hasFile": true, "monitored": true, "airDateUtc": "2026-11-06T01:00:00Z",
			"series": {"id": 12, "title": "Andor"}
		},
		{
			"id": 32, "title": "Finale", "seasonNumber": 2, "episodeNumber": 12,
			"hasFile": false, "monitored": true, "airDateUtc": "2026-11-06T02:00:00Z",
			"series": {"id": 13, "title": "The Expanse"}
		}
	]`,
	"/api/v3/health": `[
		{"id": 1, "source": "IndexerStatusCheck", "type": "warning", "message": "Indexer unavailable"},
		{"id": 2, "source": "DownloadClientCheck", "type": "error", "message": "No download client"}
	]`,
	"/api/v3/diskspace": `[
		{"id": 1, "path": "/tv", "label": "media", "freeSpace": 1099511627776, "totalSpace": 4398046511104}
	]`,
}

var radarrFixtures = map[string]string{
	"/api/v3/queue/status": `{
		"totalCount": 3, "count": 3, "unknownCount": 0,
		"errors": false, "warnings": true,
		"unknownErrors": false, "unknownWarnings": false
	}`,
	"/api/v3/wanted/missing": `{"page":1,"pageSize":1,"totalRecords":58,"records":[]}`,
	"/api/v3/wanted/cutoff":  `{"page":1,"pageSize":1,"totalRecords":4,"records":[]}`,
	// Matches includeUnknownMovieItems=false: every record has a movie.
	"/api/v3/queue": `{
		"page": 1, "pageSize": 200, "totalRecords": 2,
		"records": [
			{
				"id": 1, "title": "Dune.Part.Two.2024.1080p.BluRay", "size": 8589934592, "sizeleft": 4294967296,
				"protocol": "torrent", "status": "downloading",
				"trackedDownloadStatus": "ok", "trackedDownloadState": "downloading",
				"quality": {"quality": {"id": 7, "name": "Bluray-1080p"}},
				"movie": {"id": 41, "title": "Dune: Part Two", "titleSlug": "dune-part-two-693134", "tmdbId": 693134}
			},
			{
				"id": 2, "title": "Sinners.2025.720p.WEBDL", "size": 1073741824, "sizeleft": 0,
				"protocol": "usenet", "status": "completed",
				"trackedDownloadStatus": "ok", "trackedDownloadState": "importPending",
				"quality": {"quality": {"id": 3, "name": "WEBDL-720p"}},
				"movie": {"id": 42, "title": "Sinners", "titleSlug": "sinners-123456", "tmdbId": 123456}
			}
		]
	}`,
	"/api/v3/calendar": `[
		{"id": 51, "title": "Mickey 17", "year": 2025, "hasFile": false, "monitored": true},
		{"id": 52, "title": "Sinners", "year": 2025, "hasFile": true, "monitored": true}
	]`,
	"/api/v3/health": `[
		{"id": 1, "source": "RootFolderCheck", "type": "error", "message": "Missing root folder"}
	]`,
	"/api/v3/diskspace": `[
		{"id": 1, "path": "/movies", "label": "media", "freeSpace": 549755813888, "totalSpace": 2199023255552}
	]`,
}

// Serves the fixture set and asserts every request carried the API key.
func arrServer(t *testing.T, fixtures map[string]string, wantKey string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != wantKey {
			t.Errorf("%s: X-Api-Key = %q, want %q", r.URL.Path, got, wantKey)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if _, ok := r.Header["Authorization"]; ok {
			t.Errorf("%s: api_key auth must not send Authorization", r.URL.Path)
		}
		body, ok := fixtures[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Proves the shipped examples extract real Sonarr and Radarr payloads, so the
// Varken replacement is exercised rather than merely schema-valid.
func TestArrExamplesCollect(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		source   string
		envVar   string
		key      string
		fixtures map[string]string
		want     map[string][]string
	}{
		{
			name:     "sonarr",
			path:     "../../examples/sonarr/metricule.yaml",
			source:   "sonarr",
			envVar:   "SONARR_API_KEY",
			key:      "sonarr-test-key",
			fixtures: sonarrFixtures,
			want: map[string][]string{
				"sonarr_queue_items":                  {"sonarr_queue_items 7"},
				"sonarr_queue_items_unknown":          {"sonarr_queue_items_unknown 1"},
				"sonarr_queue_errored":                {"sonarr_queue_errored 1"},
				"sonarr_queue_warned":                 {"sonarr_queue_warned 0"},
				"sonarr_wanted_missing_episodes":      {"sonarr_wanted_missing_episodes 142"},
				"sonarr_wanted_cutoff_unmet_episodes": {"sonarr_wanted_cutoff_unmet_episodes 9"},
				"sonarr_queue_item_size_bytes": {
					`series="Severance"`, `release="Severance.S04E07.1080p.HDTV"`, `season="4"`,
					`quality="HDTV-1080p"`, `protocol="torrent"`,
					`tracked_state="downloading"`, `2.147483648e+09`,
					`series="The Expanse"`, `tracked_status="warning"`,
				},
				"sonarr_queue_item_remaining_bytes": {`series="Severance"`, `1.073741824e+09`},
				"sonarr_calendar_episode_has_file": {
					`series="Andor"`, `episode="Pilot"`, `number="1"`,
					`series="The Expanse"`, `episode="Finale"`,
				},
				"sonarr_health_issue": {
					`check="IndexerStatusCheck",state="warning"} 1`,
					`check="DownloadClientCheck",state="error"} 1`,
				},
				"sonarr_disk_free_bytes":  {`path="/tv"`, `1.099511627776e+12`},
				"sonarr_disk_total_bytes": {`path="/tv"`, `4.398046511104e+12`},
			},
		},
		{
			name:     "radarr",
			path:     "../../examples/radarr/metricule.yaml",
			source:   "radarr",
			envVar:   "RADARR_API_KEY",
			key:      "radarr-test-key",
			fixtures: radarrFixtures,
			want: map[string][]string{
				"radarr_queue_items":                {"radarr_queue_items 3"},
				"radarr_queue_items_unknown":        {"radarr_queue_items_unknown 0"},
				"radarr_queue_errored":              {"radarr_queue_errored 0"},
				"radarr_queue_warned":               {"radarr_queue_warned 1"},
				"radarr_wanted_missing_movies":      {"radarr_wanted_missing_movies 58"},
				"radarr_wanted_cutoff_unmet_movies": {"radarr_wanted_cutoff_unmet_movies 4"},
				"radarr_queue_item_size_bytes": {
					`movie="Dune: Part Two"`, `title_slug="dune-part-two-693134"`,
					`release="Dune.Part.Two.2024.1080p.BluRay"`,
					`quality="Bluray-1080p"`, `8.589934592e+09`, `movie="Sinners"`,
				},
				"radarr_queue_item_remaining_bytes": {`movie="Dune: Part Two"`, `4.294967296e+09`},
				"radarr_calendar_movie_has_file": {
					`movie="Mickey 17",year="2025"`, `movie="Sinners",year="2025"`,
				},
				"radarr_health_issue":     {`check="RootFolderCheck",state="error"} 1`},
				"radarr_disk_free_bytes":  {`path="/movies"`, `5.49755813888e+11`},
				"radarr_disk_total_bytes": {`path="/movies"`, `2.199023255552e+12`},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envVar, tc.key)
			srv := arrServer(t, tc.fixtures, tc.key)

			cfg, _, err := config.Load(tc.path)
			if err != nil {
				t.Fatalf("load %s: %v", tc.path, err)
			}
			// The example targets a service name no test can resolve.
			cfg.Sources[tc.source].HTTP.BaseURL = srv.URL

			if len(cfg.Metrics) != len(tc.want) {
				t.Errorf("example defines %d metrics but the test asserts %d", len(cfg.Metrics), len(tc.want))
			}

			for name, wants := range tc.want {
				report, err := runtime.CollectOnce(context.Background(), cfg, name, nil)
				if err != nil {
					t.Errorf("collect %s: %v", name, err)
					continue
				}
				if report.Samples == 0 {
					t.Errorf("%s produced no samples", name)
				}
				for _, w := range wants {
					if !strings.Contains(report.Exposition, w) {
						t.Errorf("%s exposition missing %q, got:\n%s", name, w, report.Exposition)
					}
				}
			}
		})
	}
}

// Pins why the examples set includeUnknownSeriesItems=false.
//
// A label selector must resolve on every row. `null: omit` governs the value only,
// so one unknown-series item would fail the entire collection rather than drop a
// sample. If this behavior ever changes, the examples can carry unknown items.
func TestArrQueueLabelRequiresEveryRow(t *testing.T) {
	t.Setenv("SONARR_API_KEY", "k")

	fixtures := make(map[string]string, len(sonarrFixtures))
	for k, v := range sonarrFixtures {
		fixtures[k] = v
	}
	fixtures["/api/v3/queue"] = `{
		"page": 1, "pageSize": 200, "totalRecords": 1,
		"records": [
			{
				"id": 2, "title": "Unknown.Release.720p", "size": 5368709120, "sizeleft": 0,
				"seasonNumber": 0, "protocol": "usenet", "status": "completed",
				"trackedDownloadStatus": "warning", "trackedDownloadState": "importPending",
				"quality": {"quality": {"id": 3, "name": "WEBDL-720p"}},
				"series": null
			}
		]
	}`
	srv := arrServer(t, fixtures, "k")

	cfg, _, err := config.Load("../../examples/sonarr/metricule.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Sources["sonarr"].HTTP.BaseURL = srv.URL

	_, err = runtime.CollectOnce(context.Background(), cfg, "sonarr_queue_item_size_bytes", nil)
	if err == nil {
		t.Fatal("a null series label resolved; the examples may now include unknown items")
	}
	if !strings.Contains(err.Error(), "labels.series") {
		t.Errorf("error = %v, want it to name the unresolved label", err)
	}
}
