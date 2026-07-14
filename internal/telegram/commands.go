package telegram

import (
	"context"
	"database/sql"
	"strings"

	"skygate/internal/headscale"
	"skygate/internal/i18n"
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

	// 2026-07-14: Этап 14 v5 — bot i18n.
	//
	// Lang is the resolved language for the inbound chat
	// ("ru" or "en"). Handlers should look up every
	// user-visible string via i18n.T(Lang, "bot.key"). The
	// value is populated by envForMessage from
	// telegram_bindings.lang (for identified chats) or
	// LangFromTelegramCode(TelegramLangCode) for unbound
	// chats (so the very first /start greets the user in
	// their Telegram client language).
	//
	// TelegramLangCode is the raw BCP-47 tag from
	// message.from.language_code (e.g. "ru", "en-US", or
	// "" if the user hid the setting). Kept separate from
	// Lang so the /login reply can do its own
	// auto-detect on first bind without losing the raw
	// value (Telegram's resolve vs. our two-language
	// vocabulary is a one-way translation we don't want to
	// re-do on every command).
	Lang            string
	TelegramLangCode string
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

// 2026-07-14: Этап 14 v9 — butler voice v2 envelope.
//
// Every command reply now goes through the butler envelope:
//   🪶  <header>          ← one-line context tag
//   <blank>
//   <body>                ← the actual answer (1..N lines)
//   <blank>               ← only when verbose
//   — <sign-off>          ← only when verbose
//
// The header is the topic of the reply (registry / codex /
// version / ack / bind / add / del / err / welcome). The
// body is unchanged from v1. The footer is added only when
// the body is long (>3 lines OR >300 runes); short replies
// stay clean.
//
// The mapping from command → header is the
// `commandContext` table below. Each command picked the
// context that most naturally names its topic; e.g. /help
// → "codex" (the help IS the codex), /status → "registry"
// (a list of counters), /version → "version" (the version
// scroll), /restart → "err" (the operator warning that
// precedes a process kill).
//
// The v1 greeting helpers (greetingForNewChat /
// greetingForReturningUser) compose themselves internally
// for backward-compat with the v1 personality tests. We
// detect "already composed" bodies by the `butlerSigil +
// "  "` prefix and skip re-wrapping.

// commandContext is the static mapping from command name to
// the butler envelope context for that command. Adding a
// new command = one line here + one i18n catalog key for
// the header (bot.header.<context>).
var commandContext = map[string]string{
	// admin scope
	"/status":            "registry",
	"/nodes":             "registry",
	"/rules":             "registry",
	"/audit":             "registry",
	"/exit_nodes":        "registry",
	"/quota":             "registry",
	"/ack":               "ack",
	"/restart":           "err", // operator warning
	"/bind":              "bind",
	"/unbind":            "unbind",
	// user scope
	"/my_status":         "registry",
	"/my_nodes":          "registry",
	"/my_rules":          "registry",
	"/my_quota":          "registry",
	"/myexitnodes":       "registry",
	"/add_device":        "add",
	"/add_rule":          "add",
	"/delrule":           "del",
	"/delete_rule":       "del", // deprecated alias of /delrule
	"/clearrules":        "del",
	"/setdefaultdevice":  "add",
	"/defaultdevice":     "registry",
	"/setexitnode":       "add",
	"/defaultexitnode":   "registry",
	// auth / preferences
	"/login":             "bind",
	"/start":             "welcome", // overridden by dispatchCommand for /start with token → "bind"
	"/lang":              "registry",
	"/_bind_cancel":      "bind",
	"/unbind_self":       "unbind",
	// meta
	"/version":           "version",
	"/help":              "codex",
}

// cmdReply is the internal return shape of dispatchCommand:
// the rendered body plus the envelope context to wrap it
// in. skipWrap is true when the body is already run through
// Compose (the v1 greeting helpers, used by /start and
// /login no-args) — HandleCommand must not wrap a second
// time.
type cmdReply struct {
	body     string
	context  string
	skipWrap bool
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
	reply := dispatchCommand(env, raw)
	if reply.skipWrap {
		// /start and /login no-args delegate to loginHint which
		// returns an already-composed body (the v1 greeting
		// helpers call ComposeDefault internally for v1 test
		// stability). Re-wrapping would stack two headers.
		return reply.body
	}
	return ComposeDefault(env.Lang, reply.context, reply.body)
}

// dispatchCommand parses the command name, runs the strict-mode
// and admin-only gates, and returns the rendered body plus the
// envelope context. The actual envelope wrapping happens in
// HandleCommand.
//
// Why a separate function: the strict-mode gate and admin-only
// gate need to run BEFORE we know which command to dispatch to,
// but the envelope context differs per command (we want /status
// rejected with an "err" header, but /help accepted with a
// "codex" header). dispatchCommand evaluates the gates, picks
// the right helper, and returns the metadata for the wrapper.
// HandleCommand stays a 4-line orchestrator.
func dispatchCommand(env BotEnv, raw string) cmdReply {
	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) == 0 {
		return cmdReply{body: i18n.T(env.Lang, "bot.empty_command"), context: "err"}
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
			return cmdReply{body: strictModeLockedReply(env.Lang), context: "err"}
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
		return cmdReply{body: i18n.Tf(env.Lang, "bot.admin_only_command", cmd), context: "err"}
	}
	switch cmd {
	case "/status":
		return cmdReply{body: statusReply(env), context: lookupContext(cmd)}
	case "/help":
		if len(args) == 0 {
			return cmdReply{body: helpReply(env), context: lookupContext(cmd)}
		}
		return cmdReply{body: helpDetailReply(args[0], env), context: lookupContext(cmd)}
	case "/version":
		return cmdReply{body: versionReply(env), context: lookupContext(cmd)}
	// --- admin scope ---
	case "/nodes":
		return cmdReply{body: nodesReply(env.DB), context: lookupContext(cmd)}
	case "/rules":
		return cmdReply{body: rulesReply(env.DB), context: lookupContext(cmd)}
	case "/audit":
		return cmdReply{body: auditReply(env.DB), context: lookupContext(cmd)}
	case "/exit_nodes":
		return cmdReply{body: exitNodesReply(env.DB), context: lookupContext(cmd)}
	case "/quota":
		return cmdReply{body: quotaReply(env.DB, env), context: lookupContext(cmd)}
	case "/ack":
		return cmdReply{body: ackReply(env.DB, strings.Join(args, " ")), context: lookupContext(cmd)}
	case "/restart":
		return cmdReply{body: restartReply(env, strings.Join(args, " ")), context: lookupContext(cmd)}
	case "/bind":
		return cmdReply{body: bindReply(env, strings.Join(args, " ")), context: lookupContext(cmd)}
	case "/unbind":
		return cmdReply{body: unbindReply(env, strings.TrimSpace(strings.Join(args, " "))), context: lookupContext(cmd)}
	// --- user scope ---
	case "/my_status":
		return cmdReply{body: myStatusReply(env), context: lookupContext(cmd)}
	case "/my_nodes":
		return cmdReply{body: myNodesReply(env), context: lookupContext(cmd)}
	case "/my_rules":
		return cmdReply{body: myRulesReply(env), context: lookupContext(cmd)}
	case "/my_quota":
		return cmdReply{body: myQuotaReply(env), context: lookupContext(cmd)}
	case "/myexitnodes":
		return cmdReply{body: myExitNodesReply(env), context: lookupContext(cmd)}
	case "/add_device":
		return cmdReply{body: addDeviceReply(env, strings.Join(args, " ")), context: lookupContext(cmd)}
	case "/add_rule":
		return cmdReply{body: addRuleReply(env, args), context: lookupContext(cmd)}
	case "/delrule":
		return cmdReply{body: deleteRuleReply(env, strings.TrimSpace(strings.Join(args, " "))), context: lookupContext(cmd)}
	case "/clearrules":
		return cmdReply{body: clearRulesReply(env, strings.TrimSpace(strings.Join(args, " "))), context: lookupContext(cmd)}
	case "/delete_rule":
		// Deprecated alias of /delrule. Kept for back-compat with
		// existing /help text + scripts that still call the old name.
		// Этап 12 (2026-07-13) added /delrule as the new short form.
		return cmdReply{body: deleteRuleReply(env, strings.TrimSpace(strings.Join(args, " "))), context: lookupContext(cmd)}
	// --- Этап 11 part 2a: per-user preferences ---
	case "/setdefaultdevice":
		return cmdReply{body: setDefaultDeviceReply(env, strings.TrimSpace(strings.Join(args, " "))), context: lookupContext(cmd)}
	case "/defaultdevice":
		return cmdReply{body: defaultDeviceReply(env), context: lookupContext(cmd)}
	case "/setexitnode":
		return cmdReply{body: setExitNodeReply(env, strings.TrimSpace(strings.Join(args, " "))), context: lookupContext(cmd)}
	case "/defaultexitnode":
		return cmdReply{body: defaultExitNodeReply(env), context: lookupContext(cmd)}
	// --- Этап 12: login-by-key ---
	// /login and /start are open to any chat (the strict-mode gate
	// above whitelists them, and when strict mode is off they work
	// the same way for legacy single-admin deploys). /start <token>
	// is the Telegram UX convention: the first thing every user
	// sends to a bot is /start, so making it the entry point to
	// the login flow removes a "I have to remember /login" step.
	//
	// 2026-07-14: Этап 14 v9 — the no-args paths of /login and
	// /start delegate to loginHint() which returns an
	// already-composed body (greetingForNewChat /
	// greetingForReturningUser call ComposeDefault internally
	// for v1 test stability). skipWrap tells HandleCommand
	// not to wrap a second time. The with-args paths return
	// raw bodies and DO get wrapped.
	case "/login":
		if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
			return cmdReply{body: loginHint(env), context: "welcome", skipWrap: true}
		}
		return cmdReply{body: loginReplyBody(env, args), context: "bind"}
	case "/start":
		if len(args) == 0 {
			return cmdReply{body: loginHint(env), context: "welcome", skipWrap: true}
		}
		return cmdReply{body: startReplyBody(env, args), context: "bind"}
	case "/lang":
		// 2026-07-14: Этап 14 v5 — per-chat language switch.
		// With no arg: report the current language.
		// With "ru" / "en": persist the choice in
		// telegram_bindings and confirm. The choice survives
		// across bot restarts (the next HandleCommand reads
		// the binding row).
		return cmdReply{body: langReply(env, args), context: lookupContext(cmd)}
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
		return cmdReply{body: i18n.T(env.Lang, "bot.start.cancelled"), context: lookupContext(cmd)}
	case "/unbind_self":
		// User-self service: drop your own binding without
		// asking admin. Useful for switching phones or
		// revoking a lost device. Admin path is still
		// /unbind <chat_id> (admin-only).
		return cmdReply{body: unbindSelfReply(env), context: lookupContext(cmd)}
	default:
		return cmdReply{body: i18n.Tf(env.Lang, "bot.unknown_command", cmd), context: "err"}
	}
}

