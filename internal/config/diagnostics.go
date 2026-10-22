package config

import (
	"fmt"
	"sort"
	"strings"
)

// Severity classifies a diagnostic.
type Severity string

const (
	// SeverityError makes the configuration invalid.
	SeverityError Severity = "error"
	// SeverityWarning reports a convention violation that still loads.
	SeverityWarning Severity = "warning"
)

// Diagnostic is one validation finding, anchored to a configuration path.
type Diagnostic struct {
	Severity Severity
	// Path locates the finding, such as `metrics[2].collect.mode`.
	Path    string
	Message string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s: %s: %s", d.Severity, d.Path, d.Message)
}

// Diagnostics is an ordered set of findings.
//
// Order is deterministic so that invalid configurations fail identically across runs.
type Diagnostics []Diagnostic

// String renders diagnostics one per line.
func (ds Diagnostics) String() string {
	var b strings.Builder
	for _, d := range ds {
		b.WriteString("  " + d.String() + "\n")
	}
	return b.String()
}

// Err returns a non-nil error when any diagnostic is an error.
func (ds Diagnostics) Err() error {
	if !ds.HasErrors() {
		return nil
	}
	return validationError{ds.Errors()}
}

// HasErrors reports whether any diagnostic blocks loading.
func (ds Diagnostics) HasErrors() bool {
	for _, d := range ds {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Errors returns only the blocking diagnostics.
func (ds Diagnostics) Errors() Diagnostics {
	var out Diagnostics
	for _, d := range ds {
		if d.Severity == SeverityError {
			out = append(out, d)
		}
	}
	return out
}

// Warnings returns only the non-blocking diagnostics.
func (ds Diagnostics) Warnings() Diagnostics {
	var out Diagnostics
	for _, d := range ds {
		if d.Severity == SeverityWarning {
			out = append(out, d)
		}
	}
	return out
}

// Sorted orders diagnostics by path then message.
func (ds Diagnostics) Sorted() Diagnostics {
	out := make(Diagnostics, len(ds))
	copy(out, ds)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Message < out[j].Message
	})
	return out
}

type validationError struct{ ds Diagnostics }

func (e validationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "configuration is invalid (%d error(s)):", len(e.ds))
	for _, d := range e.ds {
		fmt.Fprintf(&b, "\n  - %s: %s", d.Path, d.Message)
	}
	return b.String()
}

// Diagnostics exposes the findings behind a validation error.
func (e validationError) Diagnostics() Diagnostics { return e.ds }

type collector struct {
	ds Diagnostics
}

func (c *collector) errorf(path, format string, args ...any) {
	c.ds = append(c.ds, Diagnostic{SeverityError, path, fmt.Sprintf(format, args...)})
}

func (c *collector) warnf(path, format string, args ...any) {
	c.ds = append(c.ds, Diagnostic{SeverityWarning, path, fmt.Sprintf(format, args...)})
}
