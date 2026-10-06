// nodeownership_b355_test.go — B355 (2026-10-06).
//
// THE LIVE CASE. A device that logged in through OIDC (headscale created the duplicate
// user 90 for it) was attributed correctly — and then sat in «ожидание» forever, with
// the mesh reporting:
//
//	19:39:04 DBG backfill node=150 name=s24-fe--ned matchedTag=tag:private api_tags=[] hasPrivate=false
//	19:39:05 DBG backfill AddTag called for node=150 (ensure tag:private)
//	19:42:56 [devices] s24-fe--ned has no device-to-device ACL entry:
//	         no per-device tag is recorded for it, so the mesh has nothing to grant
//
// The operator had to transfer the device by hand, and that transfer is what finally
// wrote a per-device tag into the row. The defect: the ownership row kept the STRATEGY's
// scope tag (`tag:private`) while headscale was given `tag:dev-<user>-<host>`, and every
// consumer of the row — the mesh grant source (`internal/db/device_owner_b316.go`, which
// refuses anything that does not parse as `tag:dev-<user>-<host>`), the per-device ACL,
// the rule fan-out and the page's own state — reads the ROW.
//
// These tests pin the row.
package nodeownership

import (
	"strings"
	"testing"

	"skygate/internal/headscale"
)

// TestB355_OIDCRegistrationRecordsThePerDeviceTag is the regression: an OIDC node (no
// preauth key, no tags, headscale user name == portal username) must end up with the
// per-device tag IN THE ROW, not only in headscale.
func TestB355_OIDCRegistrationRecordsThePerDeviceTag(t *testing.T) {
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	src := newAutoTestDB(t)
	userID := seedAutoTestUser(t, src.DB, "skyadmin", 1)

	// Exactly the live shape: the node carries no tags at the moment of attribution
	// (headscale's duplicate OIDC user is irrelevant to the tag itself), and the
	// hostname has the live device's awkward form.
	hs := newAutoTestLister(headscale.NodeView{
		ID:       "150",
		Hostname: "s24-fe--ned",
		UserName: "skyadmin",
		UserID:   "90",
	})

	Backfill(src, hs, hs.nodes, userID, "skyadmin", nil)

	var tag string
	if err := src.DB.QueryRow(
		`SELECT tag FROM node_owner_map WHERE node_id = '150'`).Scan(&tag); err != nil {
		t.Fatalf("node_owner_map has no row for the OIDC node 150: %v", err)
	}
	if tag != "tag:dev-skyadmin-s24-fe--ned" {
		t.Fatalf("node_owner_map.tag = %q, want %q — the scope tag in the row is what made the mesh report "+
			"«no per-device tag is recorded for it» on the live deployment", tag, "tag:dev-skyadmin-s24-fe--ned")
	}
	// The per-device shape is what the mesh source requires; assert it the same way
	// that reader does rather than trusting the literal above.
	if !strings.HasPrefix(tag, "tag:dev-skyadmin-") || strings.HasSuffix(tag, "tag:private") {
		t.Errorf("the recorded tag %q does not parse as tag:dev-<user>-<host>, so the mesh would still refuse it", tag)
	}
	// Headscale must still carry the scope tag as well (AddTag is additive): the
	// privacy scope and the ACL's tagOwners entry depend on it.
	applied := hs.tagsFor(150)
	if !hasTag(applied, "tag:dev-skyadmin-s24-fe--ned") {
		t.Errorf("headscale did not get the per-device tag (got %v)", applied)
	}
	if !hasTag(applied, "tag:private") {
		t.Errorf("headscale lost the scope tag tag:private (got %v)", applied)
	}
}

// TestB355_ARenamedDeviceKeepsTheRowAndTheTagInStep guards the branch that already
// updated the row: the new unconditional write must not break the rename case, and the
// row must never keep the OLD hostname's tag.
func TestB355_ARenamedDeviceKeepsTheRowAndTheTagInStep(t *testing.T) {
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	src := newAutoTestDB(t)
	userID := seedAutoTestUser(t, src.DB, "skyadmin", 1)
	// The row already knows a per-device tag for the OLD hostname.
	seedAutoTestOwner(t, src.DB, "150", 1, "skyadmin", "tag:dev-skyadmin-oldname", "oldname")

	hs := newAutoTestLister(headscale.NodeView{
		ID:       "150",
		Hostname: "newname",
		UserName: "skyadmin",
		UserID:   "1",
		Tags:     []string{"tag:dev-skyadmin-oldname"},
	})

	Backfill(src, hs, hs.nodes, userID, "skyadmin", nil)

	var tag, hostname string
	if err := src.DB.QueryRow(
		`SELECT tag, hostname FROM node_owner_map WHERE node_id = '150'`).Scan(&tag, &hostname); err != nil {
		t.Fatalf("read the row after the rename: %v", err)
	}
	if hostname != "newname" {
		t.Errorf("node_owner_map.hostname = %q, want newname", hostname)
	}
	if tag != "tag:dev-skyadmin-newname" {
		t.Errorf("node_owner_map.tag = %q, want tag:dev-skyadmin-newname (the row must follow the device)", tag)
	}
	if strings.Contains(tag, "oldname") {
		t.Errorf("the row still names the old hostname: %q", tag)
	}
}
