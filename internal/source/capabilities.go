package source

// Declared capabilities for the v0.1 adapter set (AD-008).
//
// Capabilities are part of the configuration contract, not an adapter implementation
// detail: configuration validation must reject an unsupported collection mode before
// any adapter is constructed or any connection is opened. They are therefore declared
// here by Agent 0 rather than by each adapter.
//
// An instance reports what this build implements through Capabilities. A type whose
// implementation is incomplete narrows its instance capabilities rather than
// weakening this table, so configuration written for a future milestone stays valid
// while runtime decisions see the truth (rule 3).
func init() {
	DeclareCapabilities(TypePostgres, Capabilities{
		Query:             true,
		Poll:              true,
		Notifications:     true, // LISTEN/NOTIFY (§9.2)
		TriggerManagement: true, // opt-in only (AD-006)
	})

	DeclareCapabilities(TypeSQLite, Capabilities{
		Query:        true,
		Poll:         true,
		ChangeTokens: true, // PRAGMA data_version (§10.2)
	})

	DeclareCapabilities(TypeHTTP, Capabilities{
		Query: true,
		Poll:  true,
	})
}

// Adapter type identifiers.
const (
	TypePostgres = "postgres"
	TypeSQLite   = "sqlite"
	TypeHTTP     = "http"
)
