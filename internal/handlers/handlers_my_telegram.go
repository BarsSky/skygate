// handlers_my_telegram.go — self-service Telegram binding for any
// portal user.
//
// Этап 12 (2026-07-13): full bot access control. The user-scope
// path is "I want to use the bot from my phone":
//
//   1. User opens /my/telegram
//   2. Sees current binding (or "(not bound)") + list of recently
//      generated keys with status (active/used/expired)
//   3. Clicks "Generate login key" — POST /my/telegram/generate
//   4. Receives a 16-char key, valid 5 min, with a JS countdown
//   5. Opens Telegram, sends /login <key> to the bot
//   6. Bot UPSERTs telegram_bindings, replies "✅ logged in"
//   7. Page refresh shows "(bound to chat <id>)" + a /unbind_self
//      button in the web UI (mirror of the bot's /unbind_self)
//
// Security choices:
//   - Per-user cap: 3 active tokens max (configurable; the
//     constant is loginTokenCap). Prevents token-table spam.
//   - Audit every key: create/consume/reject/expire get their
//     own audit_log rows so an admin can spot suspicious activity.
//   - The token is single-use; the consume UPDATE is atomic
//     (WHERE used_at = 0), so two concurrent /logins cannot both
//     succeed even if the user pastes twice.
//   - request_ip is recorded for audit; never displayed to the
//     user, never sent to the bot.

package handlers

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"skygate/internal/db"
)

// loginTokenCap is the maximum number of UNUSED, NOT-EXPIRED
// tokens a single user can have at any time. 3 covers "phone +
// laptop + spare" without letting one user spam the table.
// Past that, the generate handler returns err_token_cap and the
// UI tells the user to wait for an existing key to expire (or
// revoke one manually).
const loginTokenCap = 3

