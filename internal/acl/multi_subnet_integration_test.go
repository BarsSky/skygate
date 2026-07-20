// 2026-07-20: v0.22.0 Phase 1 — integration tests for the multi-subnet
// ACL builder.
//
// The user's three explicit concerns for v0.22.0:
//
//   (1) "Переход на новую подсеть для skyadmin не сломает работу
//       маршрутов?" — skyadmin already has 10.0.1.0/24 (the
//       v0.16.0 auto-allocate on operator's skyadmin row); we
//       verify the per-user rule extends correctly and no
//       other user's rule is affected.
//
//   (2) "Exit node в одной подсети а остальные устройства в
//       разных будут ли работать текущие правила для всех
//       устройств" — the `* → tag:exit-node:*` rule is global,
//       so an exit-node tagged on user A's sidecar is reachable
//       from users B/C/D regardless of which subnets they
//       own. We verify the rule is present + that per-user
//       dst lists don't accidentally narrow it.
//
//   (3) "Добавить тест проверки что подсети между собой могут
//       общаться и иметь связь с exit node" — cross-subnet
//       communication requires an explicit `user_subnet_shares`
//       row (one-directional grantor→grantee). We test the
//       end-to-end invite→consume→bridge→ACL flow, plus the
//       multi-grantor + bidirectional cases that the v0.22.0
//       mesh feature will need.
//
// The mesh feature (v0.22.0 Phase 2) extends the per-user
// dst list with the CIDRs of all OTHER members of every
// mesh the user belongs to. These tests pin the
// preconditions:
//
//   - the per-user dst list is correctly extended with own
//     CIDR (already there since v0.17.0)
//   - cross-user sharing extends the dst list (v0.17.1)
//   - multiple grantors to the same grantee all extend
//     the dst list (currently working but never explicitly
//     tested)
//   - bidirectional sharing is symmetric (currently
//     requires TWO grant() calls; the mesh feature will
//     collapse this into one operation)
//
// If any of these tests fail, the mesh feature MUST be
// deferred until the design is fixed — that's the user's
// explicit gate.

package acl

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"skygate/internal/invite"
)

// inviteCodesSchema is the v0.21.0 schema we need for the
// invite-flow tests. The acl package's minimalSchema doesn't
// include it (the package doesn't care about invites in
// normal usage), so we add it explicitly via a helper
// migration.
const inviteCodesSchema = `
CREATE TABLE invite_codes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	code TEXT NOT NULL UNIQUE,
	grantor_user_id INTEGER NOT NULL,
	grantee_username TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'active',
	created_at INTEGER NOT NULL DEFAULT 0,
	expires_at INTEGER NOT NULL DEFAULT 0,
	consumed_at INTEGER NOT NULL DEFAULT 0,
	consumed_by_user_id INTEGER NOT NULL DEFAULT 0,
	audit_message TEXT NOT NULL DEFAULT '',
	FOREIGN KEY (grantor_user_id) REFERENCES portal_users(id) ON DELETE CASCADE
);
`

// openTestDBWithInvites extends openTestDB with the
// invite_codes table so we can exercise the end-to-end
// invite→consume→bridge flow.
func openTestDBWithInvites(t *testing.T) *sql.DB {
	t.Helper()
	d := openTestDB(t)
	if _, err := d.Exec(inviteCodesSchema); err != nil {
		t.Fatalf("schema invite_codes: %v", err)
	}
	return d
}

// ruleFor extracts the JSON rule whose src contains the
// given username, returning a map for easy inspection.
// Returns nil if not found.
func ruleFor(t *testing.T, aclStr, username string) map[string]any {
	t.Helper()
	var doc struct {
		Acls []map[string]any `json:"acls"`
	}
	if err := json.Unmarshal([]byte(aclStr), &doc); err != nil {
		t.Fatalf("parse ACL: %v", err)
	}
	for _, r := range doc.Acls {
		srcs, ok := r["src"].([]any)
		if !ok {
			continue
		}
		for _, s := range srcs {
			if s == username+"@tsnet.skynas.ru" {
				return r
			}
		}
	}
	return nil
}

