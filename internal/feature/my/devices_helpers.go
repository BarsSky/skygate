// devices_helpers.go — package-local helpers of the /my/devices
// handlers: parseIntQuery (B171 post-delete flash counter) and
// parseLastSeenAndClassify (B170 expired-row sub-classification).
// Split out of devices.go (work order item 5, 2026-10-07) —
// pure move, no behaviour change.
package my

import (
	"strconv"
	"time"
)

// parseIntQuery converts a query-string value to int64,
// returning 0 on empty / unparseable input. Used by the
// B171 post-delete flash to read ?deleted_rules=N (the
// number of device_rules the comprehensive delete
// removed). The template uses the int for `gt`
// comparisons (the string form is kept too, for the
// truthy check that hides the badge when no rules
// were cleaned).
func parseIntQuery(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// countTaggedGhosts removed — was failing Go's parser. The
// counter is inlined in GetMyDevices' render block.

// parseLastSeenAndClassify is the B170 (v1.5.2) helper
// called from GetMyDevices' expiry-enrichment pass. It
// parses the raw LastSeen string from headscale (empty /
// unparseable → zero time) and returns the parsed value
// + a sub-classification hint suitable for rendering
// under the red "expired" pill on /my/devices.
//
// The caller has already established that the row is
// expired (Expiry in the past); this function only
// fingerprints the cause from the |LastSeen − Expiry|
// gap.
//
// Returns one of:
//
//	"no_activity"  — lastSeen is empty / unparseable. The
//	                 row has no recent activity record:
//	                 either an orphan (admin force-removed
//	                 from headscale), a snapshot-only
//	                 entry, or a node that never came
//	                 back online after the last refresh.
//	                 Renew will likely 410 Gone.
//	"near_expiry"  — |lastSeen − expiry| ≤ 5 min. The
//	                 device was online at the moment the
//	                 expiry was set. Most likely cause:
//	                 `tailscale logout` (which pushes
//	                 node.expiry=now from the headscale
//	                 side). The device can re-register by
//	                 re-running `tailscale up` with a
//	                 fresh preauth key.
//	"while_offline"— |lastSeen − expiry| > 5 min. The
//	                 device was offline well before the
//	                 expiry was set. Either the TTL
//	                 ran out while the device was offline
//	                 (the typical "I forgot to renew"
//	                 case), or the admin force-expired a
//	                 long-idle device via
//	                 `headscale nodes expire -i N
//	                 --immediate`.
//
// The 5-min window is a deliberate trade-off — see the
// comment block in GetMyDevices where this function is
// called. The exact threshold is unit-tested in
// devices_b170_test.go.
func parseLastSeenAndClassify(expiry time.Time, lastSeenRaw string, now time.Time) (time.Time, string) {
	if lastSeenRaw == "" {
		return time.Time{}, "no_activity"
	}
	ls, perr := time.Parse(time.RFC3339Nano, lastSeenRaw)
	if perr != nil {
		// Treat malformed timestamps the same as
		// empty: we have no usable activity signal,
		// so the safest hint is "no_activity" (the
		// operator should investigate, not assume
		// a clean natural-expiry story).
		return time.Time{}, "no_activity"
	}
	delta := expiry.Sub(ls)
	if delta < 0 {
		delta = -delta
	}
	if delta <= 5*time.Minute {
		return ls, "near_expiry"
	}
	return ls, "while_offline"
}

// backfillNodeOwnership was a local copy of the helper in
// internal/handlers/handlers_node_ownership.go. The
// refactor-v0.30 Phase B step 5b move replaced it with
// the BackfillNodeOwnership callback on the Service
// (set in main.go), so the actual work now lives in
// handlers_node_ownership.go's *App.backfillNodeOwnership
// and is invoked via the callback. The ~250-line
// implementation stays there; feature/my/devices.go
// just calls the callback. A future refactor can move
// the canonical implementation to a shared
// internal/nodeownership/ package; tracked as a
// follow-up.
