// Package exitnode — B412 (2026-10-23): provisioning a BRAND-NEW exit node
// from the panel over SSH.
//
// WHY THIS EXISTS (operator request, verbatim):
//
//	«также как фича в возможностях skygate должна была быть указана настройка
//	 exit node через вебинтерфейс skygate вот ее я и хочу проверить - по сути
//	 просто указав необходимые параметры нового vps и нажать кнопку
//	 зарегистрировать новый exit node»
//
// Until B412 the panel could only mint a pre-auth key and PRINT a command the
// operator had to copy to the new VPS by hand (B266), and separately store an
// exit_servers row. The operator's mental model — "give the panel the VPS
// address and press the button" — was reachable by neither half:
//
//   - B266 minted the key but never touched the machine;
//   - the «Добавить exit node» form stored ssh_target for a node that had to
//     already exist in headscale, so a fresh VPS had no node_id to type in.
//
// This package owns the missing middle: given [host, user, port, node name],
// it builds ONE idempotent bash script that prepares the target (forwarding,
// NAT, tailscale, `tailscale up` with the minted tagged key) and runs it over
// the same `ssh` the route sync already uses. The admin handler
// (internal/feature/admin/exit_nodes_provision.go) does the headscale half:
// mint the key, wait for the node to appear, create the exit_servers row and
// approve 0.0.0.0/0 + ::/0.
//
// SECURITY NOTES (read before touching BuildProvisionScript):
//
//   - Every value interpolated into the script is shell-quoted by shQuote, and
//     every value that originates outside skygate (host, user, node name, CIDR)
//     is shape-validated by Validate BEFORE the ssh process is spawned.
//   - The pre-auth key travels in the script fed to `bash -s` on stdin — never
//     in an argv (ps on the target would show it) and never in a URL. It is
//     single-use and its TTL is capped by the caller.
//   - The script runs as the SSH user. If that user is not root the first stage
//     fails loudly instead of half-configuring the host (sysctl/iptables/
//     tailscale all need root; silently skipping them is how an "exit node"
//     ends up advertising a default route it cannot actually carry).
package exitnode

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"skygate/internal/headscale"
)

// DefaultNATSourceCIDR is the tailnet CGNAT range the MASQUERADE rule covers.
// It matches headscale's default ip_prefix (and the tutorial on the page).
const DefaultNATSourceCIDR = "100.64.0.0/10"

// PreauthTTL is how long the minted key stays valid. The whole point of the
// flow is that the node joins within minutes; a key that outlives the attempt
// would be a standing credential nobody watches.
const PreauthTTL = time.Hour

// Stage IDs are stable identifiers the handler renders and the B-check pins.
const (
	StagePreflight     = "preflight"
	StageSysctl        = "sysctl"
	StageTailscale     = "tailscale"
	StageTailscaleUp   = "tailscale-up"
	StageNAT           = "nat"
	StageReport        = "report"
	StageSSH           = "ssh"
	StageHeadscale     = "headscale"
	StageExitServer    = "exit-server"
	StageApproveRoutes = "approve-routes"
	markerStage        = "SKYGATE_STAGE"
	markerFail         = "SKYGATE_FAIL"
	markerResult       = "SKYGATE_RESULT"
)

// Request is everything the provisioning script needs. It is built from the
// panel form and validated by Validate before any process is spawned.
type Request struct {
	// Host is the VPS address as an IPv4 literal or a DNS name.
	Host string
	// User is the SSH account. root is required for the setup steps.
	User string
	// Port is the SSH port (0 or 22 = default).
	Port int
	// NodeName is the node name in the tailnet (also the exit_servers hostname).
	NodeName string
	// KeyPath is the SSH private key path inside THIS skygate process
	// (container: /ssh-sync/... ; native: <data_dir>/ssh/id_ed25519).
	KeyPath string
	// LoginServer is the headscale URL the node must dial.
	LoginServer string
	// AcceptRoutes is the exit_servers tri-state (-1 false, 0 unset, 1 true).
	AcceptRoutes int
	// InstallTailscale allows `curl … | sh` when tailscale is absent.
	InstallTailscale bool
	// SetupNAT adds the MASQUERADE/FORWARD rules (needed for real egress).
	SetupNAT bool
	// NATSourceCIDR overrides DefaultNATSourceCIDR (validated as a CIDR).
	NATSourceCIDR string
}

// Stage is one line of the remote script's progress report.
type Stage struct {
	ID     string
	Status string
}

