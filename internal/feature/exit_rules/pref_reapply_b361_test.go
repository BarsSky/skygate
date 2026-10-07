// pref_reapply_b361_test.go — B361 (2026-10-07).
//
// PROPAGATION MUST NOT WAIT FOR AN UNRELATED THROTTLE.
//
// Measured on the reference deployment the day this block was written: after the
// assignment returned to its majority claimant (B360), the four artefacts that depend
// on it converged at four unrelated moments over ~65 minutes — ownership 15:03, the
// stored preferences 15:43 (the hourly reconciler), the relay's advertised routes had
// not run since 12:44 and needed a manual sync, and the ACL only at 16:07, behind a
// throttle deferral. A device pinned to the relay that no longer served anything spent
// that hour with no access, and nothing said why.
//
// The trigger is `ReapplyACLAfterPreferenceChange` (pref_reapply_b361.go), wired at
// boot to the exit-rules Service's own `applyACLIfDrifted` — the SAME throttled,
// trigger-agnostic drift check the ownership flips and the rule churn use. These tests
// drive the REAL generator and the REAL apply path against a headscale policy stub:
//
//	(1) a stored preference change reaches headscale promptly, and the pushed document
//	    is the one the NEW value implies;
//	(2) a BURST of changes inside the budget produces exactly ONE apply (the shared
//	    slot — on a `policy.mode: file` host every apply restarts headscale);
//	(3) a deferral is never silent: the budget that held it back is logged;
//	(4) the preference surfaces really call the hook (the wiring, not just the helper).
package exit_rules

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
	"skygate/internal/headscale"
)

// b361DB is a migrated SQLite database laid out like the live tailnet: one user
// (`skyadmin`), one device with NO rules (`a71`), and two relays whose per-node tags
// node_owner_map carries.
func b361DB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	t.Setenv("SKYGATE_ADMIN_USER", "admin")
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b361.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	exec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	      VALUES (1, 'skyadmin', 'x', 1, 1)`)
	exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname) VALUES
	      ('9',  1, 'skyadmin', 'tag:dev-skyadmin-a71',  1, 0, 'a71'),
	      ('3',  1, 'infra',    'tag:dev-infra-emilia',  1, 0, 'emilia'),
	      ('11', 1, 'infra',    'tag:dev-infra-karolina', 1, 0, 'karolina')`)
	now := time.Now().Unix()
	for _, h := range []struct{ id, host string }{{"3", "emilia"}, {"11", "karolina"}} {
		exec(`INSERT INTO exit_node_health (node_id, hostname, online, last_seen, advertised_routes_ok,
		                                    has_exit_tag, state, healthy, last_check_at, last_state_change_at, consecutive_failures)
		      VALUES (?, ?, 1, ?, 1, 1, 'online', 1, ?, ?, 0)`, h.id, h.host, now, now, now)
	}
	// karolina owns the routing; emilia owns nothing (the a71 case: emilia is online
	// and advertises only the two defaults).
	exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at) VALUES
	      ('142.250.0.0/15', 'karolina', 'explicit', 1, 1, 0),
	      ('172.217.0.0/16', 'karolina', 'explicit', 1, 1, 0)`)
	// The human's stored pin to emilia (the relay that serves nothing), recorded long
	// before — this row must survive everything the tests below do.
	exec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
	      VALUES (1, 'a71', 'tag:dev-infra-emilia', 1, 0, 1)`)
	return d
}

// clearB361Slot resets the shared apply budget so a test starts from "no apply has run".
func clearB361Slot(t *testing.T) {
	t.Helper()
	ResetACLReapplyThrottle()
	t.Cleanup(ResetACLReapplyThrottle)
}

