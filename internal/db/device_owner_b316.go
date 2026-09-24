// device_owner_B316.go — B316 (v1.5.80) — WHO owns a device must not depend on a
// username that headscale has already rewritten.
//
// LIVE CASE (native host `aro`, 2026-09-24): the operator's two machines did not ping
// each other although both are under user `daniil`:
//
//	node_owner_map:  2 workpc   tagged-devices   tag:dev-daniil-workpc
//	                 3 laptop  tagged-devices   tag:dev-daniil-laptop
//	                 6 homepc  daniil           tag:dev-daniil-homepc
//	device_rules:    workpc's rules carry user_name='daniil'
//	live policy:     0 grants of the form tag:dev-* → tag:dev-*
//
// The device-to-device mesh (writePerDeviceGrants) is built from tags grouped BY THE
// USERNAME COLUMN of node_owner_map — and headscale rewrites a node's user to the
// synthetic `tagged-devices` as soon as it wears ANY tag (B287, internal/db/device_tag.go).
// So `workpc`/`laptop` were invisible to the mesh, `daniil` was left with a single
// visible device, and `writePerDeviceGrants` skipped the user entirely
// (`len(userTags) < 2 → continue`). Nothing was logged and nothing appeared on any page:
// the two machines simply had no rule allowing them to reach each other, and headscale
// denies by default. The same code works on the agent VM only because every row there
// happens to carry a real portal username — i.e. the mesh depended on DATA that
// headscale may rewrite at any time.
//
// TWO HALVES, because either one alone is a trap:
//
//  1. RESOLVE (MeshDevices → DeviceTagsForMesh / MeshTagsByHost): when the ownership row
//     does not name a portal user, take the owner from the device's OWN tag
//     (`tag:dev-<user>-<host>`, B288's PerDeviceTagUser) and, failing that, from the user
//     its RULES were created under. Devices no source can attribute are RETURNED, so the
//     caller names them instead of silently emitting a policy with fewer grants than the
//     operator expects.
//  2. REPAIR (RepairSentinelDeviceOwners): rewrite those rows once, from the same tag, so
//     every consumer (the mesh, the panel's per-device column, tagOwners, the SSH rule
//     sources) sees a real owner again and the data stops contradicting itself.
//
// The repair is deliberately conservative: it touches a row only when its username is the
// synthetic sentinel (or empty) AND its per-device tag names an EXISTING portal user. It
// never guesses an owner it cannot prove and never edits a row that already names one.
package db

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"
)

// SentinelDeviceOwner is headscale's synthetic owner: every node carrying any tag is
// reassigned to this user, which has no portal account (B287).
const SentinelDeviceOwner = "tagged-devices"

// DeviceMeshTag is one device that CAN take part in the device-to-device mesh.
type DeviceMeshTag struct {
	NodeID   string
	Hostname string
	Tag      string
	Username string
	// Source is "owner_map" (the row named a portal user), "tag" (parsed out of the
	// device's own tag) or "rules" (the user its device_rules were created under).
	Source string
}

// DeviceMeshOwner is a device that CANNOT take part in the mesh: either no source could
// attribute it to a portal account, or it has an owner but no per-device tag to grant on.
type DeviceMeshOwner struct {
	NodeID   string
	Hostname string
	Tag      string
	// Username is whatever the ownership row said (often the synthetic sentinel).
	Username string
	// Reason names why it cannot join, for the log and the page.
	Reason string
}

