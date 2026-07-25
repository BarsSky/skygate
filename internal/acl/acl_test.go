package acl

// 2026-07-13: Этап 11 part 2b — tests for the shared ACL pipeline.
// GenerateACL is exercised end-to-end via the handlers tests
// (which still use it indirectly through the App wrapper) and
// SaveACLSnapshot + ApplyACLPipeline are tested directly here.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// minimalSchema covers the tables ApplyACLPipeline touches. The
// production migrations are not run here because the test stays
// in-memory and the schema is small.
const minimalSchema = `
CREATE TABLE portal_users (
	id INTEGER PRIMARY KEY,
	username TEXT NOT NULL,
	password_hash TEXT DEFAULT '',
	is_admin INTEGER DEFAULT 0,
	headscale_user_id INTEGER DEFAULT 0,
	theme TEXT DEFAULT 'linear',
	created_at INTEGER DEFAULT 0,
	default_device_node_id TEXT NOT NULL DEFAULT '',
	default_exit_node_id TEXT NOT NULL DEFAULT '',
	headscale_url TEXT NOT NULL DEFAULT '',
	headscale_api_key_enc TEXT NOT NULL DEFAULT ''
);
CREATE TABLE device_rules (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL,
	device_id INTEGER NOT NULL,
	exit_node_id TEXT NOT NULL DEFAULT '',
	target_type TEXT NOT NULL DEFAULT 'domain',
	target_value TEXT NOT NULL,
	action TEXT DEFAULT 'accept',
	device_ip TEXT DEFAULT '',
	parent_domain TEXT DEFAULT '',
	enabled INTEGER DEFAULT 1,
	user_name        TEXT NOT NULL DEFAULT '',
	device_hostname  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE user_exit_node_prefs (
	user_id INTEGER NOT NULL PRIMARY KEY,
	exit_node_tag TEXT NOT NULL,
	set_by_user_id INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE device_exit_node_prefs (
	user_id INTEGER NOT NULL,
	device_hostname TEXT NOT NULL,
	exit_node_tag TEXT NOT NULL,
	set_by_user_id INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, device_hostname)
);
CREATE TABLE acl_snapshots (
	id INTEGER PRIMARY KEY,
	version INTEGER NOT NULL,
	config TEXT NOT NULL,
	created_by TEXT NOT NULL,
	applied_success INTEGER DEFAULT NULL,
	error_msg TEXT DEFAULT '',
	created_at INTEGER DEFAULT 0
);
CREATE TABLE exit_rule_logs (
	id INTEGER PRIMARY KEY,
	version INTEGER NOT NULL,
	action TEXT NOT NULL,
	detail TEXT DEFAULT '',
	created_at INTEGER DEFAULT 0
);
CREATE TABLE user_subnets (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL UNIQUE,
	cidr TEXT NOT NULL,
	bits INTEGER NOT NULL DEFAULT 24,
	status TEXT NOT NULL DEFAULT 'pending',
	control_plane_url TEXT NOT NULL DEFAULT '',
	router_node_id TEXT NOT NULL DEFAULT '',
	router_container_id TEXT NOT NULL DEFAULT '',
	router_hostname TEXT NOT NULL DEFAULT '',
	created_at INTEGER DEFAULT 0,
	updated_at INTEGER DEFAULT 0
);
CREATE TABLE node_owner_map (
	node_id TEXT PRIMARY KEY,
	headscale_user_id INTEGER NOT NULL DEFAULT 0,
	username TEXT NOT NULL DEFAULT '',
	tag TEXT NOT NULL DEFAULT '',
	tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
	tagged_at INTEGER NOT NULL DEFAULT 0,
	hostname TEXT NOT NULL DEFAULT ''
);

CREATE TABLE user_subnet_shares (
	grantor_user_id INTEGER NOT NULL,
	grantee_user_id INTEGER NOT NULL,
	created_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (grantor_user_id, grantee_user_id),
	FOREIGN KEY (grantor_user_id) REFERENCES portal_users(id) ON DELETE CASCADE,
	FOREIGN KEY (grantee_user_id) REFERENCES portal_users(id) ON DELETE CASCADE
);
CREATE TABLE meshes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	code TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL DEFAULT '',
	creator_user_id INTEGER NOT NULL,
	status TEXT NOT NULL DEFAULT 'active',
	created_at INTEGER NOT NULL DEFAULT 0,
	dissolved_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE mesh_members (
	mesh_id INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	joined_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (mesh_id, user_id)
);
`

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, q := range strings.Split(minimalSchema, ";") {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedPortalUser(t *testing.T, d *sql.DB, username string) int64 {
	return seedPortalUserWithPlane(t, d, username, "")
}

func seedPortalUserWithPlane(t *testing.T, d *sql.DB, username, planeURL string) int64 {
	t.Helper()
	// minimalSchema only declares the columns GenerateACL
	// reads (id, username, headscale_user_id, headscale_url).
	// The production schema has more; the test schema is
	// kept in lock-step.
	res, err := d.Exec(
		`INSERT INTO portal_users (username, headscale_url) VALUES (?, ?)`,
		username, planeURL)
	if err != nil {
		t.Fatalf("seed user %s on plane %s: %v", username, planeURL, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// seedUserExitNodePref inserts a row in user_exit_node_prefs
// for the given user. setByUser=0 is the implicit self-set
// (a real user picks their own preferred exit-node; tests
// don't care about who set it).
func seedUserExitNodePref(t *testing.T, d *sql.DB, userID int64, tag string) {
	t.Helper()
	_, err := d.Exec(
		`INSERT INTO user_exit_node_prefs (user_id, exit_node_tag, set_by_user_id) VALUES (?, ?, 0)`,
		userID, tag)
	if err != nil {
		t.Fatalf("seed user_exit_node_prefs user=%d tag=%s: %v", userID, tag, err)
	}
}

// seedDeviceExitNodePref inserts a row in
// device_exit_node_prefs for the given (user, device).
// Used by v0.28.4 tests to verify the per-device grant
// emission.
//
// 2026-07-25: v0.28.4.
func seedDeviceExitNodePref(t *testing.T, d *sql.DB, userID int64, deviceHostname, tag string) {
	t.Helper()
	_, err := d.Exec(
		`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id) VALUES (?, ?, ?, 0)`,
		userID, deviceHostname, tag)
	if err != nil {
		t.Fatalf("seed device_exit_node_prefs user=%d device=%s tag=%s: %v",
			userID, deviceHostname, tag, err)
	}
}

// seedUserSubnet inserts a row in user_subnets for the given
// user. The cidr is the personal subnet (typically
// 10.0.<uid>.0/24 but the helper is cidr-agnostic for
// test flexibility). status defaults to 'pending' — the
// v0.28.2 grants parser doesn't care about the status
// column. The signature differs from the helper in
// multi_subnet_integration_test.go (which has no cidr
// param — it computes 10.0.<uid>.0/24); we use a
// different name to avoid the collision.
func seedUserSubnetWithCIDR(t *testing.T, d *sql.DB, userID int64, cidr string) {
	t.Helper()
	_, err := d.Exec(
		`INSERT OR REPLACE INTO user_subnets (user_id, cidr, status) VALUES (?, ?, 'active')`,
		userID, cidr)
	if err != nil {
		t.Fatalf("seed user_subnets user=%d cidr=%s: %v", userID, cidr, err)
	}
}

// recordingAlerter captures SendAlert calls. The count is
// atomic so the async SendAlert goroutine in SaveACLSnapshot
// doesn't race with the test goroutine.
type recordingAlerter struct {
	count atomic.Int64
	last  atomic.Value // string
}

func (r *recordingAlerter) SendAlert(text string) int64 {
	r.count.Add(1)
	r.last.Store(text)
	return 0
}

func TestGenerateACLValidJSONShape(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	seedPortalUser(t, d, "bob")

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	if aclStr == "" || aclStr[0] != '{' {
		t.Fatalf("ACL JSON should start with '{', got %q...", aclStr[:min(10, len(aclStr))])
	}
	for _, want := range []string{
		// 2026-07-25: v0.28.3 — per-user rule now ends
		// with autogroup:internet:* so the user can reach
		// the public internet through their own grant
		// (instead of the catch-all that any device used
		// to be able to piggyback on).
		`"dst": ["alice@tsnet.example.com:*", "autogroup:internet:*"]`,
		`"dst": ["bob@tsnet.example.com:*", "autogroup:internet:*"]`,
		`"dst": ["tag:public:*"]`,
		`"dst": ["tag:exit-node:*"]`,
		// 2026-07-15: v0.12.0.2 — internet egress via
		// autogroup:internet (NOT a literal "*:*" catch-all,
		// which would re-introduce the inter-user leak).
		// 2026-07-25: v0.28.3 — the catch-all is now
		// src=tag:public (relay nodes only), not src=* (any
		// device). End-user devices get autogroup:internet
		// via their per-user rule (see above).
		`"dst": ["autogroup:internet:*"]`,
		// 2026-07-17: v0.17.0 — tag:subnet-router must
		// be registered in tagOwners so headscale accepts
		// the v0.16.7 sidecar nodes. Owned by all portal
		// users (so any of them can host a personal subnet
		// sidecar). The auto-approver in internal/sidecar
		// issues preauth keys with this tag.
		`"tag:subnet-router": [`,
		// Этап 14 v7: SSH rules for admin to manage
		// tag:exit-node (existing) and tag:public relay
		// nodes (new) as root. Match the multi-line JSON
		// formatting exactly so we catch accidental
		// whitespace regressions.
		`"src": ["tag:private", "admin@tsnet.example.com"],` + "\n" + `      "dst": ["tag:exit-node"]`,
		`"src": ["admin@tsnet.example.com"],` + "\n" + `      "dst": ["tag:public"]`,
	} {
		if !strings.Contains(aclStr, want) {
			t.Errorf("ACL missing %q", want)
		}
	}
	// 2026-07-15: v0.12.0.1 — the catch-all `"dst": ["*:*"]`
	// rule MUST NOT be present. With it in the ACL, any
	// inter-user traffic (e.g. alice → bob) would be
	// accepted because the catch-all matches. Tailscale's
	// default-deny semantics require the ACL to end at
	// the most-restrictive rule (here, tag:exit-node) so
	// that anything not explicitly allowed is blocked.
	if strings.Contains(aclStr, `"dst": ["*:*"]`) {
		t.Errorf("ACL must not contain the catch-all \"*:*\" rule (security: leaks every user's device)")
	}
}

// TestGenerateACL_LastRuleIsAutogroupInternet pins that the
// final rule in the acls[] array is the autogroup:internet
// internet-egress accept. This is the structural guarantee
// behind the v0.12.0.2 design:
//
//   * The per-user rules (alice → alice:*, bob → bob:*)
//     cover self-traffic.
//   * The two tag rules (* → tag:public:*, * → tag:exit-node:*)
//     cover shared resources.
//   * The autogroup:internet rule (* → autogroup:internet:*)
//     allows exit-node internet egress WITHOUT re-opening
//     inter-user access (autogroup:internet explicitly
//     excludes the tailnet's 100.64.100.0/10 range).
//
// A future refactor that adds a new "broad" rule (e.g.
// for the operator's admin tooling) must place it AFTER
// autogroup:internet, not before — otherwise it would
// still leak inter-user access. The test guards against
// the obvious regressions:
//
//   1. The literal "*:*" catch-all MUST NOT appear (would
//      allow alice → bob's device via first-match fallback).
//   2. The last rule MUST reference autogroup:internet
//      (otherwise exit-node routing on Android breaks).
func TestGenerateACL_LastRuleIsAutogroupInternet(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	var doc struct {
		Acls []map[string]any `json:"acls"`
	}
	if err := json.Unmarshal([]byte(aclStr), &doc); err != nil {
		t.Fatalf("parse ACL: %v\nraw: %s", err, aclStr)
	}
	if len(doc.Acls) == 0 {
		t.Fatalf("acls[] is empty: %s", aclStr)
	}
	last := doc.Acls[len(doc.Acls)-1]
	b, _ := json.Marshal(last)
	lastRule := string(b)
	// (1) Catch-all guard — defence in depth, also
	// covered by TestGenerateACLValidJSONShape.
	if strings.Contains(lastRule, `"dst": ["*:*"]`) {
		t.Fatalf("last rule in acls[] must not be a catch-all: %s", lastRule)
	}
	// (2) Internet-egress guard — the last rule must
	// reference autogroup:internet (the v0.12.0.2 design
	// choice; any other final rule is a regression).
	//
	// 2026-07-25: v0.28.3 — the catch-all is now
	// `src: ["tag:public"]` (NOT src: ["*"]) so only
	// relay nodes can use autogroup:internet themselves.
	// End-user devices get autogroup:internet via their
	// per-user grant, not the catch-all. The "last rule"
	// check still holds because the autogroup:internet
	// dst is the catch-all — only the src changed.
	if !strings.Contains(lastRule, "autogroup:internet:*") {
		t.Fatalf("last rule in acls[] does not reference autogroup:internet: %s", lastRule)
	}
	// (3) v0.28.3: the catch-all src MUST be tag:public
	// (not "*"). With src=* any device can use any
	// exit-node for arbitrary internet destinations,
	// including relay-3's 148 PrimaryRoutes — this is
	// the bypass the user reported. The fix is to scope
	// the catch-all to tag:public (relay nodes) so only
	// they can use autogroup:internet themselves (i.e.
	// FORWARD exit-node traffic to the internet).
	if strings.Contains(aclStr, `"src": ["*"], "dst": ["autogroup:internet:*"]`) {
		t.Fatalf("v0.28.3 fix: the autogroup:internet catch-all must be src=tag:public, not src=*. Found the bypass shape: %s", aclStr)
	}
}

// TestGenerateACL_PerUserSubnetCIDR — v0.17.0. Users
// with an allocated personal subnet get an extended
// per-user rule:
//
//   { "action": "accept",
//     "src":    ["alice@tsnet.example.com"],
//     "dst":    ["alice@tsnet.example.com:*",
//                "10.0.<uid>.0/24:*"] }
//
// Users WITHOUT a subnet keep the original
// `dst: ["alice@tsnet.example.com:*"]` (no CIDR
// appended). The CIDR is unique per user, so alice
// can reach 10.0.<alice_uid>.0/24 but not
// 10.0.<bob_uid>.0/24 — first-match semantics handle
// the isolation, and the catch-all rules (tag:public,
// tag:exit-node, autogroup:internet) still apply
// for everything else.
func TestGenerateACL_PerUserSubnetCIDR(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	bobID := seedPortalUser(t, d, "bob")
	// alice has a personal subnet (10.0.<alice_uid>.0/24).
	// bob doesn't.
	aliceCIDR := fmt.Sprintf("10.0.%d.0/24", aliceID)
	_, err := d.Exec(`INSERT INTO user_subnets
		(user_id, cidr, status, control_plane_url)
		VALUES (?, ?, 'active', '')`, aliceID, aliceCIDR)
	if err != nil {
		t.Fatalf("seed alice subnet: %v", err)
	}

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}

	// alice's per-user rule should include her CIDR.
	// The renderer writes the rule on a single line, so
	// the expected substring is a single line too.
	// 2026-07-25: v0.28.3 — per-user dst also ends with
	// "autogroup:internet:*" so the user can reach the
	// public internet through their own grant.
	wantAlice := fmt.Sprintf(
		`"src": ["alice@tsnet.example.com"], "dst": ["alice@tsnet.example.com:*", "%s:*", "autogroup:internet:*"]`,
		aliceCIDR)
	if !strings.Contains(aclStr, wantAlice) {
		t.Errorf("alice's per-user rule should include her CIDR; expected %q in ACL, got excerpt: %q",
			wantAlice, aclStr[max(0, len(aclStr)-1500):])
	}
	// bob's per-user rule should NOT include any CIDR
	// (he has no subnet allocated) but DOES include
	// autogroup:internet:* (v0.28.3).
	wantBob := `"src": ["bob@tsnet.example.com"], "dst": ["bob@tsnet.example.com:*", "autogroup:internet:*"]`
	if !strings.Contains(aclStr, wantBob) {
		t.Errorf("bob's per-user rule should NOT include a CIDR; expected %q in ACL, got excerpt: %q",
			wantBob, aclStr[max(0, len(aclStr)-1500):])
	}
	// Negative: bob's CIDR (10.0.<bob_uid>.0/24) must
	// NOT appear anywhere — alice's per-user rule must
	// not include bob's subnet.
	bobCIDR := fmt.Sprintf("10.0.%d.0/24", bobID)
	if strings.Contains(aclStr, bobCIDR) {
		t.Errorf("bob's CIDR %q should not appear in ACL (alice's per-user rule must be isolated to her own CIDR)", bobCIDR)
	}
}

// TestGenerateACL_SharedSubnetsExtendDst — v0.17.1.
// When alice grants bob access to alice's personal
// subnet, bob's per-user rule gets alice's CIDR
// appended to dst:
//
//   { "action": "accept",
//     "src":    ["bob@tsnet.example.com"],
//     "dst":    ["bob@tsnet.example.com:*",
//                "10.0.<bob>.0/24:*",        ← bob's own
//                "10.0.<alice>.0/24:*"] }    ← shared
//
// Sharing is one-directional: alice's rule does NOT
// get bob's CIDR unless alice ALSO grants herself
// access to bob's subnet (which is a separate
// Grant() call). The asymmetry matches the
// `grantor → grantee` semantics of the share row.
func TestGenerateACL_SharedSubnetsExtendDst(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	bobID := seedPortalUser(t, d, "bob")
	// Both have subnets.
	aliceCIDR := fmt.Sprintf("10.0.%d.0/24", aliceID)
	bobCIDR := fmt.Sprintf("10.0.%d.0/24", bobID)
	for _, p := range []struct{ uid int64; cidr string }{
		{aliceID, aliceCIDR}, {bobID, bobCIDR}} {
		_, err := d.Exec(`INSERT INTO user_subnets
			(user_id, cidr, status, control_plane_url)
			VALUES (?, ?, 'active', '')`, p.uid, p.cidr)
		if err != nil {
			t.Fatalf("seed subnet uid=%d: %v", p.uid, err)
		}
	}
	// alice grants bob access to alice's subnet.
	_, err := d.Exec(`INSERT INTO user_subnet_shares
		(grantor_user_id, grantee_user_id, created_at)
		VALUES (?, ?, 0)`, aliceID, bobID)
	if err != nil {
		t.Fatalf("seed share: %v", err)
	}

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}

	// bob's per-user rule should now have BOTH bob's
	// CIDR and alice's CIDR. 2026-07-25: v0.28.3 — the
	// dst list now ends with "autogroup:internet:*" so
	// the user can reach the public internet through
	// their own grant.
	wantBob := fmt.Sprintf(
		`"src": ["bob@tsnet.example.com"], "dst": ["bob@tsnet.example.com:*", "%s:*", "%s:*", "autogroup:internet:*"]`,
		bobCIDR, aliceCIDR)
	if !strings.Contains(aclStr, wantBob) {
		t.Errorf("bob's per-user rule should include shared alice CIDR; expected %q in ACL, got excerpt: %q",
			wantBob, extractExcerptFromString(aclStr, "bob@tsnet"))
	}
	// alice's per-user rule should still only have
	// alice's own CIDR (the share is one-directional;
	// alice didn't grant herself access to bob's
	// subnet). 2026-07-25: v0.28.3 — ends with
	// "autogroup:internet:*".
	wantAlice := fmt.Sprintf(
		`"src": ["alice@tsnet.example.com"], "dst": ["alice@tsnet.example.com:*", "%s:*", "autogroup:internet:*"]`,
		aliceCIDR)
	if !strings.Contains(aclStr, wantAlice) {
		t.Errorf("alice's per-user rule should NOT include bob's CIDR; expected %q in ACL, got excerpt: %q",
			wantAlice, extractExcerptFromString(aclStr, "alice@tsnet"))
	}
	// Negative: bob's CIDR should NOT appear in
	// alice's per-user rule. We extract just the
	// substring starting at alice's src and ending
	// at the next "src" (or end of the acls block),
	// then check that bob's CIDR is absent.
	aliceRuleStart := strings.Index(aclStr, `"src": ["alice@tsnet.example.com"]`)
	if aliceRuleStart < 0 {
		t.Fatal("alice's rule not found")
	}
	// Find the end of alice's rule: the next "src":
	// after the current rule. The acls list is rendered
	// with one rule per line, so the next "src" is
	// aliceRuleStart + length of her rule + some
	// separator. Walk forward until we find a second
	// "src" that isn't part of alice's own rule.
	after := aclStr[aliceRuleStart:]
	// Skip past alice's rule body. Her rule is a single
	// line ending with `}` and a newline. The next
	// "src" is bob's rule. So find the FIRST
	// subsequent `"src":` that comes AFTER the
	// closing `}` of alice's rule.
	endIdx := strings.Index(after, `}`)
	if endIdx < 0 {
		t.Fatal("alice's rule has no closing brace")
	}
	afterEnd := after[endIdx+2:]
	nextSrc := strings.Index(afterEnd, `"src": [`)
	var aliceRuleEnd int
	if nextSrc < 0 {
		aliceRuleEnd = aliceRuleStart + len(after)
	} else {
		aliceRuleEnd = aliceRuleStart + endIdx + 2 + nextSrc
	}
	aliceRule := aclStr[aliceRuleStart:aliceRuleEnd]
	if strings.Contains(aliceRule, bobCIDR) {
		t.Errorf("alice's per-user rule should NOT include bob's CIDR %q; got alice's rule: %q",
			bobCIDR, aliceRule)
	}
}

// extractExcerptFromString returns a 300-char window
// around the first occurrence of needle in haystack,
// for diagnostic output. Inline here because the
// existing tests in the same file use extractExcerpt
// from the handlers package; this test in the acl
// package can't import the handlers helper without a
// cycle.
func extractExcerptFromString(haystack, needle string) string {
	i := strings.Index(haystack, needle)
	if i < 0 {
		return "<needle not found in excerpt>"
	}
	start := i - 50
	if start < 0 {
		start = 0
	}
	end := i + len(needle) + 250
	if end > len(haystack) {
		end = len(haystack)
	}
	return haystack[start:end]
}

// TestGenerateACL_SharedSubnetsAreIdempotent — v0.17.1.
// Two share rows between the same (grantor, grantee)
// pair should produce the same ACL as one row (no
// duplicate dst entries). Grant itself is idempotent
// (PRIMARY KEY + INSERT OR IGNORE), but this test pins
// the ACL output too so a future refactor can't
// regress to listing duplicates.
func TestGenerateACL_SharedSubnetsAreIdempotent(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	bobID := seedPortalUser(t, d, "bob")
	aliceCIDR := fmt.Sprintf("10.0.%d.0/24", aliceID)
	bobCIDR := fmt.Sprintf("10.0.%d.0/24", bobID)
	for _, p := range []struct{ uid int64; cidr string }{
		{aliceID, aliceCIDR}, {bobID, bobCIDR}} {
		_, err := d.Exec(`INSERT INTO user_subnets
			(user_id, cidr, status, control_plane_url)
			VALUES (?, ?, 'active', '')`, p.uid, p.cidr)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// BUG: this would fail in real life (duplicate PK),
	// but the ACL builder doesn't care about the
	// number of rows — the query returns one row per
	// (grantee, grantor) pair. We'll insert twice
	// directly to test idempotency.
	_, _ = d.Exec(`INSERT INTO user_subnet_shares
		(grantor_user_id, grantee_user_id, created_at)
		VALUES (?, ?, 0)`, aliceID, bobID)
	_, _ = d.Exec(`INSERT OR IGNORE INTO user_subnet_shares
		(grantor_user_id, grantee_user_id, created_at)
		VALUES (?, ?, 0)`, aliceID, bobID)

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	// Count occurrences of alice's CIDR in bob's
	// rule. Should be exactly 1 (no duplicates).
	bobRuleStart := strings.Index(aclStr, `"src": ["bob@tsnet.example.com"]`)
	if bobRuleStart < 0 {
		t.Fatalf("bob's rule not found in ACL")
	}
	// Find the end of bob's rule (next '"src"' or
	// ']' followed by newline).
	bobRuleEnd := strings.Index(aclStr[bobRuleStart+10:], `"src": [`)
	if bobRuleEnd < 0 {
		bobRuleEnd = len(aclStr) - bobRuleStart
	} else {
		bobRuleEnd += bobRuleStart + 10
	}
	bobRule := aclStr[bobRuleStart:bobRuleEnd]
	count := strings.Count(bobRule, aliceCIDR)
	if count != 1 {
		t.Errorf("alice's CIDR should appear exactly once in bob's rule; got %d occurrences in %q",
			count, bobRule)
	}
}

// TestGenerateACL_ExitNodeMeshStillGlobal — v0.17.0.
// The original per-user subnets design decision: exit
// nodes must remain reachable from EVERY user, not just
// the user the sidecar belongs to. Otherwise the v0.16.0+
// subnets would break the operator's existing exit-node
// routing (relay-1, relay-2, relay-3) for users who
// haven't yet allocated a subnet.
//
// The check is structural: the rule
//   { "action": "accept", "src": ["*"], "dst": ["tag:exit-node:*"] }
// must be present in the rendered ACL. v0.14.0 v7
// already added this — v0.17.0 is a regression guard.
func TestGenerateACL_ExitNodeMeshStillGlobal(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	seedPortalUser(t, d, "bob")

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}

	// Find the exit-node rule and assert src is "*" (any
	// identity, not just admin).
	wantExit := `"src": ["*"], "dst": ["tag:exit-node:*"]`
	if !strings.Contains(aclStr, wantExit) {
		t.Errorf("exit-node mesh rule must be `src: [*] → tag:exit-node:*`; expected %q in ACL, got excerpt: %q",
			wantExit, aclStr[max(0, len(aclStr)-1500):])
	}
	// Also: tag:public mesh rule (relay nodes) must be
	// similarly global. Operators configure Caddy,
	// DERP, etc. on relay-1/relay-2/relay-3.
	wantPublic := `"src": ["*"], "dst": ["tag:public:*"]`
	if !strings.Contains(aclStr, wantPublic) {
		t.Errorf("tag:public mesh rule must be `src: [*] → tag:public:*`; expected %q in ACL, got excerpt: %q",
			wantPublic, aclStr[max(0, len(aclStr)-1500):])
	}
}

func TestGenerateACLIncludesDeviceRules(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice")
	_, _ = d.Exec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip) VALUES (?, 42, 'relay-1', 'ip', '1.2.3.4', 'accept', '100.64.100.5')`, uid)

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	if !strings.Contains(aclStr, "1.2.3.4:*") {
		t.Error("expected 1.2.3.4 entry in ACL")
	}
	if !strings.Contains(aclStr, "100.64.100.5") {
		t.Error("expected device_ip 100.64.100.5 as src in ACL")
	}
}

func TestSaveACLSnapshotWritesRow(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingAlerter{}
	ver := SaveACLSnapshot(d, `{"acls":[]}`, "alice", rec)
	if ver < 1 {
		t.Errorf("SaveACLSnapshot returned %d, want >= 1", ver)
	}
	var gotVersion int
	var gotConfig, gotBy string
	_ = d.QueryRow(`SELECT version, config, created_by FROM acl_snapshots WHERE id = 1`).Scan(&gotVersion, &gotConfig, &gotBy)
	if gotVersion != ver {
		t.Errorf("version = %d, want %d", gotVersion, ver)
	}
	if gotConfig != `{"acls":[]}` {
		t.Errorf("config = %q", gotConfig)
	}
	if gotBy != "alice" {
		t.Errorf("created_by = %q, want 'alice'", gotBy)
	}
}

func TestSaveACLSnapshotAlerterNotified(t *testing.T) {
	d := openTestDB(t)
	rec := &recordingAlerter{}
	SaveACLSnapshot(d, `{"acls":[]}`, "alice", rec)
	// SaveACLSnapshot fires SendAlert async via goroutine; the
	// count is atomic so polling is race-free. Give the scheduler
	// a moment to run the goroutine.
	for i := 0; i < 100; i++ {
		if rec.count.Load() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Error("expected SendAlert to be called, got 0 calls")
}

func TestSaveACLSnapshotNilAlerter(t *testing.T) {
	d := openTestDB(t)
	// nil alerter must not panic — the bot path relies on this.
	ver := SaveACLSnapshot(d, `{"acls":[]}`, "alice", nil)
	if ver < 1 {
		t.Errorf("ver = %d, want >= 1", ver)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 snapshot row, got %d", n)
	}
}

// fakeHeadscale is a minimal httptest-backed headscale server that
// handles PUT /api/v1/policy (the only endpoint ApplyACLPipeline
// touches). The returned *headscale.Client points at the test
// server so the test runs without a real headscale instance.
func fakeHeadscale(t *testing.T, policyStatus int, policyErr error) (*headscale.Client, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/policy" || r.Method != http.MethodPut {
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, 404)
			return
		}
		calls.Add(1)
		if policyErr != nil {
			http.Error(w, policyErr.Error(), policyStatus)
			return
		}
		w.WriteHeader(policyStatus)
		_, _ = w.Write([]byte(`{"policy":"...","updated_at":"x"}`))
	}))
	t.Cleanup(srv.Close)
	hs := headscale.New(srv.URL, "fake-key")
	return hs, &calls
}

// fakeHeadscaleWithCapture mirrors fakeHeadscale but also
// records the last SetPolicy body so tests can inspect
// what was pushed. Used by v0.13.0 per-plane tests to
// verify the per-plane policy contains only that plane's
// identities.
type capturedPolicy struct {
	mu     sync.Mutex
	config string
}

func fakeHeadscaleWithCapture(t *testing.T, policyStatus int) (*headscale.Client, *capturedPolicy) {
	t.Helper()
	cap := &capturedPolicy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/policy" || r.Method != http.MethodPut {
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, 404)
			return
		}
		var body struct {
			Policy string `json:"policy"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		cap.mu.Lock()
		cap.config = body.Policy
		cap.mu.Unlock()
		w.WriteHeader(policyStatus)
		_, _ = w.Write([]byte(`{"policy":"...","updated_at":"x"}`))
	}))
	t.Cleanup(srv.Close)
	hs := headscale.New(srv.URL, "fake-key")
	return hs, cap
}

