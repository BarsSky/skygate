// tailscale_enable.go — "Generate automatically" and the B259
// enable/disable-in-container actions.
//
// Split out of tailscale.go in refactor Phase D (2026-10-01).  These
// four handlers are the ones that WRITE the auth-key path instead of
// just reading it, and they are the ones B251/B259/B321 pin: they must
// resolve the headscale owner through the canonical findUserForHostname
// (see tailscale.go), they must persist `tailscale.desired_state` so an
// explicit disable survives a container recreate, and they must never
// log the key bytes.
//
//   - handleTailscaleGenerateKey
//   - handleTailscaleEnableInContainer
//   - generateAndWriteTailscaleKeyForEnable
//   - handleTailscaleDisableInContainer

package admin

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"skygate/internal/auth"
	"skygate/internal/db"
)

// 2026-08-05 v0.33.1.11 — automated preauth key generation.
//
// handleTailscaleGenerateKey is the "Generate automatically"
// button on /admin/tailscale. The flow:
//  1. Resolve the headscale user that owns a node with the
//     configured hostname (default "skygate-host-1"). The
//     admin's first node registration creates the user; the
//     first /admin/headscale preauth key the operator
//     generated in the past is what bootstrapped that node,
//     so the user row is guaranteed to exist if the node
//     exists.
//  2. Call headscale preauthkeys create (API + CLI fallback
//     inside the headscale pkg) with a 1h expiration and
//     reusable=true. The 1h is conservative — the same key
//     is reusable for the container's lifetime, but a short
//     window limits the blast radius if the key leaks.
//  3. Write the returned key to the same /data/ts/authkey
//     path the "Save" path uses. Mode 0600.
//  4. Audit: tailscale_generate_key|username|user_id=N
//     hostname=X user_name=Y exp=1h reusable=true fp=tske...wxyz
//     (FP only; full key never logged).
//
// The handler does NOT auto-start tailscaled — the operator
// still clicks "Start" explicitly so they're aware the
// tailscaled is about to come up with the new key.
func (s *Service) handleTailscaleGenerateKey(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	hs := s.HSGlobalFn()
	if hs == nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_generate_key", "err=headscale_not_configured")
		tsRedirect(w, r, "", "Headscale клиент не сконфигурирован — skygate не знает URL/API key")
		return
	}
	hostname := s.tailscaleHostname()
	uid, userName, err := s.findUserForHostname(r.Context(), hs, hostname)
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_generate_key",
			fmt.Sprintf("err=%q hostname=%q", err.Error(), hostname))
		tsRedirect(w, r, "", "Не удалось найти headscale-пользователя для "+hostname+": "+err.Error())
		return
	}
	key, err := hs.CreatePreauthKey(uid, "1h", true)
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_generate_key",
			fmt.Sprintf("err=%q user_id=%d hostname=%q", err.Error(), uid, hostname))
		tsRedirect(w, r, "", "headscale.CreatePreauthKey: "+err.Error())
		return
	}
	if key == nil || key.Key == "" {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_generate_key",
			fmt.Sprintf("err=empty_key user_id=%d hostname=%q", uid, hostname))
		tsRedirect(w, r, "", "headscale вернул пустой ключ — проверьте логи headscale")
		return
	}
	if err := s.writeTailscaleAuthKey(key.Key); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_generate_key",
			fmt.Sprintf("err=%q user_id=%d hostname=%q", err.Error(), uid, hostname))
		tsRedirect(w, r, "", "Не удалось сохранить ключ: "+err.Error())
		return
	}
	// FP only in the audit log.
	fp := key.Key
	if len(fp) > 8 {
		fp = fp[:4] + "..." + fp[len(fp)-4:]
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_generate_key",
		fmt.Sprintf("user_id=%d hostname=%q user_name=%q exp=1h reusable=true fp=%s",
			uid, hostname, userName, fp))
	s.invalidateTailscaleState()
	tsRedirect(w, r, fmt.Sprintf("Preauth key сгенерирован для %s (user=%s, 1h, reusable). Теперь нажмите «Start» чтобы запустить tailscale.", hostname, userName), "")
}

