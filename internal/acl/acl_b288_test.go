// internal/acl/acl_b288_test.go — B288 (2026-09-22).
//
// The declarations an apply must not DELETE. Live on `aro` the live policy
// declared `tag:dev-daniil-homepc` and `tag:dev-daniil-laptop` while the policy
// this generator produced declared neither: both rows sit in `node_owner_map`
// owned by headscale's synthetic `tagged-devices` (every tagged node, B287), so
// `GetPerUserDeviceTags` — a JOIN on portal_users — cannot see them, and the
// B285 sweep only covers tags a GRANT names (homepc has no rule at all). The
// generator now also reads the ownership record itself.
//
// The same test pins that both generators agree on the OWNERS of a per-device
// tag, because two writers of one document is how the operator got a permanent
// «политика УСТАРЕЛА».
package acl

import (
	"database/sql"
	"encoding/json"
	"sort"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// b288SeedDB reproduces the live `aro` inventory on top of the B285 fixture:
// two more devices, every node_owner_map row owned by the synthetic
// `tagged-devices`, and NEITHER of the two referenced by any rule (laptop
// carries its dev tag, homepc has only its class tag in headscale).
func b288SeedDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB := b285SeedDB(t)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := sqlDB.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
	          VALUES ('3', 0, 'tagged-devices', 'tag:dev-daniil-laptop', 'laptop'),
	                 ('6', 0, 'tagged-devices', 'tag:dev-daniil-homepc', 'homepc')`)
	return sqlDB
}

func TestB288_EveryRecordedDevTagIsDeclared(t *testing.T) {
	sqlDB := b288SeedDB(t)
	gen, err := GenerateACLLiveFormat(sqlDB)
	if err != nil {
		t.Fatalf("GenerateACLLiveFormat: %v", err)
	}

	var doc struct {
		TagOwners map[string][]string `json:"tagOwners"`
	}
	if err := json.Unmarshal([]byte(gen), &doc); err != nil {
		t.Fatalf("the generated policy is not valid JSON: %v\n%s", err, gen)
	}

	// Without the ownership-record source neither laptop nor homepc can appear:
	// no grant names them and the portal-user JOIN cannot see their rows.
	for _, tag := range []string{
		"tag:dev-daniil-laptop", "tag:dev-daniil-homepc",
		"tag:dev-daniil-workpc", "tag:dev-infra-exit-node-vps",
	} {
		owners, ok := doc.TagOwners[tag]
		if !ok {
			t.Errorf("the generated policy does not declare %q — an apply would STRIP a declaration a node wears "+
				"(this is exactly the difference that made the live aro policy look 'better' than its replacement)", tag)
			continue
		}
		got := append([]string(nil), owners...)
		sort.Strings(got)
		want := []string{tagUser(t, tag) + "@ts.example.com", "tagged-devices@ts.example.com"}
		sort.Strings(want)
		if len(got) != len(want) {
			t.Errorf("owners of %q = %v, want %v", tag, owners, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("owners of %q = %v, want %v", tag, owners, want)
				break
			}
		}
	}

	// Belt and braces: whatever the document declares, it must satisfy the
	// invariant headscale enforces on the whole document.
	undeclared, err := headscale.UndeclaredTags(gen)
	if err != nil {
		t.Fatalf("UndeclaredTags: %v\n%s", err, gen)
	}
	if len(undeclared) > 0 {
		t.Errorf("the generated policy references undeclared tag(s): %v", undeclared)
	}
}

// TestB288_BothGeneratorsAgreeOnOwners: the grants generator (what the ACL apply
// paths push) and the legacy generator (what the bot/API bulk path pushes) must
// declare the same owner set for the same tag — otherwise every apply undoes the
// other writer's entry and the drift banner never clears.
func TestB288_BothGeneratorsAgreeOnOwners(t *testing.T) {
	sqlDB := b288SeedDB(t)
	live, err := GenerateACLLiveFormat(sqlDB)
	if err != nil {
		t.Fatalf("GenerateACLLiveFormat: %v", err)
	}
	legacy, err := GenerateACLForPlane(sqlDB, "")
	if err != nil {
		t.Fatalf("GenerateACLForPlane: %v", err)
	}

	ownersOf := func(doc string) map[string][]string {
		var d struct {
			TagOwners map[string][]string `json:"tagOwners"`
		}
		if err := json.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatalf("not valid JSON: %v\n%s", err, doc)
		}
		return d.TagOwners
	}
	lo, le := ownersOf(live), ownersOf(legacy)
	checked := 0
	for tag, want := range lo {
		got, ok := le[tag]
		if !ok {
			t.Errorf("the legacy generator does not declare %q which the live-format generator declares — the two "+
				"writers would rewrite each other's policy forever", tag)
			continue
		}
		checked++
		a := append([]string(nil), want...)
		b := append([]string(nil), got...)
		sort.Strings(a)
		sort.Strings(b)
		if len(a) != len(b) {
			t.Errorf("owners of %q differ between the generators: live=%v legacy=%v", tag, want, got)
			continue
		}
		for i := range a {
			if a[i] != b[i] {
				t.Errorf("owners of %q differ between the generators: live=%v legacy=%v", tag, want, got)
				break
			}
		}
	}
	if checked == 0 {
		t.Fatal("no tagOwners entry was compared — the test would pass vacuously")
	}
}

// tagUser extracts the user a per-device tag names, via the same helper the
// generator uses (so the expectation is not a second implementation).
func tagUser(t *testing.T, tag string) string {
	t.Helper()
	u, ok := db.PerDeviceTagUser(tag)
	if !ok {
		t.Fatalf("PerDeviceTagUser(%q) refused a tag this test expects to be a per-device tag", tag)
	}
	return u
}
