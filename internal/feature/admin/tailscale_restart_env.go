// tailscale_restart_env.go — the "restart skygate" action, the
// in-container .env rewrite it depends on, and the shared redirect.
//
// Split out of tailscale.go in refactor Phase D (2026-10-01).  The
// restart is deliberately the only handler that SIGTERMs the process it
// runs in, so it carries the B323 service-control rationale with it; the
// .env rewrite exists because entrypoint.sh reads
// SKYGATE_TS_LOGIN_SERVER once, at container start.
//
//   - handleTailscaleRestart (B323)
//   - isRunningInContainer
//   - updateEnvFileSKYGATE_TS_LOGIN_SERVER
//   - tsRedirect — used by every /admin/tailscale POST action above

package admin

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"skygate/internal/auth"
)

// handleTailscaleRestart restarts the skygate process (not
// just tailscaled). v0.33.1.16.
//
// WHY this exists: the entrypoint.sh reads SKYGATE_TS_LOGIN_SERVER
// at container start. Saving the value via /admin/tailscale
// only writes to the DB + (after this fix) the .env file.
// The operator's next step was either:
//
//	(a) SSH in and run `docker compose restart skygate` (or
//	    `systemctl restart skygate` on a native host), or
//	(b) remember to restart before saving. Both are error-prone.
//
// This endpoint makes restart a single click.
//
// Flow:
//  1. Determine the current effective login_server (DB > .env > default).
//  2. Write it to the in-container .env (atomic via .tmp + rename).
//     This makes the next entrypoint invocation pick up the
//     new value.
//  3. Trigger the restart through the B323 SERVICE CONTROL block
//     (serviceCtlCommandForKind): docker →
//     `docker compose -p <project> -f <host-compose-file> restart skygate`,
//     systemd → `systemctl restart skygate` (or `service skygate restart`
//     without systemctl), OpenRC → `rc-service skygate restart`, and a
//     Kubernetes/bare-binary install is refused with a message that names the
//     reason. The setsid detachment is critical — the parent skygate process
//     gets SIGTERM'd by the action and any child in the same process group dies
//     with it (see runDetachedServiceControl).
//  4. Return success to the client IMMEDIATELY (the response
//     flushes before the SIGTERM arrives; the spawn sleeps 500ms for that).
//
// B323 note: the operator asked for this control to be discoverable, so the page
// to press is now /admin/service. This entry point stays because contracts
// reference the restart_skgate action.
//
// Audit: full event log including effective URL, install kind,
// in_container and the exact command line.
func (s *Service) handleTailscaleRestart(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	effective := s.tailscaleLoginServer()

	// Step 1 (unchanged): write the effective value back to the in-container .env
	// so the next entrypoint invocation picks it up. Best-effort — if the .env is
	// read-only or does not exist (native host), the restart is attempted anyway.
	envPath := filepath.Join(s.Cfg.RepoPath, ".env")
	envUpdateMsg := ""
	if _, err := os.Stat(envPath); err == nil {
		if err := updateEnvFileSKYGATE_TS_LOGIN_SERVER(envPath, effective); err != nil {
			s.Backend.Audit(c.UserID, c.Username, "tailscale_restart_skgate",
				fmt.Sprintf("err=env_update_failed path=%s err=%q",
					envPath, err.Error()))
			tsRedirect(w, r, "", "Не удалось обновить .env: "+err.Error())
			return
		}
		envUpdateMsg = " .env обновлён"
	}

	// Step 2: B323 — the restart itself now comes from the SERVICE CONTROL block.
	// Before B323 this branch read isRunningInContainer() alone, so OpenRC,
	// Kubernetes and bare-binary installs were pushed through
	// `systemctl restart skygate || service skygate restart` and the operator got a
	// failure that named neither the install kind nor the reason. Both entry points
	// now execute the SAME kind-aware argv (serviceCtlCommandForKind) via
	// runDetachedServiceControl, and /admin/service is where the operator finds it
	// without hunting for it.
	state := s.loadServiceControlState()
	if !state.CanRestart {
		refusalKey := firstNonEmpty(state.RestartRefusal, "service_ctl.restart_unavailable")
		s.Backend.Audit(c.UserID, c.Username, "tailscale_restart_skgate",
			fmt.Sprintf("refused=%s kind=%s", refusalKey, state.Kind))
		tsRedirect(w, r, "", refusalKey+" — см. /admin/service")
		return
	}
	argv, display, _ := pickServiceControlAction("restart", state)
	restartMethod := fmt.Sprintf("%s:%s", state.Kind, display)

	s.Backend.Audit(c.UserID, c.Username, "tailscale_restart_skgate",
		fmt.Sprintf("login_server=%q kind=%s in_container=%v method=%s%s",
			effective, state.Kind, state.InContainer, restartMethod, envUpdateMsg))

	// Return IMMEDIATELY. The Go process is about to be SIGTERM'd by the restart
	// we just triggered; the response must flush before that happens (the goroutine
	// sleeps 500ms for exactly that reason). The redirect target reloads the page
	// after the restart completes, so the operator sees the new build label.
	tsRedirect(w, r,
		fmt.Sprintf("Перезапуск запущен (%s). Страница вернётся через ~30s с новой версией.", restartMethod),
		"")
	go func() {
		time.Sleep(500 * time.Millisecond)
		runDetachedServiceControl(argv, display)
	}()
}

// isRunningInContainer returns true if the current process is
// running inside a Docker/Podman/CRI-O container. We check the
// well-known marker files: /.dockerenv (Docker), /run/.containerenv
// (Podman + generic OCI). Bare-metal systemd hosts return
// false.
func isRunningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	return false
}

// updateEnvFileSKYGATE_TS_LOGIN_SERVER sets or replaces the
// SKYGATE_TS_LOGIN_SERVER= line in the given .env file.
// Atomic: write to .env.tmp, fsync, rename. The file's other
// lines are preserved as-is (no comment-stripping, no
// normalization — operators may have hand-edited the file
// with non-standard formatting).
//
// If the file doesn't contain SKYGATE_TS_LOGIN_SERVER=
// yet, the new value is appended on a new line (with a
// trailing newline). If the value is empty, the existing
// line (if any) is removed (clears the override → next
// compose-up will not pass SKYGATE_TS_LOGIN_SERVER to the
// container unless something else sets it).
func updateEnvFileSKYGATE_TS_LOGIN_SERVER(envPath, newValue string) error {
	data, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", envPath, err)
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines)+1)
	found := false
	prefix := "SKYGATE_TS_LOGIN_SERVER="
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			found = true
			if newValue != "" {
				out = append(out, prefix+newValue)
			}
			// else: skip (clears the override)
			continue
		}
		out = append(out, line)
	}
	if !found && newValue != "" {
		out = append(out, prefix+newValue)
	}
	newContent := strings.Join(out, "\n")
	tmpPath := envPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, envPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// tsRedirect is a flash-and-redirect back to /admin/tailscale.
func tsRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	q := ""
	switch {
	case okMsg != "":
		q = "?ok=" + urlQueryEscape(okMsg)
	case errMsg != "":
		q = "?err=" + urlQueryEscape(errMsg)
	}
	http.Redirect(w, r, "/admin/tailscale"+q, http.StatusSeeOther)
}
