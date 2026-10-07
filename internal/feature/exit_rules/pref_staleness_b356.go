// internal/feature/exit_rules/pref_staleness_b356.go — B356 (2026-10-07).
//
// ONE PREDICATE FOR "IS THIS STORED PREFERENCE STALE?", shared by the two surfaces
// that must never disagree about it:
//
//   - the reconciler (PlanDevicePrefChange / ReconcileDeviceExitNodePrefs), which
//     repairs a DERIVED row and refuses to touch a human's;
//   - /admin/exit-nodes, which shows the operator the row and its consequence.
//
// The live incident this block exists for (2026-10-07): relays `karolina` and
// `sharlotta` went offline, `prefix_owner` moved all 139 prefixes to `emilia`, two
// devices stayed pinned to `tag:dev-infra-karolina`, and since B265 that preference
// is a permission FILTER on the per-device `autogroup:internet` grant — so those
// devices lost their routes and NO page and NO log said so.
//
// The function is deliberately PURE and cheap: it takes only facts the caller has
// already read (the health snapshot, the device's owning relays and their tags). A
// page render must not run the full planner per preference row, and a shared
// function plus a test that pins planner/page agreement is what keeps the two honest
// instead of a comment promising it.

package exit_rules

// Reasons for a stale stored preference. The first two share the planner's
// `stale-pref-*` vocabulary so the audit trail, the notification and the page all
// name one thing; the third is the B345 reason the planner already emits for the
// same shape when the row is being REPAIRED.
const (
	// StalePrefRelayUnusable — the B273 monitor measured the relay the preference
	// names and it is not usable (`offline` / `degraded`).
	StalePrefRelayUnusable = "stale-pref-relay-unusable"
	// StalePrefRelayOwnsNothing — the relay is up, but the data plane gives it none
	// of this device's prefixes while exactly one other relay owns them.
	StalePrefRelayOwnsNothing = "stale-pref-relay-owns-nothing"
	// StalePrefOwnerOverrides — the assignment table's owner for this device's
	// prefixes is a different relay, and that owner carries a per-node tag. This is
	// the reason `owner-overrides-rule-relay` reports from the planner's repair path.
	StalePrefOwnerOverrides = "owner-overrides-rule-relay"
)

// StaleExitPrefReason decides whether a stored preference is stale and returns the
// named reason. `ok == false` means the preference is fine (the converged state), so
// callers render nothing and move nothing.
//
//	prefTag                 the stored value ("" = no preference → never stale)
//	relayKnown              exit_node_health has a row for the relay prefTag names
//	relayUsable             monitoring.ExitNodeUsable(that row's state)
//	deviceOwnerTags         owner relay hostname → that relay's per-node tag, for the
//	                        prefixes THIS device's enabled subnet/ip rules cover
//	                        (db.OwnerTagCountsForDeviceRules + db.NormalizeExitNodeTag,
//	                        as collectDevicePrefState builds it)
//
// "Unknown" is never "broken": a relay with no health row (never measured) is not
// reported stale by the usability check, because the same judgement drives a WRITE
// in the reconciler and a missing measurement must not re-point a device. A device
// with no owners at all (no rules, or none of its prefixes assigned) is likewise not
// reported: there is no relay to move it to, and that state has its own named skip
// (`stale-pref-no-owner`) in the planner.
func StaleExitPrefReason(prefTag string, relayKnown, relayUsable bool, deviceOwnerTags map[string]string) (string, bool) {
	if prefTag == "" {
		return "", false
	}
	if relayKnown && !relayUsable {
		return StalePrefRelayUnusable, true
	}
	// Exactly ONE relay owns the prefixes this device's rules cover, it carries a
	// per-node tag, and it is not the relay the preference names. That is the B345
	// shape (a duplicate rule that named the wrong relay) AND the B356 shape (the
	// preferred relay was evicted from the assignment table because it stopped being
	// healthy).
	ownerTag, owners := "", 0
	for _, tag := range deviceOwnerTags {
		if tag == "" {
			continue
		}
		owners++
		ownerTag = tag
	}
	if owners == 1 && ownerTag != prefTag {
		return StalePrefOwnerOverrides, true
	}
	return "", false
}
