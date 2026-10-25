// telegram_actions.go — the write actions behind /admin/telegram.
//
// Split out of telegram.go in refactor Phase D (2026-10-01): save / test /
// rotate / disable / strict / refresh-menu and the two egress actions (which are
// the ones B93 and B185 pin), plus the exit-server lookup they share.

package admin

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

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
	if err := db.SaveTelegramToken(s.dbc(), token, chatID); err != nil {
		s.redirectWithFlash(w, r, "", "Не удалось сохранить: "+err.Error())
		return
	}
	mask := ""
	if token != "" {
		mask = db.TelegramFingerprint(token)
	} else {
		existing, _, _, _ := db.LoadTelegramToken(s.dbc())
		mask = db.TelegramFingerprint(existing)
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_save",
		fmt.Sprintf("token=%s chat=%s", mask, redactChatID(chatID, token, c)))
	s.invalidateTelegramProbe()
	writeFlashRedirect(w, r, fmt.Sprintf("Сохранено. Токен: %s. Проверьте кнопкой «Отправить тест».", mask))
}

func (s *Service) handleTelegramTest(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	_, _, ok, err := db.LoadTelegramToken(s.dbc())
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
	_, globalChatID, hasGlobal, err := db.LoadTelegramSendTarget(s.dbc())
	if err != nil {
		s.redirectWithFlash(w, r, "", "Ошибка чтения chat_id из БД: "+err.Error())
		return
	}
	// B390 (TD-33 H5): the sends below now report whether they worked. The old
	// handler called the fire-and-forget SendTelegram/SendTelegramToChat and then
	// counted its own LOOP ITERATIONS, so "Send test" answered «Сообщение
	// отправлено (N шт.)» for a missing token, an unparseable chat_id and an
	// `{"ok":false,"description":"chat not found"}` answer alike — a test button
	// that cannot fail. A tester that cannot fail is worse than none: it tells
	// the operator the channel works while the alert that matters is dropped.
	type target struct {
		label string
		chat  int64 // 0 = global chat_id from settings
	}
	var targets []target
	if hasGlobal && globalChatID != "" {
		targets = append(targets, target{label: "global chat_id=" + globalChatID})
	} else {
		bindings, lerr := db.ListTelegramBindings(s.dbc())
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
			targets = append(targets, target{
				label: fmt.Sprintf("binding chat_id=%d", b.ChatID),
				chat:  b.ChatID,
			})
		}
	}
	outcome := sendTestMessages(len(targets), func(i int) error {
		if targets[i].chat == 0 {
			return s.Notifier.SendTelegramErr(text)
		}
		return s.Notifier.SendTelegramToChatErr(text, targets[i].chat)
	}, func(i int) string { return targets[i].label })

	// The audit row carries BOTH halves, so the operator's own record of a
	// failed test survives the flash message.
	auditDetail := subject
	if len(outcome.sent) > 0 {
		auditDetail = fmt.Sprintf("%s [sent: %s]", subject, strings.Join(outcome.sent, ", "))
	}
	if len(outcome.failed) > 0 {
		auditDetail = fmt.Sprintf("%s [failed: %s]", auditDetail, strings.Join(outcome.failed, "; "))
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_test_sent", auditDetail)

	flash := testFlashMessage(outcome)
	writeFlashRedirect(w, r, flash)
}

