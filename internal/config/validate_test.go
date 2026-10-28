package config

import (
	"os"
	"strings"
	"testing"

	"github.com/teagan42/metricule/internal/source"
)

// §5.5 validation matrix. Every bullet of the specification's validation list has at
// least one case here, tagged with the bullet it covers.
//
// Expected paths and messages are spelled out literally. Deriving them from the
// validator would make the assertions tautological.

// Builds a document with one sqlite source and the supplied metrics list body.
func sqliteDoc(metrics string) string {
	return minimalHeader + "metrics:\n" + metrics
}

// Builds a document with one http source. sourceFields is indented four spaces.
func httpDoc(sourceFields, metrics string) string {
	return `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
    base_url: http://app:8080
` + sourceFields + "metrics:\n" + metrics
}

// Builds a document with one postgres source. sourceFields is indented four spaces.
func postgresDoc(sourceFields, metrics string) string {
	return `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  pg:
    type: postgres
    dsn:
      value: postgres://localhost/app
` + sourceFields + "metrics:\n" + metrics
}

const sqliteGauge = `  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`

const httpGauge = `  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      path: /status
    mapping:
      value: "$.value"
    collect:
      mode: poll
      interval: 30s
`

const postgresGauge = `  - name: app_thing
    help: A thing
    type: gauge
    source: pg
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`

// Rejections that happen during decoding, before any diagnostic is produced.
//
// Decode errors are reported as a plain error rather than Diagnostics, so these cases
// assert on the error text instead of a path.
func TestDecodeRejections(t *testing.T) {
	cases := []struct {
		name string
		// rule names the §5.5 bullet or §5.2 requirement under test.
		rule string
		doc  string
		want string
	}{
		{
			name: "unsupported_source_type",
			rule: "unsupported source types",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  db:
    type: oracle
metrics: []
`,
			want: `unsupported source type "oracle"`,
		},
		{
			name: "source_type_missing",
			rule: "unsupported source types",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  db:
    path: /data/app.db
metrics: []
`,
			want: `source is missing required field "type"`,
		},
		{
			name: "source_not_a_mapping",
			rule: "invalid types",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  db: /data/app.db
metrics: []
`,
			want: "source must be a mapping",
		},
		{
			name: "metrics_not_a_sequence",
			rule: "invalid types",
			doc:  minimalHeader + "metrics: not-a-list\n",
			want: "parse config",
		},
		{
			name: "duration_given_as_integer",
			rule: "invalid duration formats",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30
`),
			want: `invalid duration "30": time: missing unit`,
		},
		{
			name: "duration_unparseable",
			rule: "invalid duration formats",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30 seconds
`),
			want: `invalid duration "30 seconds"`,
		},
		{
			name: "unknown_root_key",
			rule: "unknown keys",
			doc:  minimalHeader + "metrics:\n" + sqliteGauge + "retention: 7d\n",
			want: "retention",
		},
		{
			name: "unknown_mapping_key",
			rule: "unknown keys",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
      aggregate: sum
    collect:
      mode: poll
      interval: 30s
`),
			want: `unknown mapping field "aggregate"`,
		},
		{
			name: "unknown_collect_key",
			rule: "unknown keys",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
      backoff: 5s
`),
			want: "backoff",
		},
		{
			name: "foreign_source_key",
			rule: "unknown keys",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
    base_url: http://app:8080
    busy_timeout: 3s
metrics:
` + httpGauge,
			want: "busy_timeout",
		},
		{
			name: "empty_document",
			rule: "§5.2 required root properties",
			doc:  "",
			want: "configuration is empty",
		},
		{
			name: "multiple_documents",
			rule: "§5.2 required root properties",
			doc:  minimalHeader + "metrics:\n" + sqliteGauge + "---\napiVersion: metricule.io/v1alpha1\n",
			want: "more than one YAML document",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Parse([]byte(tc.doc), source.CapabilitiesFor)
			if err == nil {
				t.Fatalf("%s: configuration must be rejected", tc.rule)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: error should contain %q, got: %v", tc.rule, tc.want, err)
			}
		})
	}
}

