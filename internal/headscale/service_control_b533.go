// service_control_b533.go — B533 (2026-11-27)
//
// /admin/headscale must be able to answer three questions about the headscale
// SERVICE that this skygate depends on, and ACT on the answer:
//
//  1. WHAT IS IT DOING — the manager (systemd / OpenRC / docker), the unit or
//     container, the state, when it became active, how many times it was
//     restarted, the last error it logged, whether its API answers, and which
//     version answers.
//  2. WHERE DOES ITS NAME POINT — the control URL skygate calls, the addresses
//     that name resolves to, and the addresses THIS HOST actually owns.
//  3. WHAT TO DO ABOUT IT — remediations, each carrying copy-paste commands,
//     derived from the measured state instead of from a wiki page.
//
// WHY (measured 2026-11-27, the incident this block exists for). The provider
// moved the production VM to a new IPv4; the zone kept pointing `head`,
// `skygate` and `derp` at the old one. Headscale itself was healthy and
// listening, skygate answered /healthz on loopback — and every device in the
// tailnet lost its coordination server, the panel vanished from the network, and
// the only signal anywhere was "headscale is down", which named the product
// instead of the name. The mismatch between "what this host is" and "what its
// name resolves to" is one line of measurement; nothing measured it.
//
// The RESTART goes through a root-owned path unit (deploy/skygate-apply-service.sh
// + skygate-service.path) exactly like the policy (B272.3) and local-routes
// (B293) appliers: the native skygate unit runs unprivileged under
// ProtectSystem=strict, so `systemctl restart headscale` from the service is not
// a right it has. `restartHeadscaleUnit` (acl.go) stays what it was — the
// in-process attempt used by the file-mode policy write — and this block adds the
// privileged door the operator can knock on from the page.
package headscale

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ServiceState is the verdict about the headscale SERVICE, not about its API.
//
// The two are deliberately separate: an API failure with a running service is a
// name / port / proxy / policy problem, while a stopped service is an
// operational one, and the operator's next click differs in each case.
type ServiceState string

const (
	// ServiceRunning — the manager reports active and the API answered.
	ServiceRunning ServiceState = "running"
	// ServiceRunningAPIDown — the service is up, the API does not answer.
	ServiceRunningAPIDown ServiceState = "running_api_down"
	// ServiceFailed — the unit is in the failed state (crash loop, bad exit).
	ServiceFailed ServiceState = "failed"
	// ServiceStopped — the unit exists and is inactive.
	ServiceStopped ServiceState = "stopped"
	// ServiceAbsent — no local headscale is managed by this host (external
	// control plane, or installed some other way): the page says so instead of
	// pretending a restart button would do anything.
	ServiceAbsent ServiceState = "absent"
	// ServiceUnknown — we could not ask. Never rendered as "healthy".
	ServiceUnknown ServiceState = "unknown"
)

// Healthy reports whether the state needs no operator action.
func (s ServiceState) Healthy() bool { return s == ServiceRunning }

// Remediation is one named problem with the commands that end it.
//
// Key is the stable i18n suffix (headscale_service.fix_<key>), so the page
// translates WHY while the commands stay literal — a translated shell command is
// a command the operator cannot paste.
type Remediation struct {
	Key      string
	Commands []string
}

// ServiceFacts is everything the page shows and every remediation is derived
// from. Every filled field is a MEASURED value; what could not be read is listed
// in CollectNotes and never guessed.
type ServiceFacts struct {
	Manager     string // "systemd" | "openrc" | "docker" | "none"
	Unit        string // unit / service / container name
	State       ServiceState
	StateRaw    string // the manager's own words (ActiveState/SubState, rc output)
	ActiveSince string
	Restarts    int
	LastError   string

	APIOK         bool
	APIError      string
	ServerVersion string

	ControlURL  string
	ControlHost string

	ResolvedIPs []string
	LocalIPs    []string
	// NameMismatch is true when the control name resolves to addresses this
	// host does not own. Only evaluated for a LOCAL headscale: an external
	// control plane legitimately lives at another address.
	NameMismatch bool

	PolicyStatus string // the B272 PolicyAuditStatus, when it was measured
	PolicyPath   string

	// MeasuredAt is when this collection STARTED (B535). It is the ordering point
	// for the applier's verdict: a verdict written after it describes a service
	// that this page has not looked at yet.
	MeasuredAt time.Time
	// Journal is the unit's own last lines (B535), redacted. A snapshot that could
	// not be read carries a Note instead of lines.
	Journal JournalSnapshot

	Remediations []Remediation
	// CollectNotes lists what could NOT be measured and why, so a partial
	// summary never reads as a complete one.
	CollectNotes []string
}

