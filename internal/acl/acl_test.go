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
	enabled INTEGER DEFAULT 1
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
CREATE TABLE user_subnet_shares (
	grantor_user_id INTEGER NOT NULL,
	grantee_user_id INTEGER NOT NULL,
	created_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (grantor_user_id, grantee_user_id),
	FOREIGN KEY (grantor_user_id) REFERENCES portal_users(id) ON DELETE CASCADE,
	FOREIGN KEY (grantee_user_id) REFERENCES portal_users(id) ON DELETE CASCADE
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
		`"dst": ["alice@tsnet.skynas.ru:*"]`,
		`"dst": ["bob@tsnet.skynas.ru:*"]`,
		`"dst": ["tag:public:*"]`,
		`"dst": ["tag:exit-node:*"]`,
		// 2026-07-15: v0.12.0.2 — internet egress via
		// autogroup:internet (NOT a literal "*:*" catch-all,
		// which would re-introduce the inter-user leak).
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
		`"src": ["tag:private", "skyadmin@tsnet.skynas.ru"],` + "\n" + `      "dst": ["tag:exit-node"]`,
		`"src": ["skyadmin@tsnet.skynas.ru"],` + "\n" + `      "dst": ["tag:public"]`,
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
//     excludes the tailnet's 100.64.0.0/10 range).
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
	if !strings.Contains(lastRule, "autogroup:internet:*") {
		t.Fatalf("last rule in acls[] does not reference autogroup:internet: %s", lastRule)
	}
}

// TestGenerateACL_PerUserSubnetCIDR — v0.17.0. Users
// with an allocated personal subnet get an extended
// per-user rule:
//
//   { "action": "accept",
//     "src":    ["alice@tsnet.skynas.ru"],
//     "dst":    ["alice@tsnet.skynas.ru:*",
//                "10.0.<uid>.0/24:*"] }
//
// Users WITHOUT a subnet keep the original
// `dst: ["alice@tsnet.skynas.ru:*"]` (no CIDR
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
	wantAlice := fmt.Sprintf(
		`"src": ["alice@tsnet.skynas.ru"], "dst": ["alice@tsnet.skynas.ru:*", "%s:*"]`,
		aliceCIDR)
	if !strings.Contains(aclStr, wantAlice) {
		t.Errorf("alice's per-user rule should include her CIDR; expected %q in ACL, got excerpt: %q",
			wantAlice, aclStr[max(0, len(aclStr)-1500):])
	}
	// bob's per-user rule should NOT include any CIDR
	// (he has no subnet allocated). The grep for
	// "bob@tsnet.skynas.ru:*" should appear but NOT
	// as a multi-CIDR dst (his dst has exactly one entry).
	wantBob := `"src": ["bob@tsnet.skynas.ru"], "dst": ["bob@tsnet.skynas.ru:*"]`
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
//     "src":    ["bob@tsnet.skynas.ru"],
//     "dst":    ["bob@tsnet.skynas.ru:*",
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
	// CIDR and alice's CIDR.
	wantBob := fmt.Sprintf(
		`"src": ["bob@tsnet.skynas.ru"], "dst": ["bob@tsnet.skynas.ru:*", "%s:*", "%s:*"]`,
		bobCIDR, aliceCIDR)
	if !strings.Contains(aclStr, wantBob) {
		t.Errorf("bob's per-user rule should include shared alice CIDR; expected %q in ACL, got excerpt: %q",
			wantBob, extractExcerptFromString(aclStr, "bob@tsnet"))
	}
	// alice's per-user rule should still only have
	// alice's own CIDR (the share is one-directional;
	// alice didn't grant herself access to bob's
	// subnet).
	wantAlice := fmt.Sprintf(
		`"src": ["alice@tsnet.skynas.ru"], "dst": ["alice@tsnet.skynas.ru:*", "%s:*"]`,
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
	aliceRuleStart := strings.Index(aclStr, `"src": ["alice@tsnet.skynas.ru"]`)
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
	bobRuleStart := strings.Index(aclStr, `"src": ["bob@tsnet.skynas.ru"]`)
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
// routing (emilia, sharlotta, karolina) for users who
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
	// identity, not just skyadmin).
	wantExit := `"src": ["*"], "dst": ["tag:exit-node:*"]`
	if !strings.Contains(aclStr, wantExit) {
		t.Errorf("exit-node mesh rule must be `src: [*] → tag:exit-node:*`; expected %q in ACL, got excerpt: %q",
			wantExit, aclStr[max(0, len(aclStr)-1500):])
	}
	// Also: tag:public mesh rule (relay nodes) must be
	// similarly global. Operators configure Caddy,
	// DERP, etc. on emilia/sharlotta/karolina.
	wantPublic := `"src": ["*"], "dst": ["tag:public:*"]`
	if !strings.Contains(aclStr, wantPublic) {
		t.Errorf("tag:public mesh rule must be `src: [*] → tag:public:*`; expected %q in ACL, got excerpt: %q",
			wantPublic, aclStr[max(0, len(aclStr)-1500):])
	}
}

func TestGenerateACLIncludesDeviceRules(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice")
	_, _ = d.Exec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip) VALUES (?, 42, 'emilia', 'ip', '1.2.3.4', 'accept', '100.64.0.5')`, uid)

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	if !strings.Contains(aclStr, "1.2.3.4:*") {
		t.Error("expected 1.2.3.4 entry in ACL")
	}
	if !strings.Contains(aclStr, "100.64.0.5") {
		t.Error("expected device_ip 100.64.0.5 as src in ACL")
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

	res := ApplyACLPipeline(d, hs, rec, "alice", "user alice added rule test")
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

	res := ApplyACLPipeline(d, hs, rec, "alice", "user alice added rule test")
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

	res := ApplyACLPipeline(d, hs, nil, "alice", "test")
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

	res := ApplyACLPipeline(d, hs, nil, "alice", "test")
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
	if !strings.Contains(got, "alice@tsnet.skynas.ru") {
		t.Errorf("global plane should include alice, got: %q", got)
	}
	if strings.Contains(got, "bob@tsnet.skynas.ru") {
		t.Errorf("global plane must NOT include bob (he's on plane B), got: %q", got)
	}
	if strings.Contains(got, "carol@tsnet.skynas.ru") {
		t.Errorf("global plane must NOT include carol (she's on plane B), got: %q", got)
	}

	// Plane B — must include bob+carol, exclude alice.
	got, err = GenerateACLForPlane(d, "https://plane-b.example")
	if err != nil {
		t.Fatalf("GenerateACLForPlane(plane B): %v", err)
	}
	if !strings.Contains(got, "bob@tsnet.skynas.ru") {
		t.Errorf("plane B should include bob, got: %q", got)
	}
	if !strings.Contains(got, "carol@tsnet.skynas.ru") {
		t.Errorf("plane B should include carol, got: %q", got)
	}
	if strings.Contains(got, "alice@tsnet.skynas.ru") {
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

	res := ApplyACLPipelineForPlane(d, hs, "", nil, "alice", "test")
	if !res.Applied {
		t.Fatalf("Applied = false; err = %v", res.Err)
	}
	// The captured SetPolicy body is the global-plane policy.
	// It should mention alice but NOT bob (bob is on plane B).
	if !strings.Contains(captured.config, "alice@tsnet.skynas.ru") {
		t.Errorf("SetPolicy body should contain alice, got: %q", captured.config)
	}
	if strings.Contains(captured.config, "bob@tsnet.skynas.ru") {
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