// Result is the parsed outcome of one provisioning run.
type Result struct {
	// Stages is the ordered progress log (preflight → report).
	Stages []Stage
	// TailscaleIPv4 is the address the node got in the tailnet ("" if unknown).
	TailscaleIPv4 string
	// FailedStage / FailReason name the first failing step (empty on success).
	FailedStage string
	FailReason  string
	// OK reports that the remote script reached its final marker.
	OK bool
	// RawOutput is the combined output, kept for the audit log on failure.
	RawOutput string
	// Probed carries the facts a dry-run probe observed (uid, tailscale
	// presence, default interface). Empty for a real run.
	Probed ProbeInfo
}

// ProbeInfo is what `Probe` learned about the target without changing it.
type ProbeInfo struct {
	UID            string
	TailscaleFound bool
	Kernel         string
	DefaultIface   string
}

// nodeNameRe is the charset headscale/mDNS accept for a node name. A name with
// a leading dash or a space would either be rejected by headscale or rendered
// into the script as a flag.
var nodeNameRe = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,62}$`)

// userRe accepts the POSIX login-name shape ssh itself accepts.
var userRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,31}$`)

// cidrRe is the shape of an IPv4 CIDR (the MASQUERADE source).
var cidrRe = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2}$`)

// loginServerRe accepts the http(s) URL the node dials.
var loginServerRe = regexp.MustCompile(`^https?://[A-Za-z0-9._-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$`)

// SSHTarget renders the `[user@]host[:port]` value stored in
// exit_servers.ssh_target and validated by headscale.IsSafeSSHTarget.
func (r Request) SSHTarget() string {
	host := strings.TrimSpace(r.Host)
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	t := strings.TrimSpace(r.User) + "@" + host
	if r.Port > 0 && r.Port != 22 {
		t += ":" + strconv.Itoa(r.Port)
	}
	return t
}

// sshHost is the `user@host` word the ssh client receives (the port travels as
// `-p`). The user is ALWAYS spelled out: inside the skygate container the local
// account is `skygate`, so an omitted user would silently try skygate@<vps> and
// fail with "Permission denied (publickey)".
func (r Request) sshHost() string {
	host := strings.TrimSpace(r.Host)
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	u := strings.TrimSpace(r.User)
	if u == "" {
		return host
	}
	return u + "@" + host
}

// Validate refuses a request before anything is spawned. Every message names
// the field and the accepted shape — this is the form's only server-side gate.
func Validate(r Request) error {
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return fmt.Errorf("host: укажите IP или DNS-имя VPS")
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, " \t\n\r/\\;|&$`'\"<>(){}*?!") {
		return fmt.Errorf("host: %q не похож на адрес VPS (ожидается IPv4 или DNS-имя без схемы, пробелов и слэшей)", host)
	}
	user := strings.TrimSpace(r.User)
	if user == "" {
		return fmt.Errorf("ssh user: укажите пользователя (обычно root)")
	}
	if !userRe.MatchString(user) {
		return fmt.Errorf("ssh user: %q — ожидается логин вида root / deploy (латиница, цифры, _ -, до 32 символов)", user)
	}
	if r.Port < 0 || r.Port > 65535 {
		return fmt.Errorf("ssh port: %d вне диапазона 1–65535", r.Port)
	}
	// Final gate: the composed value is what actually reaches the ssh argv and
	// exit_servers.ssh_target, so it must clear the same check the route sync
	// applies before exec (B266).
	if !headscale.IsSafeSSHTarget(r.SSHTarget()) {
		return fmt.Errorf("host: %q не похож на адрес VPS (ожидается IPv4 или DNS-имя без схемы, пробелов и слэшей)", host)
	}
	if !nodeNameRe.MatchString(strings.TrimSpace(r.NodeName)) {
		return fmt.Errorf("node name: %q — латиница, цифры, точка, дефис, подчёркивание (1–63 символа, не с дефиса)", strings.TrimSpace(r.NodeName))
	}
	if p := strings.TrimSpace(r.KeyPath); p == "" {
		return fmt.Errorf("ssh key: путь к приватному ключу не задан")
	}
	if ls := strings.TrimSpace(r.LoginServer); ls == "" || !loginServerRe.MatchString(ls) {
		return fmt.Errorf("login server: %q — ожидается https://headscale.example.com (значение берётся из SKYGATE_TS_LOGIN_SERVER / HEADSCALE_SERVER_URL)", strings.TrimSpace(r.LoginServer))
	}
	if c := strings.TrimSpace(r.NATSourceCIDR); c != "" && !cidrRe.MatchString(c) {
		return fmt.Errorf("nat source: %q — ожидается CIDR вида 100.64.0.0/10", c)
	}
	return nil
}

