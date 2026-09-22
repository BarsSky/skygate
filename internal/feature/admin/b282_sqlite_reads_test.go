// internal/feature/admin/b282_sqlite_reads_test.go — B282 (2026-09-22).
//
// Live case on the native `aro` host (SQLite at
// /var/lib/skygate/skygate.db), three operator surfaces broken by
// PostgreSQL-only SQL running on SQLite:
//
//	/admin/audit                    500 SQL logic error: unrecognized token: ":"
//	exit_rules.preferred_mismatch   fail: query rules: SQL logic error: unrecognized token: ":"
//	/admin/headscale/acl            500 list acl: unmarshal policy: json: cannot unmarshal
//	                                 string into Go value of type admin.ACLView
//
// The tests below run the REAL queries against a REAL in-memory SQLite
// database (db.ApplyMigrations through the dual-dialect layer), because
// the whole failure class is invisible to a pure-function check: the SQL
// parses on PostgreSQL and does not parse at all on SQLite.
package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// b282SQLiteDB opens an in-memory SQLite database with the full skygate
// schema and returns it. Mirrors setupRenameTestDB (users_rename_test.go).
func b282SQLiteDB(t *testing.T) *dbDBSource {
	t.Helper()
	_, sqlDB, err := db.OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect(:memory:): %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.ApplyMigrations(sqlDB, db.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations(sqlite): %v", err)
	}
	return &dbDBSource{db: sqlDB}
}

// TestBuildUnifiedAuditQueryIsDialectCleanB282 pins both SQL forms of the
// /admin/audit statement. The SQLite form is the regression: a single
// `::` anywhere in it makes the whole UNION unparseable.
func TestBuildUnifiedAuditQueryIsDialectCleanB282(t *testing.T) {
	pgs := []string{"::text", "to_timestamp("}
	pgQuery, _ := buildUnifiedAuditQuery(db.DialectPostgres, "login_fail", "daniil", "", time.Hour, 50)
	for _, token := range pgs {
		if !strings.Contains(pgQuery, token) {
			t.Errorf("PostgreSQL query lost %q — the timestamp/literal casts were how this page "+
				"was written; query:\n%s", token, pgQuery)
		}
	}

	liteQuery, _ := buildUnifiedAuditQuery(db.DialectSQLite, "login_fail", "daniil", "", time.Hour, 50)
	for _, token := range append(pgs, "::") {
		if strings.Contains(liteQuery, token) {
			t.Errorf("SQLite query contains %q — SQLite has no such token and the page answers "+
				"`unrecognized token: \":\"` (the live B282 500); query:\n%s", token, liteQuery)
		}
	}
	if !strings.Contains(liteQuery, "CAST") && strings.Contains(liteQuery, "detail::") {
		t.Errorf("SQLite query still casts cluster_audit.detail the PG way:\n%s", liteQuery)
	}

	// Placeholders: both forms must stay $N (modernc.org/sqlite binds
	// $NNN by ordinal; pgx rejects `?`). The count must also match the
	// argument slice, or Query() fails at bind time.
	pgQ, pgArgs := buildUnifiedAuditQuery(db.DialectPostgres, "x", "y", "", time.Hour, 50)
	liteQ, liteArgs := buildUnifiedAuditQuery(db.DialectSQLite, "x", "y", "", time.Hour, 50)
	for _, built := range []struct {
		name string
		q    string
		args []any
	}{
		{"postgres", pgQ, pgArgs},
		{"sqlite", liteQ, liteArgs},
	} {
		if n := strings.Count(built.q, "$"); n != len(built.args) {
			t.Errorf("%s: query has %d $N placeholders but %d args — bind mismatch", built.name, n, len(built.args))
		}
		if strings.Contains(built.q, "?") {
			t.Errorf("%s: query uses `?` placeholders — pgx rejects them (SQLSTATE 42601)", built.name)
		}
	}

	// An unknown/empty source filter list must be reported, not turned
	// into an empty (syntax-error) UNION.
	if q, _ := buildUnifiedAuditQuery(db.DialectSQLite, "", "", "nonsense", 0, 50); q != "" {
		t.Errorf("unknown source filter produced a query instead of the empty marker:\n%s", q)
	}
}

