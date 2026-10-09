// Package exit_rules — sync_acl.go owns the ACL apply pipeline: the
// one shared apply slot and its budgets (takeACLApplySlot), the
// ownership-driven re-apply and the drift checks that decide whether the
// live headscale policy still matches the one the database implies.
//
// Split out of sync.go (2026-10-08, PURE MOVE — the declarations below are
// byte-identical to what sync.go carried before the split). Route
// advertisement lives in sync_routes.go, the domain auto-updater in
// sync_domain.go; sync.go keeps the package doc.
package exit_rules

import (
	"fmt"
	"log"
	"sync"
	"time"

	"skygate/internal/acl"
	"skygate/internal/headscale"
	"skygate/internal/prefixowner"

	"skygate/internal/db"
)

// B276 — the ACL must follow the assignment table.
//
// The ownership table and the advertised routes are recomputed every few minutes
// (SyncAdvertisedRoutes / StaggeredSync), while the ACL — whose per-CIDR grants
// carry `via=[owner]` since B275 — was regenerated ONLY when a rule, user or
// device changed. So a prefix that changed relay kept an ACL pin naming the OLD
// relay: headscale's `via` is a permission filter, the client then never receives
// that route, and the traffic silently falls back to the direct path. Live on the
// reference host: the ACL was applied at 18:19, the table moved 28 Cloudflare/
// Google prefixes to the other relay after 19:26, and the operator's device lost
// its whole Cloudflare set while every log line stayed quiet.
//
// These fields live on the package (not on Service) because the apply is triggered
// from two goroutines — the admin/API path and the staggered background sync — and
// they must share one throttle: a churning table must not turn into an apply storm
// (each apply writes an acl_snapshots row and restarts nothing, but it does hit the
// policy API, and on a file-mode host a policy write restarts headscale).
//
// B374 (2026-10-09) — A DRIFT IS NOT ALWAYS CHURN. See `aclDriftBudget`.
var (
	ownershipACLMu      sync.Mutex
	ownershipACLLastRun time.Time
)

// aclApplySlotLastRun is the same instant `ownershipACLLastRun` holds, exposed for the
// DEFERRAL LOG ONLY.
//
// B361 needed the throttle decision in one function so that a preference change spends the
// same budget as an ownership flip. That moved the "when did one last run" read inside
// `takeACLApplySlot`, and the deferral line is the operator's only signal that a re-apply
// was postponed — so it has to keep naming how long ago the last apply ran and which
// budget held it back (scripts/check_b298_cdn_rule_churn.sh contract B7). Reading the
// instant here, under the same mutex, is not a second throttle: it is the same slot,
// observed.
func aclApplySlotLastRun() time.Time {
	ownershipACLMu.Lock()
	defer ownershipACLMu.Unlock()
	return ownershipACLLastRun
}

// ownershipACLThrottle bounds how often an ownership-driven ACL re-apply may run.
// Ownership flips are rare once B276's per-device claim counting is in place, so
// this only absorbs a genuinely churning table (the domain auto-updater rewrites
// derived rows every few minutes).
const ownershipACLThrottle = 60 * time.Second

// B361 (2026-10-07) — THE ONE PLACE THAT HANDS OUT AN APPLY SLOT.
//
// Every path that may need a re-apply (an ownership flip, an operator rule change,
// and now a stored exit preference change) must go through this function, because
// the budget it enforces is what turns a BURST of changes into ONE apply — on a
// `policy.mode: file` host every apply is a `systemctl restart headscale`.
//
// It is a pure function of (lastRun, now, throttle) plus the atomic claim of the
// package-level slot, so a test can drive a scripted burst without a clock:
//
//	run1 at T          → ok=true  (the first change applies)
//	run2 at T+1s       → ok=false (deferred: inside the budget)
//	run3 at T+61s      → ok=true  (the budget elapsed; the deferred state applies)
//
// A DEFERRAL IS NEVER SILENT (L-60): the caller logs the interval it is waiting
// for, so "the ACL did not follow" always has a line in the journal naming the
// budget that held it back. The deferred state is not lost either — the periodic
// drift check re-asks the question on its own interval, and the next change within
// the window applies on the same slot.
func takeACLApplySlot(now time.Time, throttle time.Duration) (bool, time.Duration) {
	ownershipACLMu.Lock()
	defer ownershipACLMu.Unlock()
	if !ownershipACLLastRun.IsZero() {
		elapsed := now.Sub(ownershipACLLastRun)
		if elapsed < throttle {
			return false, throttle - elapsed
		}
	}
	ownershipACLLastRun = now
	return true, 0
}