// ServiceAllowedServices is the ONLY set of services the privileged applier will
// touch. It is validated on BOTH sides — here, before a request is written, and
// in deploy/skygate-apply-service.sh before a command runs — because the request
// file is written by an unprivileged process.
var ServiceAllowedServices = []string{"headscale", "skygate", "caddy", "derper", "tailscaled"}

// ServiceAllowedActions is the action allow-list. "status" is read-only and
// exists so the panel can prove the helper is installed and working.
var ServiceAllowedActions = []string{"restart", "status"}

// ServiceRequestValid reports whether (action, service) may be handed over.
func ServiceRequestValid(action, service string) error {
	if !b533Contains(ServiceAllowedActions, action) {
		return fmt.Errorf("service helper: action %q is not in the allow-list %v", action, ServiceAllowedActions)
	}
	if !b533Contains(ServiceAllowedServices, service) {
		return fmt.Errorf("service helper: service %q is not in the allow-list %v", service, ServiceAllowedServices)
	}
	return nil
}

func b533Contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// --- paths and arming -------------------------------------------------------

func serviceHelperPath() string {
	return getenvDefault("SKYGATE_SERVICE_APPLIER", "/usr/local/lib/skygate/skygate-apply-service.sh")
}

func servicePathUnit() string {
	return getenvDefault("SKYGATE_SERVICE_PATH_UNIT", "/etc/systemd/system/skygate-service.path")
}

// ServiceRequestPath is the data-only file the privileged applier consumes.
func ServiceRequestPath() string {
	if p := os.Getenv("SKYGATE_SERVICE_REQUEST_PATH"); p != "" {
		return p
	}
	return filepath.Join(SkygateUpdateDir(), "service.request.props")
}

// ServiceApplyStatusPath is where the applier records its last verdict.
func ServiceApplyStatusPath() string {
	if p := os.Getenv("SKYGATE_SERVICE_APPLY_STATUS"); p != "" {
		return p
	}
	return filepath.Join(SkygateUpdateDir(), "service.apply.status")
}

// SkygateUpdateDir is the data-only directory skygate may write requests into.
// It mirrors PolicyRequestPath's resolution so both helpers live side by side.
func SkygateUpdateDir() string {
	if dir := os.Getenv("SKYGATE_UPDATE_DIR"); dir != "" {
		return dir
	}
	if state := os.Getenv("SKYGATE_UPDATE_STATE_PATH"); state != "" {
		return filepath.Join(filepath.Dir(state), "update")
	}
	return "/var/lib/skygate/update"
}

// ServiceHelperArmed reports whether the privileged helper is installed. False
// means the restart button must explain what to install instead of pretending to
// have restarted anything.
func ServiceHelperArmed() bool {
	if _, err := os.Stat(serviceHelperPath()); err != nil {
		return false
	}
	_, err := os.Stat(servicePathUnit())
	return err == nil
}

// --- pure helpers (unit-tested without a host) ------------------------------

// ParseSystemctlShow reads the `systemctl show` property dump B533 asks for.
//
// The output is `Key=Value` lines; every key is optional, because older systemd
// releases omit some of them and a missing field must stay missing.
func ParseSystemctlShow(out string) (activeState, subState, activeEnter string, restarts int) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			activeState = val
		case "SubState":
			subState = val
		case "ActiveEnterTimestamp":
			activeEnter = val
		case "NRestarts":
			if n, err := strconv.Atoi(val); err == nil {
				restarts = n
			}
		}
	}
	return activeState, subState, activeEnter, restarts
}

