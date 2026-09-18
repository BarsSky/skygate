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
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// Service extension: probe cache state. The struct is in
// service.go; this file adds the probe-cache fields. To keep
// everything in one place, we declare a struct that the
// Service composes.
type serviceProbeCache struct {
	mu      sync.Mutex
	at      time.Time
	result  TelegramProbeResult
	tokenFP string
	// refreshing deduplicates the B253 background refresh: several page
	// loads (or operators) hitting a stale cache must not spawn one probe
	// goroutine each.
	refreshing bool
}

const (
	// telegramProbeTTLSuccess is how long a HEALTHY probe result is
	// trusted. Short on purpose: a green "api.telegram.org reachable"
	// badge should reflect reality within half a minute.
	telegramProbeTTLSuccess = 30 * time.Second

	// telegramProbeTTLError is how long a FAILED result is trusted. Much
	// longer, because a failing probe costs the full HTTP timeout, and the
	// pre-B253 code re-ran it on every page load: an unreachable Telegram
	// API made /admin/telegram take ~5s on every refresh.
	telegramProbeTTLError = 5 * time.Minute
)

// telegramProbeTTLFor returns the TTL that applies to a cached result:
// successes expire quickly, failures are remembered for 5 minutes.
func telegramProbeTTLFor(res TelegramProbeResult) time.Duration {
	if res.State == ProbeOKDirect || res.State == ProbeOKRelay {
		return telegramProbeTTLSuccess
	}
	return telegramProbeTTLError
}

// Helper attached to Service (we can't add methods to a struct
// in a different file, so we use a method-set on *Service and
// the cache is a per-instance field). Service carries a
// telegramProbeCache field that these methods use.

// cachedTelegramProbe returns the cached probe result.
//
// B253 (stale-while-revalidate): a result younger than the applicable TTL
// (30s healthy / 5min failed) is returned as-is. A STALE result is also
// returned immediately — marked Stale + StaleAt — while a single
// background goroutine refreshes it, so the page never blocks on the probe.
// Pre-B253 the handler probed synchronously on every stale load, which meant
// up to 5 seconds of dead time whenever the Telegram API was slow or the
// network path was broken (exactly the situation the page exists to
// diagnose).
//
// Only when there is nothing cacheable at all (first ever load, or the bot
// token was rotated) does it probe synchronously — an empty page would be
// worse than one slow render.
func (s *Service) cachedTelegramProbe(ctx context.Context, tokenFP string) TelegramProbeResult {
	s.telegramProbeCache.mu.Lock()
	cached := s.telegramProbeCache.result
	at := s.telegramProbeCache.at
	sameToken := s.telegramProbeCache.tokenFP == tokenFP
	s.telegramProbeCache.mu.Unlock()

	if sameToken && !at.IsZero() {
		if age := time.Since(at); age < telegramProbeTTLFor(cached) {
			return cached
		}
		// Stale: serve what we have and refresh out of band. The request's
		// ctx is deliberately NOT used — it dies with the response.
		go s.refreshProbeAsync(tokenFP)
		cached.Stale = true
		cached.StaleAt = at.UTC().Format(time.RFC3339)
		return cached
	}

	// Cache miss / token rotated — probe once, synchronously.
	return s.probeNowSync(ctx, tokenFP)
}

// probeNowSync runs the probe synchronously, stores the result in the cache
// and returns it. This is the path used by the first page load, by the
// token-rotation case, and by the "Probe now" POST handler (which exists
// precisely to bypass the cache: the operator has just fixed something and
// wants an immediate answer instead of waiting for the background refresh).
func (s *Service) probeNowSync(ctx context.Context, tokenFP string) TelegramProbeResult {
	token, _, _, _ := db.LoadTelegramToken(s.dbc())
	res := probeTelegramAPI(ctx, token)

	s.telegramProbeCache.mu.Lock()
	s.telegramProbeCache.result = res
	s.telegramProbeCache.at = time.Now()
	s.telegramProbeCache.tokenFP = tokenFP
	s.telegramProbeCache.mu.Unlock()
	return res
}

// refreshProbeAsync re-probes in the background. Deduplicated through the
// cache's `refreshing` flag: a page-refresh storm (or several operators)
// must not spawn one goroutine per request, and the probe itself costs an
// outbound HTTP round trip.
//
// Errors need no special handling: probeTelegramAPI always returns a result
// (unreachable state + message), which is what gets cached.
func (s *Service) refreshProbeAsync(tokenFP string) {
	s.telegramProbeCache.mu.Lock()
	if s.telegramProbeCache.refreshing {
		s.telegramProbeCache.mu.Unlock()
		return
	}
	s.telegramProbeCache.refreshing = true
	s.telegramProbeCache.mu.Unlock()

	defer func() {
		s.telegramProbeCache.mu.Lock()
		s.telegramProbeCache.refreshing = false
		s.telegramProbeCache.mu.Unlock()
	}()

	// Bounded context: the background probe must never outlive its
	// usefulness, and probeTelegramAPI's own timeout is 5s.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.probeNowSync(ctx, tokenFP)
}

