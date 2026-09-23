// internal/db/cluster_sql_b291.go — B291 (2026-09-22).
//
// The cluster / HA feature was WRITTEN for PostgreSQL and silently dead on
// SQLite. Live on the native `aro` host (SQLite at /var/lib/skygate/skygate.db):
//
//	cluster.discovery.error  insert discovered node: SQL logic error:
//	                         near "['skygate-standby']": syntax error   (every 5 minutes)
//	/admin/cluster           empty
//	/admin/ha                empty
//
// Every write path in the cluster tree was PostgreSQL-shaped — `ARRAY['x']::text[]`,
// `'skygate' = ANY (roles)`, `unnest(roles || ARRAY['skygate']::text[])`,
// `array_remove(roles, 'skygate')`, `array_to_string(roles, ',')`, `NOW()`,
// `NOW() - INTERVAL '5 minutes'`, `extract(epoch FROM …)::bigint`,
// `$N::jsonb`, `detail->>'key'`, `substr(md5(random()::text), 1, 12)` and
// `RETURNING id` — so `cluster_node` never received a single row: discovery
// failed every tick and both pages had nothing to render. The read side had
// already been ported (the audit page decodes both timestamp shapes through
// `ParseDBTime`, and `parsePGTextArray` handles the `{a,b}` literal), which is
// why the pages looked "empty" rather than "broken".
//
// This file owns the dialect-native SQL fragments those call sites need. The
// rules it follows:
//
//   - a `roles text[]` column is written as a PG array LITERAL (`{a,b}`) bound
//     as a string, which PostgreSQL coerces (`$1::text[]`) and SQLite stores
//     verbatim in its TEXT-affinity column — the same shape the read side
//     already parses;
//   - role membership and role edits happen in GO (`RolesContain`/`RolesAdd`/
//     `RolesRemove`) instead of in SQL, so neither `ANY(roles)`, `unnest` nor
//     `array_remove` is needed;
//   - timestamps are bound as values and compared in Go; the only SQL time
//     expression left is `NowMinusExpr`, rendered per dialect;
//   - JSON is cast per dialect (`CastJSON`) and read with `JSONField`.
package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// NowExpr returns the dialect-native "current timestamp" expression.
//
//	PG:     NOW()
//	SQLite: CURRENT_TIMESTAMP
//
// Callers that can bind a Go time value SHOULD (it is dialect-neutral and keeps
// the stored representation consistent); this exists for the few statements
// where a literal is genuinely simpler, and for the `ON CONFLICT … DO UPDATE`
// clauses that must not re-bind a parameter.
func (k DialectKind) NowExpr() string {
	switch k {
	case DialectPostgres:
		return "NOW()"
	case DialectSQLite:
		return "CURRENT_TIMESTAMP"
	default:
		return "CURRENT_TIMESTAMP"
	}
}

// NowMinusExpr returns "the current timestamp minus d", dialect-native.
//
//	PG:     NOW() - INTERVAL '300 seconds'
//	SQLite: datetime('now', '-300 seconds')
//
// Both produce a value comparable with the timestamp column of a
// DEFAULT-CURRENT_TIMESTAMP table WITHOUT binding a parameter: on SQLite the
// column holds `YYYY-MM-DD HH:MM:SS` TEXT (what `CURRENT_TIMESTAMP` writes) and
// `datetime()` returns the same format, so the comparison is lexicographic and
// correct. Prefer comparing in Go when the column may hold another shape.
func (k DialectKind) NowMinusExpr(d time.Duration) string {
	secs := int64(d / time.Second)
	switch k {
	case DialectPostgres:
		return fmt.Sprintf("NOW() - INTERVAL '%d seconds'", secs)
	case DialectSQLite:
		return fmt.Sprintf("datetime('now', '-%d seconds')", secs)
	default:
		return fmt.Sprintf("datetime('now', '-%d seconds')", secs)
	}
}

// CastJSON returns the dialect-native cast of <expr> to a JSON document.
//
//	PG:     <expr>::jsonb
//	SQLite: <expr>          (no cast needed — SQLite is dynamically typed and
//	                         the column is plain TEXT)
//
// Live symptom of getting this wrong (2026-09-22, `aro`): every cluster_audit
// INSERT failed with `near "::": syntax error`.
func (k DialectKind) CastJSON(expr string) string {
	switch k {
	case DialectPostgres:
		return fmt.Sprintf("%s::jsonb", expr)
	default:
		return expr
	}
}

