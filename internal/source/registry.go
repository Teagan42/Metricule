package source

import (
	"fmt"
	"sort"
	"sync"
)

// Factory constructs an unopened source from its resolved settings.
type Factory func(settings Settings) (Source, error)

// Settings carries resolved connection settings to an adapter factory.
//
// Secret references are resolved before construction so adapters never depend on
// internal/secrets and never re-read credentials at collection time.
type Settings struct {
	ID   string
	Type string

	// Raw holds the adapter-specific settings block. Adapters assert their own
	// concrete type. Kept as any so this package does not import config.
	Raw any
}

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
	declared  = map[string]Capabilities{}
)

// DeclareCapabilities records what an adapter type supports.
//
// Separate from Register so configuration validation works in a build where an
// adapter is declared but not yet implemented.
//
// Panics on duplicate declaration: a collision is a wiring mistake.
func DeclareCapabilities(sourceType string, c Capabilities) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := declared[sourceType]; dup {
		panic("source: duplicate capability declaration for type " + sourceType)
	}
	declared[sourceType] = c
}

// Register adds an adapter factory. Called from adapter package init functions.
//
// Panics when the type has no declared capabilities, or on duplicate registration.
func Register(sourceType string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := declared[sourceType]; !ok {
		panic("source: adapter " + sourceType + " registered without declared capabilities")
	}
	if _, dup := factories[sourceType]; dup {
		panic("source: duplicate adapter registration for type " + sourceType)
	}
	factories[sourceType] = f
}

// New constructs a source of the registered type.
//
// A declared but unregistered type reports that the adapter is absent from this
// build, rather than reporting the type as unknown.
func New(settings Settings) (Source, error) {
	mu.RLock()
	f, registered := factories[settings.Type]
	_, isDeclared := declared[settings.Type]
	mu.RUnlock()

	switch {
	case registered:
		return f(settings)
	case isDeclared:
		return nil, fmt.Errorf("source %q: adapter for type %q is declared but not implemented in this build", settings.ID, settings.Type)
	default:
		return nil, fmt.Errorf("source %q: unsupported type %q (declared: %v)", settings.ID, settings.Type, Declared())
	}
}

// CapabilitiesFor reports a declared type's capabilities without constructing it.
func CapabilitiesFor(sourceType string) (Capabilities, bool) {
	mu.RLock()
	defer mu.RUnlock()
	c, ok := declared[sourceType]
	return c, ok
}

// Implemented reports whether an adapter factory is linked into this build.
func Implemented(sourceType string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[sourceType]
	return ok
}

// Declared lists declared adapter types in sorted order.
func Declared() []string { return sortedTypes(declared) }

// Registered lists implemented adapter types in sorted order.
func Registered() []string { return sortedTypes(factories) }

func sortedTypes[V any](m map[string]V) []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
