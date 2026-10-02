// tailscale_runtime.go — how skygate asks the host what Tailscale is
// doing, and the three exec helpers that change it.
//
// Split out of tailscale.go in refactor Phase D (2026-10-01).  These
// are the only functions in the package that touch the process table,
// the unix control socket, `/data/ts/authkey` or `tailscale up`; every
// handler above them is a thin wrapper that renders their verdict.
//
//   - tailscaleAvailable / tailscaledRunning (+ the stubbable
//     tailscaledRunningFn) / tailscaleDaemonAnswers (B321)
//   - tailLogTail — the last N lines of the daemon log
//   - tailscaleStatus — parsed `tailscale status --json`
//   - tailscaleAdvertisedRoutes (B236)
//   - readTailscaleAuthKey / writeTailscaleAuthKey (length only, never
//     the key bytes)
//   - startTailscaled / tailscaleUp / stopTailscaled

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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
func tailscaledRunning() bool { return tailscaledRunningFn() }

// tailscaledRunningFn is the indirection used by unit tests
// to stub the "is tailscaled up?" check without touching the
// host's actual unix socket. Production code calls
// tailscaledRunning; tests override this var.
// tailscaledRunning reports whether the daemon is actually ANSWERING on its
// unix socket. Pre-2026-09-19 this only stat()ed the socket file, so a stale
// file left behind by a previous container (the run dir is a bind mount:
// data/ts/run → /var/run/tailscale) made the page claim "running" and made the
// Start flow skip its wait and run `tailscale up` against nothing:
//
//	tailscale up: exit status 1 — output: failed to connect to local tailscaled;
//	it doesn't appear to be running
//
// (operator report 2026-09-19). Dialling the socket is the honest check.
var tailscaledRunningFn = tailscaleDaemonAnswers

