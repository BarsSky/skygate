// internal/feature/admin/cluster_ha_sqlite_b291_test.go — B291 (2026-09-22).
//
// Live on the native `aro` host (SQLite): /admin/cluster and /admin/ha rendered
// with NO nodes, NO invites and NO events, even though the cluster_* tables were
// being written by the discovery tick. Both page loaders were PostgreSQL-shaped:
//
//	cluster_invite  expires_at > NOW()                     → no such function: NOW
//	cluster_audit   extract(epoch FROM created_at)::bigint → syntax error
//	                detail::text
//	audit_log       unix_timestamp                         → no such column (BOTH dialects!)
//	cluster_node    joined_at/last_seen_at into NullTime   → the row was dropped
//
// These tests call the REAL page-data collectors against a REAL migrated SQLite
// database, so "the SQL parses" is not mistaken for "the page renders".
package admin

import (
	"net/http/httptest"
	"testing"
	"time"
)

// TestB291_ClusterPagePopulatesOnSQLite asserts that every section of
// /admin/cluster has real content on SQLite.
func TestB291_ClusterPagePopulatesOnSQLite(t *testing.T) {
	dbc := b282SQLiteDB(t)
	d := dbc.db

	now := time.Now().UTC()
	stale := now.Add(-10 * time.Minute)

	if _, err := d.Exec(
		`INSERT INTO cluster (id, name, chain) VALUES ('skygate-staging', 'staging', '[]')`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	// Two rows, each carrying a DIFFERENT SQLite timestamp shape: RFC3339 (what
	// DialectKind.TimeValue writes) and Go's time.Time String() form (what the
	// pre-B291 writers stored). BOTH must render a timestamp, not "—", and
	// neither may make the scan drop the row.
	if _, err := d.Exec(`
		INSERT INTO cluster_node
		  (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at, last_seen_at)
		VALUES ('node-primary', 'skygate-staging', 'primary', '100.64.0.1', '{skygate}', 'ready', 'v1.5.55', $1, $2)`,
		stale.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed cluster_node (RFC3339): %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO cluster_node
		  (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at, last_seen_at)
		VALUES ('node-standby', 'skygate-staging', 'standby', '100.64.0.2', '{skygate-standby}', 'ready', 'v1.5.55', $1, $2)`,
		stale.Unix(), stale.Format("2006-01-02 15:04:05.999999999 -0700 MST")); err != nil {
		t.Fatalf("seed cluster_node (INTEGER + Go String()): %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO cluster_database
		  (id, cluster_id, primary_node_id, replica_node_ids, current_dsn, sslmode)
		VALUES ('skygate-staging', 'skygate-staging', 'node-primary', '{node-standby}',
		        'postgres://skygate:secret@192.0.2.10:5432/skygate?sslmode=disable', 'disable')`); err != nil {
		t.Fatalf("seed cluster_database: %v", err)
	}
	// One live pending invite + one EXPIRED pending invite (the SQL `expires_at >
	// NOW()` filter used to be the only thing hiding it, and on SQLite the whole
	// query errored, hiding the live one too).
	if _, err := d.Exec(`
		INSERT INTO cluster_invite (id, cluster_id, role, target_hostname, issued_at, expires_at, status)
		VALUES ('inv-live', 'skygate-staging', 'skygate-standby', 'standby', $1, $2, 'pending'),
		       ('inv-dead', 'skygate-staging', 'skygate-standby', 'standby', $3, $4, 'pending')`,
		now.Format(time.RFC3339Nano), now.Add(24*time.Hour).Format(time.RFC3339Nano),
		now.Add(-48*time.Hour).Unix(), now.Add(-24*time.Hour).Unix()); err != nil {
		t.Fatalf("seed cluster_invite: %v", err)
	}
	// A cluster audit row whose created_at is the TEXT form the SQLite DDL default
	// produces, plus a node_health row with INTEGER unix seconds.
	if _, err := d.Exec(`
		INSERT INTO cluster_audit (cluster_id, actor, action, target_node_id, detail, result, created_at)
		VALUES ('skygate-staging', 'daniil', 'node_join', 'node-standby', '{"reason":"manual"}', 'ok', '2026-09-22 15:00:00'),
		       ('skygate-staging', 'elector', 'node_health', 'node-primary', '{"from":"ready","to":"failed"}', 'ok', $1)`,
		now.Unix()); err != nil {
		t.Fatalf("seed cluster_audit: %v", err)
	}

	svc := &Service{DB: dbc, SelfHostname: "primary"}
	req := httptest.NewRequest("GET", "/admin/cluster", nil)
	data := svc.collectClusterPageData(req)

	if data.FlashError != "" {
		t.Fatalf("FlashError = %q — the page renders this instead of its sections", data.FlashError)
	}
	if !data.HasCluster || data.ClusterName != "staging" {
		t.Errorf("HasCluster/ClusterName = %v/%q, want true/staging", data.HasCluster, data.ClusterName)
	}
	if data.NodeCount != 2 {
		t.Fatalf("NodeCount = %d, want 2 (a timestamp shape must not drop the row)", data.NodeCount)
	}
	for _, n := range data.Nodes {
		if n.JoinedAt == "—" || n.LastSeenAt == "—" {
			t.Errorf("node %s rendered a placeholder timestamp (%q / %q) — ParseDBTime must "+
				"decode every shape the column can hold", n.Hostname, n.JoinedAt, n.LastSeenAt)
		}
	}
	if !data.DBConfigured || data.DBPrimary != "node-primary" || data.DBReplicaCnt != 1 {
		t.Errorf("DB section = configured:%v primary:%q replicas:%d, want true/node-primary/1",
			data.DBConfigured, data.DBPrimary, data.DBReplicaCnt)
	}
	if data.DBDSNHost != "192.0.2.10:5432" {
		t.Errorf("DBDSNHost = %q, want 192.0.2.10:5432", data.DBDSNHost)
	}
	if data.InviteCount != 1 {
		t.Errorf("InviteCount = %d, want 1 (the live invite only — the expired one is filtered "+
			"in Go now that `expires_at > NOW()` is gone)", data.InviteCount)
	} else if data.Invites[0].ID != "inv-live" {
		t.Errorf("invite id = %q, want inv-live", data.Invites[0].ID)
	} else if data.Invites[0].ExpiresInSec <= 0 {
		t.Errorf("ExpiresInSec = %d, want a positive countdown", data.Invites[0].ExpiresInSec)
	}
	if len(data.RecentEvents) != 2 {
		t.Fatalf("RecentEvents = %d, want 2 (the cluster_audit query used extract(epoch …)::bigint)",
			len(data.RecentEvents))
	}
	for _, ev := range data.RecentEvents {
		if ev.WhenUnix == 0 {
			t.Errorf("event %s has WhenUnix=0 — its timestamp did not decode", ev.Action)
		}
		if ev.Detail == "" {
			t.Errorf("event %s lost its detail (detail::text is PostgreSQL-only)", ev.Action)
		}
	}
	if data.OnlineCount+data.OfflineCount != 2 {
		t.Errorf("online+offline = %d, want 2", data.OnlineCount+data.OfflineCount)
	}
}

// TestB291_HAPagePopulatesOnSQLite asserts the same for /admin/ha, including the
// audit_log branch of the events union — that query selected a column
// (`unix_timestamp`) which exists in NEITHER schema, so half the event history was
// silently missing on PostgreSQL too.
func TestB291_HAPagePopulatesOnSQLite(t *testing.T) {
	dbc := b282SQLiteDB(t)
	d := dbc.db

	now := time.Now().UTC()
	if _, err := d.Exec(
		`INSERT INTO cluster (id, name, chain) VALUES ('skygate-staging', 'staging', '[]')`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO cluster_node
		  (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at, last_seen_at)
		VALUES ('node-primary', 'skygate-staging', 'primary', '100.64.0.1', '{skygate}', 'ready', 'v1.5.55', $1, $1),
		       ('node-standby', 'skygate-staging', 'standby', '100.64.0.2', '{skygate-standby}', 'ready', 'v1.5.55', $1, $2)`,
		now.Format(time.RFC3339Nano), now.Unix()); err != nil {
		t.Fatalf("seed cluster_node: %v", err)
	}
	// audit_log.created_at is INTEGER unix seconds on both dialects.
	if _, err := d.Exec(`
		INSERT INTO audit_log (user_id, username, action, detail, created_at)
		VALUES (1, 'daniil', 'ha.node.add', 'legacy ha event', $1)`, now.Unix()); err != nil {
		t.Fatalf("seed audit_log: %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO cluster_audit (cluster_id, actor, action, target_node_id, detail, result, created_at)
		VALUES ('skygate-staging', 'elector', 'node_health', 'node-primary',
		        '{"reason":"ha.tick","from":"ready","to":"failed"}', 'ok', '2026-09-22 15:00:00')`); err != nil {
		t.Fatalf("seed cluster_audit: %v", err)
	}

	svc := &Service{DB: dbc, SelfHostname: "primary"}
	req := httptest.NewRequest("GET", "/admin/ha", nil)
	data := svc.collectHAPageData(req)

	if len(data.ClusterNodes) != 2 {
		t.Fatalf("ClusterNodes = %d, want 2 (the failover button table)", len(data.ClusterNodes))
	}
	var sawPrimary, sawEligibleStandby bool
	for _, row := range data.ClusterNodes {
		switch row.Hostname {
		case "primary":
			if row.LastSeenUnix == 0 {
				t.Errorf("primary LastSeenUnix = 0 — `COALESCE(extract(epoch FROM last_seen_at)::bigint, 0)` " +
					"is PostgreSQL-only; ParseDBTime must fill it")
			}
			if row.EligibleForPromote {
				t.Errorf("primary must not be eligible for promotion (already primary): %+v", row)
			}
			sawPrimary = true
		case "standby":
			if row.LastSeenUnix == 0 {
				t.Errorf("standby LastSeenUnix = 0 — its last_seen_at was written as INTEGER unix seconds")
			}
			if !row.EligibleForPromote {
				t.Errorf("standby must be eligible for promotion (ready + skygate-standby, no skygate "+
					"role): %+v", row)
			}
			sawEligibleStandby = true
		}
	}
	if !sawPrimary || !sawEligibleStandby {
		t.Errorf("node rows = %+v, want both primary and standby", data.ClusterNodes)
	}

	if len(data.RecentEvents) != 2 {
		t.Fatalf("RecentEvents = %d, want 2 (one from audit_log, one from cluster_audit — the "+
			"audit_log branch selected `unix_timestamp`, which exists in neither schema)", len(data.RecentEvents))
	}
	sources := map[string]bool{}
	for _, ev := range data.RecentEvents {
		if ev.WhenUnix == 0 {
			t.Errorf("event %s/%s has WhenUnix=0", ev.Source, ev.Action)
		}
		sources[ev.Source] = true
	}
	if !sources["audit_log"] || !sources["cluster_audit"] {
		t.Errorf("event sources = %v, want both audit_log and cluster_audit", sources)
	}
}