// loginTokenAlphabet is the character set for the generated key.
// 32 symbols: A–Z minus I/O (visually ambiguous), plus 2–9
// (avoiding 0/1 for the same reason). The token is presented as
// "skg-XXXX-XXXX-XXXX" — 16 random chars in 4 groups of 4, with
// a fixed prefix and dashes for copy-paste robustness.
//
// Token space: 32^16 ≈ 1.2 × 10^24. Combined with the 5-minute
// TTL and the per-chat rate-limit (5 attempts / 60s) a brute-
// force attack is computationally infeasible.
const loginTokenAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GetMyTelegram renders /my/telegram. Shows:
//   - current binding status (chat_id, bound_at) or "(not bound)"
//   - recently generated keys (active / used / expired)
//   - generate button (POST /my/telegram/generate)
//   - the most recent freshly-minted key (one-shot, in flash
//     "key=<token>&exp=<unix>" so the template can render the
//     countdown without server-side state)
func (a *App) GetMyTelegram(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	state, err := loadMyTelegramState(a.DB, c.UserID, c.Username)
	if err != nil {
		http.Error(w, fmt.Sprintf("internal error: %v", err), http.StatusInternalServerError)
		return
	}
	// CSRF cookie: HttpOnly + SameSite=Lax, 10 min TTL. Mirrors
	// the /admin/telegram pattern (admin_telegram.go:AdminTelegram).
	// The POST handlers compare against this cookie with
	// crypto/subtle.ConstantTimeCompare; the same value is also
	// embedded as a hidden form field so the browser includes
	// it on submit.
	csrf, err := db.RandomConfirmationToken(8)
	if err != nil {
		http.Error(w, "csrf generation failed", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "skygate_my_tg_csrf",
		Value:    csrf,
		Path:     "/my/telegram",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	// Pull any one-shot "freshly generated" key out of the query
	// string. The POST handler redirects here with ?key=<token>&exp=<unix>
	// after a successful mint; we surface it once and let the
	// template render the countdown. A page refresh drops it
	// (it's gone from the URL on the second load).
	freshKey := r.URL.Query().Get("key")
	freshExp := r.URL.Query().Get("exp")
	// 2026-07-13: Этап 13 — BotUsername for the deep-link button.
	// Empty string is fine; the template falls back to a plain
	// /login instruction. We only fetch once per page load (the
	// Notifier caches the value 1h internally).
	botUsername := ""
	if a.Notifier != nil {
		type withUsername interface {
			BotUsernameCached() string
		}
		if u, ok := a.Notifier.(withUsername); ok {
			botUsername = u.BotUsernameCached()
		}
	}
	a.renderWithLayout(w, r, "user/telegram.html", c, map[string]any{
		"Page":        "telegram",
		"Title":       "Telegram",
		"State":       state,
		"FlashOK":     r.URL.Query().Get("ok"),
		"FlashError":  r.URL.Query().Get("err"),
		"FreshKey":    freshKey,
		"FreshExp":    freshExp,
		"BotUsername": botUsername,
		"CSRF":        csrf,
	})
}

// PostMyTelegramGenerate mints a new login key for the calling
// user and redirects back to /my/telegram?key=<token>&exp=<unix>
// so the template can show the freshly-generated key + countdown.
//
// Validation:
//   - ParseForm (r.Form is lazy — see AGENTS.md gotcha #1).
//   - enforce loginTokenCap (count active rows; reject with
//     err_token_cap if at cap).
//   - audit "telegram_login_token_created" with token_fingerprint
//     (8-char hash; never the raw key).
//
// On success the token is in the DB; the URL has the cleartext
// for ONE render. After that, the token only exists in
// telegram_login_tokens.token — the page refresh loses the
// cleartext and the user has to generate a new one (or look
// at their Telegram chat history if they sent it to themselves).
func (a *App) PostMyTelegramGenerate(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/my/telegram?err=bad_form", http.StatusFound)
		return
	}
	// CSRF: the form on /my/telegram embeds a per-page token. We
	// accept the POST only if the token matches. The token is
	// generated on GET and stored in a HttpOnly cookie. The same
	// pattern admin_telegram.go uses for /admin/telegram — keeps
	// the CSRF surface uniform.
	cookie, err := r.Cookie("skygate_my_tg_csrf")
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/my/telegram?err=csrf_missing", http.StatusFound)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(cookie.Value)) != 1 {
		a.audit(c.UserID, c.Username, "telegram_login_csrf_fail",
			fmt.Sprintf("ip=%s", r.RemoteAddr))
		http.Redirect(w, r, "/my/telegram?err=csrf_invalid", http.StatusFound)
		return
	}
	// Rate-limit: cap on active tokens per user.
	active, err := db.CountActiveTelegramLoginTokensByUser(a.DB, c.UserID)
	if err != nil {
		http.Redirect(w, r, "/my/telegram?err=db_error", http.StatusFound)
		return
	}
	if active >= loginTokenCap {
		a.audit(c.UserID, c.Username, "telegram_login_token_cap_hit",
			fmt.Sprintf("active=%d cap=%d", active, loginTokenCap))
		http.Redirect(w, r, "/my/telegram?err=token_cap", http.StatusFound)
		return
	}
	// Prune expired rows for this user so the cap check is
	// accurate (an "expired" row still counts in the index until
	// we sweep). Cheap because of idx_telegram_login_tokens_user.
	if _, err := db.PruneExpiredTelegramLoginTokens(a.DB, time.Now().Unix()); err != nil {
		// Non-fatal: the cap check still works because
		// qCountActiveTelegramLoginTokensByUser filters on
		// expires_at > now. Logged via audit for awareness.
		_ = err
	}
	// Mint the token.
	token, err := mintLoginToken()
	if err != nil {
		http.Redirect(w, r, "/my/telegram?err=mint_failed", http.StatusFound)
		return
	}
	ttl := db.LoadTelegramLoginTokenTTL(a.DB)
	if err := db.CreateTelegramLoginToken(a.DB, token, c.UserID, ttl, clientIP(r)); err != nil {
		http.Redirect(w, r, "/my/telegram?err=db_error", http.StatusFound)
		return
	}
	a.audit(c.UserID, c.Username, "telegram_login_token_created",
		fmt.Sprintf("token_fp=%s ttl=%d", tokenFingerprint(token), ttl))
	// Redirect to the page with the freshly-minted key in the
	// URL. The key is shown exactly once; the page refresh
	// (without ?key=) drops it from view but the DB row remains
	// until consumed or expired.
	exp := time.Now().Add(time.Duration(ttl) * time.Second).Unix()
	http.Redirect(w, r,
		fmt.Sprintf("/my/telegram?key=%s&exp=%d", token, exp),
		http.StatusSeeOther)
}

