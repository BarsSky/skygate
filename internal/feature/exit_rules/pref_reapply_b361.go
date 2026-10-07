// pref_reapply_b361.go — B361 (2026-10-07) — a stored preference change must
// reach headscale promptly, through the SAME apply path and budget as every other
// trigger.
//
// MEASURED, on the reference deployment the day this block was written: after the
// assignment returned to its majority claimant (B360), the four artefacts that
// depend on it converged at four unrelated moments — ownership 15:03, the stored
// preferences 15:43 (hourly reconciler), the relay's advertised routes had not run
// since 12:44 (needed a manual sync) and the ACL only at 16:07 (a throttle
// deferral). ~65 minutes during which the operator's device was pinned to a relay
// that served nothing.
//
// THE FIX IS NOT A NEW THROTTLE. `applyACLIfDrifted` (sync.go) already decides "is
// the live policy the one the database implies?" and already writes only when it
// differs; the only thing missing was a TRIGGER from the paths that store a
// preference. This file adds that trigger and nothing else:
//
//   - `TakeACLReapplySlot` is the exported form of the ONE package-level slot every
//     apply path spends (`takeACLApplySlot`, sync.go) — so a burst of preference
//     changes inside the 60s budget becomes ONE apply, exactly as a churning
//     ownership table does, and a preference change cannot starve an ownership
//     flip of its budget either (they share it, which is the point).
//   - `ReapplyACLAfterPreferenceChange` is the hook the /my and /admin preference
//     handlers call after a successful write. A nil hook (a test, or a boot path
//     that never wired it) is a NO-OP, never a panic: a preference write must not
//     fail because the ACL trigger is absent.
//
// WHY A HOOK AND NOT A DIRECT CALL: `internal/feature/my` and `internal/feature/admin`
// would have to import `internal/feature/exit_rules` and hold an `*exit_rules.Service`
// (with its HS client, notifier and DBSource) to call `applyACLIfDrifted` themselves,
// and feature/admin already imports feature/exit_rules only for the read-only
// staleness predicate. The callback keeps the feature packages independent and puts
// the wiring where every other cross-feature callback in this project already lives
// (cmd/skygate/main.go), which is also what makes the shared budget provable: the
// hook and the sync paths close over the very same function.
package exit_rules

import "time"

// prefReapply is the wired ACL trigger. Set once at boot by
// SetPreferenceACLReapply and read by ReapplyACLAfterPreferenceChange.
var prefReapply func(actor, detail string)

// SetPreferenceACLReapply wires the ACL re-apply the preference-writing surfaces
// (my.PostMyDevicePreferredExit, my.PostAdminDevicePreferredExit) run after a
// successful `device_exit_node_prefs` write.
//
// main.go passes `exitRulesSvc.ReapplyACLIfDrifted`, i.e. the same throttled,
// trigger-agnostic drift check the ownership flips and the rule churn use.
func SetPreferenceACLReapply(fn func(actor, detail string)) {
	prefReapply = fn
}

// ReapplyACLAfterPreferenceChange runs the wired trigger, or does nothing when no
// trigger is wired. The detail string names the change so the acl_snapshots row
// and the deferral log line say WHAT asked for the apply (a preference change is
// not an ownership flip, and the journal must be able to tell them apart).
func ReapplyACLAfterPreferenceChange(actor, detail string) {
	if prefReapply == nil {
		return
	}
	prefReapply(actor, detail)
}

// TakeACLReapplySlot is the exported form of the shared apply budget: it reports
// whether the caller may spend an apply NOW (and claims the slot when it may), or
// how long is left of the current budget.
//
// Exported for the B361 tests, which script a burst of preference changes and
// assert that only the first one may apply — the property that stops a burst of
// changes from becoming a burst of applies (and, on a `policy.mode: file` host, a
// burst of headscale restarts).
func TakeACLReapplySlot(now time.Time, throttle time.Duration) (bool, time.Duration) {
	return takeACLApplySlot(now, throttle)
}

// ResetACLReapplyThrottle clears the shared slot. TEST-ONLY: production never
// calls it (the budget is the point).
func ResetACLReapplyThrottle() {
	ownershipACLMu.Lock()
	ownershipACLLastRun = time.Time{}
	ownershipACLMu.Unlock()
}
