// internal/headscale/local_apply_b293.go — B293 (2026-09-23).
//
// Applying route changes to a relay that IS this host, with a PRIVILEGE LADDER.
//
// The live shape (operator's `aro`): headscale + skygate + the exit node in one
// place, the local tailscaled being `exit-node-vps` (100.64.0.1). Routes must be
// applied by `tailscale set …` against that daemon — locally, not over SSH.
//
// `tailscale set` needs root (or the daemon's `--operator` user), and the
// operator asked explicitly that the feature must work "и без root, чтобы не
// было ситуации что нет возможности настроить по причине доступа". So the
// transport is a ladder, and every rung is NAMED in the result/log:
//
//  1. direct  — `tailscale set …` as this process (root, or a daemon configured
//     with `tailscale set --operator=<skygate user>`);
//  2. sudo    — `sudo -n tailscale set …` (a passwordless sudoers rule);
//  3. helper  — a data-only request file consumed by the root-owned
//     `skygate-apply-routes.sh` (deploy/skygate-routes.path), the same
//     privilege split the headscale policy write uses (B272.3);
//  4. nothing — a NAMED error listing all three fixes, never a bare
//     "permission denied".
//
// A real `tailscale set` failure (bad CIDR, daemon down) is NOT masked by trying
// the next rung: only a privilege refusal falls through.
package headscale

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrLocalRoutesUnavailable means none of the three transports can apply a local
// route change on this host.
var ErrLocalRoutesUnavailable = errors.New("no local way to apply tailscale routes (need root, sudo, or the privileged routes helper)")

// LocalTransport names one rung of the ladder.
type LocalTransport struct {
	// Name is "direct" | "sudo" | "helper".
	Name string
	// Detail explains the rung in the operator's terms (used by the page and by
	// the sync result when the rung fails).
	Detail string
}

// RoutesRequestPath returns the request file the root-owned routes applier
// watches. It sits next to the policy request in the update dir — both are
// "privileged action requests" and share the ownership model.
func RoutesRequestPath() string {
	if p := strings.TrimSpace(os.Getenv("SKYGATE_ROUTES_REQUEST_PATH")); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(PolicyRequestPath()), "routes.request.props")
}

// RoutesApplyStatusPath returns the file the applier records its last outcome in,
// so the sync can report what actually happened instead of "queued".
func RoutesApplyStatusPath() string {
	if p := strings.TrimSpace(os.Getenv("SKYGATE_ROUTES_STATUS_PATH")); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(RoutesRequestPath()), "routes-apply.status")
}

// RoutesApplyRequest is the data-only body skygate hands to the root applier.
type RoutesApplyRequest struct {
	Routes       []string `json:"routes"`
	AcceptRoutes int      `json:"accept_routes"`
	RequestedAt  string   `json:"requested_at"`
	RequestedBy  string   `json:"requested_by"`
}

// RoutesApplyStatus is the applier's last recorded outcome.
type RoutesApplyStatus struct {
	Result  string // "ok" | "failed"
	TS      string
	Routes  string
	Command string
	Reason  string
}

// RequestRoutesApply stages a route change for the privileged applier.
//
// The handoff is temp + rename (never an in-place rewrite of a file another
// process reads), exactly like RequestPolicyApply after B283.
func RequestRoutesApply(routes []string, acceptRoutes int) error {
	path := RoutesRequestPath()
	if !filepath.IsAbs(path) {
		return fmt.Errorf("routes request path must be absolute (got %q)", path)
	}
	if len(routes) == 0 {
		return errors.New("refusing to stage an empty route list (it would strip the exit-node base routes)")
	}
	for _, r := range routes {
		if !isPlainRoute(r) {
			return fmt.Errorf("refusing to stage a route that is not a bare CIDR: %q", r)
		}
	}
	if acceptRoutes < -1 || acceptRoutes > 1 {
		return fmt.Errorf("accept_routes must be -1, 0 or 1 (got %d)", acceptRoutes)
	}
	// LINE-BASED key=value data (never sourced, never eval'd) — the same shape the
	// policy applier consumes, so a value can never become code.
	body := strings.Join([]string{
		"ROUTES=" + strings.Join(routes, ","),
		fmt.Sprintf("ACCEPT_ROUTES=%d", acceptRoutes),
		"REQUESTED_AT=" + time.Now().UTC().Format(time.RFC3339),
		"REQUESTED_BY=skygate",
		"",
	}, "\n")
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "routes.request.props.tmp-*")
	if err != nil {
		return fmt.Errorf("create temp request in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp request: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp request: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod temp request: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("hand over request to %s: %w", path, err)
	}
	return nil
}

