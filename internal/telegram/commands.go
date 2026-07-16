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

	// 2026-07-16: v0.12.1 — per-user headscale client routing.
	// Populated by RealNotifier.env() from a closure the App
	// installs at startup. Returns the *headscale.Client to use
	// for the given portal user id (their per-plane override
	// from portal_users.headscale_url + headscale_api_key_enc,
	// or the global default if no override). Bot handlers
	// should call env.userHS() instead of reading env.HS
	// directly so a per-user /add_device routes the preauth
	// key issuance to the right control plane.
	//
	// nil is valid and means "no per-user routing" — the bot
	// then uses env.HS (the global default) for every
	// command. Backward compatible with v0.12.0 single-plane
	// deploys that don't call SetHSForUser.
	HSForPortalUser func(userID int64) *headscale.Client
	// 2026-07-16: v0.13.0 — per-user plane-URL routing.
	// Parallel to HSForPortalUser: returns the headscale_url
	// the given portal user is on ("" = global default).
	// Used by env.userPlaneURL() so the bot's ACL pipeline
	// can scope acl.GenerateACLForPlane to the right
	// identities (headscale rejects unknown identities
	// in tagOwners). nil falls through to "".
	PortalPlaneURL func(userID int64) string

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

// splitMessageMarker is the sentinel that a reply function
// uses to mark where one Telegram message ends and the next
// begins. The send path (RealNotifier.sendPlain + reply) splits
// the body on this marker and sends each part as a separate
// sendMessage call. Used by long replies like /help (which
// splits into 3 messages: Auth / User-scope / Admin) so each
// "form" is short and scannable on mobile.
//
// 2026-07-16: v0.16.5 — "split long replies" pass. The operator
// reported that on a phone, /help and other long replies are
// hard to read because the text is small and packed into one
// bubble. Telegram doesn't support font-size changes (the only
// "big" text is the per-message Big Emoji mode), so the cleanest
// fix is to break long replies into multiple shorter messages.
// Each section gets its own bubble and is easier to scan at
// default font size.
//
// The marker is a non-printing string with a unique prefix
// (so it can never collide with real user content), padded with
// newlines so each part stands on its own. The send path
// trims the marker itself (it's never visible in Telegram) and
// any extra blank lines around the split point.
const splitMessageMarker = "\n\n\x00SPLIT\x00\n\n"

// markHTMLReply sets the next reply's parse_mode to "HTML"
// so Telegram renders the <b>/<i>/<pre>/<code> tags in
// the body. Call from any reply function that uses
// Field()/Section()/PreLinesRaw() (the "more HTML" pass
// helpers in format.go) — without parse_mode=HTML, the
// tags show up as literal text in the chat and the
// message reads as raw source code.
//
// The function is a no-op if the pending slot is already
// populated (e.g. /myexitnodes sets an inline-keyboard
// for the same reply); in that case we just set the
// ParseMode field on the existing struct, so callers
// don't have to special-case "do I have a keyboard or
// not".
//
// 2026-07-16: v0.16.2 — "more HTML" pass bug fix. The
// v0.16.1 release added the HTML formatting helpers but
// did not set parse_mode on the sendMessage payload, so
// /my_status, /my_rules, /my_quota, /myexitnodes,
// /my_nodes, /version, /audit, /exit_nodes_health all
// rendered their <b> and <code> as literal text. The
// fix is a single opt-in per reply function (call
// markHTMLReply() at the top).
//
// Why opt-in instead of a global default: many replies
// (e.g. /help, /bind errors, the welcome card) use
// literal "<" characters as placeholders in their
// catalog text (like "<id>" or "<chat_id>"). Telegram
// rejects messages with unbalanced < or & in
// parse_mode=HTML — so a global default would break
// those replies. The opt-in keeps HTML off for
// literal-text replies and on for the structured ones.
func markHTMLReply() {
	if pendingReplyForCurrentMessage == nil {
		pendingReplyForCurrentMessage = &PendingReply{ParseMode: "HTML"}
	} else {
		pendingReplyForCurrentMessage.ParseMode = "HTML"
	}
}

