// tag_kind_b279_test.go — B279 (v1.5.46) — regression guards for the
// class-tag vs per-node-tag distinction.
//
// Live case (native host `aro`, 2026-09-22): the operator's only relay
// carried the class tag `tag:exit-node` and nothing else. The exit_rules
// copy of "strip tag: to get a hostname" turned that into "node", the
// "Use preferred" button wrote the phantom into 23 device_rules rows
// (`my_exit_rules_apply_preferred preferred=node updated=21` in
// audit_log), and the monitor's per-tick sync put the class tag back
// into node_owner_map every five minutes, so no per-node tag could ever
// survive. These tests pin the predicates that now decide, once.
//
// Pure-function tests always run; the DB-backed ones are PG-only and
// skip when SKYGATE_TEST_PG_DSN is unset (same contract as the other
// db tests).
package db

import (
	"strings"
	"testing"
)

// TestIsClassTag_B279 — the four role tags are classes; everything
// per-node (and the empty/untagged markers) is not.
func TestIsClassTag_B279(t *testing.T) {
	classes := []string{
		"tag:exit-node",
		"tag:public",
		"tag:private",
		"tag:subnet-router",
		"TAG:Exit-Node",  // case drift must not resurrect the phantom
		"  tag:public  ", // whitespace
	}
	for _, c := range classes {
		if !IsClassTag(c) {
			t.Errorf("IsClassTag(%q) = false, want true (a class tag names a role, not a node)", c)
		}
	}
	notClasses := []string{
		"",
		"tag:untagged",
		"tag:dev-infra-exit-node-vps",
		"tag:dev-daniil-workpc",
		"tag:exit-emilia",
		"exit-node-vps",
	}
	for _, c := range notClasses {
		if IsClassTag(c) {
			t.Errorf("IsClassTag(%q) = true, want false", c)
		}
	}
}

// TestIsPerNodeTag_B279 — only a tag that names exactly one node is a
// per-node tag. "tag:exit-node" is the trap: it has the legacy
// "tag:exit-" prefix but is the class sentinel.
func TestIsPerNodeTag_B279(t *testing.T) {
	perNode := []string{
		"tag:dev-infra-exit-node-vps",
		"tag:dev-infra-emilia",
		"tag:dev-daniil-workpc",
		"tag:exit-emilia", // legacy per-node form still accepted
	}
	for _, tag := range perNode {
		if !IsPerNodeTag(tag) {
			t.Errorf("IsPerNodeTag(%q) = false, want true", tag)
		}
	}
	notPerNode := []string{
		"",
		"tag:untagged",
		"tag:exit-node",     // CLASS — the live aro value
		"tag:public",        // CLASS
		"tag:private",       // CLASS
		"tag:subnet-router", // CLASS
		"tag:dev-infra-",    // no hostname
		"tag:dev-",          // no hostname
		"tag:exit-",         // no hostname
		"exit-emilia",       // bare hostname is not a tag
	}
	for _, tag := range notPerNode {
		if IsPerNodeTag(tag) {
			t.Errorf("IsPerNodeTag(%q) = true, want false", tag)
		}
	}
}

// TestIsExitNodeTagForm_RejectsClassTag_B279 — NormalizeExitNodeTag's
// gate. Before B279 the "tag:exit-" prefix check let the class sentinel
// through, and that value then became the operator's "preferred
// exit-node".
func TestIsExitNodeTagForm_RejectsClassTag_B279(t *testing.T) {
	cases := map[string]bool{
		"tag:dev-infra-exit-node-vps": true,
		"tag:exit-emilia":             true,
		"tag:exit-node":               false, // B279: the class sentinel
		"tag:public":                  false,
		"tag:private":                 false,
		"tag:subnet-router":           false,
		"tag:dev-daniil-workpc":       false, // TD-17.1
		"tag:untagged":                false,
		"tag:dev-infra-":              false,
		"":                            false,
	}
	for tag, want := range cases {
		if got := isExitNodeTagForm(tag); got != want {
			t.Errorf("isExitNodeTagForm(%q) = %v, want %v", tag, got, want)
		}
	}
}

