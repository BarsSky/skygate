package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"skygate/internal/headscale"
)

// BotEnv is the read-only context HandleCommand needs beyond the
// database: per-user rule limits (from SKYGATE_USER_MAX_RULES), the
// default cap, the app version (set once in main.go from app.Version),
// and the DB itself.
//
// 2026-07-12: Этап 11 — added identity fields (ChatID, PortalUserID,
// Username, IsAdmin). The dispatcher in notify.go populates them from
// the inbound update + telegram_bindings table before each call.
//
// Backward-compat rule: when ChatID == 0, the bot treats the caller
// as admin (legacy single-chat behavior + the unit tests, which pass
// an empty BotEnv). When ChatID > 0 the bot enforces per-user
// permission checks: admin-only commands (e.g. /restart) reject a
// non-admin, and user-scope commands (e.g. /my_rules) filter to the
// caller's data.
//
// Why a struct: Phase 3 (/quota) needs to know per-user caps to
// answer "who is close to the limit". /ack needs the DB to update
// telegram_alerts. /version needs the build version. Threading a
// single struct is cleaner than a growing argument list, and
// tests can construct a BotEnv with empty limits to exercise the
// reply formatters without pulling in the full config stack.
type BotEnv struct {
	DB           *sql.DB
	UserMaxRules map[string]int
	DefaultMax   int
	// Version is the build label set by main.go (e.g. "v0.3").
	// Empty string means "version not configured" — /version then
	// prints "v0.0-dev" rather than failing the command.
	Version string

	// Identity of the inbound message. Populated by RealNotifier.Run
	// after looking up chat_id in telegram_bindings. Zero values
	// (ChatID=0) are treated as "admin" — see IsIdentified below.

	// ChatID is the Telegram chat_id of the inbound update. 0 means
	// "no identity, treat as admin" (used by tests + bootstrap).
	ChatID int64
	// PortalUserID is the skygate user this chat is bound to. 0 when
	// the chat is not bound.
	PortalUserID int64
	// Username is the portal_users.username of the bound user. "" when
	// not bound.
	Username string
	// IsAdmin mirrors portal_users.is_admin at bind time. The dispatcher
	// also sets this to true when ChatID matches the configured
	// telegram.chat_id (bootstrap admin chat) even without a binding
	// row, for backward compat with the single-admin deploy.
	IsAdmin bool

	// 2026-07-13: Этап 11 part 1 — *headscale.Client, snapshotted
	// from RealNotifier.HS by env() once per message. Needed by
	// write-side bot commands (/add_device issues a real preauth key;
	// /add_rule and /delrule trigger an ACL sync).
	// nil is a valid value: read-only deploys run the bot for
	// status/nodes/rules/audit without writes. Write commands guard
	// against nil and reply with a clear hint.
	HS *headscale.Client

	// 2026-07-13: Этап 11 part 2b — per-device and total rule
	// caps, snapshotted from the App's *config.Config at startup.
	// /add_rule checks these before inserting (the web form does
	// the same in PostMyExitRule). Zero means "no cap" — same
	// convention as UserMaxRules / DefaultMax.
	MaxRulesPerDevice int
	MaxTotalRules     int
}

// IsIdentified returns true when the bot knows which Telegram chat
// (and therefore which portal user) the message came from. Identified
// callers get permission checks; unidentified callers fall through to
// the legacy admin path so the existing test suite and the bootstrap
// single-chat deploy keep working.
func (e BotEnv) IsIdentified() bool { return e.ChatID != 0 }

// EffectiveAdmin returns true when this call should be treated as
// admin-level. True when:
//   - the caller is bound and IsAdmin, OR
//   - the caller is unidentified (legacy/anon mode)
//
// Used by every admin-only command (e.g. /restart) to decide whether
// to run or to return "admin only".
func (e BotEnv) EffectiveAdmin() bool {
	if !e.IsIdentified() {
		return true // legacy fallback
	}
	return e.IsAdmin
}

// MaxFor returns the per-user cap (from UserMaxRules) or the default.
// Return value of 0 means "no limit configured" — callers should
// render that as "∞" or "[no limit]" in the reply, not as a divisor.
func (e BotEnv) MaxFor(username string) int {
	if v, ok := e.UserMaxRules[username]; ok {
		return v
	}
	return e.DefaultMax
}

