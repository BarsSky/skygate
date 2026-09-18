// Returns the comma-joined placeholder list for n parameters.
//
// 2026-09-18: `$1, $2, ...` is the UNIVERSAL form — it is NOT a
// PostgreSQL-only choice. SQLite accepts `$N` as a named parameter, and
// modernc.org/sqlite binds `$NNN` by Go argument ordinal, so the same SQL
// text works on both drivers. `?` is the opposite: pgx does not translate
// it (plain syntax error, SQLSTATE 42601), which made every `?` in a
// shared query a latent PG break — see the 2026-09-18 fixes in
// node_owner_map.go (upsertExitServerFromSyncNode) and
// headscale_version/monitor.go (listHeadscaleReleases).
//
// Reviewer's rule of thumb: a `$N` placeholder in a shared file is fine
// on BOTH backends; a `?` placeholder is a bug. Prefer
// PlaceholdersList / PlaceholdersRange / PlaceholderAt over typing
// either form by hand.
package db

import "strings"

func placeholdersList(n int) string {
	return placeholders(n)
}

// PlaceholdersList is the public mirror of placeholdersList,
// used by callers outside the db package (e.g. system_tests.go's
// PersistRun + ListRecentRuns which need to dispatch the
// INSERT/LIMIT ? placeholder between SQLite and PG without
// forking the caller). Same pattern as
// db.nowUnixSQL / db.SetGlobalSetting.
func PlaceholdersList(n int) string { return placeholdersList(n) }

// PlaceholdersRange returns the comma-joined placeholder list
// for parameters indexed [from, to] (inclusive) for the current
// backend. SQLite returns N question marks; PG returns
// "$from, $from+1, ..., $to". Useful when a query has an
// inlined SQL function in the middle of a VALUES clause —
// the function is spliced, not a placeholder, so the
// surrounding placeholder numbers have to "skip" past it.
//
// Pre-v0.33.1.27 the convention was `placeholdersList(N-1) +
// placeholdersList(1)`, which on PG produced "$1,$2,$3,$1"
// — TWO references to $1 in the same query. pgx then rejected
// the query with "mismatched param and argument count"
// because the number of unique $N placeholders didn't match
// the number of Go args. The pre-fix assumption was that
// placeholdersList(1) would return "$N" (where N is the
// previous count); it actually returns "$1" (always starts
// from 1). The fix: this function takes an explicit
// [from, to] range.
//
// Example (5 Go args, inlined nowUnixSQL() at the 4th
// position): placeholdersRange(1, 3) + ", " + nowUnixSQL() +
// ", " + placeholdersRange(4, 5) → "$1, $2, $3, EXTRACT(...),
// $4, $5".
//
// v0.33.1.27.
func PlaceholdersRange(from, to int) string {
	if from < 1 || to < from {
		return ""
	}
	return placeholdersFromTo(from, to)
}

// PlaceholderAt returns the i-th (0-indexed) placeholder from
// a PlaceholdersList(n) string. Useful when a query needs the
// placeholders distributed across multiple positions:
//
//	ph := strings.Split(db.PlaceholdersList(2), ",")
//	err := d.QueryRow(
//	    `... WHERE a = `+ph[0]+` AND b = `+ph[1]+` `,
//	    valA, valB,
//	)
//
// v0.33.1.14 — added because the v0.33.1.12 sweep wrote
// "placeholdersList(1)+placeholdersList(1)" for 2-arg
// queries, which on PG produced "$1 AND ... = $1" (two refs
// to the same param) and silently returned 0 rows. The proper
// way is to call PlaceholdersList(2) ONCE (giving "$1, $2")
// and split it. Same as db.NowUnixSQL() for the literal-now
// cases.
//
// The 0-indexed i is the desired position. Returns "" if i is
// out of range (caller bug).
func PlaceholderAt(n, i int) string {
	if i < 0 || i >= n {
		return ""
	}
	parts := strings.Split(PlaceholdersList(n), ",")
	return parts[i]
}
