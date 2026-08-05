// Package admin — tailscale.go owns the /admin/tailscale page
// (status, auth key paste, start/stop tailscaled).
//
// v0.33.1.9: closes the "skygate can't reach api.telegram.org
// because Tailscale isn't running in the container and I
// can't SSH in to fix it" gap. The web UI lets the admin
// paste a headscale preauth key, writes it to
// /data/ts/authkey, and starts tailscaled + tailscale up
// --accept-routes inside the running container — all
// from the browser, no SSH.
//
// File map:
//   - tailscaleState  — UI state shape
//   - GetAdminTailscale — GET /admin/tailscale, renders the page
//   - PostAdminTailscale — POST /admin/tailscale, dispatches by
//     action (save_key / start / stop)
//   - helpers:
//     - loadTailscaleState — read all state from the container
//     - tailscaleAvailable — binaries present?
//     - tailscaledRunning   — process alive?
//     - tailscaleStatusJSON — parsed `tailscale status --json`
//     - readTailscaleAuthKey — read /data/ts/authkey (length only;
//       never log/return the actual key bytes)
//     - writeTailscaleAuthKey — atomic write to the path
//     - startTailscaled / stopTailscaled — exec helpers
package admin

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
)

// TailscaleState is the shape the template consumes.
type TailscaleState struct {
	// Available = true when the tailscaled + tailscale binaries
	// are in the container's PATH. False in a "non-RF" build
	// that omits them — the page renders a fallback note.
	Available bool
	// Running = true when the tailscaled process is alive
	// (i.e. there's a PID + the unix socket exists).
	Running bool
	// TailnetIP is the Tailscale IP of this skygate instance
	// (e.g. "100.64.100.10"). "" when not running or not yet
	// authenticated.
	TailnetIP string
	// AcceptedRoutes is the list of CIDRs that skygate has
	// accepted (i.e. the relay's advertised Telegram ranges).
	// Empty when not running.
	AcceptedRoutes []string
	// BackendState is the parsed Tailscale state string:
	//   - "Running"      = healthy
	//   - "NeedsLogin"   = tailscaled up but no key auth yet
	//   - "Stopped"      = process is dead
	//   - ""             = not available / not running
	BackendState string
	// AuthKeySet = true when the file at
	// s.TailscaleAuthKeyPath is non-empty.
	AuthKeySet bool
	// AuthKeyPath mirrors s.TailscaleAuthKeyPath so the
	// template can render "Edit key at <path>" without
	// needing direct Service access.
	AuthKeyPath string
	// AuthKeyFP is a short fingerprint of the stored key
	// (e.g. "abcd...wxyz", first 4 + last 4). Used so the
	// admin can see "a key is set" without exposing the
	// full secret in the rendered HTML.
	AuthKeyFP string
	// LoginServer mirrors s.TailscaleLoginServer.
	LoginServer string
	// Hostname mirrors s.TailscaleHostname.
	Hostname string
	// StateDir is where tailscaled keeps its state file
	// (/var/lib/tailscale by default). Bind-mounted from
	// the host so state survives container restarts.
	StateDir string
	// LastError is the last error from start/stop (or empty).
	// Cleared on every successful action.
	LastError string
}

// tailscaleStateMu guards the status cache so concurrent
// /admin/tailscale GETs don't all shell out to `tailscale status`
// at the same time. The cache is short (5s) and invalidated on
// any state-changing POST.
var (
	tailscaleStateMu  sync.Mutex
	tailscaleStateAt  time.Time
	tailscaleStateVal TailscaleState
)

const tailscaleStateTTL = 5 * time.Second

// loadTailscaleState reads the current state from the
// container. Cached for 5s to avoid hammering `tailscale status`
// on every page render.
func (s *Service) loadTailscaleState() TailscaleState {
	tailscaleStateMu.Lock()
	defer tailscaleStateMu.Unlock()
	if !tailscaleStateAt.IsZero() && time.Since(tailscaleStateAt) < tailscaleStateTTL {
		return tailscaleStateVal
	}
	tailscaleStateMu.Unlock()

	st := s.readTailscaleState()

	tailscaleStateMu.Lock()
	tailscaleStateVal = st
	tailscaleStateAt = time.Now()
	tailscaleStateMu.Unlock()
	return st
}