// dstList returns the dst array of a rule as a slice of
// strings, with each entry cleaned of the trailing ":*"
// suffix. Used by the multi-CIDR assertions below.
func dstList(t *testing.T, rule map[string]any) []string {
	t.Helper()
	raw, ok := rule["dst"].([]any)
	if !ok {
		t.Fatalf("rule has no dst array: %v", rule)
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			continue
		}
		// Strip ":*" suffix for the assertion.
		s = strings.TrimSuffix(s, ":*")
		out = append(out, s)
	}
	return out
}

// containsCIDR returns true if the dst list contains
// the given CIDR (without the ":*" suffix).
func containsCIDR(dst []string, cidr string) bool {
	for _, d := range dst {
		if d == cidr {
			return true
		}
	}
	return false
}

// TestACLBuilder_MultiUserSubnets_PinIsolation pins the
// v0.17.0 design: each user with a personal subnet has
// their OWN CIDR in dst, and no other user's CIDR leaks
// into their rule. With 3 users on the same plane:
//
//   alice (uid=N) → dst = ["alice@*:N", "10.0.N.0/24:*"]
//   bob   (uid=M) → dst = ["bob@*:M",   "10.0.M.0/24:*"]
//   carol (uid=P) → dst = ["carol@*:P", "10.0.P.0/24:*"]
//
// No cross-contamination. The first-match semantics of
// the per-user rule means alice can reach 10.0.N.0/24 but
// not 10.0.M.0/24 or 10.0.P.0/24 (her dst doesn't list
// them; the catch-all tag:exit-node and autogroup:internet
// rules don't grant access to other users' CIDRs).
func TestACLBuilder_MultiUserSubnets_PinIsolation(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	bobID := seedPortalUser(t, d, "bob")
	carolID := seedPortalUser(t, d, "carol")
	aliceCIDR := fmt.Sprintf("10.0.%d.0/24", aliceID)
	bobCIDR := fmt.Sprintf("10.0.%d.0/24", bobID)
	carolCIDR := fmt.Sprintf("10.0.%d.0/24", carolID)
	for _, p := range []struct {
		uid  int64
		cidr string
	}{
		{aliceID, aliceCIDR},
		{bobID, bobCIDR},
		{carolID, carolCIDR},
	} {
		if _, err := d.Exec(`INSERT INTO user_subnets
			(user_id, cidr, status, control_plane_url)
			VALUES (?, ?, 'active', '')`, p.uid, p.cidr); err != nil {
			t.Fatalf("seed subnet uid=%d: %v", p.uid, err)
		}
	}

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}

	// Each user's rule should contain only their OWN CIDR.
	for _, tc := range []struct {
		uname string
		cidr  string
		other []string
	}{
		{"alice", aliceCIDR, []string{bobCIDR, carolCIDR}},
		{"bob", bobCIDR, []string{aliceCIDR, carolCIDR}},
		{"carol", carolCIDR, []string{aliceCIDR, bobCIDR}},
	} {
		rule := ruleFor(t, aclStr, tc.uname)
		if rule == nil {
			t.Fatalf("%s's per-user rule not found in ACL", tc.uname)
		}
		dst := dstList(t, rule)
		// Own identity + own CIDR (and nothing else from
		// the per-user CIDR set).
		if !containsCIDR(dst, tc.uname+"@tsnet.skynas.ru") {
			t.Errorf("%s's dst missing own identity; dst=%v", tc.uname, dst)
		}
		if !containsCIDR(dst, tc.cidr) {
			t.Errorf("%s's dst missing own CIDR %s; dst=%v", tc.uname, tc.cidr, dst)
		}
		// No other user's CIDR leaks in.
		for _, otherCIDR := range tc.other {
			if containsCIDR(dst, otherCIDR) {
				t.Errorf("%s's dst leaks other CIDR %s; dst=%v", tc.uname, otherCIDR, dst)
			}
		}
	}
}