// ClassifyServiceState turns what the manager said plus what the API answered
// into the page's verdict. Pure, so the whole matrix is testable without a
// systemd host: a stopped unit outranks an API that also failed (the API failure
// is then a CONSEQUENCE), and an API failure with a healthy unit keeps its own
// state because the operator's next action differs.
func ClassifyServiceState(manager, activeState, subState string, apiOK bool) ServiceState {
	switch manager {
	case "", "none":
		return ServiceAbsent
	case "systemd":
		switch strings.TrimSpace(activeState) {
		case "active":
			if apiOK {
				return ServiceRunning
			}
			return ServiceRunningAPIDown
		case "failed":
			return ServiceFailed
		case "activating", "deactivating", "reloading":
			// Mid-transition: a restart is in flight. An API that answers is
			// the better fact; otherwise this needs the operator's click.
			if apiOK {
				return ServiceRunning
			}
			return ServiceStopped
		default: // inactive, unknown
			if apiOK {
				return ServiceRunning
			}
			return ServiceStopped
		}
	case "openrc":
		// rc-service status prints " * status: started" / "stopped" / "crashed".
		s := strings.ToLower(strings.TrimSpace(activeState))
		switch {
		case strings.Contains(s, "started"):
			if apiOK {
				return ServiceRunning
			}
			return ServiceRunningAPIDown
		case strings.Contains(s, "crashed"):
			return ServiceFailed
		default:
			if apiOK {
				return ServiceRunning
			}
			return ServiceStopped
		}
	case "docker":
		s := strings.ToLower(strings.TrimSpace(activeState))
		switch {
		case strings.Contains(s, "running"):
			if apiOK {
				return ServiceRunning
			}
			return ServiceRunningAPIDown
		case strings.Contains(s, "restarting"):
			return ServiceFailed
		default:
			if apiOK {
				return ServiceRunning
			}
			return ServiceStopped
		}
	}
	return ServiceUnknown
}

