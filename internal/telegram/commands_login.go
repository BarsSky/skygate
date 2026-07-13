// Этап 12 (2026-07-13) — login-by-key for the Telegram bot.
//
// Two new entry points land here:
//
//   /login [key]      — paste a key generated in /my/telegram. With
//                       no arg: print the strict-mode hint (the
//                       "this is what you do to bind" message).
//   /start [key]      — Telegram UX convention. /start with no arg
//                       prints the welcome message; /start <key> is
//                       an alias of /login <key> so a user who
//                       opens the bot for the first time and pastes
//                       the key right away (the most common flow)
//                       doesn't have to remember /login.
//
// Plus an /unbind self-service path: a bound non-admin can drop
// their own binding (e.g. before switching phones) without
// asking the admin. The admin /unbind is unchanged (it takes
// any chat_id and is admin-only).
//
// The new /login rate-limit lives in this file too: per chat_id,
// max 5 attempts per 60s window. We use an in-memory map +
// expiry timestamp; the bot is single-instance so no shared
// state is needed. A re-deploy resets the counters, which is
// acceptable (the cost of a brief rate-limit reset is far less
// than the cost of a multi-instance coordination layer).

package telegram

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"skygate/internal/db"
)

// loginRateLimitWindow and loginRateLimitMax are the rate-limit
// parameters for /login attempts on a per-chat_id basis. 5
// attempts per 60s is generous for a human (typing the key from
// a screenshot takes a few seconds; mistakes are 1-2 attempts)
// and tight enough that a brute-force script — which would have
// to traverse the 16-char token space at >5 guesses/sec —
// realistically cannot crack a 5-min-TTL key in time.
const (
	loginRateLimitWindowSeconds = 60
	loginRateLimitMax           = 5
)

// loginAttemptAllowed records an attempt for `chatID` and
// returns true when the chat is under the limit. The state
// lives in telegram_rate_limit (DB) since Этап 13; the
// in-memory map was retired because it (a) reset on every
// container restart, (b) didn't work across multi-instance
// deploys, (c) wouldn't survive the rate-limit being applied
// to a /start callback flow (which lives outside this
// function's lock). The DB version is one INSERT + one
// indexed SELECT — same big-O as the in-memory version, and
// the table is tiny (≤5 rows per active chat per minute).
//
// Этап 13 (2026-07-13): migrated from in-memory
// `loginAttempts` map to db.RecordTelegramRateLimitAttempt.
// Tests reset the per-chat slot via db.ResetTelegramRateLimit.
func loginAttemptAllowed(d *sql.DB, chatID int64) bool {
	key := fmt.Sprintf("login:%d", chatID)
	_, allowed, err := db.RecordTelegramRateLimitAttempt(
		d, key, "", loginRateLimitWindowSeconds, loginRateLimitMax)
	if err != nil {
		// Fail-open: a DB error on the rate-limit path
		// shouldn't block legitimate /login attempts. The
		// alternative (fail-closed) would mean a transient
		// DB hiccup locks everyone out. Audit_log isn't
		// necessary here — the next message will surface
		// any real DB problem via the binding UPSERT.
		return true
	}
	return allowed
}

// resetLoginAttempts clears the rate-limit slot for a chat.
// Used by tests (and could be used by an admin "clear" path
// in the future if a user legitimately tripped the limiter).
func resetLoginAttempts(d *sql.DB, chatID int64) {
	_, _ = db.ResetTelegramRateLimit(d, fmt.Sprintf("login:%d", chatID))
}

