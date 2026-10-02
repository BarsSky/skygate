// telegram_handlers.go — the page and its POST dispatcher.
//
// Split out of telegram.go in refactor Phase D (2026-10-01): GET /admin/telegram,
// the two background-fragment endpoints, the CSRF mint and the action switch.

package admin

import (
	"crypto/subtle"
	"fmt"
	"html"
	"net/http"
	"strings"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

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