// TestPickPerNodeTag_B279 — headscale's array order must not decide the
// DB tag, and a class tag must never be picked.
func TestPickPerNodeTag_B279(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{
			name: "class tag first (the aro order) — infra tag still wins",
			tags: []string{"tag:exit-node", "tag:dev-infra-exit-node-vps"},
			want: "tag:dev-infra-exit-node-vps",
		},
		{
			name: "infra tag after class tags",
			tags: []string{"tag:public", "tag:private", "tag:dev-infra-emilia"},
			want: "tag:dev-infra-emilia",
		},
		{
			name: "only a class tag — no per-node identity",
			tags: []string{"tag:exit-node"},
			want: "",
		},
		{
			name: "only class tags",
			tags: []string{"tag:exit-node", "tag:public"},
			want: "",
		},
		{
			name: "no tags at all",
			tags: nil,
			want: "",
		},
		{
			name: "infra beats user-device tag",
			tags: []string{"tag:dev-daniil-workpc", "tag:dev-infra-emilia"},
			want: "tag:dev-infra-emilia",
		},
		{
			name: "user-device tag when no infra tag exists",
			tags: []string{"tag:private", "tag:dev-daniil-workpc"},
			want: "tag:dev-daniil-workpc",
		},
		{
			name: "legacy exit tag as the last resort",
			tags: []string{"tag:exit-node", "tag:exit-emilia"},
			want: "tag:exit-emilia",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PickPerNodeTag(c.tags); got != c.want {
				t.Errorf("PickPerNodeTag(%v) = %q, want %q", c.tags, got, c.want)
			}
		})
	}
}

// TestNormalizeExitNodeTag_RejectsClassTag_B279 — a node whose
// node_owner_map row holds the class tag has NO canonical per-node tag,
// so the preference form must be refused instead of storing a role.
func TestNormalizeExitNodeTag_RejectsClassTag_B279(t *testing.T) {
	if testing.Short() {
		t.Skip("PG-backed test; skipped in -short")
	}
	d := openTestDB(t)
	defer d.Close()

	const host = "b279classexit"
	_, _ = d.Exec(`DELETE FROM node_owner_map WHERE LOWER(hostname) = LOWER($1)`, host)
	b188SeedNodeOwner(t, d, 7200001, host, "tag:exit-node")
	defer func() { _, _ = d.Exec(`DELETE FROM node_owner_map WHERE LOWER(hostname) = LOWER($1)`, host) }()

	tag, err := NormalizeExitNodeTag(d, host)
	if tag != "" {
		t.Errorf("NormalizeExitNodeTag(%q) = %q, want empty (a class tag is not an identity)", host, tag)
	}
	if err == nil {
		t.Fatal("NormalizeExitNodeTag(class-tagged host) err = nil, want ErrClassTagNotPerNode")
	}
	if !isClassTagErr(err) {
		t.Errorf("err = %v, want errors.Is(..., ErrClassTagNotPerNode)", err)
	}
	if !strings.Contains(err.Error(), "tag:dev-infra-") {
		t.Errorf("err message should name the fix (tag:dev-infra-<host>); got %q", err.Error())
	}
}