// handleTailscaleEnableInContainer (B259, 2026-09-16) flips
// the in-container Tailscale from disabled (/dev/null) to
// enabled (/data/ts/authkey) entirely from the web UI. The
// operator does NOT have to edit docker-compose.yml + restart
// the container — they just click the button.
//
// Flow:
//  1. Persist the new path (/data/ts/authkey) to
//     global_settings[tailscale.auth_key_path] — overrides
//     the env var via tailscaleAuthKeyPath()'s DB-first
//     resolution order.
//  2. Generate a fresh preauth key against the running
//     headscale (same path as "Generate automatically"). The
//     key is written to /data/ts/authkey via writeTailscaleAuthKey.
//     This is a regular file (not /dev/null), so writes succeed.
//  3. Call startTailscaled() — spawns tailscaled + `tailscale up`
//     against the configured login server.
//
// Audit: tailscale_enable_in_container with the new path,
// generated key fingerprint, and start output. On any error
// the DB row is NOT rolled back (the operator may want to
// retry without regenerating a key).
func (s *Service) handleTailscaleEnableInContainer(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	const newPath = "/data/ts/authkey"
	// 1. Persist the new path to the DB override.
	if err := db.SetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, newPath); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_enable_in_container",
			"err=db_set "+err.Error())
		tsRedirect(w, r, "", "Не удалось сохранить путь в БД: "+err.Error())
		return
	}
	// 2. Generate a fresh preauth key + write to /data/ts/authkey.
	//    Re-use the existing handleTailscaleGenerateKey logic —
	//    it does the headscale lookup + writes the file + audits.
	//    But we want to combine it with a direct call so we
	//    can chain into startTailscaled. We invoke the
	//    generate helper inline by calling the headscale
	//    package's GenerateTailscaleKey (if present) or fall
	//    back to writeTailscaleAuthKey with the existing path.
	key, err := s.generateAndWriteTailscaleKeyForEnable(c.UserID, c.Username)
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_enable_in_container",
			"err=key_generate "+err.Error())
		tsRedirect(w, r, "", "Не удалось сгенерировать ключ: "+err.Error()+
			" — путь сохранён в БД, попробуйте нажать 'Start' вручную.")
		return
	}
	// 3. Start tailscaled. startTailscaled re-reads the path
	//    via tailscaleAuthKeyPath() (which now returns the
	//    DB value), reads the freshly-written key, and runs
	//    `tailscale up`. Idempotent: returns immediately if
	//    tailscaled is already running.
	out, err := s.startTailscaled()
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_enable_in_container",
			fmt.Sprintf("path=%s key_fp=%s err=start %s", newPath, key, err.Error()))
		tsRedirect(w, r, "",
			"Путь сохранён и ключ записан, но не удалось запустить tailscaled: "+err.Error()+
				" — output: "+truncate(out, 200))
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_enable_in_container",
		fmt.Sprintf("path=%s key_fp=%s out=%s", newPath, key, truncate(out, 200)))
	// B321: the enable click is the operator saying "Tailscale must be up" — record it
	// so the next container recreate brings it up without another click.
	if err := s.setTailscaleDesiredState(tailscaleDesiredOn); err != nil {
		log.Printf("tailscale: could not persist tailscale.desired_state=on: %v", err)
	}
	s.invalidateTailscaleState()
	tsRedirect(w, r,
		"Tailscale включён: путь сохранён в БД, ключ сгенерирован, tailscaled запущен.",
		"")
}