// TestAuditQueryRunsOnSQLiteB282 is the runtime half: the generated
// statement must actually execute and return the audit rows. Pre-B282
// this exact call failed with
// `SQL logic error: unrecognized token: ":"`.
func TestAuditQueryRunsOnSQLiteB282(t *testing.T) {
	dbc := b282SQLiteDB(t)
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)

	seed := func(ts time.Time, action, detail, targetType, targetID string) {
		t.Helper()
		if _, err := dbc.db.Exec(
			`INSERT INTO audit_log (user_id, username, action, detail, target_type, target_id, created_at)
			 VALUES (1, 'daniil', $1, $2, $3, $4, $5)`,
			action, detail, targetType, targetID, ts.Unix(),
		); err != nil {
			t.Fatalf("seed audit_log(%s): %v", action, err)
		}
	}
	seed(now, "my_exit_rules_apply_preferred", "preferred=exit-node-vps updated=21", "acl", "42")
	seed(old, "login_ok", "ancient row", "", "")

	// No filter: both rows, newest first, timestamps decoded from the
	// INTEGER column.
	query, args := buildUnifiedAuditQuery(db.DialectSQLite, "", "", "", 0, 200)
	rows, err := dbc.db.Query(query, args...)
	if err != nil {
		t.Fatalf("audit query failed on SQLite (the live B282 500): %v\nquery:\n%s", err, query)
	}
	defer rows.Close()

	type got struct {
		source, ts, actor, action, target, detail string
	}
	var out []got
	for rows.Next() {
		var (
			source, actor, action, target, targetType, targetID, detail, result, errMsg string
			tsRaw                                                                        any
		)
		if err := rows.Scan(&source, &tsRaw, &actor, &action, &target, &targetType, &targetID,
			&detail, &result, &errMsg); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		ts, ok := db.ParseDBTime(tsRaw)
		if !ok {
			t.Fatalf("timestamp %#v did not decode (B282: audit_log.created_at is INTEGER unix seconds on SQLite)", tsRaw)
		}
		out = append(out, got{source, ts.UTC().Format("2006-01-02 15:04:05"), actor, action, target, detail})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d audit rows, want 2: %+v", len(out), out)
	}
	if out[0].action != "my_exit_rules_apply_preferred" {
		t.Errorf("newest row first: got action=%q, want my_exit_rules_apply_preferred", out[0].action)
	}
	if out[0].source != "audit_log" || out[0].actor != "daniil" {
		t.Errorf("source/actor = %q/%q, want audit_log/daniil", out[0].source, out[0].actor)
	}
	if out[0].target != "acl:42" {
		t.Errorf("target = %q, want acl:42 (target_type || ':' || target_id)", out[0].target)
	}
	if want := now.Format("2006-01-02 15:04:05"); out[0].ts != want {
		t.Errorf("timestamp = %q, want %q — the INTEGER unix column must render as a real time", out[0].ts, want)
	}

	// The ?since= filter: bound as unix seconds on SQLite, so it must
	// actually exclude the 48h-old row.
	query, args = buildUnifiedAuditQuery(db.DialectSQLite, "", "", "", 24*time.Hour, 200)
	rows2, err := dbc.db.Query(query, args...)
	if err != nil {
		t.Fatalf("filtered audit query failed on SQLite: %v\nquery:\n%s", err, query)
	}
	defer rows2.Close()
	n := 0
	for rows2.Next() {
		n++
	}
	if n != 1 {
		t.Errorf("?since=24h matched %d rows, want 1 — binding a time.Time instead of unix seconds "+
			"makes a TEXT/INTEGER comparison that is always false on SQLite", n)
	}
}

// TestPreferredMismatchQueryRunsOnSQLiteB282 covers the second live
// failure: the system test's join cast `r.device_id::text`.
func TestPreferredMismatchQueryRunsOnSQLiteB282(t *testing.T) {
	dbc := b282SQLiteDB(t)

	if _, err := dbc.db.Exec(
		`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
		 VALUES ('1', 86, 'daniil', 'tag:dev-daniil-workpc', 'workpc')`); err != nil {
		t.Fatalf("seed node_owner_map: %v", err)
	}
	if _, err := dbc.db.Exec(
		`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, enabled)
		 VALUES (1, 1, 'exit-node-vps', 'subnet', '104.16.0.0/12', 1),
		        (1, 1, 'exit-node-vps', 'domain', 'openai.com', 1)`); err != nil {
		t.Fatalf("seed device_rules: %v", err)
	}

	// PG form keeps the shorthand (the join is text = integer without it).
	pgQuery := preferredMismatchRulesQuery(db.DialectPostgres)
	if !strings.Contains(pgQuery, "r.device_id::text") {
		t.Errorf("PostgreSQL form lost the ::text cast:\n%s", pgQuery)
	}
	liteQuery := preferredMismatchRulesQuery(db.DialectSQLite)
	if strings.Contains(liteQuery, "::") {
		t.Errorf("SQLite form contains \"::\" — the statement does not parse there (the live B282 "+
			"`unrecognized token: \":\"`):\n%s", liteQuery)
	}

	// The SQLite form must run and resolve the hostname through the join.
	// The query deliberately selects EVERY enabled rule with a non-empty
	// exit_node_id (the test cross-checks prefs in Go afterwards), so both
	// the subnet and the domain rule are expected here — the regression is
	// the JOIN, not the filter.
	rows, err := dbc.db.Query(liteQuery)
	if err != nil {
		t.Fatalf("preferred-mismatch query failed on SQLite (the live B282 test failure): %v\nquery:\n%s",
			err, liteQuery)
	}
	defer rows.Close()
	matched := 0
	for rows.Next() {
		var userID int64
		var host, exit string
		if err := rows.Scan(&userID, &host, &exit); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if host != "workpc" || exit != "exit-node-vps" {
			t.Errorf("row = user=%d host=%q exit=%q, want host=workpc exit=exit-node-vps", userID, host, exit)
		}
		matched++
	}
	if matched != 2 {
		t.Errorf("query matched %d rules, want 2 (both enabled rules; the hostname must come from the "+
			"node_owner_map join)", matched)
	}
}

