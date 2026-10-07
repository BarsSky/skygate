// internal/feature/admin/cluster_reason_b359_test.go — B359 (2026-10-07).
//
// The /admin/cluster half of B359. The live symptom was three exit-node RELAYS
// (emilia, karolina, sharlotta) rendered as permanently-failed
// "skygate-standby" candidates with nothing anywhere saying why:
//
//	node-disc-emilia    | emilia    | failed | {skygate-standby}
//	node-disc-karolina  | karolina  | failed | {skygate-standby}
//	node-disc-sharlotta | sharlotta | failed | {skygate-standby}
//
// The page now carries a NAMED reason and a next action per row. These tests
// pin the rule table (pure) and the three facts the collector reads from the
// database (exit_servers relay classification, node_owner_map ownership, the
// elector's own `node_health` reason sentence), against a real migrated SQLite
// database so "the SQL parses" is not mistaken for "the page renders".
package admin

import (
	"net/http/httptest"
	"testing"
)

// TestB359_PageClassifiesTheLiveRelayRows is the end-to-end shape of the
// measurement: the three relays of the live deployment, plus one genuine
// skygate host.
func TestB359_PageClassifiesTheLiveRelayRows(t *testing.T) {
	dbc := b282SQLiteDB(t)
	d := dbc.db
	if _, err := d.Exec(`INSERT INTO cluster (id, name, chain) VALUES ('skygate-staging', 'staging', '[]')`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	// The live rows, verbatim: synthetic discovery ids, state=failed, the
	// default standby role, and the "(discovered via Tailscale)" version marker
	// the ticker writes.
	for _, r := range []struct{ host, ip string }{
		{"emilia", "100.64.0.3"},
		{"karolina", "100.64.0.2"},
		{"sharlotta", "100.64.0.4"},
	} {
		if _, err := d.Exec(`
			INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version)
			VALUES ($1, 'skygate-staging', $2, $3, '{skygate-standby}', 'failed', '(discovered via Tailscale)')`,
			"node-disc-"+r.host, r.host, r.ip); err != nil {
			t.Fatalf("seed cluster_node %s: %v", r.host, err)
		}
	}
	// Two of the three relays are also known to skygate as exit servers (the
	// emilia row is the one whose registration the live DB was missing, which is
	// why the tag is the decisive fact).
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, tailscale_ip) VALUES
		('n-kar', 'karolina', '100.64.0.2'), ('n-shar', 'sharlotta', '100.64.0.4')`); err != nil {
		t.Fatalf("seed exit_servers: %v", err)
	}
	// emilia carries tag:exit-node in node_owner_map (B111 re-attributed the
	// relays to the infra user with the relay's own infra tag).
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
		VALUES ('77', 3, 'infra', 'tag:exit-node', 'emilia')`); err != nil {
		t.Fatalf("seed node_owner_map: %v", err)
	}
	// A genuine second skygate host, owned by the infra user through the tag the
	// panel's onboarding mints.
	if _, err := d.Exec(`
		INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version)
		VALUES ('node-disc-svyatoslava', 'skygate-staging', 'svyatoslava', '100.64.0.20', '{skygate-standby}', 'pending', '(discovered via Tailscale)')`); err != nil {
		t.Fatalf("seed svyatoslava: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
		VALUES ('88', 3, 'infra', 'tag:dev-infra-svyatoslava', 'svyatoslava')`); err != nil {
		t.Fatalf("seed svyatoslava owner: %v", err)
	}
	// The elector's own sentence about why emilia failed (the ONLY place that
	// reason exists — no column carries it).
	if _, err := d.Exec(`INSERT INTO cluster_audit (cluster_id, actor, action, target_node_id, detail, result)
		VALUES ('skygate-staging', 'elector', 'node_health', 'node-disc-emilia',
		        '{"node_id":"node-disc-emilia","from":"pending","to":"failed","reason":"no heartbeat since pending (3x heartbeat interval)","actor":"elector"}', 'ok')`); err != nil {
		t.Fatalf("seed cluster_audit: %v", err)
	}

	svc := &Service{DB: dbc, SelfHostname: "skygate-host"}
	req := httptest.NewRequest("GET", "/admin/cluster", nil)
	data := svc.collectClusterPageData(req)
	if data.FlashError != "" {
		t.Fatalf("FlashError = %q", data.FlashError)
	}
	if data.NodeCount != 4 {
		t.Fatalf("NodeCount = %d, want 4", data.NodeCount)
	}
	byHost := map[string]clusterNodeRow{}
	for _, n := range data.Nodes {
		byHost[n.Hostname] = n
	}

	// THE PROPERTY: not one relay may be offered as a skygate-standby candidate.
	for _, host := range []string{"emilia", "karolina", "sharlotta"} {
		n, ok := byHost[host]
		if !ok {
			t.Fatalf("%s missing from the page", host)
		}
		if !n.ForeignRow {
			t.Errorf("%s.ForeignRow = false — an exit-node relay is not a skygate host", host)
		}
		if n.ReasonKey != "cluster.reason_foreign_relay" {
			t.Errorf("%s.ReasonKey = %q, want cluster.reason_foreign_relay", host, n.ReasonKey)
		}
		if n.ActionKey != "cluster.action_foreign_relay" {
			t.Errorf("%s.ActionKey = %q, want the 'remove this row' action", host, n.ActionKey)
		}
	}
	// The genuine host is NOT flagged, and its reason names the wait-for-join
	// step rather than a relay.
	host, ok := byHost["svyatoslava"]
	if !ok {
		t.Fatal("svyatoslava missing from the page")
	}
	if host.ForeignRow {
		t.Error("svyatoslava.ForeignRow = true — a genuine skygate host must not be classified as a relay")
	}
	if host.OwnedBy != "infra" || host.OwnedTag != "tag:dev-infra-svyatoslava" {
		t.Errorf("svyatoslava ownership = %q/%q, want infra/tag:dev-infra-svyatoslava (the panel-only path records ownership)",
			host.OwnedBy, host.OwnedTag)
	}
	if host.ReasonKey != "cluster.reason_pending_owned" {
		t.Errorf("svyatoslava.ReasonKey = %q, want cluster.reason_pending_owned", host.ReasonKey)
	}
	// The elector's sentence reaches the row it belongs to.
	if byHost["emilia"].HealthReason == "" {
		t.Error("emilia.HealthReason is empty — the elector's reason must be surfaced")
	}
	// And every row has a non-empty reason AND action: a blank is the silent
	// drop L-54 warns about, one page further out.
	for _, n := range data.Nodes {
		if n.ReasonKey == "" {
			t.Errorf("%s has no ReasonKey", n.Hostname)
		}
		if n.ActionKey == "" {
			t.Errorf("%s has no ActionKey", n.Hostname)
		}
	}
}

// TestB359_ReasonRuleTable pins every branch of the pure rule, including that a
// relay is classified as a relay NO MATTER what its state is — the whole point.
func TestB359_ReasonRuleTable(t *testing.T) {
	cases := []struct {
		name       string
		node       clusterNodeRow
		relay      bool
		ownedBy    string
		wantReason string
		wantAction string
	}{
		{
			name: "a relay in state ready is still not a skygate host",
			node: clusterNodeRow{Hostname: "emilia", State: "ready"}, relay: true, ownedBy: "infra",
			wantReason: "cluster.reason_foreign_relay", wantAction: "cluster.action_foreign_relay",
		},
		{
			name: "a relay in state pending is still not a skygate host",
			node: clusterNodeRow{Hostname: "karolina", State: "pending"}, relay: true,
			wantReason: "cluster.reason_foreign_relay", wantAction: "cluster.action_foreign_relay",
		},
		{
			name:       "the self row",
			node:       clusterNodeRow{Hostname: "skygate-host", State: "ready", IsSelf: true},
			wantReason: "cluster.reason_self", wantAction: "cluster.action_self",
		},
		{
			name:       "pending and nobody owns it yet",
			node:       clusterNodeRow{Hostname: "svyatoslava", State: "pending"},
			wantReason: "cluster.reason_pending_new", wantAction: "cluster.action_pending_new",
		},
		{
			name: "pending with an ownership row",
			node: clusterNodeRow{Hostname: "svyatoslava", State: "pending"}, ownedBy: "infra",
			wantReason: "cluster.reason_pending_owned", wantAction: "cluster.action_pending_owned",
		},
		{
			name: "ready and owned",
			node: clusterNodeRow{Hostname: "svyatoslava", State: "ready"}, ownedBy: "infra",
			wantReason: "cluster.reason_ready", wantAction: "cluster.action_ready",
		},
		{
			name:       "ready but unowned",
			node:       clusterNodeRow{Hostname: "svyatoslava", State: "ready"},
			wantReason: "cluster.reason_ready_unowned", wantAction: "cluster.action_ready_unowned",
		},
		{
			name:       "draining",
			node:       clusterNodeRow{Hostname: "svyatoslava", State: "draining"},
			wantReason: "cluster.reason_draining", wantAction: "cluster.action_draining",
		},
		{
			name:       "failed with the elector's reason",
			node:       clusterNodeRow{Hostname: "svyatoslava", State: "failed", HealthReason: "last_seen 1m0s ago (3+ missed heartbeats)"},
			wantReason: "cluster.reason_failed", wantAction: "cluster.action_failed",
		},
		{
			name:       "failed with no event at all",
			node:       clusterNodeRow{Hostname: "svyatoslava", State: "failed"},
			wantReason: "cluster.reason_failed_unknown", wantAction: "cluster.action_failed",
		},
		{
			name:       "an unmapped state is still named",
			node:       clusterNodeRow{Hostname: "svyatoslava", State: "quarantined"},
			wantReason: "cluster.reason_unknown", wantAction: "cluster.action_unknown",
		},
	}
	for _, c := range cases {
		gotReason, gotAction := clusterNodeReasonAndNext(c.node, c.relay, c.ownedBy)
		if gotReason != c.wantReason || gotAction != c.wantAction {
			t.Errorf("%s: clusterNodeReasonAndNext = %q/%q, want %q/%q",
				c.name, gotReason, gotAction, c.wantReason, c.wantAction)
		}
	}
}

// TestB359_ExtractAuditReason covers the JSON-lite reader: the elector's reasons
// are fixed sentences with no escaped quotes, and anything unexpected must
// degrade to "" (rendered as "unknown") rather than to a WRONG sentence.
func TestB359_ExtractAuditReason(t *testing.T) {
	cases := []struct{ detail, want string }{
		{`{"from":"pending","to":"failed","reason":"no heartbeat since pending (3x heartbeat interval)"}`,
			"no heartbeat since pending (3x heartbeat interval)"},
		{`{"reason":"last_seen 42s ago (3+ missed heartbeats)","actor":"elector"}`,
			"last_seen 42s ago (3+ missed heartbeats)"},
		{detail: `{"from":"ready","to":"failed"}`},
		{detail: ``},
		{detail: `not json at all`},
		{detail: `{"reason":`},
	}
	for _, c := range cases {
		if got := extractAuditReason(c.detail); got != c.want {
			t.Errorf("extractAuditReason(%q) = %q, want %q", c.detail, got, c.want)
		}
	}
}

// TestB359_LoadersReadTheRealSchema exercises the three collector helpers
// against the migrated schema, because each one is a query that only the
// database can falsify.
func TestB359_LoadersReadTheRealSchema(t *testing.T) {
	dbc := b282SQLiteDB(t)
	d := dbc.db
	if _, err := d.Exec(`INSERT INTO cluster (id, name, chain) VALUES ('skygate-staging', 'staging', '[]')`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state)
		VALUES ('node-disc-svyatoslava', 'skygate-staging', 'Svyatoslava', '100.64.0.20', '{skygate-standby}', 'failed'),
		       ('node-disc-other', 'skygate-staging', 'other', '100.64.0.21', '{skygate-standby}', 'ready')`); err != nil {
		t.Fatalf("seed cluster_node: %v", err)
	}
	// The operator's row keeps its own casing; the match must be
	// case-insensitive both ways.
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
		VALUES ('88', 3, 'infra', 'tag:dev-infra-svyatoslava', 'SVYATOSLAVA')`); err != nil {
		t.Fatalf("seed node_owner_map: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO cluster_audit (cluster_id, actor, action, target_node_id, detail, result)
		VALUES ('skygate-staging', 'elector', 'node_health', 'node-disc-svyatoslava', '{"reason":"first sentence"}', 'ok'),
		       ('skygate-staging', 'elector', 'node_health', 'node-disc-svyatoslava', '{"reason":"older sentence"}', 'ok'),
		       ('skygate-staging', 'elector', 'node_health', 'node-disc-other', '{"from":"ready","to":"failed"}', 'ok')`); err != nil {
		t.Fatalf("seed cluster_audit: %v", err)
	}

	owned := loadClusterOwnership(d, "skygate-staging")
	if got := owned["svyatoslava"]; got.username != "infra" || got.tag != "tag:dev-infra-svyatoslava" {
		t.Errorf("loadClusterOwnership = %+v, want the infra row (matched case-insensitively)", got)
	}
	reasons := loadClusterHealthReasons(d, "skygate-staging")
	if got := reasons["node-disc-svyatoslava"]; got != "older sentence" {
		// ORDER BY id DESC: the newest row wins. (The fixture inserts "first"
		// then "older" so the second insert is the newest.)
		t.Errorf("health reason = %q, want the newest node_health sentence", got)
	}
	if got, ok := reasons["node-disc-other"]; !ok || got != "" {
		t.Errorf("a node_health row without a reason must map to \"\" (rendered as unknown), got %q (present=%v)", got, ok)
	}
}
