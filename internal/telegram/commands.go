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

	// 2026-07-13: Этап 11 part 2b follow-up — Notifier for
	// async operator alerts. addRuleReply / delRuleReply /
	// clearRulesReply ping this on SetPolicy failure so the
	// operator gets a Telegram alert ("ACL apply failed")
	// even if the user doesn't notice the warning in the
	// bot reply. nil is a valid value: read-only deploys
	// (no notifier configured) skip the alert; audit_log
	// is the audit trail regardless.
	Notifier Notifier

	// 2026-07-13: Этап 12 — strict mode flag. When true, an
	// unidentified chat (no row in telegram_bindings) is
	// rejected for every command except /help and /version
	// (and /login, which is the only way to *become*
	// identified). Loaded from global_settings on every
	// message by env() so an operator can flip the toggle
	// in /admin/telegram without restarting skygate.
	//
	// When false, the legacy behaviour holds: unidentified
	// callers fall through to the bootstrap-admin path. This
	// is preserved for single-admin-chat deploys that
	// predate the v0.29 bindings table.
	StrictMode bool
}

// pendingReplyForCurrentMessage is a package-level slot
// reply functions use to attach an inline-keyboard to the
// message they're about to return. The polling loop sets
// it to nil before each call, then reads it after.
//
// Why a package var and not a BotEnv field: BotEnv is
// passed by value throughout, so a reply function writing
// `env.PendingReply = ...` would mutate a local copy and
// the polling loop would never see the keyboard. The
// package var sidesteps the value-semantics problem without
// the refactor of every reply-function signature from
// `env BotEnv` to `env *BotEnv` (which would be ~50 sites).
//
// Concurrency: safe because the polling loop is
// single-threaded — getUpdates runs in one goroutine, and
// the var is set+read by the same goroutine. A future
// second poller would need its own per-goroutine slot, but
// that's a follow-up.
//
// 2026-07-13: Этап 13.
var pendingReplyForCurrentMessage *PendingReply