func TestApplyACLPipelineSuccess(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	hs, hsCalls := fakeHeadscale(t, http.StatusOK, nil)
	rec := &recordingAlerter{}

	res := ApplyACLPipeline(d, hs, rec, "alice", "user alice added rule test", false)
	if !res.Applied {
		t.Errorf("Applied = false, want true; err = %v", res.Err)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	if res.Version < 1 {
		t.Errorf("Version = %d, want >= 1", res.Version)
	}
	if hsCalls.Load() != 1 {
		t.Errorf("HS SetPolicy called %d times, want 1", hsCalls.Load())
	}

	// acl_snapshots row marked applied.
	var applied sql.NullInt64
	_ = d.QueryRow(`SELECT applied_success FROM acl_snapshots WHERE version = ?`, res.Version).Scan(&applied)
	if !applied.Valid || applied.Int64 != 1 {
		t.Errorf("applied_success = %v, want 1", applied)
	}

	// exit_rule_logs has one row for the apply.
	var logAction, logDetail string
	_ = d.QueryRow(`SELECT action, detail FROM exit_rule_logs WHERE version = ? ORDER BY id DESC LIMIT 1`, res.Version).Scan(&logAction, &logDetail)
	if logAction != "apply" {
		t.Errorf("log action = %q, want %q", logAction, "apply")
	}
	if !strings.Contains(logDetail, "user alice added rule test") {
		t.Errorf("log detail = %q, want to contain the detailForLog", logDetail)
	}
}

func TestApplyACLPipelineSetPolicyError(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	hs, hsCalls := fakeHeadscale(t, http.StatusInternalServerError, fmt.Errorf("policy boom"))
	rec := &recordingAlerter{}

	res := ApplyACLPipeline(d, hs, rec, "alice", "user alice added rule test", false)
	if res.Applied {
		t.Error("Applied = true, want false on SetPolicy failure")
	}
	if res.Err == nil {
		t.Error("Err = nil, want non-nil")
	}
	if res.Version < 1 {
		t.Errorf("Version = %d, want >= 1 (snapshot is always saved)", res.Version)
	}
	if hsCalls.Load() != 1 {
		t.Errorf("HS SetPolicy called %d times, want 1", hsCalls.Load())
	}

	// acl_snapshots row exists but is NOT marked applied.
	var nApplied, nFailed int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots WHERE version = ? AND applied_success = 1`, res.Version).Scan(&nApplied)
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots WHERE version = ? AND applied_success = 0`, res.Version).Scan(&nFailed)
	if nApplied != 0 {
		t.Errorf("expected 0 applied rows on failure, got %d", nApplied)
	}
	if nFailed != 1 {
		t.Errorf("expected 1 failed row on failure, got %d", nFailed)
	}

	// error_msg captures the headscale error.
	var errMsg string
	_ = d.QueryRow(`SELECT error_msg FROM acl_snapshots WHERE version = ?`, res.Version).Scan(&errMsg)
	if !strings.Contains(errMsg, "policy boom") {
		t.Errorf("error_msg = %q, want to contain 'policy boom'", errMsg)
	}

	// exit_rule_logs has the failure row.
	var logAction, logDetail string
	_ = d.QueryRow(`SELECT action, detail FROM exit_rule_logs WHERE version = ? ORDER BY id DESC LIMIT 1`, res.Version).Scan(&logAction, &logDetail)
	if logAction != "apply_fail" {
		t.Errorf("log action = %q, want %q", logAction, "apply_fail")
	}
	if !strings.Contains(logDetail, "policy boom") {
		t.Errorf("log detail = %q, want to contain 'policy boom'", logDetail)
	}
}

