// Package dbmigrate — dsn_parse.go is a small helper to
// parse a libpq-style DSN into its components. Mirrors
// admin.parseLibpqDSN (which is in the admin package and
// can't be reused across packages without a cycle).
//
// We only need this for the rollback path (B214) where
// we reconstruct the MigrationContext from a DB row
// (which stores the DSN, not the parsed fields). The
// parsed fields are used by individual step Rollback()
// methods that read mc.TargetHost etc.

package dbmigrate

import "strings"

// parseTargetDSNForRollback parses a postgres:// DSN into
// its components for the rollback path. Returns ok=false
// for empty / non-postgres DSNs — callers should treat
// that as "I don't have the parsed fields, but the
// individual step Rollback() methods are nil-safe".
func parseTargetDSNForRollback(dsn string) (host, port, dbname, user, sslmode string, ok bool) {
	if dsn == "" {
		return "", "", "", "", "", false
	}
	const prefix = "postgres://"
	if !strings.HasPrefix(dsn, prefix) {
		return "", "", "", "", "", false
	}
	rest := strings.TrimPrefix(dsn, prefix)
	if i := strings.Index(rest, "@"); i >= 0 {
		userpart := rest[:i]
		rest = rest[i+1:]
		if j := strings.Index(userpart, ":"); j >= 0 {
			user = userpart[:j]
		} else {
			user = userpart
		}
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		hostpart := rest[:i]
		rest = rest[i+1:]
		if j := strings.Index(hostpart, ":"); j >= 0 {
			host = hostpart[:j]
			port = hostpart[j+1:]
		} else {
			host = hostpart
		}
		dbname = rest
	}
	if i := strings.Index(dbname, "?"); i >= 0 {
		params := dbname[i+1:]
		dbname = dbname[:i]
		for _, p := range strings.Split(params, "&") {
			if strings.HasPrefix(p, "sslmode=") {
				sslmode = strings.TrimPrefix(p, "sslmode=")
			}
		}
	}
	if host == "" || dbname == "" {
		return host, port, dbname, user, sslmode, false
	}
	return host, port, dbname, user, sslmode, true
}
