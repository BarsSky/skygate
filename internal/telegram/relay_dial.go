// internal/telegram/relay_dial.go — B356.1 (2026-10-07).
//
// THE RELAY INVENTORY AND THE TUNNEL SOCKET.
//
// This file answers two questions for the Telegram egress fallback:
//
//  1. WHICH relays may carry the Bot API traffic, in which order? The answer reuses
//     the ladder B310/B353 already built for the exit-node sync: the relays are the
//     `exit_servers` rows (the same table the admin page and the route sync read), a
//     relay's TAILNET address is preferred over the operator's public `ssh_target`
//     (B310: the path that survives a provider-level block of the relay's public IP),
//     and "this relay has answered us before" — the B309 `relay_apply_state:<relay>`
//     record — orders the candidates (B353's proven-first rule).
//
//     DIVERGENCE, and why: B353's readJumpHopRows/selectJumpHops live in
//     internal/feature/exit_rules, which IMPORTS this package (it sends alerts), so
//     importing it back is an import cycle. This file therefore reads the SAME table
//     through the SAME db helper (db.ListExitServers) and reuses the SAME pure rules
//     (tailnet-first, proven-first, hostname tie-break) instead of copying the route
//     sync's code path. Extracting one shared `internal/relayhop` package for both
//     callers is a follow-up (B356.2), not something one agent can do inside
//     internal/feature/exit_rules while another owns that tree.
//
//  2. HOW is the tunnel opened? `ssh -W <dest>:<port> -- <relay>` (argv built by
//     headscale.SSHTunnelArgv, which owns every validation) with the child's
//     stdin/stdout wrapped in a net.Conn. The TLS session is this process's own: the
//     relay moves bytes and never sees the bot token, and no command that mentions the
//     token is ever run there.
package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// Environment knobs (B356.1 requirement 4). Documented in docs/operations.md §8.5.
const (
	// EnvRelayFallback is the explicit OFF switch: "0", "false", "no" or "off"
	// disables the fallback entirely (the direct path is then the only path, exactly
	// as before B356.1). Anything else — including unset — leaves it ON, because the
	// whole point is that the bot keeps working when the direct path does not.
	EnvRelayFallback = "SKYGATE_TELEGRAM_RELAY_FALLBACK"
	// EnvPreferredRelay names the relay to try FIRST. Empty = the proven-first order.
	EnvPreferredRelay = "SKYGATE_TELEGRAM_PREFERRED_RELAY"
	// EnvRelaySSHKey is the private key the tunnel uses when the relay's own
	// `exit_servers.ssh_key_path` is empty.
	EnvRelaySSHKey = "SKYGATE_TELEGRAM_SSH_KEY"
)

// relayApplyStatePrefix mirrors exit_rules.SettingRelayApplyStatePrefix (B309), whose
// value is "<unix>|ok" or "<unix>|err|<reason>". It is duplicated as a literal rather
// than imported because importing internal/feature/exit_rules from here is an import
// cycle (see the file doc).
const (
	relayApplyStatePrefix   = "relay_apply_state:"
	relayApplyStateOKSuffix = "|ok"
)

// relayCandidate is one relay the tunnel may be opened through. Every field is
// already validated: whatever reaches this struct may be handed to ssh.
type relayCandidate struct {
	// Hostname is exit_servers.hostname — the stable identity used for ordering,
	// cooldowns and everything the operator sees.
	Hostname string
	// Target is the validated `[user@]host[:port]` the tunnel is opened to.
	Target string
	// KeyPath is the validated absolute private key path.
	KeyPath string
	// Proven is the B309 evidence: skygate has already applied routes to this relay,
	// i.e. this relay demonstrably answers the portal.
	Proven bool
	// Address says WHERE Target came from ("its tailnet address", "the operator's
	// ssh_target"), for the log and the snapshot.
	Address string
}

// relayInventory is one discovery pass.
type relayInventory struct {
	// Candidates are validated and ordered (preferred first, then proven, then by
	// hostname).
	Candidates []relayCandidate
	// Skipped names every row that could NOT become a candidate, with the reason, so
	// "the fallback did nothing" is never silent.
	Skipped []string
}