// TestB361_PreferenceChangeReachesHeadscalePromptly is (1): the generated document with
// the a71 pin (a relay that owns nothing) is NOT the one headscale is serving, and the
// hook re-applies it — through the shared drift check, i.e. the same decision and the
// same audit trail every other trigger uses.
func TestB361_PreferenceChangeReachesHeadscalePromptly(t *testing.T) {
	clearB361Slot(t)
	d := b361DB(t)
	// What headscale is serving: a policy that still pins a71 to the ownerless relay.
	stub := &b276Stub{served: `{
  "grants": [
    { "src": ["tag:dev-skyadmin-a71"], "dst": ["autogroup:internet"], "ip": ["*"], "via": ["tag:dev-infra-emilia"] }
  ],
  "hosts": {},
  "tagOwners": {}
}`}
	srv := stub.server(t)
	svc := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}
	SetPreferenceACLReapply(svc.ReapplyACLIfDrifted)
	t.Cleanup(func() { SetPreferenceACLReapply(nil) })

	// The preference CHANGES (the operator re-points the device at the relay that owns
	// the routing) — the trigger the surfaces call after a successful write.
	ReapplyACLAfterPreferenceChange("my_device_preferred_exit_set", "preference for a71 set to tag:dev-infra-karolina")

	if got := stub.putCount(); got != 1 {
		t.Fatalf("policy PUTs = %d, want 1 — a stored preference change must reach headscale without waiting for an unrelated throttle", got)
	}
	// The pushed document must no longer PIN the device to the ownerless relay. The
	// assertion is about a GRANT, not about the tag string: `tag:dev-infra-emilia` is
	// still declared in tagOwners (every relay's per-node tag is, so a future grant can
	// reference it) — what must be gone is the `via` on a71's autogroup:internet grant.
	if n := countAutogroupInternetViaForDevice(stub.lastPut(), "tag:dev-skyadmin-a71", "tag:dev-infra-emilia"); n != 0 {
		t.Errorf("the pushed policy still carries %d via-pinned autogroup:internet grant(s) for a71 → emilia (a relay that owns nothing):\n%s", n, stub.lastPut())
	}
	if !strings.Contains(stub.lastPut(), `"tag:dev-skyadmin-a71"`) {
		t.Errorf("the pushed policy does not mention the device at all:\n%s", stub.lastPut())
	}
	// The stored row is untouched even though the PIN moved.
	var tag string
	var setBy int64
	if err := d.QueryRow(`SELECT exit_node_tag, set_by_user_id FROM device_exit_node_prefs
	                      WHERE user_id = 1 AND device_hostname = 'a71'`).Scan(&tag, &setBy); err != nil {
		t.Fatalf("the human's stored preference is gone: %v", err)
	}
	if tag != "tag:dev-infra-emilia" || setBy != 1 {
		t.Errorf("stored row = (%q, set_by_user_id=%d), want it untouched", tag, setBy)
	}
}

// TestB361_BurstOfPreferenceChangesIsOneApply is (2) and (3): the budget is shared, so
// five changes inside the window are one apply — and every deferral names the budget
// that held it back. This is the property that keeps a file-mode host from restarting
// headscale five times.
func TestB361_BurstOfPreferenceChangesIsOneApply(t *testing.T) {
	clearB361Slot(t)
	d := b361DB(t)
	stub := &b276Stub{served: `{"grants":[],"hosts":{},"tagOwners":{}}`}
	srv := stub.server(t)
	svc := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}
	SetPreferenceACLReapply(svc.ReapplyACLIfDrifted)
	t.Cleanup(func() { SetPreferenceACLReapply(nil) })

	for i := 0; i < 5; i++ {
		ReapplyACLAfterPreferenceChange("my_device_preferred_exit_set", "burst change")
	}
	if got := stub.putCount(); got != 1 {
		t.Fatalf("policy PUTs = %d after a burst of 5 preference changes, want 1 — the burst must not become a burst of applies", got)
	}

	// And the slot itself, as a scripted timeline (no clock, no HTTP).
	ResetACLReapplyThrottle()
	base := time.Unix(1_800_000_000, 0)
	if ok, _ := TakeACLReapplySlot(base, ownershipACLThrottle); !ok {
		t.Fatal("the first change must be allowed to apply")
	}
	if ok, wait := TakeACLReapplySlot(base.Add(1*time.Second), ownershipACLThrottle); ok {
		t.Fatal("a second change 1s later must be deferred")
	} else if wait <= 0 || wait > ownershipACLThrottle {
		t.Errorf("wait = %s, want a positive remainder of the budget", wait)
	}
	if ok, _ := TakeACLReapplySlot(base.Add(ownershipACLThrottle), ownershipACLThrottle); !ok {
		t.Fatal("a change after the budget elapsed must be allowed to apply — the deferred state is not lost")
	}
}