// JSONField returns the dialect-native expression that reads a STRING field out
// of a JSON document column:
//
//	PG:     <expr>->>'<key>'
//	SQLite: json_extract(<expr>, '$.<key>')
//
// The key is interpolated (both dialects require a literal), so callers must
// pass a hardcoded identifier — never user input.
func (k DialectKind) JSONField(expr, key string) string {
	switch k {
	case DialectPostgres:
		return fmt.Sprintf("%s->>'%s'", expr, key)
	default:
		return fmt.Sprintf("json_extract(%s, '$.%s')", expr, key)
	}
}

// CastTextArray returns the dialect-native cast for a `text[]` parameter or
// column expression:
//
//	PG:     <expr>::text[]
//	SQLite: <expr>
//
// The VALUE is always the PG array literal produced by TextArrayLiteral, which
// is exactly what SQLite's TEXT-affinity column stores.
func (k DialectKind) CastTextArray(expr string) string {
	switch k {
	case DialectPostgres:
		return fmt.Sprintf("%s::text[]", expr)
	default:
		return expr
	}
}

// RandomHexExpr returns a dialect-native SQL expression producing `chars`
// lowercase hex characters:
//
//	PG:     substr(md5(random()::text), 1, <chars>)
//	SQLite: lower(hex(randomblob((<chars> + 1) / 2)))
//
// Used for the synthetic `node-…` ids. `chars` must be a positive literal
// (interpolated; never user input).
func (k DialectKind) RandomHexExpr(chars int) string {
	if chars <= 0 {
		chars = 12
	}
	switch k {
	case DialectPostgres:
		return fmt.Sprintf("substr(md5(random()::text), 1, %d)", chars)
	default:
		// hex(randomblob(n)) yields 2n hex chars; take one extra byte and
		// slice so odd lengths are exact.
		return fmt.Sprintf("substr(lower(hex(randomblob(%d))), 1, %d)", (chars+1)/2, chars)
	}
}

// ForUpdateExpr returns the dialect-native row-lock suffix for a SELECT.
//
//	PG:     " FOR UPDATE"
//	SQLite: ""            (SQLite has no FOR UPDATE — the statement is a
//	                       syntax error there: `near "FOR": syntax error`)
//
// SQLite does not need it: a write transaction takes a database-level lock, so
// the read-modify-write sequences in the cluster tree are serialised anyway.
// Live symptom of getting this wrong (2026-09-22, `aro`): every DrainNode /
// ApproveNode / RejoinNode call failed with `near "FOR": syntax error`.
func (k DialectKind) ForUpdateExpr() string {
	if k == DialectPostgres {
		return " FOR UPDATE"
	}
	return ""
}

