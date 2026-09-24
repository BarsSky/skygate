// internal/feature/exit_rules/relay_transport_tailnet_b310.go — B310 (v1.5.75).
//
// WHY THIS EXISTS (measured on the agent VM, 2026-09-23)
//
// The operator's exit node `karolina` became unreachable ("блок, не пингуется")
// and skygate's route sync could do nothing about it:
//
//	exit-node sync(karolina): not a local relay (local daemon unreadable) — using the SSH transport
//	staggeredSync(aggregated): karolina SSH err: ssh root@100.64.0.2:18022 … Operation timed out
//
// The target is a TAILNET address (100.64.0.2), which is the right idea — a tailnet
// path survives a provider-level block of the relay's public IP — but on this host it
// could never work, and nothing said so:
//
//	$ ip route get 100.64.0.2
//	100.64.0.2 via 192.168.13.1 dev ens18 src 192.168.13.10     # the LAN gateway!
//	$ tailscale status
//	failed to connect to local tailscaled; it doesn't appear to be running
//	$ docker exec skygate-skygate-1 tailscale status
//	failed to connect to local tailscaled; it doesn't appear to be running
//
// The skygate HOST is not in the tailnet at all (no tailscaled, no tailscale0) and
// neither is the container (its Tailscale is configured but disabled:
// SKYGATE_TS_AUTHKEY_FILE=/dev/null), so every packet to 100.64.0.0/10 left through
// the LAN gateway and timed out. B292's "use the live Tailscale IP" fallback then
// looked like a working automatic repair while it was structurally impossible — the
// page showed an IP, the sync showed a timeout, and the operator had no way to tell
// "the node is down" from "skygate is not on the network it is trying to use".
//
// WHAT THIS BLOCK ADDS
//
//  1. `RelayEndpoint` — every way skygate may reach ONE relay, as data: the tailnet
//     address(es) headscale reports, the operator's `exit_servers.ssh_target`, and
//     the bare node name as the last resort.
//  2. A real LADDER: each candidate is TCP-probed first (bounded, 3 s) and then
//     actually used; the first that answers wins, and the transport that carried the
//     routes is recorded (`relay_apply_via:<relay>`) so the page can show it. The
//     tailnet candidate is preferred when skygate itself is on the tailnet — that is
//     the path that keeps working when a geo-block or a provider rule removes the
//     public one — and the existing behaviour (use the operator's target) is the
//     fallback, not a replacement.
//  3. A truthful answer when it CANNOT work: `SkygateTailnetState()` reports whether
//     skygate has a usable tailnet path (an interface with a 100.64.0.0/10 address,
//     not merely a running tailscaled in userspace mode, which cannot carry a plain
//     ssh), and both the ladder and /admin/exit-nodes say "the tailnet path is
//     unavailable: skygate is not on the tailnet" instead of showing a timeout.
//  4. The onboarding half (see exit_node_register.go): a new exit node is given
//     skygate's management public key when it joins, so the tailnet path is usable
//     from the first sync instead of requiring a hand-copied key later.
//
// It deliberately does NOT change which relay advertises which prefix (B274/B275) —
// only how skygate TALKS to the relay that already owns the work.
package exit_rules

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"skygate/internal/headscale"
	"skygate/internal/tsstate"
)

// Endpoint kinds, recorded per relay and rendered on the page.
const (
	RelayEndpointTailnet = "tailnet"
	RelayEndpointPublic  = "public"
	RelayEndpointName    = "name"
)

// relayProbeTimeout bounds ONE candidate probe. Three seconds is short enough that
// a dead candidate cannot hold the whole staggered sync (which walks every relay)
// and long enough for a tailnet handshake on a slow link.
var relayProbeTimeout = 3 * time.Second

// RelayEndpoint is one way to reach one relay over ssh.
type RelayEndpoint struct {
	// Kind is "tailnet", "public" or "name" (see the constants above).
	Kind string
	// User is the ssh user (defaults to root).
	User string
	// Host is the address or name ssh is given.
	Host string
	// Port is the sshd port ("" means 22).
	Port string
	// Why names how this candidate was derived, for the log and the page.
	Why string
}