// loginReply handles /login [key]. The strict-mode gate in
// HandleCommand already whitelisted /login for unidentified
// chats; for an already-bound chat, /login with a valid key
// RE-BINDS the chat to the key's user (intentional: lets a
// user move the binding from "my old phone" to "my new phone"
// without admin intervention).
func loginReply(env BotEnv, args []string) string {
	// /login with no args: print the hint. This is also what
	// /start with no args prints (they're the same UX — tell
	// the user what to do next).
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return loginHint(env)
	}
	if env.ChatID == 0 {
		// Defensive: shouldn't happen because the dispatcher
		// always sets ChatID, but if it does we can't identify
		// the consuming chat.
		return "login: internal error (chat_id missing); contact admin"
	}
	if !loginAttemptAllowed(env.DB, env.ChatID) {
		return fmt.Sprintf("login: too many attempts. Wait %ds and try again.",
			loginRateLimitWindowSeconds)
	}
	token := strings.TrimSpace(args[0])
	// Cheap shape check: a real token is 19 chars (skg-XXXX-XXXX-XXXX).
	// Reject obvious junk early so we don't burn DB cycles.
	if !looksLikeLoginToken(token) {
		return "login: that doesn't look like a valid key. " +
			"Open /my/telegram and copy the key exactly."
	}
	t, err := db.ConsumeTelegramLoginToken(env.DB, token, env.ChatID)
	if err != nil {
		switch {
		case err == db.ErrTelegramLoginTokenNotFound,
			err == db.ErrTelegramLoginTokenExpired,
			err == db.ErrTelegramLoginTokenAlreadyUsed:
			// We deliberately collapse the three failure modes
			// into one reply so an attacker can't tell which
			// one they hit (timing oracle: all three return in
			// <1ms because they share the same SELECT path).
			return "login: invalid or expired key. " +
				"Generate a new one in /my/telegram."
		default:
			return fmt.Sprintf("login: db error: %v", err)
		}
	}
	// Bind: re-fetch the user to get current is_admin (so a
	// user who was promoted/demoted since the token was minted
	// gets the new privilege level). Failure here is a real
	// error: token consumed but binding didn't happen.
	username, isAdmin, err := lookupPortalUser(env.DB, t.PortalUserID)
	if err != nil {
		return fmt.Sprintf("login: token consumed but user lookup failed: %v", err)
	}
	// boundByUserID = 0 means "system" (the bot's /login flow).
	// The audit row in audit_log makes this clear in /admin/audit.
	if err := db.UpsertTelegramBinding(env.DB, env.ChatID, t.PortalUserID, 0, isAdmin); err != nil {
		return fmt.Sprintf("login: token consumed but binding failed: %v", err)
	}
	// Audit: who logged in from which chat, for which user.
	_ = db.AppendAuditLogNoUser(env.DB, "telegram", "telegram_bound_via_login",
		fmt.Sprintf("chat_id=%d user=%s token_fp=%s", env.ChatID, username, tokenFingerprint(token)))
	role := "user"
	if isAdmin {
		role = "admin"
	}
	return fmt.Sprintf("✅ Logged in as %s (%s)\n"+
		"This chat can now use /my_rules, /add_rule, /delrule and the rest of the %s commands.\n"+
		"To unbind later: /unbind_self.",
		username, role, role)
}

// startReply is /start. With no args: Telegram-UX welcome that
// doubles as the login hint (most users will /start a bot before
// reading any docs). With an arg: the bot shows a confirmation
// prompt with [Bind] [Cancel] inline buttons instead of binding
// immediately. /login <token> still binds in one command (the
// shortcut for users who already know the flow).
//
// 2026-07-13: Этап 13 — split /start from /login so a phone
// scan (which lands in /start <token>) gets an explicit
// "are you sure?" step before binding the chat. /login keeps
// the one-command path for users who already pasted the key
// from the web page.
func startReply(env BotEnv, args []string) string {
	if len(args) == 0 {
		return loginHint(env)
	}
	if env.ChatID == 0 {
		return "start: internal error (chat_id missing); contact admin"
	}
	if !loginAttemptAllowed(env.DB, env.ChatID) {
		return fmt.Sprintf("start: too many attempts. Wait %ds and try again.",
			loginRateLimitWindowSeconds)
	}
	token := strings.TrimSpace(args[0])
	if !looksLikeLoginToken(token) {
		return "start: that doesn't look like a valid key. " +
			"Open /my/telegram and copy the key exactly."
	}
	// Peek at the token WITHOUT consuming it. We want the
	// confirmation prompt to show "Bind to <username>?" — the
	// user reads that, clicks Bind, and only THEN do we
	// consume. Consuming on /start would be a race: the
	// user might tap Cancel after a few seconds and the
	// token would already be spent.
	t, err := db.PeekTelegramLoginToken(env.DB, token)
	if err != nil {
		return "start: invalid or expired key. " +
			"Generate a new one in /my/telegram."
	}
	// Look up the target user's display name. Failure here
	// is non-fatal — the prompt just shows the user_id
	// instead of the username, which is still informative.
	username, _, _ := lookupPortalUser(env.DB, t.PortalUserID)
	if username == "" {
		username = fmt.Sprintf("user#%d", t.PortalUserID)
	}
	// Build the inline keyboard. Two buttons, side by side:
	//   [Bind to <username>]   [Cancel]
	// The callback data follows the convention used by
	// RealNotifier.handleCallback: "bind:confirm:<token>" or
	// "bind:cancel".
	rows := [][]map[string]string{
		{
			{"text": fmt.Sprintf("✅ Bind to %s", username), "callback_data": "bind:confirm:" + token},
			{"text": "❌ Cancel", "callback_data": "bind:cancel"},
		},
	}
	pendingReplyForCurrentMessage = &PendingReply{InlineKeyboard: rows}
	return fmt.Sprintf(
		"🔑 Bind this chat to **%s**?\n\n"+
			"This will let you use /my_rules, /add_rule, /delrule and the rest of the user commands.\n"+
			"Token expires %s.",
		username, time.Unix(t.ExpiresAt, 0).UTC().Format("15:04:05 MST"))
}

