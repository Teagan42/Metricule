package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/runtime"
	"github.com/teagan42/metricule/internal/triggers"
)

const triggerCommandTimeout = 30 * time.Second

func runTriggers(stdout, stderr io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected subcommand plan, apply, status, or remove", errUsage)
	}
	op := args[0]
	switch op {
	case "plan", "apply", "status", "remove":
	default:
		return fmt.Errorf("%w: unknown subcommand %q, expected plan, apply, status, or remove", errUsage, op)
	}

	fs := flags("triggers "+op, stderr)
	path := fs.String("config", "", "path to the configuration file")
	source := fs.String("source", "", "limit the operation to one source")
	if err := fs.Parse(args[1:]); err != nil {
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

	targets, err := runtime.BuildTriggerTargets(cfg)
	if err != nil {
		return err
	}
	targets = selectTargets(targets, *source)
	if *source != "" && len(targets) == 0 {
		return fmt.Errorf("source %q has no triggers block", *source)
	}
	if len(targets) == 0 {
		return fmt.Errorf("no source configures triggers")
	}

	for _, target := range targets {
		if op == "plan" || op == "apply" {
			if !target.Manage {
				return fmt.Errorf("source %q: triggers.manage is false; enable it to provision", target.SourceID)
			}
		}
		if err := runTriggerOp(stdout, stderr, op, target); err != nil {
			return fmt.Errorf("source %q: %w", target.SourceID, err)
		}
	}
	return nil
}

func selectTargets(targets []triggers.Target, source string) []triggers.Target {
	if source == "" {
		return targets
	}
	out := targets[:0]
	for _, t := range targets {
		if t.SourceID == source {
			out = append(out, t)
		}
	}
	return out
}

func runTriggerOp(stdout, stderr io.Writer, op string, target triggers.Target) error {
	ctx, cancel := context.WithTimeout(context.Background(), triggerCommandTimeout)
	defer cancel()

	m, err := triggers.Open(ctx, target.DSN)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close(context.Background()) }()

	switch op {
	case "plan":
		report, err := m.Plan(ctx, target)
		if err != nil {
			return err
		}
		printWarnings(stderr, report.Warnings)
		printReport(stdout, report, true)
	case "apply":
		report, err := m.Apply(ctx, target)
		if err != nil {
			return err
		}
		printWarnings(stderr, report.Warnings)
		printReport(stdout, report, false)
	case "status":
		report, err := m.Status(ctx, target)
		if err != nil {
			return err
		}
		printWarnings(stderr, report.Warnings)
		printStatus(stdout, report)
	case "remove":
		report, err := m.Remove(ctx, target)
		if err != nil {
			return err
		}
		printWarnings(stderr, report.Warnings)
		printReport(stdout, report, false)
	}
	return nil
}

func printWarnings(w io.Writer, warnings []string) {
	for _, warning := range warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

func printReport(w io.Writer, report triggers.Report, dryRun bool) {
	fmt.Fprintf(w, "source %s (schema %s):\n", report.Source, report.Schema)
	if !report.Changed() {
		fmt.Fprintf(w, "  no changes; %d object(s) already current\n", report.Unchanged)
		return
	}
	for _, action := range report.Actions {
		if len(action.Statements) == 0 {
			fmt.Fprintf(w, "  unchanged: %s\n", action.Object)
			continue
		}
		if dryRun {
			fmt.Fprintf(w, "  -- %s %s\n", action.Kind, action.Object)
			for _, stmt := range action.Statements {
				fmt.Fprintf(w, "  %s;\n", stmt)
			}
			continue
		}
		fmt.Fprintf(w, "  %s: %s\n", action.Kind, action.Object)
	}
}

func printStatus(w io.Writer, report triggers.StatusReport) {
	fmt.Fprintf(w, "source %s (schema %s):\n", report.Source, report.Schema)
	if report.SchemaPresent {
		fmt.Fprintf(w, "  schema %s: present\n", report.Schema)
	} else {
		fmt.Fprintf(w, "  schema %s: missing\n", report.Schema)
	}
	fmt.Fprintf(w, "  function %s.%s(): %s\n", report.Schema, triggers.FunctionName, report.Function)
	if len(report.Objects) == 0 {
		fmt.Fprintln(w, "  no desired triggers")
		return
	}
	for _, object := range report.Objects {
		switch object.State {
		case triggers.StateOrphan:
			fmt.Fprintf(w, "  %s on %s: %s\n", object.Trigger, object.Table, object.State)
		default:
			fmt.Fprintf(w, "  %s on %s (channel %s): %s\n",
				object.Trigger, object.Table, object.Channel, object.State)
		}
	}
}