// Target renders the `[user@]host[:port]` shape SetAdvertisedRoutes expects.
func (e RelayEndpoint) Target() string {
	user := strings.TrimSpace(e.User)
	if user == "" {
		user = "root"
	}
	t := user + "@" + e.Host
	if p := strings.TrimSpace(e.Port); p != "" && p != "22" {
		t += ":" + p
	}
	return t
}

// Label is the short human form used in the log and in the failure report
// ("tailnet 100.64.0.2:18022").
func (e RelayEndpoint) Label() string {
	host := e.Host
	if p := strings.TrimSpace(e.Port); p != "" {
		host += ":" + p
	}
	return e.Kind + " " + host
}

// IsTailnetAddress reports whether host is inside the CGNAT range Tailscale hands
// out (100.64.0.0/10) or its IPv6 prefix (fd7a:115c:a1e0::/48).
//
// This is what tells "the operator configured the tailnet path" (which needs
// skygate to BE on the tailnet) apart from "the operator configured a public
// address" (which works from anywhere).
func IsTailnetAddress(host string) bool {
	h := strings.Trim(strings.TrimSpace(host), "[]")
	if ip := net.ParseIP(h); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
		}
		return strings.HasPrefix(strings.ToLower(ip.String()), "fd7a:115c:a1e0")
	}
	return false
}

// splitSSHEndpoint splits `[user@]host[:port]` into its three parts. IPv6
// literals are accepted in brackets (`root@[fd7a::1]:22`), which is the only
// unambiguous shape once a port is present.
func splitSSHEndpoint(target string) (user, host, port string) {
	t := strings.TrimSpace(target)
	if at := strings.LastIndex(t, "@"); at >= 0 {
		user = t[:at]
		t = t[at+1:]
	}
	if strings.HasPrefix(t, "[") {
		if end := strings.Index(t, "]"); end > 0 {
			host = t[1:end]
			if rest := strings.TrimPrefix(t[end+1:], ":"); rest != t[end+1:] {
				port = rest
			}
			return user, host, port
		}
	}
	// A single colon means host:port; two or more mean a bare IPv6 literal.
	if strings.Count(t, ":") == 1 {
		if idx := strings.LastIndex(t, ":"); idx > 0 {
			return user, t[:idx], t[idx+1:]
		}
	}
	return user, t, port
}

// relaySSHConfig is the exit_servers row as the transport decision needs it.
type relaySSHConfig struct {
	SSHTarget   string
	TailscaleIP string
	SSHPort     string
	KeyPath     string
}

// lookupRelaySSHConfig reads the per-relay ssh configuration. A missing row is not
// an error: the ladder then only has the tailnet addresses from headscale and the
// node name, exactly like the pre-B310 code, and says so.
func lookupRelaySSHConfig(d *sql.DB, node string) relaySSHConfig {
	var cfg relaySSHConfig
	if d == nil || strings.TrimSpace(node) == "" {
		return cfg
	}
	err := d.QueryRow(`SELECT COALESCE(ssh_target, ''), COALESCE(tailscale_ip, ''), COALESCE(ssh_port, ''), COALESCE(ssh_key_path, '')
	                     FROM exit_servers WHERE hostname = $1 LIMIT 1`, node).
		Scan(&cfg.SSHTarget, &cfg.TailscaleIP, &cfg.SSHPort, &cfg.KeyPath)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("exit-node sync(%s): cannot read the ssh configuration from exit_servers: %v", node, err)
	}
	cfg.SSHTarget = strings.TrimSpace(cfg.SSHTarget)
	cfg.TailscaleIP = strings.TrimSpace(cfg.TailscaleIP)
	cfg.SSHPort = strings.TrimSpace(cfg.SSHPort)
	cfg.KeyPath = strings.TrimSpace(cfg.KeyPath)
	return cfg
}

