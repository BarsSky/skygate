// relay_keepalive_b352_test.go — B352 (2026-10-05).
//
// The live incident these pin, in order:
//
//	19:44:10  karolina's route application times out (one SSH hiccup)
//	19:54:07  karolina is excluded from the healthy set (B309) and 251 of its claimed
//	          prefixes move to emilia/shardlotta
//	19:54:10  the very next application to karolina SUCCEEDS — and nothing re-runs the
//	          assignment, so it still owns nothing 40 minutes later
//	20:2x     basic's 21 prefixes are pinned `via=emilia`/`via=sharlotta` while the
//	          device is pinned to karolina: no access, with every server-side fact green
//
// plus the two structural defects the same incident exposed: a relay skygate had NEVER
// applied routes to was a legal owner (sharlotta owned 95 prefixes advertising 2), and
// a first-time assignment did not regenerate the ACL, so the pins kept the old owners.
package exit_rules

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
	"skygate/internal/headscale"
)

// b352SetState writes a relay's last route-application record (the B309 key).
func b352SetState(t *testing.T, d *sql.DB, relay, value string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		RelayApplyStateKey(relay), value); err != nil {
		t.Fatalf("seed relay_apply_state for %s: %v", relay, err)
	}
}

// TestB352_NeverAppliedRelayIsNotAnAssignmentCandidate: an online relay skygate has
// never configured must not be able to take prefixes — live, sharlotta owned 95 of them
// while advertising 2 routes, because the sync loops are built from the rules and no
// rule ever named it.
func TestB352_NeverAppliedRelayIsNotAnAssignmentCandidate(t *testing.T) {
	d := newB276DB(t)
	seedB276(t, d)
	now := time.Now().Unix()
	// Only karolina has ever been applied to. emilia is "healthy" in the health table.
	b352SetState(t, d, "karolina", fmt.Sprintf("%d|ok", now))

	got := healthyExitRelaysForAssignment(d)
	if len(got) != 1 || got[0] != "karolina" {
		t.Fatalf("healthy set = %v, want [karolina] — a relay with no recorded apply must not be a candidate", got)
	}
}

// TestB352_RecoveredRelayReclaimsItsClaimedPrefixes is the live sequence: karolina's
// application fails, the prefix moves to emilia, karolina answers again — and the NEXT
// pass must bring the prefix home. Before B352 the second pass never happened (the
// ownership decision only ran from a sync), so the pin stayed on emilia for good.
func TestB352_RecoveredRelayReclaimsItsClaimedPrefixes(t *testing.T) {
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Time{}
	ownershipACLMu.Unlock()
	t.Cleanup(func() {
		ownershipACLMu.Lock()
		ownershipACLLastRun = time.Time{}
		ownershipACLMu.Unlock()
	})

	d := newB276DB(t)
	seedB276(t, d)
	stub := &b276Stub{served: stalePolicyForB276(t)}
	srv := stub.server(t)
	hs := headscale.New(srv.URL, "test-token")
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: hs}

	now := time.Now().Unix()
	// emilia has been applied to before, so it is a legal candidate.
	b352SetState(t, d, "emilia", fmt.Sprintf("%d|ok", now-3600))
	// karolina's LAST application failed two minutes ago: it is excluded.
	b352SetState(t, d, "karolina", fmt.Sprintf("%d|err|dial tcp 100.64.0.2:18022: i/o timeout", now-120))

	if _, _, err := s.reconcilePrefixOwnership(); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	var owner string
	if err := d.QueryRow(`SELECT exit_node_id FROM prefix_owner WHERE prefix='104.16.0.0/12'`).Scan(&owner); err != nil {
		t.Fatalf("read back the assignment after the failure: %v", err)
	}
	if owner == "karolina" {
		t.Fatalf("the prefix stayed on karolina while its last application had failed — the exclusion is not in force")
	}

	// karolina answers again (this is what happened 3 seconds after the eviction).
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Time{}
	ownershipACLMu.Unlock()
	b352SetState(t, d, "karolina", fmt.Sprintf("%d|ok", now))

	if _, chg, err := s.reconcilePrefixOwnership(); err != nil {
		t.Fatalf("second reconcile: %v", err)
	} else if chg == 0 {
		t.Fatalf("the recovered relay did not take its claimed prefix back (chg=0) — this is the B352 live bug")
	}
	if err := d.QueryRow(`SELECT exit_node_id FROM prefix_owner WHERE prefix='104.16.0.0/12'`).Scan(&owner); err != nil {
		t.Fatalf("read back the assignment after the recovery: %v", err)
	}
	if owner != "karolina" {
		t.Fatalf("assignment owner = %q after karolina recovered, want karolina (its rules are the only claim)", owner)
	}
	if stub.putCount() == 0 {
		t.Errorf("the ACL was never regenerated after the owner changed — the per-CIDR pin would still name the old relay")
	}
}

