// setupstate_b400_test.go — 2026-10-20 (B400): the wizard must ask the ownership
// question where the answer lives.
//
// THE REPORT (operator, verbatim): «сейчас все устройства принадлежат ему [tagged
// devices] и различие идёт только строго по тегам … в начальных настройках
// указывается что 14 устройств не определены за пользователем».
//
// THE MEASUREMENT (his installation, 2026-10-20):
//
//	node_owner_map: 14 rows, 0 unattributed (skyadmin 6 · infra 5 · michail 3)
//	headscale     : all 14 nodes belong to the built-in user `tagged-devices`
//	wizard        : «14 node(s) have no panel owner yet»
//
// The old rule compared headscale's `node.User` with the panel's username list. On a
// fully tagged tailnet that string is `tagged-devices` for EVERY node, so the counter
// could never reach zero — it measured a property of headscale's bookkeeping, not the
// presence of an owner. These tests pin the corrected order: infrastructure →
// node_owner_map → headscale's user (only for an untagged node) → review.
package setupstate

import (
	"context"
	"testing"

	"skygate/internal/headscale"
)

// b400Nodes are the four shapes that matter, all in one tailnet.
func b400Nodes() []headscale.NodeView {
	return []headscale.NodeView{
		// 1. tagged + attributed in the panel — the operator's 14 devices.
		{ID: "9", GivenName: "skyworker", UserName: taggedDevicesUser,
			Tags: []string{"tag:dev-skyadmin-skyworker"}},
		// 2. tagged, no panel row — the REAL review case.
		{ID: "17", GivenName: "stray-laptop", UserName: taggedDevicesUser,
			Tags: []string{"tag:dev-nobody-stray-laptop"}},
		// 3. an infrastructure relay (tag:dev-infra-*).
		{ID: "11", GivenName: "karolina", UserName: taggedDevicesUser,
			Tags: []string{"tag:dev-infra-karolina"}},
		// 4. legacy: no tag at all, headscale's user is still meaningful.
		{ID: "42", GivenName: "old-phone", UserName: "michail"},
	}
}

func b400Config() Config {
	return Config{
		InfraUsername: "infra",
		PortalByUsername: map[string]PortalUser{
			"skyadmin": {ID: 1, Name: "skyadmin"},
			"michail":  {ID: 2, Name: "michail"},
			"infra":    {ID: 3, Name: "infra"},
		},
		PortalOwn: []string{"skyadmin", "infra"},
		OwnerByNodeID: map[string]NodeOwner{
			"9":  {Username: "skyadmin", Tag: "tag:dev-skyadmin-skyworker"},
			"11": {Username: "infra", Tag: "tag:dev-infra-karolina"},
			// "17" deliberately absent: the unattributed case.
			// "42" absent too, but it has no tag → headscale still owns it.
		},
	}
}

// b400Row finds one row by hostname.
func b400Row(t *testing.T, rows []NodeRow, host string) NodeRow {
	t.Helper()
	for _, r := range rows {
		if r.Hostname == host {
			return r
		}
	}
	t.Fatalf("node %q missing from the table", host)
	return NodeRow{}
}

// TestB400_PanelOwnerIsTheAnswer is the exact live defect: a tagged device with a
// node_owner_map row is OWNED, even though headscale says `tagged-devices`.
func TestB400_PanelOwnerIsTheAnswer(t *testing.T) {
	cfg := b400Config()
	var rep Report
	rows := buildNodeRows(b400Nodes(), cfg, &rep)

	row := b400Row(t, rows, "skyworker")
	if row.NeedsReview {
		t.Fatalf("a device with a node_owner_map row was reported as awaiting review — the exact live bug (headscale user = %q)", taggedDevicesUser)
	}
	if row.Username != "skyadmin" {
		t.Errorf("owner = %q, want skyadmin (from node_owner_map, not from headscale)", row.Username)
	}
	if row.OwnerSource != OwnerSourcePanel {
		t.Errorf("ownerSource = %q, want %q — the page must be able to say WHERE the owner came from", row.OwnerSource, OwnerSourcePanel)
	}
}

// TestB400_TrulyUnattributedIsReview — the case the counter exists for: tagged, and
// no panel row. Not silently accepted.
func TestB400_TrulyUnattributedIsReview(t *testing.T) {
	cfg := b400Config()
	var rep Report
	rows := buildNodeRows(b400Nodes(), cfg, &rep)

	row := b400Row(t, rows, "stray-laptop")
	if !row.NeedsReview {
		t.Fatal("a tagged device with no node_owner_map row was accepted as owned")
	}
	if row.OwnerSource != "" {
		t.Errorf("ownerSource = %q, want empty for an unattributed device", row.OwnerSource)
	}
	if rep.PendingReview.Value != 1 {
		t.Errorf("PendingReview = %d, want 1 (only stray-laptop)", rep.PendingReview.Value)
	}
}

// TestB400_InfraIsNeverPending — a relay carrying tag:dev-infra-* is infrastructure
// even before its node_owner_map row exists.
func TestB400_InfraIsNeverPending(t *testing.T) {
	cfg := b400Config()
	delete(cfg.OwnerByNodeID, "11") // no panel row at all
	var rep Report
	rows := buildNodeRows(b400Nodes(), cfg, &rep)

	row := b400Row(t, rows, "karolina")
	if !row.Infra {
		t.Fatal("a tag:dev-infra-* node was not classified as infrastructure")
	}
	if row.NeedsReview {
		t.Error("an infrastructure node must not await review")
	}
	if rep.InfraNodes.Value != 1 {
		t.Errorf("InfraNodes = %d, want 1", rep.InfraNodes.Value)
	}
}