// TestACLBuilder_ExitNodeGlobalAcrossSubnets pins the
// v0.17.0 design decision: exit nodes remain reachable
// from every user, regardless of which subnet the exit
// node lives in. The rule is structural: src is "*" (any
// identity), not per-user.
//
// This is the user's explicit question (2): "Exit node в
// одной подсети а остальные устройства в разных будут ли
// работать текущие правила для всех устройств".
//
// We assert: with 3 users (alice, bob, carol) each in
// their own subnet, the ACL still contains
// `{src: ["*"], dst: ["tag:exit-node:*"]}` as a global
// rule. The Tailscale-side enforcement of which user can
// pick which exit node is the client's own exit-node
// menu (Tailscale native), NOT the ACL — the ACL just
// says "the tag:exit-node tag is reachable from anyone".
func TestACLBuilder_ExitNodeGlobalAcrossSubnets(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	bobID := seedPortalUser(t, d, "bob")
	carolID := seedPortalUser(t, d, "carol")
	for _, p := range []struct {
		uid  int64
		cidr string
	}{
		{aliceID, fmt.Sprintf("10.0.%d.0/24", aliceID)},
		{bobID, fmt.Sprintf("10.0.%d.0/24", bobID)},
		{carolID, fmt.Sprintf("10.0.%d.0/24", carolID)},
	} {
		if _, err := d.Exec(`INSERT INTO user_subnets
			(user_id, cidr, status, control_plane_url)
			VALUES (?, ?, 'active', '')`, p.uid, p.cidr); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}

	// The exit-node rule MUST have src=["*"] and
	// dst=["tag:exit-node:*"]. The previous test
	// (TestGenerateACL_ExitNodeMeshStillGlobal) covered
	// the no-subnet case; this one is the multi-subnet
	// version.
	var doc struct {
		Acls []map[string]any `json:"acls"`
	}
	if err := json.Unmarshal([]byte(aclStr), &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, r := range doc.Acls {
		srcs, _ := r["src"].([]any)
		dsts, _ := r["dst"].([]any)
		if len(srcs) != 1 || srcs[0] != "*" {
			continue
		}
		if len(dsts) == 1 && dsts[0] == "tag:exit-node:*" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ACL missing global `* → tag:exit-node:*` rule; ACL:\n%s", aclStr)
	}
	// Same for tag:public (the relay nodes).
	foundPublic := false
	for _, r := range doc.Acls {
		srcs, _ := r["src"].([]any)
		dsts, _ := r["dst"].([]any)
		if len(srcs) != 1 || srcs[0] != "*" {
			continue
		}
		if len(dsts) == 1 && dsts[0] == "tag:public:*" {
			foundPublic = true
			break
		}
	}
	if !foundPublic {
		t.Fatalf("ACL missing global `* → tag:public:*` rule; ACL:\n%s", aclStr)
	}
}

// TestACLBuilder_SkyadminMigrationIsolated pins the
// user's explicit question (1): "Переход на новую
// подсеть для skyadmin не сломает работу маршрутов?"
//
// The live state on the VM as of v0.21.1: skyadmin has
// 10.0.1.0/24 (uid=1). The v0.16.0 auto-allocate
// happened when the operator's skyadmin row was first
// loaded. We verify:
//
//   - skyadmin's per-user rule includes 10.0.1.0/24
//   - skyadmin's rule is otherwise unchanged (no
//     extra CIDRs from other users' allocations)
//   - other users' rules are unchanged (no skyadmin
//     CIDR leak)
//
// The "no-op" property: adding skyadmin's CIDR to the
// per-user rule is the ONLY delta; the rest of the
// policy is structurally identical to the no-skyadmin-
// subnet case. The mesh feature (v0.22.0) will rely
// on this — every per-user rule is independent of
// the others.
func TestACLBuilder_SkyadminMigrationIsolated(t *testing.T) {
	d := openTestDB(t)
	skyadminID := seedPortalUser(t, d, "skyadmin")
	michailID := seedPortalUser(t, d, "michail")
	guestID := seedPortalUser(t, d, "guest")
	daniilID := seedPortalUser(t, d, "daniil")

	// Live state: skyadmin is in 10.0.1.0/24, michail
	// is in 10.0.8.0/24, guest in 10.0.11.0/24, daniil
	// in 10.0.12.0/24. Other users (none here in test
	// data) might or might not have subnets — doesn't
	// matter for this assertion.
	type userSubnet struct {
		uid  int64
		cidr string
	}
	for _, p := range []userSubnet{
		{skyadminID, "10.0.1.0/24"},
		{michailID, "10.0.8.0/24"},
		{guestID, "10.0.11.0/24"},
		{daniilID, "10.0.12.0/24"},
	} {
		if _, err := d.Exec(`INSERT INTO user_subnets
			(user_id, cidr, status, control_plane_url)
			VALUES (?, ?, 'active', '')`, p.uid, p.cidr); err != nil {
			t.Fatalf("seed %d: %v", p.uid, err)
		}
	}

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}

	// skyadmin's rule should have own identity + own CIDR.
	skyadminRule := ruleFor(t, aclStr, "skyadmin")
	if skyadminRule == nil {
		t.Fatal("skyadmin's rule not found")
	}
	skyadminDst := dstList(t, skyadminRule)
	wantSkyadminDst := []string{
		"skyadmin@tsnet.skynas.ru",
		"10.0.1.0/24",
	}
	if !equalStringSlice(skyadminDst, wantSkyadminDst) {
		t.Errorf("skyadmin's dst = %v, want %v", skyadminDst, wantSkyadminDst)
	}

	// michail's rule should have own identity + own CIDR.
	// NO leak from skyadmin or other users.
	michailRule := ruleFor(t, aclStr, "michail")
	if michailRule == nil {
		t.Fatal("michail's rule not found")
	}
	michailDst := dstList(t, michailRule)
	if containsCIDR(michailDst, "10.0.1.0/24") {
		t.Errorf("michail's dst leaks skyadmin's CIDR 10.0.1.0/24; dst=%v", michailDst)
	}
	if !containsCIDR(michailDst, "10.0.8.0/24") {
		t.Errorf("michail's dst missing own CIDR 10.0.8.0/24; dst=%v", michailDst)
	}

	// guest + daniil — no leaks either.
	for _, tc := range []struct {
		uname string
		own   string
	}{
		{"guest", "10.0.11.0/24"},
		{"daniil", "10.0.12.0/24"},
	} {
		rule := ruleFor(t, aclStr, tc.uname)
		if rule == nil {
			t.Fatalf("%s's rule not found", tc.uname)
		}
		dst := dstList(t, rule)
		if containsCIDR(dst, "10.0.1.0/24") {
			t.Errorf("%s's dst leaks skyadmin's CIDR 10.0.1.0/24; dst=%v", tc.uname, dst)
		}
		if !containsCIDR(dst, tc.own) {
			t.Errorf("%s's dst missing own CIDR %s; dst=%v", tc.uname, tc.own, dst)
		}
	}
}

