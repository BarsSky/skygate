// Package admin — telegram.go owns the /admin/telegram page
// (token save, test, rotate, disable, strict mode, menu refresh).
//
// refactor-v0.30 Phase B step 3b.1a (2026-07-29): moved from
// internal/handlers/admin_telegram.go. The probe cache state
// (telegramProbeMu, telegramProbeAt, telegramProbeResult,
// telegramProbeTokenFP) was on *App; it's now fields on
// *Service (telegramProbeMu, telegramProbeAt, etc.) so the
// cache is owned by the feature that uses it.

package admin

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/telegram"
)

// Service extension: probe cache state. The struct is in
// service.go; this file adds the probe-cache fields. To keep
// everything in one place, we declare a struct that the
// Service composes.
type serviceProbeCache struct {
	mu     sync.Mutex
	at     time.Time
	result TelegramProbeResult
	tokenFP string
}

const telegramProbeTTL = 30 * time.Second

// Helper attached to Service (we can't add methods to a struct
// in a different file, so we use a method-set on *Service and
// the cache is a per-instance field). Service carries a
// telegramProbeCache field that these methods use.

// cachedTelegramProbe returns the cached probe result if
// (a) the cache is non-empty, (b) it is younger than
// telegramProbeTTL (30s), and (c) the bot token hasn't been
// rotated. Otherwise it runs the probe synchronously,
// stores the result, and returns it.
func (s *Service) cachedTelegramProbe(ctx context.Context, tokenFP string) TelegramProbeResult {
	s.telegramProbeCache.mu.Lock()
	if !s.telegramProbeCache.at.IsZero() &&
		time.Since(s.telegramProbeCache.at) < telegramProbeTTL &&
		s.telegramProbeCache.tokenFP == tokenFP {
		res := s.telegramProbeCache.result
		s.telegramProbeCache.mu.Unlock()
		return res
	}
	s.telegramProbeCache.mu.Unlock()

	// Cache miss / stale / token rotated — re-probe.
	token, _, _, _ := db.LoadTelegramToken(s.DB)
	res := probeTelegramAPI(ctx, token)

	s.telegramProbeCache.mu.Lock()
	s.telegramProbeCache.result = res
	s.telegramProbeCache.at = time.Now()
	s.telegramProbeCache.tokenFP = tokenFP
	s.telegramProbeCache.mu.Unlock()
	return res
}

// invalidateTelegramProbe clears the cache.
func (s *Service) invalidateTelegramProbe() {
	s.telegramProbeCache.mu.Lock()
	s.telegramProbeCache.at = time.Time{}
	s.telegramProbeCache.result = TelegramProbeResult{}
	s.telegramProbeCache.tokenFP = ""
	s.telegramProbeCache.mu.Unlock()
}

// telegramUIState is the shape the template consumes.
type telegramUIState struct {
	Configured    bool
	TokenFP       string
	ChatID        string
	UpdatedAt     string
	StrictMode    bool
	LoginTokenTTL int
	Probe         TelegramProbeResult
}

func (s *Service) loadTelegramUIState() telegramUIState {
	token, chatID, ok, err := db.LoadTelegramToken(s.DB)
	state := telegramUIState{
		LoginTokenTTL: db.LoadTelegramLoginTokenTTL(s.DB),
		StrictMode:    db.LoadTelegramStrictMode(s.DB),
	}
	if err != nil || !ok {
		return state
	}
	state.Configured = true
	state.TokenFP = db.TelegramFingerprint(token)
	state.ChatID = chatID
	var ts int64
	row := s.DB.QueryRow(`SELECT MAX(updated_at) FROM global_settings WHERE key IN (?, ?)`,
		"telegram.bot_token", "telegram.chat_id")
	if err := row.Scan(&ts); err == nil && ts > 0 {
		state.UpdatedAt = time.Unix(ts, 0).UTC().Format("2006-01-02 15:04:05 UTC")
	}
	return state
}

