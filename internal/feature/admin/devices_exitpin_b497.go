// devices_exitpin_b497.go — B497 (2026-11-19).
//
// THE PAGE MUST ANSWER THE QUESTION IT ASKS.
//
// `/admin/devices` has always rendered ONE thing in the exit-node column: the row
// `device_exit_node_prefs` holds for (portal user, hostname). Everything the ACL
// generator actually decides was invisible, so three different situations looked
// identical on screen:
//
//  1. a real pin — the row is `via_enabled = 1`, was saved by a human, the node
//     carries a tag and the relay is healthy: the policy emits
//     `{ src: [tag:dev-…], dst: [autogroup:internet], via: [<tag>] }`;
//  2. a STORED preference the policy ignores — `via_enabled = 0`, a derived row
//     (`set_by_user_id = 0`, B398), a node without a tag (B284), or an unhealthy
//     relay (B356/B361): the policy emits the loose, UNPINNED grant and the device
//     may use ANY relay;
//  3. nothing at all — an empty cell, which on this page rendered a `<select>`
//     whose first `<option>` is a relay name. Read as «привязано к emilia» while
//     the ACL said «free choice».
//
// The operator reported exactly that: «где-то настройки цепляются и дают сбой»,
// with two phones that work (`skybars`, `nothing-phone-2`) and two that do not
// (`olesya`, `A71`) differing only in the ACL, never in the panel.
//
// This file is the glue: it resolves each device's OWN owner, looks up both
// preference tables for that owner only, hands the facts to `exitpin` and returns
// the answer keyed by hostname. The rules live in the `exitpin` package so the
// same table drives the tests; only the reads live here.
package admin

import (
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/exitpin"
	"skygate/internal/feature/exit_rules"
)

// b497PrefKey is the ONE spelling of the (portal user, device) preference key.
//
// It exists because the handler used to build this key inline in two places, and
// a device's hostname arrives from headscale (mixed case) while node_owner_map
// stores it lowercased. Two spellings of the same key is how a stored preference
// stops being found — and how the dead-rule count and the pin badge could
// disagree about the same device.
func b497PrefKey(userID int64, hostname string) string {
	return strconv.FormatInt(userID, 10) + ":" + strings.ToLower(strings.TrimSpace(hostname))
}

// b497Pins computes the effective exit-node decision for every device, keyed by
// LOWERCASED hostname.
//
//	owners       lowercased hostname → portal user id of the device's owner
//	devTags      lowercased hostname → the tag the node carries ("" = untagged)
//	devicePrefs  b497PrefKey(owner, hostname) → the per-device row
//	userPrefs    portal user id → the per-user row
//	relayStates  lowercased relay hostname → the exit-node monitor's last state
//
// A device whose owner cannot be resolved gets NO preference lookup at all: the
// old code indexed per-device rows by hostname alone, so one user's preference
// for `laptop` silently became another user's answer for a different `laptop`
// with the same name. Guessing an owner is worse than saying «не знаю».
func b497Pins(
	owners map[string]int64,
	devTags map[string]string,
	devicePrefs map[string]db.DeviceExitNodePref,
	userPrefs map[int64]db.ExitNodePref,
	relayStates map[string]string,
) map[string]exitpin.Pin {
	out := make(map[string]exitpin.Pin, len(owners))
	for host, uid := range owners {
		relay := func(tag string) string {
			return strings.ToLower(strings.TrimSpace(exit_rules.TagToHostname(tag)))
		}
		in := exitpin.Inputs{
			DeviceTag: strings.TrimSpace(devTags[host]),
			Tagged:    strings.TrimSpace(devTags[host]) != "",
		}
		if dp, ok := devicePrefs[b497PrefKey(uid, host)]; ok {
			p := exitpin.FromDevice(dp)
			in.Device = &p
			in.DeviceRelayState = relayStates[relay(p.Tag)]
		}
		if up, ok := userPrefs[uid]; ok {
			p := exitpin.FromUser(up)
			in.User = &p
			in.UserRelayState = relayStates[relay(p.Tag)]
		}
		out[host] = exitpin.Effective(in)
	}
	return out
}

// b497PrefByHostname is the relay a rule is compared against to decide whether it
// is «dead» — the device's STORED preference, whether or not the policy uses it.
//
// That is deliberate: «this rule points at a relay the device is not pinned to» is
// a statement about the configuration, and it stays true when the pin is withheld
// (B356/B361) — the badge next to the count says whether the pin applies. What the
// map must NOT do is what the pre-B497 code did: build itself from
// `device_exit_node_prefs` keyed by hostname ALONE, so one user's preference for
// `laptop` was applied to another user's different `laptop`, and a rule could be
// reported dead against a preference its owner never made.
func b497PrefByHostname(pins map[string]exitpin.Pin) map[string]string {
	out := make(map[string]string, len(pins))
	for host, pin := range pins {
		if pin.Tag == "" {
			continue
		}
		out[host] = exit_rules.TagToHostname(pin.Tag)
	}
	return out
}
