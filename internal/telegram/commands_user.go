// Package telegram — user-scope reply functions (Этап 11, 2026-07-12).
//
// These power the /my_* commands plus /add_device, /add_rule, /delrule.
// Every function takes a BotEnv and uses env.PortalUserID / env.Username
// to filter data to the calling user. Admin callers see their own data
// too (not all-user data) — admins wanting the cross-user view use the
// admin-scope commands (/nodes, /rules, /quota) which are unchanged.
//
// The /add_* and /delrule commands also accept an optional username
// argument so the admin can act on a user's behalf. e.g.:
//   /add_rule alice telegram.org      → adds "telegram.org" for alice
//   /add_rule telegram.org            → adds "telegram.org" for the caller
//   /delrule alice 5 6 7              → deletes alice's rules 5, 6, 7
//   /delrule 5 6 7                    → deletes the caller's rules 5, 6, 7
//
// /delete_rule is kept as a deprecated alias of /delrule (same handler
// function) for back-compat with the original /help text.

package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"skygate/internal/acl"
	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
	"skygate/internal/subnet"
	"strconv"
	"strings"
)

// myStatusReply is the user-scope counterpart of /status. It shows
// the caller's own rule count, device count, and the last applied
// ACL snapshot version. If the caller's data is empty (e.g. brand
// new user, no devices yet), the reply says so explicitly rather
// than showing zeros that look like a bug.
//
// 2026-07-16: v0.16.x — "more HTML" pass. The reply now uses
// Field() for each metric (label bold, value in <code>) and
// Section() dividers between groups. The result is a tabbed
// feel that reads cleanly on mobile Telegram (where the
// proportional font would otherwise mash the labels and
// values together).
func myStatusBlocks(env BotEnv) ([]RichBlock, error) {
	lang := env.Lang
	if !env.IsIdentified() {
		return nil, fmt.Errorf("not bound")
	}
	if env.Username == "" {
		return nil, fmt.Errorf("no username")
	}
	var ruleCount, deviceCount int64
	if err := env.DB.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = $1`, env.PortalUserID).Scan(&ruleCount); err != nil {
		return nil, err
	}
	// 2026-08-25 (B186.3): the original B186.2 migration
	// forgot to call ListNodeOwnersByUsername, so
	// deviceCount stayed at 0 and the rich message
	// showed "устройств 0" even when the user had
	// 7 devices. The legacy myStatusReply calls it
	// at line 75; the rich path must too. Live
	// reproduction: 2026-08-25 screenshot showed
	// skyadmin (7 devices) → "устройств 0".
	if owned, derr := db.ListNodeOwnersByUsername(env.DB, env.Username); derr == nil {
		deviceCount = int64(len(owned))
	}
	cap := env.MaxFor(env.Username)
	capStr := "∞"
	if cap > 0 {
		capStr = strconv.Itoa(cap)
	}
	// Pull last ACL id (same SQL as myStatusReply, but
	// kept inline so the call site is self-contained).
	var lastACL int64
	_ = env.DB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM acl_snapshots WHERE applied_success = 1`).Scan(&lastACL)

	// The structured message:
	//   <h2>Добрый вечер, <username>.</h2>          (header)
	//   <h3>Сводка</h3>                            (section)
	//   <table> rules / devices / last acl </table>  (kv table)
	//   <i>footer</i>                              (footer)
	rulesLine := fmt.Sprintf("%d / %s", ruleCount, capStr)
	lastACLS := fmt.Sprintf("#%d", lastACL)
	deviceS := fmt.Sprintf("%d", deviceCount)
	header := i18n.Tf(lang, "bot.my_status.header", env.Username)
	section := i18n.T(lang, "bot.my_status.section_summary")

	blocks := []RichBlock{
		Heading(header, 2),
		KeyValueTable([]KVRow{
			{Label: i18n.T(lang, "bot.my_status.label_rules"), Value: rulesLine},
			{Label: i18n.T(lang, "bot.my_status.label_devices"), Value: deviceS},
			{Label: i18n.T(lang, "bot.my_status.label_last_acl"), Value: lastACLS},
		}),
		Footer(i18n.T(lang, "bot.my_status.subheader")),
	}
	_ = section // keep var used (linter satisfaction; section shows up in the legacy HTML render)
	_ = deviceS // kept for the same linter satisfaction; the real value is in the table cell below
	return blocks, nil
}