// HandleCommand returns the reply text for a command message.
// It is safe to call from the polling loop in Run().
//
// Command categories (2026-07-12: Этап 11):
//
//   user-scope  /my_status, /my_nodes, /my_rules, /my_quota,
//                /add_device, /add_rule, /delrule, /clearrules
//                — work for any identified user; data is filtered
//                  to the caller's own. Admin can use them too and
//                  gets the same scoping (admin's own data).
//
//   admin-only  /status, /nodes, /rules, /quota, /audit, /exit_nodes,
//                /ack, /restart, /bind, /unbind
//                — show ALL data; rejected for non-admin callers.
//
//   common      /help [command], /version
//                — open to any caller.
//
// Legacy (single-admin-chat deploys and tests) treats unidentified
// callers (BotEnv.ChatID==0) as admin so the original behaviour is
// preserved.
func HandleCommand(ctx context.Context, env BotEnv, raw string) string {
	_ = ctx
	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) == 0 {
		return "Empty command."
	}
	cmd := strings.ToLower(parts[0])
	args := parts[1:]
	// Admin-only commands: short-circuit to "admin only" when the
	// caller is identified and not admin. We check each case
	// explicitly (not a generic "if !admin return error" guard)
	// so the help text can list every admin command and the
	// /help command itself can be called by anyone.
	adminOnly := map[string]bool{
		"/status": true, "/nodes": true, "/rules": true, "/audit": true,
		"/exit_nodes": true, "/quota": true, "/ack": true, "/restart": true,
		"/bind": true, "/unbind": true,
	}
	if adminOnly[cmd] && env.IsIdentified() && !env.IsAdmin {
		return fmt.Sprintf("%s: admin only. Use the /my_* variants for your own data.", cmd)
	}
	switch cmd {
	case "/status":
		return statusReply(env.DB)
	case "/help":
		if len(args) == 0 {
			return helpReply(env)
		}
		return helpDetailReply(args[0], env)
	case "/version":
		return versionReply(env)
	// --- admin scope ---
	case "/nodes":
		return nodesReply(env.DB)
	case "/rules":
		return rulesReply(env.DB)
	case "/audit":
		return auditReply(env.DB)
	case "/exit_nodes":
		return exitNodesReply(env.DB)
	case "/quota":
		return quotaReply(env.DB, env)
	case "/ack":
		return ackReply(env.DB, strings.Join(args, " "))
	case "/restart":
		return restartReply(env, strings.Join(args, " "))
	case "/bind":
		return bindReply(env, strings.Join(args, " "))
	case "/unbind":
		return unbindReply(env, strings.TrimSpace(strings.Join(args, " ")))
	// --- user scope ---
	case "/my_status":
		return myStatusReply(env)
	case "/my_nodes":
		return myNodesReply(env)
	case "/my_rules":
		return myRulesReply(env)
	case "/my_quota":
		return myQuotaReply(env)
	case "/add_device":
		return addDeviceReply(env, strings.Join(args, " "))
	case "/add_rule":
		return addRuleReply(env, args)
	case "/delrule":
		return deleteRuleReply(env, strings.TrimSpace(strings.Join(args, " ")))
	case "/clearrules":
		return clearRulesReply(env, strings.TrimSpace(strings.Join(args, " ")))
	case "/delete_rule":
		// Deprecated alias of /delrule. Kept for back-compat with
		// existing /help text + scripts that still call the old name.
		// Этап 12 (2026-07-13) added /delrule as the new short form.
		return deleteRuleReply(env, strings.TrimSpace(strings.Join(args, " ")))
	// --- Этап 11 part 2a: per-user preferences ---
	case "/setdefaultdevice":
		return setDefaultDeviceReply(env, strings.TrimSpace(strings.Join(args, " ")))
	case "/defaultdevice":
		return defaultDeviceReply(env)
	case "/setexitnode":
		return setExitNodeReply(env, strings.TrimSpace(strings.Join(args, " ")))
	case "/defaultexitnode":
		return defaultExitNodeReply(env)
	default:
		return fmt.Sprintf("Unknown command: %s. Try /help.", cmd)
	}
}

func statusReply(d *sql.DB) string {
	var totalRules, totalUsers, lastACL int64
	if err := d.QueryRow(`SELECT COUNT(*) FROM device_rules`).Scan(&totalRules); err != nil {
		return fmt.Sprintf("status: db error: %v", err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM portal_users`).Scan(&totalUsers); err != nil {
		return fmt.Sprintf("status: db error: %v", err)
	}
	if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM acl_snapshots WHERE applied_success=1`).Scan(&lastACL); err != nil {
		return fmt.Sprintf("status: db error: %v", err)
	}
	return fmt.Sprintf("Skygate status\nrules: %d\nusers: %d\nlast acl: #%d", totalRules, totalUsers, lastACL)
}

// helpReply shows a short list of commands the caller can use.
// The list is split by category: common (everyone), user-scope
// (identified callers), admin-scope (admin only). Unidentified
// callers (legacy single-chat deploys) see the full list collapsed
// into one section.
func helpReply(env BotEnv) string {
	common := "/version — Skygate build, Go runtime, DB schema level\n" +
		"/help [command] — this list, or detailed help for one command"
	userScope := "/my_status — your own summary (rules, devices, quota)\n" +
		"/my_nodes — your own devices\n" +
		"/my_rules — your own exit-rules\n" +
		"/my_quota — your rule count vs cap\n" +
		"/add_device — issue a 1h single-use preauth key for yourself\n" +
		"/add_rule <target> — add an exit-rule for yourself\n" +
		"/delrule <id> [id2 ...] — delete one or more of your rules\n" +
		"/clearrules [username] — wipe ALL exit-rules for you (or another user, admin only); requires /clearrules confirm within 30s\n" +
		"/setdefaultdevice [node_id|clear] — set your default device for /add_rule\n" +
		"/defaultdevice — show your current default device\n" +
		"/setexitnode [node_id|clear] — set your default exit-node for /add_rule\n" +
		"/defaultexitnode — show your current default exit-node"
	adminScope := "/status — system-wide summary (rules/users/last acl)\n" +
		"/nodes — list ALL tailnet devices by user+tag\n" +
		"/exit_nodes — list exit-nodes (tag:exit-node) with last-seen\n" +
		"/rules — recent exit-rules across all users\n" +
		"/quota — per-user rule count vs cap\n" +
		"/audit — last 20 audit_log entries\n" +
		"/ack <id> — acknowledge an alert (id is the [#N] prefix)\n" +
		"/restart — graceful container restart (requires confirm)\n" +
		"/bind <chat_id> <username> — bind a chat to a portal user\n" +
		"/unbind <chat_id> — remove a binding"
	if !env.IsIdentified() || env.IsAdmin {
		return "Commands (all):\n\n" + common + "\n\n" + userScope + "\n\n" + adminScope
	}
	return "Your commands:\n\n" + common + "\n\n" + userScope
}