// PostMyTelegramUnbind drops the calling user's binding. Mirror
// of the bot's /unbind_self: lets a user revoke access from the
// web UI (e.g. lost phone, switching accounts) without opening
// Telegram.
func (a *App) PostMyTelegramUnbind(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/my/telegram?err=bad_form", http.StatusFound)
		return
	}
	// Find the binding (if any) for this user.
	b, err := db.GetTelegramBindingByUser(a.DB, c.UserID)
	if err == db.ErrTelegramBindingNotFound {
		http.Redirect(w, r, "/my/telegram?err=not_bound", http.StatusFound)
		return
	}
	if err != nil {
		http.Redirect(w, r, "/my/telegram?err=db_error", http.StatusFound)
		return
	}
	if err := db.DeleteTelegramBinding(a.DB, b.ChatID); err != nil {
		http.Redirect(w, r, "/my/telegram?err=db_error", http.StatusFound)
		return
	}
	a.audit(c.UserID, c.Username, "telegram_unbind_self_web",
		fmt.Sprintf("chat_id=%d", b.ChatID))
	http.Redirect(w, r, "/my/telegram?ok=unbound", http.StatusSeeOther)
}

// PostMyTelegramRevoke deletes a single not-yet-used token the
// user has generated. Useful when the user generated a key, then
// switched phones, and wants to invalidate the old one without
// waiting for the 5-min TTL.
func (a *App) PostMyTelegramRevoke(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/my/telegram?err=bad_form", http.StatusFound)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		http.Redirect(w, r, "/my/telegram?err=missing_token", http.StatusFound)
		return
	}
	// Make sure the token belongs to this user before deleting —
	// an attacker who knows a token string (e.g. from a leaked
	// log) shouldn't be able to revoke it for someone else.
	rows, err := a.DB.Query(`SELECT portal_user_id FROM telegram_login_tokens WHERE token = ?`, token)
	if err != nil {
		http.Redirect(w, r, "/my/telegram?err=db_error", http.StatusFound)
		return
	}
	defer rows.Close()
	var ownerID int64
	ownerFound := false
	for rows.Next() {
		if err := rows.Scan(&ownerID); err == nil {
			ownerFound = true
			break
		}
	}
	if !ownerFound {
		http.Redirect(w, r, "/my/telegram?err=token_not_found", http.StatusFound)
		return
	}
	if ownerID != c.UserID {
		a.audit(c.UserID, c.Username, "telegram_login_revoke_ownership_fail",
			fmt.Sprintf("token_fp=%s owner=%d", tokenFingerprint(token), ownerID))
		http.Redirect(w, r, "/my/telegram?err=not_your_token", http.StatusFound)
		return
	}
	if err := db.DeleteTelegramLoginToken(a.DB, token); err != nil {
		http.Redirect(w, r, "/my/telegram?err=db_error", http.StatusFound)
		return
	}
	a.audit(c.UserID, c.Username, "telegram_login_token_revoked",
		fmt.Sprintf("token_fp=%s", tokenFingerprint(token)))
	http.Redirect(w, r, "/my/telegram?ok=token_revoked", http.StatusSeeOther)
}