// TextArrayLiteral encodes vals as a PostgreSQL array literal (`{a,b}`, `{}`).
//
// This is the ONE representation both backends accept: PostgreSQL casts it to
// text[] (`CastTextArray`), SQLite stores the literal in the TEXT-affinity
// column the migration declares, and the read side (`parsePGTextArray`,
// `StringArray.Scan`) parses it on both. Elements containing a comma, brace,
// quote, space or backslash are double-quoted and escaped, matching
// StringArray.Value.
func TextArrayLiteral(vals []string) string {
	if len(vals) == 0 {
		return "{}"
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		if strings.ContainsAny(v, `, "{}`+"`"+`\`) {
			esc := strings.ReplaceAll(v, `\`, `\\`)
			esc = strings.ReplaceAll(esc, `"`, `\"`)
			parts[i] = `"` + esc + `"`
		} else {
			parts[i] = v
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// RolesContain reports whether the role list contains role (exact match).
//
// This replaces the PostgreSQL-only `'x' = ANY (roles)` predicate: the variant
// of the cluster failover/drill queries that used it now reads the row and
// answers in Go, which is dialect-neutral and strict (a substring test would let
// "skygate" match "skygate-standby").
func RolesContain(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// RolesAdd returns roles with role appended, preserving order and never
// duplicating an existing entry (the PostgreSQL statement it replaces used
// `SELECT DISTINCT unnest(roles || ARRAY['x']::text[])`).
func RolesAdd(roles []string, role string) []string {
	if role == "" {
		return roles
	}
	if RolesContain(roles, role) {
		return roles
	}
	out := make([]string, 0, len(roles)+1)
	out = append(out, roles...)
	return append(out, role)
}

// RolesRemove returns roles without role (order preserved) — the Go equivalent
// of `array_remove(roles, 'x')`.
func RolesRemove(roles []string, role string) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		if r != role {
			out = append(out, r)
		}
	}
	return out
}

// TimeValue is the value to BIND for a timestamp column on this dialect.
//
//	PG:     the time.Time itself (the column is timestamptz).
//	SQLite: t.UTC().Format(time.RFC3339Nano) — a canonical string.
//
// WHY (B291, measured, not guessed): the modernc.org/sqlite driver stores a
// bound time.Time in its Go `String()` form ("2026-09-23 03:29:58.2457633 +0000
// UTC"). That shape is (a) not decodable by db.ParseDBTime before B291 and (b)
// not scannable into sql.NullTime/*time.Time at all, so the cluster pages
// silently dropped every row that had one. Writing RFC3339 keeps BOTH reader
// styles working (sql.NullTime parses RFC3339 natively, and ParseDBTime has the
// layout) and is still a valid date string for SQLite's date functions.
//
// nil is returned for the zero time so a NULL column stays NULL.
func (k DialectKind) TimeValue(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	switch k {
	case DialectPostgres:
		return t
	default:
		return t.UTC().Format(time.RFC3339Nano)
	}
}

// rowQueryer is the subset of *sql.DB / *sql.Tx the cluster helpers below need.
type rowQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// execer is the write-side twin of rowQueryer.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// FindClusterPrimary returns the (id, hostname) of the current skygate primary:
// a state='ready' node whose roles contain the exact role "skygate", picked by
// ascending id for determinism.
//
// This replaces the PostgreSQL-only `AND 'skygate' = ANY (roles)` predicate used
// by both the failover and the drill transactions (B291). The role test happens
// in Go because SQLite stores `roles` as the `{a,b}` TEXT literal — a SQL-side
// membership test would need `LIKE '%skygate%'`, which also matches
// "skygate-standby" and would promote the node that is already the standby.
// Returns ErrNoPrimary when nothing qualifies (the caller's contract).
func FindClusterPrimary(q rowQueryer) (string, string, error) {
	rows, err := q.Query(`SELECT id, hostname, COALESCE(roles, '') FROM cluster_node WHERE state = 'ready' ORDER BY id ASC`)
	if err != nil {
		return "", "", fmt.Errorf("find current primary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, hostname, rolesStr string
		if err := rows.Scan(&id, &hostname, &rolesStr); err != nil {
			continue
		}
		if rolesContainLiteral(rolesStr, "skygate") {
			return id, hostname, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", fmt.Errorf("find current primary: %w", err)
	}
	return "", "", ErrNoPrimary
}

// NodeRoles reads one node's roles as a slice (the `{a,b}` literal parsed the
// same way on both backends).
func NodeRoles(q rowQueryer, nodeID string) ([]string, error) {
	var raw string
	if err := q.QueryRow(`SELECT COALESCE(roles, '') FROM cluster_node WHERE id = $1`, nodeID).Scan(&raw); err != nil {
		return nil, err
	}
	var sa StringArray
	if err := sa.Scan(raw); err != nil {
		return nil, fmt.Errorf("parse roles %q: %w", raw, err)
	}
	return []string(sa), nil
}

// SetNodeRoles rewrites one node's roles with a dialect-native literal.
func SetNodeRoles(q execer, nodeID string, roles []string) error {
	d := ActiveDialect()
	if _, err := q.Exec(`UPDATE cluster_node SET roles = `+d.CastTextArray("$1")+` WHERE id = $2`,
		TextArrayLiteral(roles), nodeID); err != nil {
		return fmt.Errorf("set roles of %s: %w", nodeID, err)
	}
	return nil
}

// rolesContainLiteral reports whether a raw `roles` column value ({a,b}) contains
// role as an exact element. It is the string-in variant of RolesContain, used on
// values that never went through StringArray.
func rolesContainLiteral(raw, role string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if !strings.HasPrefix(raw, "{") || !strings.HasSuffix(raw, "}") {
		return raw == role
	}
	for _, p := range strings.Split(raw[1:len(raw)-1], ",") {
		if strings.Trim(strings.TrimSpace(p), `"`) == role {
			return true
		}
	}
	return false
}