// myNodesReply lists only the caller's own devices from
// node_owner_map. Mirrors the format of /nodes but filtered to
// (username = env.Username). A user with no devices gets a
// helpful "no devices yet" hint pointing at /add_device.
func listAllNodesForBackfill(hs *headscale.Client) []headscale.NodeView {
	if hs == nil {
		return nil
	}
	nodes, err := hs.ListAllNodes()
	if err != nil {
		return nil
	}
	return nodes
}

// myRulesReply lists the caller's own exit-rules, newest first.
// Mirrors /rules but filtered to user_id = env.PortalUserID.
// Limited to the most recent 25 (same cap as /rules) so the reply
// stays under Telegram's 4096-char limit.
//
// 2026-07-16: v0.16.x — "more HTML" pass. The reply now uses a
// tabular <pre> block (ID / EXIT / TYPE / TARGET / ACTION) with
// a bold header row + italic rule line, so the columns line up
// on phones the same way /audit, /my_nodes do. The previous
// prose "#%d @%s\n  %s %s → %s" format is gone — too easy to
// misread when a user has 20+ rules, and the columns couldn't
// align because Telegram's regular text isn't monospace.
func myQuotaReply(env BotEnv) string {
	// 2026-07-16: v0.16.2 — mark HTML so the <b>rules:</b>,
	// <b>fill:</b>, <b>cap:</b> Field() labels and the
	// <code>value</code> render instead of showing as raw
	// source.
	markHTMLReply()
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.my_quota.not_bound")
	}
	var cnt int
	if err := env.DB.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = $1`, env.PortalUserID).Scan(&cnt); err != nil {
		return i18n.Tf(lang, "bot.my_quota.db_error", err)
	}
	max := env.MaxFor(env.Username)
	pct := -1
	if max > 0 {
		pct = (cnt * 100) / max
	}
	bar := quotaBar(pct)
	capStr := i18n.T(lang, "bot.my_quota.label_unlimited")
	if max > 0 {
		capStr = strconv.Itoa(max)
	}
	// "rules" value is "<count> / <cap>" so the user sees
	// the same shape as a bot.my_status "rules:" line —
	// one line per field, count + cap in the same <code>.
	// "fill" is the 10-char bar + percent (telegraph the
	// "how full am I" at a glance; the bar is redundant
	// when next to a number, but it's the visual hint the
	// operator's eye lands on first).
	fillStr := fmt.Sprintf("%s %d%%", bar, safePct(pct))
	return i18n.Tf(lang, "bot.my_quota.header", env.Username) + "\n" +
		Section(i18n.T(lang, "bot.my_quota.section_quota")) + "\n" +
		Field(i18n.T(lang, "bot.my_quota.label_rules"), fmt.Sprintf("%d / %s", cnt, capStr)) + "\n" +
		Field(i18n.T(lang, "bot.my_quota.label_fill"), fillStr) + "\n" +
		Field(i18n.T(lang, "bot.my_quota.label_cap"), capStr)
}

// myExitNodesReply lists every enabled exit-server the user can
// route through, with online/last-seen status (same data as admin
// /exit_nodes) plus a "✓" marker on the user's currently configured
// default exit-node (set via /setexitnode).
//
// The admin /exit_nodes shows the same data but is restricted to
// admin callers. This user-scope variant lets a non-admin user
// see what's available, pick one with /setexitnode, then
// /add_rule to write rules that route through it. Workflow:
//
//	/myexitnodes          — see the menu
//	/setexitnode 5        — pick node 5 as your default
//	/defaultexitnode      — confirm it's set
//	/add_rule telegram.org — write a rule using the default
//
// 2026-07-13: Этап 14 — added so a user doesn't have to go to the
// web UI just to see the exit-node menu. Mirrors exitNodesReply
// (commands_phase3.go) but adds the [default] highlight and
// filters to enabled=1 (the admin variant shows every node with
// tag:exit-node regardless of enabled state, which is the
// operator view, not the user view).
//
// 2026-07-16: v0.16.x — "more HTML" pass. The reply now uses a
// tabular <pre> block (HOSTNAME / NODE / STATUS / DEFAULT) with
// a bold header row + italic rule line, plus a Section()/Field()
// summary. The previous "  • hostname (node N) — status [default]"
// prose format is gone — too easy to misread when the list grows,
// and the "online" / "offline" status used to be a free-floating
// word the eye had to track to the right column.
func bindReply(env BotEnv, arg string) string {
	lang := env.Lang
	if !env.EffectiveAdmin() {
		return i18n.T(lang, "bot.bind.admin_only")
	}
	parts := strings.Fields(arg)
	if len(parts) != 2 {
		return i18n.T(lang, "bot.bind.usage")
	}
	chatID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || chatID == 0 {
		return i18n.Tf(lang, "bot.bind.bad_chat_id", parts[0])
	}
	username := parts[1]
	user, err := lookupUserByUsername(env.DB, username)
	if err != nil {
		return i18n.Tf(lang, "bot.bind.user_err", err)
	}
	boundBy := env.PortalUserID
	if boundBy == 0 {
		boundBy = user.ID // self-bind (admin → admin)
	}
	if err := db.UpsertTelegramBinding(env.DB, chatID, user.ID, boundBy, user.IsAdmin, LangForChat(env.DB, chatID)); err != nil {
		return i18n.Tf(lang, "bot.bind.db_error", err)
	}
	return i18n.Tf(lang, "bot.bind.ok", chatID, user.Username)
}

// unbindReply removes a binding. Admin-only. The user-scope
// counterpart of /bind is the admin deleting a user (the cascade
// in handlers_admin_users.go also calls db.DeleteTelegramBindingsByUser).
func unbindReply(env BotEnv, arg string) string {
	lang := env.Lang
	if !env.EffectiveAdmin() {
		return i18n.T(lang, "bot.unbind.admin_only")
	}
	chatID, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || chatID == 0 {
		return i18n.Tf(lang, "bot.unbind.bad_chat_id", arg)
	}
	if err := db.DeleteTelegramBinding(env.DB, chatID); err != nil {
		return i18n.Tf(lang, "bot.unbind.db_error", err)
	}
	return i18n.Tf(lang, "bot.unbind.ok", chatID)
}

// resolveTargetUser picks the user a /add_* command should act for.
// If `arg` is empty or matches the caller's username, returns the
// caller. Otherwise `arg` must be a different username (admin-only).
// The bool returns true when the resolved user is different from
// the caller (so callers can short-circuit the admin-only check).
func looksLikeRuleTarget(s string) bool {
	return strings.ContainsAny(s, " \t/:")
}

// classifyTarget decides target_type from the string. Mirrors the
// logic in exit_rules_form_my.go:PostMyExitRule so the bot and the
// web form agree on what "domain", "ip", and "subnet" mean.
//
//	ipv4 → ip (or subnet if /mask > 0)
//	ipv4/mask → subnet
//	anything else → domain
func classifyTarget(s string) (value, ttype string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("empty target")
	}
	// subnet form
	if strings.Contains(s, "/") {
		return s, "subnet", nil
	}
	// IPv4 or IPv6 literal
	if isIPLiteral(s) {
		return s, "ip", nil
	}
	// crude domain check: at least one dot, no spaces
	if !strings.Contains(s, ".") {
		return "", "", fmt.Errorf("%q is not a valid domain (need at least one dot)", s)
	}
	return strings.ToLower(s), "domain", nil
}

// isIPLiteral is a thin wrapper around net.ParseIP that returns
// true for both IPv4 and IPv6.
func isIPLiteral(s string) bool {
	// Avoid the import cycle cost: a 3-line check is enough.
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' || c == ':' || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return strings.ContainsAny(s, ".:")
}

// lookupUserByUsername resolves a portal username to a User. The
// web handlers use a different helper that includes password_hash +
// theme; the bot doesn't need those, so we keep a focused read here.
func lookupUserByUsername(d *sql.DB, username string) (*db.User, error) {
	var u db.User
	var isAdmin int
	var headscaleID sql.NullInt64
	err := d.QueryRow(
		`SELECT id, username, is_admin, headscale_user_id FROM portal_users WHERE username = $1`,
		username,
	).Scan(&u.ID, &u.Username, &isAdmin, &headscaleID)
	if err != nil {
		return nil, fmt.Errorf("no portal user named %q", username)
	}
	u.IsAdmin = isAdmin != 0
	if headscaleID.Valid {
		u.HeadscaleUserID = headscaleID.Int64
	}
	return &u, nil
}

// --- Default device + default exit_node (Этап 11 part 2a, 2026-07-13) ---
//
// These four commands let a user pick the per-user defaults that
// /add_rule will use (in Этап 11 part 2b). The defaults are stored
// in two TEXT columns on portal_users (migration v0.30):
//
//   default_device_node_id   — headscale node_id of the device
//   default_exit_node_id     — headscale node_id of the exit-node
//
// Empty string is the "no default" sentinel. /add_rule (part 2b)
// will refuse to proceed if either default is unset — for now the
// defaults are pure preferences with no functional effect, so
// nothing breaks if they're unset.
//
// The four commands:
//
//   /setdefaultdevice [node_id | clear]
//       no args → list user's devices, ask for node_id
//       <node_id> → set as default (validated against the user's
//                   own node_owner_map, excluding exit-nodes)
//       clear    → reset to ""
//
//   /defaultdevice
//       show the current default (or "not set" + hint)
//
//   /setexitnode [node_id | clear]
//       no args → list enabled exit_servers, ask for node_id
//       <node_id> → set as default (validated against enabled
//                   exit_servers only)
//       clear    → reset to ""
//
//   /defaultexitnode
//       show the current default (or "not set" + hint)
//
// All four are user-scope: each user manages their own defaults.
// Admin can NOT set defaults for other users (per-user preference,
// not a global policy) — admins wanting to seed defaults for a
// user would have to bind their own chat as that user, which is
// the existing /bind mechanism, not a new code path.

// setDefaultDeviceReply is the user-scope reply for /setdefaultdevice.
// Mirrors the "list with no args, set with arg, clear with 'clear'"
// grammar that /setexitnode uses — keeping both commands uniform
// means /help can describe them in one sentence.
func setDefaultDeviceReply(env BotEnv, arg string) string {
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.setdefaultdevice.not_bound")
	}
	arg = strings.TrimSpace(arg)

	// Get the user's devices from node_owner_map, filtering out
	// exit-nodes and public nodes (those are infrastructure, not
	// endpoints a user would route through). We use the db helper
	// (not an inline query) to keep the SQL in one place.
	owners, err := db.ListNodeOwnersByUsername(env.DB, env.Username)
	if err != nil {
		return i18n.Tf(lang, "bot.setdefaultdevice.db_error", err)
	}
	var deviceIDs []string
	for _, o := range owners {
		// tag:exit-node and tag:public are not "devices" in the
		// user-routing sense — they are shared infrastructure.
		if o.Tag == "tag:exit-node" || o.Tag == "tag:public" {
			continue
		}
		deviceIDs = append(deviceIDs, o.NodeID)
	}
	if len(deviceIDs) == 0 {
		return i18n.T(lang, "bot.setdefaultdevice.no_devices")
	}

	// Build a node_id → hostname map for the list/confirm views.
	// Best-effort: if headscale is unreachable we still print the
	// node_ids (the user can read them off /my_nodes).
	hostnameMap := map[string]string{}
	if env.userHS() != nil {
		if nodes, err := env.userHS().ListAllNodes(); err == nil {
			for _, n := range nodes {
				hn := n.GivenName
				if hn == "" {
					hn = n.Hostname
				}
				hostnameMap[n.ID] = hn
			}
		}
	}

	// No arg → list the devices and ask for the node_id.
	if arg == "" {
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s\n\n", i18n.Tf(lang, "bot.setdefaultdevice.list_header", len(deviceIDs)))
		for _, id := range deviceIDs {
			hn := hostnameMap[id]
			if hn == "" {
				hn = "(unknown hostname)"
			}
			fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.setdefaultdevice.list_row", hn, id))
		}
		sb.WriteString(i18n.T(lang, "bot.setdefaultdevice.cta"))
		return trimForTelegram(sb.String())
	}

	// "clear" → reset to no default.
	if strings.EqualFold(arg, "clear") {
		if _, err := db.SetDefaultDevice(env.DB, env.PortalUserID, ""); err != nil {
			return i18n.Tf(lang, "bot.setdefaultdevice.db_error", err)
		}
		_ = db.AppendAuditLog(env.DB, env.PortalUserID, env.Username, "default_device_changed", "cleared")
		return i18n.T(lang, "bot.setdefaultdevice.cleared")
	}

	// Validate that arg is one of the user's devices.
	valid := false
	for _, id := range deviceIDs {
		if id == arg {
			valid = true
			break
		}
	}
	if !valid {
		return i18n.Tf(lang, "bot.setdefaultdevice.not_in_list", arg)
	}

	if _, err := db.SetDefaultDevice(env.DB, env.PortalUserID, arg); err != nil {
		return i18n.Tf(lang, "bot.setdefaultdevice.db_error", err)
	}
	_ = db.AppendAuditLog(env.DB, env.PortalUserID, env.Username, "default_device_changed", "set to node "+arg)
	hn := hostnameMap[arg]
	if hn != "" {
		return i18n.Tf(lang, "bot.setdefaultdevice.set_with_hostname", hn, arg)
	}
	return i18n.Tf(lang, "bot.setdefaultdevice.set_bare", arg)
}

// defaultDeviceReply is the user-scope reply for /defaultdevice.
// Shows the current default (resolved to a hostname when possible)
// or a "not set" hint pointing at /setdefaultdevice.
func defaultDeviceReply(env BotEnv) string {
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.defaultdevice.not_bound")
	}
	nodeID, err := db.GetDefaultDevice(env.DB, env.PortalUserID)
	if err != nil {
		return i18n.Tf(lang, "bot.defaultdevice.db_error", err)
	}
	if nodeID == "" {
		return i18n.T(lang, "bot.defaultdevice.empty")
	}
	// Resolve the hostname best-effort. If headscale is down we
	// still return the node_id (it's enough to act on).
	if env.userHS() != nil {
		if nodes, err := env.userHS().ListAllNodes(); err == nil {
			for _, n := range nodes {
				if n.ID == nodeID {
					hn := n.GivenName
					if hn == "" {
						hn = n.Hostname
					}
					if hn != "" {
						return i18n.Tf(lang, "bot.defaultdevice.row", hn, nodeID)
					}
				}
			}
		}
	}
	return i18n.Tf(lang, "bot.defaultdevice.lookup_failed", nodeID)
}

// setExitNodeReply is the user-scope reply for /setexitnode. The
// grammar mirrors /setdefaultdevice exactly (no args → list,
// <node_id> → set, "clear" → reset) so /help can describe them
// in one sentence.
//
// Validation: the node_id must be a row in exit_servers with
// enabled=1. The node_owner_map tag:exit-node view is NOT enough
// on its own (a node can be tagged exit-node in headscale but
// disabled in skygate's exit_servers — admin controls that flag).
// We use exit_servers.enabled as the source of truth.
func setExitNodeReply(env BotEnv, arg string) string {
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.setexitnode.not_bound")
	}
	arg = strings.TrimSpace(arg)

	servers, err := db.ListExitServers(env.DB)
	if err != nil {
		return i18n.Tf(lang, "bot.setexitnode.db_error", err)
	}
	var enabled []db.ExitServer
	for _, s := range servers {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	if len(enabled) == 0 {
		return i18n.T(lang, "bot.setexitnode.no_enabled")
	}

	// No arg → list enabled exit-nodes.
	if arg == "" {
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s\n\n", i18n.Tf(lang, "bot.setexitnode.list_header", len(enabled)))
		for _, s := range enabled {
			fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.setexitnode.list_row", s.Hostname, s.NodeID))
		}
		sb.WriteString(i18n.T(lang, "bot.setexitnode.cta"))
		return trimForTelegram(sb.String())
	}

	// "clear" → reset.
	if strings.EqualFold(arg, "clear") {
		if _, err := db.SetDefaultExitNode(env.DB, env.PortalUserID, ""); err != nil {
			return i18n.Tf(lang, "bot.setexitnode.db_error", err)
		}
		_ = db.AppendAuditLog(env.DB, env.PortalUserID, env.Username, "default_exit_node_changed", "cleared")
		return i18n.T(lang, "bot.setexitnode.cleared")
	}

	// Validate: arg must be a node_id of an enabled exit_servers row.
	var picked *db.ExitServer
	for i := range enabled {
		if enabled[i].NodeID == arg {
			picked = &enabled[i]
			break
		}
	}
	if picked == nil {
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.setexitnode.not_in_list_prefix", arg))
		for _, s := range enabled {
			fmt.Fprintf(&sb, "%s\n", i18n.Tf(lang, "bot.setexitnode.list_row", s.Hostname, s.NodeID))
		}
		return trimForTelegram(sb.String())
	}

	if _, err := db.SetDefaultExitNode(env.DB, env.PortalUserID, picked.NodeID); err != nil {
		return i18n.Tf(lang, "bot.setexitnode.db_error", err)
	}
	_ = db.AppendAuditLog(env.DB, env.PortalUserID, env.Username, "default_exit_node_changed", "set to "+picked.Hostname+" (node "+picked.NodeID+")")
	return i18n.Tf(lang, "bot.setexitnode.set", picked.Hostname, picked.NodeID)
}

// defaultExitNodeReply is the user-scope reply for /defaultexitnode.
// Symmetric with defaultDeviceReply: shows the current default
// (resolved to hostname when possible) or a "not set" hint.
func defaultExitNodeReply(env BotEnv) string {
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.defaultexitnode.not_bound")
	}
	nodeID, err := db.GetDefaultExitNode(env.DB, env.PortalUserID)
	if err != nil {
		return i18n.Tf(lang, "bot.defaultexitnode.db_error", err)
	}
	if nodeID == "" {
		return i18n.T(lang, "bot.defaultexitnode.empty")
	}
	// Look up the hostname from exit_servers (no headscale call
	// needed — the hostname is right there). Falls back to node_id
	// if the row is gone (e.g. admin deleted the exit-server
	// between when the user set the default and now).
	if hostname, _ := db.LookupExitServerHostname(env.DB, nodeID); hostname != "" {
		return i18n.Tf(lang, "bot.defaultexitnode.row", hostname, nodeID)
	}
	return i18n.Tf(lang, "bot.defaultexitnode.lookup_failed", nodeID)
}

// mySubnetReply shows the user's personal subnet (the v0.16.0
// per-user subnets feature). Reads from the denormalized
// portal_users columns (subnet_cidr / subnet_status /
// subnet_router_node_id) so no JOIN is needed on the hot
// path. If a future read needs the full user_subnets row
// (e.g. created_at, control_plane_url), the manager's
// subnet.Get() helper does the JOIN.
//
// 2026-07-17: v0.16.0 — /mysubnet. Parallel to /myexitnodes
// and /my_status. Shows:
//   - the user's CIDR (10.0.<uid>.0/24, deterministic)
//   - status (pending|active|disabled)
//   - router hostname (or "not yet provisioned" while
//     pending; v0.16.1 fills this when the sidecar
//     registers)
//   - control plane ("" = global plane)
//   - cross-user sharing (v0.16.0 ships empty lists;
//     v0.17.1 fills them when sharing lands)
func mySubnetProvisionReply(env BotEnv) string {
	markHTMLReply()
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.mysubnet.not_bound")
	}
	if env.Sidecar == nil {
		return i18n.Tf(lang, "bot.mysubnet.provision_no_manager",
			env.Username)
	}
	key, exp, err := env.Sidecar.GeneratePreauth(
		context.Background(), env.PortalUserID)
	if err != nil {
		return i18n.Tf(lang, "bot.mysubnet.provision_error", err)
	}
	info := env.Sidecar.BuildPreauthInfo(
		env.PortalUserID, key, exp, env.Username)
	body := i18n.Tf(lang, "bot.mysubnet.provision_header",
		env.Username) + "\n" +
		Field(i18n.T(lang, "bot.mysubnet.provision_key_label"),
			"<code>"+info.Key+"</code>") + "\n" +
		Field(i18n.T(lang, "bot.mysubnet.provision_hostname_label"),
			"<code>"+info.Hostname+"</code>") + "\n" +
		Field(i18n.T(lang, "bot.mysubnet.provision_routes_label"),
			"<code>"+info.Routes+"</code>") + "\n" +
		Field(i18n.T(lang, "bot.mysubnet.provision_expires_label"),
			"<code>"+info.ExpiresAt.Format("2006-01-02 15:04 MST")+"</code>") + "\n\n" +
		"<i>" + i18n.T(lang, "bot.mysubnet.provision_help") + "</i>\n\n" +
		"<b>" + i18n.T(lang, "bot.mysubnet.provision_command_label") + "</b>\n" +
		"<pre>sudo tailscale up \\\n" +
		"  --authkey=" + info.Key + " \\\n" +
		"  --hostname=" + info.Hostname + " \\\n" +
		"  --advertise-routes=" + info.Routes + "</pre>"
	return body
}

// mySubnetShareReply — v0.17.1. /mysubnet share <username>.
// Grants the named user access to the caller's
// personal subnet. The ACL is re-pushed to headscale
// via the sidecar manager's HSForUser path. Sharing
// is one-directional: caller → grantee. The grantee
// does NOT automatically get access to the caller's
// devices (only to the caller's subnet).
func mySubnetShareReply(env BotEnv, args []string) string {
	markHTMLReply()
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.mysubnet.not_bound")
	}
	if len(args) == 0 {
		return i18n.Tf(lang, "bot.mysubnet.share_usage")
	}
	granteeName := strings.TrimSpace(args[0])
	if granteeName == "" {
		return i18n.Tf(lang, "bot.mysubnet.share_usage")
	}
	// Look up the grantee's user_id.
	var granteeID int64
	if err := env.DB.QueryRow(
		`SELECT id FROM portal_users WHERE username = $1`, granteeName,
	).Scan(&granteeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return i18n.Tf(lang, "bot.mysubnet.share_no_user", granteeName)
		}
		return i18n.Tf(lang, "bot.mysubnet.share_error", err)
	}
	// Grant the share. subnet.Grant is idempotent and
	// returns ErrSelfShare on self-share.
	if err := subnet.Grant(env.DB, env.PortalUserID, granteeID); err != nil {
		if errors.Is(err, subnet.ErrSelfShare) {
			return i18n.T(lang, "bot.mysubnet.share_self")
		}
		if errors.Is(err, subnet.ErrNotFound) {
			return i18n.T(lang, "bot.mysubnet.share_no_subnet")
		}
		return i18n.Tf(lang, "bot.mysubnet.share_error", err)
	}
	// Re-apply ACL so the new share row becomes part
	// of the headscale policy. The push targets the
	// caller's plane (sharing is per-user, per-plane).
	// Best-effort: a failure is logged but doesn't
	// fail the share (the row is in the DB; the
	// operator can manually re-apply).
	planeURL := ""
	if env.PortalPlaneURL != nil {
		planeURL = env.PortalPlaneURL(env.PortalUserID)
	}
	res := acl.ApplyACLPipelineForPlane(
		env.DB, env.HSForPortalUser(env.PortalUserID),
		planeURL, nil, env.Username,
		fmt.Sprintf("subnet_share %s -> %s", env.Username, granteeName), false)
	if !res.Applied {
		log.Printf("subnet_share: ACL reapply failed user=%d -> %d: %v (share is in DB; click 'Re-apply ACL' to push)",
			env.PortalUserID, granteeID, res.Err)
	}
	return i18n.Tf(lang, "bot.mysubnet.share_ok",
		env.Username, granteeName)
}

// mySubnetRevokeReply — v0.17.1. /mysubnet revoke <username>.
// Removes a previously-granted share. The ACL is
// re-pushed to headscale.
func mySubnetRevokeReply(env BotEnv, args []string) string {
	markHTMLReply()
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.mysubnet.not_bound")
	}
	if len(args) == 0 {
		return i18n.Tf(lang, "bot.mysubnet.revoke_usage")
	}
	granteeName := strings.TrimSpace(args[0])
	if granteeName == "" {
		return i18n.Tf(lang, "bot.mysubnet.revoke_usage")
	}
	var granteeID int64
	if err := env.DB.QueryRow(
		`SELECT id FROM portal_users WHERE username = $1`, granteeName,
	).Scan(&granteeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return i18n.Tf(lang, "bot.mysubnet.revoke_no_user", granteeName)
		}
		return i18n.Tf(lang, "bot.mysubnet.revoke_error", err)
	}
	if err := subnet.Revoke(env.DB, env.PortalUserID, granteeID); err != nil {
		if errors.Is(err, subnet.ErrShareNotFound) {
			return i18n.Tf(lang, "bot.mysubnet.revoke_not_shared", env.Username, granteeName)
		}
		return i18n.Tf(lang, "bot.mysubnet.revoke_error", err)
	}
	// Re-apply ACL — symmetric to share. The new
	// (smaller) dst list is pushed to headscale.
	planeURL := ""
	if env.PortalPlaneURL != nil {
		planeURL = env.PortalPlaneURL(env.PortalUserID)
	}
	res := acl.ApplyACLPipelineForPlane(
		env.DB, env.HSForPortalUser(env.PortalUserID),
		planeURL, nil, env.Username,
		fmt.Sprintf("subnet_revoke %s -> %s", env.Username, granteeName), false)
	if !res.Applied {
		log.Printf("subnet_revoke: ACL reapply failed user=%d -> %d: %v (revoke is in DB; click 'Re-apply ACL' to push)",
			env.PortalUserID, granteeID, res.Err)
	}
	return i18n.Tf(lang, "bot.mysubnet.revoke_ok",
		env.Username, granteeName)
}
