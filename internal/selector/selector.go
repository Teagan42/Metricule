// Package selector evaluates JSON selectors for the HTTP/JSON adapter.
//
// Semantics are RFC 9535 JSONPath, delegated to github.com/theory/jsonpath. This
// wrapper exists so selector behavior is documented by reference to a standard rather
// than a bespoke dialect (§11.2), and so the implementation can be replaced without
// touching config, mapping, or adapter code.
package selector

import (
	"errors"
	"fmt"

	"github.com/theory/jsonpath"
)

// ErrNotFound reports that a selector matched nothing.
//
// Distinct from a null match: the null policy applies to a present null value,
// not to an absent path (§12.4).
var ErrNotFound = errors.New("selector matched no value")

// ErrAmbiguous reports that a scalar selector matched more than one value.
//
// Picking the first match would make sample values depend on JSON ordering.
var ErrAmbiguous = errors.New("selector matched multiple values")

// Selector is a compiled RFC 9535 JSONPath expression.
type Selector struct {
	raw  string
	path *jsonpath.Path
}

// Parse compiles a selector. Call at configuration time so malformed selectors fail
// at startup rather than at collection time.
func Parse(raw string) (*Selector, error) {
	if raw == "" {
		return nil, errors.New("selector is empty")
	}
	p, err := jsonpath.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid JSONPath selector %q: %w", raw, err)
	}
	return &Selector{raw: raw, path: p}, nil
}

// Valid reports whether raw is a well-formed selector. Used by offline validation.
func Valid(raw string) error {
	_, err := Parse(raw)
	return err
}

// String returns the original expression.
func (s *Selector) String() string { return s.raw }

// SelectAll returns every matching node. Used for row selection.
func (s *Selector) SelectAll(doc any) []any {
	return s.path.Select(doc)
}

// SelectOne returns the single matching node.
//
// Returns ErrNotFound when nothing matches and ErrAmbiguous when several do, so that
// a mapping error is never silently resolved to an arbitrary match.
func (s *Selector) SelectOne(doc any) (any, error) {
	nodes := s.path.Select(doc)
	switch len(nodes) {
	case 1:
		return nodes[0], nil
	case 0:
		return nil, fmt.Errorf("%q: %w", s.raw, ErrNotFound)
	default:
		return nil, fmt.Errorf("%q matched %d values: %w", s.raw, len(nodes), ErrAmbiguous)
	}
}