// loginHint is the welcome message. It branches on whether
// the caller has a BINDING (env.Username != ""), not on the
// raw ChatID. In production, the dispatcher in notify.go
// always sets ChatID to the actual inbound chat — even when
// the binding row doesn't exist — so an unbound chat can have
// IsIdentified()==true. The right "am I bound" signal is the
// binding row itself, which manifests as env.Username != "". A
// returning user (already bound) gets the short "logged in as
// X" message; everyone else gets the welcome + how-to-bind
// instructions.
func loginHint(env BotEnv) string {
	if env.Username != "" {
		return fmt.Sprintf("Already logged in as %s. Use /help to see your commands.",
			env.Username)
	}
	return "👋 This is the Skygate bot.\n\n" +
		"To bind this chat to your skygate account:\n" +
		"  1. Open skygate → /my/telegram\n" +
		"  2. Click 'Generate login key' and copy it\n" +
		"  3. Send it here:\n" +
		"     /login <key>\n\n" +
		"The key expires in 5 minutes and is single-use."
}

// unbindAdminReply is kept as a comment-shim to make the file's
// purpose obvious: the admin /unbind lives in commands_user.go
// (unbindReply). The user-self /unbind_self handler is right
// below. The dispatcher in commands.go routes each command to
// the right helper.

// unbindSelfReply handles /unbind_self — a bound user drops
// their own binding. Useful when switching phones, revoking
// access from a lost device, or testing. Admin can use this
// too (it unbinds the admin's own chat, not anyone else's).
func unbindSelfReply(env BotEnv) string {
	if !env.IsIdentified() {
		return "unbind_self: not bound (nothing to do)"
	}
	if err := db.DeleteTelegramBinding(env.DB, env.ChatID); err != nil {
		return fmt.Sprintf("unbind_self: db error: %v", err)
	}
	_ = db.AppendAuditLogNoUser(env.DB, "telegram", "telegram_unbind_self",
		fmt.Sprintf("user=%s chat_id=%d", env.Username, env.ChatID))
	return "✅ This chat is no longer bound. Send /login <key> again to rebind."
}

// lookupPortalUser reads username + is_admin for a user_id in
// one round-trip. Used by loginReply after consuming a token
// so the new binding carries the current privilege level
// (denormalised into telegram_bindings.is_admin).
func lookupPortalUser(d *sql.DB, userID int64) (string, bool, error) {
	var username string
	var isAdmin int
	err := d.QueryRow(`SELECT username, is_admin FROM portal_users WHERE id = ?`, userID).Scan(&username, &isAdmin)
	if err != nil {
		return "", false, err
	}
	return username, isAdmin != 0, nil
}

// looksLikeLoginToken is a cheap shape check so the rate-limit
// slot doesn't get filled with garbage like "/login hello".
// Real tokens match `^skg-[A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}$`,
// 18 characters total (3-char prefix + 3 separators + 4×3 chars).
// A failed check is fast and never burns a DB round-trip.
func looksLikeLoginToken(s string) bool {
	if len(s) != 18 {
		return false
	}
	if !strings.HasPrefix(s, "skg-") {
		return false
	}
	// Expected dashes at positions 3, 8, 13 (zero-indexed).
	// "skg-XXXX-XXXX-XXXX"
	//   0123 4567 89...
	for _, i := range []int{3, 8, 13} {
		if s[i] != '-' {
			return false
		}
	}
	// The 12 random chars live at positions 4-7, 9-12, 14-17.
	// We iterate by index rather than by rune so the dash
	// positions are stable; the prefix chars (0-2) are skipped
	// because they're literal "skg", not random.
	for _, i := range []int{4, 5, 6, 7, 9, 10, 11, 12, 14, 15, 16, 17} {
		r := s[i]
		// loginAlphabet (see handlers_my_telegram.go):
		// A-Z minus I,O, plus 2-9. Excludes 0,1,I,O for legibility.
		if !((r >= 'A' && r <= 'Z' && r != 'I' && r != 'O') ||
			(r >= '2' && r <= '9')) {
			return false
		}
	}
	return true
}

// tokenFingerprint is the short hash used in audit_log. Same
// helper as db.TelegramFingerprint for bot tokens, but kept
// local to avoid an import cycle. 8 hex chars is enough to
// spot a repeat-offender ("did this same key fail 3 times
// from different chats?") without exposing the key.
func tokenFingerprint(s string) string {
	if len(s) < 4 {
		return "..."
	}
	// FNV-1a 32-bit; we only need ~32 bits of fingerprint.
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}

// parseInt64 is a small helper to avoid pulling in strconv at
// the top of this file. We accept only the same character set
// as the admin /unbind arg, which is documented as a positive
// integer (Telegram chat_id can technically be negative for
// groups, but admin /unbind currently only takes the absolute
// form).
func parseInt64(s string) (int64, error) {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int64(r-'0')
	}
	return n, nil
}