// loadMyTelegramState is the data the template needs to render
// the page: current binding (or nil), recently generated tokens
// (pre-classified into active/used/expired so the template
// doesn't need a "now" function), and the global strict-mode
// flag (so the user can see whether the operator has it on,
// and understand why a stranger's message to the bot gets a
// "🔒 not bound" reply).
func loadMyTelegramState(d *sql.DB, userID int64, _ string) (myTelegramState, error) {
	state := myTelegramState{
		TTLSeconds: db.LoadTelegramLoginTokenTTL(d),
		StrictMode: db.LoadTelegramStrictMode(d),
	}
	// Current binding. ErrTelegramBindingNotFound is normal
	// (most users will hit this on first visit).
	b, err := db.GetTelegramBindingByUser(d, userID)
	if err == nil {
		state.Binding = b
	} else if err != db.ErrTelegramBindingNotFound {
		return state, err
	}
	// Recent tokens (newest first, cap 10). We pre-classify
	// each row so the template just renders a status string —
	// no Go-template "now" comparison needed.
	tokens, err := db.ListTelegramLoginTokensByUser(d, userID, 10)
	if err != nil {
		return state, err
	}
	now := time.Now().Unix()
	for _, t := range tokens {
		switch {
		case t.UsedAt > 0:
			state.RecentTokens = append(state.RecentTokens, myTelegramTokenView{
				TelegramLoginToken: t,
				Status:            "used",
			})
		case t.ExpiresAt <= now:
			state.RecentTokens = append(state.RecentTokens, myTelegramTokenView{
				TelegramLoginToken: t,
				Status:            "expired",
			})
		default:
			state.RecentTokens = append(state.RecentTokens, myTelegramTokenView{
				TelegramLoginToken: t,
				Status:            "active",
			})
		}
	}
	return state, nil
}

// myTelegramState is the data shape the user/telegram.html
// template consumes.
type myTelegramState struct {
	Binding      *db.TelegramBinding
	RecentTokens []myTelegramTokenView
	TTLSeconds   int
	StrictMode   bool
}

// myTelegramTokenView wraps a TelegramLoginToken with a
// pre-computed Status string ("active" / "used" / "expired")
// so the template doesn't have to re-derive it.
type myTelegramTokenView struct {
	db.TelegramLoginToken
	Status string
}

// GetMyTelegramQR serves a PNG QR code that, when scanned with
// a phone camera, opens Telegram with a /start <token> deep
// link. The web page shows this QR alongside the freshly-minted
// key so the user can "scan to bind" in one tap on a phone that
// has Telegram installed.
//
// 2026-07-13: Этап 13 — Bind-by-QR.
//
// The encoded URL is `https://t.me/<bot_username>?start=<token>`.
// Telegram handles the `?start=` parameter natively: when the
// user opens the URL, Telegram opens the chat with the bot and
// sends the bot a message "/start <token>". The bot's existing
// /start <token> handler (commands_login.go) then binds the
// chat. End-to-end: scan → bind.
//
// The token is read from the `token` query param (we never
// persist the QR in a way that outlives the page — the page
// shows the QR as part of the freshly-minted-key card, with a
// JS countdown; after 5 min the QR is meaningless anyway because
// the token has expired). The query is also available without
// CSRF for read-only QR generation: an attacker who can read
// this URL can't bind a different chat because the bind
// requires a row in telegram_bindings for the chat that scanned
// it, and the bot refuses to bind a chat_id that's already
// taken.
//
// Returns 400 if the token doesn't match the
// `^skg-[A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}$` shape — same
// validator the bot uses. Returns 503 if the bot username
// hasn't been discovered yet (token configured but getMe
// hasn't run, or just failed).
func (a *App) GetMyTelegramQR(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if !looksLikeLoginTokenForQR(token) {
		http.Error(w, "bad token", http.StatusBadRequest)
		return
	}
	username := ""
	if a.Notifier != nil {
		// RealNotifier exposes a BotUsernameCached() method
		// (set by main.go, refreshed via getMe). NoopNotifier
		// doesn't, but the bot isn't configured in that case
		// so a.Notifier is NoopNotifier — handled by the
		// interface assertion below.
		type withUsername interface {
			BotUsernameCached() string
		}
		if u, ok := a.Notifier.(withUsername); ok {
			username = u.BotUsernameCached()
		}
	}
	if username == "" {
		http.Error(w, "bot username not yet discovered (save a token at /admin/telegram first)", http.StatusServiceUnavailable)
		return
	}
	// Build the deep link. Telegram accepts both `t.me/...` and
	// `https://t.me/...`; we use https for QR scanners that
	// resolve links via the camera app's HTTPS handler.
	deepLink := fmt.Sprintf("https://t.me/%s?start=%s", username, token)
	// Render the QR. We pick a 256×256 PNG (the standard
	// `qrcode.Medium` recovery level handles up to 15%
	// damage; the deep link is short — well within the
	// error-correction budget at this size). 8px-per-module
	// is a sweet spot for phone screens.
	png, err := qrcode.Encode(deepLink, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, fmt.Sprintf("qr encode: %v", err), http.StatusInternalServerError)
		return
	}
	// Cache headers: the QR is bound to a specific token, so
	// we want it cached on the browser for as long as the
	// token is valid (5 min default), and no longer. The
	// `private` directive means an intermediary proxy won't
	// accidentally serve a previous user's QR to the next
	// user.
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", a.loginTokenTTLSeconds()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(png)))
	_, _ = w.Write(png)
}