// tailnetAddressesOf returns every tailnet address known for a relay: what
// headscale reports live (the authority) plus what the exit_servers row recorded
// (which still matters when headscale is unreachable).
func tailnetAddressesOf(hs *headscale.Client, cfg relaySSHConfig, node string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(ip string) {
		ip = strings.TrimSpace(ip)
		if ip == "" || seen[ip] {
			return
		}
		seen[ip] = true
		out = append(out, ip)
	}
	for _, ip := range liveExitNodeIPs(hs, node) {
		add(ip)
	}
	for _, raw := range strings.Split(cfg.TailscaleIP, ",") {
		if ip := parseOneTailscaleIP(raw); ip != "" {
			add(ip)
		}
	}
	return out
}

// parseOneTailscaleIP trims a stored address the way the DB layer does (the column
// holds a comma-joined list, IPv4 first).
func parseOneTailscaleIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if idx := strings.Index(raw, ","); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.Trim(raw, "[]")
}

// relaySSHEndpoints builds the ordered candidate list for one relay plus the notes
// explaining every path that is degraded.
//
// Order when skygate IS on the tailnet: tailnet (the path that survives a blocked
// public IP) → the operator's ssh_target → the bare node name.
// Order when it is NOT: the operator's ssh_target → tailnet → node name. The
// tailnet candidates stay in the list — a host can still reach 100.64.0.0/10 through
// a subnet router or a static route even without tailscaled — but they are tried
// last and the note says plainly why they are unlikely to answer, which is the
// difference between "we tried and named the reason" and pre-B310's silent timeout.
func relaySSHEndpoints(hs *headscale.Client, cfg relaySSHConfig, node string) (cands []RelayEndpoint, notes []string) {
	port := cfg.SSHPort
	user := ""
	if cfg.SSHTarget != "" {
		if u, _, p := splitSSHEndpoint(cfg.SSHTarget); p != "" {
			port = p
		} else if u != "" {
			user = u
		}
	}
	tailnetIPs := tailnetAddressesOf(hs, cfg, node)
	state := SkygateTailnetState()

	var tailnetCands, publicCands, nameCands []RelayEndpoint
	seen := map[string]bool{}
	addTailnet := func(u, host, p, why string) {
		if strings.TrimSpace(host) == "" || seen["t:"+host] {
			return
		}
		seen["t:"+host] = true
		tailnetCands = append(tailnetCands, RelayEndpoint{
			Kind: RelayEndpointTailnet, User: firstNonEmpty(u, user, "root"), Host: host, Port: p, Why: why,
		})
	}

	if cfg.SSHTarget != "" {
		u, host, p := splitSSHEndpoint(cfg.SSHTarget)
		if p == "" {
			p = cfg.SSHPort
		}
		if IsTailnetAddress(host) {
			addTailnet(u, host, p, "operator ssh_target (a tailnet address)")
		} else if !seen["p:"+host] {
			seen["p:"+host] = true
			publicCands = append(publicCands, RelayEndpoint{
				Kind: RelayEndpointPublic, User: firstNonEmpty(u, user, "root"), Host: host, Port: p, Why: "operator ssh_target",
			})
		}
	}
	for _, ip := range tailnetIPs {
		addTailnet("", ip, port, "headscale reports this tailnet address for the relay")
	}
	if strings.TrimSpace(node) != "" && !seen["p:"+node] && !seen["t:"+node] {
		nameCands = append(nameCands, RelayEndpoint{
			Kind: RelayEndpointName, User: firstNonEmpty(user, "root"), Host: node, Port: port,
			Why: "last resort: the node name (needs DNS)",
		})
	}

	if state.Ready {
		cands = append(cands, tailnetCands...)
		cands = append(cands, publicCands...)
	} else {
		if len(tailnetCands) > 0 {
			notes = append(notes, fmt.Sprintf(
				"tailnet path unavailable (%s): skygate itself is not on the tailnet, so %s is unlikely to answer — enable Tailscale on /admin/tailscale to manage exit nodes over the tailnet",
				state.Reason, strings.Join(tailnetHosts(tailnetCands), ", ")))
		}
		cands = append(cands, publicCands...)
		cands = append(cands, tailnetCands...)
	}
	cands = append(cands, nameCands...)
	return cands, notes
}