// defaultRelayKeyPaths is the ordered fallback list used when a relay row has no
// usable `ssh_key_path` of its own. Every entry is a path a documented deployment
// actually mounts; the reference container mounts the operator's SSH dir at
// /ssh-sync, and docs/deploy.md names /ssh-sync/skygate_sync as the bind-mounted
// default. A path is only used when it really exists and is readable (SSHKeyProblem),
// so this list cannot invent a key that is not there — the B292/B270 lesson.
func defaultRelayKeyPaths() []string {
	return []string{
		envOrEmpty(EnvRelaySSHKey),
		envOrEmpty("SKYGATE_EXIT_SSH_KEY"),
		"/ssh-sync/skygate_sync",
		headscale.ContainerDefaultSSHKey, // /ssh-sync/id_ed25519
	}
}

// usableTunnelKey reports whether `path` can be used as the tunnel's identity: it must
// be an absolute, existing, readable file AND its shape must survive being passed to
// ssh (SSHTunnelKeyProblem). Both verdicts come from the packages that own them.
func usableTunnelKey(path string) bool {
	if path == "" {
		return false
	}
	return headscale.SSHKeyProblem(path) == "" && headscale.SSHTunnelKeyProblem(path) == ""
}

// relayAddressFor renders the address the portal should use to reach this relay, and
// says why.
//
// The tailnet address wins (B310), then the operator's `ssh_target` — which may itself
// be a public address and that is fine: the relay only has to be reachable FROM THE
// PORTAL and be able to reach api.telegram.org. A row with neither is not a candidate.
//
// Pure (unit-tested).
func relayAddressFor(row db.ExitServer) (target, how string) {
	user := ""
	if u, _, _ := splitRelayTarget(row.SSHTarget); u != "" {
		user = u
	}
	if ip := db.FirstTailscaleIP(row.TailscaleIP); ip != "" {
		port := strings.TrimSpace(row.SSHPort)
		if _, p := headscale.SplitSSHTarget(row.SSHTarget); p != "" {
			port = p
		}
		return renderRelayTarget(firstNonEmptyString(user, "root"), ip, port), "its tailnet address"
	}
	if t := strings.TrimSpace(row.SSHTarget); t != "" {
		return t, "the operator's ssh_target"
	}
	return "", ""
}

// splitRelayTarget splits `[user@]host[:port]` into its three parts, using
// headscale.SplitSSHTarget for the host:port half so the tunnel and the exit-node sync
// read the same value the same way.
//
// Pure (unit-tested).
func splitRelayTarget(target string) (user, host, port string) {
	t := strings.TrimSpace(target)
	if at := strings.LastIndex(t, "@"); at >= 0 {
		user = t[:at]
		t = t[at+1:]
	}
	host, port = headscale.SplitSSHTarget(t)
	return user, host, port
}

// renderRelayTarget renders `user@host[:port]`, bracketing an IPv6 literal (B353.1:
// an unbracketed IPv6 target is refused by IsSafeSSHTarget and would have been read by
// ssh as user `root@fd7a` plus a malformed host).
//
// Pure (unit-tested).
func renderRelayTarget(user, host, port string) string {
	h := strings.TrimSpace(host)
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		h = "[" + strings.Trim(h, "[]") + "]"
	}
	t := strings.TrimSpace(user)
	if t == "" {
		t = "root"
	}
	out := t + "@" + h
	if p := strings.TrimSpace(port); p != "" {
		out += ":" + p
	}
	return out
}

// orderRelayCandidates puts the operator's preferred relay first, then the proven ones
// (B353's rule: a relay skygate has already applied routes to has answered the portal
// before), then the rest by hostname — a stable order, so two ticks an operator
// compares do not show the candidates shuffled.
//
// Pure (unit-tested).
func orderRelayCandidates(cands []relayCandidate, preferred string) []relayCandidate {
	p := strings.ToLower(strings.TrimSpace(preferred))
	seen := map[string]bool{}
	rest := make([]relayCandidate, 0, len(cands))
	head := make([]relayCandidate, 0, 1)
	for _, c := range cands {
		key := strings.ToLower(c.Hostname)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if p != "" && key == p {
			head = append(head, c)
			continue
		}
		rest = append(rest, c)
	}
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].Proven != rest[j].Proven {
			return rest[i].Proven
		}
		return rest[i].Hostname < rest[j].Hostname
	})
	return append(head, rest...)
}