// churnACLThrottle bounds an ACL re-apply driven by DERIVED-rule churn: the
// domain auto-updater rewriting its resolved /32 rows (normal DNS/CDN rotation,
// not a configuration change) and the periodic drift check that exists to notice
// it afterwards.
//
// B298 (2026-09-23) — WHY THIS IS NOT THE 60s BUDGET. Live on `aro`, where
// `policy.mode: file` means every ACL write is a `systemctl restart headscale`:
// one Telegram/OpenAI /32 rotating was enough to re-apply the policy, and the
// journal showed a restart every five minutes, forever —
//
//	acl-drift: ACL re-applied (snapshot v483 …) — auto-updater tick changed 18 rule(s)
//	acl-drift: periodic drift check (…) needs a re-apply but one ran 0s ago — deferring (throttle 1m0s)
//
// A rotating derived /32 is worth waiting for; a control-plane restart every five
// minutes is not. Operator actions (rule create/delete, an explicit resync, an
// ownership move) keep the 60s budget, so an intentional change still lands
// within a minute.
const churnACLThrottle = 30 * time.Minute

// B374 (2026-10-09) — THE DRIFT PATH MUST NOT HIDE BEHIND THE CHURN BUDGET WHEN THE
// PLANES DISAGREE.
//
// Measured live on the reference deployment: after the 14:08 container recreate the
// assignment table had moved karolina's 197 prefixes to emilia minutes earlier, and
// the periodic drift check deferred its re-apply with
//
//	acl-drift: periodic drift check (the assignment table did not move) needs a
//	re-apply but one ran … ago — deferring to the next pass (throttle 30m0s)
//
// — a FALSE statement (the table HAD moved) carrying a FALSE budget: it spends the
// 30-minute DERIVED-ROW budget on a conflict that is not churn at all.
// The drill-down: `churnACLThrottle` exists for DERIVED rows the domain auto-updater
// rewrites from DNS (B298: one rotating /32 must not restart headscale every five
// minutes). The owner-convergence path has no such excuse: an ownership move
// changes every affected per-CIDR `via=` pin, and on a `policy.mode: file` host the
// apply is a headscale restart, so deferring it for half an hour leaves every client
// of every moved prefix without its route.
//
// The two halves are therefore named, not inferred:
//
//   - `moved == true` — the tables moved in this pass or the advertised/owned planes
//     disagree — spends the OPERATOR budget (ownershipACLThrottle, 60s), the same one
//     an operator action spends, and the detail text says which of the two actually
//     happened;
//   - `moved == false` — the pure derived-row churn path — keeps the 30m budget and
//     B298 is untouched.
func aclDriftBudget(moved bool) time.Duration {
	if moved {
		return ownershipACLThrottle
	}
	return churnACLThrottle
}

// aclDriftReason names WHY a drift re-apply is being considered, so a deferral (and
// the apply line) can never report a state that did not happen. B374: the live
// journal said "the assignment table did not move" about a pass whose table had moved
// minutes earlier, which sends the operator looking at the wrong plane.
func aclDriftReason(moved bool) string {
	if moved {
		return "the assignment table or the advertised/owned planes disagree (an ownership move, not derived-rule churn)"
	}
	return "no ownership move was observed in this pass"
}

// periodicDriftCheckInterval bounds how often the ownership-stable path compares
// the live policy with the one the database implies (B288). The comparison costs
// one `GenerateACLLiveFormat` + one headscale policy read and writes nothing when
// the two describe the same policy, so it is cheap — but it does not need to run
// on every staggered-sync pass either.
const periodicDriftCheckInterval = 5 * time.Minute

var (
	periodicDriftMu      sync.Mutex
	periodicDriftLastRun time.Time
)

