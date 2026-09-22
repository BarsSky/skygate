// device_tag.go — B287 (2026-09-22) — the per-device tag is an OWNERSHIP
// record, so any page that asks "is this device mine?" must read it.
//
// Live case (native host `aro`): every device in `node_owner_map` carried
// headscale's synthetic owner in its `username` column —
//
//	id  hostname        username         tag
//	1   exit-node-vps   tagged-devices   tag:dev-infra-exit-node-vps
//	2   workpc          tagged-devices   tag:dev-daniil-workpc
//	3   laptop          tagged-devices   tag:dev-daniil-laptop
//
// headscale reassigns a node to the synthetic `tagged-devices` user as soon as
// it carries any tag, and the DB snapshot copied that name verbatim. The two
// places that decide ownership both keyed on that column:
//
//   - /my/devices matched the live node user (`n.UserName == username`) and the
//     snapshot rows BY USERNAME, so every tagged device was invisible to its
//     owner — the page rendered empty while /admin/devices listed the devices
//     (with the meaningless owner `tagged-devices`);
//   - the ACL tagOwners source (`GetPerUserDeviceTags`) is a username JOIN, so
//     it saw no tags at all (that gap is what B285's sweep works around).
//
// The tag does name the owner: `tag:dev-<user>-<host>` is minted per user and
// headscale only accepts it once that user (or `tagged-devices`) owns it. These
// helpers make that the single place the question is answered.
//
// 2026-09-22: B287, extended by B288 with the OWNERSHIP side of the same idea:
// `PerDeviceTagUser` (parse the user out of the tag), `TagOwnersForUser` (the
// owner pair every writer must emit) and `ListDevTagsFromOwnerMap` (every
// per-device tag `node_owner_map` records, for the ACL's tagOwners block).
//
// 2026-09-22: B287.
package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// PerDeviceTag returns the per-user device tag for a portal user and hostname:
//
//	PerDeviceTag("Daniil", "WorkPC") -> "tag:dev-daniil-workpc"
//
// Both halves are lowercased because headscale 0.29 rejects uppercase tags
// (B176) and every other mint site lowercases too. Returns "" when either half
// is empty — an empty half would produce a tag that names nothing.
func PerDeviceTag(username, hostname string) string {
	u := strings.ToLower(strings.TrimSpace(username))
	h := strings.ToLower(strings.TrimSpace(hostname))
	if u == "" || h == "" {
		return ""
	}
	return "tag:dev-" + u + "-" + h
}

// TagNamesUser reports whether a per-device tag names this portal user:
//
//	TagNamesUser("tag:dev-daniil-workpc", "daniil") -> true
//	TagNamesUser("tag:dev-daniil-workpc", "dan")    -> false (segment, not prefix)
//	TagNamesUser("tag:dev-infra-emilia", "infra")   -> false (infra convention)
//	TagNamesUser("tag:exit-node", "daniil")         -> false (class tag)
//
// The user segment is matched as `tag:dev-<user>-` INCLUDING the separator dash,
// so a shorter username cannot claim a longer one's devices, and hostnames may
// contain dashes freely. Infra tags (`tag:dev-infra-<host>`) are deliberately
// excluded: they name a role, not a portal account, and the legacy
// `tag:exit-<host>` form carries no user at all.
func TagNamesUser(tag, username string) bool {
	t := strings.ToLower(strings.TrimSpace(tag))
	u := strings.ToLower(strings.TrimSpace(username))
	if t == "" || u == "" || IsClassTag(t) {
		return false
	}
	if strings.HasPrefix(t, "tag:dev-infra-") || strings.HasPrefix(t, "tag:exit-") {
		return false
	}
	prefix := "tag:dev-" + u + "-"
	return strings.HasPrefix(t, prefix) && len(t) > len(prefix)
}

// HasPerDeviceTag reports whether a live headscale tag list carries the
// per-device tag minted for (username, hostname). Case-insensitive: headscale
// normalises tags, but a hand-edited value or an older row may differ.
func HasPerDeviceTag(tags []string, username, hostname string) bool {
	want := PerDeviceTag(username, hostname)
	if want == "" {
		return false
	}
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), want) {
			return true
		}
	}
	return false
}

// PerDeviceTagUser parses the portal user a per-device tag names:
//
//	PerDeviceTagUser("tag:dev-daniil-workpc")            -> "daniil", true
//	PerDeviceTagUser("tag:dev-infra-exit-node-vps")      -> "infra",  true
//	PerDeviceTagUser("tag:private")                      -> "",       false
//	PerDeviceTagUser("tag:dev-workpc")                   -> "",       false
//
// The user is the FIRST dash segment after `tag:dev-`, which is the same rule
// the tag-minting sites use, and hostnames may contain dashes freely. A tag
// with no `<user>-<host>` split names nobody and is refused, so callers can
// fall back explicitly instead of inventing an owner.
//
// B288 (2026-09-22): this is the single parser behind the tag-ownership
// derivation, so the ACL generator, the ownership backfill and the tag
// reconciler can never disagree about who owns `tag:dev-<user>-<host>`.
func PerDeviceTagUser(tag string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(tag))
	const prefix = "tag:dev-"
	if !strings.HasPrefix(t, prefix) {
		return "", false
	}
	rest := t[len(prefix):]
	idx := strings.Index(rest, "-")
	if idx <= 0 || idx == len(rest)-1 {
		return "", false
	}
	return rest[:idx], true
}