// Rejections reported as diagnostics, anchored to a configuration path.
func TestValidationMatrix(t *testing.T) {
	cases := []struct {
		name string
		// rule names the §5.5 bullet under test.
		rule string
		doc  string
		path string
		want string
	}{
		// --- §5.2 required root properties ---
		{
			name: "api_version_missing",
			rule: "§5.2 required root properties",
			doc:  "kind: Exporter\nsources:\n  db:\n    type: sqlite\n    path: /data/app.db\nmetrics:\n" + sqliteGauge,
			path: "apiVersion",
			want: "required field is missing",
		},
		{
			name: "api_version_unsupported",
			rule: "§5.2 required root properties",
			doc: `apiVersion: metricule.io/v1
kind: Exporter
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge,
			path: "apiVersion",
			want: `unsupported version "metricule.io/v1"`,
		},
		{
			name: "kind_unsupported",
			rule: "§5.2 required root properties",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Deployment
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge,
			path: "kind",
			want: `unsupported kind "Deployment"`,
		},
		{
			name: "sources_empty",
			rule: "missing sources",
			doc:  "apiVersion: metricule.io/v1alpha1\nkind: Exporter\nsources: {}\nmetrics: []\n",
			path: "sources",
			want: "at least one source is required",
		},
		{
			name: "metrics_absent",
			rule: "§5.2 required root properties",
			doc:  minimalHeader,
			path: "metrics",
			want: "at least one metric is required",
		},

		// --- duplicate metric names / conflicting output families ---
		{
			name: "duplicate_metric_name_names_the_earlier_index",
			rule: "duplicate metric names",
			doc: sqliteDoc(sqliteGauge + `  - name: app_thing
    help: A different thing
    type: counter
    source: db
    query:
      sql: SELECT 2 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[1].name",
			want: `duplicate metric name "app_thing", already declared by metrics[0]`,
		},
		{
			name: "conflicting_label_sets_on_one_family",
			rule: "duplicate or conflicting output families",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value, 'x' AS tier_col
    mapping:
      value: value
      labels:
        tier: tier_col
      static_labels:
        tier: primary
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.static_labels.tier",
			want: `label "tier" is also declared in labels`,
		},

		// --- missing sources / invalid references ---
		{
			name: "metric_references_undeclared_source",
			rule: "missing sources",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: warehouse
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].source",
			want: `unknown source "warehouse"`,
		},
		{
			name: "metric_source_missing",
			rule: "missing sources",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].source",
			want: "required field is missing",
		},
		{
			name: "source_name_is_not_a_valid_label",
			rule: "invalid Prometheus names and labels",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  app-db:
    type: sqlite
    path: /data/app.db
metrics:
  - name: app_thing
    help: A thing
    type: gauge
    source: app-db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`,
			path: "sources.app-db",
			want: "used as a telemetry label",
		},

		// --- unsupported mapping types ---
		{
			name: "metric_type_unsupported",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: summary
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].type",
			want: `unsupported type "summary" (supported: gauge, counter, state_set, histogram)`,
		},
		{
			name: "histogram_requires_bucket_fields",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_duration
    help: Duration
    type: histogram
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.buckets",
			want: "require a bucket-bound field",
		},
		{
			name: "gauge_rejects_histogram_fields",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
      buckets: le
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.buckets",
			want: "apply only to histogram metrics",
		},
		{
			name: "metric_name_count_suffix_reserved",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_request_count
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].name",
			want: `suffix "_count" is reserved`,
		},
		{
			name: "metric_type_missing",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].type",
			want: "required field is missing",
		},
		{
			name: "null_policy_unsupported",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
      null: zero
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.null",
			want: `unsupported policy "zero" (supported: fail, omit)`,
		},
		{
			name: "states_on_a_gauge",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
      states:
        - up
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.states",
			want: "states apply only to state_set metrics",
		},
		{
			name: "state_set_without_states",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_state
    help: Service state
    type: state_set
    source: db
    query:
      sql: SELECT 'up' AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.states",
			want: "require an explicit state list",
		},
		{
			name: "duplicate_state",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_state
    help: Service state
    type: state_set
    source: db
    query:
      sql: SELECT 'up' AS value
    mapping:
      value: value
      states:
        - up
        - up
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.states[1]",
			want: `duplicate state "up"`,
		},
		{
			name: "mapping_value_missing",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      labels:
        tier: tier_col
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.value",
			want: "required field is missing",
		},
		{
			name: "sql_label_column_empty",
			rule: "unsupported mapping types",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
      labels:
        tier: ""
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.labels.tier",
			want: "column name must not be empty",
		},
		{
			name: "rows_on_a_sql_source",
			rule: "unsupported features for an adapter",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      rows: "$.items[*]"
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.rows",
			want: "rows selection applies only to http sources",
		},

		// --- invalid Prometheus names and labels ---
		{
			name: "metric_name_has_illegal_characters",
			rule: "invalid Prometheus names and labels",
			doc: sqliteDoc(`  - name: "app thing"
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].name",
			want: `metric name "app thing" must match`,
		},
		{
			name: "metric_name_missing",
			rule: "invalid Prometheus names and labels",
			doc: sqliteDoc(`  - help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].name",
			want: "required field is missing",
		},
		{
			name: "help_missing",
			rule: "invalid Prometheus names and labels",
			doc: sqliteDoc(`  - name: app_thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].help",
			want: "required field is missing",
		},
		{
			name: "label_name_has_illegal_characters",
			rule: "invalid Prometheus names and labels",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value, 'x' AS tier_col
    mapping:
      value: value
      labels:
        job-tier: tier_col
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.labels.job-tier",
			want: `label name "job-tier" must match`,
		},
		{
			name: "static_label_name_has_illegal_characters",
			rule: "invalid Prometheus names and labels",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
      static_labels:
        job-tier: primary
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.static_labels.job-tier",
			want: `label name "job-tier" must match`,
		},

		// --- reserved naming collisions ---
		{
			name: "telemetry_prefix_reserved",
			rule: "reserved naming collisions",
			doc: sqliteDoc(`  - name: metricule_custom_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].name",
			want: `prefix "metricule_" is reserved`,
		},
		{
			name: "bucket_suffix_reserved",
			rule: "reserved naming collisions",
			doc: sqliteDoc(`  - name: app_latency_bucket
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].name",
			want: `suffix "_bucket" is reserved for histogram and summary families`,
		},
		{
			name: "sum_suffix_reserved",
			rule: "reserved naming collisions",
			doc: sqliteDoc(`  - name: app_latency_sum
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].name",
			want: `suffix "_sum" is reserved for histogram and summary families`,
		},
		{
			name: "double_underscore_label_reserved",
			rule: "reserved naming collisions",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value, 'x' AS tier_col
    mapping:
      value: value
      labels:
        __tier: tier_col
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.labels.__tier",
			want: "the __ prefix is reserved by Prometheus",
		},
		{
			name: "state_label_reserved_in_labels",
			rule: "reserved naming collisions",
			doc: sqliteDoc(`  - name: app_state
    help: Service state
    type: state_set
    source: db
    query:
      sql: SELECT 'up' AS value
    mapping:
      value: value
      labels:
        state: value
      states:
        - up
        - down
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.labels.state",
			want: `label "state" is generated for state_set metrics`,
		},
		{
			name: "state_label_reserved_in_static_labels",
			rule: "reserved naming collisions",
			doc: sqliteDoc(`  - name: app_state
    help: Service state
    type: state_set
    source: db
    query:
      sql: SELECT 'up' AS value
    mapping:
      value: value
      static_labels:
        state: up
      states:
        - up
        - down
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.static_labels.state",
			want: `label "state" is generated for state_set metrics`,
		},
		{
			name: "server_paths_collide",
			rule: "reserved naming collisions",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
server:
  health_path: /metrics
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge,
			// Paths are checked in sorted field order, so health_path claims /metrics
			// first and metrics_path reports the collision.
			path: "server.metrics_path",
			want: `path "/metrics" collides with server.health_path`,
		},
		{
			name: "server_path_is_relative",
			rule: "reserved naming collisions",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
server:
  metrics_path: metrics
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge,
			path: "server.metrics_path",
			want: `path "metrics" must begin with /`,
		},

		// --- incorrect collection configuration ---
		{
			name: "collect_mode_unsupported",
			rule: "incorrect collection configuration",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: streaming
`),
			path: "metrics[0].collect.mode",
			want: `unsupported mode "streaming" (supported: poll, notify, auto)`,
		},
		{
			name: "notify_without_channel",
			rule: "incorrect collection configuration",
			doc: postgresDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: pg
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: notify
`),
			path: "metrics[0].collect.channel",
			want: "notify mode requires a channel",
		},
		{
			name: "channel_in_poll_mode",
			rule: "incorrect collection configuration",
			doc: postgresDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: pg
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
      channel: app_changed
`),
			path: "metrics[0].collect.channel",
			want: "channel applies only to notify mode",
		},
		{
			name: "cleanup_without_manage",
			rule: "incorrect PostgreSQL configuration",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  pg:
    type: postgres
    dsn:
      value: postgres://u:p@db.invalid/app
    triggers:
      manage: false
      cleanup_on_shutdown: true
metrics: []
`,
			path: "sources.pg.triggers.cleanup_on_shutdown",
			want: "requires triggers.manage: true",
		},
		{
			name: "notify_with_unqualified_watch_table",
			rule: "incorrect collection configuration",
			doc: postgresDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: pg
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: notify
      channel: app_changed
      fallback_interval: 30s
      watch:
        tables:
          - jobs
`),
			path: "metrics[0].collect.watch.tables[0]",
			want: "must be schema-qualified",
		},
		{
			name: "notify_with_an_overlong_channel",
			rule: "incorrect collection configuration",
			doc: postgresDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: pg
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: notify
      channel: this_channel_name_is_far_too_long_for_a_postgresql_notification_identifier
      fallback_interval: 30s
`),
			path: "metrics[0].collect.channel",
			want: "63-byte identifier limit",
		},
		{
			name: "require_notifications_in_poll_mode",
			rule: "incorrect collection configuration",
			doc: postgresDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: pg
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
      require_notifications: true
`),
			path: "metrics[0].collect.require_notifications",
			want: "require_notifications applies only to notify and auto modes",
		},
		{
			name: "auto_without_channel_on_notifying_source",
			rule: "incorrect collection configuration",
			doc: postgresDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: pg
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: auto
      fallback_interval: 30s
`),
			path: "metrics[0].collect.channel",
			want: "auto mode on source type \"postgres\" resolves to notifications and requires a channel",
		},
		{
			name: "channel_on_a_source_without_notifications",
			rule: "incorrect collection configuration",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: auto
      channel: app_changed
      fallback_interval: 30s
`),
			path: "metrics[0].collect.channel",
			want: "source type \"sqlite\" does not support notification channels",
		},
		{
			name: "watch_in_poll_mode",
			rule: "incorrect collection configuration",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
      watch:
        tables:
          - users
`),
			path: "metrics[0].collect.watch",
			want: "watch applies only to notify and auto modes",
		},
		{
			name: "watch_event_unsupported",
			rule: "incorrect collection configuration",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: auto
      fallback_interval: 60s
      watch:
        events:
          - truncate
`),
			path: "metrics[0].collect.watch.events[0]",
			want: `unsupported event "truncate" (supported: insert, update, delete)`,
		},
		{
			name: "negative_debounce",
			rule: "incorrect collection configuration",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
      debounce: -1s
`),
			path: "metrics[0].collect.debounce",
			want: "must not be negative",
		},
		{
			name: "negative_jitter",
			rule: "incorrect collection configuration",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
      jitter: -1s
`),
			path: "metrics[0].collect.jitter",
			want: "must not be negative",
		},
		{
			name: "negative_interval",
			rule: "incorrect collection configuration",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: -30s
`),
			path: "metrics[0].collect.interval",
			want: "poll mode requires a positive interval",
		},
		{
			name: "mode_manual_reserved",
			rule: "incorrect collection configuration",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: manual
`),
			path: "metrics[0].collect.mode",
			want: `mode "manual" is reserved for a future release`,
		},
		{
			name: "mode_cdc_reserved",
			rule: "incorrect collection configuration",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: cdc
`),
			path: "metrics[0].collect.mode",
			want: `mode "cdc" is reserved for a future release`,
		},
		{
			name: "failure_mode_unsupported",
			rule: "incorrect collection configuration",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
defaults:
  failure:
    mode: freeze
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge,
			path: "defaults.failure.mode",
			want: `unsupported mode "freeze" (supported: retain, drop)`,
		},
		{
			name: "metric_failure_mode_unsupported",
			rule: "incorrect collection configuration",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
    failure:
      mode: freeze
`),
			path: "metrics[0].failure.mode",
			want: `unsupported mode "freeze" (supported: retain, drop)`,
		},

		// --- missing authentication references ---
		{
			name: "bearer_without_token",
			rule: "missing authentication references",
			doc: httpDoc(`    authentication:
      type: bearer
`, httpGauge),
			path: "sources.api.authentication.token",
			want: "secret reference is empty",
		},
		{
			name: "basic_without_username",
			rule: "missing authentication references",
			doc: httpDoc(`    authentication:
      type: basic
      password:
        env: APP_PASSWORD
`, httpGauge),
			path: "sources.api.authentication.username",
			want: "required for basic authentication",
		},
		{
			name: "basic_without_password",
			rule: "missing authentication references",
			doc: httpDoc(`    authentication:
      type: basic
      username: metrics
`, httpGauge),
			path: "sources.api.authentication.password",
			want: "secret reference is empty",
		},
		{
			name: "authentication_type_missing",
			rule: "missing authentication references",
			doc: httpDoc(`    authentication:
      token:
        env: APP_TOKEN
`, httpGauge),
			path: "sources.api.authentication.type",
			want: "required field is missing",
		},
		{
			name: "authentication_type_unsupported",
			rule: "missing authentication references",
			doc: httpDoc(`    authentication:
      type: mtls
`, httpGauge),
			path: "sources.api.authentication.type",
			want: `unsupported type "mtls" (supported: bearer, basic)`,
		},
		{
			name: "bearer_with_basic_fields",
			rule: "missing authentication references",
			doc: httpDoc(`    authentication:
      type: bearer
      token:
        env: APP_TOKEN
      username: metrics
`, httpGauge),
			path: "sources.api.authentication",
			want: "bearer authentication does not use username or password",
		},
		{
			name: "basic_with_token",
			rule: "missing authentication references",
			doc: httpDoc(`    authentication:
      type: basic
      username: metrics
      password:
        env: APP_PASSWORD
      token:
        env: APP_TOKEN
`, httpGauge),
			path: "sources.api.authentication",
			want: "basic authentication does not use token",
		},
		{
			name: "postgres_dsn_missing",
			rule: "missing authentication references",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  pg:
    type: postgres
metrics:
` + postgresGauge,
			path: "sources.pg.dsn",
			want: "secret reference is empty",
		},
		{
			name: "postgres_dsn_ambiguous",
			rule: "missing authentication references",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  pg:
    type: postgres
    dsn:
      env: APP_DATABASE_URL
      file: /run/secrets/app_database_url
metrics:
` + postgresGauge,
			path: "sources.pg.dsn",
			want: "secret reference is ambiguous",
		},
		{
			name: "trigger_dsn_ambiguous",
			rule: "missing authentication references",
			doc: postgresDoc(`    triggers:
      manage: true
      schema: metricule
      dsn:
        env: OWNER_URL
        value: postgres://owner@localhost/app
`, postgresGauge),
			path: "sources.pg.triggers.dsn",
			want: "secret reference is ambiguous",
		},

		// --- invalid selectors ---
		{
			name: "invalid_value_selector",
			rule: "invalid selectors",
			doc: httpDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      path: /status
    mapping:
      value: "$.["
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.value",
			want: `invalid JSONPath selector "$.["`,
		},
		{
			name: "invalid_rows_selector",
			rule: "invalid selectors",
			doc: httpDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      path: /status
    mapping:
      rows: "$.["
      value: "$.value"
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.rows",
			want: `invalid JSONPath selector "$.["`,
		},
		{
			name: "invalid_label_selector",
			rule: "invalid selectors",
			doc: httpDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      path: /status
    mapping:
      value: "$.value"
      labels:
        worker: "$.["
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].mapping.labels.worker",
			want: `invalid JSONPath selector "$.["`,
		},

		// --- invalid cardinality limits ---
		{
			name: "negative_series_limit",
			rule: "invalid cardinality limits",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
defaults:
  limits:
    max_series_per_metric: -1
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge,
			path: "defaults.limits.max_series_per_metric",
			want: "must be greater than zero, got -1",
		},
		{
			name: "negative_row_limit",
			rule: "invalid cardinality limits",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
defaults:
  limits:
    max_rows_per_query: -5
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge,
			path: "defaults.limits.max_rows_per_query",
			want: "must be greater than zero, got -5",
		},
		{
			name: "negative_label_value_limit",
			rule: "invalid cardinality limits",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
defaults:
  limits:
    max_label_value_bytes: -1
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge,
			path: "defaults.limits.max_label_value_bytes",
			want: "must be greater than zero, got -1",
		},
		{
			name: "series_limit_exceeds_global_budget",
			rule: "invalid cardinality limits",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
defaults:
  limits:
    max_series_per_metric: 2000
    max_total_series: 1000
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge,
			path: "defaults.limits.max_series_per_metric",
			want: "2000 exceeds max_total_series 1000",
		},
		{
			name: "metric_limit_overrides_are_validated",
			rule: "invalid cardinality limits",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
    limits:
      max_concurrent_collections: -2
`),
			path: "metrics[0].limits.max_concurrent_collections",
			want: "must be greater than zero, got -2",
		},
		{
			name: "metric_count_exceeds_max_metrics",
			rule: "invalid cardinality limits",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
defaults:
  limits:
    max_metrics: 1
sources:
  db:
    type: sqlite
    path: /data/app.db
metrics:
` + sqliteGauge + `  - name: app_other_thing
    help: Another thing
    type: gauge
    source: db
    query:
      sql: SELECT 2 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`,
			path: "metrics",
			want: "2 metrics exceeds defaults.limits.max_metrics 1",
		},

		// --- unsupported features for an adapter ---
		{
			name: "notify_on_sqlite_points_at_auto",
			rule: "unsupported features for an adapter",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: notify
      channel: app_changed
      fallback_interval: 60s
`),
			path: "metrics[0].collect.mode",
			want: `source type "sqlite" does not support notifications; use mode "auto"`,
		},
		{
			name: "notify_on_http",
			rule: "unsupported features for an adapter",
			doc: httpDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      path: /status
    mapping:
      value: "$.value"
    collect:
      mode: notify
      channel: app_changed
      fallback_interval: 60s
`),
			path: "metrics[0].collect.mode",
			want: `source type "http" does not support notifications`,
		},
		{
			name: "query_block_on_http_source",
			rule: "unsupported features for an adapter",
			doc: httpDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: api
    query:
      sql: SELECT 1 AS value
    mapping:
      value: "$.value"
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].query",
			want: "does not accept a query block",
		},
		{
			name: "request_block_on_sql_source",
			rule: "unsupported features for an adapter",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    request:
      path: /status
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].request",
			want: "does not accept a request block",
		},
		{
			name: "query_block_missing_on_sql_source",
			rule: "unsupported features for an adapter",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].query",
			want: "requires a query block",
		},
		{
			name: "empty_sql",
			rule: "unsupported features for an adapter",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: "   "
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].query.sql",
			want: "required field is empty",
		},
		{
			name: "request_method_not_get",
			rule: "unsupported features for an adapter",
			doc: httpDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      method: POST
      path: /status
    mapping:
      value: "$.value"
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].request.method",
			want: `only GET is supported in v0.1, got "POST"`,
		},
		{
			name: "request_path_missing",
			rule: "unsupported features for an adapter",
			doc: httpDoc("", `  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      method: GET
    mapping:
      value: "$.value"
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].request.path",
			want: "required field is missing",
		},
		{
			name: "request_path_outside_allowed_paths",
			rule: "unsupported features for an adapter",
			doc: httpDoc(`    allowed_paths:
      - /api/status
`, `  - name: app_thing
    help: A thing
    type: gauge
    source: api
    request:
      path: /api/secrets
    mapping:
      value: "$.value"
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].request.path",
			want: "is not in the source's allowed_paths",
		},
		{
			name: "allowed_path_is_relative",
			rule: "unsupported features for an adapter",
			doc: httpDoc(`    allowed_paths:
      - api/status
`, httpGauge),
			path: "sources.api.allowed_paths[0]",
			want: `path "api/status" must begin with /`,
		},
		{
			name: "http_base_url_missing",
			rule: "unsupported features for an adapter",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
metrics:
` + httpGauge,
			path: "sources.api.base_url",
			want: "required field is missing",
		},
		{
			name: "http_base_url_wrong_scheme",
			rule: "unsupported features for an adapter",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
    base_url: ftp://app:8080
metrics:
` + httpGauge,
			path: "sources.api.base_url",
			want: `scheme must be http or https, got "ftp"`,
		},
		{
			name: "http_base_url_has_no_host",
			rule: "unsupported features for an adapter",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  api:
    type: http
    base_url: "http://"
metrics:
` + httpGauge,
			path: "sources.api.base_url",
			want: "URL has no host",
		},
		{
			name: "negative_redirect_budget",
			rule: "unsupported features for an adapter",
			doc:  httpDoc("    max_redirects: -1\n", httpGauge),
			path: "sources.api.max_redirects",
			want: "must not be negative",
		},
		{
			name: "negative_retry_budget",
			rule: "unsupported features for an adapter",
			doc:  httpDoc("    retries: -1\n", httpGauge),
			path: "sources.api.retries",
			want: "must not be negative",
		},
		{
			name: "sqlite_path_missing",
			rule: "unsupported features for an adapter",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  db:
    type: sqlite
metrics:
` + sqliteGauge,
			path: "sources.db.path",
			want: "required field is missing",
		},
		{
			name: "sqlite_busy_timeout_negative",
			rule: "unsupported features for an adapter",
			doc: `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  db:
    type: sqlite
    path: /data/app.db
    busy_timeout: -1s
metrics:
` + sqliteGauge,
			path: "sources.db.busy_timeout",
			want: "must be greater than zero",
		},
		{
			name: "postgres_pool_idle_exceeds_open",
			rule: "unsupported features for an adapter",
			doc: postgresDoc(`    pool:
      max_open: 2
      max_idle: 8
`, postgresGauge),
			path: "sources.pg.pool.max_idle",
			want: "8 exceeds max_open 2",
		},
		{
			name: "postgres_pool_open_negative",
			rule: "unsupported features for an adapter",
			doc: postgresDoc(`    pool:
      max_open: -1
`, postgresGauge),
			path: "sources.pg.pool.max_open",
			want: "must be greater than zero",
		},
		{
			name: "postgres_pool_idle_negative",
			rule: "unsupported features for an adapter",
			doc: postgresDoc(`    pool:
      max_open: 4
      max_idle: -1
`, postgresGauge),
			path: "sources.pg.pool.max_idle",
			want: "must not be negative",
		},
		{
			name: "postgres_sslmode_unsupported",
			rule: "unsupported features for an adapter",
			doc: postgresDoc(`    tls:
      mode: paranoid
`, postgresGauge),
			path: "sources.pg.tls.mode",
			want: `unsupported sslmode "paranoid"`,
		},
		{
			name: "trigger_schema_not_an_identifier",
			rule: "unsupported features for an adapter",
			doc: postgresDoc(`    triggers:
      manage: true
      schema: "public; DROP TABLE users"
`, postgresGauge),
			path: "sources.pg.triggers.schema",
			want: "is not a plain identifier",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := parseInvalid(t, tc.doc)
			requireDiagnostic(t, ds, tc.path, tc.want)
		})
	}
}