func tailnetHosts(cands []RelayEndpoint) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Host)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ProbeRelayEndpoint answers the one question a candidate list must answer before
// ssh runs: does anything answer on this address and port at all? A TCP handshake
// separates "the path is gone" (blocked, wrong network, node down) from "the path
// is there but ssh refused us" — two failures with completely different fixes.
func ProbeRelayEndpoint(ep RelayEndpoint, timeout time.Duration) error {
	port := strings.TrimSpace(ep.Port)
	if port == "" {
		port = "22"
	}
	addr := net.JoinHostPort(ep.Host, port)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// TailnetState answers "can skygate use the tailnet right now?".
type TailnetState struct {
	// Ready is true only when a usable KERNEL path exists: an interface carrying a
	// Tailscale address. A tailscaled running in userspace-networking mode cannot
	// carry a plain `ssh` to 100.64.0.0/10 (it needs a ProxyCommand), so it is
	// reported as not ready with that reason rather than as a silent timeout.
	Ready bool
	// IP is the local tailnet address when Ready.
	IP string
	// Iface is the interface that carries it.
	Iface string
	// Reason explains the verdict either way (used verbatim on the page).
	Reason string
}

var (
	tailnetStateMu  sync.Mutex
	tailnetStateAt  time.Time
	tailnetStateVal TailnetState
)

// tailnetStateTTL keeps the check off the per-relay hot path: the staggered sync
// walks every relay, and this is a process-wide fact.
var tailnetStateTTL = 15 * time.Second

// SkygateTailnetState reports whether skygate has a usable tailnet path (cached).
func SkygateTailnetState() TailnetState {
	tailnetStateMu.Lock()
	defer tailnetStateMu.Unlock()
	if !tailnetStateAt.IsZero() && time.Since(tailnetStateAt) < tailnetStateTTL {
		return tailnetStateVal
	}
	tailnetStateVal = detectTailnetState()
	tailnetStateAt = time.Now()
	return tailnetStateVal
}

// resetTailnetStateCache is used by tests and after the operator toggles Tailscale
// on /admin/tailscale (which drops the socket this check looks at).
func resetTailnetStateCache() {
	tailnetStateMu.Lock()
	tailnetStateAt = time.Time{}
	tailnetStateMu.Unlock()
}

// tailnetStateOverride lets tests pin "skygate is / is not on the tailnet" without
// depending on the interfaces of the machine running them.
var tailnetStateOverride *TailnetState

func detectTailnetState() TailnetState {
	if tailnetStateOverride != nil {
		return *tailnetStateOverride
	}
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, ifc := range ifaces {
			addrs, aerr := ifc.Addrs()
			if aerr != nil {
				continue
			}
			for _, a := range addrs {
				ipnet, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				ip := ipnet.IP
				if IsTailnetAddress(ip.String()) {
					return TailnetState{
						Ready:  true,
						IP:     ip.String(),
						Iface:  ifc.Name,
						Reason: fmt.Sprintf("skygate is on the tailnet as %s on %s", ip.String(), ifc.Name),
					}
				}
			}
		}
	}
	// No kernel path. Ask tailscale why, so the page can name the fix instead of
	// reporting "no interface found".
	reason := tailscaleBackendReason()
	// B318: the daemon's own answer is only half the story. Both admin pages used
	// to describe this state differently — /admin/tailscale rendered "enabled"
	// because a DB override pointed at a real key file while the CONTAINER ENV still
	// carried the disabled sentinel — so the shared facts are appended here and the
	// wording stays one story on both pages.
	if extra := tsstate.Detect("").Explain(); extra != "" {
		reason = reason + " — " + extra
	}
	return TailnetState{Ready: false, Reason: reason}
}