// MeshDevices resolves every device named in node_owner_map to a portal owner and reports
// the devices that cannot be resolved. The owner comes from the ownership row, then the
// device's own tag, then the user its rules were created under — in that order, because
// the row is the operator's explicit statement and the tag/rules are evidence.
func MeshDevices(d *sql.DB) ([]DeviceMeshTag, []DeviceMeshOwner, error) {
	if d == nil {
		return nil, nil, nil
	}
	portal, err := portalUserIndex(d)
	if err != nil {
		return nil, nil, err
	}
	if len(portal) == 0 {
		return nil, nil, nil
	}
	ruleOwner, err := deviceRuleOwnerIndex(d)
	if err != nil {
		return nil, nil, err
	}
	rows, err := d.Query(`SELECT COALESCE(node_id, ''), COALESCE(hostname, ''),
	                             COALESCE(username, ''), COALESCE(tag, '')
	                        FROM node_owner_map`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var out []DeviceMeshTag
	var unresolved []DeviceMeshOwner
	seen := map[string]bool{}
	for rows.Next() {
		var nodeID, hostname, username, tag string
		if err := rows.Scan(&nodeID, &hostname, &username, &tag); err != nil {
			continue
		}
		nodeID = strings.TrimSpace(nodeID)
		hostname = strings.TrimSpace(hostname)
		username = strings.ToLower(strings.TrimSpace(username))
		tag = strings.ToLower(strings.TrimSpace(tag))

		// Only a PER-DEVICE tag can carry a mesh grant: `tag:private` and friends name a
		// class, not a device. B288's parser refuses anything that is not
		// `tag:dev-<user>-<host>`.
		_, perDevice := PerDeviceTagUser(tag)

		owner, source := "", ""
		switch {
		case username != "" && username != SentinelDeviceOwner && portalHas(portal, username):
			owner, source = username, "owner_map"
		case perDevice:
			if u, ok := PerDeviceTagUser(tag); ok && portalHas(portal, u) {
				owner, source = u, "tag"
			}
		}
		if owner == "" {
			if u := ruleOwnerForDevice(ruleOwner, nodeID, hostname); u != "" && portalHas(portal, u) {
				owner, source = u, "rules"
			}
		}
		if owner == "" {
			if perDevice {
				unresolved = append(unresolved, DeviceMeshOwner{
					NodeID: nodeID, Hostname: hostname, Tag: tag, Username: username,
					Reason: "its ownership row names " + fallbackText(username, "(nothing)") +
						" and its tag names no existing portal user",
				})
			}
			continue
		}
		if !perDevice {
			unresolved = append(unresolved, DeviceMeshOwner{
				NodeID: nodeID, Hostname: hostname, Tag: tag, Username: username,
				Reason: "no per-device tag is recorded for it, so the mesh has nothing to grant",
			})
			continue
		}
		key := owner + "\x00" + tag
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, DeviceMeshTag{
			NodeID: nodeID, Hostname: hostname, Tag: tag, Username: owner, Source: source,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Username != out[j].Username {
			return out[i].Username < out[j].Username
		}
		return out[i].Tag < out[j].Tag
	})
	sort.Slice(unresolved, func(i, j int) bool { return unresolved[i].Hostname < unresolved[j].Hostname })
	return out, unresolved, nil
}

// DeviceTagsForMesh groups every mesh-capable device's per-device tag by portal user —
// the shape the ACL's writePerDeviceGrants consumes. Keys are LOWERCASED portal
// usernames and every list is sorted, so the generated policy stays byte-stable.
func DeviceTagsForMesh(d *sql.DB) (map[string][]string, []DeviceMeshOwner, error) {
	byUser := map[string][]string{}
	devices, unresolved, err := MeshDevices(d)
	if err != nil {
		return byUser, unresolved, err
	}
	for _, dev := range devices {
		byUser[dev.Username] = append(byUser[dev.Username], dev.Tag)
	}
	for user := range byUser {
		sort.Strings(byUser[user])
	}
	return byUser, unresolved, nil
}

// MeshTagsByHost is the page-shaped projection: lowercased hostname → per-device tag for
// every device that can join the mesh, plus hostname → reason for the ones that cannot.
func MeshTagsByHost(d *sql.DB) (map[string]string, map[string]string, error) {
	byHost := map[string]string{}
	problems := map[string]string{}
	devices, unresolved, err := MeshDevices(d)
	if err != nil {
		return byHost, problems, err
	}
	for _, dev := range devices {
		if h := strings.ToLower(dev.Hostname); h != "" {
			byHost[h] = dev.Tag
		}
	}
	for _, u := range unresolved {
		if h := strings.ToLower(u.Hostname); h != "" {
			problems[h] = u.Reason
		}
	}
	return byHost, problems, nil
}

// RepairSentinelDeviceOwners re-attributes rows whose username is headscale's synthetic
// sentinel (or empty) from the device's own per-device tag.
//
// Returns the number of rows changed plus one human-readable line per change ("workpc:
// tagged-devices -> daniil (from tag:dev-daniil-workpc)"), which the caller logs. It only
// writes an owner it can prove: the tag must parse AND name an existing portal user.
func RepairSentinelDeviceOwners(d *sql.DB) (int, []string, error) {
	if d == nil {
		return 0, nil, nil
	}
	portal, err := portalUserIndex(d)
	if err != nil {
		return 0, nil, err
	}
	if len(portal) == 0 {
		return 0, nil, nil
	}
	rows, err := d.Query(`SELECT COALESCE(node_id, ''), COALESCE(hostname, ''),
	                             COALESCE(username, ''), COALESCE(tag, '')
	                        FROM node_owner_map
	                       WHERE LOWER(COALESCE(username, '')) IN ('', ?)`, SentinelDeviceOwner)
	if err != nil {
		return 0, nil, err
	}
	type change struct {
		nodeID, hostname, tag, from, to string
	}
	var pending []change
	for rows.Next() {
		var nodeID, hostname, username, tag string
		if err := rows.Scan(&nodeID, &hostname, &username, &tag); err != nil {
			continue
		}
		user, ok := PerDeviceTagUser(tag)
		if !ok || !portalHas(portal, user) {
			continue // cannot prove an owner: leave the row exactly as it is
		}
		pending = append(pending, change{
			nodeID:   strings.TrimSpace(nodeID),
			hostname: strings.TrimSpace(hostname),
			tag:      strings.ToLower(strings.TrimSpace(tag)),
			from:     strings.ToLower(strings.TrimSpace(username)),
			to:       user,
		})
	}
	rows.Close()

	applied, changes := 0, []string{}
	for _, ch := range pending {
		res, uerr := d.Exec(`UPDATE node_owner_map
		                        SET username = ?
		                      WHERE node_id = ?
		                        AND LOWER(COALESCE(username, '')) IN ('', ?)`,
			ch.to, ch.nodeID, SentinelDeviceOwner)
		if uerr != nil {
			return applied, changes, uerr
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		applied++
		changes = append(changes, ch.hostname+": "+fallbackText(ch.from, "(empty)")+
			" -> "+ch.to+" (from "+ch.tag+")")
	}
	return applied, changes, nil
}

// portalUserIndex is the set of lowercased portal usernames.
func portalUserIndex(d *sql.DB) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	rows, err := d.Query(`SELECT LOWER(COALESCE(username, '')) FROM portal_users`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			continue
		}
		if u = strings.TrimSpace(u); u != "" {
			out[u] = struct{}{}
		}
	}
	return out, nil
}