// PendingReply carries the optional inline-keyboard markup
// for a bot reply. Kept minimal for now — the only consumer
// is the /start <token> confirmation prompt. 2026-07-13:
// Этап 13.
type PendingReply struct {
	// InlineKeyboard is the JSON shape Telegram expects
	// under reply_markup.inline_keyboard. We build the rows
	// here (server-side) so the polling loop can include
	// them verbatim in the sendMessage payload.
	InlineKeyboard [][]map[string]string
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
//   - the caller is unidentified AND strict mode is OFF (legacy/anon mode)
//
// When strict mode is ON, an unidentified caller is NOT admin
// (they have to /login first). The HandleCommand gate is the
// first line of defence; this helper is the second — admin-only
// commands also check `env.EffectiveAdmin()` directly.
func (e BotEnv) EffectiveAdmin() bool {
	if !e.IsIdentified() {
		// Strict mode flips the legacy "unknown = admin" fallback
		// off. Without this single check, every multi-user
		// deploy would leak /status, /nodes, /audit to anyone
		// who can guess the bot username.
		if e.StrictMode {
			return false
		}
		return true // legacy fallback (single-admin-chat deploys)
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
// Command categories (2026-07-12: Этап 11; 2026-07-13: Этап 12):
//
//   user-scope  /my_status, /my_nodes, /my_rules, /my_quota,
//                /myexitnodes, /add_device, /add_rule,
//                /delrule, /clearrules
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
//   auth        /login, /start
//                — open to any caller (including unidentified),
//                  because they're the ONLY way an unidentified
//                  chat can become identified. /start <token> is
//                  an alias of /login <token> for Telegram UX
//                  convention (Telegram sends /start when the user
//                  first opens a chat, so we can't ignore it).
//
// Legacy (single-admin-chat deploys and tests) treats unidentified
// callers (BotEnv.ChatID==0) as admin so the original behaviour is
// preserved — UNLESS strict mode is on, in which case unidentified
// callers get a "🔒 chat not bound" reply for everything except
// /help, /version, /login, /start.
func HandleCommand(ctx context.Context, env BotEnv, raw string) string {
	_ = ctx
	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) == 0 {
		return "Empty command."
	}
	cmd := strings.ToLower(parts[0])
	args := parts[1:]
	// Strict-mode gate: when the operator has flipped strict mode
	// on, an unidentified chat cannot touch any portal data. We
	// allow /help, /version, /login, and /start through the gate
	// — the first two are pure read-only metadata, the last two
	// are the path TO becoming identified. Everything else gets
	// the same "chat not bound" hint so the user has a clear next
	// step (paste a key) instead of an opaque "admin only".
	if env.StrictMode && !env.IsIdentified() {
		authCmds := map[string]bool{
			"/help": true, "/version": true,
			"/login": true, "/start": true,
		}
		if !authCmds[cmd] {
			return strictModeLockedReply()
		}
	}
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
	case "/myexitnodes":
		return myExitNodesReply(env)
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
	// --- Этап 12: login-by-key ---
	// /login and /start are open to any chat (the strict-mode gate
	// above whitelists them, and when strict mode is off they work
	// the same way for legacy single-admin deploys). /start <token>
	// is the Telegram UX convention: the first thing every user
	// sends to a bot is /start, so making it the entry point to
	// the login flow removes a "I have to remember /login" step.
	case "/login":
		return loginReply(env, args)
	case "/start":
		return startReply(env, args)
	case "/_bind_cancel":
		// 2026-07-13: Этап 13 — synthetic command used by the
		// inline-keyboard "Cancel" button on the /start
		// confirmation prompt. Not a user-facing command;
		// the dispatcher never sees it from a real Telegram
		// update because the polling loop only routes text
		// starting with "/" and "_" is filtered out. The
		// callback dispatcher (notify.go:handleCallback)
		// reuses HandleCommand's reply rendering by passing
		// this synthetic command.
		return "Cancelled. The key is still valid — send /login <key> any time to bind."
	case "/unbind_self":
		// User-self service: drop your own binding without
		// asking admin. Useful for switching phones or
		// revoking a lost device. Admin path is still
		// /unbind <chat_id> (admin-only).
		return unbindSelfReply(env)
	default:
		return fmt.Sprintf("Unknown command: %s. Try /help.", cmd)
	}
}

// strictModeLockedReply is the message an unidentified chat gets
// when strict mode is on and it tries anything other than
// /help /version /login /start. The hint points the user at the
// exact next step (open /my/telegram in the web, generate a key,
// paste it back here) so they don't have to guess.
func strictModeLockedReply() string {
	return "🔒 This chat is not bound to a skygate user.\n" +
		"Open skygate → /my/telegram → 'Generate login key' → paste the key here:\n" +
		"  /login <key>\n" +
		"The key expires in 5 minutes and is single-use."
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
//
// Этап 12 (2026-07-13): strict mode is reflected in the auth
// section. An unidentified chat in a strict deploy sees only
// /login /start /help /version — every other command is locked
// until they bind.
func helpReply(env BotEnv) string {
	common := "/version — Skygate build, Go runtime, DB schema level\n" +
		"/help [command] — this list, or detailed help for one command"
	auth := "/login <key> — bind this chat to your skygate account (paste the key from /my/telegram)\n" +
		"/start <key> — same as /login, Telegram UX convention"
	userScope := "/my_status — your own summary (rules, devices, quota)\n" +
		"/my_nodes — your own devices\n" +
		"/my_rules — your own exit-rules\n" +
		"/my_quota — your rule count vs cap\n" +
		"/myexitnodes — list enabled exit-nodes with [default] marker\n" +
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
		"/bind <chat_id> <username> — bind a chat to a portal user (admin only)\n" +
		"/unbind <chat_id> — remove a binding (admin only)"
	// Three layouts:
	//   - unidentified + strict mode: only auth + common (locked)
	//   - identified non-admin: auth + common + user-scope
	//   - admin (identified or legacy unidentified): all four
	switch {
	case !env.IsIdentified() && env.StrictMode:
		return "🔒 Strict mode is ON. This chat is not bound to a skygate user.\n\n" +
			auth + "\n\n" + common
	case !env.IsIdentified() || env.IsAdmin:
		return "Commands (all):\n\n" + common + "\n\n" + auth + "\n\n" + userScope + "\n\n" + adminScope
	default:
		return "Your commands:\n\n" + common + "\n\n" + auth + "\n\n" + userScope
	}
}
