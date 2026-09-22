// internal/db/device_tag_b287_test.go — B287 (2026-09-22).
//
// The live shape: every `node_owner_map` row carries headscale's synthetic
// owner in `username` (headscale reassigns a node to `tagged-devices` as soon as
// it wears any tag) while its TAG names the real owner. /my/devices keyed on the
// username column in both of its ownership paths, so a user's own tagged devices
// were invisible on their page while /admin/devices listed them.
//
// These tests pin the two helpers that make the tag the ownership record.
package db

import (
	"testing"
)

// TestPerDeviceTagB287 pins the tag minting (lowercased, both halves required).
func TestPerDeviceTagB287(t *testing.T) {
	cases := []struct {
		user, host, want string
	}{
		{"daniil", "workpc", "tag:dev-daniil-workpc"},
		{"Daniil", "WorkPC", "tag:dev-daniil-workpc"}, // B176: headscale needs lowercase
		{" daniil ", " workpc ", "tag:dev-daniil-workpc"},
		{"", "workpc", ""},
		{"daniil", "", ""},
	}
	for _, c := range cases {
		if got := PerDeviceTag(c.user, c.host); got != c.want {
			t.Errorf("PerDeviceTag(%q, %q) = %q, want %q", c.user, c.host, got, c.want)
		}
	}
}

// TestTagNamesUserB287 pins the ownership question, including the two ways it
// can be answered WRONGLY (a prefix match across users, and treating an infra or
// class tag as a portal owner).
func TestTagNamesUserB287(t *testing.T) {
	cases := []struct {
		tag, user string
		want      bool
	}{
		{"tag:dev-daniil-workpc", "daniil", true},
		{"TAG:DEV-DANIIL-WORKPC", "Daniil", true},
		{"tag:dev-daniil-work-pc-2", "daniil", true}, // hostnames may contain dashes
		{"tag:dev-daniil-workpc", "dan", false},      // must not match a shorter name
		{"tag:dev-daniil-workpc", "daniil2", false},
		{"tag:dev-infra-exit-node-vps", "infra", false}, // a role, not an account
		{"tag:dev-infra-exit-node-vps", "daniil", false},
		{"tag:exit-node", "daniil", false}, // class tag
		{"tag:private", "daniil", false},
		{"tag:dev-daniil-", "daniil", false}, // no hostname → names no device
		{"", "daniil", false},
	}
	for _, c := range cases {
		if got := TagNamesUser(c.tag, c.user); got != c.want {
			t.Errorf("TagNamesUser(%q, %q) = %v, want %v", c.tag, c.user, got, c.want)
		}
	}
}

// TestHasPerDeviceTagB287 pins the live-tag check used by the live branch.
func TestHasPerDeviceTagB287(t *testing.T) {
	live := []string{"tag:exit-node", "tag:dev-daniil-workpc"}
	if !HasPerDeviceTag(live, "daniil", "workpc") {
		t.Error("the node carries its per-device tag but HasPerDeviceTag said no")
	}
	if HasPerDeviceTag(live, "daniil", "laptop") {
		t.Error("HasPerDeviceTag matched the wrong hostname")
	}
	if HasPerDeviceTag(live, "michail", "workpc") {
		t.Error("HasPerDeviceTag matched another user's tag")
	}
}

// TestListNodeOwnerNodeIDsByUserTagB287 runs the snapshot lookup against a real
// SQLite database with the live rows (username = the synthetic owner).
func TestListNodeOwnerNodeIDsByUserTagB287(t *testing.T) {
	_, sqlDB, err := OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := ApplyMigrations(sqlDB, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
		VALUES ('1', 0, 'tagged-devices', 'tag:dev-infra-exit-node-vps', 'exit-node-vps'),
		       ('2', 0, 'tagged-devices', 'tag:dev-daniil-workpc', 'workpc'),
		       ('3', 0, 'tagged-devices', 'tag:dev-daniil-laptop', 'laptop'),
		       ('4', 0, 'michail', 'tag:dev-michail-basic', 'basic')`); err != nil {
		t.Fatalf("seed node_owner_map: %v", err)
	}

	ids, err := ListNodeOwnerNodeIDsByUserTag(sqlDB, "daniil")
	if err != nil {
		t.Fatalf("ListNodeOwnerNodeIDsByUserTag: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("daniil ids = %v, want the two devices whose TAG names him (2, 3)", ids)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen["2"] || !seen["3"] {
		t.Errorf("ids = %v, want 2 and 3", ids)
	}
	if seen["4"] {
		t.Error("a username lookup must not leak another user's device (michail/basic)")
	}

	// A username lookup — the pre-B287 path — finds NOTHING on this data, which
	// is exactly why /my/devices rendered empty.
	byName, err := ListNodeOwnerNodeIDsByUsername(sqlDB, "daniil")
	if err != nil {
		t.Fatalf("ListNodeOwnerNodeIDsByUsername: %v", err)
	}
	if len(byName) != 0 {
		t.Errorf("by-username lookup = %v, want none (the rows say tagged-devices) — "+
			"this is the pre-B287 blind spot the test exists for", byName)
	}

	if got, _ := ListNodeOwnerNodeIDsByUserTag(sqlDB, ""); got != nil {
		t.Errorf("empty username = %v, want nil", got)
	}
}