// reconcilePrefixOwnership brings the assignment table up to date with the rules
// and regenerates the ACL when the two disagree — either because this pass MOVED
// a prefix to another relay (B276) or because the live policy was already stale
// for some other reason (B288).
//
// Returns the (inserted, changed) counts the caller logs, so both sync paths report
// the same numbers they used to.
func (s *Service) reconcilePrefixOwnership() (int, int, error) {
	// B312: fill the location of any relay that is due before the assignment runs, so
	// the location-priority fallback has data on the very pass that needs it. The
	// lookup has its own 12h guard, is skipped when SKYGATE_GEO_LOOKUP=off, and can
	// never fail this pass (a relay simply stays "unknown").
	s.RefreshExitNodeLocations()

	// B312: and when a prefix's owner is unreachable, hand it to the CLOSEST healthy
	// relay (same city → same country → shorter distance) instead of whichever relay
	// the engine visits first — the operator's «приоритет по сходному расположению».
	ins, chg, err := prefixowner.ReconcileWithPreference(s.dbc(), healthyExitRelaysForAssignment(s.dbc()), s.nearestRelayPreference())
	if err != nil {
		return ins, chg, err
	}
	// B352 (2026-10-05): an INSERT is a decision too. This used to re-apply the ACL
	// only when a prefix CHANGED owner, so a first-time assignment (the table was
	// rebuilt, a DB restore, an operator handing every prefix back with
	// SetManual(prefix, "")) left the per-CIDR `via=` pins naming the OLD owners —
	// measured live: after the table was rebuilt with `inserted=191 changed=0`, `basic`
	// still had 10 grants pinned to sharlotta and 11 to emilia until the ACL was
	// regenerated by hand from the panel.
	if ins > 0 || chg > 0 {
		s.applyACLAfterOwnershipChange(ins, chg)
		return ins, chg, nil
	}
	s.periodicDriftCheck()
	return ins, chg, nil
}

// periodicDriftCheck re-applies the ACL when headscale is serving a policy the
// current database state no longer implies, even though the assignment table did
// not move.
//
// B288 (2026-09-22) — this is the case the B276 commentary already claimed to
// cover ("a pre-existing mismatch created before this release"), but the call
// site gated the whole check on `chg > 0`: the table is stable on a healthy
// install, so a document written by an older generator — or one whose apply
// failed once — stayed stale FOREVER. Live on `aro`: «политика headscale
// УСТАРЕЛА» on /admin/exit-nodes while every exit rule was green and the tailnet
// has a single relay, and nothing in the system was ever going to change it.
//
// Safety: `applyACLIfDrifted` reads the live policy fresh, compares it with
// `headscale.PolicyEquivalent` (set semantics, B288) and returns WITHOUT a write
// when the two describe the same policy — so a converged host does one policy
// read per interval and no write at all. The write itself stays behind the CHURN
// budget (B298: 30m, not the 60s ownership throttle — on a file-mode host every
// write restarts headscale, and this path re-checks a rule set the domain
// auto-updater rewrites from DNS every tick), and the no-op line is suppressed
// (the periodic path runs unattended; the ownership-triggered path keeps its log).
//
// B374 (2026-10-09): `moved` is the caller's knowledge that the ownership table
// moved in THIS pass (or that the advertised and owned planes disagree). When it is
// true the check spends the OPERATOR budget and says so — see `aclDriftBudget`. The
// DEFAULT is false and is what the periodic caller passes: the periodic check's own
// reason is genuinely "no ownership move was observed this tick", so the historical
// message stays TRUE where it was always meant, and can no longer be printed about a
// pass that did move the table.
//
// The two variants are SEPARATE FUNCTIONS, not one function with a flag, because
// scripts/check_b298_cdn_rule_churn.sh contract B6 and
// scripts/check_b288_policy_drift_truth.sh contracts C1/C2/C3 legitimately pin the
// churn call site's exact shape — and that shape is what makes "which budget does
// this path spend?" auditable by grep. The moved variant reuses the same guard.
func (s *Service) periodicDriftCheck() {
	if !s.claimPeriodicDriftSlot() {
		return
	}
	s.applyACLIfDriftedChurn("skygate-periodic-drift",
		"periodic drift check ("+aclDriftReason(false)+")", false)
}

// periodicDriftCheckAfterOwnershipMove is the B374 half: the pass KNOWS the
// ownership table moved (or the advertised/owned planes disagree), so it must not
// spend the 30-minute derived-row budget on it and must not describe it as churn.
func (s *Service) periodicDriftCheckAfterOwnershipMove() {
	if !s.claimPeriodicDriftSlot() {
		return
	}
	s.applyACLIfDriftedThrottled("skygate-periodic-drift",
		"periodic drift check ("+aclDriftReason(true)+")", false, aclDriftBudget(true))
}

// claimPeriodicDriftSlot applies the five-minute guard shared by both variants and
// reports whether this caller may run.
func (s *Service) claimPeriodicDriftSlot() bool {
	periodicDriftMu.Lock()
	defer periodicDriftMu.Unlock()
	if !periodicDriftLastRun.IsZero() && time.Since(periodicDriftLastRun) < periodicDriftCheckInterval {
		return false
	}
	periodicDriftLastRun = time.Now()
	return true
}