// TestACLBuilder_MultipleSharesToOneGrantee pins a
// pre-condition for the v0.22.0 mesh feature: when
// multiple grantors share their subnets with the same
// grantee, the grantee's per-user dst list must contain
// ALL of the grantors' CIDRs (deduplicated, but the
// v0.17.1 INSERT OR IGNORE on the PK already prevents
// duplicates so we don't need an explicit dedup pass).
//
// This is exactly the "shared network" use case the
// operator described as "radmin-like": A, C, D all in
// the same mesh as B, B's rule extends to all of them.
func TestACLBuilder_MultipleSharesToOneGrantee(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	bobID := seedPortalUser(t, d, "bob")
	carolID := seedPortalUser(t, d, "carol")
	daniilID := seedPortalUser(t, d, "daniil")
	// All four have subnets.
	aliceCIDR := fmt.Sprintf("10.0.%d.0/24", aliceID)
	bobCIDR := fmt.Sprintf("10.0.%d.0/24", bobID)
	carolCIDR := fmt.Sprintf("10.0.%d.0/24", carolID)
	daniilCIDR := fmt.Sprintf("10.0.%d.0/24", daniilID)
	for _, p := range []struct {
		uid  int64
		cidr string
	}{
		{aliceID, aliceCIDR}, {bobID, bobCIDR},
		{carolID, carolCIDR}, {daniilID, daniilCIDR},
	} {
		if _, err := d.Exec(`INSERT INTO user_subnets
			(user_id, cidr, status, control_plane_url)
			VALUES (?, ?, 'active', '')`, p.uid, p.cidr); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// alice, carol, daniil all share their subnets with bob.
	// (bob's own share is a no-op via ErrSelfShare, but the
	// ACL builder doesn't see it — only rows in the shares
	// table are read.)
	for _, grantorID := range []int64{aliceID, carolID, daniilID} {
		if _, err := d.Exec(`INSERT INTO user_subnet_shares
			(grantor_user_id, grantee_user_id, created_at)
			VALUES (?, ?, 0)`, grantorID, bobID); err != nil {
			t.Fatalf("seed share grantor=%d: %v", grantorID, err)
		}
	}

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}

	// bob's per-user rule should have own identity + own
	// CIDR + all three grantors' CIDRs.
	bobRule := ruleFor(t, aclStr, "bob")
	if bobRule == nil {
		t.Fatal("bob's rule not found")
	}
	bobDst := dstList(t, bobRule)
	wantBobDst := map[string]bool{
		"bob@tsnet.skynas.ru": true,
		bobCIDR:               true,
		aliceCIDR:             true,
		carolCIDR:             true,
		daniilCIDR:            true,
	}
	if len(bobDst) != len(wantBobDst) {
		t.Errorf("bob's dst has %d entries, want %d; dst=%v",
			len(bobDst), len(wantBobDst), bobDst)
	}
	for want := range wantBobDst {
		if !containsCIDR(bobDst, want) {
			t.Errorf("bob's dst missing %q; dst=%v", want, bobDst)
		}
	}
}