// TestB361_ReconcilerWriteAlsoTriggersTheReapply is the other caller of the same hook:
// a preference the RECONCILER derived/repaired must reach the ACL through the same
// shared path and budget (that is the 15:43 → 16:07 gap of the live measurement).
func TestB361_ReconcilerWriteAlsoTriggersTheReapply(t *testing.T) {
	clearB361Slot(t)
	d := b361DB(t)
	// Make the derived repair real: a DERIVED row (set_by_user_id = 0) naming the
	// ownerless relay, with rules that unambiguously point at the relay that serves.
	if _, err := d.Exec(`UPDATE device_exit_node_prefs SET set_by_user_id = 0, exit_node_tag = 'tag:dev-infra-emilia'
	                     WHERE device_hostname = 'a71'`); err != nil {
		t.Fatalf("prepare derived row: %v", err)
	}
	stub := &b276Stub{served: `{"grants":[],"hosts":{},"tagOwners":{}}`}
	srv := stub.server(t)
	svc := &Service{DB: skygatedb.FixedDBSource{DB: d}, HS: headscale.New(srv.URL, "test-token")}
	SetPreferenceACLReapply(svc.ReapplyACLIfDrifted)
	t.Cleanup(func() { SetPreferenceACLReapply(nil) })

	svc.reapplyACLAfterPrefWrite("update", &ReconcilerChange{
		Action: "update", UserID: 1, Username: "skyadmin", DeviceHostname: "a71",
		OldTag: "tag:dev-infra-emilia", NewTag: "tag:dev-infra-karolina", Reason: "stale-pref-relay-unusable",
	})
	if got := stub.putCount(); got != 1 {
		t.Fatalf("policy PUTs = %d, want 1 — the reconciler's own write must trigger the ACL too", got)
	}
}

// TestB361_NilHookIsANoOp: a preference write must never fail because the ACL trigger
// is not wired (a test, a boot path that skipped it). The hook is a callback, and an
// absent one is silence — not a panic, and not a failed write.
func TestB361_NilHookIsANoOp(t *testing.T) {
	SetPreferenceACLReapply(nil)
	t.Cleanup(func() { SetPreferenceACLReapply(nil) })
	ReapplyACLAfterPreferenceChange("my_device_preferred_exit_set", "no trigger wired")
}

// countAutogroupInternetViaForDevice counts the grants in `policy` whose src is the
// given device tag, whose dst contains autogroup:internet, and whose via names the
// given relay. It is a text scan on purpose: the pushed bytes ARE the assertion (a
// JSON round-trip would hide a malformed document).
func countAutogroupInternetViaForDevice(policy, devTag, viaTag string) int {
	n := 0
	for _, line := range strings.Split(policy, "\n") {
		if !strings.Contains(line, `"`+devTag+`"`) {
			continue
		}
		if !strings.Contains(line, "autogroup:internet") || !strings.Contains(line, `"`+viaTag+`"`) {
			continue
		}
		if !strings.Contains(line, `"via"`) {
			continue
		}
		n++
	}
	return n
}

// TestB361_PreferenceSurfacesCallTheHook is (4): the wiring, not just the helper. The
// two endpoints an operator uses to re-point a device must reach the shared trigger —
// this is the change that turns the live ~65-minute convergence into one minute.
//
// The path is relative to THIS package's directory (the `go test` working directory),
// which is where the file lives: internal/feature/my/device_exit_pref.go.
func TestB361_PreferenceSurfacesCallTheHook(t *testing.T) {
	const path = "../my/device_exit_pref.go"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	src := string(raw)
	if n := strings.Count(src, "ReapplyACLAfterPreferenceChange("); n < 2 {
		t.Errorf("ReapplyACLAfterPreferenceChange appears %d time(s) in device_exit_pref.go, want one per handler (my + admin)", n)
	}
	for _, actor := range []string{"my_device_preferred_exit_set", "admin_device_preferred_exit_set"} {
		if !strings.Contains(src, `"`+actor+`"`) {
			t.Errorf("the %s handler does not name itself in the trigger detail — the audit trail must say what asked for the apply", actor)
		}
	}
}