// isPlainRoute reports whether s is a bare CIDR ("10.0.0.0/8", "fd7a::/64") —
// the only shape the privileged applier accepts, so a request can never carry a
// shell fragment or an extra flag.
func isPlainRoute(s string) bool {
	s = strings.TrimSpace(s)
	ip, _, err := net.ParseCIDR(s)
	return err == nil && ip != nil && !strings.ContainsAny(s, " \t\"';$`|&")
}

// ReadRoutesApplyStatus reads the applier's last outcome (key=value; keys are
// matched case-insensitively so the shell side can use its own spelling).
func ReadRoutesApplyStatus() (RoutesApplyStatus, bool) {
	raw, err := os.ReadFile(RoutesApplyStatusPath())
	if err != nil {
		return RoutesApplyStatus{}, false
	}
	st := RoutesApplyStatus{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "result", "status":
			st.Result = strings.TrimSpace(v)
		case "ts", "at":
			st.TS = strings.TrimSpace(v)
		case "routes":
			st.Routes = strings.TrimSpace(v)
		case "command":
			st.Command = strings.TrimSpace(v)
		case "reason":
			st.Reason = strings.TrimSpace(v)
		}
	}
	if st.Result == "" {
		return RoutesApplyStatus{}, false
	}
	return st, true
}

// LocalTransports reports the rungs that look available on this host, without
// changing anything. Used by /admin/exit-nodes so the operator sees HOW the next
// sync will apply routes (and what to fix for the rungs that are missing).
func LocalTransports() []LocalTransport {
	var out []LocalTransport
	bin := TailscaleCLI()
	if _, err := localLookPath(bin); err == nil {
		if os.Geteuid() == 0 {
			out = append(out, LocalTransport{Name: "direct", Detail: "this process is root"})
		} else {
			out = append(out, LocalTransport{Name: "direct", Detail: "as the skygate service user (needs the daemon's --operator=<user>)"})
		}
	} else {
		out = append(out, LocalTransport{Name: "direct", Detail: fmt.Sprintf("unavailable: %q not in PATH (set SKYGATE_TAILSCALE_CLI)", bin)})
	}
	if _, err := localLookPath("sudo"); err == nil {
		out = append(out, LocalTransport{Name: "sudo", Detail: "sudo -n " + bin + " set … (needs a NOPASSWD rule)"})
	} else {
		out = append(out, LocalTransport{Name: "sudo", Detail: "unavailable: sudo not installed"})
	}
	out = append(out, LocalTransport{Name: "helper", Detail: "root-owned applier via " + RoutesRequestPath() +
		" (install: sudo bash deploy/install-routes-helper.sh)"})
	return out
}

// RoutesFallbackHint is the operator-facing half of the ladder failure.
//
// B301: option (c) carries a warning because the systemd unit the installers
// write sets `NoNewPrivileges=yes` — on a native/systemd host sudo is refused by
// the kernel flag before any sudoers rule is consulted, so a NOPASSWD rule there
// changes nothing and the privileged helper (d) is the only option that works
// without touching the unit or the daemon's `--operator` grant.
func RoutesFallbackHint() string {
	bin := TailscaleCLI()
	return fmt.Sprintf("apply the routes locally with one of: (a) run skygate as root, "+
		"(b) allow the service user on the daemon: `sudo %s set --operator=<skygate user>`, "+
		"(c) add a NOPASSWD sudoers rule for `%s set` — on a systemd install this CANNOT work, "+
		"the unit ships NoNewPrivileges=yes and sudo is refused by the kernel flag first, or "+
		"(d) install the privileged helper: `sudo bash deploy/install-routes-helper.sh`",
		bin, bin)
}