// PendingReply carries the optional inline-keyboard markup
// for a bot reply. 2026-07-13: Этап 13 — added for the
// /start <token> confirmation prompt. 2026-07-14: Этап 14
// v12 — each button map may also include a `copy_text`
// field (Telegram Bot API v7.0+). When set, tapping the
// button copies that text to the user's clipboard; the
// bot doesn't need to handle a callback for the action.
// We use this for the preauth key in /add_device — the
// key is long enough that "select the code block and copy"
// is awkward on mobile, so we ship an explicit Copy button.
type PendingReply struct {
	// InlineKeyboard is the JSON shape Telegram expects
	// under reply_markup.inline_keyboard. We build the rows
	// here (server-side) so the polling loop can include
	// them verbatim in the sendMessage payload.
	//
	// 2026-07-15: changed from [][]map[string]string to
	// [][]map[string]any so the inner "copy_text" field
	// (Telegram Bot API 7.0+) can be a typed object
	// {"text": "..."} rather than a bare string. A bare
	// string triggers a 400 from sendMessage with
	// "Field \"copy_text\" must be of type Object" — which
	// silently dropped the entire /add_device reply in
	// production until the v0.14.1 logging fix.
	InlineKeyboard [][]map[string]any
	// ParseMode is "HTML" or "MarkdownV2" if the text uses
	// Telegram's entity syntax. Empty (the common case) =
	// plain text. 2026-07-15: v0.14.1 — added so /add_device
	// can render the preauth key inside <code>...</code>
	// for easier mobile selection, without making every
	// other bot reply opt into HTML.
	ParseMode string
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

// userHS returns the *headscale.Client to use for the bound
// portal user. When HSForPortalUser is set AND the chat is
// bound (PortalUserID > 0), it returns the per-user plane
// (from portal_users.headscale_url + headscale_api_key_enc,
// or the global default if the user has no override). When
// HSForPortalUser is nil (no per-user routing wired) or the
// chat is unbound, it falls back to env.HS.
//
// 2026-07-16: v0.12.1 — every bot handler that previously
// read env.HS directly should now call env.userHS() so a
// per-user /add_device issues the preauth key on the
// right control plane.
func (e BotEnv) userHS() *headscale.Client {
	if e.HSForPortalUser != nil && e.PortalUserID > 0 {
		if c := e.HSForPortalUser(e.PortalUserID); c != nil {
			return c
		}
	}
	return e.HS
}

// userPlaneURL returns the headscale_url the bound portal
// user is on ("" = global default). Used by the bot's
// ACL pipeline to scope acl.GenerateACLForPlane to the
// right identities — headscale rejects unknown identities
// in tagOwners, so a per-user /add_rule must push a
// policy that only contains the user's own plane's
// identities.
//
// 2026-07-16: v0.13.0.
func (e BotEnv) userPlaneURL() string {
	if e.PortalPlaneURL != nil && e.PortalUserID > 0 {
		return e.PortalPlaneURL(e.PortalUserID)
	}
	return ""
}

// userTargetPlaneURL returns the plane URL for a specific
// portal user id (used by admin commands that act on
// another user — e.g. /add_rule alice 1.2.3.4). Falls
// through to "" (global default) when the per-user
// routing isn't wired.
func (e BotEnv) userTargetPlaneURL(userID int64) string {
	if e.PortalPlaneURL != nil && userID > 0 {
		return e.PortalPlaneURL(userID)
	}
	return ""
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
	"/exit_nodes_health": "registry",
	"/sync_nodes":        "registry",
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
		"/exit_nodes": true, "/exit_nodes_health": true, "/sync_nodes": true, "/quota": true, "/ack": true, "/restart": true,
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
		return cmdReply{body: nodesReply(env), context: lookupContext(cmd)}
	case "/rules":
		return cmdReply{body: rulesReply(env), context: lookupContext(cmd)}
	case "/audit":
		return cmdReply{body: auditReply(env), context: lookupContext(cmd)}
	case "/exit_nodes":
		return cmdReply{body: exitNodesReply(env), context: lookupContext(cmd)}
	case "/exit_nodes_health":
		return cmdReply{body: exitNodesHealthReply(env), context: lookupContext(cmd)}
	case "/sync_nodes":
		return cmdReply{body: syncNodesReply(env), context: lookupContext(cmd)}
	case "/quota":
		return cmdReply{body: quotaReply(env), context: lookupContext(cmd)}
	case "/ack":
		return cmdReply{body: ackReply(env, strings.Join(args, " ")), context: lookupContext(cmd)}
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
		// 2026-07-16: v0.15.2 — addDeviceReply uses
		// butlerEnvelope() which renders the <pre>key</pre>
		// as monospace on Telegram. We set skipWrap so
		// HandleCommand's Compose() doesn't add a second
		// gate envelope on top of our butler gate.
		return cmdReply{body: addDeviceReply(env, strings.Join(args, " ")), context: lookupContext(cmd), skipWrap: true}
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
	// 2026-07-16: v0.15.2 — gate envelope is applied by
	// Compose() in HandleCommand. We just return the body
	// (4 short data lines) and let the v2 envelope wrap it.
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
// 2026-07-15: v0.14.0 — restructured to a real table-like layout:
// section headers (★/🔐/🛠), aligned command+description columns,
// and an opening "command-list" header with a Telegram-native
// code-style hint. The plain-text version uses Unicode alignment
// (4-space gutter + ≥ 2 spaces between command and description)
// so it renders as a table in any Telegram client without
// requiring parse_mode.
//
// The previous v0.10.4 "butler-gatekeeper" version (with the
// warden's sigil + codex-style "Your top three" intro) is the
// inspiration for the ✦/✧/🪶 glyphs but kept lighter for the
// MVP: the operator asked for a tabular list, not a personality
// piece.
//
// Этап 12 (2026-07-13): strict mode is reflected in the auth
// section. An unidentified chat in a strict deploy sees only
// /login /start /help /version — every other command is locked
// until they bind.
//
// 2026-07-16: v0.15.5 — gutter widened to 18 chars (max command
// `/exit_nodes_health` is 17 chars) and the duplicate `\`<cmd>\`` in
// every EN description was dropped. The gutter now carries the
// command name; the description is the explanation. Inline
// sub-commands (like `/clearrules confirm`) are still back-ticked
// for clarity. `/unbind_self` was missing from /help; added under
// the Auth section since it's a self-service command any identified
// user can run.
//
// 2026-07-16: v0.16.3 — "more HTML" pass for /help. The reply
// now renders each section as a tabular <pre> block (command +
// description in monospace, columns aligned), preceded by a <b>
// section header. The catalog backticks (`<id>`, `<target>`,
// etc.) are converted to <code>...</code> (HTML-escaped < >)
// so they render as monospace too. The 18-char gutter moves
// INSIDE the <pre> block so alignment survives the
// proportional→monospace switch. The header before the table
// is the <b>section name</b> (was a plain "🔐 Auth — ..." line
// before; same shape, just bold + the table is below it).
//
// 2026-07-16: v0.16.5 — split into multiple messages. The
// operator reported that on a phone, the single-bubble /help
// is hard to read because Telegram's default font is small
// and the three sections all share one screen real estate.
// We now send each section as its own message bubble (Auth
// in message #1, User-scope in #2, Admin in #3). Telegram
// doesn't support font-size changes (the only "big" text
// is the per-message Big Emoji mode), but multiple shorter
// bubbles are easier to scan at default font size than one
// long bubble. The splitMessageMarker sentinel marks the
// boundary; the send path (RealNotifier.reply) splits on
// it and issues separate sendMessage calls.
//
// Rationale: the v0.16.1/v0.16.2 "more HTML" pass left /help
// in plain text, so the catalog's markdown backticks showed up
// as raw `\`<id>\`` characters. The v0.16.2 hotfix for
// /my_rules et al. didn't touch /help because the helper
// would have rejected the literal `<` from the placeholders
// in parse_mode=HTML. The fix is two-pronged:
//   1) catalog: every backtick inside bot.help.* is now
//      `<code>...</code>` (with &, <, > escaped inside),
//   2) reply: tabular <pre> blocks per section so the
//      command column lines up on every Telegram client
//      (Telegram's <pre> uses a fixed-pitch font).
//   3) v0.16.5: split into multiple bubbles so each
//      section gets its own screen on mobile.
func helpReply(env BotEnv) string {
	// 2026-07-16: v0.16.3 — mark HTML so the <b>, <i>,
	// <pre>, <code> in the body render instead of
	// showing as raw source. The "more HTML" pass for
	// the read commands did this; /help is the last
	// big plain-text reply and benefits from the same
	// treatment now that the catalog is HTML-safe.
	markHTMLReply()
	lang := env.Lang

	// Gutter for the command column inside <pre> blocks.
	// 20 chars = max command "/exit_nodes_health" (17) +
	// 3-char right margin. Any future longer command will
	// just overflow into the description column; the gutter
	// only matters for short commands (where the pad keeps
	// descriptions left-aligned).
	const gutter = 20
	padCmd := func(cmd string) string {
		if len(cmd) < gutter {
			return cmd + strings.Repeat(" ", gutter-len(cmd))
		}
		return cmd
	}
	// table renders a <pre> block with header + rule line
	// + data rows. Used for each of the three sections.
	table := func(header string, rows ...string) string {
		var lines []string
		// No header row, no rule line — the section title
		// above the <pre> already labels the columns
		// (the eye learns "first column = command" from
		// the first row). The rule line was nice but it
		// doubled the visual noise for what is a quick
		// command reference.
		for _, r := range rows {
			lines = append(lines, r)
		}
		return "<b>" + header + "</b>\n" + PreLinesRaw(lines...)
	}
	row := func(cmd, desc string) string {
		return padCmd(cmd) + "  " + desc
	}

	// Section: Auth (everyone, even unidentified).
	authRows := []string{
		row("/login", i18n.T(lang, "bot.help.auth_login")),
		row("/start", i18n.T(lang, "bot.help.auth_start")),
		row("/lang", i18n.T(lang, "bot.help.lang")),
		row("/help", i18n.T(lang, "bot.help.common_help")),
		row("/version", i18n.T(lang, "bot.help.common_version")),
		row("/unbind_self", i18n.T(lang, "bot.help.auth_unbind_self")),
	}
	auth := table("🔐 "+i18n.T(lang, "bot.help.section_auth"), authRows...)

	// Section: User-scope (every identified user).
	commonRows := []string{
		row("/my_status", i18n.T(lang, "bot.help.user_top_my_status")),
		row("/my_nodes", i18n.T(lang, "bot.help.user_rest_my_nodes")),
		row("/my_rules", i18n.T(lang, "bot.help.user_top_my_rules")),
		row("/my_quota", i18n.T(lang, "bot.help.user_rest_my_quota")),
		row("/myexitnodes", i18n.T(lang, "bot.help.user_rest_myexitnodes")),
		row("/add_device", i18n.T(lang, "bot.help.user_rest_add_device")),
		row("/add_rule", i18n.T(lang, "bot.help.user_top_add_rule")),
		row("/delrule", i18n.T(lang, "bot.help.user_rest_delrule")),
		row("/clearrules", i18n.T(lang, "bot.help.user_rest_clearrules")),
		row("/setdefaultdevice", i18n.T(lang, "bot.help.user_rest_setdefaultdevice")),
		row("/defaultdevice", i18n.T(lang, "bot.help.user_rest_defaultdevice")),
		row("/setexitnode", i18n.T(lang, "bot.help.user_rest_setexitnode")),
		row("/defaultexitnode", i18n.T(lang, "bot.help.user_rest_defaultexitnode")),
	}
	common := table("✦ "+i18n.T(lang, "bot.help.section_common"), commonRows...)

	// Section: Admin (skyadmin only).
	adminRows := []string{
		row("/status", i18n.T(lang, "bot.help.admin_top_status")),
		row("/nodes", i18n.T(lang, "bot.help.admin_top_nodes")),
		row("/exit_nodes", i18n.T(lang, "bot.help.admin_top_exit_nodes")),
		row("/exit_nodes_health", i18n.T(lang, "bot.help.admin_top_exit_nodes_health")),
		row("/sync_nodes", i18n.T(lang, "bot.help.admin_top_sync_nodes")),
		row("/rules", i18n.T(lang, "bot.help.admin_rest_rules")),
		row("/quota", i18n.T(lang, "bot.help.admin_rest_quota")),
		row("/audit", i18n.T(lang, "bot.help.admin_rest_audit")),
		row("/ack", i18n.T(lang, "bot.help.admin_rest_ack")),
		row("/restart", i18n.T(lang, "bot.help.admin_rest_restart")),
		row("/bind", i18n.T(lang, "bot.help.admin_rest_bind")),
		row("/unbind", i18n.T(lang, "bot.help.admin_rest_unbind")),
	}
	admin := table("🛠 "+i18n.T(lang, "bot.help.section_admin"), adminRows...)

	// 2026-07-16: v0.16.5 — split into multiple
	// bubbles. The first bubble carries the title
	// + subtitle + the Auth section; subsequent
	// bubbles carry the User-scope and Admin
	// sections. Strict-mode locked layout: only
	// the locked note + Auth (single bubble).
	//
	// 3-bubble layout (admin):
	//   #1: <b>title</b> + <i>subtitle</i> + Auth table
	//   #2: User-scope table
	//   #3: Admin table
	//
	// 2-bubble layout (user):
	//   #1: <b>title</b> + <i>subtitle</i> + Auth table
	//   #2: User-scope table
	//
	// 1-bubble layout (locked):
	//   #1: <b>title</b> + <i>subtitle</i> + locked note + Auth

	title := "<b>" + i18n.T(lang, "bot.help.header") + "</b>"
	subtitle := "<i>" + i18n.T(lang, "bot.help.subtitle") + "</i>"

	switch {
	case !env.IsIdentified() && env.StrictMode:
		// Locked layout: single bubble.
		return title + "\n" + subtitle + "\n\n" +
			"🔒 " + i18n.T(lang, "bot.help.strict_locked_note") + "\n\n" + auth
	case !env.IsIdentified() || env.IsAdmin:
		// Admin: 3 bubbles (Auth / User-scope / Admin).
		// The title + subtitle are in the first bubble
		// (so the user knows which command produced
		// this burst of messages). The Auth section
		// follows in the same bubble. The User-scope
		// and Admin sections each get their own bubble.
		first := title + "\n" + subtitle + "\n\n" + auth
		return first + splitMessageMarker + common + splitMessageMarker + admin
	default:
		// User: 2 bubbles (Auth / User-scope).
		first := title + "\n" + subtitle + "\n\n" + auth
		return first + splitMessageMarker + common
	}
}
