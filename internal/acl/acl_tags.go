// acl_tags.go — the tagOwners JSON for every declared dev tag.
//
// Split out of acl.go in refactor Phase D (2026-10-01). B288's invariant is
// that every tag a rule carries is DECLARED in the policy: an undeclared tag
// makes headscale reject the node that carries it. devTagsToDeclare is the
// query and devTagOwnerJSON is the emitter, so they belong together — and away
// from the two large generators that call them.

package acl

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"

	"skygate/internal/db"
)

func quoteAll(ss []string) []string {
	res := make([]string, len(ss))
	for i, s := range ss {
		res[i] = strconv.Quote(s)
	}
	return res
}

// ownerListJSON renders a headscale owner list for one `tagOwners` entry.
//
// B288 (2026-09-22): every writer of `tagOwners` now renders its owner list
// through this function and derives the owners from db.TagOwnersForUser, so the
// ACL generator, the ownership backfill and the tag reconciler cannot disagree
// (they used to, and each rewrite of the policy undid the other's entry).
func ownerListJSON(owners []string) string {
	return "[" + strings.Join(quoteAll(owners), ",") + "]"
}

// devTagOwnerJSON returns the `tagOwners` value for one tag:
//
//   - a per-device tag (`tag:dev-<user>-<host>`) is owned by the user its NAME
//     names plus the `tagged-devices` sentinel (db.TagOwnersForUser);
//   - anything else (a class tag, a legacy `tag:exit-<host>`) keeps the admin
//     identity, which is the pre-B288 behaviour.
func devTagOwnerJSON(tag, baseDomain string) (string, error) {
	if user, ok := db.PerDeviceTagUser(tag); ok {
		owners, err := db.TagOwnersForUser(user, baseDomain)
		if err != nil {
			return "", err
		}
		return ownerListJSON(owners), nil
	}
	return "[\"" + envAdminIdentity() + "@" + baseDomain + "\"]", nil
}

// devTagsToDeclare returns every per-device tag the policy must declare: the
// tags the caller already knows from its own sources (the portal-user JOIN, the
// per-device exit preferences) UNION every `tag:dev-<user>-<host>` recorded in
// `node_owner_map` — the ownership record B287 established.
//
// Why the union matters (B288, live on `aro`): the JOIN cannot see a row whose
// `username` is headscale's synthetic `tagged-devices` (every tagged node), and
// the grant-referenced sweep (B285) only covers tags a grant names — so a device
// whose dev tag nothing references (no exit rule, no preference) was declared in
// the live policy and MISSING from the generated one. Pressing "re-apply" would
// then strip the declaration the node actually wears. De-duplicated and sorted,
// so the document is stable for the byte-vs-byte drift comparison.
func devTagsToDeclare(d *sql.DB, known []string) ([]string, error) {
	set := make(map[string]bool, len(known)+4)
	for _, tag := range known {
		if t := strings.TrimSpace(tag); t != "" {
			set[t] = true
		}
	}
	recorded, err := db.ListDevTagsFromOwnerMap(d)
	if err != nil {
		return nil, err
	}
	for _, rec := range recorded {
		set[rec.Tag] = true
	}
	out := make([]string, 0, len(set))
	for tag := range set {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out, nil
}
