// Package db — array.go owns the PG-array scanner used by
// GetClusterDatabase (and any future reads of TEXT[] columns
// via database/sql).
//
// v1.5.0+ / B203.1.
//
// Why this exists
//
// pgx v5's database/sql adapter (pgx/v5/stdlib) returns TEXT[]
// columns as their literal string form ("{a,b}" / "{}") instead
// of a []string. database/sql then refuses to scan a string
// into a *[]string with the unhelpful error:
//
//	sql: Scan error on column index N, name "foo":
//	  unsupported Scan, storing driver.Value type string
//	  into type *[]string
//
// We hit this on the first live B203 test where the
// cluster_database row was successfully read past
// primary_node_id (after the COALESCE fix) but then failed
// on replica_node_ids. Pre-B203.1 the pre-B203 code never
// reached the replica_node_ids column because the
// primary_node_id NULL short-circuited the Scan; the
// pre-B203.1 production watchdog always used
// `row.CurrentDSN` directly via a DSNReader closure, not
// via the full ClusterDatabase struct, so it never scanned
// the array either.
//
// After B203.1 GetClusterDatabase actually scans the
// replica_node_ids column. To make that scan work, the
// destination must implement sql.Scanner. ReplicaNodeIDs
// is a []string, which doesn't. So we wrap it in a
// StringArray type that implements sql.Scanner + driver.Valuer.
//
// PG array literal format
//
// The standard PostgreSQL array literal is documented in
// the PG docs (8.15. Array Functions and Operators):
//
//   - empty:    "{}"
//   - singleton: "{a}"
//   - multi:    "{a,b,c}"
//   - quoted:   "{\"a b\",\"c,d\"}"  (commas/spaces inside quotes)
//
// The parser below handles all four cases. It's intentionally
// minimal — we don't need every edge case, just the ones
// the admin UI is likely to produce (and the test fixtures
// use).
//
// Why not use github.com/lib/pq: it's a separate driver and
// we use pgx v5. Adding lib/pq for one helper is heavy.
// Why not use pgtype.Array: it requires pgx-native
// connection (not database/sql), which the rest of skygate
// uses. Switching just for arrays is invasive.

package db

import (
	"database/sql/driver"
	"fmt"
	"strings"
)

// StringArray is a []string that implements sql.Scanner +
// driver.Valuer for PostgreSQL TEXT[] columns. Use it
// in place of []string for any column that scans from
// or writes to a PG array.
type StringArray []string

// Scan parses a PostgreSQL array literal (e.g. "{a,b}" or
// "{}" or "{\"a b\",\"c,d\"}") into the receiver.
// Returns nil on an empty / NULL input so the destination
// becomes a non-nil empty slice (matches the project's
// convention for "no values" rather than nil).
func (s *StringArray) Scan(src interface{}) error {
	if src == nil {
		*s = StringArray{}
		return nil
	}
	var raw string
	switch v := src.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return fmt.Errorf("StringArray.Scan: unsupported src type %T", src)
	}
	parsed, err := parsePGArrayLiteral(raw)
	if err != nil {
		return err
	}
	*s = StringArray(parsed)
	return nil
}

// Value implements driver.Valuer by serialising the
// slice as a PG array literal. The reverse of Scan.
func (s StringArray) Value() (driver.Value, error) {
	if s == nil {
		return "{}", nil
	}
	parts := make([]string, len(s))
	for i, v := range s {
		// Quote any element containing comma, space, brace,
		// quote, or backslash. PG requires double-quoting
		// and backslash-escaping inside the literal.
		if needsQuoting(v) {
			// PG literal escape rules: inside "..." a literal
			// backslash is "\\" and a literal double-quote
			// is "\"". Order matters: do backslash first so
			// the inserted backslashes aren't themselves
			// re-escaped by the quote pass.
			escaped := strings.ReplaceAll(v, "\\", "\\\\")
			escaped = strings.ReplaceAll(escaped, `"`, `\"`)
			parts[i] = `"` + escaped + `"`
		} else {
			parts[i] = v
		}
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

// needsQuoting returns true if v contains any PG array
// delimiter that requires double-quoting. Per the PG docs,
// the delimiters inside a non-quoted element are: comma,
// space, opening/closing brace, double-quote, and
// backslash. Elements containing any of these MUST be
// wrapped in double-quotes (with the backslash and
// double-quote inside further escaped).
func needsQuoting(v string) bool {
	return strings.ContainsAny(v, `, "{}`+"`"+`\`)
}

// parsePGArrayLiteral parses a PG array literal of the form
// "{a,b,c}" (possibly with quoted elements containing
// commas/spaces). Returns a nil slice for "{}" so the caller
// can distinguish "no values" from a SQL NULL.
func parsePGArrayLiteral(s string) ([]string, error) {
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, fmt.Errorf("parsePGArrayLiteral: not a PG array literal: %q", s)
	}
	body := s[1 : len(s)-1]
	if body == "" {
		return []string{}, nil
	}
	// Walk the body, respecting double-quotes and backslash
	// escapes. State: inQuote (we're inside "..."), escape
	// (next char is literal).
	var out []string
	var cur strings.Builder
	inQuote := false
	escape := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case escape:
			cur.WriteByte(c)
			escape = false
		case c == '\\' && inQuote:
			escape = true
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out, nil
}
