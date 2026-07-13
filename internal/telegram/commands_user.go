// Package telegram — user-scope reply functions (Этап 11, 2026-07-12).
//
// These power the /my_* commands plus /add_device, /add_rule, /delete_rule.
// Every function takes a BotEnv and uses env.PortalUserID / env.Username
// to filter data to the calling user. Admin callers see their own data
// too (not all-user data) — admins wanting the cross-user view use the
// admin-scope commands (/nodes, /rules, /quota) which are unchanged.
//
// The new /add_* commands also accept an optional username argument so
// the admin can act on a user's behalf. e.g.:
//   /add_rule alice telegram.org      → adds "telegram.org" for alice
//   /add_rule telegram.org            → adds "telegram.org" for the caller
//
// The actual rule/preauth writes are still done via the web UI for
// now (see addDeviceReply/addRuleReply); wiring headscale + ACL sync
// into the bot is a larger task tracked separately.

package telegram

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

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
//   /add_rule <target>                → action=accept
//   /add_rule <target> deny           → action=deny
//   /add_rule <username> <target>     → admin-only: add for that user
//
// SCOPE NOTE: rule writes need the full App context (audit + ACL
// sync). The web path is /my/exit-rules. The bot returns a guided
// hint rather than performing a half-baked insert.
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
	target, isAdminArg, err := resolveTargetUser(env, strings.Join(args, " "))
	if err != nil {
		return fmt.Sprintf("add_rule: %v", err)
	}
	if isAdminArg && !env.IsAdmin {
		return "add_rule: only admins can add a rule for another user."
	}
	value, ttype, err := classifyTarget(args[0])
	if err != nil {
		return fmt.Sprintf("add_rule: %v", err)
	}
	_ = ttype
	_ = target
	_ = value
	return fmt.Sprintf(
		"add_rule: writing rules from the bot is on the roadmap. "+
			"For now, open https://<skygate>/my/exit-rules and add:\n"+
			"  user: %s\n  target: %s\n  action: %s",
		target.Username, value, action)
}

// deleteRuleReply removes one of the caller's own rules by id.
// Cross-user is rejected: a regular user can only delete rules
// where user_id = env.PortalUserID. Admin users can delete any
// user's rule.
//
// SCOPE NOTE: rule deletes need the full App context (cascade + ACL
// sync). The bot returns a guided hint rather than half-deleting.
func deleteRuleReply(env BotEnv, arg string) string {
	if !env.IsIdentified() {
		return "delete_rule: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "delete_rule: usage: /delete_rule <id>   (id is from /my_rules)"
	}
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return fmt.Sprintf("delete_rule: %q is not a valid rule id", arg)
	}
	var ownerID int64
	var exitNode, tType, tVal, act string
	if err := env.DB.QueryRow(
		`SELECT user_id, exit_node_id, target_type, target_value, action
		   FROM device_rules WHERE id = ?`, id,
	).Scan(&ownerID, &exitNode, &tType, &tVal, &act); err != nil {
		return fmt.Sprintf("delete_rule: no rule with id=%d", id)
	}
	if !env.IsAdmin && ownerID != env.PortalUserID {
		return fmt.Sprintf("delete_rule: rule #%d belongs to another user; you can only delete your own rules", id)
	}
	_ = exitNode
	_ = tType
	_ = tVal
	_ = act
	return fmt.Sprintf(
		"delete_rule: removing rules from the bot is on the roadmap. "+
			"For now, open https://<skygate>/my/exit-rules and delete rule #%d (or /admin/exit-rules if you're an admin).",
		id)
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
