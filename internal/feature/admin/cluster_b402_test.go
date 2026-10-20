// cluster_b402_test.go — 2026-10-20 (B402).
//
// The page half of the measurement. On the operator's installation `/admin/cluster`
// showed:
//
//	svyatoslava | {skygate-standby} | failed | v1.5.108+59fbad7 | last_seen = NOW
//
// i.e. a node the page called dead while its heartbeats kept arriving every 30 s, and
// a build three months old that the row could never update. These tests pin the two
// derived facts the page now reports — the contradiction (HeartbeatAlive) and the
// drift (VersionDrift) — plus the reason/action pair that names them.
package admin

import (
	"net/http/httptest"
	"testing"
	"time"
)

// TestB402_FailedRowWithAFreshHeartbeatIsNamedAndActionable is the live shape: the
// state says failed, the heartbeat says alive, and the page must say BOTH.
func TestB402_FailedRowWithAFreshHeartbeatIsNamedAndActionable(t *testing.T) {
	n := clusterNodeRow{
		ID:             "node-disc-svyatoslava",
		Hostname:       "svyatoslava",
		State:          "failed",
		HeartbeatAlive: true,
	}
	reason, action := clusterNodeReasonAndNext(n, false, "skyadmin")
	if reason != "cluster.reason_failed_heartbeat_alive" {
		t.Errorf("reason = %q, want cluster.reason_failed_heartbeat_alive", reason)
	}
	if action != "cluster.action_failed_heartbeat_alive" {
		t.Errorf("action = %q, want cluster.action_failed_heartbeat_alive", action)
	}

	// A genuinely dead row (no fresh heartbeat) keeps the old verdict, because the
	// elector's sentence is the only fact there is.
	dead := clusterNodeRow{ID: "x", Hostname: "x", State: "failed", HealthReason: "last_seen 17m ago"}
	if r, _ := clusterNodeReasonAndNext(dead, false, ""); r != "cluster.reason_failed" {
		t.Errorf("a dead row's reason = %q, want cluster.reason_failed (the elector's sentence)", r)
	}
}

// TestB402_VersionDriftIsMeasuredVerbatim pins the comparison rule: the "+<commit>"
// suffix is part of the build identity, an empty side is "not measured", and the hub's
// own build never drifts from itself.
func TestB402_VersionDriftIsMeasuredVerbatim(t *testing.T) {
	cases := []struct {
		node, hub string
		drift     bool
	}{
		{"v1.5.108+59fbad7", "v1.5.142+9de732f0", true},   // the live case
		{"v1.5.144+1847ff75", "v1.5.144+1847ff75", false}, // current standby
		{"V1.5.144+1847FF75", "v1.5.144+1847ff75", false}, // hex case is not a difference
		{"", "v1.5.144", false},                           // older agent: not measured
		{"v1.5.144", "", false},                           // hub without a build label
	}
	for _, c := range cases {
		if got := !sameBuild(c.node, c.hub); got != c.drift {
			t.Errorf("sameBuild(%q, %q) drift = %t, want %t", c.node, c.hub, got, c.drift)
		}
	}
}

// TestB402_PageSetsTheContradictionFromTheDatabase is the end-to-end half: the
// collector must derive HeartbeatAlive from the row it reads, not from a template
// condition. Seeds the operator's live shape (state=failed + last_seen = now).
func TestB402_PageSetsTheContradictionFromTheDatabase(t *testing.T) {
	dbc := b282SQLiteDB(t)
	d := dbc.db
	if _, err := d.Exec(`INSERT INTO cluster (id, name, chain) VALUES ('skygate-staging','staging','[]')`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	// last_seen_at = now (fresh), state = failed (the elector's stale verdict).
	if _, err := d.Exec(`
		INSERT INTO cluster_node (id, cluster_id, hostname, tailscale_ip, roles, state,
		                          skygate_version, joined_at, last_seen_at)
		VALUES ('node-disc-svyatoslava','skygate-staging','svyatoslava','100.64.0.9',
		        '{skygate-standby}','failed','v1.5.108+59fbad7', $1, $1)`,
		time.Now().UTC()); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	svc := &Service{DB: dbc, SelfHostname: "skygate-host"}
	data := svc.collectClusterPageData(httptest.NewRequest("GET", "/admin/cluster", nil))
	var found bool
	for _, n := range data.Nodes {
		if n.Hostname != "svyatoslava" {
			continue
		}
		found = true
		if !n.HeartbeatAlive {
			t.Error("HeartbeatAlive = false for a failed row whose last_seen is now — the page would call a live node dead")
		}
		if n.ReasonKey != "cluster.reason_failed_heartbeat_alive" {
			t.Errorf("ReasonKey = %q, want cluster.reason_failed_heartbeat_alive", n.ReasonKey)
		}
	}
	if !found {
		t.Fatal("the seeded node is missing from the page data")
	}
}