// decideWithoutLivePolicy answers "should we write the policy?" from the last
// APPLIED snapshot in acl_snapshots when headscale cannot be read (B295).
//
// The snapshot is what skygate itself last wrote, so comparing the freshly
// generated document with it is the same question the live read would have
// answered — and it needs no API, no restart and no guess.
func (s *Service) decideWithoutLivePolicy(generated string) headscale.SnapshotVerdict {
	version, verr := db.LastAppliedACLVersion(s.dbc())
	if verr != nil || version <= 0 {
		return headscale.CompareWithSnapshot(generated, 0, "")
	}
	snapshot, serr := db.GetACLConfig(s.dbc(), version)
	if serr != nil {
		return headscale.CompareWithSnapshot(generated, version, "")
	}
	return headscale.CompareWithSnapshot(generated, version, snapshot)
}

// applyACLAfterOwnershipChange regenerates the policy and pushes it when (and only
// when) headscale is actually serving a different policy than the one the current
// ownership table implies.
//
// The comparison is why this is safe to call on every flip: an unchanged table, an
// unchanged rule set or a policy skygate already applied all end in "nothing to
// do" without a write. Failures are logged with the reason and never propagate —
// the routes sync must still finish (the alternative would be a half-synced pass
// where the routes moved and the ACL did not, which is exactly the bug).
func (s *Service) applyACLAfterOwnershipChange(ins, chg int) {
	s.applyACLIfDrifted("skygate-prefix-owner",
		fmt.Sprintf("prefix ownership changed (inserted=%d changed=%d) — ACL regenerated so every per-CIDR via= pin follows the new owner", ins, chg))
}

// applyACLIfDrifted regenerates the policy and pushes it when headscale is serving
// something else (B276).
//
// This is the single "make the control plane match the database" step, and it is
// deliberately trigger-agnostic: the three ways the live policy goes stale are an
// ownership flip (the per-CIDR pin), a rule change (the domain auto-updater adds and
// removes resolved CIDRs on every tick — measured live: 8 of the 15 newest rules had
// no alias in the policy at all), and a pre-existing mismatch created before this
// release. All three are the same question — "is the live policy the one I would
// generate right now?" — so they share one implementation, one throttle and one
// audit trail.
//
// `actor` is recorded as the acl_snapshots author (a stable system name for the
// automatic paths, so the audit trail says what decided).
//
// v1.5.42 (B-pending-write, 2026-09-21): the helper now returns
// acl.ApplyResult instead of bool so callers (PostMyExitRule,
// PostDeleteExitRule) can read the snapshot Version for their
// audit-detail line. Pre-v1.5.42 the handler wrote the snapshot
// silently via acl.ApplyGeneratedPolicy and the calling site had
// no way to know it happened. Callers that only care about the
// boolean "did a write happen?" can check `res.Applied`.
//
// Result semantics:
//   - {Version: 0, Applied: false, Err: nil} — throttle / no
//     headscale client / live-policy already matches. No write.
//   - {Version: 0, Applied: false, Err: <non-nil>} — DB or
//     regeneration failure. The live policy stays stale; the
//     next pass retries.
//   - {Version: N>0, Applied: true, Err: nil} — fresh snapshot
//     written, headscale SetPolicy succeeded. `Version` is the
//     acl_snapshots row id; the calling site should reference
//     it in audit_log and the user-facing success line.
func (s *Service) applyACLIfDrifted(actor, detail string) acl.ApplyResult {
	return s.applyACLIfDriftedThrottled(actor, detail, true, ownershipACLThrottle)
}

// ReapplyACLIfDrifted is the EXPORTED trigger B361 wires into the preference
// surfaces (see pref_reapply_b361.go). It is deliberately the very same call the
// ownership flip and the rule churn make — one decision, one budget, one audit
// trail — so a preference change cannot invent a second apply path (or a second
// throttle) beside them.
func (s *Service) ReapplyACLIfDrifted(actor, detail string) {
	_ = s.applyACLIfDrifted(actor, detail)
}

// applyACLIfDriftedChurn is the DERIVED-rule path — the domain auto-updater and
// the periodic drift check that follows it — behind the long churn budget (B298).
// Same decision, same generator, same equivalence guard; only the minimum interval
// between writes differs, because here the trigger is DNS rotation rather than an
// operator action.
func (s *Service) applyACLIfDriftedChurn(actor, detail string, logNoop bool) acl.ApplyResult {
	return s.applyACLIfDriftedThrottled(actor, detail, logNoop, churnACLThrottle)
}