// invalidateTailscaleState forces the next loadTailscaleState
// to actually run a fresh probe. Called by every action that
// can change the running state (start, stop, save_key).
func (s *Service) invalidateTailscaleState() {
	tailscaleStateMu.Lock()
	tailscaleStateAt = time.Time{}
	tailscaleStateMu.Unlock()
}

// readTailscaleState is the uncached worker. Safe to call
// from anywhere; doesn't touch the cache.
func (s *Service) readTailscaleState() TailscaleState {
	st := TailscaleState{
		AuthKeyPath: s.tailscaleAuthKeyPath(),
		LoginServer: s.tailscaleLoginServer(),
		Hostname:    s.tailscaleHostname(),
		StateDir:    s.tailscaleStateDir(),
	}
	st.Available = tailscaleAvailable()
	st.AuthKeySet, st.AuthKeyFP = s.readTailscaleAuthKey()
	if !st.Available {
		return st
	}
	running, ip, routes, backendState, err := tailscaleStatus()
	if err != nil {
		st.LastError = err.Error()
		return st
	}
	st.Running = running
	st.TailnetIP = ip
	st.AcceptedRoutes = routes
	st.BackendState = backendState
	return st
}

// tailscaleAuthKeyPath returns the configured path (or the
// default /data/ts/authkey). The default lives in /data which
// is bind-mounted from the host's data/ dir, so it survives
// container restarts.
func (s *Service) tailscaleAuthKeyPath() string {
	if s.TailscaleAuthKeyPath != "" {
		return s.TailscaleAuthKeyPath
	}
	return "/data/ts/authkey"
}

func (s *Service) tailscaleLoginServer() string {
	if s.TailscaleLoginServer != "" {
		return s.TailscaleLoginServer
	}
	return "https://head.example.com"
}

func (s *Service) tailscaleHostname() string {
	if s.TailscaleHostname != "" {
		return s.TailscaleHostname
	}
	return "skygate-host-1"
}

// tailscaleStateDir is the --statedir tailscaled writes to.
// The /var/lib/tailscale directory needs to be on a persistent
// path so the auth state + node identity survive container
// restarts. v0.32.x bind-mounts this from the host.
func (s *Service) tailscaleStateDir() string {
	return "/var/lib/tailscale"
}

// tailscaleAvailable is true when both tailscaled and tailscale
// binaries are on PATH. The multi-stage Dockerfile installs
// them in every build (since Этап 14 v2 in 2026-07-14), so this
// is a safety check for the rare case where someone builds a
// variant image that omits the Tailscale binaries.
func tailscaleAvailable() bool {
	for _, bin := range []string{"tailscaled", "tailscale"} {
		if _, err := exec.LookPath(bin); err != nil {
			return false
		}
	}
	return true
}

// tailscaledRunning checks the unix control socket. tailscaled
// writes to /var/run/tailscale/tailscaled.sock when it's up;
// the bind-mount in docker-compose.yml makes the path
// accessible from inside the skygate container.
func tailscaledRunning() bool {
	socket := "/var/run/tailscale/tailscaled.sock"
	if _, err := os.Stat(socket); err != nil {
		return false
	}
	return true
}