func TestApplyACLPipelineGenerateACLError(t *testing.T) {
	// Closed DB → GenerateACL fails → no snapshot, no SetPolicy
	// call, returned Version=0, Err!=nil.
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = d.Close()
	hs, hsCalls := fakeHeadscale(t, http.StatusOK, nil)

	res := ApplyACLPipeline(d, hs, nil, "alice", "test", false)
	if res.Err == nil {
		t.Error("expected Err on closed DB")
	}
	if res.Applied {
		t.Error("Applied = true, want false on GenerateACL error")
	}
	if res.Version != 0 {
		t.Errorf("Version = %d, want 0 on GenerateACL error", res.Version)
	}
	if hsCalls.Load() != 0 {
		t.Errorf("HS SetPolicy called %d times on GenerateACL error, want 0", hsCalls.Load())
	}
}

func TestApplyACLPipelineNilAlerter(t *testing.T) {
	// Bot-style: no notifier, just the DB + HS writes. The
	// pipeline must not panic on nil alerter.
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	hs, _ := fakeHeadscale(t, http.StatusOK, nil)

	res := ApplyACLPipeline(d, hs, nil, "alice", "test", false)
	if !res.Applied {
		t.Errorf("Applied = false; err = %v", res.Err)
	}
}

// TestGenerateACLForPlane_ScopesToPlaneUsers pins v0.13.0:
// GenerateACLForPlane only includes the identities of
// portal users on the given control plane. Other planes'
// users are excluded — headscale rejects unknown
// identities in tagOwners, so the per-plane policy must
// be scoped.
func TestGenerateACLForPlane_ScopesToPlaneUsers(t *testing.T) {
	d := openTestDB(t)
	// alice on the global default plane; bob and carol on
	// plane "https://plane-b.example".
	seedPortalUser(t, d, "alice")
	seedPortalUserWithPlane(t, d, "bob", "https://plane-b.example")
	seedPortalUserWithPlane(t, d, "carol", "https://plane-b.example")

	// Global plane (URL="") — must include alice, exclude
	// bob+carol.
	got, err := GenerateACLForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLForPlane(global): %v", err)
	}
	if !strings.Contains(got, "alice@tsnet.example.com") {
		t.Errorf("global plane should include alice, got: %q", got)
	}
	if strings.Contains(got, "bob@tsnet.example.com") {
		t.Errorf("global plane must NOT include bob (he's on plane B), got: %q", got)
	}
	if strings.Contains(got, "carol@tsnet.example.com") {
		t.Errorf("global plane must NOT include carol (she's on plane B), got: %q", got)
	}

	// Plane B — must include bob+carol, exclude alice.
	got, err = GenerateACLForPlane(d, "https://plane-b.example")
	if err != nil {
		t.Fatalf("GenerateACLForPlane(plane B): %v", err)
	}
	if !strings.Contains(got, "bob@tsnet.example.com") {
		t.Errorf("plane B should include bob, got: %q", got)
	}
	if !strings.Contains(got, "carol@tsnet.example.com") {
		t.Errorf("plane B should include carol, got: %q", got)
	}
	if strings.Contains(got, "alice@tsnet.example.com") {
		t.Errorf("plane B must NOT include alice (she's on the default plane), got: %q", got)
	}
}

