//go:build !postgres

// SQLite variant (default build). The bare `INSERT OR
// IGNORE` syntax is what callers use. The
// OnConflictDoNothing suffix is a no-op (returns "")
// because the IGNORE semantics are baked into the verb.
//
// PG: see on_conflict_postgres.go.
package db

func onConflict(cols string) string {
	return ""
}

func insertIgnoreVerb() string {
	return "INSERT OR IGNORE"
}
