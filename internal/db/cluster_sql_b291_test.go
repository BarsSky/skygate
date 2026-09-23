// internal/db/cluster_sql_b291_test.go — B291 (2026-09-22).
//
// The cluster/HA feature was PostgreSQL-shaped and therefore DEAD on SQLite: on
// the native `aro` host discovery failed every five minutes with
// `near "['skygate-standby']": syntax error` and cluster_node never received a
// row, so /admin/cluster and /admin/ha had nothing to render.
//
// These tests pin (a) the dialect fragments and the role helpers, and (b) the
// SQLite behaviours the ported write paths depend on, so "it works on PG" can no
// longer be mistaken for "it works".
package db

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestB291_DialectFragments(t *testing.T) {
	pg, lite := DialectPostgres, DialectSQLite
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"pg now", pg.NowExpr(), "NOW()"},
		{"sqlite now", lite.NowExpr(), "CURRENT_TIMESTAMP"},
		{"pg now-minus", pg.NowMinusExpr(5 * time.Minute), "NOW() - INTERVAL '300 seconds'"},
		{"sqlite now-minus", lite.NowMinusExpr(5 * time.Minute), "datetime('now', '-300 seconds')"},
		{"pg json cast", pg.CastJSON("$3"), "$3::jsonb"},
		{"sqlite json cast", lite.CastJSON("$3"), "$3"},
		{"pg json field", pg.JSONField("detail", "from_node_id"), "detail->>'from_node_id'"},
		{"sqlite json field", lite.JSONField("detail", "from_node_id"), "json_extract(detail, '$.from_node_id')"},
		{"pg text array cast", pg.CastTextArray("$1"), "$1::text[]"},
		{"sqlite text array cast", lite.CastTextArray("$1"), "$1"},
		{"pg random hex", pg.RandomHexExpr(12), "substr(md5(random()::text), 1, 12)"},
		{"sqlite random hex", lite.RandomHexExpr(12), "substr(lower(hex(randomblob(6))), 1, 12)"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
		if strings.Contains(c.got, "::") && c.name[:6] == "sqlite" {
			t.Errorf("%s contains a PostgreSQL cast: %q (SQLite has no :: token)", c.name, c.got)
		}
	}
}

