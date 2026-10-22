package config

import (
	"fmt"

	yaml "go.yaml.in/yaml/v3"
)

// fieldSet records which configuration paths the document actually contained.
//
// Required because zero is a legitimate value for several fields. Without presence
// information, `retries: 0` is indistinguishable from an absent `retries`, so an
// operator cannot express "no retries" and an invalid `max_series_per_metric: 0` is
// silently rewritten to the default instead of being rejected (§5.1 forbids silently
// accepting nonfunctional configuration).
type fieldSet map[string]bool

// has reports whether the document set this path.
//
// A nil fieldSet reports everything absent, so a hand-built Config still receives
// defaults.
func (f fieldSet) has(path string) bool { return f[path] }

// Paths use dotted keys with bracketed sequence indices, matching the form used in
// validation diagnostics: `metrics[0].collect.interval`.
func collectPresent(n *yaml.Node) fieldSet {
	out := fieldSet{}
	walkPresent(n, "", out)
	return out
}

func walkPresent(n *yaml.Node, prefix string, out fieldSet) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkPresent(c, prefix, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			// A bare `null` key resolves to the null scalar, not the string
			// "null"; the mapping null policy is spelled that way (§5.1).
			key := k.Value
			if k.Tag == "!!null" {
				key = "null"
			}
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			out[path] = true
			walkPresent(v, path, out)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			path := fmt.Sprintf("%s[%d]", prefix, i)
			out[path] = true
			walkPresent(c, path, out)
		}
	case yaml.AliasNode:
		walkPresent(n.Alias, prefix, out)
	}
}
