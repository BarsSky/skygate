// 2026-09-16 (B257): pure-function tests for
// classifyNodeForAdoption. The integration (headscale API +
// DB) is exercised live on 192.168.13.69; the rules below
// pin the decision logic so a future refactor doesn't
// silently flip the rejection list.
//
// The rejection rules are documented at the classifyNodeForAdoption
// function; the tests below exercise each one plus the happy path.
// B303 (v1.5.68) renegotiated rules #3 and #4 — see the
// _BecomesOwnerPick_B303 tests.

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

// TestClassifyNodeForAdoption_EmptyUserNameBecomesOwnerPick_B303 is
// the renegotiated rule #3 (B303, v1.5.68): a node whose UserName
// headscale does not report is no longer dropped — it becomes a
// candidate that asks the operator to name the owner (NeedsOwnerPick).
// The pre-B303 rule #3 asserted the skip; THAT is what left the
// operator's ownerless device with no working action at all.
func TestClassifyNodeForAdoption_EmptyUserNameBecomesOwnerPick_B303(t *testing.T) {
	n := mkNode("42", "cyborg", "2147455555", "") // tagged-devices sentinel
	portal := map[string]portalByHS{
		"2147455555": {id: 99, username: "tagged-devices"},
	}
	c, ok := classifyNodeForAdoption(n, map[string]struct{}{}, portal)
	if !ok || c == nil {
		t.Fatalf("empty UserName must now produce a candidate (B303 owner picker)")
	}
	if !c.NeedsOwnerPick {
		t.Errorf("NeedsOwnerPick=false; want true for a node headscale reports no user for")
	}
	if c.PortalUsername != "" || c.PortalUserID != 0 {
		t.Errorf("no owner may be guessed: got username=%q id=%d", c.PortalUsername, c.PortalUserID)
	}
}

// TestClassifyNodeForAdoption_OrphanHeadscaleUserBecomesOwnerPick_B303
// is the renegotiated rule #4 (B303, v1.5.68): the synthetic
// `tagged-devices` user (and any headscale user that has no
// portal_users row yet) cannot name a portal owner, so the row is
// offered with an explicit owner picker instead of being hidden.
func TestClassifyNodeForAdoption_OrphanHeadscaleUserBecomesOwnerPick_B303(t *testing.T) {
	n := mkNode("42", "cyborg", "2147455555", "tagged-devices")
	portal := map[string]portalByHS{
		"1": {id: 1, username: "skyadmin"},
	}
	c, ok := classifyNodeForAdoption(n, map[string]struct{}{}, portal)
	if !ok || c == nil {
		t.Fatalf("tagged-devices node must now produce a candidate (B303)")
	}
	if !c.NeedsOwnerPick {
		t.Errorf("NeedsOwnerPick=false; want true for a node with no portal owner")
	}
}

// TestClassifyNodeForAdoption_RejectZeroPortalID pinned rule #5
// (defensive: a portalByHS entry with id=0 was rejected).
//
// **Renegotiated by B303 (v1.5.68)**: a zero id means the map cannot
// name an owner, which is the owner-pick case — not a reason to hide
// the device. The defensive intent is kept: no user is auto-selected.
func TestClassifyNodeForAdoption_RejectZeroPortalID(t *testing.T) {
	n := mkNode("42", "cyborg", "1", "skyadmin")
	portal := map[string]portalByHS{
		"1": {id: 0, username: ""}, // explicit zero — should never roundtrip through, but guard anyway
	}
	c, ok := classifyNodeForAdoption(n, map[string]struct{}{}, portal)
	if !ok || c == nil {
		t.Fatalf("zero portal id must produce an owner-pick candidate (B303)")
	}
	if !c.NeedsOwnerPick || c.PortalUserID != 0 || c.PortalUsername != "" {
		t.Errorf("zero portal id must not select an owner: NeedsOwnerPick=%v id=%d username=%q",
			c.NeedsOwnerPick, c.PortalUserID, c.PortalUsername)
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