// portalHas reports whether a username is a portal account.
func portalHas(idx map[string]struct{}, name string) bool {
	_, ok := idx[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// deviceRuleOwnerIndex maps a device (by device_id and by lowercased hostname) to the
// user its rules were created under — the third source, and the only one aro's
// workpc/laptop had before this block.
func deviceRuleOwnerIndex(d *sql.DB) (map[string]string, error) {
	out := map[string]string{}
	rows, err := d.Query(`SELECT COALESCE(device_id, 0), LOWER(COALESCE(device_hostname, '')),
	                             LOWER(COALESCE(user_name, ''))
	                        FROM device_rules
	                       WHERE COALESCE(user_name, '') <> ''`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var deviceID int64
		var hostname, userName string
		if err := rows.Scan(&deviceID, &hostname, &userName); err != nil {
			continue
		}
		hostname = strings.TrimSpace(hostname)
		userName = strings.TrimSpace(userName)
		if userName == "" || userName == SentinelDeviceOwner {
			continue
		}
		if deviceID > 0 {
			key := "id:" + strconv.FormatInt(deviceID, 10)
			if _, ok := out[key]; !ok {
				out[key] = userName
			}
		}
		if hostname != "" {
			key := "h:" + hostname
			if _, ok := out[key]; !ok {
				out[key] = userName
			}
		}
	}
	return out, nil
}

func ruleOwnerForDevice(idx map[string]string, nodeID, hostname string) string {
	if nodeID != "" {
		if u, ok := idx["id:"+nodeID]; ok {
			return u
		}
	}
	if hostname != "" {
		if u, ok := idx["h:"+strings.ToLower(hostname)]; ok {
			return u
		}
	}
	return ""
}

func fallbackText(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}
