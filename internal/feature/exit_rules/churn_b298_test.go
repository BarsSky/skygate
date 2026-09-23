// B298 — derived-rule churn must not restart the control plane.
//
// Live on the native host `aro`, every five minutes, forever:
//
//	acl-drift: ACL re-applied (snapshot v483, generated=7331 bytes) — auto-updater tick changed 18 rule(s) (added=17 removed=1)
//	auto-updater: dedup removed 16 redundant derived rule row(s)
//
// On a `policy.mode: file` host an ACL re-apply IS `systemctl restart headscale`,
// so that pair was a control-plane restart 288 times a day. Two independent defects
// produced it:
//
//  1. `CollapseDuplicateDerivedRules` partitioned on FIVE columns, excluding
//     parent_domain, while `device_rules_natural_key_uniq` is a SIX-column index
//     (B237.23/V068) that deliberately lets two domains of one CDN keep a row per
//     CIDR each. The collapse therefore deleted the row the CDN short-circuit looks
//     for, so that domain re-resolved and re-inserted its whole published range set
//     on the next tick — and the collapse deleted it again.
//  2. The auto-updater and the periodic drift check shared the 60-second ownership
//     throttle, so one rotating /32 from DNS was enough to re-apply the policy.
//
// The tests below pin both halves: the collapse keeps per-domain rows, and the churn
// path defers where an operator action still applies.
package exit_rules

import (
	"database/sql"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
	"skygate/internal/headscale"
)

// resetChurnThrottles clears the package-level apply timestamps so each test
// starts from "nothing has been written yet".
func resetChurnThrottles(t *testing.T) {
	t.Helper()
	reset := func() {
		ownershipACLMu.Lock()
		ownershipACLLastRun = time.Time{}
		ownershipACLMu.Unlock()
		periodicDriftMu.Lock()
		periodicDriftLastRun = time.Time{}
		periodicDriftMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// b298Harness is a migrated database seeded with a rule whose generated policy
// differs from what the stub headscale serves — i.e. a tick that really has
// something to apply.
func b298Harness(t *testing.T) (*sql.DB, *b276Stub, *Service) {
	t.Helper()
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")
	resetChurnThrottles(t)
	d := newB276DB(t)
	seedB276(t, d)
	stub := &b276Stub{served: stalePolicyForB276(t)}
	srv := stub.server(t)
	return d, stub, &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}
}

// setLastApply backdates the shared "an ACL write happened at" stamp.
func setLastApply(d time.Duration) {
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Now().Add(-d)
	ownershipACLMu.Unlock()
}

// clearLastApply makes the next write look like the first one ever. (Note that
// setLastApply(0) does NOT do this — it stamps "just now", which is how the first
// version of this test asserted a deferral for the wrong reason.)
func clearLastApply() {
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Time{}
	ownershipACLMu.Unlock()
}

// The crux: two domains of the same CDN each keep their own row for the same CIDR.
// The six-column UNIQUE index allows that on purpose — that per-domain row is what
// the CDN short-circuit looks for (`parent_domain LIKE 'cdn:%:<domain>'`) and what
// B184's DOMAIN-status propagation reads. The pre-B298 five-column collapse deleted
// exactly those rows.
func TestB298_CollapseKeepsPerDomainRows(t *testing.T) {
	d := newB276DB(t)
	// The FK on device_rules needs the seeded user/device/relay rows.
	seedB276(t, d)
	exec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// Identical natural key, different parent_domain — one row per CF-fronted domain.
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, parent_domain)
	      VALUES (1, 9, 'skyadmin', 'karolina', 'subnet', '104.24.0.0/14', 'accept', 1, 'cdn:cloudflare:openai.com')`)
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, parent_domain)
	      VALUES (1, 9, 'skyadmin', 'karolina', 'subnet', '104.24.0.0/14', 'accept', 1, 'cdn:cloudflare:rutracker.org')`)

	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	removed, err := s.CollapseDuplicateDerivedRules()
	if err != nil {
		t.Fatalf("CollapseDuplicateDerivedRules: %v", err)
	}
	if removed != 0 {
		t.Fatalf("the collapse removed %d row(s) — it must only remove EXACT duplicates, otherwise the losing domain loses the marker its short-circuit needs", removed)
	}
	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM device_rules WHERE target_type='subnet' AND target_value='104.24.0.0/14'`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 2 {
		t.Fatalf("rows for 104.24.0.0/14 = %d, want 2 (one per domain); with 1 the tick re-inserts the other every five minutes", n)
	}
}

// A derived-rule change five minutes after the last write must WAIT for the churn
// budget; the same change with the budget free must still apply (otherwise the
// deferral assertion above would pass for the wrong reason).
func TestB298_ChurnThrottleDefersThenApplies(t *testing.T) {
	_, stub, s := b298Harness(t)

	setLastApply(5 * time.Minute) // past the 60s ownership budget, inside the 30m churn one
	if res := s.applyACLIfDriftedChurn("skygate-auto-updater",
		"auto-updater tick changed 18 rule(s) (added=17 removed=1)", true); res.Applied {
		t.Fatalf("a derived-rule change inside the churn budget was applied (snapshot v%d) — that is one headscale restart per DNS tick", res.Version)
	}
	if got := stub.putCount(); got != 0 {
		t.Fatalf("policy PUTs after the deferred churn call = %d, want 0", got)
	}

	clearLastApply() // budget free: the SAME call must apply, proving the drift is real
	res := s.applyACLIfDriftedChurn("skygate-auto-updater",
		"auto-updater tick changed 18 rule(s) (added=17 removed=1)", true)
	if !res.Applied {
		t.Fatalf("with the churn budget free the drift was still not applied: %+v", res)
	}
	if got := stub.putCount(); got != 1 {
		t.Fatalf("policy PUTs after the free churn call = %d, want 1", got)
	}
}

// An operator action in the same five-minute window is NOT churn and must land.
func TestB298_OperatorActionIgnoresTheChurnBudget(t *testing.T) {
	_, stub, s := b298Harness(t)
	setLastApply(5 * time.Minute)

	if res := s.applyACLIfDrifted("user-rule-create", "user rule by skyadmin"); !res.Applied {
		t.Fatalf("an operator action was deferred by the churn budget: %+v", res)
	}
	if got := stub.putCount(); got != 1 {
		t.Fatalf("policy PUTs after the operator action = %d, want 1", got)
	}
}

// The periodic drift check follows the auto-updater and exists to notice its churn,
// so it spends the same budget. Pre-B298 it used the 60s one and re-applied five
// minutes after every auto-updater write.
func TestB298_PeriodicDriftCheckSpendsTheChurnBudget(t *testing.T) {
	_, stub, s := b298Harness(t)
	setLastApply(5 * time.Minute)

	s.periodicDriftCheck()

	if got := stub.putCount(); got != 0 {
		t.Fatalf("the periodic drift check wrote the policy %d time(s) inside the churn budget", got)
	}
}