// PostAdminTelegramProbeNow is the "Probe now" trigger (B253). It bypasses
// the cache — and therefore the stale-while-revalidate path — because the
// operator clicks it right after fixing something (re-enabled tailscaled in
// the container, rotated the bot token, changed the network path) and wants
// an immediate answer instead of waiting up to 5 minutes for the background
// refresh.
//
// Redirects back to the page: the refreshed probe state IS the feedback (the
// badge flips green/red). Answering JSON here would render raw text in the
// browser — the B180 bug class — so no JSON, ever.
func (s *Service) PostAdminTelegramProbeNow(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	token, _, _, _ := db.LoadTelegramToken(s.dbc())
	res := s.probeNowSync(r.Context(), db.TelegramFingerprint(token))
	s.Backend.Audit(c.UserID, c.Username, "telegram_probe_now",
		fmt.Sprintf("state=%s stale=false", res.State.String()))
	if res.State == ProbeOKDirect || res.State == ProbeOKRelay {
		http.Redirect(w, r, "/admin/telegram?ok="+url.QueryEscape("probe: "+res.State.String()), http.StatusSeeOther)
		return
	}
	msg := "probe: " + res.State.String()
	if res.Message != "" {
		msg += " — " + res.Message
	}
	http.Redirect(w, r, "/admin/telegram?err="+url.QueryEscape(msg), http.StatusSeeOther)
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
	// Egress carries the v0.33.1.8 "which relay runs
	// Telegram-CIDR" selector. SelectedNodeID is the headscale
	// node_id of the currently chosen exit-node ("" when none).
	// SelectedHostname is the friendly name rendered in the
	// "currently selected" line. Available is the list of every
	// enabled exit-node the admin can pick from (sourced from
	// exit_servers via db.ListExitServers).
	Egress EgressState
	// Container (B185) carries the live tailscaled state
	// inside the skygate container — RouteAll / AdvertiseTags
	// / ExitNodeID / TailscaleIPs. Filled by
	// readContainerTailscaleState which `docker exec`s into
	// the container and parses `tailscale status --json`.
	// When the container is unreachable (no docker socket
	// mount, wrong hostname, etc.) the helper returns a
	// state with Available=false and the page shows a
	// "container diagnostic not available" note instead
	// of failing the page render.
	Container ContainerTailscaleState
}

// ContainerTailscaleState is the diagnostic snapshot of the
// skygate container's tailscaled — the field that decides
// whether the container will accept subnet routes from the
// chosen egress relay. If RouteAll is false the Telegram
// probe will be "unreachable" no matter what the relay
// advertises; the "Re-apply accept-routes" button is the
// one-click fix (calls `tailscale set --accept-routes=true`
// inside the container, which updates the persisted state
// without the "requires mentioning all non-default flags"
// gotcha that breaks `tailscale up` when the state already
// has --advertise-tags set).
//
// 2026-08-25 (B185): replaces the "ssh to relay + run
// update-routes.sh + check 4 things by hand" runbook
// with a single page that shows the live container state
// + a button to fix the most common break (RouteAll=false).
type ContainerTailscaleState struct {
	Available      bool
	Hostname       string
	BackendState   string
	IP4            string
	IP6            string
	RouteAll       bool
	AdvertiseTags  []string
	ExitNodeID     string
	HasAcceptIssue bool   // RouteAll=false OR no AdvertiseTags
	RawStderr      string // last `tailscale status` / `docker exec` error
}

// EgressState is the per-page state for the egress-relay card.
// Lives in telegram.go (kept private) so the template can read
// it via {{.State.Egress.SelectedHostname}} etc.
type EgressState struct {
	SelectedNodeID   string
	SelectedHostname string
	Available        []db.ExitServer
}