// ApplyRoutesLocally applies `--advertise-routes` (+ optional --accept-routes) to
// THIS host's tailscaled, walking the ladder. It returns the rung that worked and
// that command's output.
func (c *Client) ApplyRoutesLocally(routes []string, acceptRoutes int) (LocalTransport, string, error) {
	if len(routes) == 0 {
		return LocalTransport{}, "", errors.New("refusing an empty route list (it would strip the exit-node base routes)")
	}
	args := BuildTailscaleSetArgs(routes, acceptRoutes)
	bin := TailscaleCLI()

	// Rung 1 — direct.
	if _, err := localLookPath(bin); err != nil {
		// No CLI at all: skip straight to the helper (it may have a different
		// path) but keep the reason for the final error.
		out, herr := applyViaHelper(routes, acceptRoutes)
		if herr == nil {
			return LocalTransport{Name: "helper"}, out, nil
		}
		return LocalTransport{}, out, fmt.Errorf("local route apply: %q not in PATH (%v); helper: %v — %s",
			bin, err, herr, RoutesFallbackHint())
	}
	out, err := localRunner(bin, args...)
	if err == nil {
		return LocalTransport{Name: "direct"}, out, nil
	}
	if !isPrivilegeRefusal(err, out) {
		// A real failure (bad CIDR, daemon down, unknown flag): do not mask it by
		// trying another rung — the same command would fail there too.
		return LocalTransport{}, out, fmt.Errorf("tailscale set (local, direct): %s: %w", collapseOutput(out, err), err)
	}
	directOut := collapseOutput(out, err)

	// Rung 2 — passwordless sudo.
	if _, lookErr := localLookPath("sudo"); lookErr == nil {
		sudoArgs := append([]string{"-n", bin}, args...)
		if sout, serr := localRunner("sudo", sudoArgs...); serr == nil {
			return LocalTransport{Name: "sudo"}, sout, nil
		} else if !isPrivilegeRefusal(serr, sout) {
			return LocalTransport{}, sout, fmt.Errorf("tailscale set (local, sudo): %s: %w", collapseOutput(sout, serr), serr)
		}
	}

	// Rung 3 — the privileged applier.
	if out, herr := applyViaHelper(routes, acceptRoutes); herr == nil {
		return LocalTransport{Name: "helper"}, out, nil
	} else {
		return LocalTransport{}, out, fmt.Errorf("local route apply refused: %s; sudo route unavailable; privileged helper: %v — %s",
			directOut, herr, RoutesFallbackHint())
	}
}

// routesApplierPath is where the installers put the root-owned applier. Its
// presence (or an active skygate-routes.path unit) is how skygate tells "the
// helper is installed, the verdict is just not written yet" from "nobody will
// ever consume this request".
const routesApplierPath = "/usr/local/lib/skygate/skygate-apply-routes.sh"

// routesHelperArmedFn is routesHelperArmed, injectable so the ladder tests are
// deterministic on hosts without systemd (a CI runner HAS systemctl but no
// skygate-routes.path, which would otherwise short-circuit the helper rung).
var routesHelperArmedFn = routesHelperArmed

// routesHelperArmed reports whether the privileged helper is installed.
// known=false means "this host cannot answer" (no systemd, e.g. a bare install or
// a developer machine) — the caller then probes once instead of trusting a wait.
func routesHelperArmed() (armed, known bool) {
	if _, err := os.Stat(routesApplierPath); err == nil {
		return true, true
	}
	if _, err := os.Stat("/etc/systemd/system/skygate-routes.path"); err == nil {
		return true, true
	}
	if _, err := localLookPath("systemctl"); err != nil {
		return false, false
	}
	out, err := localRunner("systemctl", "is-active", "skygate-routes.path")
	state := strings.TrimSpace(out)
	if err == nil && state == "active" {
		return true, true
	}
	// systemctl answered (inactive/failed/unknown) — positively not armed.
	return false, true
}