// lookupContext returns the butler envelope context for a
// command. Falls back to "err" for unknown commands so an
// unknown-command reply still gets an envelope (we never
// return a body without one).
func lookupContext(cmd string) string {
	if c, ok := commandContext[cmd]; ok {
		return c
	}
	return "err"
}

// strictModeLockedReply is the message an unidentified chat gets
// when strict mode is on and it tries anything other than
// /help /version /login /start. The hint points the user at the
// exact next step (open /my/telegram in the web, generate a key,
// paste it back here) so they don't have to guess.
//
// 2026-07-14: Этап 14 v5 — translated via i18n. The lang for an
// unidentified chat in strict mode is whatever the dispatcher
// resolved from message.from.language_code (or 'en' fallback);
// since this chat is NOT bound, we don't have a stored preference.
// env.Lang is correct here because envForMessage populates it
// from LangFromTelegramCode even when there's no binding.
func strictModeLockedReply(lang string) string {
	return i18n.T(lang, "bot.strict_locked.locked")
}

// statusReply renders the admin /status reply. Reads the
// system-wide counters from device_rules / portal_users /
// acl_snapshots and returns a 3-line summary.
//
// 2026-07-14: Этап 14 v5 — translated via i18n. Takes the
// full BotEnv (not just *sql.DB) so it can pull env.Lang for
// the catalog lookup. The format of the reply is fixed by
// the catalog keys; the order in the ruCatalog and enCatalog
// blocks matches so the labels land in the same place.
func statusReply(env BotEnv) string {
	var totalRules, totalUsers, lastACL int64
	if err := env.DB.QueryRow(`SELECT COUNT(*) FROM device_rules`).Scan(&totalRules); err != nil {
		return i18n.Tf(env.Lang, "bot.status.db_error", err)
	}
	if err := env.DB.QueryRow(`SELECT COUNT(*) FROM portal_users`).Scan(&totalUsers); err != nil {
		return i18n.Tf(env.Lang, "bot.status.db_error", err)
	}
	if err := env.DB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM acl_snapshots WHERE applied_success=1`).Scan(&lastACL); err != nil {
		return i18n.Tf(env.Lang, "bot.status.db_error", err)
	}
	return i18n.T(env.Lang, "bot.status.header") + "\n" +
		i18n.Tf(env.Lang, "bot.status.rules", totalRules) + "\n" +
		i18n.Tf(env.Lang, "bot.status.users", totalUsers) + "\n" +
		i18n.Tf(env.Lang, "bot.status.last_acl", lastACL)
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
//
// 2026-07-14: Этап 14 v5 — every visible string now goes
// through i18n.T(env.Lang, "bot.help.*"). The layout is
// preserved from the previous helpReply (the v0.10.4
// butler-gatekeeper version with section headers lives on
// the v0.10.4 branch; the bot-i18n-v5 branch keeps the
// simpler "Commands (all)" / "Your commands" labels so the
// MVP is shippable, and the personality layer is upgraded
// separately).
func helpReply(env BotEnv) string {
	lang := env.Lang
	// Each catalog key already includes the leading "/cmd — " or
	// "/cmd <arg> — " prefix, so we just concatenate. This is the
	// MVP layout: a plain command list with one line per
	// command. The full butler-gatekeeper "codex" layout (with
	// section headers, "Your top three", and the warden's sigil)
	// lives on the v0.10.4 branch and is a future upgrade once
	// the bot i18n MVP is shipping and verified.
	common := i18n.T(lang, "bot.help.common_version") + "\n" +
		i18n.T(lang, "bot.help.common_help")
	auth := i18n.T(lang, "bot.help.auth_login") + " (paste the key from /my/telegram)\n" +
		"/start <key> — same as /login, Telegram UX convention"
	userScope := i18n.T(lang, "bot.help.user_top_my_status") + "\n" +
		i18n.T(lang, "bot.help.user_rest_my_nodes") + "\n" +
		i18n.T(lang, "bot.help.user_top_my_rules") + "\n" +
		i18n.T(lang, "bot.help.user_rest_my_quota") + "\n" +
		i18n.T(lang, "bot.help.user_rest_myexitnodes") + " with [default] marker\n" +
		i18n.T(lang, "bot.help.user_rest_add_device") + " for yourself\n" +
		"/add_rule <target> — add an exit-rule for yourself\n" +
		i18n.T(lang, "bot.help.user_rest_delrule") + "\n" +
		i18n.T(lang, "bot.help.user_rest_clearrules") + " (or another user, admin only); requires /clearrules confirm within 30s\n" +
		i18n.T(lang, "bot.help.user_rest_setdefaultdevice") + "\n" +
		i18n.T(lang, "bot.help.user_rest_defaultdevice") + "\n" +
		i18n.T(lang, "bot.help.user_rest_setexitnode") + "\n" +
		i18n.T(lang, "bot.help.user_rest_defaultexitnode")
	adminScope := i18n.T(lang, "bot.help.admin_top_status") + "\n" +
		i18n.T(lang, "bot.help.admin_top_nodes") + "\n" +
		i18n.T(lang, "bot.help.admin_top_exit_nodes") + "\n" +
		i18n.T(lang, "bot.help.admin_rest_rules") + "\n" +
		i18n.T(lang, "bot.help.admin_rest_quota") + "\n" +
		i18n.T(lang, "bot.help.admin_rest_audit") + "\n" +
		i18n.T(lang, "bot.help.admin_rest_ack") + "\n" +
		i18n.T(lang, "bot.help.admin_rest_restart") + "\n" +
		i18n.T(lang, "bot.help.admin_rest_bind") + "\n" +
		i18n.T(lang, "bot.help.admin_rest_unbind")
	// Three layouts:
	//   - unidentified + strict mode: only auth + common (locked)
	//   - identified non-admin: auth + common + user-scope
	//   - admin (identified or legacy unidentified): all four
	switch {
	case !env.IsIdentified() && env.StrictMode:
		return "🔒 " + i18n.T(lang, "bot.help.strict_locked_note") + "\n\n" +
			auth + "\n\n" + common
	case !env.IsIdentified() || env.IsAdmin:
		return "Commands (all):\n\n" + common + "\n\n" + auth + "\n\n" + userScope + "\n\n" + adminScope
	default:
		return "Your commands:\n\n" + common + "\n\n" + auth + "\n\n" + userScope
	}
}