// shQuote renders s as a single-quoted shell word. Values that reach this
// function are already shape-validated; quoting is the second, independent
// guard (the B266 lesson: a value that looks harmless in one layer becomes an
// option/command in the next).
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// BuildProvisionScript renders the idempotent setup script fed to `bash -s` on
// the target. Pure function: no I/O, no clock, no environment — so the exact
// text can be asserted by a test and syntax-checked with `bash -n`.
//
// authKey is the minted pre-auth key (secret). It appears in the script text
// (stdin only) and never in an argv.
func BuildProvisionScript(r Request, authKey string) string {
	cidr := strings.TrimSpace(r.NATSourceCIDR)
	if cidr == "" {
		cidr = DefaultNATSourceCIDR
	}
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("# B412 — generated by skygate: prepare a new exit node and join the tailnet.\n")
	b.WriteString("# Idempotent: every step checks its own effect first, so a second run is a no-op.\n")
	b.WriteString("# The pre-auth key below is single-use and expires; it is passed on stdin, not in argv.\n")
	b.WriteString("set -u\n")
	b.WriteString("say() { printf '" + markerStage + " %s %s\\n' \"$1\" \"${2:-}\"; }\n")
	b.WriteString("fail() { printf '" + markerStage + " %s fail\\n' \"$1\"; printf '" + markerFail + " %s %s\\n' \"$1\" \"${2:-}\"; exit 1; }\n\n")

	// --- preflight: the setup needs root, and a non-root run must not leave
	// half-configured host behind.
	b.WriteString("# --- preflight: root is required (sysctl/iptables/tailscale all need it)\n")
	b.WriteString("UID_NOW=$(id -u 2>/dev/null || echo none)\n")
	b.WriteString("if [ \"$UID_NOW\" != \"0\" ]; then\n")
	b.WriteString("  fail " + StagePreflight + " \"SSH user is not root (uid=$UID_NOW) — use root, or grant the deploy user passwordless sudo\"\n")
	b.WriteString("fi\n")
	b.WriteString("say " + StagePreflight + " ok\n\n")

	// --- forwarding
	b.WriteString("# --- ip forwarding (a relay that does not forward carries nothing)\n")
	b.WriteString("if ! sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1; then\n")
	b.WriteString("  fail " + StageSysctl + " \"cannot enable net.ipv4.ip_forward\"\n")
	b.WriteString("fi\n")
	b.WriteString("printf 'net.ipv4.ip_forward = 1\\nnet.ipv6.conf.all.forwarding = 1\\n' > /etc/sysctl.d/99-skygate-exit.conf\n")
	b.WriteString("sysctl -p /etc/sysctl.d/99-skygate-exit.conf >/dev/null 2>&1 || true\n")
	b.WriteString("say " + StageSysctl + " ok\n\n")

	// --- tailscale presence
	b.WriteString("# --- tailscale\n")
	b.WriteString("if command -v tailscale >/dev/null 2>&1; then\n")
	b.WriteString("  say " + StageTailscale + " present\n")
	b.WriteString("else\n")
	if r.InstallTailscale {
		b.WriteString("  TS_INSTALL_LOG=/tmp/skygate-tailscale-install.log\n")
		b.WriteString("  if curl -fsSL https://tailscale.com/install.sh | sh >\"$TS_INSTALL_LOG\" 2>&1; then\n")
		b.WriteString("    say " + StageTailscale + " installed\n")
		b.WriteString("  else\n")
		b.WriteString("    fail " + StageTailscale + " \"install failed: $(tail -n 2 \"$TS_INSTALL_LOG\" 2>/dev/null | tr '\\n' ' ')\"\n")
		b.WriteString("  fi\n")
	} else {
		b.WriteString("  fail " + StageTailscale + " \"tailscale is not installed and auto-install is off (light the option in the form, or install it by hand)\"\n")
	}
	b.WriteString("fi\n\n")

	// --- join the tailnet
	b.WriteString("# --- join the tailnet with the tagged key (tag:exit-node is applied by the key itself)\n")
	b.WriteString("UP_LOG=/tmp/skygate-tailscale-up.log\n")
	b.WriteString("TS=\"tailscale\"\n")
	b.WriteString("if command -v timeout >/dev/null 2>&1; then TS=\"timeout 180 tailscale\"; fi\n")
	b.WriteString("if $TS up \\\n")
	b.WriteString("  --login-server=" + shQuote(strings.TrimSpace(r.LoginServer)) + " \\\n")
	b.WriteString("  --authkey=" + shQuote(strings.TrimSpace(authKey)) + " \\\n")
	b.WriteString("  --hostname=" + shQuote(strings.TrimSpace(r.NodeName)) + " \\\n")
	b.WriteString("  --advertise-exit-node --accept-routes --ssh >\"$UP_LOG\" 2>&1; then\n")
	b.WriteString("  say " + StageTailscaleUp + " ok\n")
	b.WriteString("else\n")
	b.WriteString("  fail " + StageTailscaleUp + " \"$(tail -n 3 \"$UP_LOG\" 2>/dev/null | tr '\\n' ' ')\"\n")
	b.WriteString("fi\n\n")

	// --- NAT
	b.WriteString("# --- NAT for the tailnet range (idempotent: -C before -A)\n")
	if r.SetupNAT {
		b.WriteString("EXT_IF=$(ip route 2>/dev/null | awk '/^default/ {print $5; exit}')\n")
		b.WriteString("if [ -z \"$EXT_IF\" ]; then\n")
		b.WriteString("  fail " + StageNAT + " \"no default route — cannot pick the uplink interface\"\n")
		b.WriteString("fi\n")
		b.WriteString("if iptables -t nat -C POSTROUTING -s " + shQuote(cidr) + " -o \"$EXT_IF\" -j MASQUERADE 2>/dev/null; then\n")
		b.WriteString("  say " + StageNAT + " present\n")
		b.WriteString("else\n")
		b.WriteString("  iptables -t nat -A POSTROUTING -s " + shQuote(cidr) + " -o \"$EXT_IF\" -j MASQUERADE || fail " + StageNAT + " \"MASQUERADE rule was refused\"\n")
		b.WriteString("  iptables -C FORWARD -i tailscale0 -o \"$EXT_IF\" -j ACCEPT 2>/dev/null || iptables -A FORWARD -i tailscale0 -o \"$EXT_IF\" -j ACCEPT || fail " + StageNAT + " \"FORWARD rule was refused\"\n")
		b.WriteString("  iptables -C FORWARD -i \"$EXT_IF\" -o tailscale0 -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || iptables -A FORWARD -i \"$EXT_IF\" -o tailscale0 -m state --state RELATED,ESTABLISHED -j ACCEPT || fail " + StageNAT + " \"FORWARD return rule was refused\"\n")
		b.WriteString("  say " + StageNAT + " added\n")
		b.WriteString("  if command -v netfilter-persistent >/dev/null 2>&1; then\n")
		b.WriteString("    if netfilter-persistent save >/dev/null 2>&1; then say " + StageNAT + " persisted; else say " + StageNAT + " not-persisted; fi\n")
		b.WriteString("  else\n")
		b.WriteString("    say " + StageNAT + " not-persisted\n")
		b.WriteString("  fi\n")
		b.WriteString("fi\n\n")
	} else {
		b.WriteString("say " + StageNAT + " skipped\n\n")
	}

	// --- report
	b.WriteString("# --- report the address the node got\n")
	b.WriteString("IP4=$(tailscale ip -4 2>/dev/null | head -n 1)\n")
	b.WriteString("say " + StageReport + " \"${IP4:-none}\"\n")
	b.WriteString("printf '" + markerResult + " ok=1 ip=%s\\n' \"${IP4:-}\"\n")
	return b.String()
}

