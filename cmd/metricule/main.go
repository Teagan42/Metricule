// Command metricule runs the declarative Prometheus exporter.
//
// Dispatch only: each command delegates to internal packages so the CLI holds no
// collection or validation logic.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/runtime"
	"github.com/teagan42/metricule/internal/source"
	"github.com/teagan42/metricule/internal/version"
	"github.com/teagan42/metricule/schemas"
)

// Exit codes. Separating usage from runtime failure lets orchestrators distinguish
// a bad invocation from an unreachable source.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// errUsage signals a bad invocation rather than a runtime failure.
var errUsage = errors.New("usage")

type command struct {
	name    string
	summary string
	run     func(stdout, stderr io.Writer, args []string) error
}

func commands() []command {
	return []command{
		{"serve", "Run the exporter", runServe},
		{"validate", "Validate a configuration file", runValidate},
		{"test", "Execute one metric's collection and print the result", runTest},
		{"config", "Print the configuration JSON Schema", runConfig},
		{"version", "Print build information", runVersion},
	}
}

func main() {
	os.Exit(dispatch(os.Stdout, os.Stderr, os.Args[1:]))
}

func dispatch(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		usage(stderr, "")
		return exitUsage
	}

	name := args[0]
	for _, c := range commands() {
		if c.name != name {
			continue
		}
		if err := c.run(stdout, stderr, args[1:]); err != nil {
			if errors.Is(err, errUsage) {
				fmt.Fprintf(stderr, "metricule %s: %v\n", name, err)
				return exitUsage
			}
			fmt.Fprintf(stderr, "metricule %s: %v\n", name, err)
			return exitFailure
		}
		return exitOK
	}

	usage(stderr, fmt.Sprintf("unknown command %q", name))
	return exitUsage
}

func usage(w io.Writer, problem string) {
	if problem != "" {
		fmt.Fprintf(w, "metricule: %s\n\n", problem)
	}
	fmt.Fprint(w, "Metricule turns existing data sources into Prometheus metrics.\n\nUsage:\n  metricule <command> [flags]\n\nCommands:\n")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-9s %s\n", c.name, c.summary)
	}
	fmt.Fprint(w, "\nRun 'metricule <command> --help' for command flags.\n")
}

// Builds a flag set that reports errors through the command's stderr.
func flags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func runVersion(stdout, stderr io.Writer, args []string) error {
	fs := flags("version", stderr)
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	v := version.Get()
	fmt.Fprintf(stdout, "metricule %s\n  commit:   %s\n  built:    %s\n  go:       %s\n  platform: %s\n",
		v.Version, v.Commit, v.Date, v.GoVersion, v.Platform)
	return nil
}

func runConfig(stdout, stderr io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected subcommand \"schema\"", errUsage)
	}
	switch args[0] {
	case "schema":
		_, err := stdout.Write(schemas.V1Alpha1)
		return err
	default:
		return fmt.Errorf("%w: unknown subcommand %q, expected \"schema\"", errUsage, args[0])
	}
}

func runValidate(stdout, stderr io.Writer, args []string) error {
	fs := flags("validate", stderr)
	path := fs.String("config", "", "path to the configuration file")
	online := fs.Bool("online", false, "additionally connect to each source and check queries")
	printConfig := fs.Bool("print-config", false, "print the effective configuration with secrets redacted")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *path == "" {
		return fmt.Errorf("%w: --config is required", errUsage)
	}

	cfg, ds, err := config.Load(*path)
	for _, d := range ds.Warnings() {
		fmt.Fprintf(stderr, "warning: %s: %s\n", d.Path, d.Message)
	}
	if err != nil {
		return err
	}

	if *printConfig {
		out, err := config.Redacted(cfg)
		if err != nil {
			return fmt.Errorf("render configuration: %w", err)
		}
		if _, err := stdout.Write(out); err != nil {
			return err
		}
	}

	if *online {
		if err := validateOnline(stderr, cfg); err != nil {
			return err
		}
	}

	fmt.Fprintf(stdout, "%s is valid: %d source(s), %d metric(s)\n", *path, len(cfg.Sources), len(cfg.Metrics))
	return nil
}

// Connects to each source to check reachability (§15.1). Offline validation has
// already passed at this point.
func validateOnline(stderr io.Writer, cfg *config.Config) error {
	sources, err := runtime.BuildSources(cfg)
	if err != nil {
		return err
	}
	defer closeAll(stderr, sources)

	for _, name := range sortedKeys(sources) {
		ctx, cancel := context.WithTimeout(context.Background(), onlineProbeTimeout)
		err := sources[name].Open(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("source %q: %w", name, err)
		}
		fmt.Fprintf(stderr, "source %q: reachable\n", name)
	}
	return nil
}

// Bounds each reachability probe so `validate --online` cannot hang in CI.
const onlineProbeTimeout = 15 * time.Second

func runServe(stdout, stderr io.Writer, args []string) error {
	fs := flags("serve", stderr)
	path := fs.String("config", "", "path to the configuration file")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *path == "" {
		return fmt.Errorf("%w: --config is required", errUsage)
	}

	cfg, ds, err := config.Load(*path)
	for _, d := range ds.Warnings() {
		fmt.Fprintf(stderr, "warning: %s: %s\n", d.Path, d.Message)
	}
	if err != nil {
		return err
	}

	// Sources are constructed before the listener binds. Serving a /metrics that
	// can never produce samples would present a broken deployment as a healthy one.
	sources, err := runtime.BuildSources(cfg)
	if err != nil {
		return err
	}
	defer closeAll(stderr, sources)

	return errors.New("the collection engine and exporter are not implemented in this build (milestone M1)")
}

func runTest(stdout, stderr io.Writer, args []string) error {
	fs := flags("test", stderr)
	path := fs.String("config", "", "path to the configuration file")
	metric := fs.String("metric", "", "name of the metric to collect once")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	if *path == "" {
		return fmt.Errorf("%w: --config is required", errUsage)
	}
	if *metric == "" {
		return fmt.Errorf("%w: --metric is required", errUsage)
	}

	cfg, _, err := config.Load(*path)
	if err != nil {
		return err
	}

	var selected *config.Metric
	for i := range cfg.Metrics {
		if cfg.Metrics[i].Name == *metric {
			selected = &cfg.Metrics[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("unknown metric %q (configured: %s)", *metric, strings.Join(metricNames(cfg), ", "))
	}

	src, err := runtime.BuildSource(selected.Source, cfg.Sources[selected.Source])
	if err != nil {
		return err
	}
	defer func() {
		if err := src.Close(); err != nil {
			fmt.Fprintf(stderr, "warning: close source %q: %v\n", selected.Source, err)
		}
	}()

	return errors.New("single-metric collection is not implemented in this build (milestone M1)")
}

func metricNames(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Metrics))
	for _, m := range cfg.Metrics {
		out = append(out, m.Name)
	}
	return out
}

func closeAll(stderr io.Writer, sources map[string]source.Source) {
	for _, name := range sortedKeys(sources) {
		if err := sources[name].Close(); err != nil {
			fmt.Fprintf(stderr, "warning: close source %q: %v\n", name, err)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