// TestAuditQueryClusterBranchOnSQLiteB282 covers the second half of the
// UNION: cluster_audit. Its SQLite DDL is self-inconsistent — the column is
// declared INTEGER but defaults to CURRENT_TIMESTAMP, which SQLite evaluates
// to the TEXT form "2006-01-02 15:04:05" — so the same column can hold unix
// seconds or text. Both must render, and neither shape may make the statement
// fail to parse (the pre-B282 form used to_timestamp + detail::text here).
func TestAuditQueryClusterBranchOnSQLiteB282(t *testing.T) {
	dbc := b282SQLiteDB(t)

	// A TEXT row (what the SQLite DDL default produces) …
	if _, err := dbc.db.Exec(
		`INSERT INTO cluster_audit (cluster_id, actor, action, target_node_id, detail, result, error_message, created_at)
		 VALUES ('skygate-staging', 'system', 'node_health', 'n1', '{"reason":"ha.tick"}', 'ok', '', '2026-09-22 15:00:00')`); err != nil {
		t.Fatalf("seed cluster_audit (TEXT timestamp): %v", err)
	}
	// … and a unix-seconds row.
	if _, err := dbc.db.Exec(
		`INSERT INTO cluster_audit (cluster_id, actor, action, target_node_id, detail, result, error_message, created_at)
		 VALUES ('skygate-staging', 'daniil', 'node_drain', 'n2', '{"reason":"manual"}', 'ok', '', 1790100000)`); err != nil {
		t.Fatalf("seed cluster_audit (INTEGER timestamp): %v", err)
	}

	query, args := buildUnifiedAuditQuery(db.DialectSQLite, "", "", "cluster_audit", 0, 200)
	rows, err := dbc.db.Query(query, args...)
	if err != nil {
		t.Fatalf("cluster_audit branch failed on SQLite: %v\nquery:\n%s", err, query)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var (
			source, actor, action, target, targetType, targetID, detail, result, errMsg string
			tsRaw                                                                        any
		)
		if err := rows.Scan(&source, &tsRaw, &actor, &action, &target, &targetType, &targetID,
			&detail, &result, &errMsg); err != nil {
			t.Fatalf("scan cluster_audit row: %v", err)
		}
		if source != "cluster_audit" {
			t.Errorf("source = %q, want cluster_audit", source)
		}
		if targetType != "cluster_node" {
			t.Errorf("target_type = %q, want cluster_node", targetType)
		}
		if _, ok := db.ParseDBTime(tsRaw); !ok {
			t.Errorf("cluster_audit timestamp %#v (%s) did not decode — both the TEXT "+
				"CURRENT_TIMESTAMP form and unix seconds must be readable", tsRaw, action)
		}
		seen[action] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if !seen["node_health"] || !seen["node_drain"] {
		t.Errorf("cluster_audit rows seen = %v, want both node_health (TEXT ts) and node_drain (INTEGER ts)", seen)
	}
}

// TestListACLRendersStringifiedPolicyB282 is the /admin/headscale/acl
// regression: a stringified policy must render, not 500.
func TestListACLRendersStringifiedPolicyB282(t *testing.T) {
	dbc := b282SQLiteDB(t)
	hsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/policy" && r.Method == http.MethodGet {
			// The live `aro` shape: the policy is a JSON STRING.
			_, _ = w.Write([]byte(`{"policy":"{\"acls\":[{\"action\":\"accept\",\"src\":[\"tag:dev-daniil-workpc\"],\"dst\":[\"h-rule-104-16-0-0-12\"]}],\"hosts\":{\"h-rule-104-16-0-0-12\":\"104.16.0.0/12\"}}"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer hsSrv.Close()

	hs := headscale.New(hsSrv.URL, "fake-key")
	hs.SetCacheTTL(0)
	svc := &Service{
		DB:         dbc,
		HSGlobalFn: func() *headscale.Client { return hs },
	}

	view, err := svc.ListACL(context.Background())
	if err != nil {
		t.Fatalf("ListACL on a stringified policy: %v (the live B282 `unmarshal policy: json: cannot "+
			"unmarshal string into Go value of type admin.ACLView` 500)", err)
	}
	if view.TotalCount != 1 {
		t.Errorf("TotalCount = %d, want 1 (parsed grants)", view.TotalCount)
	}
	if view.ExternalCount != 1 {
		t.Errorf("ExternalCount = %d, want 1 (no headscale_acl_rules row for this fingerprint)", view.ExternalCount)
	}
	if len(view.AllACLs) == 0 || len(view.AllACLs[0].Src) != 1 ||
		view.AllACLs[0].Src[0] != "tag:dev-daniil-workpc" {
		t.Errorf("parsed grant lost its src: %+v", view.AllACLs)
	}
	// PolicyRaw stays the operator-facing bytes (the "show source"
	// toggle must keep showing what headscale actually said).
	if !strings.Contains(view.PolicyRaw, "tag:dev-daniil-workpc") {
		t.Errorf("PolicyRaw lost the raw policy text: %q", view.PolicyRaw)
	}
}