// Warnings report convention violations without blocking the load (§5.5 vs §8.6).
func TestWarningsDoNotBlockLoading(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		path string
		want string
	}{
		{
			name: "counter_without_total_suffix",
			doc: sqliteDoc(`  - name: app_events
    help: Processed events
    type: counter
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].name",
			want: "counters conventionally end with _total",
		},
		{
			name: "mixed_case_metric_name",
			doc: sqliteDoc(`  - name: AppThing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 30s
`),
			path: "metrics[0].name",
			want: "conventionally lowercase snake_case",
		},
		{
			name: "timeout_not_shorter_than_interval",
			doc: sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: poll
      interval: 5s
      timeout: 10s
`),
			path: "metrics[0].collect.timeout",
			want: "collections can overlap continuously",
		},
		{
			name: "trigger_management_reuses_collection_dsn",
			doc: postgresDoc(`    triggers:
      manage: true
`, postgresGauge),
			path: "sources.pg.triggers.dsn",
			want: "should not hold schema-owner privileges",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ds, err := Parse([]byte(tc.doc), source.CapabilitiesFor)
			if err != nil {
				t.Fatalf("a convention violation must not block loading: %v", err)
			}
			if ds.HasErrors() {
				t.Fatalf("expected warnings only, got errors:\n%s", ds)
			}
			requireDiagnostic(t, ds.Warnings(), tc.path, tc.want)
		})
	}
}