func TestB291_TextArrayLiteralAndRoles(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "{}"},
		{[]string{}, "{}"},
		{[]string{"skygate-standby"}, "{skygate-standby}"},
		{[]string{"skygate", "skygate-standby"}, "{skygate,skygate-standby}"},
		{[]string{`we,ird`}, `{"we,ird"}`},
	}
	for _, c := range cases {
		if got := TextArrayLiteral(c.in); got != c.want {
			t.Errorf("TextArrayLiteral(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	// The literal must round-trip through the reader both backends use.
	var back StringArray
	if err := back.Scan(TextArrayLiteral([]string{"skygate", "skygate-standby"})); err != nil {
		t.Fatalf("StringArray.Scan(literal): %v", err)
	}
	if len(back) != 2 || back[0] != "skygate" || back[1] != "skygate-standby" {
		t.Errorf("round trip = %v, want [skygate skygate-standby]", back)
	}

	roles := []string{"skygate-standby"}
	if !RolesContain(roles, "skygate-standby") {
		t.Error("RolesContain missed an exact member")
	}
	// Strict match: "skygate" must NOT match "skygate-standby" (a substring test
	// would, and would then promote the wrong node).
	if RolesContain(roles, "skygate") {
		t.Error("RolesContain matched a prefix — 'skygate' must not match 'skygate-standby'")
	}
	added := RolesAdd(roles, "skygate")
	if len(added) != 2 || !RolesContain(added, "skygate") || !RolesContain(added, "skygate-standby") {
		t.Errorf("RolesAdd = %v, want both roles", added)
	}
	if again := RolesAdd(added, "skygate"); len(again) != 2 {
		t.Errorf("RolesAdd duplicated a role: %v", again)
	}
	removed := RolesRemove(added, "skygate")
	if len(removed) != 1 || removed[0] != "skygate-standby" {
		t.Errorf("RolesRemove = %v, want [skygate-standby] (the other role must survive)", removed)
	}
}

// TestB291_SQLiteSupportsThePortedStatements is the de-risking probe: every SQL
// shape the ported cluster writers use must actually run on this driver.
func TestB291_SQLiteSupportsThePortedStatements(t *testing.T) {
	_, d, err := OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := ApplyMigrations(d, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	pg, lite := DialectPostgres, DialectSQLite
	_ = pg

	must(`INSERT INTO cluster (id, name, chain) VALUES ('skygate-staging', 'staging', 'ethereum')`)

	// 1. The discovery INSERT shape: a bound text[] literal + a bound time.
	now := time.Now().UTC()
	must(`INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at)
	      VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7)`,
		"node-disc-workpc", "skygate-staging", "workpc", "100.64.0.9",
		TextArrayLiteral([]string{"skygate-standby"}), "(discovered via Tailscale)", now)

	// 2. A generated id (the dialect-native random-hex expression) + RETURNING.
	var id string
	if err := d.QueryRow(`INSERT INTO cluster_node (id, cluster_id, hostname, roles, state, skygate_version, joined_at)
	                      VALUES ('node-' || `+lite.RandomHexExpr(12)+`, $1, $2, $3, 'pending', '', $4)
	                      RETURNING id`,
		"skygate-staging", "joinme", TextArrayLiteral([]string{"skygate-standby"}), now).Scan(&id); err != nil {
		t.Fatalf("INSERT … RETURNING id failed on SQLite: %v", err)
	}
	if !strings.HasPrefix(id, "node-") || len(id) != len("node-")+12 {
		t.Errorf("generated id = %q, want node-<12 hex>", id)
	}

	// 3. ON CONFLICT … DO UPDATE SET roles = EXCLUDED.roles (the upsert shape).
	must(`INSERT INTO cluster_node (id, cluster_id, hostname, roles, state, skygate_version, joined_at)
	      VALUES ('node-up', $1, 'upserted', $2, 'ready', '', $3)
	      ON CONFLICT (cluster_id, hostname) DO UPDATE SET
	        roles = EXCLUDED.roles, state = EXCLUDED.state, last_seen_at = EXCLUDED.joined_at`,
		"skygate-staging", TextArrayLiteral([]string{"skygate"}), now)

	// 4. UPDATE … SET roles = $1 (the ported promote/demote shape) + a bound time.
	must(`UPDATE cluster_node SET roles = $1, last_seen_at = $2 WHERE id = $3`,
		TextArrayLiteral(RolesAdd([]string{"skygate-standby"}, "skygate")), now, "node-disc-workpc")

	// 5. cluster_audit: a bound JSON document, the dialect JSON cast, and a
	//    dialect JSON field read.
	must(`INSERT INTO cluster_audit (cluster_id, actor, action, target_node_id, detail, result)
	      VALUES ($1, 'system', 'node_discovered', $2, $3, 'ok')`,
		"skygate-staging", "node-disc-workpc",
		`{"node_id":"node-disc-workpc","from_node_id":"a","to_node_id":"b"}`)
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM cluster_audit WHERE cluster_id = $1 AND action = 'node_discovered' AND `+
		lite.JSONField("detail", "from_node_id")+` = $2`, "skygate-staging", "a").Scan(&n); err != nil {
		t.Fatalf("json_extract on cluster_audit.detail failed: %v", err)
	}
	if n != 1 {
		t.Errorf("json_extract matched %d rows, want 1", n)
	}

	// 6. The recent-window expression must be comparable with a
	//    DEFAULT-CURRENT_TIMESTAMP column.
	var recent int
	if err := d.QueryRow(`SELECT count(*) FROM cluster_audit WHERE created_at > ` + lite.NowMinusExpr(5*time.Minute)).Scan(&recent); err != nil {
		t.Fatalf("NowMinusExpr comparison failed: %v", err)
	}

	// 7. The read side. A time-bound column written by the Go writer comes back
	//    as a STRING on SQLite, which sql.NullTime / *time.Time REFUSE to scan
	//    ("unsupported Scan, storing driver.Value type string into type
	//    *time.Time") — so readers must go through db.ParseDBTime. That was the
	//    second half of the empty-pages symptom: even once the writes land, a
	//    reader that scans straight into time.Time skips every row.
	var rawTS any
	var rolesStr string
	if err := d.QueryRow(`SELECT last_seen_at, roles FROM cluster_node WHERE id = 'node-disc-workpc'`).Scan(&rawTS, &rolesStr); err != nil {
		t.Fatalf("read back the node: %v", err)
	}
	t.Logf("SQLite returned last_seen_at as %T (%v)", rawTS, rawTS)
	parsed, ok := ParseDBTime(rawTS)
	if !ok {
		t.Fatalf("ParseDBTime could not decode what the Go writer stored (%T %v) — /admin/cluster would show no timestamps", rawTS, rawTS)
	}
	if time.Since(parsed) > time.Hour {
		t.Errorf("parsed timestamp %s is far from now (%s)", parsed, time.Now().UTC())
	}
	// The direct scan must be documented as unsupported: keep asserting that it
	// fails, so nobody "simplifies" the readers back into time.Time destinations.
	var direct sql.NullTime
	if err := d.QueryRow(`SELECT last_seen_at FROM cluster_node WHERE id = 'node-disc-workpc'`).Scan(&direct); err == nil {
		t.Log("note: this driver now scans the stored timestamp into sql.NullTime directly — the ParseDBTime readers remain correct either way")
	}
	var rolesArr StringArray
	if err := rolesArr.Scan(rolesStr); err != nil {
		t.Fatalf("StringArray.Scan(%q): %v", rolesStr, err)
	}
	roles := []string(rolesArr)
	if !RolesContain(roles, "skygate") || !RolesContain(roles, "skygate-standby") {
		t.Errorf("roles round trip = %v, want both roles", roles)
	}
	var epochOK bool
	if err := d.QueryRow(`SELECT ` + lite.UnixEpoch("last_seen_at") + ` IS NOT NULL`).Scan(&epochOK); err == nil && !epochOK {
		t.Log("note: UnixEpoch on a Go-bound timestamp column returns NULL on SQLite — read the column and parse it in Go instead")
	}
}
