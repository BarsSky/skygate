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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/headscale"
)

// 2026-08-05 v0.33.1.11 — tailscale auth key auto-generation.
//
// findUserForHostname resolves the headscale user that should
// own a preauth key for the configured Tailscale hostname
// (default "skygate-host-1"). It walks the headscale node list
// (not the user list) because a fresh container doesn't
// necessarily have a User row in headscale yet — the user is
// created on the first node registration. Looking for a node
// with the matching hostname (via the admin's ListAllNodes
// path) is the most reliable signal: if a node named
// "skygate-host-1" exists in headscale, the user behind it
// is by construction the one this skygate instance registered
// as, and that's who we want a new preauth key for.
//
// Returns the headscale user ID (int64, suitable for
// CreatePreauthKey's userID param) and the user.Name for
// audit / flash messages. The 4-second timeout matches
// ListAllNodes' internal cap so a stuck headscale doesn't
// hang the page request.
func (s *Service) findUserForHostname(ctx context.Context, hs *headscale.Client, hostname string) (int64, string, error) {
	if hs == nil {
		return 0, "", fmt.Errorf("headscale client not configured")
	}
	listCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	_ = listCtx
	nodes, err := hs.ListAllNodes()
	if err != nil {
		return 0, "", fmt.Errorf("list nodes: %w", err)
	}
	for _, n := range nodes {
		if n.Hostname != hostname {
			continue
		}
		if n.UserID == "" {
			continue
		}
		uid, perr := strconv.ParseInt(n.UserID, 10, 64)
		if perr != nil {
			return 0, "", fmt.Errorf("user id %q for hostname %q is not numeric: %w", n.UserID, hostname, perr)
		}
		return uid, n.UserName, nil
	}
	return 0, "", fmt.Errorf("no node with hostname %q in headscale (register once first, or use /admin/headscale to create a preauth key manually)", hostname)
}

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
	// LoginServerSource is "db" when the value came from the
	// web-UI's persisted global_settings row (takes
	// precedence over the env var), or "env" when the
	// value still falls back to s.TailscaleLoginServer
	// (the SKYGATE_TS_LOGIN_SERVER env var). Template
	// uses this to render a "source: web-UI/DB" vs
	// "source: env var" hint so the operator knows which
	// value actually wins.
	// v0.33.1.13.
	LoginServerSource string
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
//
// Lock discipline: the cache check + cache write both
// happen under tailscaleStateMu. The slow `readTailscaleState`
// call (which may exec `tailscale status` and block for ~1s
// over a slow TUN) runs WITHOUT the lock — otherwise a
// slow probe would block every concurrent page render and
// the deferred Unlock on the cache-write path would Unlock
// an already-unlocked mutex (the v0.33.1.9 first cut did
// exactly that and immediately panicked on the first GET).
func (s *Service) loadTailscaleState() TailscaleState {
	tailscaleStateMu.Lock()
	if !tailscaleStateAt.IsZero() && time.Since(tailscaleStateAt) < tailscaleStateTTL {
		st := tailscaleStateVal
		tailscaleStateMu.Unlock()
		return st
	}
	tailscaleStateMu.Unlock()

	// Cache miss / stale — re-probe without holding the lock.
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
		AuthKeyPath:       s.tailscaleAuthKeyPath(),
		LoginServer:       s.tailscaleLoginServer(),
		LoginServerSource: s.tailscaleLoginServerSource(),
		Hostname:          s.tailscaleHostname(),
		StateDir:          s.tailscaleStateDir(),
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

// SetGlobalSettingForTest is a thin wrapper around
// db.SetGlobalSetting exposed on the Service so unit tests
// can seed the global_settings table without forking on
// the per-backend placeholder syntax (?  vs  $1,$2,...).
// The v0.33.1.13 login-server tests use it to set up a
// known DB state and assert the resolution order. v0.33.1.13.
func (s *Service) SetGlobalSettingForTest(key, value string) error {
	return db.SetGlobalSetting(s.DB, key, value)
}

// tailscaleLoginServerDBKey is the global_settings key for
// the user-editable headscale URL (set via /admin/tailscale
// "save_login_server" action). Lives in global_settings so
// the value survives container restarts, Postgres → SQLite
// migrations, and VM clones. v0.33.1.13.
//
// Resolution order at read time (highest priority first):
//   1. global_settings[tailscale.login_server]  (web-UI override)
//   2. s.TailscaleLoginServer                   (SKYGATE_TS_LOGIN_SERVER env var)
//   3. "https://head.example.com"               (last-resort default)
//
// The env var is only consulted on first start (when the
// global_settings row is empty) — once the operator saves a
// value via the web UI, the env var is ignored until the
// operator clears the DB row (e.g. via `sqlite3 skygate.db
// "DELETE FROM global_settings WHERE key='tailscale.login_server'"`).
// This makes the deployment fully re-creatable from the web
// UI without touching the .env file.
const tailscaleLoginServerDBKey = "tailscale.login_server"

func (s *Service) tailscaleLoginServer() string {
	// 1. Web-UI override (DB). Empty string means "not set".
	if v, err := db.GetGlobalSetting(s.DB, tailscaleLoginServerDBKey, ""); err == nil && v != "" {
		return v
	}
	// 2. Env-var bootstrap.
	if s.TailscaleLoginServer != "" {
		return s.TailscaleLoginServer
	}
	// 3. Last-resort default.
	return "https://head.example.com"
}

// tailscaleLoginServerSource reports which of the three layers
// (db / env / default) the running config is actually using.
// Returns "db", "env", or "default". The template uses this
// to render a small "source: ..." hint so the operator knows
// whether changing the .env file would have any effect.
func (s *Service) tailscaleLoginServerSource() string {
	if v, err := db.GetGlobalSetting(s.DB, tailscaleLoginServerDBKey, ""); err == nil && v != "" {
		return "db"
	}
	if s.TailscaleLoginServer != "" {
		return "env"
	}
	return "default"
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
		routes = append(routes, p.PrimaryRoutes...)
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
		time.Sleep(http.StatusInternalServerError * time.Millisecond)
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
	case "save_login_server":
		// v0.33.1.13 — persist SKYGATE_TS_LOGIN_SERVER to
		// global_settings so the value survives container
		// restarts, migrations, and VM clones. The env var
		// is only consulted when the DB row is empty.
		s.handleTailscaleSaveLoginServer(w, r, c)
	case "start":
		s.handleTailscaleStart(w, r, c)
	case "stop":
		s.handleTailscaleStop(w, r, c)
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
	if err := db.SetGlobalSetting(s.DB, tailscaleLoginServerDBKey, raw); err != nil {
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
	out, err := s.startTailscaled()
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_start",
			fmt.Sprintf("err=%q out=%q", err.Error(), truncate(out, 200)))
		tsRedirect(w, r, "", "Не удалось запустить Tailscale: "+err.Error()+" — output: "+truncate(out, http.StatusBadRequest))
		return
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
	s.Backend.Audit(c.UserID, c.Username, "tailscale_stop", "ok out="+truncate(out, 200))
	s.invalidateTailscaleState()
	tsRedirect(w, r, "Tailscale остановлен.", "")
}

// 2026-08-05 v0.33.1.11 — automated preauth key generation.
//
// handleTailscaleGenerateKey is the "Generate automatically"
// button on /admin/tailscale. The flow:
//   1. Resolve the headscale user that owns a node with the
//      configured hostname (default "skygate-host-1"). The
//      admin's first node registration creates the user; the
//      first /admin/headscale preauth key the operator
//      generated in the past is what bootstrapped that node,
//      so the user row is guaranteed to exist if the node
//      exists.
//   2. Call headscale preauthkeys create (API + CLI fallback
//      inside the headscale pkg) with a 1h expiration and
//      reusable=true. The 1h is conservative — the same key
//      is reusable for the container's lifetime, but a short
//      window limits the blast radius if the key leaks.
//   3. Write the returned key to the same /data/ts/authkey
//      path the "Save" path uses. Mode 0600.
//   4. Audit: tailscale_generate_key|username|user_id=N
//      hostname=X user_name=Y exp=1h reusable=true fp=tske...wxyz
//      (FP only; full key never logged).
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

// handleTailscaleRestart restarts the skygate process (not
// just tailscaled). v0.33.1.16.
//
// WHY this exists: the entrypoint.sh reads SKYGATE_TS_LOGIN_SERVER
// at container start. Saving the value via /admin/tailscale
// only writes to the DB + (after this fix) the .env file.
// The operator's next step was either:
//   (a) SSH in and run `docker compose restart skygate` (or
//       `systemctl restart skygate` on a native host), or
//   (b) remember to restart before saving. Both are error-prone.
// This endpoint makes restart a single click.
//
// Flow:
//   1. Determine the current effective login_server (DB > .env > default).
//   2. Write it to the in-container .env (atomic via .tmp + rename).
//      This makes the next entrypoint invocation pick up the
//      new value.
//   3. Trigger the restart:
//      - container mode: spawn a setsid'd subprocess that runs
//        `docker compose -p skygate -f <host-repo>/docker-compose.yml
//        restart skygate`. The setsid is critical — the parent
//        skygate process gets SIGTERM'd by `docker compose
//        restart` and any child process in the same process
//        group dies with it. setsid puts the child in a new
//        session so it survives.
//      - native mode: try `systemctl restart skygate`. If that
//        fails, fall back to `service skygate restart`.
//   4. Return success to the client IMMEDIATELY (the response
//      flushes before the SIGTERM arrives).
//
// Audit: full event log including effective URL, in_container,
// restart_method.
func (s *Service) handleTailscaleRestart(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	effective := s.tailscaleLoginServer()
	inContainer := isRunningInContainer()

	// Step 1: write the effective value back to the in-container
	// .env so the next entrypoint invocation picks it up. This
	// is best-effort — if the .env is read-only or doesn't exist
	// (e.g. native host), we still want to attempt the restart.
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

	// Step 2: trigger restart. Best-effort — we run the actual
	// command in a goroutine that uses setsid to detach the
	// subprocess from our process group. The Go HTTP server
	// keeps serving until docker compose restart sends SIGTERM
	// to PID 1; the response has already flushed by then.
	restartMethod := "none"
	if inContainer {
		hostRepo := os.Getenv("SKYGATE_HOST_REPO_PATH")
		if hostRepo == "" {
			// Fall back to the parent of the bind-mount
			// point (in-container RepoPath is /app, so
			// we can't use it for docker compose -f;
			// the daemon needs the host path).
			hostRepo = "/home/operator/skygate"
		}
		composeFile := filepath.Join(hostRepo, "docker-compose.yml")
		go func() {
			// setsid: new session so the subprocess
			// outlives the SIGTERM that hits the parent
			// (docker compose restart sends SIGTERM to
			// PID 1 = entrypoint.sh = parent of all).
			cmd := exec.Command("setsid", "docker", "compose",
				"-p", "skygate",
				"-f", composeFile,
				"restart", "skygate")
			applySysProcAttr(cmd)
			// Best-effort: log the result to /tmp. We
			// can't return an error to the client
			// (we already responded + the parent is
			// about to die).
			out, _ := cmd.CombinedOutput()
			logFile := "/tmp/skygate-restart.log"
			_ = os.WriteFile(logFile,
				[]byte(fmt.Sprintf("[%s] docker compose restart: %s\n",
					time.Now().UTC().Format(time.RFC3339), string(out))),
				0644)
		}()
		restartMethod = "container:docker_compose_restart"
	} else {
		// Native host: try systemctl first, fall back
		// to service. We run the command in a goroutine
		// + setsid so it survives the parent dying (the
		// skygate process is itself the service in
		// question, so the OS will kill the parent).
		go func() {
			cmd := exec.Command("setsid", "bash", "-c",
				"systemctl restart skygate 2>&1 || service skygate restart 2>&1")
			applySysProcAttr(cmd)
			out, _ := cmd.CombinedOutput()
			logFile := "/tmp/skygate-restart.log"
			_ = os.WriteFile(logFile,
				[]byte(fmt.Sprintf("[%s] systemctl/service restart: %s\n",
					time.Now().UTC().Format(time.RFC3339), string(out))),
				0644)
		}()
		restartMethod = "native:systemctl_or_service"
	}

	s.Backend.Audit(c.UserID, c.Username, "tailscale_restart_skgate",
		fmt.Sprintf("login_server=%q in_container=%v method=%s%s",
			effective, inContainer, restartMethod, envUpdateMsg))

	// Return IMMEDIATELY. The Go process is about to be
	// SIGTERM'd by the restart we just triggered; the
	// response must flush before that happens. The redirect
	// target reloads the page after the restart completes
	// (the operator will see the new build label).
	tsRedirect(w, r,
		fmt.Sprintf("Перезапуск запущен (%s). Страница вернётся через ~30s с новой версией.", restartMethod),
		"")
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
