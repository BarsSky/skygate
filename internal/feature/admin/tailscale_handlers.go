// tailscale_handlers.go — the /admin/tailscale page and the
// save/start/stop half of its POST dispatch.
//
// Split out of tailscale.go in refactor Phase D (2026-10-01).  What is
// left here is exactly the surface the sidebar links to: GET renders the
// five cards, POST routes an `action=` form field to one handler, and
// each handler validates the admin claim, calls a runtime helper and
// redirects through tsRedirect.
//
//   - GetAdminTailscale / PostAdminTailscale
//   - handleTailscaleSaveKey / handleTailscaleSaveLoginServer
//   - handleTailscaleStart / handleTailscaleStop
//
// The key-minting and in-container toggle actions live in
// tailscale_enable.go; the restart action and the .env rewrite live in
// tailscale_restart_env.go.

package admin

import (
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/auth"
	"skygate/internal/db"
)

// GetAdminTailscale renders /admin/tailscale. Admin-only.
func (s *Service) GetAdminTailscale(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	state := s.loadTailscaleState()
	csrf, err := db.RandomConfirmationToken(8)
	if err != nil {
		http.Error(w, "csrf generation failed", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "skygate_ts_csrf",
		Value:    csrf,
		Path:     "/admin/tailscale",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	s.Backend.RenderWithLayout(w, r, "admin/tailscale.html", c, map[string]any{
		"Page":         "admin/tailscale",
		"Title":        "Tailscale",
		"State":        state,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
		"CSRF":         csrf,
	})
}

// PostAdminTailscale dispatches the form. Admin-only. CSRF
// enforced (constant-time compare with the skygate_ts_csrf
// cookie set by GET).
func (s *Service) PostAdminTailscale(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		tsRedirect(w, r, "", "Ошибка парсинга формы: "+err.Error())
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	cookie, err := r.Cookie("skygate_ts_csrf")
	if err != nil || cookie.Value == "" {
		tsRedirect(w, r, "", "CSRF-cookie отсутствует — обновите страницу и повторите")
		return
	}
	submitted := r.FormValue("csrf")
	if subtle.ConstantTimeCompare([]byte(submitted), []byte(cookie.Value)) != 1 {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_csrf_fail",
			fmt.Sprintf("action=%s ip=%s", action, r.RemoteAddr))
		tsRedirect(w, r, "", "Неверный CSRF-токен — обновите страницу и повторите")
		return
	}
	switch action {
	case "save_key":
		s.handleTailscaleSaveKey(w, r, c)
	case "save_login_server":
		// v0.33.1.13 — persist SKYGATE_TS_LOGIN_SERVER to
		// global_settings so the value survives container
		// restarts, migrations, and VM clones. The env var
		// is only consulted when the DB row is empty.
		s.handleTailscaleSaveLoginServer(w, r, c)
	case "save_hostname":
		// B321 — persist the desired tailnet hostname so a
		// container recreate cannot rename the node back to the
		// legacy v0.33.1.9 placeholder.
		s.handleTailscaleSaveHostname(w, r, c)
	case "start":
		s.handleTailscaleStart(w, r, c)
	case "stop":
		s.handleTailscaleStop(w, r, c)
	case "enable_in_container":
		// B259: flip the DB-overridable path from /dev/null
		// (disabled) to /data/ts/authkey (enabled), generate a
		// fresh preauth key via headscale, write the key to
		// that file, and start tailscaled. Operator doesn't
		// need to edit docker-compose.yml + restart.
		s.handleTailscaleEnableInContainer(w, r, c)
	case "disable_in_container":
		// B259: flip the path from /data/ts/authkey back to
		// /dev/null (disabled) and stop tailscaled if it's
		// running. Operator doesn't need to edit
		// docker-compose.yml.
		s.handleTailscaleDisableInContainer(w, r, c)
	case "generate_key":
		// 2026-08-05 v0.33.1.11 — automated preauth key
		// generation against the running headscale. The
		// admin no longer has to copy a key from
		// /admin/headscale and paste it here — skygate
		// resolves the user behind the configured
		// hostname, calls headscale preauthkeys create,
		// and writes the key to the same /data/ts/authkey
		// file the "Save" path uses.
		s.handleTailscaleGenerateKey(w, r, c)
	case "restart_skgate":
		// v0.33.1.16 — restart the entire skygate
		// process (not just tailscaled). Required after
		// saving SKYGATE_TS_LOGIN_SERVER (the entrypoint
		// reads the env var at container start, not at
		// runtime). In container mode, this triggers
		// `docker compose restart skygate` via a detached
		// subprocess. In native mode, it triggers
		// `systemctl restart skygate`.
		s.handleTailscaleRestart(w, r, c)
	case "set_advertise_routes":
		// B236 — manage the subnet routes this skygate
		// instance advertises to the tailnet. The form
		// takes a comma-separated list of CIDRs (or empty
		// string to clear). The handler validates against
		// (a) the host's own LAN (advertising your own
		// LAN shadows other LAN clients' direct Ethernet
		// routes, see the B236 AGENTS.md entry for the
		// 2026-09-04 skyworker incident) and (b) the
		// docker bridge ranges (172.17/172.18/172.19/...)
		// which are unreachable from outside the host.
		s.handleTailscaleSetAdvertiseRoutes(w, r, c)
	default:
		tsRedirect(w, r, "", "Неизвестное действие: "+action)
	}
}

// handleTailscaleSaveKey writes the pasted auth key to the
// configured path. Does NOT start tailscaled automatically —
// that's a separate "Start" button click.
//
// B321 (2026-09-25) correction: the old comment here claimed
// that "after a container restart, the entrypoint picks the key
// up automatically". That is only true when the container
// environment leaves SKYGATE_TS_AUTHKEY_FILE empty — the
// reference compose pins it to /dev/null, and Docker freezes the
// environment at container creation, so every update recreated a
// container whose entrypoint skipped Tailscale and the operator
// had to press Start again. The process itself now honours the
// recorded intent (tailscale.desired_state) in
// RunTailscaleAutostart, which the entrypoint cannot see.
func (s *Service) handleTailscaleSaveKey(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	key := strings.TrimSpace(r.FormValue("auth_key"))
	if key == "" {
		tsRedirect(w, r, "", "Пустой auth key — вставьте preauth key из headscale")
		return
	}
	// Preauth keys typically look like <hex>. Don't over-
	// validate; the tailscale up call will reject a bad
	// key with a clear "Registration error" message.
	if err := s.writeTailscaleAuthKey(key); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_save_key",
			fmt.Sprintf("err=%q", err.Error()))
		tsRedirect(w, r, "", "Не удалось сохранить: "+err.Error())
		return
	}
	// Don't log the key. Audit the FP only.
	fp := key
	if len(fp) > 8 {
		fp = fp[:4] + "..." + fp[len(fp)-4:]
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_save_key", "fp="+fp)
	s.invalidateTailscaleState()
	tsRedirect(w, r, "Auth key сохранён. Теперь нажмите «Start» чтобы запустить tailscale.", "")
}

