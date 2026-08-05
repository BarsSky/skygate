//go:build postgres

// PostgreSQL variant (compiled with -tags postgres).
// Returns the comma-joined placeholder list for n parameters,
// using the $1, $2, ... syntax that pgx/extended-protocol
// PostgreSQL requires. SQLite uses the simpler "?" for every
// parameter (see placeholders_sqlite.go).
package db

// placeholders returns "n" parameters as a comma-joined
// $1, $2, ... string suitable for splicing into a SQL
// prepared-statement template.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, 3*n)
	for i := 1; i <= n; i++ {
		if i > 1 {
			out = append(out, ',')
		}
		out = append(out, '$')
		// Integer-to-decimal (no fmt.Sprintf to avoid the
		// dependency on the format machinery in the
		// hot path; for n < 100 this is fine).
		if i >= 10 {
			out = append(out, byte('0'+(i/10)%10))
		}
		out = append(out, byte('0'+i%10))
	}
	return string(out)
}