// buildRelayCandidates turns the exit_servers rows into validated candidates.
//
// A row is skipped — with a named reason — when it is disabled, has no address the
// portal can dial, has a target that fails the B266 shape gate, or has no usable
// private key. Nothing unvalidated ever reaches the argv, so a stored
// `-oProxyCommand=…` (the B266 escalation) cannot become an ssh option here either.
//
// `keyUsable` is injected so the inventory can be tested without a filesystem.
//
// Pure (unit-tested).
func buildRelayCandidates(rows []db.ExitServer, keyPaths []string, proven map[string]bool, keyUsable func(string) bool) relayInventory {
	var inv relayInventory
	for _, row := range rows {
		name := strings.TrimSpace(row.Hostname)
		if name == "" {
			continue
		}
		if !row.Enabled {
			inv.Skipped = append(inv.Skipped, name+": disabled in exit_servers")
			continue
		}
		target, how := relayAddressFor(row)
		if target == "" {
			inv.Skipped = append(inv.Skipped, name+": no tailnet address and no ssh_target")
			continue
		}
		if !headscale.IsSafeSSHTarget(target) {
			inv.Skipped = append(inv.Skipped, fmt.Sprintf("%s: refusing unsafe ssh target %q", name, target))
			continue
		}
		key := ""
		for _, candidate := range append([]string{strings.TrimSpace(row.SSHKeyPath)}, keyPaths...) {
			path := strings.TrimSpace(candidate)
			// An empty path is never "usable": a key check that answers "yes" for ""
			// would hand ssh an argv with no identity at all.
			if path == "" || !keyUsable(path) {
				continue
			}
			key = path
			break
		}
		if key == "" {
			inv.Skipped = append(inv.Skipped, name+": no usable ssh private key (exit_servers.ssh_key_path, "+EnvRelaySSHKey+" or SKYGATE_EXIT_SSH_KEY)")
			continue
		}
		inv.Candidates = append(inv.Candidates, relayCandidate{
			Hostname: name,
			Target:   target,
			KeyPath:  key,
			Proven:   proven != nil && proven[strings.ToLower(name)],
			Address:  how,
		})
	}
	return inv
}

// readRelayCandidates reads the relay table once and returns the ordered candidates
// plus the reasons any row was skipped.
func readRelayCandidates(d *sql.DB, preferred string) relayInventory {
	if d == nil {
		return relayInventory{Skipped: []string{"no database handle"}}
	}
	rows, err := db.ListExitServers(d)
	if err != nil {
		return relayInventory{Skipped: []string{"cannot read exit_servers: " + err.Error()}}
	}
	proven := map[string]bool{}
	for _, row := range rows {
		if name := strings.TrimSpace(row.Hostname); name != "" {
			proven[strings.ToLower(name)] = relayProven(d, name)
		}
	}
	inv := buildRelayCandidates(rows, defaultRelayKeyPaths(), proven, usableTunnelKey)
	inv.Candidates = orderRelayCandidates(inv.Candidates, preferred)
	return inv
}

// relayProven reports whether the B309 record for this relay says the last route
// application succeeded — the only evidence of "this relay answers the portal"
// available from inside the container.
func relayProven(d *sql.DB, hostname string) bool {
	if d == nil {
		return false
	}
	key := relayApplyStatePrefix + strings.ToLower(strings.TrimSpace(hostname))
	v, err := db.GetGlobalSetting(d, key, "")
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.TrimSpace(v), relayApplyStateOKSuffix)
}