// tailscaleBackendReason turns `tailscale status --json` into one operator-facing
// sentence. Everything here is best effort: an absent binary, a stopped daemon and
// a logged-out node each get their own wording because each needs a different fix.
func tailscaleBackendReason() string {
	path, err := exec.LookPath("tailscale")
	if err != nil {
		return "no tailnet interface and no tailscale binary in this process — install/build the image with the tailscale client"
	}
	cmd := exec.Command(path, "status", "--json")
	cmd.Env = append(cmd.Environ(), "TAILSCALE_BE_CLI=1")
	done := make(chan struct{})
	var out []byte
	var runErr error
	go func() {
		out, runErr = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		return "tailscaled did not answer within 3s (the daemon is hung or the socket is stale)"
	}
	if runErr != nil && len(out) == 0 {
		msg := strings.TrimSpace(runErr.Error())
		if ee, ok := runErr.(*exec.ExitError); ok {
			if txt := strings.TrimSpace(string(ee.Stderr)); txt != "" {
				msg = txt
			}
		}
		return "tailscaled is not running: " + firstLine(msg)
	}
	var st struct {
		BackendState string
		Self         struct {
			TailscaleIPs []string
		}
	}
	if jerr := json.Unmarshal(out, &st); jerr != nil {
		return "tailscaled answered something unreadable: " + firstLine(string(out))
	}
	switch st.BackendState {
	case "Running":
		ips := strings.Join(st.Self.TailscaleIPs, ", ")
		return fmt.Sprintf("tailscaled reports Running (%s) but no interface carries a tailnet address — userspace-networking mode cannot carry a plain ssh; run the container with /dev/net/tun (NET_ADMIN), or use the public target", firstNonEmpty(ips, "no addresses"))
	case "NeedsLogin":
		return "tailscaled is running and NOT logged in (NeedsLogin) — generate an auth key and enable Tailscale on /admin/tailscale"
	case "Stopped":
		return "tailscaled is running but the backend is Stopped — enable Tailscale on /admin/tailscale"
	case "NoState":
		return "tailscaled has no state yet — enable Tailscale on /admin/tailscale"
	default:
		if st.BackendState == "" {
			return "tailscaled answered without a backend state: " + firstLine(string(out))
		}
		return "tailscaled backend state is " + st.BackendState
	}
}

// sshLadderResult is the outcome of trying every candidate.
type sshLadderResult struct {
	OK       bool
	Via      string
	Endpoint string
	Output   string
	// Attempts is the per-candidate failure list, in order.
	Attempts []string
	// Notes carries the paths that were never candidates (the tailnet path when
	// skygate is not on the tailnet) so a failure explains the whole picture.
	Notes []string
}

// applyRoutesOverSSHLadder places the advertised routes on ONE relay over the first
// candidate that works: TCP probe, then the real ssh command. Every attempt is
// logged, and a total failure names each candidate with its own reason — the
// pre-B310 message named exactly one target, which is why "skygate is not on the
// tailnet" was invisible for so long.
func applyRoutesOverSSHLadder(hs *headscale.Client, node string, routes []string, acceptRoutes int, keyPath string, cands []RelayEndpoint, notes []string) sshLadderResult {
	res := sshLadderResult{Notes: notes}
	if len(cands) == 0 {
		res.Attempts = append(res.Attempts, "no usable transport is configured for this relay (no tailnet address, no ssh_target)")
		return res
	}
	for _, ep := range cands {
		if err := ProbeRelayEndpoint(ep, relayProbeTimeout); err != nil {
			res.Attempts = append(res.Attempts, ep.Label()+" is not answering: "+err.Error())
			log.Printf("exit-node sync(%s): %s is not answering (%v) — trying the next transport", node, ep.Label(), err)
			continue
		}
		out, err := hs.SetAdvertisedRoutes(node, routes, acceptRoutes, ep.Target(), keyPath)
		if err != nil {
			res.Attempts = append(res.Attempts, ep.Label()+": "+err.Error())
			log.Printf("exit-node sync(%s): %s answered but the routes could not be applied: %v — trying the next transport", node, ep.Label(), err)
			continue
		}
		res.OK = true
		res.Via = ep.Kind
		res.Endpoint = ep.Label()
		res.Output = out
		log.Printf("exit-node sync(%s): routes applied over the %s transport (%s; %s)", node, ep.Kind, ep.Label(), ep.Why)
		return res
	}
	return res
}