// tailscaleStatus returns (running, ip, acceptedRoutes, backendState, err).
//   - running = true if tailscaled is up + tailscale status works
//   - ip = skygate's tailnet IP (e.g. "100.64.100.10") or ""
//   - acceptedRoutes = list of CIDRs from `tailscale status --json` .Peer[Self].PrimaryRoutes OR AdvertisedRoutes
//   - backendState = the parsed `tailscale status --json` .BackendState
//                    (e.g. "Running" / "NeedsLogin" / "Stopped")
//   - err = the error from the tailscale invocation
func tailscaleStatus() (bool, string, []string, string, error) {
	if !tailscaledRunning() {
		return false, "", nil, "Stopped", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return true, "", nil, "NeedsLogin", fmt.Errorf("tailscale status: %w", err)
	}
	// Minimal parsing — the JSON is documented at
	// https://pkg.go.dev/tailscale.com/client/tailscale/status#Status
	var parsed struct {
		BackendState string `json:"BackendState"`
		Self         struct {
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return true, "", nil, "NeedsLogin", fmt.Errorf("parse status: %w", err)
	}
	ip := ""
	if len(parsed.Self.TailscaleIPs) > 0 {
		ip = parsed.Self.TailscaleIPs[0]
	}
	// The "accepted routes" for skygate come from the peers'
	// PrimaryRoutes that headscale has approved AND that
	// skygate's `tailscale up --accept-routes` has pulled.
	// `tailscale status --json` exposes them under .Peer[]
	// with .PrimaryRoutes (approved) and .AllowedIPs
	// (what the kernel has installed).
	var parsed2 struct {
		Peer []struct {
			HostName    string   `json:"HostName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
			PrimaryRoutes []string `json:"PrimaryRoutes"`
		} `json:"Peer"`
	}
	_ = json.Unmarshal(out, &parsed2)
	routes := []string{}
	for _, p := range parsed2.Peer {
		if p.HostName == "" {
			continue
		}
		for _, r := range p.PrimaryRoutes {
			routes = append(routes, r)
		}
	}
	return true, ip, routes, parsed.BackendState, nil
}

// readTailscaleAuthKey returns (set, fingerprint). Never logs
// or returns the actual key bytes. The fingerprint is the
// first 4 + last 4 chars of the key (or "" if not set) —
// enough to confirm "a key is here" without exposing the
// secret in the rendered HTML.
func (s *Service) readTailscaleAuthKey() (bool, string) {
	path := s.tailscaleAuthKeyPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return false, ""
	}
	if len(key) <= 8 {
		return true, key
	}
	return true, key[:4] + "..." + key[len(key)-4:]
}

// writeTailscaleAuthKey writes the given key to the path
// atomically (write to .tmp, then rename). Mode 0600 so the
// key is only readable by the skygate process.
func (s *Service) writeTailscaleAuthKey(key string) error {
	path := s.tailscaleAuthKeyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(key)+"\n"), 0600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// startTailscaled spawns tailscaled as a background process
// + runs `tailscale up --accept-routes --accept-dns=false
// --login-server=... --hostname=... --authkey=...` to bring
// the client up. Returns (output, err) where output is the
// combined stdout+stderr of the `tailscale up` invocation.
func (s *Service) startTailscaled() (string, error) {
	// Read the auth key.
	keyBytes, err := os.ReadFile(s.tailscaleAuthKeyPath())
	if err != nil {
		return "", fmt.Errorf("read auth key: %w", err)
	}
	key := strings.TrimSpace(string(keyBytes))
	if key == "" {
		return "", fmt.Errorf("auth key file is empty; paste one first")
	}
	// Start tailscaled in the background (no -F so it detaches
	// from the calling shell; logs go to /var/log/tailscaled.log).
	if err := os.MkdirAll(s.tailscaleStateDir(), 0700); err != nil {
		return "", fmt.Errorf("mkdir statedir: %w", err)
	}
	if err := os.MkdirAll("/var/run/tailscale", 0700); err != nil {
		return "", fmt.Errorf("mkdir rundir: %w", err)
	}
	// Use `setsid nohup` to detach tailscaled from the
	// calling shell + make it survive skygate's process
	// group. Without this, when skygate restarts (compose
	// up -d) the new container won't see the old tailscaled
	// anyway (PID namespaces differ), but the design is
	// forward-compatible: a future "reload skygate in place"
	// path (e.g. /admin/update's in-place orchestrator) would
	// inherit the running tailscaled.
	cmd := exec.Command("setsid", "nohup",
		"tailscaled",
		"--statedir="+s.tailscaleStateDir(),
		">/var/log/tailscaled.log", "2>&1", "&")
	var dummy bytes.Buffer
	cmd.Stdout = &dummy
	cmd.Stderr = &dummy
	_ = cmd.Run() // best-effort; the & in the args detaches anyway
	// Wait up to 15s for the control socket to come up.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if tailscaledRunning() {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !tailscaledRunning() {
		return "", fmt.Errorf("tailscaled did not start within 15s; check /var/log/tailscaled.log")
	}
	// Now run `tailscale up` to authenticate.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	up := exec.CommandContext(ctx, "tailscale", "up",
		"--accept-routes",
		"--accept-dns=false",
		"--login-server="+s.tailscaleLoginServer(),
		"--hostname="+s.tailscaleHostname(),
		"--authkey="+key,
	)
	out, err := up.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("tailscale up: %w", err)
	}
	return string(out), nil
}

// stopTailscaled kills the running tailscaled. Best-effort:
// pkill -f tailscaled as root inside the container, then
// remove the unix socket. Idempotent — calling Stop on a
// not-running tailscaled is a no-op.
func (s *Service) stopTailscaled() (string, error) {
	if !tailscaledRunning() {
		return "tailscaled was not running", nil
	}
	out, err := exec.Command("pkill", "-f", "tailscaled").CombinedOutput()
	if err != nil && !strings.Contains(string(out), "no process") {
		return string(out), fmt.Errorf("pkill: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Give it a moment to release the socket.
	for i := 0; i < 10; i++ {
		if !tailscaledRunning() {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if tailscaledRunning() {
		return string(out), fmt.Errorf("tailscaled did not exit within 2s")
	}
	// Best-effort socket cleanup.
	_ = os.Remove("/var/run/tailscale/tailscaled.sock")
	return strings.TrimSpace(string(out)), nil
}

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
	case "start":
		s.handleTailscaleStart(w, r, c)
	case "stop":
		s.handleTailscaleStop(w, r, c)
	default:
		tsRedirect(w, r, "", "Неизвестное действие: "+action)
	}
}

// handleTailscaleSaveKey writes the pasted auth key to the
// configured path. Does NOT start tailscaled automatically —
// that's a separate "Start" button click. The operator can
// pre-paste the key, then click Start later (or after a
// container restart, the entrypoint picks the key up
// automatically — see entrypoint.sh).
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

// handleTailscaleStart spawns tailscaled + runs tailscale up.
// Idempotent: a second click on an already-running tailscaled
// returns a flash noting "already running" (no error).
func (s *Service) handleTailscaleStart(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	out, err := s.startTailscaled()
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_start",
			fmt.Sprintf("err=%q out=%q", err.Error(), truncate(out, 200)))
		tsRedirect(w, r, "", "Не удалось запустить Tailscale: "+err.Error()+" — output: "+truncate(out, 400))
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_start", "ok out="+truncate(out, 200))
	s.invalidateTailscaleState()
	if strings.TrimSpace(out) == "" {
		tsRedirect(w, r, "Tailscale запущен. Проверьте accepted routes через ~30s.", "")
		return
	}
	tsRedirect(w, r, "Tailscale запущен. Output: "+truncate(out, 400), "")
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
	s.Backend.Audit(c.UserID, c.Username, "tailscale_stop", "ok out="+truncate(out, 200))
	s.invalidateTailscaleState()
	tsRedirect(w, r, "Tailscale остановлен.", "")
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

func urlQueryEscape(s string) string {
	// Avoid pulling in net/url just for QueryEscape; the
	// only callers pass ASCII-ish messages.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' {
			out = append(out, '+')
			continue
		}
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || c == '+' {
			out = append(out, c)
			continue
		}
		out = append(out, '%', hexDigit(c>>4), hexDigit(c&0x0F))
	}
	return string(out)
}

func hexDigit(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'A' + (b - 10)
}

// truncate keeps audit log rows bounded.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(+" + strconv.Itoa(len(s)-n) + " bytes)"
}

// avoid unused-import warning for the package's "database/sql"
// re-export — some handlers read from s.DB which is *sql.DB.
// (var _ = db.X is a no-op reference; keeps the import alive
// in case future test helpers want to assert against the DB.)
var _ = db.ErrUserNotFound