// NormalizeAddrs lowercases, strips zones and de-duplicates an address list,
// sorted, so two sets can be compared and displayed deterministically.
func NormalizeAddrs(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		a := strings.TrimSpace(strings.ToLower(raw))
		if a == "" {
			continue
		}
		if i := strings.IndexByte(a, '%'); i >= 0 { // fe80::1%eth0
			a = a[:i]
		}
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// ControlNamePointsElsewhere is the predicate behind the 2026-11-27 incident: a
// LOCAL headscale whose control name resolves to addresses this host does not
// own. A name that does not resolve at all is a different failure (ResolvedIPs is
// empty) and is reported separately.
func ControlNamePointsElsewhere(resolved, local []string) bool {
	if len(resolved) == 0 || len(local) == 0 {
		return false
	}
	own := map[string]struct{}{}
	for _, a := range NormalizeAddrs(local) {
		own[a] = struct{}{}
	}
	for _, a := range NormalizeAddrs(resolved) {
		if _, ok := own[a]; ok {
			return false
		}
	}
	return true
}

// DeriveServiceRemediations is the "what to do about it" half: the same measured
// facts turned into named problems with commands. Pure, so the page and the
// contract test agree on what a state means.
func DeriveServiceRemediations(f ServiceFacts) []Remediation {
	var out []Remediation
	add := func(key string, cmds ...string) {
		out = append(out, Remediation{Key: key, Commands: cmds})
	}
	unit := orDefault(f.Unit, "headscale")
	host := orDefault(f.ControlHost, "<control-host>")
	controlURL := orDefault(f.ControlURL, "http://127.0.0.1:8080")

	switch f.State {
	case ServiceAbsent:
		add("external_or_absent",
			"# headscale is not managed by this host.",
			"# If it runs elsewhere, check it there — the panel can only report the API:",
			"curl -fsS "+controlURL+"/health")
		return out
	case ServiceUnknown:
		add("manager_missing",
			"# Neither systemctl, rc-service nor docker answered on PATH, so this host cannot report the service.",
			"command -v systemctl || command -v rc-service || command -v docker")
	case ServiceFailed, ServiceStopped:
		add("start",
			"sudo systemctl reset-failed "+unit+" 2>/dev/null || true",
			"sudo systemctl start "+unit,
			"systemctl is-active "+unit)
		add("logs",
			"journalctl -u "+unit+" -n 80 --no-pager",
			"journalctl -u "+unit+" -b --no-pager | grep -iE 'error|fatal|panic' | tail -20")
	}

	if !f.APIOK {
		switch {
		case f.NameMismatch:
			// THE incident: the service is fine, the name is wrong.
			add("dns_mismatch",
				"# the control name resolves to an address this host does not own",
				"dig +short "+host,
				"# this host owns: "+strings.Join(NormalizeAddrs(f.LocalIPs), " "),
				"# point the A/AAAA record of "+host+" at one of them, then:",
				"getent hosts "+host)
		case len(f.ResolvedIPs) == 0:
			add("dns_unresolved",
				"# the control name does not resolve at all",
				"dig +short "+host,
				"# check /etc/resolv.conf and the zone's NS records")
		default:
			add("api_unreachable",
				"# the name resolves to this host and the API still does not answer",
				"ss -ltnp | grep -E ':(8080|50443|443)\\b'",
				"curl -fsS "+controlURL+"/health",
				"journalctl -u "+unit+" -n 50 --no-pager")
		}
	}

	if f.Manager != "" && f.Manager != "none" && !ServiceHelperArmed() {
		add("helper_missing",
			"# the panel cannot restart the service without the privileged applier:",
			"sudo bash deploy/install-service-helper.sh",
			"systemctl status skygate-service.path --no-pager")
	}
	return out
}

// --- collection (I/O) -------------------------------------------------------

// ServiceProbe is the I/O surface of CollectServiceFacts, injectable so the
// collection logic is testable on a machine with no headscale at all.
type ServiceProbe struct {
	// Run executes a command and returns stdout+stderr; nil = exec.CommandContext.
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// LookupHost resolves a name; nil = net.DefaultResolver.LookupHost.
	LookupHost func(ctx context.Context, host string) ([]string, error)
	// LocalAddrs lists this host's addresses; nil = every non-loopback interface.
	LocalAddrs func() []string
	// LookPath; nil = exec.LookPath.
	LookPath func(string) (string, error)
}

func (p ServiceProbe) run(ctx context.Context, name string, args ...string) (string, error) {
	if p.Run != nil {
		return p.Run(ctx, name, args...)
	}
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func (p ServiceProbe) has(name string) bool {
	if p.LookPath != nil {
		_, err := p.LookPath(name)
		return err == nil
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func (p ServiceProbe) lookupHost(ctx context.Context, host string) ([]string, error) {
	if p.LookupHost != nil {
		return p.LookupHost(ctx, host)
	}
	return net.DefaultResolver.LookupHost(ctx, host)
}

func (p ServiceProbe) localAddrs() []string {
	if p.LocalAddrs != nil {
		return NormalizeAddrs(p.LocalAddrs())
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return NormalizeAddrs(out)
}

// ServiceUnitName is the unit / container name B533 operates on.
func (c *Client) ServiceUnitName() string {
	if c != nil && c.headscaleUnit != "" {
		return c.headscaleUnit
	}
	return getenvDefault("SKYGATE_HEADSCALE_UNIT", "headscale")
}

// CollectServiceFacts measures the service, its name and its API.
//
// It never returns an error: a summary that could not be collected is a summary
// with CollectNotes, because "we could not look" must be visible as its own thing
// (the B522 lesson) and must not blank the page.
func (c *Client) CollectServiceFacts(ctx context.Context, probe ServiceProbe) ServiceFacts {
	f := ServiceFacts{Unit: c.ServiceUnitName()}
	// B535: the ordering point for the applier's verdict. Set FIRST, because the
	// question it answers is "was the verdict written before we looked".
	f.MeasuredAt = time.Now().UTC()
	unit := f.Unit

	// (a) who manages it, in the order installs are laid out.
	var activeState, subState string
	switch {
	case probe.has("systemctl") && systemdUnitListed(probe, ctx, unit):
		f.Manager = "systemd"
		out, err := probe.run(ctx, "systemctl", "show", unit,
			"--property=ActiveState,SubState,ActiveEnterTimestamp,NRestarts", "--no-pager")
		if err != nil {
			f.CollectNotes = append(f.CollectNotes, fmt.Sprintf("systemctl show %s: %v", unit, err))
		}
		var restarts int
		activeState, subState, f.ActiveSince, restarts = ParseSystemctlShow(out)
		f.StateRaw = strings.TrimSpace(activeState + "/" + subState)
		f.Restarts = restarts
		if activeState == "failed" || subState == "failed" {
			f.LastError = lastJournalError(probe, ctx, unit)
		}
	case probe.has("rc-service") && openRCServiceListed(probe, ctx, unit):
		f.Manager = "openrc"
		out, _ := probe.run(ctx, "rc-service", unit, "status")
		f.StateRaw = firstLine(out)
		activeState = f.StateRaw
	case probe.has("docker") && dockerContainerListed(probe, ctx, unit):
		f.Manager = "docker"
		out, err := probe.run(ctx, "docker", "inspect", "-f",
			"{{.State.Status}} restarts={{.RestartCount}} started={{.State.StartedAt}}", unit)
		if err != nil {
			f.CollectNotes = append(f.CollectNotes, fmt.Sprintf("docker inspect %s: %v", unit, err))
		}
		f.StateRaw = firstLine(out)
		activeState = f.StateRaw
	default:
		f.Manager = "none"
	}

	// (b) the API and the version that answers it.
	switch {
	case c == nil || c.BaseURL == "":
		f.CollectNotes = append(f.CollectNotes, "no headscale client is configured, so only the local service could be measured")
	case c.http == nil:
		// A client with no transport cannot ask anything. Say so instead of
		// dereferencing it: this collector must never take the page down.
		f.ControlURL = c.BaseURL
		if u, err := url.Parse(c.BaseURL); err == nil {
			f.ControlHost = u.Hostname()
		}
		f.CollectNotes = append(f.CollectNotes, "the headscale client has no HTTP transport configured, so the API was not probed")
	default:
		f.ControlURL = c.BaseURL
		if u, err := url.Parse(c.BaseURL); err == nil {
			f.ControlHost = u.Hostname()
		}
		apiCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		if err := c.PingContext(apiCtx); err != nil {
			f.APIError = err.Error()
		} else {
			f.APIOK = true
			if v, _, verr := c.VersionProbe(apiCtx); verr == nil {
				f.ServerVersion = v
			} else {
				f.CollectNotes = append(f.CollectNotes, "version probe: "+verr.Error())
			}
		}
	}

	// (c) the state, now that the API has spoken.
	f.State = ClassifyServiceState(f.Manager, activeState, subState, f.APIOK)

	// (d) the name — only meaningful for a LOCAL headscale.
	f.LocalIPs = probe.localAddrs()
	if f.ControlHost != "" {
		if addrs, err := probe.lookupHost(ctx, f.ControlHost); err != nil {
			f.CollectNotes = append(f.CollectNotes, fmt.Sprintf("resolve %s: %v", f.ControlHost, err))
		} else {
			f.ResolvedIPs = NormalizeAddrs(addrs)
		}
	}
	if f.Manager != "" && f.Manager != "none" {
		f.NameMismatch = ControlNamePointsElsewhere(f.ResolvedIPs, f.LocalIPs)
	}

	// (e) the unit's own words (B535). Read for EVERY manager and every state, not
	// only for a failed unit: "служба работает, но в журнале повторяется одна и та
	// же ошибка" is exactly the state the operator cannot see any other way. The
	// window is bounded and redacted; a window we could not read carries a Note.
	f.Journal = CollectServiceJournal(ctx, probe, f.Manager, unit, ServiceJournalRead)
	if f.LastError == "" && f.Journal.Errors > 0 {
		f.LastError = lastErrorLine(f.Journal.Lines)
	}

	// (f) the B272 policy audit — only when headscale is local, because a remote
	// control plane's policy file is not ours to inspect.
	if f.Manager != "" && f.Manager != "none" {
		audit := AuditHeadscalePolicy()
		f.PolicyStatus = string(audit.Status)
		f.PolicyPath = audit.Path
	}

	f.Remediations = DeriveServiceRemediations(f)
	return f
}

func systemdUnitListed(probe ServiceProbe, ctx context.Context, unit string) bool {
	out, err := probe.run(ctx, "systemctl", "list-unit-files", "--no-legend", "--no-pager", unit+".service")
	if err != nil {
		return false
	}
	return strings.Contains(out, unit+".service")
}

func openRCServiceListed(probe ServiceProbe, ctx context.Context, unit string) bool {
	if _, err := os.Stat(filepath.Join("/etc/init.d", unit)); err == nil {
		return true
	}
	out, err := probe.run(ctx, "rc-service", "--list")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == unit {
			return true
		}
	}
	return false
}

func dockerContainerListed(probe ServiceProbe, ctx context.Context, unit string) bool {
	out, err := probe.run(ctx, "docker", "ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == unit {
			return true
		}
	}
	return false
}

// lastJournalError reads the last lines that look like a failure. Bounded (one
// command, -n 80) because a crash loop can log megabytes.
func lastJournalError(probe ServiceProbe, ctx context.Context, unit string) string {
	out, err := probe.run(ctx, "journalctl", "-u", unit, "-n", "80", "--no-pager", "-o", "cat")
	if err != nil {
		return ""
	}
	var last string
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		low := strings.ToLower(l)
		if l == "" {
			continue
		}
		if strings.Contains(low, "error") || strings.Contains(low, "fatal") ||
			strings.Contains(low, "panic") || strings.Contains(low, "failed") {
			last = l
		}
	}
	return last
}

// --- privileged request (unprivileged half) ---------------------------------

// ErrServiceHelperUnavailable means the root-owned applier is not installed.
var ErrServiceHelperUnavailable = errors.New("service helper is not installed (deploy/install-service-helper.sh)")

// RequestServiceAction hands (action, service) to the privileged applier.
//
// The request is DATA ONLY and written through a rename, for the reason B283 paid
// for on the policy path: an applier that reads a file being rewritten in place
// splices two documents together.
func RequestServiceAction(action, service string) error {
	if err := ServiceRequestValid(action, service); err != nil {
		return err
	}
	if !ServiceHelperArmed() {
		return ErrServiceHelperUnavailable
	}
	req := ServiceRequestPath()
	dir := filepath.Dir(req)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("service helper: create %s: %w", dir, err)
	}
	body := fmt.Sprintf("# skygate privileged service request (B533)\n# data only — never sourced by the applier\nACTION=%s\nSERVICE=%s\nREQUESTED_AT=%q\nREQUESTED_BY=skygate\n",
		action, service, time.Now().UTC().Format(time.RFC3339))
	tmp, err := os.CreateTemp(dir, "service.request.*.tmp")
	if err != nil {
		return fmt.Errorf("service helper: create temp request in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("service helper: write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("service helper: close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return fmt.Errorf("service helper: chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, req); err != nil {
		return fmt.Errorf("service helper: rename %s -> %s: %w", tmpName, req, err)
	}
	return nil
}

// ServiceApplyResult is the applier's answer to the last request.
type ServiceApplyResult struct {
	Present   bool
	OK        bool
	Result    string // ok | failed
	State     string // the manager's state AFTER the action
	Command   string
	Reason    string
	UpdatedAt string
	// B535: whether the unit was ACTIVE after the restart, and how long the applier
	// waited for that. A restart that returns before the unit is up is a restart
	// nobody verified.
	ActiveAfter string
	Waited      string
}

// ReadServiceApplyResult parses <update_dir>/service.apply.status.
//
// A missing file is NOT an error: it means the applier has not run yet (the path
// unit fires within milliseconds, but a page may be rendered before it does).
func ReadServiceApplyResult() (ServiceApplyResult, error) {
	var r ServiceApplyResult
	raw, err := os.ReadFile(ServiceApplyStatusPath())
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return r, err
	}
	r.Present = true
	for _, line := range strings.Split(string(raw), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "RESULT":
			r.Result = val
			r.OK = val == "ok"
		case "STATE":
			r.State = val
		case "COMMAND":
			r.Command = val
		case "REASON":
			r.Reason = val
		case "TS":
			r.UpdatedAt = val
		case "ACTIVE_AFTER":
			r.ActiveAfter = val
		case "WAITED":
			r.Waited = val
		}
	}
	return r, nil
}