// TagOwnersForUser returns the headscale identities that must own a per-device
// tag minted for this user:
//
//	TagOwnersForUser("daniil", "ts.example.com")
//	  -> ["daniil@ts.example.com", "tagged-devices@ts.example.com"]
//	TagOwnersForUser("tagged-devices", "ts.example.com")
//	  -> ["tagged-devices@ts.example.com"]
//
// Why the sentinel is always a co-owner: headscale reassigns a node to its
// synthetic `tagged-devices` user as soon as it wears ANY tag (B287), and a tag
// can only be (re)applied by a user that owns the node — so without
// `tagged-devices@<baseDomain>` in the owner list, re-applying the tag to an
// already-tagged device fails with `400 requested tags [...] are invalid or
// not permitted` (the B272.7/v1.5.14 incident).
//
// B288 (2026-09-22): before this helper there were THREE derivations — the ACL
// generator emitted `<user>@` alone, the ownership backfill emitted
// `<portal-user>@ + tagged-devices@`, and the tag reconciler emitted
// `<row-username>@ + tagged-devices@` (where the row username is the synthetic
// owner for every tagged node). They disagreed, so every ACL apply stripped the
// sentinel owner and the next tag apply failed; and since the drift banner
// compares the two documents, the operator saw a permanent "политика
// УСТАРЕЛА". One derivation, one owner set.
//
// An empty base domain is an error, never a silent skip: without it the tag can
// never become permitted, and silence is what made this class invisible.
func TagOwnersForUser(username, baseDomain string) ([]string, error) {
	base := strings.TrimSpace(baseDomain)
	if base == "" {
		return nil, fmt.Errorf("base domain is empty, so the owner of a per-device tag cannot be expressed in the policy (set SKYGATE_BASE_DOMAIN)")
	}
	u := strings.ToLower(strings.TrimSpace(username))
	if u == "" {
		u = "tagged-devices"
	}
	owners := []string{u + "@" + base}
	if u != "tagged-devices" {
		owners = append(owners, "tagged-devices@"+base)
	}
	return owners, nil
}

// DevTagOwner is one per-device tag recorded in `node_owner_map`, with the
// portal user its NAME attributes it to (never the row's `username` column,
// which headscale sets to the synthetic `tagged-devices` for a tagged node —
// B287).
type DevTagOwner struct {
	Tag      string
	Username string
}

// ListDevTagsFromOwnerMap returns every per-device tag in `node_owner_map`
// (`tag:dev-<user>-<host>`), de-duplicated and sorted.
//
// Why the generator needs it: `node_owner_map.tag` is the ownership record — it
// holds the tag the node ACTUALLY wears, and its `username` column may be
// headscale's synthetic owner, so the portal-user JOIN (`GetPerUserDeviceTags`)
// cannot see those rows at all. Without this list the generated policy simply
// did not declare such a tag, and every apply REMOVED the declaration a node
// needs (live on `aro`: `tag:dev-daniil-homepc`/`tag:dev-daniil-laptop` were
// declared in the live policy and would have been dropped).
func ListDevTagsFromOwnerMap(d *sql.DB) ([]DevTagOwner, error) {
	if d == nil {
		return nil, nil
	}
	rows, err := d.Query(
		`SELECT DISTINCT LOWER(COALESCE(tag, '')) FROM node_owner_map
		  WHERE LOWER(COALESCE(tag, '')) LIKE 'tag:dev-%'
		  ORDER BY 1`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DevTagOwner
	seen := map[string]bool{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		user, ok := PerDeviceTagUser(tag)
		if !ok {
			// A `tag:dev-*` value the parser refuses names nobody; declaring it
			// with an invented owner would be worse than leaving it to the
			// grant-referenced sweep.
			continue
		}
		seen[tag] = true
		out = append(out, DevTagOwner{Tag: tag, Username: user})
	}
	return out, rows.Err()
}

// ListNodeOwnerNodeIDsByUserTag returns the `node_owner_map.node_id` values
// whose snapshot tag names this user (`tag:dev-<user>-<host>`).
//
// Why it exists: the row's `username` column can hold headscale's synthetic
// `tagged-devices` for a tagged node (see the file header), so a lookup BY
// USERNAME finds nothing while the tag still names the real owner. This is the
// snapshot-side twin of HasPerDeviceTag, used by /my/devices to treat the tag as
// the ownership record.
//
// The LIKE pattern escapes `%`, `_` and `\` so a username containing a wildcard
// character cannot match another user's tags.
func ListNodeOwnerNodeIDsByUserTag(d *sql.DB, username string) ([]string, error) {
	u := strings.ToLower(strings.TrimSpace(username))
	if d == nil || u == "" {
		return nil, nil
	}
	pattern := escapeLike("tag:dev-"+u+"-") + "%"
	rows, err := d.Query(
		`SELECT node_id FROM node_owner_map WHERE LOWER(COALESCE(tag, '')) LIKE $1 ESCAPE '\'`,
		pattern,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if strings.TrimSpace(id) != "" {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// escapeLike escapes the LIKE metacharacters so the value is matched literally
// (the caller appends its own trailing '%').
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
