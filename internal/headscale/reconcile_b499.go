// reconcile_b499.go — B499 (2026-11-19), the D half.
//
// «УБЕДИТЬСЯ, ЧТО ДУБЛИ SKYADMIN НЕ ПОЯВЯТСЯ СНОВА» — the operator's request, and the
// live history behind it. skygate wrote this audit row hourly for weeks:
//
//	headscale_user_reconcile  {"outcome":"duplicate_name","old_hs_id":1,
//	                           "candidate_hs_id":"88","action":"operator_must_pick",
//	                           "hint":"delete the OIDC-created duplicate via
//	                           'docker exec headscale headscale users delete -i <oidc-id> --force' …"}
//
// Three things were wrong with that, and B243's refusal to pick silently was right
// about only the first of them:
//
//  1. PICKING THE LAST ENTRY was a bug (the OIDC duplicate won over the bootstrap
//     admin, and 7 devices lost their owner). B243 stopped it — correctly.
//  2. THE HINT NAMED A PLACEHOLDER. `<oidc-id>` is not an id; the operator has to
//     reconstruct it from `headscale users list`, which is why the row repeated for
//     weeks instead of being resolved once.
//  3. NOTHING RESOLVED THE UNAMBIGUOUS CASE. When one candidate is referenced by the
//     portal table and the others are referenced by nobody, there is exactly one right
//     answer, and refusing to write it is not caution — it is a deadlock the operator
//     can only break by hand.
//
// So B499 keeps the refusal for the AMBIGUOUS case (two candidates both referenced, or
// no candidate referenced at all) and resolves the unambiguous one LOUDLY: an audit row
// naming every candidate id, the chosen one, and the exact delete commands for the
// rest. The choice rule is the OPPOSITE of the pre-B243 bug: the OLDEST headscale user
// wins (the bootstrap admin was id=1 and the duplicate was id=88), and only when every
// other candidate is referenced by no portal row.
package headscale

import (
	"sort"
	"strconv"
	"strings"
)

// buildUserIndex returns BOTH views the reconciler needs:
//
//	byName — lowercased name → one user (kept for the existing call sites)
//	all    — lowercased name → every user with that name, ordered by numeric id
//
// `byName` keeps `buildByNameWithDuplicates`' last-write-wins semantics for the
// healthy path; `all` is what decides a duplicate, so no information is lost.
func buildUserIndex(hsUsers []HSUser) (map[string]HSUser, map[string][]HSUser) {
	byName := make(map[string]HSUser, len(hsUsers))
	all := make(map[string][]HSUser, len(hsUsers))
	for _, u := range hsUsers {
		key := strings.ToLower(strings.TrimSpace(u.Name))
		byName[key] = u
		all[key] = append(all[key], u)
	}
	for k := range all {
		sort.SliceStable(all[k], func(i, j int) bool {
			return int64FromString(all[k][i].ID) < int64FromString(all[k][j].ID)
		})
	}
	return byName, all
}

// b499PickCanonical decides the unambiguous duplicate and reports the ids that can be
// deleted. `referenced` is the set of headscale ids any portal_users row points at.
//
// Rules, in order:
//
//   - fewer than two candidates → not a duplicate, nothing to decide;
//   - exactly one candidate is referenced → that one is canonical (the portal table
//     already says which user is the real one), the rest are extras;
//   - no candidate is referenced → the OLDEST is canonical **only if** every other
//     candidate is older-id-free? no: the oldest is canonical, the rest are extras —
//     this is the bootstrap-admin case (id 1 vs the OIDC duplicate);
//   - two or more candidates are referenced → AMBIGUOUS: refuse (ok=false). Each of
//     them belongs to a portal user; picking would break one of them.
//
// The caller must audit whatever it does: this function is a decision, not a record.
func b499PickCanonical(candidates []HSUser, referenced map[int64]bool) (HSUser, []int64, bool) {
	if len(candidates) < 2 {
		return HSUser{}, nil, false
	}
	// Sort defensively: the decision point must not depend on the caller having used
	// buildUserIndex. The order is the whole basis of the "oldest wins" rule.
	ordered := make([]HSUser, len(candidates))
	copy(ordered, candidates)
	sort.SliceStable(ordered, func(i, j int) bool {
		return int64FromString(ordered[i].ID) < int64FromString(ordered[j].ID)
	})
	var chosen HSUser
	var extras []int64
	referencedCount := 0
	for _, u := range ordered {
		if referenced[int64FromString(u.ID)] {
			referencedCount++
		}
	}
	switch {
	case referencedCount == 1:
		for _, u := range ordered {
			if referenced[int64FromString(u.ID)] {
				chosen = u
				continue
			}
			extras = append(extras, int64FromString(u.ID))
		}
	case referencedCount == 0:
		// ordered[0] is the oldest, i.e. the bootstrap/original user.
		chosen = ordered[0]
		for _, u := range ordered[1:] {
			extras = append(extras, int64FromString(u.ID))
		}
	default:
		// two or more portal users point at different duplicates — refusing is the
		// only answer that does not break one of them.
		return HSUser{}, nil, false
	}
	if int64FromString(chosen.ID) <= 0 {
		return HSUser{}, nil, false
	}
	return chosen, extras, true
}

// b499DeleteHint renders the exact command(s) the operator runs, with REAL ids. The
// pre-B499 text said `<oidc-id>`, which is why the same audit row kept repeating.
func b499DeleteHint(extras []int64) string {
	if len(extras) == 0 {
		return ""
	}
	parts := make([]string, 0, len(extras))
	for _, id := range extras {
		parts = append(parts, "headscale users delete -i "+strconv.FormatInt(id, 10)+" --force")
	}
	return "delete the duplicate(s): " + strings.Join(parts, " ; ")
}

// b499IDList renders ids for an audit row (["1","88"]).
func b499IDList(us []HSUser) string {
	parts := make([]string, 0, len(us))
	for _, u := range us {
		parts = append(parts, u.ID)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// b499HasID reports whether the candidate list contains this headscale id. Used to
// make the "is this portal row's link still valid?" test EXACT: the pre-B499 code only
// scanned the deduplicated byName map, so with a duplicate present it could call a
// perfectly valid link "stale" and start the relink machinery.
func b499HasID(cands []HSUser, id int64) bool {
	if id <= 0 {
		return false
	}
	for _, u := range cands {
		if int64FromString(u.ID) == id {
			return true
		}
	}
	return false
}

// b499JoinIDs renders the duplicate ids as a JSON array body (88,90).
func b499JoinIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}
