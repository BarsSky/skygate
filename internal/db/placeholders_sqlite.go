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

// placeholdersFromTo is the SQLite variant of the [from, to]
// range. SQLite uses "?" for every parameter, so the [from, to]
// range only affects the COUNT of question marks, not the
// numeric labels. Same as placeholders(to - from + 1) for the
// purpose of Go-driver-arg-count matching. v0.33.1.27.
func placeholdersFromTo(from, to int) string {
	if from < 1 || to < from {
		return ""
	}
	return placeholders(to - from + 1)
}