// tailscaleDaemonAnswers dials the control socket. A live daemon accepts the
// connection; a stale socket file fails with ECONNREFUSED/ENOENT.
func tailscaleDaemonAnswers() bool {
	c, err := net.DialTimeout("unix", tailscaledSocketPath, 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// tailscaledSocketPath is the control socket tailscaled writes.
const tailscaledSocketPath = "/var/run/tailscale/tailscaled.sock"

// tailLogTail returns the last n lines of the tailscaled log (best-effort) so a
// failed start reports WHY instead of a bare timeout.
func tailLogTail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(unreadable: " + err.Error() + ")"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// tailscaleStatus returns (running, ip, acceptedRoutes, backendState, err).
//   - running = true if tailscaled is up + tailscale status works
//   - ip = skygate's tailnet IP (e.g. "100.64.100.10") or ""
//   - acceptedRoutes = list of CIDRs from `tailscale status --json` .Peer[Self].PrimaryRoutes OR AdvertisedRoutes
//   - backendState = the parsed `tailscale status --json` .BackendState
//     (e.g. "Running" / "NeedsLogin" / "Stopped")
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
			HostName      string   `json:"HostName"`
			TailscaleIPs  []string `json:"TailscaleIPs"`
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

// tailscaleAdvertisedRoutes reads the currently-advertised
// subnet routes from `tailscale status --json`.
//
// Returns 3 values:
//   - requested:  the Prefs.AdvertiseRoutes list (what the
//     operator asked for via `tailscale set --advertise-routes=`).
//     This is the source of truth for the UI.
//   - approved:   the Self.PrimaryRoutes list (what headscale
//     has actually approved and pushed to the tailnet). May
//     be a subset of requested when the headscale policy
//     rejects some routes.
//   - source:     "prefs" (the request) or "self" (only the
//     approved list was non-empty). "prefs" is preferred so
//     the UI shows the operator's intent even when nothing
//     has been approved yet.
//
// Returns (nil, nil, "") when tailscaled isn't running.
// B236 — surfaces the advertised routes on /admin/tailscale
// so the operator can manage them without SSH'ing into
// skygate-host-1.
func tailscaleAdvertisedRoutes() (requested, approved []string, source string) {
	if !tailscaledRunning() {
		return nil, nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return nil, nil, ""
	}
	var parsed struct {
		Self struct {
			PrimaryRoutes []string `json:"PrimaryRoutes"`
		} `json:"Self"`
		Prefs struct {
			AdvertiseRoutes []string `json:"AdvertiseRoutes"`
		} `json:"Prefs"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, nil, ""
	}
	// Prefs.AdvertiseRoutes is the operator's intent
	// (the value passed to `tailscale set
	// --advertise-routes=`). When it's non-empty
	// (regardless of approval status), show it.
	if len(parsed.Prefs.AdvertiseRoutes) > 0 {
		return parsed.Prefs.AdvertiseRoutes, parsed.Self.PrimaryRoutes, "prefs"
	}
	// Fall back to Self.PrimaryRoutes for the case
	// where Prefs is empty (older Tailscale versions or
	// pre-B236 setups that never went through `tailscale
	// set`).
	if len(parsed.Self.PrimaryRoutes) > 0 {
		return parsed.Self.PrimaryRoutes, parsed.Self.PrimaryRoutes, "self"
	}
	return []string{}, []string{}, "prefs"
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
	// Start tailscaled in the background. Two pre-2026-09-19 bugs fixed here:
	//   (1) the command was spawned as
	//         setsid nohup tailscaled --statedir=… ">/var/log/tailscaled.log" "2>&1" "&"
	//       — with no shell involved those redirection tokens were passed to
	//       tailscaled as ARGV, so it exited immediately with an argument error
	//       that nobody read (the output buffer was discarded);
	//   (2) the readiness loop trusted a SOCKET FILE, so a stale socket from a
	//       previous container satisfied it instantly and `tailscale up` then
	//       failed with "failed to connect to local tailscaled".
	// Now: a stale socket is removed, tailscaled is started with a real log file
	// and detached into its own session (detachProcess), and the wait polls for
	// a daemon that ANSWERS.
	if err := os.MkdirAll(s.tailscaleStateDir(), 0700); err != nil {
		return "", fmt.Errorf("mkdir statedir: %w", err)
	}
	if err := os.MkdirAll("/var/run/tailscale", 0700); err != nil {
		return "", fmt.Errorf("mkdir rundir: %w", err)
	}
	if !tailscaleDaemonAnswers() {
		_ = os.Remove(tailscaledSocketPath) // stale file from a previous container
	}
	if tailscaleDaemonAnswers() {
		// Already up (started by the entrypoint, or by a previous click) — just
		// authenticate; spawning a second daemon would only log "address in use".
		return s.tailscaleUp(key)
	}
	const tsLog = "/var/log/tailscaled.log"
	logFile, err := os.OpenFile(tsLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", tsLog, err)
	}
	defer logFile.Close()
	tsCmd := exec.Command("tailscaled", "--statedir="+s.tailscaleStateDir())
	tsCmd.Stdout = logFile
	tsCmd.Stderr = logFile
	detachProcess(tsCmd)
	if err := tsCmd.Start(); err != nil {
		return "", fmt.Errorf("start tailscaled: %w", err)
	}
	// Wait up to 15s for a daemon that actually answers on the socket.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if tailscaleDaemonAnswers() {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !tailscaleDaemonAnswers() {
		return "", fmt.Errorf("tailscaled did not become ready within 15s (last lines of %s: %s)",
			tsLog, tailLogTail(tsLog, 5))
	}
	_ = tsCmd // tailscaled keeps running after this handler returns
	// Now run `tailscale up` to authenticate.
	return s.tailscaleUp(key)
}

// tailscaleUp authenticates the (already running) daemon with the key from the
// auth-key file. Split out of startTailscaled so the "daemon already answers"
// path can reuse it without spawning a second tailscaled.
func (s *Service) tailscaleUp(key string) (string, error) {
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
