// 2026-09-16 (B257): pure-function tests for
// classifyNodeForAdoption. The integration (headscale API +
// DB) is exercised live on 192.168.13.69; the rules below
// pin the decision logic so a future refactor doesn't
// silently flip the rejection list.
//
// The 5 rejection rules are documented at the classifyNodeForAdoption
// function; the tests below exercise each one plus the happy path.

package admin

import (
	"testing"

	"skygate/internal/headscale"
)

// helper: build a minimal NodeView for tests.
func mkNode(id, hostname, userID, userName string, tags ...string) headscale.NodeView {
	return headscale.NodeView{
		ID:         id,
		Hostname:   hostname,
		UserID:     userID,
		UserName:   userName,
		Tags:       tags,
		IPAddresses: []string{"100.64.0.99"},
	}
}

// TestClassifyNodeForAdoption_HappyPath pins the basic case:
// a headscale node owned by a known portal user, with no
// existing node_owner_map row, returns an AdoptionCandidate
// that maps hostname → dev-tag prefix + portal_username.
func TestClassifyNodeForAdoption_HappyPath(t *testing.T) {
	n := mkNode("42", "cyborg", "1", "skyadmin")
	owned := map[string]struct{}{}
	portal := map[string]portalByHS{
		"1": {id: 1, username: "skyadmin"},
	}
	c, ok := classifyNodeForAdoption(n, owned, portal)
	if !ok {
		t.Fatalf("expected candidate for happy path, got skip")
	}
	if c.PortalUsername != "skyadmin" || c.PortalUserID != 1 {
		t.Errorf("PortalUsername=%q PortalUserID=%d; want skyadmin/1", c.PortalUsername, c.PortalUserID)
	}
	if c.ID != "42" || c.Hostname != "cyborg" {
		t.Errorf("ID/Hostname lost in copy: %q/%q", c.ID, c.Hostname)
	}
}

// TestClassifyNodeForAdoption_RejectEmptyNodeID pins rule #1:
// an empty NodeID is rejected. Defensive — headscale returns
// non-empty IDs in practice, but a stale/blank entry should
// never make it into the candidates list.
func TestClassifyNodeForAdoption_RejectEmptyNodeID(t *testing.T) {
	n := mkNode("", "ghost", "1", "skyadmin")
	portal := map[string]portalByHS{"1": {id: 1, username: "skyadmin"}}
	if _, ok := classifyNodeForAdoption(n, map[string]struct{}{}, portal); ok {
		t.Errorf("empty NodeID should be rejected (rule #1)")
	}
}

// TestClassifyNodeForAdoption_RejectAlreadyOwned pins rule #2:
// a node that's already in node_owner_map is not a candidate.
// (Re-adoption would create a duplicate row, which the
// node_id PK + UNIQUE constraint would catch — but rejecting
// at this layer keeps the UI clean: the "Assign" button
// only shows for actually-unadopted rows.)
func TestClassifyNodeForAdoption_RejectAlreadyOwned(t *testing.T) {
	n := mkNode("42", "cyborg", "1", "skyadmin")
	portal := map[string]portalByHS{"1": {id: 1, username: "skyadmin"}}
	owned := map[string]struct{}{"42": {}}
	if _, ok := classifyNodeForAdoption(n, owned, portal); ok {
		t.Errorf("already-owned node 42 should be rejected (rule #2)")
	}
}

// TestClassifyNodeForAdoption_RejectEmptyUserName pins rule #3:
// headscale's synthetic "tagged-devices" user shows up in
// node listings with UserName="" (the synthetic user has no
// per-node user). Rejecting these means they're not surfaced
// for manual adoption — they belong to the dev-tag strategy
// (Strategy D in the B77 backfill) instead.
func TestClassifyNodeForAdoption_RejectEmptyUserName(t *testing.T) {
	n := mkNode("42", "cyborg", "2147455555", "") // tagged-devices sentinel
	portal := map[string]portalByHS{
		"2147455555": {id: 99, username: "tagged-devices"},
	}
	if _, ok := classifyNodeForAdoption(n, map[string]struct{}{}, portal); ok {
		t.Errorf("empty UserName (tagged-devices) should be rejected (rule #3)")
	}
}

// TestClassifyNodeForAdoption_RejectOrphanHeadscaleUser pins
// rule #4: a headscale user with no portal_user row is NOT an
// adoption candidate. Adopting such a node would assign a
// portal user who isn't actually associated with the headscale
// user — wrong attribution. The flow for the orphan
// headscale user is B-mod-first-run-adoption (separate
// /admin/users action), not this handler.
func TestClassifyNodeForAdoption_RejectOrphanHeadscaleUser(t *testing.T) {
	n := mkNode("42", "cyborg", "77", "orphan_hs_user")
	// No entry in portalByHSID for user ID 77 — that's
	// the orphan-HS-user case.
	portal := map[string]portalByHS{
		"1": {id: 1, username: "skyadmin"},
	}
	if _, ok := classifyNodeForAdoption(n, map[string]struct{}{}, portal); ok {
		t.Errorf("orphan headscale user (no portal_users row) should be rejected (rule #4)")
	}
}

// TestClassifyNodeForAdoption_RejectZeroPortalID pins rule #5:
// the caller (findAdoptionCandidates) only inserts rows with
// HeadscaleUserID > 0 into portalByHSID, so id=0 shouldn't
// happen at runtime. The classifier doesn't trust it
// regardless — reject defensively.
func TestClassifyNodeForAdoption_RejectZeroPortalID(t *testing.T) {
	n := mkNode("42", "cyborg", "1", "skyadmin")
	portal := map[string]portalByHS{
		"1": {id: 0, username: ""}, // explicit zero — should never roundtrip through, but guard anyway
	}
	if _, ok := classifyNodeForAdoption(n, map[string]struct{}{}, portal); ok {
		t.Errorf("zero portal id should be rejected (rule #5)")
	}
}

// TestClassifyNodeForAdoption_PopulatesOSAndRole pins that the
// candidate's OS + DeviceType are filled by devicemeta.Detect*
// (not the raw nodeview which has no such fields). Spot-
// checks the common cases — full devicemeta coverage lives
// in internal/devicemeta and isn't re-asserted here.
func TestClassifyNodeForAdoption_PopulatesOSAndRole(t *testing.T) {
	n := mkNode("42", "raspberrypi-3", "1", "skyadmin")
	portal := map[string]portalByHS{"1": {id: 1, username: "skyadmin"}}
	c, ok := classifyNodeForAdoption(n, map[string]struct{}{}, portal)
	if !ok {
		t.Fatalf("expected candidate")
	}
	// OS should be non-empty (linux/macos/etc — exact label depends on devicemeta).
	if c.OS == "" {
		t.Errorf("OS not populated (got empty)")
	}
	// DeviceType is "client" / "exit-node" / "subnet-router" / "server" / "unknown".
	if c.DeviceType == "" {
		t.Errorf("DeviceType not populated (got empty)")
	}
}