// handleTelegramAlertTest sends a TEST ALERT through the REAL alert path.
//
// B421 (2026-10-25). «Send test» above sends a REPLY: SendTelegramErr, which — like
// every reply — falls back to a chat bound with /start + [Bind] when
// global_settings.telegram.chat_id is empty. Notifications do NOT take that path: they
// go through SendAlertEvent → resolveAlertTarget → postMessageErr(…, "HTML"). So on an
// install configured only by a binding, the old button said «Сообщение отправлено» while
// every alert was dropped — the operator's report, exactly.
//
// This handler therefore exercises the alert path and reports what the ledger recorded:
// the outcome, the reason, the target it resolved to and the transport (direct or a
// relay tunnel). A tester that cannot fail is worse than none (B390), and a tester that
// tests a DIFFERENT path than the product is worse than both.
func (s *Service) handleTelegramAlertTest(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	if s.Notifier == nil {
		s.redirectWithFlash(w, r, "", "Notifier не инициализирован — перезапустите skygate")
		return
	}
	if _, isNoop := s.Notifier.(telegram.NoopNotifier); isNoop {
		s.redirectWithFlash(w, r, "", "Бот не сконфигурирован — Notifier в no-op режиме")
		return
	}
	lang := s.I18n.LangFromRequest(r)
	if lang == "" {
		lang = "ru"
	}
	// No DedupKey on purpose: a test must always attempt a real send, otherwise the
	// second press inside the window would report «suppressed» and prove nothing.
	id := s.Notifier.SendAlertEvent(telegram.Alert{
		Severity: telegram.SeverityInfo,
		TitleKey: "telegram.alert_test_title",
		Facts: []telegram.AlertFact{
			{Label: i18n.T(lang, "telegram.alert_test_fact_source"), Value: r.Host},
			{Label: i18n.T(lang, "telegram.alert_test_fact_path"), Value: "SendAlertEvent"},
		},
		Lang: lang,
	})
	stats := telegram.AlertDeliverySnapshot()
	s.Backend.Audit(c.UserID, c.Username, "telegram_alert_test",
		fmt.Sprintf("outcome=%s target=%s alert_id=%d path=%s", stats.LastOutcome, stats.TargetSource, id, stats.LastPath))
	writeFlashRedirect(w, r, alertTestFlash(id, stats))
}

// alertTestFlash is the operator-facing sentence for an alert test. Split out so the
// decision "what does the operator read" is testable without HTTP (the B390 lesson).
func alertTestFlash(id int64, stats telegram.AlertDeliveryStats) string {
	switch stats.LastOutcome {
	case telegram.AlertSent:
		path := stats.LastPath
		if path == "" {
			path = "direct"
		}
		return fmt.Sprintf("Уведомление доставлено: #%d, получатель %s (%s), транспорт %s. Это тот же путь, которым идут настоящие уведомления.",
			id, stats.TargetSource, stats.TargetMasked, path)
	case telegram.AlertSuppressed:
		return "Уведомление подавлено окном дедупликации — это не ошибка, но тест должен отправляться всегда; повторите."
	default:
		return fmt.Sprintf("Уведомление НЕ доставлено: %s. Причина: %s. Получатель: %s. Укажите токен и «ID чата» на этой странице.",
			string(stats.LastOutcome), stats.LastReason, stats.TargetSource)
	}
}

// testSendOutcome is what "Send test" really did: the targets that accepted the
// message, and the ones that did not with the reason Telegram (or the loader)
// gave.
type testSendOutcome struct {
	sent   []string
	failed []string
}

// sendTestMessages performs the sends and classifies them.
//
// Split out of the handler so it can be exercised without HTTP: it is the half
// that decides what the operator is told, and the B390 defect lived exactly here
// (a reply that assumed success instead of asking).
func sendTestMessages(n int, send func(i int) error, label func(i int) string) testSendOutcome {
	var out testSendOutcome
	for i := 0; i < n; i++ {
		if err := send(i); err != nil {
			out.failed = append(out.failed, fmt.Sprintf("%s: %v", label(i), err))
			continue
		}
		out.sent = append(out.sent, label(i))
	}
	return out
}

// testFlashMessage is the operator-facing sentence: a success only when every
// target accepted the message, and the reasons otherwise.
func testFlashMessage(o testSendOutcome) string {
	if len(o.failed) == 0 {
		return fmt.Sprintf("Сообщение отправлено (%d шт.). Проверьте Telegram: %s.",
			len(o.sent), strings.Join(o.sent, ", "))
	}
	if len(o.sent) == 0 {
		return fmt.Sprintf("НЕ отправлено ни одного сообщения (%d попыток). Причина: %s. "+
			"Проверьте токен, chat_id и доступность api.telegram.org из контейнера.",
			len(o.failed), strings.Join(o.failed, "; "))
	}
	return fmt.Sprintf("Отправлено %d, НЕ отправлено %d. Успешно: %s. Ошибки: %s.",
		len(o.sent), len(o.failed), strings.Join(o.sent, ", "), strings.Join(o.failed, "; "))
}