// BuildProbeScript renders the read-only script a dry run executes: it changes
// nothing and answers "can we even reach this host and is it a suitable
// target".
func BuildProbeScript() string {
	return "#!/bin/bash\n" +
		"# B412 dry run — read-only probe, changes nothing.\n" +
		"set -u\n" +
		"printf '" + markerStage + " " + StageSSH + " ok\\n'\n" +
		"printf 'PROBE uid=%s\\n' \"$(id -u 2>/dev/null || echo none)\"\n" +
		"if command -v tailscale >/dev/null 2>&1; then printf 'PROBE tailscale=present\\n'; else printf 'PROBE tailscale=absent\\n'; fi\n" +
		"printf 'PROBE kernel=%s\\n' \"$(uname -sr 2>/dev/null | tr ' ' '_')\"\n" +
		"printf 'PROBE default_if=%s\\n' \"$(ip route 2>/dev/null | awk '/^default/ {print $5; exit}')\"\n" +
		"printf '" + markerResult + " ok=1\\n'\n"
}

// ParseOutput turns the remote script's stdout into a Result. Pure function.
func ParseOutput(out string) Result {
	res := Result{RawOutput: out}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, markerStage+" "):
			rest := strings.TrimSpace(strings.TrimPrefix(line, markerStage+" "))
			parts := strings.SplitN(rest, " ", 2)
			st := Stage{ID: parts[0]}
			if len(parts) == 2 {
				st.Status = strings.TrimSpace(parts[1])
			}
			res.Stages = append(res.Stages, st)
			if st.Status == "fail" && res.FailedStage == "" {
				res.FailedStage = st.ID
			}
		case strings.HasPrefix(line, markerFail+" "):
			rest := strings.TrimSpace(strings.TrimPrefix(line, markerFail+" "))
			parts := strings.SplitN(rest, " ", 2)
			if res.FailedStage == "" {
				res.FailedStage = parts[0]
			}
			if len(parts) == 2 {
				res.FailReason = strings.TrimSpace(parts[1])
			}
		case strings.HasPrefix(line, markerResult+" "):
			fields := strings.Fields(strings.TrimPrefix(line, markerResult+" "))
			for _, f := range fields {
				if strings.HasPrefix(f, "ok=") {
					res.OK = strings.TrimPrefix(f, "ok=") == "1"
				}
				if strings.HasPrefix(f, "ip=") {
					res.TailscaleIPv4 = strings.TrimPrefix(f, "ip=")
				}
			}
		case strings.HasPrefix(line, "PROBE "):
			for _, f := range strings.Fields(strings.TrimPrefix(line, "PROBE ")) {
				k, v, ok := strings.Cut(f, "=")
				if !ok {
					continue
				}
				switch k {
				case "uid":
					res.Probed.UID = v
				case "tailscale":
					res.Probed.TailscaleFound = v == "present"
				case "kernel":
					res.Probed.Kernel = v
				case "default_if":
					res.Probed.DefaultIface = v
				}
			}
		}
	}
	return res
}