// handleTailscaleSaveLoginServer persists the operator-edited
// headscale URL (SKYGATE_TS_LOGIN_SERVER equivalent) to
// global_settings. The new value takes effect on the NEXT
// `tailscale up` invocation — i.e. the operator should
// follow Save with Stop → Start. v0.33.1.13.
//
// Validation: must start with http:// or https://. We don't
// try to resolve the host (could be a private LAN like
// 192.168.x.x where DNS would otherwise fail; the operator
// knows the real URL). Empty string is allowed (clears the
// override → falls back to env var).
//
// Audit: stores the full URL (it's not a secret — it's the
// public headscale endpoint the operator wants to join).
func (s *Service) handleTailscaleSaveLoginServer(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	raw := strings.TrimSpace(r.FormValue("login_server"))
	if raw != "" {
		// Use url.Parse; require a non-empty scheme that is
		// http or https and a non-empty host. Don't try to
		// resolve it — see comment above.
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			tsRedirect(w, r, "", "Некорректный URL. Ожидается https:// или http://, например https://head.example.com.")
			return
		}
	}
	if err := db.SetGlobalSetting(s.dbc(), tailscaleLoginServerDBKey, raw); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_save_login_server",
			fmt.Sprintf("err=%q", err.Error()))
		tsRedirect(w, r, "", "Не удалось сохранить: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_save_login_server",
		"url_set="+strconv.FormatBool(raw != "")+" value="+raw)
	// Invalidate the state cache so the next GET shows
	// the new source ("db") + value immediately.
	s.invalidateTailscaleState()
	// Different success message depending on whether
	// tailscaled is currently running (operator may want
	// to restart it to pick up the new value).
	if s.loadTailscaleState().Running {
		tsRedirect(w, r, "Headscale URL сохранён в БД. Перезапустите Tailscale (Stop → Start), чтобы применить.", "")
		return
	}
	tsRedirect(w, r, "Headscale URL сохранён в БД. Будет использован при следующем Start.", "")
}