// applyACLIfDriftedThrottled decides whether the live policy needs the generated
// one, writing it only when it truly differs. `throttle` is the minimum interval
// since the previous write that this caller accepts (B298: 60s for an operator
// action, 30m for derived-rule churn).
func (s *Service) applyACLIfDriftedThrottled(actor, detail string, logNoop bool, throttle time.Duration) acl.ApplyResult {
	// B361: the shared slot. One function decides whether this caller may apply,
	// so a burst of changes inside the budget produces ONE apply and a deferral
	// always leaves a line naming the budget.
	if ok, _ := takeACLApplySlot(time.Now(), throttle); !ok {
		// Contract B7 of scripts/check_b298_cdn_rule_churn.sh pins this line's shape: it
		// must name the budget it actually spent (`throttle`) and how long ago the last
		// apply ran, so a deferral is never silent and never mislabelled. B361 moved the
		// decision into `takeACLApplySlot`; the instant is read back through the same
		// slot's accessor, under the same mutex.
		log.Printf("acl-drift: %s needs a re-apply but one ran %s ago — deferring to the next pass (throttle %s)",
			detail, time.Since(aclApplySlotLastRun()).Round(time.Second), throttle)
		return acl.ApplyResult{Version: 0, Applied: false, Err: nil}
	}

	if s.HS == nil {
		log.Printf("acl-drift: %s but no headscale client is wired — the live policy is STALE; re-apply it manually on /admin/exit-rules", detail)
		return acl.ApplyResult{Version: 0, Applied: false, Err: nil}
	}
	gen, err := acl.GenerateACLLiveFormat(s.dbc())
	if err != nil {
		log.Printf("acl-drift: cannot regenerate the ACL (%s): %v — the live policy stays stale", detail, err)
		return acl.ApplyResult{Version: 0, Applied: false, Err: err}
	}
	// Compare against what headscale is serving. The cached policy may predate the
	// last apply, so force a fresh read: a stale "in sync" verdict here would skip
	// exactly the re-apply this function exists for.
	s.HS.InvalidateCache()
	live, err := s.HS.GetACL()
	if err != nil {
		// B295: do NOT write blind. A write on a `policy.mode: file` host restarts
		// headscale (`systemctl restart`), i.e. answering a read failure with a
		// restart is how the pre-B295 code turned a one-second restart window into a
		// permanent «состояние политики неизвестно» on the page. The last
		// successfully applied snapshot is in the database, so the same question can
		// be answered without touching the API.
		verdict := s.decideWithoutLivePolicy(gen)
		log.Printf("acl-drift: cannot read the live policy to decide whether a re-apply is needed (%v) — %s", err, verdict.Reason)
		if !verdict.Apply {
			return acl.ApplyResult{Version: 0, Applied: false, Err: nil}
		}
	} else if same, cmpErr := headscale.PolicyEquivalent(gen, live); cmpErr == nil && same {
		if logNoop {
			log.Printf("acl-drift: live policy already matches the generated one (generated=%d live=%d bytes) — %s", len(gen), len(live), detail)
		}
		return acl.ApplyResult{Version: 0, Applied: false, Err: nil}
	} else if cmpErr != nil {
		log.Printf("acl-drift: cannot compare the live policy with the generated one (%v) — applying, since the generated document is valid and the comparison cannot decide", cmpErr)
	}
	// B288.1: if the privileged applier reported a failure the last time it ran,
	// say so HERE. The handoff (a rename into the watched directory) succeeds
	// even when the write later fails or is rolled back, so without this line the
	// journal shows a successful apply every tick while the live policy never
	// changes — which is exactly how the live host stayed stale for days with a
	// green acl_snapshots row every five minutes.
	if st, ok := headscale.ReadPolicyApplyStatus(); ok && st.Result != "ok" && st.Result != "unchanged" {
		log.Printf("acl-drift: the privileged policy applier last reported %s — the write is NOT landing; check %s and %s",
			st.Summary(), headscale.PolicyApplyStatusPath(), "the applier's log next to it")
	}

	res := acl.ApplyGeneratedPolicy(s.dbc(), s.HS, gen, actor, detail, nil)
	if res.Err != nil {
		log.Printf("acl-drift: re-apply FAILED (%v) — the live policy stays stale; the next pass retries", res.Err)
		if s.Notifier != nil {
			go s.Notifier.SendAlert(fmt.Sprintf("❌ the headscale policy is stale but the re-apply failed\n  %s\n  err: %v", detail, res.Err))
		}
		return res
	}
	log.Printf("acl-drift: ACL re-applied (snapshot v%d, generated=%d bytes) — %s", res.Version, len(gen), detail)
	return res
}