// TestB352_FirstAssignmentReappliesACL pins the second half of the same fix: an INSERT
// is a decision. The pass used to re-apply the policy only when a prefix CHANGED owner,
// so a rebuilt table (inserted=191 changed=0) left every pin on the previous owner.
func TestB352_FirstAssignmentReappliesACL(t *testing.T) {
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Time{}
	ownershipACLMu.Unlock()
	t.Cleanup(func() {
		ownershipACLMu.Lock()
		ownershipACLLastRun = time.Time{}
		ownershipACLMu.Unlock()
	})

	d := newB276DB(t)
	seedB276(t, d)
	now := time.Now().Unix()
	b352SetState(t, d, "karolina", fmt.Sprintf("%d|ok", now))
	stub := &b276Stub{served: stalePolicyForB276(t)}
	srv := stub.server(t)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}

	ins, chg, err := s.reconcilePrefixOwnership()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if ins == 0 {
		t.Fatalf("expected the pass to INSERT the assignment (the table was empty)")
	}
	if chg != 0 {
		t.Fatalf("chg = %d, want 0 — this test is about the INSERT-only case", chg)
	}
	if stub.putCount() == 0 {
		t.Errorf("a first-time assignment did not regenerate the ACL — the pins keep the owners from before the rebuild")
	}
}

// TestB352_RelaysToKeepSynced: the sync lists are built from the rules, so a relay that
// lost its last rule is never visited again and keeps its last advertisement forever
// (live: emilia advertised 29 prefixes that had moved to karolina). Every relay with a
// recorded apply, and every relay the assignment table names, must stay in the list.
func TestB352_RelaysToKeepSynced(t *testing.T) {
	d := newB276DB(t)
	seedB276(t, d)
	now := time.Now().Unix()
	b352SetState(t, d, "emilia", fmt.Sprintf("%d|ok", now))
	b352SetState(t, d, "sharlotta", fmt.Sprintf("%d|err|dial tcp 100.64.0.4:22: i/o timeout", now))
	if _, err := d.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
	                     VALUES ('104.16.0.0/12', 'karolina', 'explicit', 1, 1, 0)`); err != nil {
		t.Fatalf("seed prefix_owner: %v", err)
	}
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	got := s.relaysToKeepSynced(map[string]bool{"karolina": true})
	want := map[string]bool{"emilia": true, "sharlotta": true}
	if len(got) != len(want) {
		t.Fatalf("relaysToKeepSynced = %v, want %v", got, []string{"emilia", "sharlotta"})
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("relaysToKeepSynced returned %q, which is not in the expected set", name)
		}
	}
	// The relay the caller already covers must not be returned twice.
	for _, name := range got {
		if name == "karolina" {
			t.Errorf("relaysToKeepSynced returned karolina although the caller's `already` set covers it")
		}
	}
}

// TestB352_PruneTargetIsTheOwnedSet: a relay with an apply record but no owned prefixes
// must be synced with an EMPTY route list (which `tailscale set --advertise-routes=`
// turns into the base routes) — that is what removes a stale advertisement.
func TestB352_PruneTargetIsTheOwnedSet(t *testing.T) {
	d := newB276DB(t)
	seedB276(t, d)
	now := time.Now().Unix()
	b352SetState(t, d, "emilia", fmt.Sprintf("%d|ok", now))
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	if !s.hasRelayApplyRecord("emilia") {
		t.Fatalf("hasRelayApplyRecord(emilia) = false although its record says ok")
	}
	if s.hasRelayApplyRecord("sharlotta") {
		t.Errorf("hasRelayApplyRecord(sharlotta) = true although it was never applied")
	}
	if got := s.ownedPrefixesForNodeFromTable("emilia"); len(got) != 0 {
		t.Errorf("ownedPrefixesForNodeFromTable(emilia) = %v, want empty (it owns nothing)", got)
	}
}
