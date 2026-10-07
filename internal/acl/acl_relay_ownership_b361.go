// acl_relay_ownership_b361.go — B361 (2026-10-07) — "does the relay this
// preference names SERVE anything this device can use?".
//
// WHY THIS EXISTS, AND WHY IT IS NOT THE B356 FILE.
//
// B356 (acl_relay_health_b356.go) made the per-device `autogroup:internet` pin
// conditional on the relay being USABLE (the B273 predicate: `online`/`untagged`).
// That closed the "the relay is DOWN" half of the black hole. It left the other
// half wide open, and that is the operator's own device:
//
//	device a71    ZERO rows in device_rules
//	              one device_exit_node_prefs row, set_by_user_id = 1 (a HUMAN),
//	              ~46 days old, naming tag:dev-infra-emilia
//	prefix_owner  120 rows, ALL of them karolina (B360 had just returned the
//	              assignment to its majority claimant)
//	emilia        advertises 2 routes (0.0.0.0/0, ::/0) — it serves NONE of the
//	              per-destination routing
//	emilia state  `online`
//
// So B356's safety net did not fire: the relay was healthy. And the ACL kept
// emitting `via: ["tag:dev-infra-emilia"]` for a71, which headscale applies as a
// permission FILTER — the device was allowed to exit ONLY through a relay that
// carried none of the destinations karolina had taken over. "The relay is up" and
// "the relay carries what this device needs" are two different questions, and the
// generator must ask both.
//
// THE PREDICATE (one sentence): the preferred relay serves something this device
// can use when
//
//	(a) the device HAS rules — at least one of the prefixes those rules claim is
//	    owned by the preferred relay in `prefix_owner`; or
//	(b) the device has NO rules — the device's egress depends entirely on
//	    whatever the relay advertises, so the relay must own at least one prefix
//	    in `prefix_owner` at all. A relay that owns ZERO prefixes (emilia above)
//	    gives the device nothing but a restriction.
//
// When the predicate fails the caller emits the UNPINNED grant: the device keeps
// working through any available relay instead of being locked out. The operator's
// stored row is NEVER touched — only the pin is withheld while it would black-hole
// the device, so the human's choice takes effect again by itself the moment that
// relay serves something again. That distinction is the whole point of the block:
// never undo a human's choice, never lock the device out either.
//
// ASYMMETRY, DELIBERATELY: an EMPTY `prefix_owner` table (a fresh install, or a
// generation that ran before the first assignment pass) must NOT unpin anything.
// With no ownership data at all the generator cannot answer the question, and "I
// cannot answer" must keep the pre-B361 document — the same rule the health check
// applies to a relay with no `exit_node_health` row. Otherwise the first ACL
// generation on every fresh install would strip every pin in the tailnet.
package acl

import (
	"strings"
)

// servesNothing reports whether the preferred relay serves nothing this device
// can use, and why — the B361 predicate. It is PURE (every input is already
// resolved) so the unit tests need no database.
//
//	prefTag          the stored preference (a tag, e.g. "tag:dev-infra-emilia")
//	ownerTagsByPrefix prefix → owning relay TAG (prefixowner.TagByPrefix), the
//	                  same map the per-CIDR pin uses, so the switch control and
//	                  the pin can never name different relays
//	claimedPrefixes  every prefix the DEVICE's enabled ip/subnet rules claim
//	                  (nil/empty for a device with no rules — case (b))
//
// It returns (reason, true) when the pin must be withheld. `reason` is the raw
// fact for the log line, never a user-facing sentence.
func servesNothing(prefTag string, ownerTagsByPrefix map[string]string, claimedPrefixes map[string]bool) (string, bool) {
	if prefTag == "" {
		return "", false
	}
	// An empty assignment table answers nothing. Keep the pin: absence of
	// evidence must not rewrite a policy (the B356 rule, applied to ownership).
	if len(ownerTagsByPrefix) == 0 {
		return "", false
	}
	// The device's own rules: does the preferred relay own one of the prefixes
	// they claim?
	if len(claimedPrefixes) > 0 {
		for prefix := range claimedPrefixes {
			if strings.EqualFold(strings.TrimSpace(ownerTagsByPrefix[prefix]), prefTag) {
				return "", false
			}
		}
		return "the preferred relay owns none of the " + itoaB361(len(claimedPrefixes)) + " prefix(es) this device's rules claim", true
	}
	// No rules at all: the device leans entirely on what the relay advertises, so
	// the relay must own at least one prefix.
	for _, tag := range ownerTagsByPrefix {
		if strings.EqualFold(strings.TrimSpace(tag), prefTag) {
			return "", false
		}
	}
	return "the device has no rules of its own and the preferred relay owns no prefix at all — the pin would be a restriction with nothing behind it", true
}

// relayOwnsAnyPrefix reports whether the preferred relay owns at least one prefix
// in the assignment table, and how many.
//
// This is the same question the no-rules half of `servesNothing` asks, exposed so
// the reconciler and the pages can state the FACT the operator needs ("that relay
// currently serves 0 prefixes") without re-deriving it from a different source —
// the L-54 rule: one decision, one predicate.
//
// An empty map returns (false, 0), which the callers render as "0" and treat as
// "serves nothing"; the pages only reach this point when the assignment table has
// been read successfully, so 0 rows genuinely means 0.
func relayOwnsAnyPrefix(prefTag string, ownerTagsByPrefix map[string]string) (bool, int) {
	if prefTag == "" {
		return false, 0
	}
	n := 0
	for _, tag := range ownerTagsByPrefix {
		if strings.EqualFold(strings.TrimSpace(tag), prefTag) {
			n++
		}
	}
	return n > 0, n
}

// itoaB361 keeps the reason strings free of a strconv import in the hot path.
func itoaB361(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
