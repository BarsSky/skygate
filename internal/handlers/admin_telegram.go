package handlers

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/telegram"
)

// Telegram admin UI lives at /admin/telegram (GET) and /admin/telegram
// (POST). It is admin-only.
//
// Flash messages are passed via redirect query parameters
// (?saved=ok|err&msg=...) instead of cookies — every other admin page in
// this codebase follows that pattern and we keep consistent here.

func (a *App) AdminTelegram(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	state := a.loadTelegramUIState()
	// 2026-07-14: Этап 14 v2 — Tailscale reachability probe.
	// Runs synchronously on every GET so the banner is always
	// current. 5s timeout via the probe function. We only run it
	// when the bot is configured (token is set); otherwise the
	// banner shows "save a token to enable the probe" instead
	// of attempting an unauthenticated request.
	if state.Configured {
		token, _, _, _ := db.LoadTelegramToken(a.DB)
		state.Probe = probeTelegramAPI(r.Context(), token)
	}
	csrf, err := db.RandomConfirmationToken(8)
	if err != nil {
		http.Error(w, "csrf generation failed", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "skygate_tg_csrf",
		Value:    csrf,
		Path:     "/admin/telegram",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	a.renderWithLayout(w, r, "admin/telegram.html", c, map[string]any{
		"Page":         "admin/telegram",
		"Title":        "Telegram",
		"State":        state,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
		"CSRF":         csrf,
	})
}

func (a *App) AdminTelegramPost(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		a.redirectWithFlash(w, r, "", fmt.Sprintf("Ошибка парсинга формы: %s", err.Error()))
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	cookie, err := r.Cookie("skygate_tg_csrf")
	if err != nil || cookie.Value == "" {
		a.redirectWithFlash(w, r, "", "CSRF-cookie отсутствует — обновите страницу и повторите")
		return
	}
	submitted := r.FormValue("csrf")
	if subtle.ConstantTimeCompare([]byte(submitted), []byte(cookie.Value)) != 1 {
		a.audit(c.UserID, c.Username, "telegram_csrf_fail",
			fmt.Sprintf("action=%s ip=%s", action, r.RemoteAddr))
		a.redirectWithFlash(w, r, "", "Неверный CSRF-токен — обновите страницу и повторите")
		return
	}
	switch action {
	case "save":
		a.handleTelegramSave(w, r, c)
	case "test":
		a.handleTelegramTest(w, r, c)
	case "rotate":
		a.handleTelegramRotate(w, r, c)
	case "disable":
		a.handleTelegramDisable(w, r, c)
	case "strict":
		// 2026-07-13: Этап 12 — toggle strict mode. The form
		// submits a single checkbox; checked means "enable".
		// The handler reads the checkbox's presence to decide.
		a.handleTelegramStrict(w, r, c)
	default:
		a.redirectWithFlash(w, r, "", "Неизвестное действие: "+action)
	}
}

func (a *App) handleTelegramSave(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	token := strings.TrimSpace(r.FormValue("bot_token"))
	chatID := strings.TrimSpace(r.FormValue("chat_id"))
	if token == "" && chatID == "" {
		a.redirectWithFlash(w, r, "", "Заполните хотя бы одно поле (токен или chat_id)")
		return
	}
	if token != "" && !looksLikeTelegramBotToken(token) {
		a.redirectWithFlash(w, r, "", "Токен выглядит не как BotFather token: ожидается '<id>:<secret>'")
		return
	}
	if chatID != "" && !looksLikeTelegramChatID(chatID) {
		a.redirectWithFlash(w, r, "", "chat_id должен быть числом (например 12345) или -100… для супергруппы")
		return
	}
	if err := db.SaveTelegramToken(a.DB, token, chatID); err != nil {
		a.redirectWithFlash(w, r, "", "Не удалось сохранить: "+err.Error())
		return
	}
	mask := ""
	if token != "" {
		mask = db.TelegramFingerprint(token)
	} else {
		existing, _, _, _ := db.LoadTelegramToken(a.DB)
		mask = db.TelegramFingerprint(existing)
	}
	a.audit(c.UserID, c.Username, "telegram_save",
		fmt.Sprintf("token=%s chat=%s", mask, redactChatID(chatID, token, c)))
	writeFlashRedirect(w, r, fmt.Sprintf("Сохранено. Токен: %s. Проверьте кнопкой «Отправить тест».", mask))
}

func (a *App) handleTelegramTest(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	_, _, ok, err := db.LoadTelegramToken(a.DB)
	if err != nil {
		a.redirectWithFlash(w, r, "", "Ошибка чтения из БД: "+err.Error())
		return
	}
	if !ok {
		a.redirectWithFlash(w, r, "", "Сначала сохраните токен и chat_id")
		return
	}
	subject := strings.TrimSpace(r.FormValue("test_subject"))
	body := strings.TrimSpace(r.FormValue("test_body"))
	if subject == "" {
		subject = "skygate test"
	}
	if body == "" {
		body = "Telegram notification channel is operational. Sent from admin → telegram page."
	}

	// 2026-07-11: route the test message through app.Notifier (Go-native
	// HTTP) instead of shelling out to curl. No dep on /usr/bin/curl,
	// same code path as real notifications, errors logged by RealNotifier.
	text := formatTelegramMessage(r.Host, subject, body)
	if a.Notifier == nil {
		a.redirectWithFlash(w, r, "", "Notifier не инициализирован — перезапустите skygate")
		return
	}
	if _, isNoop := a.Notifier.(telegram.NoopNotifier); isNoop {
		// Should not happen because the form button is disabled when !Configured,
		// but guard anyway in case of a race.
		a.redirectWithFlash(w, r, "", "Бот не сконфигурирован — Notifier в no-op режиме")
		return
	}
	a.Notifier.SendTelegram(text)
	// We can't tell from this interface whether the HTTP POST succeeded;
	// audit as "telegram_test_sent" and let the operator verify in Telegram.
	// The RealNotifier already logs the Telegram response on failure.
	a.audit(c.UserID, c.Username, "telegram_test_sent", subject)
	writeFlashRedirect(w, r, "Сообщение отправлено. Проверьте Telegram.")
}

func (a *App) handleTelegramRotate(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	if r.FormValue("confirm") != "yes" {
		a.redirectWithFlash(w, r, "", "Поставьте галочку подтверждения для rotate")
		return
	}
	if err := db.DeleteTelegramToken(a.DB); err != nil {
		a.redirectWithFlash(w, r, "", "Не удалось очистить старый токен: "+err.Error())
		return
	}
	a.audit(c.UserID, c.Username, "telegram_rotate", "")
	writeFlashRedirect(w, r, "Старый токен удалён. Сохраните новый.")
}

func (a *App) handleTelegramDisable(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	if r.FormValue("confirm") != "yes" {
		a.redirectWithFlash(w, r, "", "Поставьте галочку подтверждения для disable")
		return
	}
	if err := db.DeleteTelegramToken(a.DB); err != nil {
		a.redirectWithFlash(w, r, "", "Ошибка при удалении: "+err.Error())
		return
	}
	a.audit(c.UserID, c.Username, "telegram_disable", "")
	writeFlashRedirect(w, r, "Telegram отключён. Уведомления будут писаться в ~/.skygate-notify.log")
}

// handleTelegramStrict (Этап 12, 2026-07-13) toggles strict
// mode in global_settings.telegram.strict_mode. The bot reads
// this on every incoming message, so the change takes effect
// within the next poll (≤2s, the configured poll interval).
//
// "Strict mode" is a one-way ratchet in spirit: enabling it is
// safe (it only blocks chats that have no row in
// telegram_bindings, i.e. nobody who was using the bot before),
// disabling it requires a confirmation checkbox so a stray
// "save" click doesn't silently re-open the bot to strangers.
// We accept the "confirm" field on enable too — same UX, same
// pattern as rotate/disable.
//
// We log the old value in the audit row so the timeline shows
// the transition (off → on vs on → off) without needing to
// query the DB at audit-read time.
func (a *App) handleTelegramStrict(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	if r.FormValue("confirm") != "yes" {
		a.redirectWithFlash(w, r, "", "Поставьте галочку подтверждения для strict mode")
		return
	}
	want := r.FormValue("enabled") == "1"
	old := db.LoadTelegramStrictMode(a.DB)
	if want == old {
		// No-op: still want to confirm so the operator's
		// intent is recorded (someone might have submitted
		// the form by accident).
		writeFlashRedirect(w, r, "Strict mode already in the requested state.")
		return
	}
	if err := db.SaveTelegramStrictMode(a.DB, want); err != nil {
		a.redirectWithFlash(w, r, "", "Ошибка при сохранении: "+err.Error())
		return
	}
	state := "off"
	if want {
		state = "on"
	}
	a.audit(c.UserID, c.Username, "telegram_strict_mode_changed",
		fmt.Sprintf("from=%s to=%s", boolToOnOff(old), state))
	writeFlashRedirect(w, r, fmt.Sprintf("Strict mode %s. Bot will read the new state within 2s.", state))
}

// boolToOnOff renders a bool as "on" / "off" for the audit row.
func boolToOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// telegramUIState is the shape the template consumes.
type telegramUIState struct {
	Configured bool
	TokenFP    string
	ChatID     string
	UpdatedAt  string
	// 2026-07-13: Этап 12 — strict mode toggle. When true, the
	// bot rejects any chat that has no row in telegram_bindings
	// (with a small whitelist for /help /version /login /start).
	// Loaded from global_settings on every GET so the operator
	// sees the current value without a refresh dance.
	StrictMode    bool
	LoginTokenTTL int
	// 2026-07-14: Этап 14 v2 — Tailscale reachability probe.
	// Probe.State is the discrete outcome (unreachable / ok_direct
	// / ok_relay); the template renders a banner that matches.
	// Probe is the zero value (State=unreachable, Message="")
	// when the bot isn't configured — the template treats that
	// case as "no probe yet" rather than as a failure.
	Probe TelegramProbeResult
}

func (a *App) loadTelegramUIState() telegramUIState {
	token, chatID, ok, err := db.LoadTelegramToken(a.DB)
	state := telegramUIState{
		LoginTokenTTL: db.LoadTelegramLoginTokenTTL(a.DB),
		StrictMode:    db.LoadTelegramStrictMode(a.DB),
	}
	if err != nil || !ok {
		return state
	}
	state.Configured = true
	state.TokenFP = db.TelegramFingerprint(token)
	state.ChatID = chatID
	var ts int64
	row := a.DB.QueryRow(`SELECT MAX(updated_at) FROM global_settings WHERE key IN (?, ?)`,
		"telegram.bot_token", "telegram.chat_id")
	if err := row.Scan(&ts); err == nil && ts > 0 {
		state.UpdatedAt = time.Unix(ts, 0).UTC().Format("2006-01-02 15:04:05 UTC")
	}
	return state
}

// looksLikeTelegramBotToken: structural sanity check. "<bot-id>:<secret>",
// bot-id must be digits, secret non-empty. Length is not enforced
// because Telegram has changed it historically.
func looksLikeTelegramBotToken(s string) bool {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// looksLikeTelegramChatID: digits, optional leading minus. Empty OK
// (token-only path).
func looksLikeTelegramChatID(s string) bool {
	if s == "" {
		return true
	}
	if s[0] == '-' {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// formatTelegramMessage mirrors what scripts/notify.sh builds, so
// on-call engineers recognise the layout from both channels.
func formatTelegramMessage(host, subject, body string) string {
	return fmt.Sprintf("[%s] %s\n%s\n—\n%s",
		host, subject, time.Now().UTC().Format("2006-01-02T15:04:05Z"), body)
}

// redirectWithFlash centralises the redirect + flash query param logic
// so handler branches don't have to repeat it.
func (a *App) redirectWithFlash(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	q := url.Values{}
	if okMsg != "" {
		q.Set("ok", okMsg)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	target := "/admin/telegram"
	if encoded := q.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func writeFlashRedirect(w http.ResponseWriter, r *http.Request, okMsg string) {
	q := url.Values{}
	if okMsg != "" {
		q.Set("ok", okMsg)
	}
	http.Redirect(w, r, "/admin/telegram?"+q.Encode(), http.StatusSeeOther)
}

func writeErrRedirect(w http.ResponseWriter, r *http.Request, errMsg string) {
	q := url.Values{}
	q.Set("err", errMsg)
	http.Redirect(w, r, "/admin/telegram?"+q.Encode(), http.StatusSeeOther)
}

// redactChatID returns "<id>" if token was unchanged (chat_id is the
// new value), or hides the chat_id if it was a token rotation only.
// We use it for audit_log only — never anywhere else.
func redactChatID(chatID, token string, c *auth.Claims) string {
	if chatID == "" {
		return "<token-only>"
	}
	return chatID
}