// TestB400_UntaggedNodeFallsBackToHeadscaleUser — the legacy path must keep working:
// a node with no tag is still attributable by headscale's user field.
func TestB400_UntaggedNodeFallsBackToHeadscaleUser(t *testing.T) {
	cfg := b400Config()
	var rep Report
	rows := buildNodeRows(b400Nodes(), cfg, &rep)

	row := b400Row(t, rows, "old-phone")
	if row.NeedsReview {
		t.Fatal("an untagged node whose headscale user is a panel user was reported as unattributed")
	}
	if row.OwnerSource != OwnerSourceHeadscale {
		t.Errorf("ownerSource = %q, want %q", row.OwnerSource, OwnerSourceHeadscale)
	}
	if row.Username != "michail" {
		t.Errorf("owner = %q, want michail", row.Username)
	}
}

// TestB400_OwnerNamingAnUnknownPanelUserIsReview — an attribution the runtime cannot
// honour is not an attribution: the wizard must surface it, not hide it.
func TestB400_OwnerNamingAnUnknownPanelUserIsReview(t *testing.T) {
	cfg := b400Config()
	cfg.OwnerByNodeID["9"] = NodeOwner{Username: "ghost-user", Tag: "tag:dev-ghost-user-skyworker"}
	var rep Report
	rows := buildNodeRows(b400Nodes(), cfg, &rep)

	if row := b400Row(t, rows, "skyworker"); !row.NeedsReview {
		t.Fatal("a node_owner_map row naming a user the panel does not have was accepted as owned")
	}
}

// TestB400_NoPanelAttributionKeepsTheOldRule — a caller that cannot read
// node_owner_map (nil map) must behave exactly as before, not report everything as
// unattributed: the fallback is the property the pre-B400 installation relied on.
func TestB400_NoPanelAttributionKeepsTheOldRule(t *testing.T) {
	cfg := b400Config()
	cfg.OwnerByNodeID = nil
	var rep Report
	rows := buildNodeRows(b400Nodes(), cfg, &rep)
	if row := b400Row(t, rows, "old-phone"); row.NeedsReview || row.OwnerSource != OwnerSourceHeadscale {
		t.Fatalf("without node_owner_map the headscale-user rule must still apply (got review=%t source=%q)",
			row.NeedsReview, row.OwnerSource)
	}
	// And the tagged ones are honestly unknown, because nothing can attribute them.
	if row := b400Row(t, rows, "skyworker"); !row.NeedsReview {
		t.Fatal("without node_owner_map a tagged node cannot be attributed — it must await review, not be guessed")
	}
}

// TestB400_DetectReportsTheCorrectedCount drives the whole Detect path so the report
// the dashboard renders carries the corrected number.
func TestB400_DetectReportsTheCorrectedCount(t *testing.T) {
	rep := Detect(context.Background(), b400FakeClient{nodes: b400Nodes(), users: []headscale.HSUser{
		{ID: "1", Name: "skyadmin"}, {ID: "2", Name: "michail"}, {ID: "3", Name: "infra"},
	}}, b400Config())
	if rep.State != StatePopulated {
		t.Fatalf("state = %s, want populated", rep.StateName())
	}
	if !rep.PendingReview.Known {
		t.Fatal("PendingReview is unknown — the count must be measured, not guessed")
	}
	if rep.PendingReview.Value != 1 {
		t.Errorf("PendingReview = %d, want 1 (only stray-laptop; the 3 attributed nodes are NOT pending)", rep.PendingReview.Value)
	}
}

// TestB400_TheStepIsNotBlockedWhenOnlyOneDeviceNeedsReview — the wizard step may be
// todo, but the number it prints must be the measured one (1, not 14).
func TestB400_TheStepIsNotBlockedWhenOnlyOneDeviceNeedsReview(t *testing.T) {
	rep := Detect(context.Background(), b400FakeClient{nodes: b400Nodes(), users: []headscale.HSUser{
		{ID: "1", Name: "skyadmin"}, {ID: "2", Name: "michail"}, {ID: "3", Name: "infra"},
	}}, b400Config())
	var review *Step
	for i := range rep.Steps {
		if rep.Steps[i].ID == "review" {
			review = &rep.Steps[i]
		}
	}
	if review == nil {
		t.Fatal("no review step in the populated wizard")
	}
	if review.Why == "" || review.WhyCount != 1 {
		t.Fatalf("review step = %+v, want a key plus WhyCount=1 (the measured number)", *review)
	}
}

// b400FakeClient is the narrow surface Detect needs.
type b400FakeClient struct {
	nodes []headscale.NodeView
	users []headscale.HSUser
}

func (f b400FakeClient) PingContext(context.Context) error { return nil }
func (f b400FakeClient) ListUsers() ([]headscale.HSUser, error) {
	return f.users, nil
}
func (f b400FakeClient) ListAllNodes() ([]headscale.NodeView, error) { return f.nodes, nil }
func (f b400FakeClient) GetACL() (string, error)                     { return `{"acls": []}`, nil }