// generateAndWriteTailscaleKeyForEnable is the shared
// headscale-lookup + preauth-create + file-write helper used by
// both handleTailscaleGenerateKey (legacy "Generate automatically"
// button on /admin/tailscale) and handleTailscaleEnableInContainer
// (B259 "Включить Tailscale в контейнере" button). The latter
// needs the key fingerprint back to audit it, so it can't just
// re-use handleTailscaleGenerateKey's flash-message return
// semantics — hence this wrapper that returns the FP.
//
// User lookup is delegated to findUserForHostname (B251). For
// the reserved hostname "skygate-host", that helper pins to
// headscale user `infra` (uid=85) unconditionally per the
// operator's 2026-08-13 directive — no phantom `skygate-host`
// headscale user is ever created.
func (s *Service) generateAndWriteTailscaleKeyForEnable(actingUserID int64, actingUsername string) (string, error) {
	// B259.1 (2026-09-17): delegate the headscale-user lookup
	// to findUserForHostname (the canonical B251 helper) instead
	// of duplicating u.Name == hostname logic inline. Pre-B259.1
	// required headscale user `skygate-host` to exist — but that
	// user never existed in prod (and shouldn't: skygate-host is
	// the tailnet hostname, the headscale user is `infra` per
	// operator 2026-08-13 directive). The inline lookup forced
	// the operator to manually create a phantom user, which is
	// the wrong shape. Now: hostname `skygate-host` →
	// findUserForHostname → infraHeadscaleUserID → SELECT
	// headscale_user_id FROM portal_users WHERE username='infra'
	// → uid=85 → CreatePreauthKey(85, ...). No phantom user.
	hs := s.HSGlobalFn()
	if hs == nil {
		return "", fmt.Errorf("headscale client not configured")
	}
	hostname := s.tailscaleHostname()
	userID, userName, err := s.findUserForHostname(context.Background(), hs, hostname)
	if err != nil {
		return "", fmt.Errorf("find user for hostname %q: %w", hostname, err)
	}
	preauth, err := hs.CreatePreauthKeyWithTags(userID, "1h", true, nil)
	if err != nil {
		return "", fmt.Errorf("headscale preauthkeys create: %w", err)
	}
	if preauth == nil || preauth.Key == "" {
		return "", fmt.Errorf("headscale returned empty preauth key for user %s", userName)
	}
	if err := s.writeTailscaleAuthKey(preauth.Key); err != nil {
		return "", fmt.Errorf("write auth key: %w", err)
	}
	fp := preauth.Key
	if len(fp) > 8 {
		fp = fp[:4] + "..." + fp[len(fp)-4:]
	}
	s.Backend.Audit(actingUserID, actingUsername, "tailscale_generate_key",
		fmt.Sprintf("username=%s user_id=%d exp=1h reusable=true fp=%s", userName, userID, fp))
	return fp, nil
}

// handleTailscaleDisableInContainer (B259, 2026-09-16) flips
// the in-container Tailscale from enabled (/data/ts/authkey
// or whatever) back to disabled (/dev/null) from the web UI.
// Stops tailscaled if running + persists the disable sentinel
// to the DB. Mirrors handleTailscaleEnableInContainer.
//
// The env var SKYGATE_TS_AUTHKEY_FILE is NOT touched — the
// DB override takes precedence. On the next entrypoint restart
// the env var will be re-read, but until then the DB row
// keeps the path at /dev/null. Documented for the operator in
// the flash message.
func (s *Service) handleTailscaleDisableInContainer(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	const newPath = "/dev/null"
	// 1. Stop tailscaled if running (best-effort).
	if tailscaledRunning() {
		if _, err := s.stopTailscaled(); err != nil {
			// Non-fatal — the DB write below still disables
			// future starts. Log + continue.
			s.Backend.Audit(c.UserID, c.Username, "tailscale_disable_in_container",
				"warn=stop_failed "+err.Error())
		}
	}
	// 2. Persist the disable sentinel to the DB override.
	if err := db.SetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, newPath); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_disable_in_container",
			"err=db_set "+err.Error())
		tsRedirect(w, r, "", "Не удалось сохранить путь в БД: "+err.Error())
		return
	}
	// 3. Remove the auth key file (just to be tidy — the
	//    file at /data/ts/authkey will be ignored since the
	//    DB path now points at /dev/null, but having a
	//    dangling key on disk is a security smell).
	if err := os.Remove(s.tailscaleAuthKeyPath()); err != nil && !os.IsNotExist(err) {
		// Non-fatal.
		s.Backend.Audit(c.UserID, c.Username, "tailscale_disable_in_container",
			"warn=remove_failed "+err.Error())
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_disable_in_container",
		"path="+newPath+" stopped="+strconv.FormatBool(tailscaledRunning()))
	// B321: an explicit disable must survive a recreate as well.
	if err := s.setTailscaleDesiredState(tailscaleDesiredOff); err != nil {
		log.Printf("tailscale: could not persist tailscale.desired_state=off: %v", err)
	}
	s.invalidateTailscaleState()
	tsRedirect(w, r, "Tailscale отключён: путь /dev/null сохранён в БД, tailscaled остановлен.", "")
}