// Offline validation reads neither the environment nor the filesystem (§15.1).
//
// Unresolved references therefore surface at startup, where internal/runtime resolves
// them, not here. `metricule validate` must stay runnable in CI without production
// secrets.
func TestOfflineValidationDoesNotResolveSecrets(t *testing.T) {
	doc := `apiVersion: metricule.io/v1alpha1
kind: Exporter
sources:
  pg:
    type: postgres
    dsn:
      env: METRICULE_TEST_UNSET_VARIABLE
  api:
    type: http
    base_url: http://app:8080
    authentication:
      type: bearer
      token:
        file: /nonexistent/metricule/token
metrics:
` + postgresGauge

	if _, err := os.Stat("/nonexistent/metricule/token"); err == nil {
		t.Skip("fixture path unexpectedly exists")
	}
	if _, ok := os.LookupEnv("METRICULE_TEST_UNSET_VARIABLE"); ok {
		t.Skip("fixture environment variable unexpectedly set")
	}

	if _, _, err := Parse([]byte(doc), source.CapabilitiesFor); err != nil {
		t.Fatalf("offline validation must not resolve secret references: %v", err)
	}
}

// Diagnostics carry a severity, a path, and a message, in that textual order.
//
// Operators and CI parse this output, so its shape is part of the CLI contract (§15).
func TestDiagnosticStringFormat(t *testing.T) {
	d := Diagnostic{Severity: SeverityError, Path: "metrics[0].name", Message: "boom"}
	if got, want := d.String(), "error: metrics[0].name: boom"; got != want {
		t.Errorf("diagnostic rendered %q, want %q", got, want)
	}
}

