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
// 2026-09-22: B287.
package db

import (
	"database/sql"
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
