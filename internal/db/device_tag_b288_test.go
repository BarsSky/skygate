// internal/db/device_tag_b288_test.go — B288 (2026-09-22).
//
// The owner side of B287's idea. Three writers emitted `tagOwners` entries for
// `tag:dev-<user>-<host>` and each derived the owner list differently:
//
//	ACL generator         -> ["<user>@<base>"]
//	ownership backfill    -> ["<portal-user>@<base>", "tagged-devices@<base>"]
//	tag reconciler (auto) -> ["<row-username>@<base>", "tagged-devices@<base>"]
//
// where the row's `username` is headscale's synthetic `tagged-devices` for every
// tagged node. So an ACL apply stripped the sentinel owner and the next tag apply
// was refused with `400 requested tags [...] are invalid or not permitted`; the
// drift banner compared the two documents and reported a permanent «политика
// УСТАРЕЛА». These tests pin the single derivation and the tag parser behind it.
package db

import (
	"testing"
)

func TestB288_PerDeviceTagUser(t *testing.T) {
	cases := []struct {
		tag  string
		user string
		ok   bool
	}{
		{"tag:dev-daniil-workpc", "daniil", true},
		{"tag:dev-infra-exit-node-vps", "infra", true},
		{"TAG:DEV-Daniil-WorkPC", "daniil", true}, // headscale normalises case
		{"tag:dev-workpc", "", false},             // no <user>-<host> split
		{"tag:dev--workpc", "", false},            // empty user segment
		{"tag:dev-daniil-", "", false},            // empty host segment
		{"tag:private", "", false},                // class tag
		{"tag:exit-node-vps", "", false},          // legacy exit tag is not a dev tag
		{"", "", false},
	}
	for _, c := range cases {
		user, ok := PerDeviceTagUser(c.tag)
		if user != c.user || ok != c.ok {
			t.Errorf("PerDeviceTagUser(%q) = (%q, %v), want (%q, %v)", c.tag, user, ok, c.user, c.ok)
		}
	}
}

func TestB288_TagOwnersForUser(t *testing.T) {
	owners, err := TagOwnersForUser("daniil", "ts.example.com")
	if err != nil {
		t.Fatalf("TagOwnersForUser: %v", err)
	}
	if len(owners) != 2 || owners[0] != "daniil@ts.example.com" || owners[1] != "tagged-devices@ts.example.com" {
		t.Errorf("owners = %v, want [daniil@ts.example.com tagged-devices@ts.example.com] — without the sentinel "+
			"headscale refuses to re-apply the tag to an already-tagged node", owners)
	}

	// The infra convention: the tag names the role, and the sentinel still needs
	// to be able to carry it (that is what the live policy declares for
	// tag:dev-infra-exit-node-vps).
	owners, err = TagOwnersForUser("infra", "ts.example.com")
	if err != nil {
		t.Fatalf("TagOwnersForUser(infra): %v", err)
	}
	if len(owners) != 2 || owners[0] != "infra@ts.example.com" || owners[1] != "tagged-devices@ts.example.com" {
		t.Errorf("infra owners = %v, want [infra@ts.example.com tagged-devices@ts.example.com]", owners)
	}

	// A synthetic/empty user names only the sentinel (no self-referential pair).
	for _, u := range []string{"", "tagged-devices"} {
		owners, err = TagOwnersForUser(u, "ts.example.com")
		if err != nil {
			t.Fatalf("TagOwnersForUser(%q): %v", u, err)
		}
		if len(owners) != 1 || owners[0] != "tagged-devices@ts.example.com" {
			t.Errorf("owners for user %q = %v, want [tagged-devices@ts.example.com]", u, owners)
		}
	}

	if _, err := TagOwnersForUser("daniil", ""); err == nil {
		t.Error("an empty base domain returned no error — the tag could never become permitted and nothing would say so")
	}
}

func TestB288_ListDevTagsFromOwnerMap(t *testing.T) {
	_, d, err := OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := ApplyMigrations(d, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	// The live shape: EVERY row is owned by headscale's synthetic user, so the
	// portal-user JOIN (GetPerUserDeviceTags) sees none of these tags.
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname) VALUES
	          ('1', 0, 'tagged-devices', 'tag:dev-infra-exit-node-vps', 'exit-node-vps'),
	          ('2', 0, 'tagged-devices', 'tag:dev-daniil-workpc', 'workpc'),
	          ('3', 0, 'tagged-devices', 'tag:dev-daniil-homepc', 'homepc'),
	          ('4', 0, 'tagged-devices', 'tag:private', 'laptop'),
	          ('5', 0, 'tagged-devices', 'tag:dev-daniil-workpc', 'workpc-dup')`)

	got, err := ListDevTagsFromOwnerMap(d)
	if err != nil {
		t.Fatalf("ListDevTagsFromOwnerMap: %v", err)
	}
	want := []DevTagOwner{
		{Tag: "tag:dev-daniil-homepc", Username: "daniil"},
		{Tag: "tag:dev-daniil-workpc", Username: "daniil"},
		{Tag: "tag:dev-infra-exit-node-vps", Username: "infra"},
	}
	if len(got) != len(want) {
		t.Fatalf("ListDevTagsFromOwnerMap = %+v, want %+v (only per-device tags, de-duplicated, sorted)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %+v, want %+v (the user comes from the TAG, not the synthetic username column)", i, got[i], want[i])
		}
	}
}
