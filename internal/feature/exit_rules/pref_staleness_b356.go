// internal/feature/exit_rules/pref_staleness_b356.go — B356 (2026-10-07), widened by
// B361 (2026-10-07).
//
// ONE PREDICATE FOR "IS THIS STORED PREFERENCE STALE?", shared by the two surfaces
// that must never disagree about it:
//
//   - the reconciler (PlanDevicePrefChange / ReconcileDeviceExitNodePrefs), which
//     repairs a DERIVED row and refuses to touch a human's;
//   - /admin/exit-nodes and /my/exit-nodes, which show the operator the row and its
//     consequence.
//
// The live incident B356 exists for (2026-10-07): relays `karolina` and `sharlotta`
// went offline, `prefix_owner` moved all 139 prefixes to `emilia`, two devices stayed
// pinned to `tag:dev-infra-karolina`, and since B265 that preference is a permission
// FILTER on the per-device `autogroup:internet` grant — so those devices lost their
// routes and NO page and NO log said so.
//
// The live incident B361 exists for (the same day, the other half): device `a71` had
// ZERO rules, a HUMAN pin recorded ~46 days earlier naming `tag:dev-infra-emilia`,
// and `prefix_owner` holding 120 rows — every one of them `karolina`. `emilia` was
// `online`, so the B356 health predicate above said "fine", and this predicate said
// "fine" too, because it was built from the OWNERS OF THE DEVICE'S PREFIXES and a
// device with no rules has none. The relay that serves NOTHING is not caught by
// asking about owners; it is caught by asking whether the relay owns anything at all.
//
// The function is deliberately PURE and cheap: it takes only facts the caller has
// already read (the health snapshot, how many prefixes the preferred relay owns, and
// the device's owners). A page render must not run the full planner per preference
// row, and a shared function plus a test that pins planner/page agreement is what
// keeps the two honest instead of a comment promising it.

package exit_rules

// Reasons for a stale stored preference. The first one shares the planner's
// `stale-pref-*` vocabulary so the audit trail, the notification and the page all
// name one thing; the next two are the planner's own repair/skip reasons for the same
// shapes.
const (
	// StalePrefRelayUnusable — the B273 monitor measured the relay the preference
	// names and it is not usable (`offline` / `degraded`).
	StalePrefRelayUnusable = "stale-pref-relay-unusable"
	// StalePrefRelayOwnsNothing — the preferred relay is up but owns NO prefix at
	// all in the assignment table, while the device either has no rules (its egress
	// leans entirely on what that relay advertises — the a71 case) or its
	// rule-covered prefixes are served by another relay. B361.
	StalePrefRelayOwnsNothing = "stale-pref-relay-owns-nothing"
	// StalePrefOwnerOverrides — the assignment table's owner for this device's
	// prefixes is a different relay. This is the reason
	// `owner-overrides-rule-relay` reports from the planner's repair path.
	StalePrefOwnerOverrides = "owner-overrides-rule-relay"
	// StalePrefNoRules — the planner's named skip for a device with no rules whose
	// pinned relay owns nothing. The page renders it as the same
	// "relay owns nothing" consequence, so the two surfaces agree by construction.
	StalePrefNoRules = "stale-pref-no-rules"
)

// StaleExitPrefReason decides whether a stored preference is stale and returns the
// named reason. `ok == false` means the preference is fine (the converged state), so
// callers render nothing and move nothing.
//
//	prefTag           the stored value ("" = no preference → never stale)
//	relayOwnsAnything whether the preferred relay owns at least ONE prefix in
//	                  `prefix_owner` (B361; the a71 case is `false`)
//	relayKnown        exit_node_health has a row for the relay prefTag names
//	relayUsable       monitoring.ExitNodeUsable(that row's state)
//	deviceHasRules    the device has at least one enabled ip/subnet rule
//	deviceOwnerCount  how many distinct relays own the prefixes THIS device's rules
//	                  cover (0 for a device with no rules)
//	prefNamesOwner    the device's owner relay IS the relay the preference names
//	                  (an empty assignment table also reports false: nothing is known)
//
// "Unknown" is never "broken": a relay with no health row (never measured) is not
// reported stale by the usability check, because the same judgement drives a WRITE in
// the reconciler and a missing measurement must not re-point a device.
//
// The ORDER is the decision. The a71 shape must be named BEFORE the owner comparison,
// because a device with no rules has no owner to compare against and would otherwise
// fall through as "fine" — which is exactly how the operator's device stayed pinned to
// a relay that served nothing, with no page, no log and no counter saying so.
func StaleExitPrefReason(prefTag string, relayOwnsAnything, relayKnown, relayUsable, deviceHasRules bool, deviceOwnerCount int, prefNamesOwner bool) (string, bool) {
	if prefTag == "" {
		return "", false
	}
	if relayKnown && !relayUsable {
		return StalePrefRelayUnusable, true
	}
	// B361 (the a71 case). A relay that owns NOTHING serves nothing the device can
	// use, whatever the device's rule set looks like: with rules, none of their
	// prefixes can be served by it; without rules, the device's whole egress depends
	// on what this relay advertises and the assignment table gives it nothing.
	//
	// It is asked FIRST, unconditionally, because "the relay owns nothing" is a
	// property of the RELAY and the assignment table — not an inference from the
	// device's own rules, which is the inference that hid the case.
	//
	// It keeps the reason the device's shape deserves: a device with no rules gets
	// `stale-pref-no-rules` (the planner's own named skip, so the two surfaces agree
	// by construction), and a device whose rules exist but are served elsewhere gets
	// `stale-pref-relay-owns-nothing`.
	if !relayOwnsAnything {
		if !deviceHasRules {
			return StalePrefNoRules, true
		}
		return StalePrefRelayOwnsNothing, true
	}
	// Exactly ONE relay owns the prefixes this device's rules cover, and it is not
	// the relay the preference names. That is the B345 shape (a duplicate rule that
	// named the wrong relay) AND the B356 shape (the preferred relay was evicted
	// from the assignment table because it stopped being healthy).
	//
	// `prefNamesOwner` is what keeps this from crying wolf: when the single owner IS
	// the preferred relay the device is correctly pinned, and the old predicate's
	// `ownerTag != prefTag` test said exactly that. The comparison lives in the
	// caller because it needs the relay's hostname, which is TagToHostname's job —
	// and an empty/unreadable assignment table reports false here, which is the
	// conservative direction for a warning.
	if deviceHasRules && deviceOwnerCount == 1 && !prefNamesOwner {
		return StalePrefOwnerOverrides, true
	}
	return "", false
}