// envOrEmpty is a tiny os.Getenv wrapper that also trims, kept here so the env surface
// of this block is visible in one file.
func envOrEmpty(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

// firstNonEmptyString returns the first non-blank value, or "".
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// tunnelStderrLimit bounds how much of ssh's stderr is kept for the failure report.
// The useful line is always the LAST one, so the buffer keeps the tail.
const tunnelStderrLimit = 2048

// tunnelCloseWait bounds how long Close waits for ssh to exit after its stdin is
// closed, before killing it. Without it a wedged ssh would hang the request instead of
// producing the named error the fallback promises.
const tunnelCloseWait = 2 * time.Second

// tailBuffer keeps the last n bytes written to it. ssh's fatal diagnostic is the last
// thing it prints, and a full scrollback would only bury it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	n   int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.n <= 0 {
		b.n = tunnelStderrLimit
	}
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.n {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.n:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

// tunnelAddr is the net.Addr reported by a tunnelConn. It exists so the connection
// satisfies net.Conn honestly instead of returning nil (a nil Addr has crashed
// callers before).
type tunnelAddr string

func (a tunnelAddr) Network() string { return "ssh-tunnel" }
func (a tunnelAddr) String() string  { return string(a) }

// tunnelConn adapts one `ssh -W …` child process to net.Conn. The TLS session the
// caller runs over it terminates in THIS process, so the relay carries ciphertext.
type tunnelConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *tailBuffer

	closeOnce sync.Once
	closeErr  error

	mu     sync.Mutex
	timer  *time.Timer
	closed bool
}

func (c *tunnelConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *tunnelConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *tunnelConn) LocalAddr() net.Addr  { return tunnelAddr("local") }
func (c *tunnelConn) RemoteAddr() net.Addr { return tunnelAddr("relay-tunnel") }

// SetDeadline / SetReadDeadline / SetWriteDeadline are honest: net/http uses them for
// its TLS-handshake and response-header bounds, and a no-op would silently convert
// "the relay stopped answering" into "the request hangs". A deadline here closes the
// tunnel (which kills the ssh child), so the pending Read/Write returns immediately.
func (c *tunnelConn) SetDeadline(t time.Time) error      { return c.armDeadline(t) }
func (c *tunnelConn) SetReadDeadline(t time.Time) error  { return c.armDeadline(t) }
func (c *tunnelConn) SetWriteDeadline(t time.Time) error { return c.armDeadline(t) }

func (c *tunnelConn) armDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if c.closed || t.IsZero() {
		return nil
	}
	d := time.Until(t)
	if d < 0 {
		d = 0
	}
	self := c
	c.timer = time.AfterFunc(d, func() { _ = self.Close() })
	return nil
}

// Close closes ssh's stdin (which makes the child exit), waits briefly for it and
// kills it if it does not. Idempotent: net/http may close a connection more than once.
func (c *tunnelConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		if c.timer != nil {
			c.timer.Stop()
			c.timer = nil
		}
		c.mu.Unlock()
		_ = c.stdin.Close()
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case c.closeErr = <-done:
		case <-time.After(tunnelCloseWait):
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
			c.closeErr = <-done
		}
		_ = c.stdout.Close()
	})
	return nil
}

// stderrTail is the relay's own explanation of why the tunnel failed, or "" when ssh
// said nothing (a killed process, a closed pipe).
func (c *tunnelConn) stderrTail() string {
	if c == nil || c.stderr == nil {
		return ""
	}
	return c.stderr.String()
}

// dialSSHTunnel starts one tunnel and returns the child's stdio as a net.Conn.
//
// The ctx is honoured for the START only. It deliberately does NOT own the child's
// lifetime: net/http cancels a dial context as soon as the connection is established
// (and again when the request ends), which would kill a tunnel the next request is
// meant to reuse — and would abort a response body still being read. The child dies
// with Close/SetDeadline, which the transport drives.
func dialSSHTunnel(ctx context.Context, c relayCandidate, network, addr string) (net.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("telegram relay %s: dial cancelled: %w", c.Hostname, ctx.Err())
	default:
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("telegram relay %s: cannot tunnel to %q: %w", c.Hostname, addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("telegram relay %s: cannot tunnel to %q: %w", c.Hostname, addr, err)
	}
	argv, err := headscale.SSHTunnelArgv(c.KeyPath, c.Target, host, port)
	if err != nil {
		return nil, fmt.Errorf("telegram relay %s: %w", c.Hostname, err)
	}
	cmd := exec.Command("ssh", argv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("telegram relay %s: ssh stdin: %w", c.Hostname, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("telegram relay %s: ssh stdout: %w", c.Hostname, err)
	}
	stderr := &tailBuffer{n: tunnelStderrLimit}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("telegram relay %s: cannot start ssh: %w", c.Hostname, err)
	}
	return &tunnelConn{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}