// TestACLBuilder_BidirectionalShares — A shares with B
// AND B shares with A. The current design (v0.17.1)
// requires two separate Grant() calls; the v0.22.0 mesh
// feature will collapse this into a single
// "membership" operation. This test pins the v0.17.1
// behavior (works via two one-directional shares) so
// the mesh refactor doesn't regress to a single
// one-directional share.
func TestACLBuilder_BidirectionalShares(t *testing.T) {
	d := openTestDB(t)
	aliceID := seedPortalUser(t, d, "alice")
	bobID := seedPortalUser(t, d, "bob")
	aliceCIDR := fmt.Sprintf("10.0.%d.0/24", aliceID)
	bobCIDR := fmt.Sprintf("10.0.%d.0/24", bobID)
	for _, p := range []struct {
		uid  int64
		cidr string
	}{
		{aliceID, aliceCIDR}, {bobID, bobCIDR},
	} {
		if _, err := d.Exec(`INSERT INTO user_subnets
			(user_id, cidr, status, control_plane_url)
			VALUES (?, ?, 'active', '')`, p.uid, p.cidr); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// Both directions: alice→bob AND bob→alice.
	if _, err := d.Exec(`INSERT INTO user_subnet_shares
		(grantor_user_id, grantee_user_id, created_at)
		VALUES (?, ?, 0), (?, ?, 0)`,
		aliceID, bobID, bobID, aliceID); err != nil {
		t.Fatalf("seed bidirectional shares: %v", err)
	}

	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}

	// Each user's rule should now contain BOTH CIDRs.
	for _, tc := range []struct {
		uname    string
		ownCIDR  string
		otherCIDR string
	}{
		{"alice", aliceCIDR, bobCIDR},
		{"bob", bobCIDR, aliceCIDR},
	} {
		rule := ruleFor(t, aclStr, tc.uname)
		if rule == nil {
			t.Fatalf("%s's rule not found", tc.uname)
		}
		dst := dstList(t, rule)
		if !containsCIDR(dst, tc.ownCIDR) {
			t.Errorf("%s's dst missing own CIDR %s; dst=%v", tc.uname, tc.ownCIDR, dst)
		}
		if !containsCIDR(dst, tc.otherCIDR) {
			t.Errorf("%s's dst missing shared CIDR %s; dst=%v",
				tc.uname, tc.otherCIDR, dst)
		}
	}
}