// Every error diagnostic reaches the error returned to the CLI.
func TestValidationErrorListsEveryErrorDiagnostic(t *testing.T) {
	doc := sqliteDoc(`  - name: "bad name"
    help: A thing
    type: summary
    source: warehouse
    mapping:
      value: value
    collect:
      mode: streaming
`)
	_, ds, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err == nil {
		t.Fatal("expected rejection")
	}
	for _, d := range ds.Errors() {
		if !strings.Contains(err.Error(), d.Path) || !strings.Contains(err.Error(), d.Message) {
			t.Errorf("error text omits diagnostic %s", d)
		}
	}
	if n := len(ds.Errors()); n < 4 {
		t.Errorf("expected at least 4 error diagnostics, got %d:\n%s", n, ds)
	}
}

// Capability checks are skipped when no capability function is supplied, so that
// config validation stays testable without linking adapters.
func TestNilCapabilityFuncSkipsAdapterChecks(t *testing.T) {
	doc := sqliteDoc(`  - name: app_thing
    help: A thing
    type: gauge
    source: db
    query:
      sql: SELECT 1 AS value
    mapping:
      value: value
    collect:
      mode: notify
      channel: app_changed
      fallback_interval: 60s
`)
	if _, _, err := Parse([]byte(doc), nil); err != nil {
		t.Fatalf("nil caps must skip capability checks: %v", err)
	}
	if _, _, err := Parse([]byte(doc), source.CapabilitiesFor); err == nil {
		t.Fatal("the same document must be rejected once capabilities are known")
	}
}

// An adapter absent from the registry must be refused rather than silently skipped.
func TestUnregisteredAdapterRejected(t *testing.T) {
	doc := sqliteDoc(sqliteGauge)
	cfg, _, err := Parse([]byte(doc), source.CapabilitiesFor)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	empty := func(string) (source.Capabilities, bool) { return source.Capabilities{}, false }
	ds := Validate(cfg, empty)
	requireDiagnostic(t, ds, "metrics[0].source", "has no registered adapter in this build")
}