// TestSyncNodesFromHeadscale_ClassTagNeverOverwritesPerNode_B279 — the
// monitor's per-tick auto-sync must not revert an intended per-node tag.
//
// This is the live `aro` loop: headscale says `tag:exit-node` (because
// that IS the node's only tag), the DB row says
// `tag:dev-infra-exit-node-vps` (what the operator wants and what the
// B272 reconciler pushes), and the sync used to take headscale's word
// for it every five minutes.
func TestSyncNodesFromHeadscale_ClassTagNeverOverwritesPerNode_B279(t *testing.T) {
	if testing.Short() {
		t.Skip("PG-backed test; skipped in -short")
	}
	d := openTestDB(t)
	defer d.Close()

	const host = "b279syncnode"
	_, _ = d.Exec(`DELETE FROM node_owner_map WHERE LOWER(hostname) = LOWER($1)`, host)
	b188SeedNodeOwner(t, d, 7200002, host, "tag:dev-infra-"+host)
	defer func() { _, _ = d.Exec(`DELETE FROM node_owner_map WHERE LOWER(hostname) = LOWER($1)`, host) }()

	// What the monitor now sends for a class-only relay: no per-node
	// tag at all. (Before B279 it sent `tag:exit-node`, which the
	// SQL-level guard below also refuses.)
	for _, incoming := range []string{"", "tag:exit-node"} {
		if _, _, err := SyncNodesFromHeadscale(d, []SyncNodeInfo{{
			ID: "7200002", Hostname: host, Tag: incoming, Username: "tagged-devices",
		}}); err != nil {
			t.Fatalf("SyncNodesFromHeadscale(tag=%q): %v", incoming, err)
		}
		var got string
		if err := d.QueryRow(`SELECT tag FROM node_owner_map WHERE node_id = $1`, "7200002").Scan(&got); err != nil {
			t.Fatalf("read back tag: %v", err)
		}
		if got != "tag:dev-infra-"+host {
			t.Errorf("after sync with headscale tag %q the row is %q, want tag:dev-infra-%s (a class tag must never overwrite a real one)", incoming, got, host)
		}
	}

	// A per-node tag from headscale still wins (the normal path).
	if _, _, err := SyncNodesFromHeadscale(d, []SyncNodeInfo{{
		ID: "7200002", Hostname: host, Tag: "tag:dev-infra-renamed", Username: "tagged-devices",
	}}); err != nil {
		t.Fatalf("SyncNodesFromHeadscale(per-node): %v", err)
	}
	var got string
	if err := d.QueryRow(`SELECT tag FROM node_owner_map WHERE node_id = $1`, "7200002").Scan(&got); err != nil {
		t.Fatalf("read back tag: %v", err)
	}
	if got != "tag:dev-infra-renamed" {
		t.Errorf("per-node headscale tag did not win: row = %q, want tag:dev-infra-renamed", got)
	}
}

// TestSyncNodesFromHeadscale_ClassTagFillsEmptyRow_B279 — the class tag
// is still allowed to FILL a row that has no tag at all (that is how a
// freshly adopted relay gets a starting value), it just may not replace
// one.
func TestSyncNodesFromHeadscale_ClassTagFillsEmptyRow_B279(t *testing.T) {
	if testing.Short() {
		t.Skip("PG-backed test; skipped in -short")
	}
	d := openTestDB(t)
	defer d.Close()

	const host = "b279emptyrow"
	_, _ = d.Exec(`DELETE FROM node_owner_map WHERE LOWER(hostname) = LOWER($1)`, host)
	b188SeedNodeOwner(t, d, 7200003, host, "")
	defer func() { _, _ = d.Exec(`DELETE FROM node_owner_map WHERE LOWER(hostname) = LOWER($1)`, host) }()

	if _, _, err := SyncNodesFromHeadscale(d, []SyncNodeInfo{{
		ID: "7200003", Hostname: host, Tag: "tag:exit-node", Username: "tagged-devices",
	}}); err != nil {
		t.Fatalf("SyncNodesFromHeadscale: %v", err)
	}
	var got string
	if err := d.QueryRow(`SELECT tag FROM node_owner_map WHERE node_id = $1`, "7200003").Scan(&got); err != nil {
		t.Fatalf("read back tag: %v", err)
	}
	if got != "tag:exit-node" {
		t.Errorf("class tag should fill an empty row; got %q, want tag:exit-node", got)
	}
}

// isClassTagErr reports whether err wraps ErrClassTagNotPerNode.
func isClassTagErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), ErrClassTagNotPerNode.Error())
}
