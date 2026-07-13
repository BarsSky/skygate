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
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"skygate/internal/acl"
	"skygate/internal/db"
)

// myStatusReply is the user-scope counterpart of /status. It shows
// the caller's own rule count, device count, and the last applied
// ACL snapshot version. If the caller's data is empty (e.g. brand
// new user, no devices yet), the reply says so explicitly rather
// than showing zeros that look like a bug.
func myStatusReply(env BotEnv) string {
	if !env.IsIdentified() {
		return "my_status: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	if env.Username == "" {
		return "my_status: your chat is bound but the user record has no username — ask an admin to re-bind."
	}
	var ruleCount, deviceCount int64
	if err := env.DB.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = ?`, env.PortalUserID).Scan(&ruleCount); err != nil {
		return fmt.Sprintf("my_status: db error: %v", err)
	}
	// 2026-07-12: Этап 10 part 4 — count of owned devices derived
	// from db.ListNodeOwnersByUsername. We use the full row list
	// (rather than a separate COUNT query) so the helper stays a
	// single source of truth; the slice is tiny (a user's devices)
	// so the cost is negligible.
	owned, _ := db.ListNodeOwnersByUsername(env.DB, env.Username)
	deviceCount = int64(len(owned))
	var lastACL int64
	_ = env.DB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM acl_snapshots WHERE applied_success = 1`).Scan(&lastACL)

	cap := env.MaxFor(env.Username)
	capStr := "∞"
	if cap > 0 {
		capStr = strconv.Itoa(cap)
	}
	return fmt.Sprintf("Your status (%s)\nrules: %d / %s\ndevices: %d\nlast acl: #%d",
		env.Username, ruleCount, capStr, deviceCount, lastACL)
}