// TestApplyACLPipelineForPlane_UsesCorrectClient pins v0.13.0:
// ApplyACLPipelineForPlane builds the policy scoped to one
// plane and pushes it to the plane's headscale client.
// tagOwners etc. must contain only that plane's identities.
func TestApplyACLPipelineForPlane_UsesCorrectClient(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	seedPortalUserWithPlane(t, d, "bob", "https://plane-b.example")
	hs, captured := fakeHeadscaleWithCapture(t, http.StatusOK)

	res := ApplyACLPipelineForPlane(d, hs, "", nil, "alice", "test", false)
	if !res.Applied {
		t.Fatalf("Applied = false; err = %v", res.Err)
	}
	// The captured SetPolicy body is the global-plane policy.
	// It should mention alice but NOT bob (bob is on plane B).
	if !strings.Contains(captured.config, "alice@tsnet.example.com") {
		t.Errorf("SetPolicy body should contain alice, got: %q", captured.config)
	}
	if strings.Contains(captured.config, "bob@tsnet.example.com") {
		t.Errorf("SetPolicy body must NOT contain bob (plane B), got: %q", captured.config)
	}
}

// TestListControlPlanesGroupsByURL pins v0.13.0: ListControlPlanes
// returns one row per distinct headscale_url (plus "" for
// the global default), with a user count.
func TestListControlPlanesGroupsByURL(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice") // default
	seedPortalUser(t, d, "alice2") // default
	seedPortalUserWithPlane(t, d, "bob", "https://plane-b.example")
	seedPortalUserWithPlane(t, d, "carol", "https://plane-c.example")

	planes, err := db.ListControlPlanes(d)
	if err != nil {
		t.Fatalf("ListControlPlanes: %v", err)
	}
	// Expect 3 distinct planes: default, plane-b, plane-c.
	counts := map[string]int{}
	for _, p := range planes {
		counts[p.URL] = p.UserCount
	}
	if counts[""] != 2 {
		t.Errorf("default plane count: want 2, got %d", counts[""])
	}
	if counts["https://plane-b.example"] != 1 {
		t.Errorf("plane-b count: want 1, got %d", counts["https://plane-b.example"])
	}
	if counts["https://plane-c.example"] != 1 {
		t.Errorf("plane-c count: want 1, got %d", counts["https://plane-c.example"])
	}
}