// TestACLBuilder_InviteConsumeBridgeEndToEnd is the
// v0.22.0 critical-path test: it exercises the FULL
// v0.21.0 flow end-to-end:
//
//   1. grantor runs /invite → invite_codes row created
//   2. grantee runs /accept → atomic consume + bridge
//   3. ACL builder now sees the bridge (user_subnet_shares
//      row) and extends the grantee's dst
//
// This is the integration test the operator asked for:
// "Добавить тест проверки что подсети между собой могут
// общаться". The mesh feature (v0.22.0 Phase 2) builds
// on the v0.21.0 bridge; if this test fails, the bridge
// is broken and the mesh can't ship.
func TestACLBuilder_InviteConsumeBridgeEndToEnd(t *testing.T) {
	d := openTestDBWithInvites(t)
	aliceID := seedPortalUser(t, d, "alice")
	bobID := seedPortalUser(t, d, "bob")
	aliceCIDR := fmt.Sprintf("10.0.%d.0/24", aliceID)
	bobCIDR := fmt.Sprintf("10.0.%d.0/24", bobID)
	for _, p := range []struct {
		uid  int64
		cidr string
	}{
		{aliceID, aliceCIDR}, {bobID, bobCIDR},
	} {
		if _, err := d.Exec(`INSERT INTO user_subnets
			(user_id, cidr, status, control_plane_url)
			VALUES (?, ?, 'active', '')`, p.uid, p.cidr); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Step 1: alice generates an invite for bob.
	inv, err := invite.CreateInvite(d, aliceID, "bob", 0, "")
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if inv.Code == "" || len(inv.Code) != 8 {
		t.Fatalf("invite code shape wrong: %q", inv.Code)
	}

	// Step 2: bob validates + consumes the code.
	// ValidateCode resolves the grantee by username; we
	// use "bob" here to match the seeded username.
	validated, err := invite.ValidateCode(d, inv.Code, "bob", bobID)
	if err != nil {
		t.Fatalf("ValidateCode: %v", err)
	}
	if validated.GrantorUserID != aliceID {
		t.Errorf("GrantorUserID = %d, want %d", validated.GrantorUserID, aliceID)
	}
	consumed, err := invite.ConsumeCode(d, inv.Code, bobID)
	if err != nil {
		t.Fatalf("ConsumeCode: %v", err)
	}
	if consumed.Status != invite.StatusConsumed {
		t.Errorf("consumed.Status = %q, want %q", consumed.Status, invite.StatusConsumed)
	}

	// Step 3: ApplyBridge writes the user_subnet_shares row
	// (this is what the bot /accept handler does in
	// production; we exercise it here directly so the
	// ACL test doesn't depend on the bot wiring).
	// nil planeURLs → no ACL re-apply goroutine (the
	// test exercises the ACL re-render synchronously
	// via GenerateACL). The applier/auditor/notifier
	// are interface-typed so we pass typed nil
	// (otherwise Go would try to convert untyped nil
	// to the interface and fail with "cannot convert
	// nil to the type ...").
	if err := invite.ApplyBridge(d, aliceID, bobID, inv.Code, "bob",
		nil,
		invite.ACLApplier(nil),
		invite.Auditor(nil),
		invite.NotifierSink(nil),
	); err != nil {
		t.Fatalf("ApplyBridge: %v", err)
	}

	// Step 4: ACL builder should now extend bob's dst
	// with alice's CIDR.
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	bobRule := ruleFor(t, aclStr, "bob")
	if bobRule == nil {
		t.Fatal("bob's rule not found")
	}
	bobDst := dstList(t, bobRule)
	if !containsCIDR(bobDst, aliceCIDR) {
		t.Errorf("bob's dst missing alice's CIDR %s after bridge; dst=%v",
			aliceCIDR, bobDst)
	}
	// alice's rule should be unchanged (the share is
	// one-directional).
	aliceRule := ruleFor(t, aclStr, "alice")
	aliceDst := dstList(t, aliceRule)
	if containsCIDR(aliceDst, bobCIDR) {
		t.Errorf("alice's dst leaks bob's CIDR %s; dst=%v", bobCIDR, aliceDst)
	}

	// Step 5: idempotency. Re-applying the bridge with
	// the same code should be a no-op (the share row
	// already exists, the ACL is unchanged).
	if err := invite.ApplyBridge(d, aliceID, bobID, inv.Code, "bob",
		nil,
		invite.ACLApplier(nil),
		invite.Auditor(nil),
		invite.NotifierSink(nil),
	); err != nil {
		t.Fatalf("ApplyBridge (2nd call): %v", err)
	}
	aclStr2, _ := GenerateACL(d)
	if aclStr != aclStr2 {
		t.Errorf("re-applying the bridge changed the ACL; want byte-equal, got diff:\n  before: %q\n  after:  %q",
			aclStr, aclStr2)
	}
}

// TestACLBuilder_TagOwnersContainAllPortalUsers pins
// the v0.17.0 + v0.21.0 invariant: every portal user
// appears in the `tagOwners.tag:private` and
// `tagOwners.tag:subnet-router` lists. The
// auto-approver (v0.16.7) issues preauth keys with
// tag:subnet-router, and the v0.16.6+ sidecar registers
// the user's tag:private devices. Without these owner
// entries, headscale rejects the policy with
// "tag not found".
//
// The mesh feature (v0.22.0) doesn't change this — it
// only extends the per-user dst list, not the
// tagOwners block. So the test is a regression guard,
// not a new assertion.
func TestACLBuilder_TagOwnersContainAllPortalUsers(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	seedPortalUser(t, d, "bob")
	seedPortalUser(t, d, "carol")
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	var doc struct {
		TagOwners map[string][]string `json:"tagOwners"`
	}
	if err := json.Unmarshal([]byte(aclStr), &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	owners, ok := doc.TagOwners["tag:private"]
	if !ok {
		t.Fatal("tagOwners.tag:private missing")
	}
	for _, want := range []string{
		"alice@tsnet.skynas.ru",
		"bob@tsnet.skynas.ru",
		"carol@tsnet.skynas.ru",
	} {
		found := false
		for _, o := range owners {
			if o == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tagOwners.tag:private missing %q; owners=%v", want, owners)
		}
	}
	// tag:subnet-router — same requirement, every portal
	// user.
	subOwners, ok := doc.TagOwners["tag:subnet-router"]
	if !ok {
		t.Fatal("tagOwners.tag:subnet-router missing")
	}
	for _, want := range []string{
		"alice@tsnet.skynas.ru",
		"bob@tsnet.skynas.ru",
		"carol@tsnet.skynas.ru",
	} {
		found := false
		for _, o := range subOwners {
			if o == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tagOwners.tag:subnet-router missing %q; owners=%v", want, subOwners)
		}
	}
}

// TestACLBuilder_InternetEgressLastRule is a
// regression guard for the v0.12.0.2 fix. The final
// rule in acls[] must be `* → autogroup:internet:*` —
// if any future change adds a new "broad" rule (e.g.
// for the v0.22.0 mesh's "shared internet" feature),
// it MUST be placed BEFORE the autogroup:internet
// rule, not after. Otherwise inter-user access via
// the tailnet (the 100.64.0.0/10 range) would be
// re-opened.
func TestACLBuilder_InternetEgressLastRule(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	seedPortalUser(t, d, "bob")
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	var doc struct {
		Acls []map[string]any `json:"acls"`
	}
	if err := json.Unmarshal([]byte(aclStr), &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	last := doc.Acls[len(doc.Acls)-1]
	dsts, _ := last["dst"].([]any)
	if len(dsts) != 1 || dsts[0] != "autogroup:internet:*" {
		t.Errorf("last rule dst = %v, want [\"autogroup:internet:*\"]; last rule: %v",
			dsts, last)
	}
}

// equalStringSlice returns true if two slices have
// the same set of entries (order-independent).
func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}
