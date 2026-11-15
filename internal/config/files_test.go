package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fileHeader = "apiVersion: metricule.io/v1alpha1\nkind: Exporter\n"

// Returns a sqlite source and one gauge reading it, as a standalone file body.
func serviceFile(source, metric, metricExtra string) string {
	return fileHeader + `sources:
  ` + source + `:
    type: sqlite
    path: /data/` + source + `.db
metrics:
  - name: ` + metric + `
    help: Rows.
    type: gauge
    source: ` + source + `
    query:
      sql: SELECT COUNT(*) AS value FROM t
    mapping:
      value: value
` + metricExtra
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func loadInvalid(t *testing.T, paths ...string) Diagnostics {
	t.Helper()
	_, ds, err := Load(paths...)
	if err == nil {
		t.Fatalf("expected configuration to be rejected")
	}
	return ds
}

// A directory merges its top-level YAML files in name order, and shared settings
// from one file apply to metrics declared in another.
func TestLoadDirectoryMergesFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "00-base.yaml"), fileHeader+`server:
  listen: ":9999"
defaults:
  collection:
    interval: 45s
`)
	writeFile(t, filepath.Join(dir, "sonarr.yaml"), serviceFile("sonarr", "sonarr_rows", ""))
	writeFile(t, filepath.Join(dir, "radarr.yml"), serviceFile("radarr", "radarr_rows", ""))
	// None of these may be read: a hidden file, a subdirectory, a non-YAML file.
	writeFile(t, filepath.Join(dir, ".hidden.yaml"), "not: [valid")
	writeFile(t, filepath.Join(dir, "old", "sonarr.yaml"), serviceFile("sonarr", "sonarr_rows", ""))
	writeFile(t, filepath.Join(dir, "README.txt"), "notes")

	cfg, _, err := Load(dir)
	if err != nil {
		t.Fatalf("directory must load: %v", err)
	}
	if got := len(cfg.Sources); got != 2 {
		t.Errorf("sources = %d, want 2", got)
	}
	var names []string
	for _, m := range cfg.Metrics {
		names = append(names, m.Name)
	}
	if got, want := strings.Join(names, ","), "radarr_rows,sonarr_rows"; got != want {
		t.Errorf("metric order = %s, want %s (file name order)", got, want)
	}
	if cfg.Server.Listen != ":9999" {
		t.Errorf("server.listen = %q, want the value from 00-base.yaml", cfg.Server.Listen)
	}
	for _, m := range cfg.Metrics {
		if got := m.Collect.Interval.Duration().String(); got != "45s" {
			t.Errorf("%s interval = %s, want inherited 45s", m.Name, got)
		}
	}
}

// Files and directories can be mixed, and load in argument order.
func TestLoadMixedPathsKeepArgumentOrder(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.yaml")
	writeFile(t, first, serviceFile("zeta", "zeta_rows", ""))
	writeFile(t, filepath.Join(dir, "conf.d", "alpha.yaml"), serviceFile("alpha", "alpha_rows", ""))

	cfg, _, err := Load(first, filepath.Join(dir, "conf.d"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Metrics[0].Name != "zeta_rows" || cfg.Metrics[1].Name != "alpha_rows" {
		t.Errorf("metrics = %s, %s; want argument order", cfg.Metrics[0].Name, cfg.Metrics[1].Name)
	}
}

// Kubernetes mounts ConfigMap keys as symlinks into a hidden timestamped directory.
func TestLoadDirectoryFollowsConfigMapSymlinks(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "..2026_11_15_00_00_00.000000000")
	writeFile(t, filepath.Join(data, "a.yaml"), serviceFile("a", "a_rows", ""))
	writeFile(t, filepath.Join(data, "b.yaml"), serviceFile("b", "b_rows", ""))
	if err := os.Symlink(filepath.Base(data), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.yaml", "b.yaml"} {
		if err := os.Symlink(filepath.Join("..data", f), filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}

	cfg, _, err := Load(dir)
	if err != nil {
		t.Fatalf("ConfigMap-shaped directory must load: %v", err)
	}
	if got := len(cfg.Metrics); got != 2 {
		t.Errorf("metrics = %d, want 2; hidden entries must not load twice", got)
	}
}

func TestLoadRejectsDuplicateSourceAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	writeFile(t, a, serviceFile("db", "a_rows", ""))
	writeFile(t, b, serviceFile("db", "b_rows", ""))

	ds := loadInvalid(t, dir)
	requireDiagnostic(t, ds, b+": sources.db", "already declared in "+a)
}

func TestLoadRejectsSingletonSectionInTwoFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	writeFile(t, a, serviceFile("a", "a_rows", "")+"server:\n  listen: \":1\"\n")
	writeFile(t, b, serviceFile("b", "b_rows", "")+"server:\n  listen: \":2\"\n")

	ds := loadInvalid(t, dir)
	requireDiagnostic(t, ds, b+": server", "already declared in "+a)
}

// A file without the header is most likely not Metricule configuration at all.
func TestLoadRequiresHeaderInEveryFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.yaml"), serviceFile("a", "a_rows", ""))
	b := filepath.Join(dir, "b.yaml")
	writeFile(t, b, strings.TrimPrefix(serviceFile("b", "b_rows", ""), fileHeader))

	ds := loadInvalid(t, dir)
	requireDiagnostic(t, ds, b+": apiVersion", "required field is missing")
	requireDiagnostic(t, ds, b+": kind", "required field is missing")
}

// Validation runs on the merged document but must name the file and its local
// metric index, or the operator cannot find the offending entry.
func TestLoadDiagnosticsNameTheFile(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	writeFile(t, a, serviceFile("a", "rows", ""))
	writeFile(t, b, serviceFile("b", "rows", "    limits:\n      max_series_per_metric: 0\n"))

	ds := loadInvalid(t, dir)
	requireDiagnostic(t, ds, b+": metrics[0].name", "already declared by "+a+": metrics[0]")
	// Proves presence tracking was rebased: an explicit zero is rejected, not defaulted.
	requireDiagnostic(t, ds, b+": metrics[0].limits.max_series_per_metric", "")
}

func TestLoadRejectsEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "notes.txt"), "x")
	if _, _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "contains no .yaml or .yml files") {
		t.Fatalf("err = %v, want empty-directory error", err)
	}
}

func TestLoadRejectsFileListedTwice(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.yaml")
	writeFile(t, f, serviceFile("a", "a_rows", ""))
	if _, _, err := Load(f, dir); err == nil || !strings.Contains(err.Error(), "listed more than once") {
		t.Fatalf("err = %v, want duplicate-file error", err)
	}
}

// A single file keeps its historical diagnostic paths, with no file prefix.
func TestLoadSingleFileKeepsPlainPaths(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.yaml")
	writeFile(t, f, serviceFile("a", "a_rows", "    limits:\n      max_series_per_metric: 0\n"))

	ds := loadInvalid(t, f)
	requireDiagnostic(t, ds, "metrics[0].limits.max_series_per_metric", "")
}