// applyViaHelper stages the request and waits briefly for the applier's verdict.
//
// The wait is bounded (the path unit fires within a second on a healthy host, and
// a real apply is a single fast command). A staged request is NOT success: when no
// verdict appears and the helper cannot be confirmed installed, the caller must
// see an actionable error instead of "queued" (the B288.1 lesson — an accepted
// handoff is not a completed action).
func applyViaHelper(routes []string, acceptRoutes int) (string, error) {
	armed, known := routesHelperArmedFn()
	if known && !armed {
		return "", fmt.Errorf("privileged routes helper is not installed (%s, and skygate-routes.path is not active)", RoutesRequestPath())
	}
	before, _ := ReadRoutesApplyStatus()
	if err := RequestRoutesApply(routes, acceptRoutes); err != nil {
		return "", err
	}
	wait := 8 * time.Second
	if !known {
		// Nobody confirmed there is an applier: do not make the operator wait for
		// a verdict that cannot come.
		wait = 2 * time.Second
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if st, ok := ReadRoutesApplyStatus(); ok {
			fresh := before.TS == "" || st.TS != before.TS
			if fresh {
				if st.Result == "ok" {
					return "helper applied " + st.Command, nil
				}
				if st.Result == "failed" {
					return "", fmt.Errorf("privileged routes helper FAILED: %s", st.Reason)
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if armed {
		return "queued for the privileged helper (no verdict yet — see " + RoutesApplyStatusPath() + ")", nil
	}
	return "", fmt.Errorf("staged %s but nothing consumed it and the helper could not be confirmed installed — %s",
		RoutesRequestPath(), RoutesFallbackHint())
}

// localRunner executes one rung's command and returns its combined output.
// Package-level so the ladder is unit-testable on any platform (no tailscale, no
// sudo, no POSIX shell required).
var localRunner = defaultLocalRunner

// localLookPath is exec.LookPath, injectable for the same reason.
var localLookPath = exec.LookPath

// defaultLocalRunner runs one rung's command.
func defaultLocalRunner(bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// isPrivilegeRefusal reports whether a failure means "you are not allowed",
// which is the only class the ladder falls through. Everything else is the real
// answer from tailscale and must be surfaced as-is.
func isPrivilegeRefusal(err error, output string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(output + " " + err.Error())
	for _, needle := range []string{
		"permission denied",
		"access denied",
		"must be run as root",
		"operation not permitted",
		"a password is required", // sudo -n without a NOPASSWD rule
		"is not in the sudoers",  // sudo refusal
		// B301 (2026-09-23): the systemd unit the installers write ships
		// `NoNewPrivileges=yes` (deploy/install-common.sh), so `sudo` can NEVER
		// become root from inside skygate — the kernel flag cannot be changed by
		// the process that is subject to it. Live on `aro` the ladder stopped
		// exactly here and never reached the root-owned helper:
		//
		//	sudo: The "no new privileges" flag is set, which prevents sudo from
		//	running as root. sudo: If sudo is running in a container, you may need
		//	to adjust the container configuration to disable the flag.
		//
		// The refusal was read as a REAL `tailscale set` failure, so
		// `ApplyRoutesLocally` returned one rung early: the routes never applied,
		// `routes-apply.status` was never created, and every prefix stayed «нет
		// маршрута» while the helper sat installed and idle. This is the strongest
		// possible "you are not allowed" — fall through to the helper.
		"no new privileges",
		"prevents sudo from running as root",
		"not allowed to execute",    // sudoers denial for this exact command
		"no tty present",            // requiretty + `sudo -n`
		"sorry, user",               // sudoers denial
		"no such file or directory", // a socket the service user cannot open
		"connect: permission denied",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// collapseOutput flattens a command's output for a one-line error/result string.
func collapseOutput(out string, err error) string {
	s := strings.Join(strings.Fields(out), " ")
	if s == "" && err != nil {
		s = err.Error()
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
