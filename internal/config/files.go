package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// singletonSections may be declared by at most one file. Merging them field by field
// would make the effective value depend on file order.
var singletonSections = []string{"metadata", "server", "defaults"}

// Expands configuration paths into the ordered list of files to load.
//
// A directory contributes its top-level *.yaml and *.yml files in name order.
// Hidden entries are skipped, which keeps Kubernetes ConfigMap mounts loadable.
func resolveFiles(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no configuration path given")
	}
	var files []string
	seen := map[string]string{}
	add := func(file, arg string) error {
		abs, err := filepath.Abs(file)
		if err != nil {
			return fmt.Errorf("resolve config %q: %w", file, err)
		}
		// Loading a file twice would report every source in it as a duplicate.
		if prev, dup := seen[abs]; dup {
			return fmt.Errorf("config file %q is listed more than once (via %q and %q)", file, prev, arg)
		}
		seen[abs] = arg
		files = append(files, file)
		return nil
	}

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("read config %q: %w", p, err)
		}
		if !info.IsDir() {
			if addErr := add(p, p); addErr != nil {
				return nil, addErr
			}
			continue
		}
		dirFiles, err := configFilesIn(p)
		if err != nil {
			return nil, err
		}
		if len(dirFiles) == 0 {
			return nil, fmt.Errorf("config directory %q contains no .yaml or .yml files", p)
		}
		for _, f := range dirFiles {
			if addErr := add(f, p); addErr != nil {
				return nil, addErr
			}
		}
	}
	return files, nil
}

// Lists a directory's top-level configuration files in name order.
func configFilesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read config directory %q: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if ext := filepath.Ext(name); ext != ".yaml" && ext != ".yml" {
			continue
		}
		full := filepath.Join(dir, name)
		// Stat follows symlinks; ConfigMap entries are links into a hidden directory.
		info, err := os.Stat(full)
		if err != nil {
			return nil, fmt.Errorf("read config %q: %w", full, err)
		}
		if info.Mode().IsRegular() {
			out = append(out, full)
		}
	}
	// ReadDir already sorts, but the load order is part of the contract.
	sort.Strings(out)
	return out, nil
}

// fragment is one decoded configuration file before merging.
type fragment struct {
	file string
	cfg  *Config
}

// Merges decoded files into one document and records where each part came from.
//
// Conflicts are reported together so an operator fixes them in one pass.
func merge(frags []fragment) (*Config, Diagnostics) {
	var ds collector
	out := &Config{
		Sources: map[string]SourceConfig{},
		present: fieldSet{},
	}
	org := &origins{sections: map[string]string{}, sources: map[string]string{}}

	for _, f := range frags {
		c := f.cfg
		at := func(path string) string { return f.file + ": " + path }

		// Every file is a self-describing document, so a stray YAML file fails here.
		switch {
		case c.APIVersion == "":
			ds.errorf(at("apiVersion"), "required field is missing")
		case c.APIVersion != APIVersion:
			ds.errorf(at("apiVersion"), "unsupported version %q, expected %q", c.APIVersion, APIVersion)
		}
		switch {
		case c.Kind == "":
			ds.errorf(at("kind"), "required field is missing")
		case c.Kind != Kind:
			ds.errorf(at("kind"), "unsupported kind %q, expected %q", c.Kind, Kind)
		}

		for _, sec := range singletonSections {
			if !c.present.has(sec) {
				continue
			}
			if prev, dup := org.sections[sec]; dup {
				ds.errorf(at(sec), "%s is already declared in %s; declare it in one file only", sec, prev)
				continue
			}
			org.sections[sec] = f.file
		}
		if c.present.has("metadata") {
			out.Metadata = c.Metadata
		}
		if c.present.has("server") {
			out.Server = c.Server
		}
		if c.present.has("defaults") {
			out.Defaults = c.Defaults
		}

		for _, name := range sortedNames(c.Sources) {
			if prev, dup := org.sources[name]; dup {
				ds.errorf(at("sources."+name), "source %q is already declared in %s", name, prev)
				continue
			}
			org.sources[name] = f.file
			out.Sources[name] = c.Sources[name]
		}

		offset := len(out.Metrics)
		org.metrics = append(org.metrics, metricRange{start: offset, count: len(c.Metrics), file: f.file})
		out.Metrics = append(out.Metrics, c.Metrics...)

		for path := range c.present {
			out.present[shiftMetricIndex(path, offset)] = true
		}
	}

	out.APIVersion = APIVersion
	out.Kind = Kind
	out.origins = org
	return out, ds.ds.Sorted()
}

// Rebases a `metrics[i]...` presence path onto the merged metric list.
func shiftMetricIndex(path string, offset int) string {
	idx, rest, ok := splitMetricPath(path)
	if !ok || offset == 0 {
		return path
	}
	return fmt.Sprintf("metrics[%d]%s", idx+offset, rest)
}

// Splits `metrics[3].name` into 3 and `.name`.
func splitMetricPath(path string) (int, string, bool) {
	const prefix = "metrics["
	if !strings.HasPrefix(path, prefix) {
		return 0, "", false
	}
	end := strings.IndexByte(path, ']')
	if end < 0 {
		return 0, "", false
	}
	idx, err := strconv.Atoi(path[len(prefix):end])
	if err != nil {
		return 0, "", false
	}
	return idx, path[end+1:], true
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// origins maps merged configuration paths back to the file that declared them.
type origins struct {
	sections map[string]string
	sources  map[string]string
	metrics  []metricRange
}

type metricRange struct {
	start, count int
	file         string
}

// Rewrites a merged path as `file: local-path`. Paths owned by no single file,
// such as `sources` itself, are returned unchanged.
func (o *origins) locate(path string) string {
	if o == nil {
		return path
	}
	if idx, rest, ok := splitMetricPath(path); ok {
		for _, r := range o.metrics {
			if idx >= r.start && idx < r.start+r.count {
				return fmt.Sprintf("%s: metrics[%d]%s", r.file, idx-r.start, rest)
			}
		}
		return path
	}
	if after, ok := strings.CutPrefix(path, "sources."); ok {
		// Match the longest declared name, so a dotted remainder is not misread.
		best := ""
		for name := range o.sources {
			if (after == name || strings.HasPrefix(after, name+".")) && len(name) > len(best) {
				best = name
			}
		}
		if best != "" {
			return o.sources[best] + ": " + path
		}
		return path
	}
	sec, _, _ := strings.Cut(path, ".")
	if file, ok := o.sections[sec]; ok {
		return file + ": " + path
	}
	return path
}
