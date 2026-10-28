package runtime

// Links the source adapters into any binary that builds a runtime.
//
// Adapters register themselves in init, so an adapter nobody imports is compiled,
// tested, and unreachable: source.Implemented reports false and `serve` refuses to
// start. The imports live here rather than in cmd/metricule so that every entry
// point which constructs a runtime gets the same adapter set.
//
// Adding an adapter package without adding it here is the one mistake that produces
// a passing test suite and a useless program.
import (
	_ "github.com/teagan42/metricule/internal/source/httpjson"
	_ "github.com/teagan42/metricule/internal/source/mysql"
	_ "github.com/teagan42/metricule/internal/source/postgres"
	_ "github.com/teagan42/metricule/internal/source/sqlite"
)