// SSHArgv is the pure argv builder for the ssh client (unit-tested). It reuses
// the hardening of SetAdvertisedRoutes: BatchMode (never prompt in a headless
// process), accept-new host keys, a bounded connect timeout, IdentitiesOnly so
// an unrelated agent key cannot be offered, and `--` before the host so a
// future validation regression still cannot turn a value into an option.
func SSHArgv(r Request, remoteCommand string) []string {
	argv := []string{
		"-i", strings.TrimSpace(r.KeyPath),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=15",
	}
	if r.Port > 0 && r.Port != 22 {
		argv = append(argv, "-p", strconv.Itoa(r.Port))
	}
	argv = append(argv, "--", r.sshHost())
	argv = append(argv, remoteCommand)
	return argv
}

// execCommandContext is the injectable edge: tests replace it to assert the
// argv and the stdin they would have fed the real ssh.
var execCommandContext = exec.CommandContext

// Run executes script on the target over ssh and parses the report. The error
// is transport-level only (ssh could not run / the remote shell died without
// reporting); a script-level failure comes back as Result with FailedStage set.
func Run(ctx context.Context, r Request, script string) (Result, error) {
	cmd := execCommandContext(ctx, "ssh", SSHArgv(r, "bash -s")...)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	res := ParseOutput(string(out))
	if err != nil && res.FailedStage == "" {
		// The remote shell never reached a fail() marker: ssh itself refused
		// (auth, DNS, timeout) or the connection died mid-script.
		return res, fmt.Errorf("ssh %s: %w: %s", r.SSHTarget(), err, firstLine(string(out)))
	}
	return res, nil
}

// Probe runs the read-only script (dry run).
func Probe(ctx context.Context, r Request) (Result, error) {
	return Run(ctx, r, BuildProbeScript())
}

// firstLine keeps ssh's own one-line diagnosis (the full banner is noise in a
// flash message; RawOutput keeps everything for the audit row).
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > 200 {
				return line[:200] + "…"
			}
			return line
		}
	}
	return ""
}

// Timeout is how long the handler waits for the whole remote setup. Exported so
// the page can say it out loud ("до 3 минут") instead of leaving the operator
// staring at a spinning button.
const Timeout = 3 * time.Minute