// myNodesReply lists only the caller's own devices from
// node_owner_map. Mirrors the format of /nodes but filtered to
// (username = env.Username). A user with no devices gets a
// helpful "no devices yet" hint pointing at /add_device.
func myNodesReply(env BotEnv) string {
	if !env.IsIdentified() {
		return "my_nodes: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	// 2026-07-12: Этап 10 part 4 — moved to
	// db.ListNodeOwnersByUsername.
	owners, err := db.ListNodeOwnersByUsername(env.DB, env.Username)
	if err != nil {
		return fmt.Sprintf("my_nodes: db error: %v", err)
	}
	type row struct{ node, tag string }
	var nodes []row
	for _, n := range owners {
		tag := n.Tag
		if tag == "" {
			tag = "tag:untagged"
		}
		nodes = append(nodes, row{node: n.NodeID, tag: tag})
	}
	if len(nodes) == 0 {
		return fmt.Sprintf("my_nodes (%s): no devices yet. Use /add_device to issue a 1h preauth key for a new one.", env.Username)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Your devices (%s): %d\n\n", env.Username, len(nodes))
	for _, n := range nodes {
		fmt.Fprintf(&sb, "  • %s [%s]\n", n.node, n.tag)
	}
	return trimForTelegram(sb.String())
}

// myRulesReply lists the caller's own exit-rules, newest first.
// Mirrors /rules but filtered to user_id = env.PortalUserID.
// Limited to the most recent 25 (same cap as /rules) so the reply
// stays under Telegram's 4096-char limit.
func myRulesReply(env BotEnv) string {
	if !env.IsIdentified() {
		return "my_rules: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	rows, err := env.DB.Query(`
		SELECT r.id, r.exit_node_id, r.target_type, r.target_value,
		       COALESCE(r.action, 'accept') AS action
		  FROM device_rules r
		 WHERE r.user_id = ?
		 ORDER BY r.id DESC
		 LIMIT 25`, env.PortalUserID)
	if err != nil {
		return fmt.Sprintf("my_rules: db error: %v", err)
	}
	defer rows.Close()
	type rule struct {
		id                         int64
		exitNode, tType, tVal, act string
	}
	var rules []rule
	for rows.Next() {
		var rr rule
		if err := rows.Scan(&rr.id, &rr.exitNode, &rr.tType, &rr.tVal, &rr.act); err != nil {
			return fmt.Sprintf("my_rules: scan error: %v", err)
		}
		rules = append(rules, rr)
	}
	if len(rules) == 0 {
		return fmt.Sprintf("my_rules (%s): no exit-rules yet. Use /add_rule <target> to add one.", env.Username)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Your exit-rules (%s, latest 25 of %d):\n\n", env.Username, len(rules))
	for _, rr := range rules {
		fmt.Fprintf(&sb, "#%d @%s\n  %s %s → %s\n\n",
			rr.id, rr.exitNode, rr.tType, rr.tVal, rr.act)
	}
	return trimForTelegram(sb.String())
}

// myQuotaReply shows the caller's own rule count vs their cap. The
// existing /quota renders the same bar across all users; this is the
// single-user version so a user can ask "how close am I?" without
// the admin's /quota having to answer.
func myQuotaReply(env BotEnv) string {
	if !env.IsIdentified() {
		return "my_quota: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	var cnt int
	if err := env.DB.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = ?`, env.PortalUserID).Scan(&cnt); err != nil {
		return fmt.Sprintf("my_quota: db error: %v", err)
	}
	max := env.MaxFor(env.Username)
	pct := -1
	if max > 0 {
		pct = (cnt * 100) / max
	}
	bar := quotaBar(pct)
	maxStr := "∞"
	if max > 0 {
		maxStr = strconv.Itoa(max)
	}
	return fmt.Sprintf("Your quota (%s)\n  %d / %s %s %d%%",
		env.Username, cnt, maxStr, bar, safePct(pct))
}

// addDeviceReply issues a 1h single-use preauth key. For a regular
// user, the key is for themselves; for an admin, an optional
// `<username>` arg makes it for that user instead.
//
// Why this lives in the bot: posting a 1h preauth key from the web
// UI requires opening /my/preauth, copying the key, and shipping it
// to the device. The bot puts the key in the user's chat directly,
// so the workflow is: bot user types /add_device, copies the key,
// pastes into the device. ~10 seconds end-to-end.
//
// 2026-07-13: Этап 11 part 1 — real preauth issuance. Mirrors
// handlers_my_preauth.go:PostMyPreauth exactly:
//   1. env.HS.CreatePreauthKey (API + CLI fallback inside headscale pkg)
//   2. db.InsertPreauthKey (local row for the temporal backfill match)
//   3. db.AppendAuditLog (user can see "where did this key come from")
//
// The audit log records the action under the *target* user (so per-
// user audit views work) with detail "1h single-use (via bot)" so
// the bot-driven issuance is distinguishable from web-driven.
//
// Read-only deploys (HS == nil) get a clear hint instead of a panic.
// That keeps the legacy single-admin-chat deploy working even
// before SetHS is called from main.go.
func addDeviceReply(env BotEnv, arg string) string {
	if !env.IsIdentified() {
		return "add_device: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	target, isAdminArg, err := resolveTargetUser(env, arg)
	if err != nil {
		return fmt.Sprintf("add_device: %v", err)
	}
	if isAdminArg && !env.IsAdmin {
		return "add_device: only admins can issue a preauth key for another user. Drop the username to issue one for yourself."
	}
	// 2026-07-13: Этап 11 part 1 — guard read-only deploys. SetHS is
	// called from main.go so HS is non-nil in production; the check
	// exists so a future operator who restarts skygate without
	// SetHS sees a clear error rather than a nil-deref panic.
	if env.HS == nil {
		return "add_device: telegram bot is in read-only mode (headscale client not wired at startup). Ask an admin to enable bot writes."
	}
	hsUserID, _, err := db.GetUserHSByID(env.DB, target.ID)
	if err != nil || !hsUserID.Valid {
		return fmt.Sprintf("add_device: %s has no headscale user linked. Ask an admin to repair the headscale binding first.", target.Username)
	}
	key, err := env.HS.CreatePreauthKey(hsUserID.Int64, "1h", false)
	if err != nil {
		return fmt.Sprintf("add_device: headscale call failed: %v", err)
	}
	expiresAt := time.Now().Add(time.Hour).Unix()
	if _, err := db.InsertPreauthKey(env.DB, target.ID, key.Key, expiresAt, key.ID); err != nil {
		return fmt.Sprintf("add_device: persist key failed: %v", err)
	}
	if err := db.AppendAuditLog(env.DB, target.ID, target.Username, "preauth_issued", "1h single-use (via bot)"); err != nil {
		return fmt.Sprintf("add_device: audit write failed: %v", err)
	}
	// The fenced hskey line is monospaced in the Telegram client so
	// it's easy to copy. The surrounding message is plain text.
	return fmt.Sprintf("add_device: 1h key for %s (single-use)\n\n```\n%s\n```\n\nExpires in 1h. Paste into the device to register.", target.Username, key.Key)
}

// addRuleReply adds a new exit-rule for the caller (or, for admins,
// for a named user).
//
// The argument grammar is intentionally simple:
//   /add_rule <target>                → action=accept (uses defaults)
//   /add_rule <target> deny           → action=deny (uses defaults)
//   /add_rule <username> <target>     → admin-only: add for that user
//
// "Defaults" = the user's /setdefaultdevice + /setexitnode
// preferences (Этап 11 part 2a). The bot refuses to add a rule
// if either default is unset, so the user is forced to pick
// their device + exit-node explicitly before they start writing
// rules — matches the web form's device_id + exit_node
// selectors, just in a "set once, reuse" shape.
//
// 2026-07-13: Этап 11 part 2b — real write. Mirrors
// handlers/exit_rules_form_my.go:PostMyExitRule:
//
//   1. Read defaults (device_node_id, exit_node_id) for the
//      target user.
//   2. Validate defaults are still current (device in
//      node_owner_map, exit-node still enabled in exit_servers).
//   3. Per-user / per-device / total rule-limit check.
//   4. DNS resolve for domains → split into /32 subnets
//      (Tailscale ACLs work at L3/L4, not L7 — domains are
//      resolved to IPs and pinned as /32 subnets).
//   5. Insert rule(s) into device_rules.
//   6. acl.ApplyACLPipeline → GenerateACL → SetPolicy →
//      MarkACLApplied/Fail + AppendExitRuleLog.
//   7. audit_log row under the *target* user (so per-user
//      audit views stay correct).
//
// The bot skips the per-rule SyncAdvertisedRoutes call (admin
// can trigger via /admin/exit-rules/sync) and the Telegram
// Notifier alert (audit_log is the bot's audit trail).
func addRuleReply(env BotEnv, args []string) string {
	if !env.IsIdentified() {
		return "add_rule: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	if len(args) == 0 {
		return "add_rule: usage: /add_rule <target> [deny]\n       /add_rule <username> <target> [deny]   (admin only)"
	}

	// Pull off a possible trailing "deny" / "accept".
	action := "accept"
	last := args[len(args)-1]
	switch strings.ToLower(last) {
	case "deny", "block", "reject":
		action = "deny"
		args = args[:len(args)-1]
	case "accept", "allow":
		action = "accept"
		args = args[:len(args)-1]
	}
	if len(args) == 0 {
		return "add_rule: missing target after action. Usage: /add_rule <target> [deny]"
	}

	// Admin target: /add_rule <username> <target> [...]
	// Two args + admin = username is first; otherwise the
	// first arg is the target. We don't use resolveTargetUser
	// here because that helper wants a single string; we
	// already have args[0] / args[1] split.
	target := db.User{ID: env.PortalUserID, Username: env.Username, IsAdmin: env.IsAdmin}
	if len(args) >= 2 && env.IsAdmin {
		u, err := lookupUserByUsername(env.DB, args[0])
		if err != nil {
			return fmt.Sprintf("add_rule: %v (admin can target another user with: /add_rule <username> <target>)", err)
		}
		target = *u
		args = args[1:]
	} else if len(args) >= 2 && !env.IsAdmin {
		return "add_rule: extra args (admin-only: /add_rule <username> <target>)"
	}
	if len(args) == 0 {
		return "add_rule: missing target. Usage: /add_rule <target> [deny]"
	}

	value, _, err := classifyTarget(args[0])
	if err != nil {
		return fmt.Sprintf("add_rule: %v", err)
	}

	// Read defaults.
	deviceNodeID, err := db.GetDefaultDevice(env.DB, target.ID)
	if err != nil || deviceNodeID == "" {
		return "add_rule: no default device set. Use /setdefaultdevice to pick one first."
	}
	exitNodeNodeID, err := db.GetDefaultExitNode(env.DB, target.ID)
	if err != nil || exitNodeNodeID == "" {
		return "add_rule: no default exit-node set. Use /setexitnode to pick one first."
	}

	// Validate defaults are still current. The default columns
	// are TEXT pointers into node_owner_map / exit_servers;
	// those rows can disappear (device removed from tailnet,
	// exit-server disabled) and the default becomes stale.
	// We re-check on every insert so a rule never lands with
	// a dead device or disabled exit-node.
	var deviceIP string
	if env.HS != nil {
		if nodes, err := env.HS.ListAllNodes(); err == nil {
			for _, n := range nodes {
				if n.ID == deviceNodeID {
					if len(n.IPAddresses) > 0 {
						deviceIP = n.IPAddresses[0]
					}
					break
				}
			}
		}
	}
	deviceOwned, err := db.CountNodeOwnerByNodeUser(env.DB, deviceNodeID, target.Username)
	if err != nil || deviceOwned == 0 {
		return fmt.Sprintf("add_rule: default device (node %s) is not in %s's devices anymore. Use /setdefaultdevice to pick another.", deviceNodeID, target.Username)
	}
	// device_id: device_rules.device_id is INT, default column
	// is TEXT (node_id). headscale node_ids are always numeric,
	// so Atoi is safe; we surface a clear error otherwise.
	devID, err := strconv.Atoi(deviceNodeID)
	if err != nil {
		return fmt.Sprintf("add_rule: default device node_id %q is not numeric (skygate expects headscale node ids like 42): %v", deviceNodeID, err)
	}

	// Resolve the exit-node hostname. device_rules.exit_node_id
	// stores the hostname (matches what the web form inserts);
	// the default column stores the node_id. The lookup is a
	// single indexed read against exit_servers. We also check
	// enabled=1 because the user might have picked an
	// exit-server that the admin later disabled — the default
	// is then stale and we should refuse to insert a rule
	// pointing at a disabled server.
	var exitNodeHostname string
	var exitNodeEnabled int
	err = env.DB.QueryRow(
		`SELECT COALESCE(hostname, ''), COALESCE(enabled, 0)
		   FROM exit_servers WHERE node_id = ?`,
		exitNodeNodeID,
	).Scan(&exitNodeHostname, &exitNodeEnabled)
	if err != nil || exitNodeHostname == "" {
		return fmt.Sprintf("add_rule: default exit-node (node %s) is no longer an enabled exit-server. Use /setexitnode to pick another.", exitNodeNodeID)
	}
	if exitNodeEnabled == 0 {
		return fmt.Sprintf("add_rule: default exit-node (node %s, %s) is currently disabled. Use /setexitnode to pick another.", exitNodeNodeID, exitNodeHostname)
	}

	// Per-user / per-device / total rule-limit checks. Same
	// counts the web form uses (CountEnabledNonSubnetRules*).
	maxPerUser := env.MaxFor(target.Username)
	if maxPerUser > 0 {
		cnt, _ := db.CountEnabledNonSubnetRulesForUser(env.DB, target.ID)
		if cnt >= maxPerUser {
			return fmt.Sprintf("add_rule: user limit reached (%d/%d rules for %s). Delete an existing rule first.", cnt, maxPerUser, target.Username)
		}
	}
	if env.MaxRulesPerDevice > 0 {
		cnt, _ := db.CountEnabledNonSubnetRulesForUserDevice(env.DB, target.ID, devID)
		if cnt >= env.MaxRulesPerDevice {
			return fmt.Sprintf("add_rule: per-device limit reached (%d/%d rules on device %d).", cnt, env.MaxRulesPerDevice, devID)
		}
	}
	if env.MaxTotalRules > 0 {
		cnt, _ := db.CountEnabledRules(env.DB)
		if cnt >= env.MaxTotalRules {
			return fmt.Sprintf("add_rule: system-wide limit reached (%d/%d rules). Ask an admin to free some.", cnt, env.MaxTotalRules)
		}
	}

	// Classify + DNS resolve. Mirrors the web form: domains get
	// resolved to A records and inserted as /32 subnets
	// (Tailscale advertises routes as CIDR, not bare IPs).
	// If DNS fails, the bot still inserts the original target
	// as target_type=domain so the autoupdater can retry later.
	dnsWarning := ""
	ipsToInsert := []string{value}
	typeToInsert := "ip"
	if strings.Contains(value, "/") {
		typeToInsert = "subnet"
	}
	// Reclassify "domain" targets by looking at the raw arg.
	rawTarget := args[0]
	if !strings.Contains(rawTarget, "/") && !isIPLiteral(rawTarget) {
		// Domain.
		typeToInsert = "subnet"
		if addrs, err := net.LookupHost(rawTarget); err == nil {
			ipsToInsert = nil
			seen := map[string]bool{}
			for _, a := range addrs {
				if strings.Contains(a, ":") {
					continue
				}
				if seen[a] {
					continue
				}
				seen[a] = true
				ipsToInsert = append(ipsToInsert, a+"/32")
			}
			if len(ipsToInsert) == 0 {
				// Domain resolved only to IPv6 — fall back
				// to storing the bare domain so the
				// autoupdater retries A records later.
				typeToInsert = "domain"
				ipsToInsert = []string{rawTarget}
			}
		} else {
			dnsWarning = rawTarget + " (DNS: " + err.Error() + ")"
			typeToInsert = "domain"
			ipsToInsert = []string{rawTarget}
		}
	} else if typeToInsert == "ip" && !strings.Contains(value, "/") {
		// Bare IP → add /32 so Tailscale accepts it as a
		// CIDR route.
		ipsToInsert = []string{value + "/32"}
		typeToInsert = "subnet"
	}

	// Save the parent domain (target_type=domain) so the
	// autoupdater can track it and add knownSubdomains.
	parentDomain := ""
	if typeToInsert == "domain" {
		parentDomain = rawTarget
	}

	// Insert the rules. The web form does dedup via
	// FindDeviceRuleID + AppendDeviceRule; the bot skips the
	// dedup check for v1 (admin can clean up duplicates later
	// via /admin/exit-rules/cleanup). One insert per IP.
	var insertedIDs []int64
	for _, ip := range ipsToInsert {
		rowID, err := db.AppendDeviceRule(env.DB, target.ID, devID, exitNodeHostname, typeToInsert, ip, action, deviceIP, parentDomain)
		if err != nil {
			return fmt.Sprintf("add_rule: db error: %v", err)
		}
		insertedIDs = append(insertedIDs, rowID)
	}

	// Apply ACL pipeline. The pipeline ALWAYS saves the
	// snapshot (even on SetPolicy failure) so the operator
	// can roll back. We pass nil for the Alerter — the bot
	// audit_log row is the bot's audit trail; an extra
	// Telegram ping per /add_rule would be noise.
	//
	// 2026-07-13: Этап 13 follow-up — read-only guard.
	// /delrule and /clearrules already skip the pipeline
	// when env.HS == nil (read-only deploy). This brings
	// /add_rule in line with them: insert the rules + audit,
	// but skip the headscale.SetPolicy call (which would
	// nil-deref) and tell the user to ask an admin to sync.
	// This also matches addDeviceReply's "telegram not wired
	// for writes" guard pattern.
	detailForLog := fmt.Sprintf("user %s added rule(s) (type=%s target=%s exit=%s) for %s via bot",
		target.Username, typeToInsert, rawTarget, exitNodeHostname, target.Username)
	if env.HS == nil {
		auditDetail := fmt.Sprintf("via bot: %s %s → %s (exit=%s, action=%s, ids=%v) — ACL sync skipped (read-only mode)",
			typeToInsert, rawTarget, exitNodeHostname, exitNodeHostname, action, insertedIDs)
		if dnsWarning != "" {
			auditDetail += "; " + dnsWarning
		}
		_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rule_added", auditDetail)
		reply := fmt.Sprintf("add_rule: ✓ added %d rule(s) for %s (ids=%v) — ACL sync skipped (read-only mode) — ask admin to /admin/exit-rules/sync.",
			len(insertedIDs), target.Username, insertedIDs)
		if dnsWarning != "" {
			reply += "\n  ⚠ " + dnsWarning
		}
		return reply
	}
	pipe := acl.ApplyACLPipeline(env.DB, env.HS, nil, target.Username, detailForLog)

	// Audit log (under the target user, so per-user audit
	// views stay correct). The action is rule_added; the
	// detail captures what was added and which exit-node.
	auditDetail := fmt.Sprintf("via bot: %s %s → %s (exit=%s, action=%s, ids=%v)",
		typeToInsert, rawTarget, exitNodeHostname, exitNodeHostname, action, insertedIDs)
	if dnsWarning != "" {
		auditDetail += "; " + dnsWarning
	}
	_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rule_added", auditDetail)

	// Reply. Success case: list the inserted ids + the
	// ACL version that was applied. SetPolicy failure case:
	// the snapshot is still saved — call it out so the user
	// knows to ask an admin to retry the sync. ALSO send a
	// Telegram alert to the operator (the "🛡️ ACL" alert
	// fires on snapshot save; the "❌ ACL apply failed" alert
	// fires here, in the failure-only branch) so the
	// operator wakes up even if the user doesn't notice
	// the warning in the bot reply.
	if pipe.Applied {
		reply := fmt.Sprintf("add_rule: ✓ added %d rule(s) for %s\n  target: %s %s (action=%s)\n  exit: %s\n  rule_ids=%v\n  ACL v%d applied to headscale",
			len(insertedIDs), target.Username, typeToInsert, rawTarget, action, exitNodeHostname, insertedIDs, pipe.Version)
		if dnsWarning != "" {
			reply += "\n  ⚠ " + dnsWarning
		}
		return reply
	}
	// SetPolicy failed — ping the operator via the same
	// Notifier that /ack uses. Async so the bot reply
	// isn't blocked on the Telegram API call.
	if env.Notifier != nil {
		go env.Notifier.SendAlert(fmt.Sprintf("❌ ACL apply failed (rule by %s)\n  target: %s %s\n  err: %v",
			target.Username, typeToInsert, rawTarget, pipe.Err))
	}
	return fmt.Sprintf("add_rule: ⚠ rule(s) added to DB for %s (target=%s %s, ids=%v) but ACL v%d was NOT applied to headscale: %v\nAsk an admin to /admin/exit-rules/sync.",
		target.Username, typeToInsert, rawTarget, insertedIDs, pipe.Version, pipe.Err)
}

// deleteRuleReply removes one or more of the caller's own rules by id.
// Cross-user is rejected: a regular user can only delete rules
// where user_id = env.PortalUserID. Admin users can delete another
// user's rule via the optional <username> prefix.
//
// The function is named deleteRuleReply (the historical name from
// when the only command was /delete_rule); it powers BOTH /delrule
// (the new short form, primary) AND /delete_rule (deprecated alias,
// kept for back-compat with the original /help text). HandleCommand
// routes both commands to this function.
//
// Grammar:
//
//	/delrule <id>                  — delete one rule
//	/delrule <id1> <id2> <id3>     — delete multiple (whitespace-separated)
//	/delrule <username> <id> ...   — admin only: delete for that user
//	/delete_rule <id>              — same (deprecated alias)
//
// 2026-07-13: Этап 12 — real write. Mirrors
// handlers/exit_rules_form_my.go:PostDeleteExitRule:
//
//	1. For each id: GetRuleTargetTypeAndParent verifies ownership
//	   (the helper's WHERE filters by user_id, so a non-owned id
//	   returns ErrNotFound — we surface both "missing" and
//	   "not yours" as "not found / not yours" to avoid leaking
//	   rule existence across users).
//	2. If target_type=domain + parent_domain: DeleteRuleOrCascadeByParentDomain
//	   deletes the rule + any sibling /32 entries with the same
//	   parent_domain (autoupdater-derived entries).
//	3. Else: DeleteRuleForUser deletes the single row.
//	4. acl.ApplyACLPipeline → GenerateACL → SetPolicy → Mark+Log.
//	5. audit_log row under the *target* user (so per-user audit
//	   views stay correct).
//
// We collect per-id errors so the user gets a full report of "what
// was skipped" rather than failing on the first bad id. Multi-id
// deletes are best-effort: if SOME ids are valid we still process
// them and only fail completely when NO id is valid.
//
// Read-only deploys (env.HS == nil) get a guard: the DB delete
// still runs but the ACL pipeline is skipped with a clear hint
// ("ACL sync skipped — ask admin to /admin/exit-rules/sync"). This
// is a small improvement over addRuleReply (which would crash on
// nil HS); the same guard should be backported to addRuleReply in
// a follow-up.
//
// The bot skips the per-rule SyncAdvertisedRoutes call and the
// Telegram Notifier alert that the web form does — admin can
// trigger sync via /admin/exit-rules/sync, and audit_log is the
// bot's audit trail.
func deleteRuleReply(env BotEnv, arg string) string {
	if !env.IsIdentified() {
		return "delrule: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	args := strings.Fields(strings.TrimSpace(arg))
	if len(args) == 0 {
		return "delrule: usage: /delrule <id> [id2 ...]\n       /delrule <username> <id> [id2 ...]   (admin only)"
	}

	// Admin target: /delrule <username> <id> ... — first arg is the
	// target user (admin only), rest are rule ids. We detect
	// "username vs id" by trying strconv.Atoi on the first arg:
	// an all-digit arg is a rule id, anything else is treated as
	// a username. This avoids the admin getting tripped up when
	// their own username happens to be a positive integer.
	target := db.User{ID: env.PortalUserID, Username: env.Username, IsAdmin: env.IsAdmin}
	_, firstErr := strconv.Atoi(args[0])
	firstIsNum := firstErr == nil
	if env.IsAdmin && !firstIsNum {
		// First arg is non-numeric — treat as a username.
		u, err := lookupUserByUsername(env.DB, args[0])
		if err != nil {
			return fmt.Sprintf("delrule: %v (admin can target another user with: /delrule <username> <id>)", err)
		}
		target = *u
		args = args[1:]
	} else if !env.IsAdmin && !firstIsNum {
		// Non-admin: a non-numeric first arg in a multi-arg
		// command looks like an attempt to use the admin
		// <username> <id> form. Reject explicitly.
		if len(args) > 1 {
			return "delrule: extra args (admin-only: /delrule <username> <id>)"
		}
		// Single non-numeric arg: it's just a bad id — fall
		// through to the per-id validation below.
	}
	if len(args) == 0 {
		return "delrule: missing ids. Usage: /delrule <id> [id2 ...]"
	}

	// Parse all ids. Per-id errors are collected into `skipped`
	// so the reply can list "what we couldn't do" alongside the
	// successful deletes.
	type idJob struct {
		id           int
		targetType   string
		parentDomain string
	}
	var jobs []idJob
	var skipped []string
	for _, a := range args {
		id, err := strconv.Atoi(a)
		if err != nil || id <= 0 {
			skipped = append(skipped, fmt.Sprintf("%q (not a positive integer)", a))
			continue
		}
		// GetRuleTargetTypeAndParent filters by (id, user_id) —
		// a missing id OR a cross-user id both return ErrNotFound.
		// We surface them as "not found / not yours" so we don't
		// leak rule existence across users.
		tType, parentDomain, err := db.GetRuleTargetTypeAndParent(env.DB, id, target.ID)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%d (not found / not yours)", id))
			continue
		}
		jobs = append(jobs, idJob{id: id, targetType: tType, parentDomain: parentDomain})
	}
	if len(jobs) == 0 {
		return fmt.Sprintf("delrule: no valid ids to delete. Skipped: %s", strings.Join(skipped, ", "))
	}

	// Delete each rule. Domain rules cascade to /32 siblings
	// (the autoupdater-derived entries with the same parent_domain).
	// The cascade count = rows_affected - 1 (the "extra" /32s beyond
	// the row we asked to delete).
	var deletedIDs []int
	totalCascade := 0
	for _, j := range jobs {
		if j.targetType == "domain" && j.parentDomain != "" {
			n, err := db.DeleteRuleOrCascadeByParentDomain(env.DB, target.ID, j.id, j.parentDomain)
			if err == nil {
				totalCascade += int(n) - 1
			}
		} else {
			_ = db.DeleteRuleForUser(env.DB, j.id, target.ID)
		}
		deletedIDs = append(deletedIDs, j.id)
	}

	// ACL pipeline. Read-only deploys (HS == nil) skip the
	// pipeline — the rules are already gone, admin can
	// /admin/exit-rules/sync to push the updated policy manually.
	if env.HS == nil {
		auditDetail := fmt.Sprintf("via bot: deleted %d rule(s) for %s (cascade: %d, ids=%v) — ACL sync skipped (read-only mode)",
			len(deletedIDs), target.Username, totalCascade, deletedIDs)
		if len(skipped) > 0 {
			auditDetail += fmt.Sprintf("; skipped: %s", strings.Join(skipped, ", "))
		}
		_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rule_deleted", auditDetail)
		return fmt.Sprintf("delrule: ✓ removed %d rule(s) for %s (cascade: %d). ACL sync skipped (read-only mode) — ask admin to /admin/exit-rules/sync.",
			len(deletedIDs), target.Username, totalCascade)
	}

	detailForLog := fmt.Sprintf("user %s deleted %d rule(s) (cascade: %d) for %s via bot",
		target.Username, len(deletedIDs), totalCascade, target.Username)
	pipe := acl.ApplyACLPipeline(env.DB, env.HS, nil, target.Username, detailForLog)

	// Audit log under target user. The action is rule_deleted; the
	// detail captures what was deleted + cascade count + skipped ids
	// (so an operator scanning audit_log sees the full picture).
	auditDetail := fmt.Sprintf("via bot: deleted %d rule(s) for %s (cascade: %d, ids=%v)",
		len(deletedIDs), target.Username, totalCascade, deletedIDs)
	if len(skipped) > 0 {
		auditDetail += fmt.Sprintf("; skipped: %s", strings.Join(skipped, ", "))
	}
	_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rule_deleted", auditDetail)

	// Reply. Success: list deleted ids + ACL version. Failure:
	// rules deleted but ACL not applied — ask admin to sync
	// AND ping the operator via Notifier (same pattern as
	// addRuleReply) so the operator wakes up even if the
	// user doesn't notice the warning in the bot reply.
	if pipe.Applied {
		reply := fmt.Sprintf("delrule: ✓ deleted %d rule(s) for %s (cascade: %d)\n  rule_ids=%v\n  ACL v%d applied to headscale",
			len(deletedIDs), target.Username, totalCascade, deletedIDs, pipe.Version)
		if len(skipped) > 0 {
			reply += fmt.Sprintf("\n  ⚠ skipped: %s", strings.Join(skipped, ", "))
		}
		return reply
	}
	if env.Notifier != nil {
		go env.Notifier.SendAlert(fmt.Sprintf("❌ ACL apply failed (delete by %s)\n  ids=%v\n  err: %v",
			target.Username, deletedIDs, pipe.Err))
	}
	return fmt.Sprintf("delrule: ⚠ rule(s) deleted from DB for %s (ids=%v) but ACL v%d was NOT applied to headscale: %v\nAsk an admin to /admin/exit-rules/sync.",
		target.Username, deletedIDs, pipe.Version, pipe.Err)
}

// bindReply binds a Telegram chat_id to a portal user. Admin-only.
// The command shape is:
//
//	/bind <chat_id> <username>
//
// e.g. /bind 123456789 michail. The user gives us their chat_id
// (a positive number for a DM, negative for a group) and the
// admin pastes it in. The chat is then "theirs" — they can use
// /my_* commands and write rules for themselves.
//
// We require the admin to type the chat_id (rather than the chat
// announcing itself) so a user can't bind someone else's chat
// to their own account by guessing an admin chat.
func bindReply(env BotEnv, arg string) string {
	if !env.EffectiveAdmin() {
		return "bind: admin only."
	}
	parts := strings.Fields(arg)
	if len(parts) != 2 {
		return "bind: usage: /bind <chat_id> <username>"
	}
	chatID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || chatID == 0 {
		return fmt.Sprintf("bind: %q is not a valid chat_id (must be a non-zero integer)", parts[0])
	}
	username := parts[1]
	user, err := lookupUserByUsername(env.DB, username)
	if err != nil {
		return fmt.Sprintf("bind: %v", err)
	}
	boundBy := env.PortalUserID
	if boundBy == 0 {
		boundBy = user.ID // self-bind (admin → admin)
	}
	if err := db.UpsertTelegramBinding(env.DB, chatID, user.ID, boundBy, user.IsAdmin); err != nil {
		return fmt.Sprintf("bind: db error: %v", err)
	}
	return fmt.Sprintf("bind: chat %d → %s ✓", chatID, user.Username)
}

// unbindReply removes a binding. Admin-only. The user-scope
// counterpart of /bind is the admin deleting a user (the cascade
// in handlers_admin_users.go also calls db.DeleteTelegramBindingsByUser).
func unbindReply(env BotEnv, arg string) string {
	if !env.EffectiveAdmin() {
		return "unbind: admin only."
	}
	chatID, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || chatID == 0 {
		return fmt.Sprintf("unbind: %q is not a valid chat_id", arg)
	}
	if err := db.DeleteTelegramBinding(env.DB, chatID); err != nil {
		return fmt.Sprintf("unbind: db error: %v", err)
	}
	return fmt.Sprintf("unbind: chat %d removed ✓", chatID)
}

// resolveTargetUser picks the user a /add_* command should act for.
// If `arg` is empty or matches the caller's username, returns the
// caller. Otherwise `arg` must be a different username (admin-only).
// The bool returns true when the resolved user is different from
// the caller (so callers can short-circuit the admin-only check).
func resolveTargetUser(env BotEnv, arg string) (db.User, bool, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" || strings.EqualFold(arg, env.Username) {
		return db.User{
			ID:       env.PortalUserID,
			Username: env.Username,
			IsAdmin:  env.IsAdmin,
		}, false, nil
	}
	if looksLikeRuleTarget(arg) {
		return db.User{}, false, fmt.Errorf("first arg looks like a rule target (%q), not a username — usage: /add_rule <username> <target>", arg)
	}
	u, err := lookupUserByUsername(env.DB, arg)
	if err != nil {
		return db.User{}, false, err
	}
	return *u, true, nil
}

// looksLikeRuleTarget is a tiny heuristic: anything that contains
// whitespace, ':' or '/' is treated as a target_value, not a username.
// We don't try to detect bare domains vs usernames from the shape
// alone because usernames in skygate are allowed to contain dots
// (e.g. "michail.test") — the only unambiguous signals are
// whitespace (rule target) and prefix tokens like a username.
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
		`SELECT id, username, is_admin, headscale_user_id FROM portal_users WHERE username = ?`,
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
	if !env.IsIdentified() {
		return "setdefaultdevice: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	arg = strings.TrimSpace(arg)

	// Get the user's devices from node_owner_map, filtering out
	// exit-nodes and public nodes (those are infrastructure, not
	// endpoints a user would route through). We use the db helper
	// (not an inline query) to keep the SQL in one place.
	owners, err := db.ListNodeOwnersByUsername(env.DB, env.Username)
	if err != nil {
		return fmt.Sprintf("setdefaultdevice: db error: %v", err)
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
		return "setdefaultdevice: you have no devices yet. Use /add_device to issue a 1h preauth key for a new one, then /setdefaultdevice again."
	}

	// Build a node_id → hostname map for the list/confirm views.
	// Best-effort: if headscale is unreachable we still print the
	// node_ids (the user can read them off /my_nodes).
	hostnameMap := map[string]string{}
	if env.HS != nil {
		if nodes, err := env.HS.ListAllNodes(); err == nil {
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
		fmt.Fprintf(&sb, "Your devices (%d):\n\n", len(deviceIDs))
		for _, id := range deviceIDs {
			hn := hostnameMap[id]
			if hn == "" {
				hn = "(unknown hostname)"
			}
			fmt.Fprintf(&sb, "  %s (node %s)\n", hn, id)
		}
		sb.WriteString("\nSend /setdefaultdevice <node_id> to pick one.\n")
		sb.WriteString("Pass \"clear\" to remove the default.")
		return trimForTelegram(sb.String())
	}

	// "clear" → reset to no default.
	if strings.EqualFold(arg, "clear") {
		if _, err := db.SetDefaultDevice(env.DB, env.PortalUserID, ""); err != nil {
			return fmt.Sprintf("setdefaultdevice: db error: %v", err)
		}
		_ = db.AppendAuditLog(env.DB, env.PortalUserID, env.Username, "default_device_changed", "cleared")
		return "setdefaultdevice: default device cleared ✓"
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
		return fmt.Sprintf("setdefaultdevice: %q is not in your device list. Try /my_nodes to see valid node_ids.", arg)
	}

	if _, err := db.SetDefaultDevice(env.DB, env.PortalUserID, arg); err != nil {
		return fmt.Sprintf("setdefaultdevice: db error: %v", err)
	}
	_ = db.AppendAuditLog(env.DB, env.PortalUserID, env.Username, "default_device_changed", "set to node "+arg)
	hn := hostnameMap[arg]
	if hn != "" {
		return fmt.Sprintf("setdefaultdevice: default device set to %s (node %s) ✓", hn, arg)
	}
	return fmt.Sprintf("setdefaultdevice: default device set to node %s ✓", arg)
}

// defaultDeviceReply is the user-scope reply for /defaultdevice.
// Shows the current default (resolved to a hostname when possible)
// or a "not set" hint pointing at /setdefaultdevice.
func defaultDeviceReply(env BotEnv) string {
	if !env.IsIdentified() {
		return "defaultdevice: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	nodeID, err := db.GetDefaultDevice(env.DB, env.PortalUserID)
	if err != nil {
		return fmt.Sprintf("defaultdevice: db error: %v", err)
	}
	if nodeID == "" {
		return "defaultdevice: no default device set.\nUse /setdefaultdevice to pick one."
	}
	// Resolve the hostname best-effort. If headscale is down we
	// still return the node_id (it's enough to act on).
	if env.HS != nil {
		if nodes, err := env.HS.ListAllNodes(); err == nil {
			for _, n := range nodes {
				if n.ID == nodeID {
					hn := n.GivenName
					if hn == "" {
						hn = n.Hostname
					}
					if hn != "" {
						return fmt.Sprintf("defaultdevice: %s (node %s)\nChange with /setdefaultdevice.", hn, nodeID)
					}
				}
			}
		}
	}
	return fmt.Sprintf("defaultdevice: node %s (hostname lookup failed)\nChange with /setdefaultdevice.", nodeID)
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
	if !env.IsIdentified() {
		return "setexitnode: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	arg = strings.TrimSpace(arg)

	servers, err := db.ListExitServers(env.DB)
	if err != nil {
		return fmt.Sprintf("setexitnode: db error: %v", err)
	}
	var enabled []db.ExitServer
	for _, s := range servers {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	if len(enabled) == 0 {
		return "setexitnode: no enabled exit-nodes in skygate. Ask an admin to enable one in /admin/exit-nodes."
	}

	// No arg → list enabled exit-nodes.
	if arg == "" {
		var sb strings.Builder
		fmt.Fprintf(&sb, "Enabled exit-nodes (%d):\n\n", len(enabled))
		for _, s := range enabled {
			fmt.Fprintf(&sb, "  %s (node %s)\n", s.Hostname, s.NodeID)
		}
		sb.WriteString("\nSend /setexitnode <node_id> to pick one.\n")
		sb.WriteString("Pass \"clear\" to remove the default.")
		return trimForTelegram(sb.String())
	}

	// "clear" → reset.
	if strings.EqualFold(arg, "clear") {
		if _, err := db.SetDefaultExitNode(env.DB, env.PortalUserID, ""); err != nil {
			return fmt.Sprintf("setexitnode: db error: %v", err)
		}
		_ = db.AppendAuditLog(env.DB, env.PortalUserID, env.Username, "default_exit_node_changed", "cleared")
		return "setexitnode: default exit-node cleared ✓"
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
		fmt.Fprintf(&sb, "setexitnode: %q is not an enabled exit-node. Valid:\n", arg)
		for _, s := range enabled {
			fmt.Fprintf(&sb, "  %s (node %s)\n", s.Hostname, s.NodeID)
		}
		return trimForTelegram(sb.String())
	}

	if _, err := db.SetDefaultExitNode(env.DB, env.PortalUserID, picked.NodeID); err != nil {
		return fmt.Sprintf("setexitnode: db error: %v", err)
	}
	_ = db.AppendAuditLog(env.DB, env.PortalUserID, env.Username, "default_exit_node_changed", "set to "+picked.Hostname+" (node "+picked.NodeID+")")
	return fmt.Sprintf("setexitnode: default exit-node set to %s (node %s) ✓", picked.Hostname, picked.NodeID)
}

// defaultExitNodeReply is the user-scope reply for /defaultexitnode.
// Symmetric with defaultDeviceReply: shows the current default
// (resolved to hostname when possible) or a "not set" hint.
func defaultExitNodeReply(env BotEnv) string {
	if !env.IsIdentified() {
		return "defaultexitnode: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	nodeID, err := db.GetDefaultExitNode(env.DB, env.PortalUserID)
	if err != nil {
		return fmt.Sprintf("defaultexitnode: db error: %v", err)
	}
	if nodeID == "" {
		return "defaultexitnode: no default exit-node set.\nUse /setexitnode to pick one."
	}
	// Look up the hostname from exit_servers (no headscale call
	// needed — the hostname is right there). Falls back to node_id
	// if the row is gone (e.g. admin deleted the exit-server
	// between when the user set the default and now).
	if hostname, _ := db.LookupExitServerHostname(env.DB, nodeID); hostname != "" {
		return fmt.Sprintf("defaultexitnode: %s (node %s)\nChange with /setexitnode.", hostname, nodeID)
	}
	return fmt.Sprintf("defaultexitnode: node %s (exit-server row not found, may be disabled/deleted)\nChange with /setexitnode.", nodeID)
}