func (s *Service) loadTelegramUIState() telegramUIState {
	token, chatID, ok, err := db.LoadTelegramToken(s.dbc())
	state := telegramUIState{
		LoginTokenTTL: db.LoadTelegramLoginTokenTTL(s.dbc()),
		StrictMode:    db.LoadTelegramStrictMode(s.dbc()),
	}
	// v0.33.1.8: load the egress selector BEFORE the early
	// return. The operator may want to pre-configure which
	// relay terminates api.telegram.org traffic BEFORE the
	// bot token is saved (e.g. the order of operations is
	// "fix the network path first, then enable the bot").
	// The previous layout (after the early return) made the
	// Egress card disappear until a token was saved, which
	// was a chicken-and-egg UX trap on the
	// "Telegram-egress unreachable" path.
	if v, gerr := db.GetGlobalSetting(s.dbc(), "telegram.egress_node_id", ""); gerr == nil {
		state.Egress.SelectedNodeID = v
	}
	if relays, lerr := db.ListExitServers(s.dbc()); lerr == nil {
		for _, e := range relays {
			if e.Enabled {
				state.Egress.Available = append(state.Egress.Available, e)
			}
		}
	}
	if state.Egress.SelectedNodeID != "" {
		for _, e := range state.Egress.Available {
			if e.NodeID == state.Egress.SelectedNodeID {
				state.Egress.SelectedHostname = e.Hostname
				break
			}
		}
		if state.Egress.SelectedHostname == "" {
			if h, herr := db.LookupExitServerHostname(s.dbc(), state.Egress.SelectedNodeID); herr == nil {
				state.Egress.SelectedHostname = h
			}
		}
	}

	if err != nil || !ok {
		return state
	}
	state.Configured = true
	state.TokenFP = db.TelegramFingerprint(token)
	state.ChatID = chatID
	var ts int64
	row := s.dbc().QueryRow(`SELECT MAX(updated_at) FROM global_settings WHERE key IN ($1, $2)`,
		"telegram.bot_token", "telegram.chat_id")
	if err := row.Scan(&ts); err == nil && ts > 0 {
		state.UpdatedAt = time.Unix(ts, 0).UTC().Format("2006-01-02 15:04:05 UTC")
	}
	// 2026-09-16 (B255): the container's tailscaled
	// state used to be read here via
	// readContainerTailscaleState(), which shells out to
	// `docker exec skygate-skygate-1 tailscale status
	// --json` (~1-3s on a healthy host, ~8s if
	// tailscaled isn't running). That blocked page
	// render. The state is now filled in by the bg
	// handler AdminTelegramContainerBg + the JS in
	// admin/telegram.html, NOT here.
	//
	// state.Container intentionally stays at its zero
	// value (Available=false) until the bg handler fires
	// — the template's id="telegram-container-slot" is
	// rendered empty + the bg response replaces it.
	return state
}

