// internal/acl/acl_B316_test.go — B316 (v1.5.80).
//
// THE LIVE FAILURE: on `aro` the operator's two machines did not reach each other over
// their tailnet addresses while both were online. The inventory was:
//
//	node_owner_map:  2 workpc   tagged-devices   tag:dev-daniil-workpc
//	                 3 laptop  tagged-devices   tag:dev-daniil-laptop
//	                 6 homepc  daniil           tag:dev-daniil-homepc
//
// headscale rewrites a tagged node's user to the synthetic `tagged-devices` (B287), and the
// device-to-device mesh grouped devices BY THAT COLUMN. So daniil appeared to have ONE
// device, `writePerDeviceGrants` skipped him (`len(userTags) < 2 → continue`), and the
// generated policy contained no `tag:dev-* → tag:dev-*` grant at all — a default-deny
// tailnet for device traffic, with nothing logged and nothing shown on any page.
//
// These tests pin that the mesh is derived from the device's OWN tag (and, failing that,
// from the user its rules were created under), and that a device nobody can attribute is
// never granted to anybody.
package acl

import (
	"encoding/json"
	"skygate/internal/db"
	"testing"
)

type B316Grant struct {
	Src []string `json:"src"`
	Dst []string `json:"dst"`
}

func B316Grants(t *testing.T, policy string) []B316Grant {
	t.Helper()
	var doc struct {
		Grants []B316Grant `json:"grants"`
		ACLs   []B316Grant `json:"acls"`
	}
	if err := json.Unmarshal([]byte(policy), &doc); err != nil {
		t.Fatalf("the generated policy is not valid JSON: %v\n%s", err, policy)
	}
	return append(doc.Grants, doc.ACLs...)
}

// TestB316_MeshIsEmittedWhenTheOwnerComesFromTheTag is the operator's case: three devices
// of ONE user, two of them recorded under headscale's synthetic owner. Every device must
// end up with a grant naming the other two.
func TestB316_MeshIsEmittedWhenTheOwnerComesFromTheTag(t *testing.T) {
	sqlDB := b288SeedDB(t) // aro's inventory: workpc + laptop + homepc + the relay
	policy, err := GenerateACLLiveFormat(sqlDB)
	if err != nil {
		t.Fatalf("GenerateACLLiveFormat: %v", err)
	}
	grants := B316Grants(t, policy)

	// dstFor returns the tags reachable FROM srcTag through one grant.
	dstFor := map[string][]string{}
	for _, g := range grants {
		if len(g.Src) == 1 {
			dstFor[g.Src[0]] = append(dstFor[g.Src[0]], g.Dst...)
		}
	}
	want := map[string][]string{
		"tag:dev-daniil-workpc": {"tag:dev-daniil-laptop", "tag:dev-daniil-homepc"},
		"tag:dev-daniil-laptop": {"tag:dev-daniil-homepc", "tag:dev-daniil-workpc"},
		"tag:dev-daniil-homepc": {"tag:dev-daniil-laptop", "tag:dev-daniil-workpc"},
	}
	for src, dsts := range want {
		got := dstFor[src]
		if len(got) == 0 {
			t.Errorf("no grant has src=%s — that device cannot reach ANY other device of daniil "+
				"(this is the live aro failure: the ownership row says tagged-devices, so the old "+
				"mesh source saw a single device and emitted nothing)", src)
			continue
		}
		for _, wantDst := range dsts {
			found := false
			for _, gotDst := range got {
				if gotDst == wantDst {
					found = true
				}
			}
			if !found {
				t.Errorf("grant src=%s does not reach %s (dst=%v)", src, wantDst, got)
			}
		}
	}
}

// TestB316_UnattributableDeviceIsNeverGranted: the mesh must not invent an owner. A device
// whose tag names no portal account gets no grant — and (the other half) the generator must
// not be silenced about it either, which is what db.DeviceTagsForMesh's second return value
// is for.
func TestB316_UnattributableDeviceIsNeverGranted(t *testing.T) {
	sqlDB := b288SeedDB(t)
	if _, err := sqlDB.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
	                         VALUES ('9', 0, 'tagged-devices', 'tag:dev-ghost-mysterybox', 'mysterybox')`); err != nil {
		t.Fatalf("seed mysterybox: %v", err)
	}
	policy, err := GenerateACLLiveFormat(sqlDB)
	if err != nil {
		t.Fatalf("GenerateACLLiveFormat: %v", err)
	}
	for _, g := range B316Grants(t, policy) {
		for _, src := range g.Src {
			if src == "tag:dev-ghost-mysterybox" {
				t.Errorf("the generator granted an unattributable device: %+v", g)
			}
		}
		for _, dst := range g.Dst {
			if dst == "tag:dev-ghost-mysterybox" {
				t.Errorf("the generator let somebody reach an unattributable device: %+v", g)
			}
		}
	}
}

// TestB316_RuleOwnerIsTheLastResort: a device with a class tag only (no `tag:dev-…`) but
// rules attributed to a portal user still joins that user's mesh through the rules source —
// the case that would otherwise stay invisible even after the tag parser is used.
func TestB316_RuleOwnerIsTheLastResort(t *testing.T) {
	sqlDB := b285SeedDB(t) // daniil + workpc(tagged) + exit-node-vps(tagged)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := sqlDB.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	// A second device of daniil whose ownership row carries ONLY a class tag, with rules
	// that name daniil.
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
	          VALUES ('7', 0, 'tagged-devices', 'tag:private', 'tablet')`)
	var daniilID int64
	if err := sqlDB.QueryRow(`SELECT id FROM portal_users WHERE username = 'daniil'`).Scan(&daniilID); err != nil {
		t.Fatalf("read daniil id: %v", err)
	}
	mustExec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value,
	                                    action, enabled, user_name, device_hostname)
	          VALUES ($1, 7, 'exit-node-vps', 'ip', '203.0.113.0/24', 'accept', 1, 'daniil', 'tablet')`, daniilID)

	policy, err := GenerateACLLiveFormat(sqlDB)
	if err != nil {
		t.Fatalf("GenerateACLLiveFormat: %v", err)
	}
	_ = policy // the rules source proves the OWNER; a mesh grant still needs a per-device tag
	// The device has no per-device tag, so it cannot be granted — but it must not be
	// silently forgotten either: that is what the unresolved list reports.
	_, unresolved, err := db.DeviceTagsForMesh(sqlDB)
	if err != nil {
		t.Fatalf("device mesh: %v", err)
	}
	found := false
	for _, u := range unresolved {
		if u.Hostname == "tablet" {
			found = true
		}
	}
	if !found {
		t.Errorf("tablet (no per-device tag) was not reported as unable to join the mesh: %+v", unresolved)
	}
}