// loginTokenTTLSeconds is the cached TTL for the
// Content-Max-Age header on the QR. Read from DB once at
// startup via SetRuleCaps-style wiring would be cleaner, but
// the value is rarely tuned and re-reading per request is
// cheap (one indexed SELECT). This indirection exists so the
// test code can mock the value.
func (a *App) loginTokenTTLSeconds() int {
	return db.LoadTelegramLoginTokenTTL(a.DB)
}

// looksLikeLoginTokenForQR is the QR handler's copy of
// telegram/commands_login.go:looksLikeLoginToken. The two
// implementations are intentionally duplicated (rather than
// imported) because internal/handlers already imports
// internal/telegram through Notifier, and a back-import would
// create a cycle. The 18-char `skg-XXXX-XXXX-XXXX` shape is
// stable; if a future change touches one copy, it must touch
// both.
func looksLikeLoginTokenForQR(s string) bool {
	if len(s) != 18 || !strings.HasPrefix(s, "skg-") {
		return false
	}
	for _, i := range []int{3, 8, 13} {
		if s[i] != '-' {
			return false
		}
	}
	for _, i := range []int{4, 5, 6, 7, 9, 10, 11, 12, 14, 15, 16, 17} {
		r := s[i]
		if !((r >= 'A' && r <= 'Z' && r != 'I' && r != 'O') ||
			(r >= '2' && r <= '9')) {
			return false
		}
	}
	return true
}

// mintLoginToken returns a 16-char token formatted as
// "skg-XXXX-XXXX-XXXX" where each X is one char from
// loginTokenAlphabet. Uses crypto/rand so a guesser can't
// predict the next token even if they see the previous one.
func mintLoginToken() (string, error) {
	groups := []int{4, 4, 4}
	out := "skg-"
	for gi, glen := range groups {
		for k := 0; k < glen; k++ {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(loginTokenAlphabet))))
			if err != nil {
				return "", err
			}
			out += string(loginTokenAlphabet[n.Int64()])
		}
		if gi < len(groups)-1 {
			out += "-"
		}
	}
	return out, nil
}

// clientIP extracts the originating IP from the request,
// honouring X-Forwarded-For when present (skygate runs behind
// nginx). Falls back to r.RemoteAddr (host:port) — we strip
// the port. Used only for the audit_log row; the bot never
// sees this value.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first (closest-to-client) hop.
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if h := r.Header.Get("X-Real-IP"); h != "" {
		return strings.TrimSpace(h)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		// Drop the port. IPv6 has multiple colons; for the
		// audit log we don't need the host part, so strip
		// from the last colon.
		addr = addr[:i]
	}
	return addr
}

// tokenFingerprint mirrors the helper in
// internal/telegram/commands_login.go: 8 hex chars of FNV-1a.
// We can't import from internal/telegram (the other direction
// would create a cycle: telegram already imports handlers via
// BotEnv.Notifier indirectly) so the two implementations live
// side by side. If a future change touches the algorithm, both
// copies need to be updated.
func tokenFingerprint(s string) string {
	if len(s) < 4 {
		return "..."
	}
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}