// handleTailscaleStart spawns tailscaled + runs tailscale up.
// Idempotent: a second click on an already-running tailscaled
// returns a flash noting "already running" (no error).
func (s *Service) handleTailscaleStart(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	// B258: refuse when the operator explicitly disabled
	// Tailscale in the container via SKYGATE_TS_AUTHKEY_FILE
	// (typically =/dev/null). The entrypoint.sh skip check
	// already skipped tailscaled on entry; the UI just
	// surfaces the reason so the operator doesn't try to
	// click Start and get a confusing "no such file" error.
	if s.tailscaleAuthKeyDisabled() {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_start",
			"refused=auth_key_disabled path="+s.tailscaleAuthKeyPath())
		tsRedirect(w, r, "",
			"Tailscale отключён в конфигурации контейнера (SKYGATE_TS_AUTHKEY_FILE=/dev/null). "+
				"Измените docker-compose.yml и перезапустите skygate.")
		return
	}
	// B258.1: refuse early when the path is a regular file
	// path but the file is missing or empty. This is the
	// third state the B258 design didn't model — the UI's
	// Start button is now hard-disabled when this state is
	// active (template tailscale.html:246), so an operator
	// reaching this branch has bypassed the disabled-button
	// guard (e.g. via direct POST). Give a clear actionable
	// error instead of the raw "read auth key: no such file"
	// ENOENT.
	if s.tailscaleAuthKeyMissingForStart() {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_start",
			"refused=auth_key_missing path="+s.tailscaleAuthKeyPath())
		tsRedirect(w, r, "",
			"Файл ключа Tailscale не найден: "+s.tailscaleAuthKeyPath()+
				". Вставьте preauth key на этой странице или сгенерируйте его через кнопку «Сгенерировать ключ».")
		return
	}
	out, err := s.startTailscaled()
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_start",
			fmt.Sprintf("err=%q out=%q", err.Error(), truncate(out, 200)))
		tsRedirect(w, r, "", "Не удалось запустить Tailscale: "+err.Error()+" — output: "+truncate(out, http.StatusBadRequest))
		return
	}
	// B321: record the operator's intent so this process brings the client up by
	// itself after the next container recreate, instead of waiting for another click.
	if err := s.setTailscaleDesiredState(tailscaleDesiredOn); err != nil {
		log.Printf("tailscale: could not persist tailscale.desired_state=on: %v", err)
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_start", "ok out="+truncate(out, 200))
	s.invalidateTailscaleState()
	if strings.TrimSpace(out) == "" {
		tsRedirect(w, r, "Tailscale запущен. Проверьте accepted routes через ~30s.", "")
		return
	}
	tsRedirect(w, r, "Tailscale запущен. Output: "+truncate(out, http.StatusBadRequest), "")
}

// handleTailscaleStop kills tailscaled. Idempotent.
func (s *Service) handleTailscaleStop(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	out, err := s.stopTailscaled()
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_stop",
			fmt.Sprintf("err=%q out=%q", err.Error(), out))
		tsRedirect(w, r, "", "Не удалось остановить: "+err.Error())
		return
	}
	// B321: an explicit Stop is an intent too — without this the autostart would
	// bring the client straight back up on the next tick.
	if err := s.setTailscaleDesiredState(tailscaleDesiredOff); err != nil {
		log.Printf("tailscale: could not persist tailscale.desired_state=off: %v", err)
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_stop", "ok out="+truncate(out, 200))
	s.invalidateTailscaleState()
	tsRedirect(w, r, "Tailscale остановлен.", "")
}