func (s *Service) handleTelegramRotate(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	if r.FormValue("confirm") != "yes" {
		s.redirectWithFlash(w, r, "", "Поставьте галочку подтверждения для rotate")
		return
	}
	if err := db.DeleteTelegramToken(s.dbc()); err != nil {
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
	if err := db.DeleteTelegramToken(s.dbc()); err != nil {
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
	old := db.LoadTelegramStrictMode(s.dbc())
	if want == old {
		writeFlashRedirect(w, r, "Strict mode already in the requested state.")
		return
	}
	if err := db.SaveTelegramStrictMode(s.dbc(), want); err != nil {
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

// handleTelegramSetEgress (v0.33.1.8) sets the egress relay
// for the Telegram bot and immediately SSHes to the chosen
// node to apply the canonical Telegram-CIDR routes via
// `tailscale set --advertise-routes=...`.
//
// Flow:
//  1. Read node_id from form (must be one of the enabled
//     exit_servers rows — verified by re-listing the table).
//  2. Look up ssh_target + ssh_key_path from exit_servers
//     (per-row config; v0.24+).
//  3. Shell out to `ssh -i <key> ... <node>
//     "tailscale set --advertise-routes=<TELEGRAM_CIDRS>"`
//     via the existing headscale.Client.SetAdvertisedRoutes
//     helper. The helper always prepends 0.0.0.0/0 and ::/0
//     to keep the node's exit-node capability.
//  4. Persist the node_id in
//     global_settings.telegram.egress_node_id so subsequent
//     re-applies know which relay to target.
//  5. Audit log row for the operator's record.
//
// Why admin-only: this changes the live advertised-routes
// on a remote node, which is operator territory, not
// user-side. The /admin/telegram route is already admin-only.
//
// Why no confirm checkbox: the JS confirm() dialog at the
// form is enough — accidental clicks land on the admin's
// own /admin/telegram page, and the SSH call is idempotent
// (re-running the same tailscale set is safe; the
// advertised-routes list is replaced atomically).
func (s *Service) handleTelegramSetEgress(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	// 2026-08-10: v0.33.1.41 — Issue 4 infra user. The
	// audit log records actions on behalf of the BOT, which
	// is infrastructure, not on behalf of the admin who
	// clicked the button. Look up the 'infra' portal user
	// and use its id for the audit row. Fall back to the
	// admin's own id (pre-existing behaviour) if the infra
	// row isn't linked yet (V054 ran but ensureInfraUser
	// hasn't completed) — better to record the admin than
	// to skip the audit row.
	auditUID, auditName := s.Backend.InfraAuditIdentity(c.UserID, c.Username)
	nodeID := strings.TrimSpace(r.FormValue("node_id"))
	if nodeID == "" {
		writeErrRedirect(w, r, "node_id обязателен")
		return
	}
	// Verify the node is in exit_servers and enabled.
	relay, err := findEnabledExitServer(s.dbc(), nodeID)
	if err != nil {
		writeErrRedirect(w, r, "Не удалось найти relay: "+err.Error())
		return
	}
	if relay == nil {
		writeErrRedirect(w, r, "node_id "+nodeID+" не зарегистрирован как enabled exit-node")
		return
	}
	// Resolve the SSH target + key path from exit_servers.
	// 2026-08-09 v0.33.1.32 B84: the SSH target now uses the
	// B81 chain (operator override → root@<tailscale_ip> → "")
	// via LookupExitServerSSHTarget, instead of the legacy
	// relay.Hostname fallback. Pre-B84, the /admin/telegram
	// "Set as egress relay" handler fell back to relay.Hostname
	// (the headscale-given hostname like "emilia") when the
	// operator left exit_servers.ssh_target empty — which the
	// `ssh` CLI couldn't resolve in most setups, so the
	// click errored with "Could not resolve hostname emilia".
	// Post-B84, the empty-ssh_target case resolves to
	// "root@<tailscale_ip>" (the same chain the
	// /admin/exit-nodes/sync path uses since v0.33.1.29), so
	// the click works end-to-end as long as Tailscale routes
	// to the relay. Live-verified via /admin/telegram
	// "Set as egress relay" for emilia on the live VM (the
	// 2026-08-09 operator report that triggered the fix).
	sshCfg, _ := db.LookupExitServerSSH(s.dbc(), relay.Hostname)
	keyPath := strings.TrimSpace(sshCfg.KeyPath)
	if keyPath == "" {
		keyPath = s.SSHKeyPath // Config-level default (SKYGATE_EXIT_SSH_KEY).
	}
	// B81 chain: stored ssh_target → root@<tailscale_ip> → "".
	// The "" case is the "no row" or "row with empty
	// tailscale_ip" — fall back to the legacy hostname so
	// the error message is still meaningful (instead of
	// "ssh :22: No address associated with hostname").
	sshTarget, _ := db.LookupExitServerSSHTarget(s.dbc(), relay.Hostname)
	sshTarget = strings.TrimSpace(sshTarget)
	if sshTarget == "" {
		sshTarget = relay.Hostname
	}
	// Apply the canonical Telegram-CIDR list (same as
	// deploy/tailscale-relay/update-routes.sh). The helper
	// prepends 0.0.0.0/0 and ::/0 so the node stays a
	// valid exit-node, and dedupes against both the base
	// pair and the caller-supplied routes. AcceptRoutes
	// is the per-node preference from exit_servers (0 =
	// "don't touch" — matches the existing /admin/exit-nodes
	// "Sync" button behaviour).
	hs := s.HSGlobalFn()
	if hs == nil {
		writeErrRedirect(w, r, "headscale client не инициализирован")
		return
	}
	out, sshErr := hs.SetAdvertisedRoutes(
		relay.Hostname,
		TelegramCIDRs,
		relay.AcceptRoutes,
		sshTarget, keyPath,
	)
	if sshErr != nil {
		s.Backend.Audit(auditUID, auditName, "telegram_egress_set",
			fmt.Sprintf("relay=%s host=%s ssh=err ip=%s",
				relay.Hostname, sshTarget, r.RemoteAddr))
		writeErrRedirect(w, r,
			fmt.Sprintf("SSH на %s не удался: %s", sshTarget, sshErr.Error()))
		return
	}
	// Persist the selection so future re-applies know which
	// relay to target. SetGlobalSetting is idempotent.
	if err := db.SetGlobalSetting(s.dbc(), "telegram.egress_node_id", relay.NodeID); err != nil {
		s.Backend.Audit(auditUID, auditName, "telegram_egress_set",
			fmt.Sprintf("relay=%s ssh=ok save_err=%q", relay.Hostname, err.Error()))
		writeErrRedirect(w, r,
			fmt.Sprintf("Маршруты применены, но не удалось сохранить выбор: %s", err.Error()))
		return
	}
	s.Backend.Audit(auditUID, auditName, "telegram_egress_set",
		fmt.Sprintf("relay=%s routes=%d ssh=ok", relay.Hostname, len(TelegramCIDRs)))
	if out != "" {
		// Some `tailscale set` calls print "Success" — surface
		// it in the flash so the operator can see the relay
		// accepted the routes.
		writeFlashRedirect(w, r,
			fmt.Sprintf("Telegram-CIDR применён на relay %s. Output: %s", relay.Hostname, out))
		return
	}
	writeFlashRedirect(w, r,
		fmt.Sprintf("Telegram-CIDR применён на relay %s. Проверьте tailscale status через ~30s.", relay.Hostname))
}

// handleTelegramClearEgress (v0.33.1.8) removes the
// stored relay selection. Tailscale then auto-picks the
// best metric between the relays still advertising the
// Telegram-CIDR list. No SSH is involved — the relay's
// advertised-routes are untouched on Clear (admin can
// still reach Telegram via whichever relay has the best
// metric; the Clear just tells skygate not to *force*
// any particular relay).
func (s *Service) handleTelegramClearEgress(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	if err := db.SetGlobalSetting(s.dbc(), "telegram.egress_node_id", ""); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "telegram_egress_clear",
			fmt.Sprintf("err=%q", err.Error()))
		writeErrRedirect(w, r, "Не удалось очистить: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_egress_clear", "ok")
	writeFlashRedirect(w, r, "Egress relay сброшен. Tailscale выберет лучший relay автоматически.")
}