// AdminTelegram renders the /admin/telegram page. Admin-only.
func (s *Service) AdminTelegram(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	state := s.loadTelegramUIState()
	if state.Configured {
		token, _, _, _ := db.LoadTelegramToken(s.DB)
		state.Probe = s.cachedTelegramProbe(r.Context(), db.TelegramFingerprint(token))
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
	s.Backend.RenderWithLayout(w, r, "admin/telegram.html", c, map[string]any{
		"Page":         "admin/telegram",
		"Title":        "Telegram",
		"State":        state,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
		"CSRF":         csrf,
	})
}

// AdminTelegramPost dispatches the form to the right handler
// based on the action field. Admin-only.
func (s *Service) AdminTelegramPost(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectWithFlash(w, r, "", fmt.Sprintf("Ошибка парсинга формы: %s", err.Error()))
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	cookie, err := r.Cookie("skygate_tg_csrf")
	if err != nil || cookie.Value == "" {
		s.redirectWithFlash(w, r, "", "CSRF-cookie отсутствует — обновите страницу и повторите")
		return
	}
	submitted := r.FormValue("csrf")
	if subtle.ConstantTimeCompare([]byte(submitted), []byte(cookie.Value)) != 1 {
		s.Backend.Audit(c.UserID, c.Username, "telegram_csrf_fail",
			fmt.Sprintf("action=%s ip=%s", action, r.RemoteAddr))
		s.redirectWithFlash(w, r, "", "Неверный CSRF-токен — обновите страницу и повторите")
		return
	}
	switch action {
	case "save":
		s.handleTelegramSave(w, r, c)
	case "test":
		s.handleTelegramTest(w, r, c)
	case "rotate":
		s.handleTelegramRotate(w, r, c)
	case "disable":
		s.handleTelegramDisable(w, r, c)
	case "strict":
		s.handleTelegramStrict(w, r, c)
	case "refresh_menu":
		s.handleTelegramRefreshMenu(w, r, c)
	default:
		s.redirectWithFlash(w, r, "", "Неизвестное действие: "+action)
	}
}

func (s *Service) handleTelegramSave(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	token := strings.TrimSpace(r.FormValue("bot_token"))
	chatID := strings.TrimSpace(r.FormValue("chat_id"))
	if token == "" && chatID == "" {
		s.redirectWithFlash(w, r, "", "Заполните хотя бы одно поле (токен или chat_id)")
		return
	}
	if token != "" && !looksLikeTelegramBotToken(token) {
		s.redirectWithFlash(w, r, "", "Токен выглядит не как BotFather token: ожидается '<id>:<secret>'")
		return
	}
	if chatID != "" && !looksLikeTelegramChatID(chatID) {
		s.redirectWithFlash(w, r, "", "chat_id должен быть числом (например 12345) или -100… для супергруппы")
		return
	}
	if err := db.SaveTelegramToken(s.DB, token, chatID); err != nil {
		s.redirectWithFlash(w, r, "", "Не удалось сохранить: "+err.Error())
		return
	}
	mask := ""
	if token != "" {
		mask = db.TelegramFingerprint(token)
	} else {
		existing, _, _, _ := db.LoadTelegramToken(s.DB)
		mask = db.TelegramFingerprint(existing)
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_save",
		fmt.Sprintf("token=%s chat=%s", mask, redactChatID(chatID, token, c)))
	s.invalidateTelegramProbe()
	writeFlashRedirect(w, r, fmt.Sprintf("Сохранено. Токен: %s. Проверьте кнопкой «Отправить тест».", mask))
}

func (s *Service) handleTelegramTest(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	_, _, ok, err := db.LoadTelegramToken(s.DB)
	if err != nil {
		s.redirectWithFlash(w, r, "", "Ошибка чтения из БД: "+err.Error())
		return
	}
	if !ok {
		s.redirectWithFlash(w, r, "", "Сначала сохраните токен и chat_id")
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

	text := formatTelegramMessage(r.Host, subject, body)
	if s.Notifier == nil {
		s.redirectWithFlash(w, r, "", "Notifier не инициализирован — перезапустите skygate")
		return
	}
	if _, isNoop := s.Notifier.(telegram.NoopNotifier); isNoop {
		s.redirectWithFlash(w, r, "", "Бот не сконфигурирован — Notifier в no-op режиме")
		return
	}
	_, globalChatID, hasGlobal, err := db.LoadTelegramSendTarget(s.DB)
	if err != nil {
		s.redirectWithFlash(w, r, "", "Ошибка чтения chat_id из БД: "+err.Error())
		return
	}
	sentCount := 0
	sentTargets := []string{}
	if hasGlobal && globalChatID != "" {
		s.Notifier.SendTelegram(text)
		sentCount = 1
		sentTargets = append(sentTargets, "global chat_id="+globalChatID)
	} else {
		bindings, lerr := db.ListTelegramBindings(s.DB)
		if lerr != nil {
			s.redirectWithFlash(w, r, "", "Ошибка чтения bindings: "+lerr.Error())
			return
		}
		if len(bindings) == 0 {
			s.redirectWithFlash(w, r, "",
				"Нет адреса для отправки: chat_id в форме пуст и ни один чат не привязан. "+
					"Откройте Telegram, найдите бота, отправьте /start и нажмите [Bind] — после этого нажмите 'Отправить тест' ещё раз.")
			return
		}
		for _, b := range bindings {
			s.Notifier.SendTelegramToChat(text, b.ChatID)
			sentCount++
			sentTargets = append(sentTargets, fmt.Sprintf("binding chat_id=%d", b.ChatID))
		}
	}
	auditDetail := subject
	if len(sentTargets) > 0 {
		auditDetail = fmt.Sprintf("%s [%s]", subject, strings.Join(sentTargets, ", "))
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_test_sent", auditDetail)
	flash := fmt.Sprintf("Сообщение отправлено (%d шт.). Проверьте Telegram: %s.", sentCount, strings.Join(sentTargets, ", "))
	writeFlashRedirect(w, r, flash)
}

func (s *Service) handleTelegramRotate(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	if r.FormValue("confirm") != "yes" {
		s.redirectWithFlash(w, r, "", "Поставьте галочку подтверждения для rotate")
		return
	}
	if err := db.DeleteTelegramToken(s.DB); err != nil {
		s.redirectWithFlash(w, r, "", "Не удалось очистить старый токен: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_rotate", "")
	s.invalidateTelegramProbe()
	writeFlashRedirect(w, r, "Старый токен удалён. Сохраните новый.")
}

func (s *Service) handleTelegramDisable(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	if r.FormValue("confirm") != "yes" {
		s.redirectWithFlash(w, r, "", "Поставьте галочку подтверждения для disable")
		return
	}
	if err := db.DeleteTelegramToken(s.DB); err != nil {
		s.redirectWithFlash(w, r, "", "Ошибка при удалении: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_disable", "")
	s.invalidateTelegramProbe()
	writeFlashRedirect(w, r, "Telegram отключён. Уведомления будут писаться в ~/.skygate-notify.log")
}

// handleTelegramStrict (Этап 12, 2026-07-13) toggles strict
// mode in global_settings.telegram.strict_mode.
func (s *Service) handleTelegramStrict(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	if r.FormValue("confirm") != "yes" {
		s.redirectWithFlash(w, r, "", "Поставьте галочку подтверждения для strict mode")
		return
	}
	want := r.FormValue("enabled") == "1"
	old := db.LoadTelegramStrictMode(s.DB)
	if want == old {
		writeFlashRedirect(w, r, "Strict mode already in the requested state.")
		return
	}
	if err := db.SaveTelegramStrictMode(s.DB, want); err != nil {
		s.redirectWithFlash(w, r, "", "Ошибка при сохранении: "+err.Error())
		return
	}
	state := "off"
	if want {
		state = "on"
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_strict_mode_changed",
		fmt.Sprintf("from=%s to=%s", boolToOnOff(old), state))
	s.invalidateTelegramProbe()
	writeFlashRedirect(w, r, fmt.Sprintf("Strict mode %s. Bot will read the new state within 2s.", state))
}

func (s *Service) handleTelegramRefreshMenu(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	notifier, ok := s.Notifier.(setMyCommandsAller)
	if !ok {
		s.redirectWithFlash(w, r, "", "Bot notifier doesn't support /setMyCommands (no Telegram token configured).")
		return
	}
	if err := notifier.SetMyCommandsAll(r.Context(), telegram.DefaultMyCommandsSpec); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "telegram_refresh_menu", "failed: "+err.Error())
		s.redirectWithFlash(w, r, "", "setMyCommands failed: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_refresh_menu", "ok")
	writeFlashRedirect(w, r, "Bot menu refreshed (en + ru).")
}

// setMyCommandsAller is the subset of the RealNotifier
// interface that the menu-refresh handler needs.
type setMyCommandsAller interface {
	SetMyCommandsAll(ctx context.Context, spec telegram.MyCommandsSpec) error
}

func boolToOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// looksLikeTelegramBotToken: structural sanity check.
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

// looksLikeTelegramChatID: digits, optional leading minus.
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

func formatTelegramMessage(host, subject, body string) string {
	return fmt.Sprintf("[%s] %s\n%s\n—\n%s",
		host, subject, time.Now().UTC().Format("2006-01-02T15:04:05Z"), body)
}

func (s *Service) redirectWithFlash(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
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

func redactChatID(chatID, token string, c *auth.Claims) string {
	if chatID == "" {
		return "<token-only>"
	}
	return chatID
}