// TestSetACLForAllPlanes_PreBuiltPolicy pins v0.13.0: the
// ACL import flow. SetACLForAllPlanes pushes a pre-built
// policy (e.g. one loaded from a JSON file the operator
// uploaded) to every plane and writes an acl_snapshots
// row, without re-running GenerateACL.
func TestSetACLForAllPlanes_PreBuiltPolicy(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	hs, captured := fakeHeadscaleWithCapture(t, http.StatusOK)

	imported := `{"acls":[],"tagOwners":{},"groups":{},"ssh":[]}`
	results := SetACLForAllPlanes(d,
		func(planeURL string) *headscale.Client { return hs },
		nil, "alice", "imported test", imported,
	)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Applied {
		t.Fatalf("Applied = false; err = %v", results[0].Err)
	}
	// SetPolicy body must match the imported policy byte-for-byte.
	if captured.config != imported {
		t.Errorf("SetPolicy body mismatch:\n  want: %q\n  got:  %q", imported, captured.config)
	}
	// An acl_snapshots row must have been written.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots WHERE config = ?`, imported).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 acl_snapshots row with imported config, got %d", n)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}


// TestGenerateACL_PerDeviceTagInSrc: v0.28.0 — the per-device
// exit-rule uses tag:dev-<user>-<device> as src, NOT the
// device_ip. This is the core fix for "rules spread to other
// devices" — a rule for workstation-1 uses a tag that only
// workstation-1 carries, so workstation-3 (which has a different tag) is
// not in scope.
func TestGenerateACL_PerDeviceTagInSrc(t *testing.T) {
	d := openTestDB(t)
	adminID := seedPortalUser(t, d, "admin")
	_ = seedPortalUser(t, d, "user1")
	// Two devices on node_owner_map for admin
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, hostname, username, tag) VALUES ('1', 'workstation-1', 'admin', 'tag:private')`); err != nil {
		t.Fatalf("seed workstation-1: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, hostname, username, tag) VALUES ('2', 'workstation-3', 'admin', 'tag:private')`); err != nil {
		t.Fatalf("seed workstation-3: %v", err)
	}
	// One rule per device (same user, different device)
	if _, err := d.Exec(`INSERT INTO device_rules
		(user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain, user_name, device_hostname, enabled)
		VALUES (?, 1, 'relay-3', 'subnet', '91.108.4.0/22', 'accept', '100.64.100.1', '', 'admin', 'workstation-1', 1)`,
		adminID); err != nil {
		t.Fatalf("seed rule 1: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO device_rules
		(user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain, user_name, device_hostname, enabled)
		VALUES (?, 2, 'relay-3', 'subnet', '8.8.8.0/24', 'accept', '100.64.100.11', '', 'admin', 'workstation-3', 1)`,
		adminID); err != nil {
		t.Fatalf("seed rule 2: %v", err)
	}
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	// Verify the per-device rules use the per-device tag in src
	wantSkyworker := `"src": ["tag:dev-admin-workstation-1"]`
	wantMsi := `"src": ["tag:dev-admin-workstation-3"]`
	if !strings.Contains(aclStr, wantSkyworker) {
		t.Errorf("expected rule with src=tag:dev-admin-workstation-1 in ACL; not found.\nACL:\n%s", aclStr)
	}
	if !strings.Contains(aclStr, wantMsi) {
		t.Errorf("expected rule with src=tag:dev-admin-workstation-3 in ACL; not found.\nACL:\n%s", aclStr)
	}
	// And they have different dsts (Telegram vs Google)
	wantTelegram := `"dst": ["91.108.4.0/22:*"]`
	wantGoogle := `"dst": ["8.8.8.0/24:*"]`
	if !strings.Contains(aclStr, wantTelegram) {
		t.Errorf("expected 91.108.4.0/22 rule; not found")
	}
	if !strings.Contains(aclStr, wantGoogle) {
		t.Errorf("expected 8.8.8.0/24 rule; not found")
	}
}

// TestGenerateACL_PerDeviceTagInTagOwners: v0.28.0 — the
// tagOwners block must include one entry per user covering
// all their per-device tags. Without these entries, the
// headscale parser rejects the policy with "tag not found".
func TestGenerateACL_PerDeviceTagInTagOwners(t *testing.T) {
	d := openTestDB(t)
	_ = seedPortalUser(t, d, "admin")
	_ = seedPortalUser(t, d, "user1")
	// admin has 2 devices, user1 has 1
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, hostname, username, tag) VALUES ('1', 'workstation-1', 'admin', 'tag:private')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, hostname, username, tag) VALUES ('2', 'workstation-3', 'admin', 'tag:private')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, hostname, username, tag) VALUES ('3', 'workstation-5', 'user1', 'tag:private')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	// admin owns 2 per-device tags
	wantSky := `"tag:dev-admin-workstation-1": ["admin@tsnet.example.com"]`
	wantMsi := `"tag:dev-admin-workstation-3": ["admin@tsnet.example.com"]`
	wantMichail := `"tag:dev-user1-workstation-5": ["user1@tsnet.example.com"]`
	for _, w := range []string{wantSky, wantMsi, wantMichail} {
		if !strings.Contains(aclStr, w) {
			t.Errorf("expected tagOwners entry %q; not found.\nACL:\n%s", w, aclStr)
		}
	}
}

// TestGenerateACL_LegacyDeviceIpFallback: v0.28.0 — pre-v0.28.0
// rules (where user_name + device_hostname are empty) fall
// back to device_ip src. The rule is live immediately after
// the migration lands, then switches to tag-based src on the
// first /my/devices load that backfills the hostname.
func TestGenerateACL_LegacyDeviceIpFallback(t *testing.T) {
	d := openTestDB(t)
	adminID := seedPortalUser(t, d, "admin")
	// Rule with empty user_name + empty device_hostname
	// (pre-v0.28.0 rows). device_ip is set.
	if _, err := d.Exec(`INSERT INTO device_rules
		(user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain, user_name, device_hostname, enabled)
		VALUES (?, 1, 'relay-3', 'subnet', '149.154.160.0/20', 'accept', '100.64.100.1', '', '', '', 1)`,
		adminID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	// Should use device_ip as src, NOT "*" and NOT the tag
	wantLegacy := `"src": ["100.64.100.1"]`
	if !strings.Contains(aclStr, wantLegacy) {
		t.Errorf("expected legacy fallback to device_ip; not found.\nACL:\n%s", aclStr)
	}
	// And NOT a tag-based src
	wantNotTag := `"src": ["tag:dev-admin-`
	if strings.Contains(aclStr, wantNotTag) {
		t.Errorf("legacy rule should NOT use tag-based src; ACL:\n%s", aclStr)
	}
}

// TestGenerateACL_PerDeviceTagDoesNotCrossUsers: the
// critical security invariant — a rule for admin's
// device does NOT apply to user1's device. We verify this
// by checking that admin's device tag is owned by
// admin@tsnet, NOT by user1@tsnet, in tagOwners.
func TestGenerateACL_PerDeviceTagDoesNotCrossUsers(t *testing.T) {
	d := openTestDB(t)
	adminID := seedPortalUser(t, d, "admin")
	user1ID := seedPortalUser(t, d, "user1")
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, hostname, username, tag) VALUES ('1', 'workstation-1', 'admin', 'tag:private')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO device_rules
		(user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain, user_name, device_hostname, enabled)
		VALUES (?, 1, 'relay-3', 'subnet', '91.108.4.0/22', 'accept', '100.64.100.1', '', 'admin', 'workstation-1', 1)`,
		adminID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// user1 has no devices, no rules
	_ = user1ID
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	// The per-device tag should be owned by admin, NOT user1
	want := `"tag:dev-admin-workstation-1": ["admin@tsnet.example.com"]`
	wantWrong := `"tag:dev-admin-workstation-1": ["user1@tsnet.example.com"]`
	if !strings.Contains(aclStr, want) {
		t.Errorf("expected admin owns the per-device tag; not found.\nACL:\n%s", aclStr)
	}
	if strings.Contains(aclStr, wantWrong) {
		t.Errorf("user1 should NOT own admin's device tag; found.\nACL:\n%s", aclStr)
	}
}

// TestGenerateACLWithVia_OutputUsesGrants pins v0.28.1: the
// grants-with-via builder emits `grants[]` (the headscale
// 0.29.0-beta.4+ replacement for `acls[]`) instead of the
// legacy `acls[]` shape. This is the foundation: every
// downstream test in this file pins a different invariant
// of the grants shape.
func TestGenerateACLWithVia_OutputUsesGrants(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	if !strings.Contains(aclStr, `"grants":`) {
		t.Errorf("output should contain a 'grants' key.\nACL:\n%s", aclStr)
	}
	// We also explicitly do NOT want acls[] in the same
	// output — the via path is pure-grants (mixing acls
	// + grants in the same file is supported in
	// headscale 0.29+ but the per-grant `via` semantics
	// are cleaner without acls noise).
	if strings.Contains(aclStr, `"acls":`) {
		t.Errorf("grants-with-via output should NOT contain 'acls'.\nACL:\n%s", aclStr)
	}
	// Per-user grant must include the required `ip: ["*"]`
	// field — without it headscale's parser drops the
	// grant as "no traffic allowed".
	if !strings.Contains(aclStr, `"ip": ["*"]`) {
		t.Errorf("per-user grant must include 'ip: [\"*\"]'.\nACL:\n%s", aclStr)
	}
}

// TestGenerateACLWithVia_NoPreferencesWhenNoneSet pins the
// day-1 case: with no rows in user_exit_node_prefs, the
// per-user grants MUST NOT include a `via` field (the
// `via: ["<tag>"]` is what headscale uses to restrict the
// user's exit-node choice; without a preference the user
// can use any of the available exit-nodes, which is the
// legacy catch-all semantics).
func TestGenerateACLWithVia_NoPreferencesWhenNoneSet(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	if strings.Contains(aclStr, `"via":`) {
		t.Errorf("no per-user prefs set — output must NOT contain 'via'.\nACL:\n%s", aclStr)
	}
	// Catch-all grants in autogroup:internet must still be
	// present (so tag:public relay nodes can FORWARD exit-node
	// traffic to the internet). 2026-07-25: v0.28.3 — the
	// catch-all is now src=tag:public (not src=*), so only
	// relays match. End-user devices get autogroup:internet
	// via the per-user grant (which always includes it,
	// regardless of whether the user has a preference set).
	if !strings.Contains(aclStr, `"autogroup:internet"`) {
		t.Errorf("output must still contain the autogroup:internet reference.\nACL:\n%s", aclStr)
	}
	// v0.28.3: the catch-all src must be tag:public, not "*".
	// Without this, workstation-3 (or any other end-user device) could
	// piggyback on the catch-all to use any exit-node for
	// arbitrary internet destinations — the bypass the user
	// reported on 2026-07-25.
	if strings.Contains(aclStr, `"src": ["*"], "dst": ["autogroup:internet"]`) {
		t.Errorf("v0.28.3: autogroup:internet catch-all must be src=tag:public, not src=*.\nACL:\n%s", aclStr)
	}
}

// TestGenerateACLWithVia_UserPrefTriggersViaAndTagOwners
// pins the core v0.28.1 invariant: a row in
// user_exit_node_prefs causes the user's per-user grant
// to include `via: ["<tag>"]`, AND the tagOwners block
// registers that tag with admin as owner (so headscale's
// parser accepts the policy).
func TestGenerateACLWithVia_UserPrefTriggersViaAndTagOwners(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	seedUserExitNodePref(t, d, aliceID, "tag:exit-relay-1")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// Per-user grant must include via: ["tag:exit-relay-1"]
	want := `"via": ["tag:exit-relay-1"]`
	if !strings.Contains(aclStr, want) {
		t.Errorf("per-user grant should include %s.\nACL:\n%s", want, aclStr)
	}
	// tagOwners must include the per-exit-node tag with
	// admin as the owner.
	wantOwner := `"tag:exit-relay-1": ["admin@tsnet.example.com"]`
	if !strings.Contains(aclStr, wantOwner) {
		t.Errorf("tagOwners should include %s.\nACL:\n%s", wantOwner, aclStr)
	}
}

// TestGenerateACLWithVia_PerExitNodeTagOwnersAreDistinct
// pins the de-dup invariant: if two users share the same
// preferred exit-node, the tagOwners block emits ONE
// entry (not two). This is the same invariant the v0.28.0
// per-device tagOwners test pins, applied to the
// per-exit-node layer.
func TestGenerateACLWithVia_PerExitNodeTagOwnersAreDistinct(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	bobID := seedPortalUser(t, d, "bob")
	seedUserExitNodePref(t, d, aliceID, "tag:exit-relay-1")
	seedUserExitNodePref(t, d, bobID, "tag:exit-relay-1")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// Count occurrences of the tagOwners entry.
	needle := `"tag:exit-relay-1": ["admin@tsnet.example.com"]`
	count := strings.Count(aclStr, needle)
	if count != 1 {
		t.Errorf("expected exactly 1 tagOwners entry for tag:exit-relay-1, got %d.\nACL:\n%s",
			count, aclStr)
	}
	// Both per-user grants must still have via (one
	// entry each — the user-level via is per-user, the
	// tagOwners entry is de-duped at the tag level).
	viaCount := strings.Count(aclStr, `"via": ["tag:exit-relay-1"]`)
	if viaCount != 2 {
		t.Errorf("expected 2 'via: [tag:exit-relay-1]' entries (one per user), got %d.\nACL:\n%s",
			viaCount, aclStr)
	}
}

// 2026-07-25: v0.28.2 — headscale 0.29.2's grants
// parser doesn't accept CIDR:port in dst. The
// workaround is to emit each CIDR as a host alias in
// the `hosts:` block and reference the alias in the
// grant. The following 3 tests pin that invariant.

// TestGenerateACLWithVia_EmitsHostsBlock pins the v0.28.2
// invariant: the output starts with a `hosts:` block
// BEFORE the `grants:` block, with one entry per
// per-user subnet (using the canonical
// "h-user-<uname>-subnet" alias name). Without
// per-user subnets, the block still has the
// `_placeholder` entry so headscale's JSON parser
// doesn't reject the file.
func TestGenerateACLWithVia_EmitsHostsBlock(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	seedUserSubnetWithCIDR(t, d, aliceID, "10.0.1.0/24")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// Output must start with `"hosts":` (not `"grants":`).
	// The hosts block is the FIRST key in the policy
	// object — headscale's v2 parser is strict about
	// shape but accepts any key order; the source-code
	// order matches the docs example.
	if !strings.Contains(aclStr, `"hosts":`) {
		t.Errorf("output must contain 'hosts:' key.\nACL:\n%s", aclStr)
	}
	// The personal subnet for alice must appear as a
	// host alias — this is the only way headscale's
	// grants parser can resolve "h-user-alice-subnet:*"
	// in the grant's dst to 10.0.1.0/24.
	wantHost := `"h-user-alice-subnet": "10.0.1.0/24"`
	if !strings.Contains(aclStr, wantHost) {
		t.Errorf("hosts block must contain %s.\nACL:\n%s", wantHost, aclStr)
	}
}

// TestGenerateACLWithVia_GrantsReferenceHostAliases pins
// the v0.28.2 invariant: per-user grant's dst
// references the host alias, NOT the raw CIDR+port
// (which headscale 0.29.2 rejects). The "h:user-<uname>-
// subnet:*" form is what the grants parser accepts.
func TestGenerateACLWithVia_GrantsReferenceHostAliases(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	seedUserSubnetWithCIDR(t, d, aliceID, "10.0.1.0/24")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// Per-user grant's dst must include the host alias.
	// We use the bare alias (no :*) because headscale's
	// parseAlias does NOT split alias:port. The `ip: ["*"]`
	// in the same grant already means "any port".
	wantAlias := `"h-user-alice-subnet"`
	if !strings.Contains(aclStr, wantAlias) {
		t.Errorf("per-user grant must reference %s.\nACL:\n%s", wantAlias, aclStr)
	}
	// The raw CIDR+port form must NOT appear as a dst
	// (otherwise headscale 0.29.2 rejects with
	// "invalid alias format"). The CIDR may still
	// appear in the `hosts:` block — that's the WHOLE
	// POINT of the workaround — but never as
	// "10.0.1.0/24:*" directly in a grant's dst.
	banned := `"dst": ["10.0.1.0/24:*"]`
	if strings.Contains(aclStr, banned) {
		t.Errorf("raw CIDR+port in grant dst is forbidden on headscale 0.29.2.\nACL:\n%s", aclStr)
	}
}

// TestGenerateACLWithVia_HostsBlockIsRequiredEvenWhenEmpty
// pins the edge case: with no per-user subnets and no
// shared CIDRs, the `hosts:` block still has the
// `_placeholder` entry. Without it, the resulting JSON
// is `{ "grants": [...] }` with no hosts key — headscale
// MAY accept that, but the v2 parser tests show
// inconsistent behavior across patch versions. The
// placeholder keeps us safe.
func TestGenerateACLWithVia_HostsBlockIsRequiredEvenWhenEmpty(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	// No user_subnets, no shared subnets, no mesh.
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// `hosts:` key MUST be present even when empty.
	if !strings.Contains(aclStr, `"hosts":`) {
		t.Errorf("output must contain 'hosts:' key (with placeholder) even when no per-user subnets exist.\nACL:\n%s", aclStr)
	}
	// The placeholder is the only entry.
	if !strings.Contains(aclStr, `"_placeholder": "0.0.0.0/32"`) {
		t.Errorf("empty hosts block must contain the _placeholder entry.\nACL:\n%s", aclStr)
	}
}

// 2026-07-25: v0.28.3 — exit-node bypass fix. The
// catch-all `* → autogroup:internet:*` (in acls[]) and
// `* → autogroup:internet` (in grants[]) was the bypass
// that let workstation-3 (tag:dev-admin-workstation-3 → admin@...) and
// any other end-user device reach relay-3's 148
// PrimaryRoutes through the exit-node path. The fix has
// two parts:
//
//   1. The per-user grant now includes "autogroup:internet"
//      in its dst list. Combined with the via=[] field
//      (which may be empty for users without a
//      preference), this gives every user internet
//      egress as themselves (a per-user grant always
//      matches the user's own packets because every
//      portal user's device resolves to their
//      <username>@tsnet.example.com identity via the
//      tagOwners block).
//
//   2. The catch-all's src is changed from "*" to
//      "tag:public" — only relay nodes can use
//      autogroup:internet themselves (i.e. FORWARD
//      exit-node traffic to the internet). End-user
//      devices no longer match this catch-all.
//
// The two-part design keeps the exit-node function
// working (relays still forward) while closing the
// bypass (end-user devices can't piggyback on the
// catch-all to pick whichever exit-node they want).
// The following 3 tests pin each part of the fix.

// TestGenerateACLWithVia_PerUserGrantHasAutogroupInternet
// pins the v0.28.3 invariant that the per-user grant's
// dst list ALWAYS includes "autogroup:internet" (as
// the LAST entry, after the user's own identity and
// subnet). The grant already has `ip: ["*"]` so the
// autogroup:internet dst resolves to "any IP in
// autogroup:internet, any port".
func TestGenerateACLWithVia_PerUserGrantHasAutogroupInternet(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// Find alice's per-user grant (the one whose src is
	// alice@tsnet.example.com) and verify its dst contains
	// "autogroup:internet".
	wantNeedle := `"src": ["alice@tsnet.example.com"], "dst": [` +
		`"alice@tsnet.example.com:*"` +
		// 2026-07-25: v0.28.3 — autogroup:internet
		// appended at the end.
		`, "autogroup:internet"` +
		`]`
	if !strings.Contains(aclStr, wantNeedle) {
		t.Errorf("per-user grant for alice must include autogroup:internet in dst.\nACL:\n%s", aclStr)
	}
}

// TestGenerateACLWithVia_CatchAllIsTagPublicNotStar pins
// the v0.28.3 invariant that the autogroup:internet
// catch-all has src=tag:public, NOT src=*. This is the
// fix for the bypass: with src=*, any device could use
// any exit-node for arbitrary internet destinations;
// with src=tag:public, only relay nodes (relay-1,
// relay-2, relay-3, etc.) can use autogroup:internet
// themselves.
func TestGenerateACLWithVia_CatchAllIsTagPublicNotStar(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// The catch-all must be src=tag:public, dst=autogroup:internet.
	// The exact needle we want:
	want := `"src": ["tag:public"], "dst": ["autogroup:internet"]`
	if !strings.Contains(aclStr, want) {
		t.Errorf("autogroup:internet catch-all must be src=tag:public.\nACL:\n%s", aclStr)
	}
	// The OLD bypass shape MUST NOT be present:
	// src=* dst=autogroup:internet. The user reported this
	// as the bypass: workstation-3 (a admin device) used relay-3
	// for internet destinations it shouldn't have been able
	// to reach.
	banned := `"src": ["*"], "dst": ["autogroup:internet"]`
	if strings.Contains(aclStr, banned) {
		t.Errorf("v0.28.3: src=* dst=autogroup:internet is the bypass — must be src=tag:public.\nACL:\n%s", aclStr)
	}
}

// TestGenerateACL_LegacyPerUserGrantHasAutogroupInternet
// pins the v0.28.3 invariant for the LEGACY acls[]
// path (used when SKYGATE_ACL_VIA_ENABLED=false).
// Same fix as the grants[] path: per-user rule
// includes autogroup:internet:* in dst, catch-all
// is src=tag:public. Legacy mode doesn't have via,
// so devices without per-user-rule coverage fall
// through to the catch-all (which is now restricted
// to tag:public — they don't match).
func TestGenerateACL_LegacyPerUserGrantHasAutogroupInternet(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	// Per-user rule for alice must include autogroup:internet:*.
	// In the legacy acls[] path the dst format keeps the :*
	// suffix (parseAlias accepts "*:*" for autogroup
	// because the autogroup is a reserved alias and the
	// port is part of the standard acls[] shape).
	wantNeedle := `"dst": ["alice@tsnet.example.com:*", "autogroup:internet:*"]`
	if !strings.Contains(aclStr, wantNeedle) {
		t.Errorf("legacy per-user rule for alice must include autogroup:internet:* in dst.\nACL:\n%s", aclStr)
	}
	// The OLD bypass shape MUST NOT be present:
	// src=* dst=autogroup:internet:*. (Same fix as the
	// grants[] path.)
	banned := `"src": ["*"], "dst": ["autogroup:internet:*"]`
	if strings.Contains(aclStr, banned) {
		t.Errorf("v0.28.3 legacy: src=* dst=autogroup:internet:* is the bypass — must be src=tag:public.\nACL:\n%s", aclStr)
	}
	// The fix: src=tag:public, dst=autogroup:internet:*.
	want := `"src": ["tag:public"], "dst": ["autogroup:internet:*"]`
	if !strings.Contains(aclStr, want) {
		t.Errorf("legacy autogroup:internet catch-all must be src=tag:public.\nACL:\n%s", aclStr)
	}
}

// 2026-07-25: v0.28.4 — per-device preferred exit-node.
// The ACL builder reads device_exit_node_prefs and emits
// a per-device grant BEFORE the per-user grant (Tailscale
// first-match). The per-device grant has src=tag:dev-<user>-
// <device> and via=[<device-pref>], so a device with a
// per-device override uses its own exit-node even when the
// user has a different default.
//
// The 3 tests below pin each layer of the v0.28.4 design.

// TestGenerateACLWithVia_PerDeviceGrantEmittedBeforePerUser
// pins the v0.28.4 ordering invariant: a per-device grant
// (when present) appears in the grants[] list BEFORE the
// per-user grant. Tailscale's first-match semantics make
// the per-device via override the per-user via for the
// matching device.
//
// Test setup:
//   * admin (id=1) has per-user pref → relay-1
//   * workstation-3 (admin's device) has per-device pref → relay-3
//   * The per-device grant for workstation-3 must be the FIRST
//     entry in grants[] (or at least, before the per-user
//     grant for admin@tsnet).
func TestGenerateACLWithVia_PerDeviceGrantEmittedBeforePerUser(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	seedUserExitNodePref(t, d, aliceID, "tag:exit-relay-1")
	seedDeviceExitNodePref(t, d, aliceID, "workstation-3", "tag:exit-relay-3")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// Per-device grant for workstation-3 must reference the device
	// tag AND the per-device via (not the per-user via).
	wantPerDevice := `{ "src": ["tag:dev-alice-workstation-3"], "dst": ["autogroup:internet"], "ip": ["*"], "via": ["tag:exit-relay-3"] }`
	if !strings.Contains(aclStr, wantPerDevice) {
		t.Errorf("per-device grant for workstation-3 missing or wrong.\nACL:\n%s", aclStr)
	}
	// The per-device grant must appear BEFORE the
	// per-user grant for alice@tsnet (Tailscale
	// first-match ordering). Find the position of each
	// grant and compare.
	perDevicePos := strings.Index(aclStr, wantPerDevice)
	perUserMarker := `"src": ["alice@tsnet.example.com"]`
	perUserPos := strings.Index(aclStr, perUserMarker)
	if perDevicePos < 0 || perUserPos < 0 {
		t.Fatalf("could not find both grants in ACL.\nACL:\n%s", aclStr)
	}
	if perDevicePos >= perUserPos {
		t.Errorf("per-device grant must be emitted BEFORE the per-user grant (Tailscale first-match).\n"+
			"  per-device at byte %d, per-user at byte %d\nACL:\n%s",
			perDevicePos, perUserPos, aclStr)
	}
	// The per-user grant must still have via=tag:exit-relay-1
	// (alice's per-user pref). The per-device override
	// doesn't change the per-user grant — it adds a
	// separate, more-specific rule.
	wantPerUser := `"via": ["tag:exit-relay-1"]`
	if !strings.Contains(aclStr, wantPerUser) {
		t.Errorf("per-user grant for alice must still have via=tag:exit-relay-1.\nACL:\n%s", aclStr)
	}
}

// TestGenerateACLWithVia_PerDeviceGrantOnlyCoversAutogroupInternet
// pins the v0.28.4 invariant that the per-device grant
// ONLY covers autogroup:internet. The user's own stuff
// (own devices, own subnet) is covered by the per-user
// grant below — duplicating those in the per-device grant
// would bloat the policy without adding reach (the
// per-user grant already covers them).
//
// Why this matters: the per-device grant is the
// via-OVERRIDE. It exists so the device's exit-node
// choice is independent of the user's choice. For
// everything else (own stuff, shared/mesh), the
// per-user grant's dst is the source of truth.
func TestGenerateACLWithVia_PerDeviceGrantOnlyCoversAutogroupInternet(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	seedUserExitNodePref(t, d, aliceID, "tag:exit-relay-1")
	seedDeviceExitNodePref(t, d, aliceID, "workstation-3", "tag:exit-relay-3")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// The per-device grant (substring) must contain
	// ONLY "autogroup:internet" in its dst list — not
	// the user's own identity, own subnet, etc.
	wantPerDevice := `{ "src": ["tag:dev-alice-workstation-3"], "dst": ["autogroup:internet"], "ip": ["*"], "via": ["tag:exit-relay-3"] }`
	// Find the per-device grant and check its dst list.
	startIdx := strings.Index(aclStr, wantPerDevice)
	if startIdx < 0 {
		t.Fatalf("per-device grant not found.\nACL:\n%s", aclStr)
	}
	// The per-device grant is a single line ending with `}`.
	// Extract the dst portion of the grant.
	endIdx := strings.Index(aclStr[startIdx:], " }")
	if endIdx < 0 {
		t.Fatalf("per-device grant not terminated.\nACL:\n%s", aclStr)
	}
	grantStr := aclStr[startIdx : startIdx+endIdx]
	// dst must be exactly ["autogroup:internet"] — nothing else.
	if !strings.Contains(grantStr, `"dst": ["autogroup:internet"]`) {
		t.Errorf("per-device grant dst must be exactly [\"autogroup:internet\"].\ngrant: %s", grantStr)
	}
	if strings.Contains(grantStr, `"dst": ["autogroup:internet",`) ||
		strings.Contains(grantStr, `"dst": [ "autogroup:internet",`) {
		t.Errorf("per-device grant dst must be ONLY autogroup:internet (no own-identity, own-subnet, etc).\ngrant: %s", grantStr)
	}
}

// TestGenerateACLWithVia_NoPerDeviceGrantWhenNoPrefsSet pins
// the v0.28.4 invariant that with no rows in
// device_exit_node_prefs, the per-device grant block is
// empty (and the per-user grants look exactly like
// v0.28.3). This guards against the v0.28.4 refactor
// accidentally injecting per-device grants for every
// device.
func TestGenerateACLWithVia_NoPerDeviceGrantWhenNoPrefsSet(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// No per-device grant means no "tag:dev-alice-..."
	// src. The string `"src": ["tag:dev-` must NOT appear.
	if strings.Contains(aclStr, `"src": ["tag:dev-`) {
		t.Errorf("no per-device prefs set — per-device grants MUST NOT appear.\nACL:\n%s", aclStr)
	}
	// The v0.28.3 catch-all (tag:public → autogroup:internet)
	// must still be present.
	if !strings.Contains(aclStr, `"src": ["tag:public"], "dst": ["autogroup:internet"]`) {
		t.Errorf("v0.28.3 catch-all missing.\nACL:\n%s", aclStr)
	}
}
