// devices_owner_b498.go — B498 (2026-11-19).
//
// TWO THINGS THE OPERATOR COULD SEE AND NEITHER OF WHICH WAS TRUE.
//
//  1. «olesya и a71 отображаются по-другому, не так как другие телефоны.» They did:
//     the page resolved a device's owner by splitting `tag:dev-<user>-<host>` at the
//     LAST hyphen and looking the left half up as a portal username. That works for
//     `tag:dev-michail-olesya` and `tag:dev-skyadmin-a71` and fails for every hostname
//     that itself contains a hyphen — live: `nothing-phone-2`, `skybars-secure`,
//     `s24-fe--ned` all resolved to uid 0 and rendered a dash. A dash also hid the
//     assign control, so the operator could not pin the very devices that looked
//     broken. `node_owner_map.username` carries the attribution directly and is
//     correct on every row; `tagged_by_user_id` is the fallback for a row whose
//     username is headscale's synthetic `tagged-devices` (B287).
//
//  2. «почему продолжает выставляться неверная настройка?» It never was that
//     device's setting. A per-user preference with strict mode on emits `via` on
//     `src: user@domain`, and headscale never matches a TAGGED node with a user
//     selector (B265) — and every skygate device is tagged. B497 printed the relay
//     name inside each device cell, where it read as «this device is pinned to
//     karolina». The truthful statement is a page-level one: this user's preference
//     reaches NONE of that user's N devices, because all of them carry tags.
//
// What is left in a device cell is only what the ACL really does with that device:
// a device pin with `via`, or «free choice».
package admin

import (
	"sort"
	"strings"

	"skygate/internal/db"
	"skygate/internal/exitpin"
	"skygate/internal/feature/exit_rules"
)

// b498OwnerUID resolves ONE ownership row to a portal user id.
//
// Order, and why:
//
//  1. `node_owner_map.username` — the row's own attribution, keyed to
//     `portal_users.username`. Correct on every live row, including the ones whose
//     HOSTNAME contains hyphens, which is exactly what tag-splitting got wrong.
//  2. `tagged_by_user_id` — the portal user of the human who applied the tag. It is
//     the only attribution left on a row whose username is headscale's synthetic
//     `tagged-devices` (B287), and it is used only while that id still names a
//     portal user, so a deleted account cannot resurrect an attribution.
//  3. nothing — and the caller renders a dash, which is honest.
func b498OwnerUID(row db.NodeOwner, uidByUsername map[string]int64, portalNameByUID map[int64]string) (int64, string) {
	if uid, ok := uidByUsername[strings.ToLower(strings.TrimSpace(row.Username))]; ok && uid > 0 {
		return uid, portalNameByUID[uid]
	}
	if row.TaggedByUserID > 0 {
		if name, known := portalNameByUID[row.TaggedByUserID]; known {
			return row.TaggedByUserID, name
		}
	}
	return 0, ""
}

// lostUserPref is one per-user preference that cannot reach that user's devices.
type lostUserPref struct {
	UserID   int64
	Username string
	Relay    string
	Devices  int
}

// b498LostUserPrefs lists the per-user preferences that govern nothing, in a stable
// order so the page does not reshuffle between loads.
//
// A row is reported only when it WOULD have pinned (strict mode on, a relay named)
// and NONE of the user's known devices is untagged — i.e. there is nothing left for
// the row to apply to. A user with no device on this page is not reported: that is
// an absence of evidence, not a finding.
func b498LostUserPrefs(
	userPrefs []db.ExitNodePref,
	userByHost map[string]int64,
	tagByHost map[string]string,
	nameByUID map[int64]string,
) []lostUserPref {
	devices := map[int64]int{}
	untagged := map[int64]int{}
	for host, uid := range userByHost {
		devices[uid]++
		if strings.TrimSpace(tagByHost[host]) == "" {
			untagged[uid]++
		}
	}
	out := make([]lostUserPref, 0, len(userPrefs))
	for _, ep := range userPrefs {
		pref := exitpin.FromUser(ep)
		if !exitpin.LostUserPref(&pref) {
			continue
		}
		n := devices[ep.UserID]
		if n == 0 || untagged[ep.UserID] > 0 {
			continue
		}
		name := strings.TrimSpace(ep.Username)
		if name == "" {
			name = nameByUID[ep.UserID]
		}
		if name == "" {
			continue
		}
		out = append(out, lostUserPref{
			UserID:   ep.UserID,
			Username: name,
			Relay:    exit_rules.TagToHostname(pref.Tag),
			Devices:  n,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}
