// acl_b443_test.go — B443. A rule's `src` must be a PER-NODE identity, never a class tag.
//
// A class tag (`tag:exit-node`, `tag:private`, `tag:server`, …) names a ROLE and matches
// every device carrying it, so a grant built from one applies on other users' devices —
// exactly the cross-user leak per-user rules exist to prevent. `node_owner_map.tag` can
// hold such a value (an operator can set one by hand, and the infra backfill has written
// sentinels), so the SHAPE is checked instead of assumed.
package acl

import (
	"testing"

	"skygate/internal/db"
)

func TestB443_ClassTagIsNeverARuleSource(t *testing.T) {
	entry := db.ACLEntry{DeviceID: 7}
	owners := map[int]deviceOwner{7: {Username: "svyatoslava", Hostname: "infra", Tag: "tag:exit-node"}}
	if got := deviceTagForRule(entry, owners); got != "" {
		t.Fatalf("a CLASS tag %q was used as a rule src — it would match other users' devices", got)
	}

	for _, class := range []string{"tag:private", "tag:server", "tag:untagged", "tag:dev-infra-"} {
		owners[7] = deviceOwner{Username: "x", Hostname: "y", Tag: class}
		if got := deviceTagForRule(entry, owners); got != "" {
			t.Errorf("class/incomplete tag %q was used as a rule src (%q)", class, got)
		}
	}
}

func TestB443_PerNodeTagIsStillTheSource(t *testing.T) {
	entry := db.ACLEntry{DeviceID: 7}
	for _, perNode := range []string{"tag:dev-alice-laptop", "tag:dev-infra-emilia", "tag:exit-node-vps"} {
		owners := map[int]deviceOwner{7: {Username: "alice", Hostname: "laptop", Tag: perNode}}
		if got := deviceTagForRule(entry, owners); got != perNode {
			t.Errorf("per-node tag %q was refused (got %q) — a real device identity must still be used", perNode, got)
		}
	}
}