// AdminTelegram renders the /admin/telegram page. Admin-only.
//
// 2026-09-16 (B253 follow-up): removed two SYNCHRONOUS calls that
// delayed page render by up to ~5s on cold-cache (the
// api.telegram.org HTTP probe) + ~1-3s for the docker-inspect
// container state. Both now load asynchronously via
//
//	GET /admin/telegram/probe-bg
//	GET /admin/telegram/container-bg
//
// started by the small JS in /admin/telegram.html.
// `state.Probe` and `state.Container` are intentionally left at
// zero-values here — the JS updates them in place. The page now
// renders with a CSS spinner instead of blocking on network.
func (s *Service) AdminTelegram(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	state := s.loadTelegramUIState()
	// IMPORTANT (B253): the next two lines USED to run synchronously:
	//
	//   if state.Configured {
	//       token, _, _, _ := db.LoadTelegramToken(s.dbc())
	//       state.Probe = s.cachedTelegramProbe(r.Context(), db.TelegramFingerprint(token))
	//   }
	//   state.Container = readContainerTailscaleState("skygate-skygate-1")
	//
	// Both are now deferred to background GET handlers below. See
	// AdminTelegramProbeBg + AdminTelegramContainerBg.
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

// AdminTelegramProbeBg renders /admin/telegram/probe-bg — the
// background poll for the Telegram API probe. Used by the small
// JS that fires from /admin/telegram.html after DOM-ready. Returns
// HTML (not JSON) so we can `el.outerHTML = ...` it directly
// instead of running a template engine on the client side.
//
// 2026-09-16 (B255): this is the rendering path extracted from
// the original synchronous AdminTelegram handler — the probe
// used to block page render for up to 5s on cold cache. The
// rendered HTML matches what the original template produced
// (admin/telegram.html lines 55-103) so the JS can drop the
// response into the same DOM slot. Admin-only.
func (s *Service) AdminTelegramProbeBg(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	state := s.loadTelegramUIState()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store") // JS polls live
	if !state.Configured {
		// Token was deleted between render and this poll.
		// Render the "not configured" pill in the same DOM
		// slot as the probe (the slot's parent already shows
		// the "not configured" pill at the page header — this
		// is the bg-localized variant so the JS swap is
		// self-contained).
		_, _ = w.Write([]byte(
			`<div class="alert alert-warn" id="telegram-probe-slot"><i class="fa-solid fa-triangle-exclamation"></i> ` +
				html.EscapeString(i18n.Tf(lang, "telegram.pill_not_configured")) + `</div>`))
		return
	}
	token, _, _, _ := db.LoadTelegramToken(s.dbc())
	probe := s.cachedTelegramProbe(r.Context(), db.TelegramFingerprint(token))
	_, _ = w.Write([]byte(renderProbeHTML(probe, state.Container, lang)))
}

// AdminTelegramContainerBg renders /admin/telegram/container-bg —
// the container-tailscaled state. Same shape as ProbeBg. The
// inner block is wrapped in #telegram-container-slot so the JS
// can drop it in via `el.outerHTML = ...`.
func (s *Service) AdminTelegramContainerBg(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	csrf, setCookie := mintTelegramCSRF(r, w)
	ct := readContainerTailscaleState("skygate-skygate-1")
	if setCookie {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// no-store; the Set-Cookie is already written by
		// mintTelegramCSRF before we set Content-Type, so the
		// browser will persist the cookie before consuming
		// the body.
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
	}
	_, _ = w.Write([]byte(renderContainerHTML(ct, csrf, lang)))
}

// mintTelegramCSRF returns the existing skygate_tg_csrf cookie
// value if present; otherwise generates a fresh one and sets it.
// Used by the bg container endpoint so the "Re-apply
// accept-routes" form inside the rendered HTML keeps working
// across polls (the original 600s MaxAge would otherwise expire
// while the page is open).
//
// `setCookie` reports whether the cookie was (re-)written on
// this request — the caller can decide whether to log it.
func mintTelegramCSRF(r *http.Request, w http.ResponseWriter) (csrf string, setCookie bool) {
	if c, err := r.Cookie("skygate_tg_csrf"); err == nil && c.Value != "" {
		return c.Value, false
	}
	tok, err := db.RandomConfirmationToken(8)
	if err != nil {
		// Extremely unlikely (rand read failure). Return
		// empty so the form is disabled rather than failing
		// the whole render.
		return "", false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "skygate_tg_csrf",
		Value:    tok,
		Path:     "/admin/telegram",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return tok, true
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
	case "set_egress":
		s.handleTelegramSetEgress(w, r, c)
	case "set_nearest_egress":
		// 2026-09-16 (B255): one-click "Pin nearest exit
		// node" — measures latency from skygate-host's
		// tailscaled to each enabled exit server, picks
		// the lowest, and reuses the set_egress path.
		s.handleTelegramSetNearestEgress(w, r, c)
	case "clear_egress":
		s.handleTelegramClearEgress(w, r, c)
	case "reapply_accept_routes":
		// 2026-08-25 (B185): one-click fix for the
		// "tailscale up fails with 'requires mentioning
		// all non-default flags'" entrypoint bug. The
		// button is shown next to the diagnostic block
		// when Container.HasAcceptIssue is true.
		s.handleTelegramReapplyAcceptRoutes(w, r, c)
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
	sentCount := 0
	sentTargets := []string{}
	if hasGlobal && globalChatID != "" {
		s.Notifier.SendTelegram(text)
		sentCount = 1
		sentTargets = append(sentTargets, "global chat_id="+globalChatID)
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

// findEnabledExitServer scans exit_servers for an
// enabled row whose node_id matches. Returns (nil, nil)
// when no row matches; (nil, err) on a real DB error;
// (row, nil) on success. Kept private to the admin
// package because the egress selector is the only caller.
func findEnabledExitServer(d *sql.DB, nodeID string) (*db.ExitServer, error) {
	rows, err := db.ListExitServers(d)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].NodeID == nodeID && rows[i].Enabled {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// TelegramCIDRs is the canonical Telegram IP list mirrored
// from deploy/tailscale-relay/update-routes.sh. The same
// constant lives in docs/telegram-relay.md; the helper
// SetAdvertisedRoutes dedupes + prepends 0.0.0.0/0+::/0 so
// the relay keeps its exit-node capability.
//
// IPv4 covers api.telegram.org + the DC ranges; IPv6 is
// aspirational (headscale routes it but Tailscale clients
// may not advertise the v6 routes without an explicit
// --advertise-routes flag on the client).
var TelegramCIDRs = []string{
	"91.108.4.0/22", "91.108.8.0/22", "91.108.12.0/22",
	"91.108.16.0/22", "91.108.20.0/22", "91.108.56.0/22",
	"149.154.160.0/20", "185.76.151.0/24",
	"2001:67c:4e8::/48", "2001:b28:f23c::/48",
	"2001:b28:f23f::/48", "2001:7a0:1::/48",
}

// (the sqlDB interface alias was removed in v0.33.1.8 —
// findEnabledExitServer takes *sql.DB directly now).

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

// readContainerTailscaleState (B185) shells out to
// `docker exec <container> tailscale status --json` and
// returns a structured snapshot. Returns Available=false
// when the container isn't reachable (no docker socket
// mounted, wrong hostname, tailscaled not running) so
// the template can render a clean "diagnostic not
// available" message instead of failing the page.
//
// The `tailscale set --accept-routes=true` recovery
// action (handleTelegramReapplyAcceptRoutes) is the
// same path the B185 entrypoint fix takes on every
// container restart, so the manual "Re-apply" button
// is only needed when the operator restarted the
// container after the entrypoint fix was deployed but
// the persisted state still has RouteAll=false (e.g.
// tailscale set --accept-routes=false was run by hand
// at some point).
func readContainerTailscaleState(containerName string) ContainerTailscaleState {
	var out ContainerTailscaleState
	out.Hostname = containerName
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", containerName,
		"tailscale", "status", "--json")
	stdout, err := cmd.Output()
	if err != nil {
		out.RawStderr = err.Error()
		return out
	}
	var raw struct {
		BackendState string   `json:"BackendState"`
		TailscaleIPs []string `json:"TailscaleIPs"`
		Self         struct {
			HostName string   `json:"HostName"`
			Tags     []string `json:"Tags"`
		} `json:"Self"`
		Prefs struct {
			RouteAll      bool     `json:"RouteAll"`
			AdvertiseTags []string `json:"AdvertiseTags"`
			ExitNodeID    string   `json:"ExitNodeID"`
		} `json:"Prefs"`
	}
	if err := json.Unmarshal(stdout, &raw); err != nil {
		out.RawStderr = "parse: " + err.Error()
		return out
	}
	out.Available = true
	out.BackendState = raw.BackendState
	if raw.Self.HostName != "" {
		out.Hostname = raw.Self.HostName
	}
	for _, ip := range raw.TailscaleIPs {
		if strings.Contains(ip, ":") {
			out.IP6 = ip
		} else {
			out.IP4 = ip
		}
	}
	out.RouteAll = raw.Prefs.RouteAll
	if len(raw.Prefs.AdvertiseTags) > 0 {
		out.AdvertiseTags = raw.Prefs.AdvertiseTags
	} else if len(raw.Self.Tags) > 0 {
		// Fallback to Self.Tags — `tailscale status --json`
		// reports the SAME tag list under both Prefs and Self
		// but the tailnet ACLs can sometimes strip one or
		// the other depending on the headscale version. Using
		// either is fine for the operator-facing display.
		out.AdvertiseTags = raw.Self.Tags
	}
	out.ExitNodeID = raw.Prefs.ExitNodeID
	out.HasAcceptIssue = !out.RouteAll || len(out.AdvertiseTags) == 0
	return out
}

// runContainerTailscale (B185) shells out to
// `docker exec <container> tailscale set <args>`. The
// "Re-apply accept-routes" button uses
// `tailscale set --accept-routes=true` (NOT
// `tailscale up --accept-routes`) precisely because the
// `tailscale up` form is "all-or-nothing" and breaks
// when the persisted state has --advertise-tags set
// (the B185 entrypoint bug). `tailscale set` only
// patches the specified field, which is what we want
// here.
func runContainerTailscale(containerName string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	full := append([]string{"exec", containerName, "tailscale", "set"}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// handleTelegramReapplyAcceptRoutes (B185) is the
// one-click fix for the B185 entrypoint bug: if the
// container's tailscaled state has RouteAll=false
// (the B185 root cause), run `tailscale set
// --accept-routes=true` to flip the bit without
// breaking the "requires mentioning all non-default
// flags" check. The persisted state is updated, the
// routes propagate to the kernel within a few seconds,
// and the next probe will see "ok_relay" instead of
// "unreachable".
func (s *Service) handleTelegramReapplyAcceptRoutes(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	const containerName = "skygate-skygate-1"
	out, err := runContainerTailscale(containerName, "--accept-routes=true")
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "telegram_reapply_accept_routes",
			fmt.Sprintf("container=%s err=%q out=%q", containerName, err.Error(), out))
		s.redirectWithFlash(w, r, "", fmt.Sprintf("docker exec tailscale set --accept-routes=true: %v (%s)", err, out))
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "telegram_reapply_accept_routes",
		fmt.Sprintf("container=%s out=%q", containerName, out))
	s.invalidateTelegramProbe()
	s.redirectWithFlash(w, r, "Container tailscaled: --accept-routes=true применён. Probe обновится в течение 30с.", "")
}

// renderProbeHTML returns the `<div class="alert alert-probe
// probe-{state}">…</div>` block used by both AdminTelegram
// (synchronous path) and AdminTelegramProbeBg (async path).
// The output is wrapped in `id="telegram-probe-slot"` so the JS
// in /admin/telegram.html can swap it via
// `document.getElementById('telegram-probe-slot').outerHTML = …`
// after fetch.
//
// `container` is the live state from readContainerTailscaleState —
// it's used to choose the FIRST troubleshooting tip when the probe
// is unreachable (the operator's B255 follow-up: the most common
// cause on the live VM was a RouteAll=false container, not a
// missing relay, so the pre-B255 banner that always listed the
// 4 generic tips pointed at the wrong knob).
//
// Mirrors admin/telegram.html lines 55-103. If you change the
// template, change this function in lockstep — the unit tests in
// telegram_b255_test.go assert the structural parity.
func renderProbeHTML(probe TelegramProbeResult, container ContainerTailscaleState, lang string) string {
	stateStr := probe.State.String()
	var sb strings.Builder
	sb.WriteString(`<div id="telegram-probe-slot" class="alert alert-probe probe-`)
	sb.WriteString(stateStr)
	sb.WriteString(`">`)
	switch stateStr {
	case "ok_direct":
		sb.WriteString(`<i class="fa-solid fa-globe"></i>`)
	case "ok_relay":
		sb.WriteString(`<i class="fa-solid fa-route"></i>`)
	default:
		sb.WriteString(`<i class="fa-solid fa-triangle-exclamation"></i>`)
	}
	sb.WriteString(`<div><strong>`)
	switch stateStr {
	case "ok_direct":
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_ok_direct_label")))
	case "ok_relay":
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_ok_relay_label")))
	default:
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_unreachable_label")))
	}
	sb.WriteString(`</strong><div class="sub">`)
	if probe.Message != "" {
		sb.WriteString(html.EscapeString(probe.Message))
	}
	if probe.LatencyMS != "" {
		sb.WriteString(" ")
		sb.WriteString(html.EscapeString(i18n.Tf(lang, "telegram.probe_latency", probe.LatencyMS)))
	}
	sb.WriteString(`</div>`)
	if len(probe.ResolvedIPs) > 0 {
		sb.WriteString(`<div class="sub" style="font-family:monospace;font-size:.85em">`)
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_resolved_prefix")))
		for _, ip := range probe.ResolvedIPs {
			sb.WriteString("<code>")
			sb.WriteString(html.EscapeString(ip))
			sb.WriteString("</code> ")
		}
		sb.WriteString(`</div>`)
	}
	if stateStr == "unreachable" {
		sb.WriteString(`<div class="sub" style="margin-top:.5rem"><strong>`)
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_troubleshooting")))
		sb.WriteString(`</strong><ul style="margin:.4rem 0 0 1.2rem;line-height:1.5">`)
		// B-bug-fix (2026-09-15): surface the actual cause first
		// instead of the generic 4 tips. Mirrors the template
		// patch on lines 90-94 of admin/telegram.html.
		if !container.Available {
			sb.WriteString(`<li><strong style="color:varc#a00)">`)
			sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_tip_container_off")))
			sb.WriteString(`</strong></li>`)
		} else if !container.RouteAll {
			sb.WriteString(`<li><strong style="color:varc#a00)">`)
			sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.probe_tip_container_no_accept")))
			sb.WriteString(`</strong></li>`)
		}
		for _, tipKey := range []string{
			"telegram.probe_tip_advertise",
			"telegram.probe_tip_approve",
			"telegram.probe_tip_update",
			"telegram.probe_tip_docs",
		} {
			sb.WriteString(`<li>`)
			sb.WriteString(html.EscapeString(i18n.T(lang, tipKey)))
			sb.WriteString(`</li>`)
		}
		sb.WriteString(`</ul></div>`)
	}
	sb.WriteString(`</div></div>`)
	return sb.String()
}

// renderContainerHTML returns the inner block of the container
// tailscale diagnostic card (admin/telegram.html lines 218-260).
// Wrapped in `id="telegram-container-slot"` so the JS can swap
// it. The `csrf` argument is written into the "Re-apply
// accept-routes" form (only rendered when HasAcceptIssue=true).
func renderContainerHTML(ct ContainerTailscaleState, csrf string, lang string) string {
	var sb strings.Builder
	sb.WriteString(`<div id="telegram-container-slot">`)
	if !ct.Available {
		sb.WriteString(`<div class="alert alert-warn"><i class="fa-solid fa-triangle-exclamation"></i> `)
		sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.container_unavailable")))
		if ct.RawStderr != "" {
			sb.WriteString(`<pre style="margin:.4rem 0 0;font-size:.8em;color:#666">`)
			sb.WriteString(html.EscapeString(ct.RawStderr))
			sb.WriteString(`</pre>`)
		}
		sb.WriteString(`</div>`)
	} else {
		sb.WriteString(`<table class="kv" style="font-size:.92em;line-height:1.5">`)
		row := func(label, value string) {
			sb.WriteString(`<tr><th style="text-align:left;width:14em">`)
			sb.WriteString(html.EscapeString(label))
			sb.WriteString(`</th><td>`)
			sb.WriteString(value) // value is pre-built (already HTML-safe code)
			sb.WriteString(`</td></tr>`)
		}
		row(i18n.T(lang, "telegram.container_hostname"), "<code>"+html.EscapeString(ct.Hostname)+"</code>")
		backend := "<code>" + html.EscapeString(ct.BackendState) + "</code>"
		if ct.BackendState == "Running" {
			backend += `<span class="muted">✓</span>`
		} else {
			backend += `<span class="muted">⚠</span>`
		}
		row(i18n.T(lang, "telegram.container_backend"), backend)
		row(i18n.T(lang, "telegram.container_ip4"), "<code>"+html.EscapeString(ct.IP4)+"</code>")
		row(i18n.T(lang, "telegram.container_ip6"), "<code>"+html.EscapeString(ct.IP6)+"</code>")
		var routeAllCell string
		if ct.RouteAll {
			routeAllCell = `<span class="badge badge-success">` +
				html.EscapeString(i18n.T(lang, "telegram.container_route_all_on")) + `</span>`
		} else {
			routeAllCell = `<span class="badge badge-danger">` +
				html.EscapeString(i18n.T(lang, "telegram.container_route_all_off")) +
				`</span><span class="muted" style="margin-left:.4rem">— ` +
				html.EscapeString(i18n.T(lang, "telegram.container_route_all_off_help")) + `</span>`
		}
		row(i18n.T(lang, "telegram.container_route_all"), routeAllCell)
		var tagsCell string
		if len(ct.AdvertiseTags) > 0 {
			for _, tag := range ct.AdvertiseTags {
				tagsCell += "<code>" + html.EscapeString(tag) + "</code> "
			}
		} else {
			tagsCell = `<span class="badge badge-danger">` +
				html.EscapeString(i18n.T(lang, "telegram.container_no_tags")) + `</span>`
		}
		row(i18n.T(lang, "telegram.container_advertise_tags"), tagsCell)
		var exitNodeCell string
		if ct.ExitNodeID != "" {
			exitNodeCell = "<code>" + html.EscapeString(ct.ExitNodeID) + "</code>"
		} else {
			exitNodeCell = `<span class="muted">` +
				html.EscapeString(i18n.T(lang, "telegram.container_no_exit_node")) + `</span>`
		}
		row(i18n.T(lang, "telegram.container_exit_node"), exitNodeCell)
		sb.WriteString(`</table>`)
		if ct.HasAcceptIssue {
			sb.WriteString(`<div class="alert alert-warn" style="margin-top:.5rem"><i class="fa-solid fa-triangle-exclamation"></i> `)
			sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.container_issue_help")))
			sb.WriteString(`</div>`)
			sb.WriteString(`<form action="/admin/telegram" method="POST" style="margin-top:.5rem" onsubmit="return confirm('`)
			sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.container_reapply_confirm")))
			sb.WriteString(`')">`)
			sb.WriteString(`<input type="hidden" name="csrf" value="`)
			sb.WriteString(html.EscapeString(csrf))
			sb.WriteString(`">`)
			sb.WriteString(`<input type="hidden" name="action" value="reapply_accept_routes">`)
			sb.WriteString(`<button type="submit" class="btn btn-primary"><i class="fa-solid fa-rotate"></i> `)
			sb.WriteString(html.EscapeString(i18n.T(lang, "telegram.container_reapply_button")))
			sb.WriteString(`</button></form>`)
		}
	}
	sb.WriteString(`</div>`)
	return sb.String()
}

// handleTelegramSetNearestEgress (B255, 2026-09-16) is the
// one-click "Pin nearest exit node" button.
//
// It enumerates the enabled exit_servers, reads
// `tailscale status --json` on the host (skygate-host-1) to
// pick up each peer's `PeerLatency`, sorts by latency ascending
// and applies the canonical Telegram-CIDR to the FASTEST one —
// the same SSH+advertise-routes code path as
// handleTelegramSetEgress. If the host's tailscaled isn't
// running (no PeerLatency data) the operator gets a clear
// flash pointing at /admin/tailscale (Start), and we fall
// back to the FIRST enabled relay so the button still does
// something useful (the operator can manually pick another
// via the dropdown above).
func (s *Service) handleTelegramSetNearestEgress(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	lang := s.I18n.LangFromRequest(r)
	auditUID, auditName := s.Backend.InfraAuditIdentity(c.UserID, c.Username)
	relays, err := db.ListExitServers(s.dbc())
	if err != nil {
		s.Backend.Audit(auditUID, auditName, "telegram_egress_set_nearest",
			fmt.Sprintf("err=%q", err.Error()))
		writeErrRedirect(w, r, i18n.Tf(lang, "telegram.egress_apply_fail", err.Error()))
		return
	}
	enabled := make([]db.ExitServer, 0, len(relays))
	for _, e := range relays {
		if e.Enabled {
			enabled = append(enabled, e)
		}
	}
	if len(enabled) == 0 {
		s.Backend.Audit(auditUID, auditName, "telegram_egress_set_nearest",
			"no_enabled_relays")
		writeErrRedirect(w, r, i18n.T(lang, "telegram.egress_no_relays"))
		return
	}

	// Measure latency from skygate-host's tailscaled. If
	// tailscaled isn't running the PeerLatency map is empty —
	// fall back to the first enabled relay (sorted by node_id
	// for stability) so the click still applies SOMETHING and
	// the operator gets an actionable flash.
	latencies, latErr := tailscalePeerLatencies()
	if latErr != nil {
		s.Backend.Audit(auditUID, auditName, "telegram_egress_set_nearest",
			fmt.Sprintf("latency_err=%q fallback=first", latErr.Error()))
	}
	// Sort the enabled relays by latency ascending; relays with
	// no latency measurement land at the bottom so they're only
	// picked if NO relay has a measurement.
	sort.SliceStable(enabled, func(i, j int) bool {
		li, oki := latencies[enabled[i].Hostname]
		lj, okj := latencies[enabled[j].Hostname]
		if oki != okj {
			return oki // measured relays first
		}
		if !oki {
			return false // both unmeasured — preserve ListExitServers order
		}
		return li < lj
	})

	picked := enabled[0]
	s.Backend.Audit(auditUID, auditName, "telegram_egress_set_nearest",
		fmt.Sprintf("picked=%s latency_ms=%s candidates=%d",
			picked.Hostname,
			strconv.FormatFloat(latencies[picked.Hostname], 'f', 1, 64),
			len(enabled)))

	// Apply via the shared helper. We rewrite r.FormValue
	// by injecting the picked node_id into r.PostForm so the
	// applyEgress helper picks it up. (handleTelegramSetEgress
	// already does the SSH + advertise-routes + audit work;
	// we reuse it to avoid duplicating 80 lines of code.)
	r.PostForm.Set("node_id", picked.NodeID)
	r.Form.Set("node_id", picked.NodeID)
	s.handleTelegramSetEgress(w, r, c)
}

// tailscaleStatusExecFn is the indirection that unit tests
// override to stub the `tailscale status --json` output
// without running tailscale on the host. Production code
// calls tailscalePeerLatencies; tests use this var to
// return canned JSON. The ctx is passed through so the
// real implementation honours the 5s timeout (tests
// typically ignore it).
var tailscaleStatusExecFn = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
}

// tailscalePeerLatencies (B255) returns a map of
// headscale `HostName` → round-trip latency in milliseconds,
// derived from `tailscale status --json` on the HOST
// (skygate-host-1). Returns (empty, nil) when tailscaled
// isn't running — callers treat that as "no measurement
// available" rather than a hard error.
//
// Why "on host" instead of inside the skygate container:
// the latency we care about is the routing latency from
// the host that runs skygate to the relay — that's the
// path the Telegram-CIDR traffic will actually take. The
// container's view is its OWN tailscaled peer graph, which
// is a different (often higher-latency) path.
//
// The status JSON exposes PeerLatency as either a number
// (the legacy "ms" form) or an object { "ms": N, ... }
// (newer clients). We accept both.
func tailscalePeerLatencies() (map[string]float64, error) {
	out := map[string]float64{}
	if !tailscaledRunning() {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := tailscaleStatusExecFn(ctx)
	if err != nil {
		return nil, fmt.Errorf("tailscale status: %w", err)
	}
	// Use json.Number via decoder so we don't lose precision
	// on the latency field. The shape is:
	//   "Peer": { "<nodekey>": { "HostName": "...", "PeerLatency": { "ms": 12.3 } } }
	// but legacy tailscale clients emit "PeerLatency": 12.3
	// (number, not object). We accept both via a custom
	// unmarshal hook on a wrapper type.
	var decoded struct {
		Peer map[string]struct {
			HostName    string          `json:"HostName"`
			PeerLatency json.RawMessage `json:"PeerLatency"`
		} `json:"Peer"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("parse status: %w", err)
	}
	for _, p := range decoded.Peer {
		if p.HostName == "" || len(p.PeerLatency) == 0 {
			continue
		}
		var ms float64
		// Try the object form first: {"ms": 12.3, ...}
		var obj struct {
			MS json.Number `json:"ms"`
		}
		if err := json.Unmarshal(p.PeerLatency, &obj); err == nil && obj.MS != "" {
			if f, ferr := strconv.ParseFloat(string(obj.MS), 64); ferr == nil {
				ms = f
			}
		}
		if ms == 0 {
			// Try the legacy bare-number form: 12.3
			if f, ferr := strconv.ParseFloat(string(p.PeerLatency), 64); ferr == nil {
				ms = f
			}
		}
		if ms > 0 {
			out[p.HostName] = ms
		}
	}
	return out, nil
}
