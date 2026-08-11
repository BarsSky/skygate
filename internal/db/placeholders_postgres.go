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

// placeholdersFromTo returns the comma-joined placeholder
// list for parameters indexed [from, to] (inclusive). For
// example, placeholdersFromTo(4, 6) = "$4,$5,$6". Used when
// a query has an inlined SQL function in the middle of a
// VALUES clause — the function is spliced, not a
// placeholder, so the surrounding placeholder numbers have
// to "skip" past it. v0.33.1.27.
func placeholdersFromTo(from, to int) string {
	if from < 1 || to < from {
		return ""
	}
	n := to - from + 1
	out := make([]byte, 0, 4*n)
	for i := from; i <= to; i++ {
		if i > from {
			out = append(out, ',')
		}
		out = append(out, '$')
		if i >= 10 {
			out = append(out, byte('0'+(i/10)%10))
		}
		out = append(out, byte('0'+i%10))
	}
	return string(out)
}
