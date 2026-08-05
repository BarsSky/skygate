//go:build !postgres

// SQLite variant (default build). The go-sqlite3 driver (and
// every other database/sql driver that supports positional
// parameters via "?" placeholders) uses one "?" per parameter.
// PostgreSQL needs $1, $2, ... (see placeholders_postgres.go).
package db

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, 2*n-1)
	for i := 0; i < n; i++ {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '?')
	}
	return string(out)
}
