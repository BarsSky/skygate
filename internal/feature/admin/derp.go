package admin

// derp.go — DERP (relay) admin HTTP handlers + the DERP data types
// they pass to templates. The data fetching / parsing / classifying
// logic lives in the same file (was split across 3 files in
// internal/handlers/ — combined here since the feature is one unit).
//
//   - GetAdminDERP / GetAdminDERPRefresh  — HTTP entry points
//   - DerpStatus / DerpPeer / ConnSummary / DerpSnapshot  — view types
//   - collectDerpStatus                   — orchestrator
//   - httpGet / parseDerperDebugHTML / parseDerperVars
//   - classifyDerpPeer(s) / summarizeDerpPeers  — pure helpers
//
// refactor-v0.30 Phase B step 6a (2026-07-29): moved from
// internal/handlers/ (3 files: handlers_derp.go, handlers_derp_collect.go,
// handlers_derp_classify.go) into this single feature file. The split
// in handlers/ was an Этап 8 size-organization choice; the feature is
// small enough (~430 lines) to keep in one place.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// ---------- HTTP entry points ----------

// GetAdminDERP renders the /admin/derp page from a freshly collected
// DerpStatus snapshot.
func (s *Service) GetAdminDERP(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.Backend.RenderWithLayout(w, r, "admin/derp.html", c, map[string]any{
		"DerpStatus": s.collectDerpStatus(),
	})
}

// GetAdminDERPRefresh forces a refresh - same page.
func (s *Service) GetAdminDERPRefresh(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/derp", http.StatusFound)
}

// ---------- DERP TYPES ----------

// DerpStatus describes the local custom DERP relay (derper) for /admin/derp.
type DerpStatus struct {
	Running         bool
	SocketListening bool
	STUNListening   bool
	DERPPort        string
	STUNPort        string
	Version         string
	Hostname        string
	RegionCode      string
	RegionID        string
	RegionName      string
	WhiteIP         string
	// WhiteIPSource records WHERE the WhiteIP came from:
	// "dns" (net.LookupHost of the derper's hostname — the
	// public IP Tailscale clients actually dial), "egress"
	// (detectEgressIP — the skygate container's own egress
	// interface, usually wrong but better than empty),
	// "derper_status" (parsed from the derper's own /debug
	// HTML — the derper's view of the WhiteIP, which equals
	// the source IP of the request — usually 172.18.0.x
	// when querying from the skygate container). Used by
	// the /admin/derp template to show a small annotation
	// so the operator knows which IP they're looking at.
	WhiteIPSource   string
	UpTime          string
	StartedAt       string
	PID             string
	Memory          string
	GoVersion       string
	Machine         string
	Connections     int
	Accepts         int
	BytesIn         int64
	BytesOut        int64
	PacketsIn       int
	PacketsOut      int
	Clients         int
	STUNRequests    int
	RecentLog       string

	// Active connections to derper (src IP, reverse DNS).
	ActiveTCP []DerpPeer
	ActiveUDP []DerpPeer
	// ConnSummary aggregates ActiveTCP+ActiveUDP by kind for the hero badges.
	ConnSummary *ConnSummary
	// Snapshot history tail (parsed recent records).
	Snapshot []DerpSnapshot
}

// DerpPeer is one observed peer connecting to derper.
type DerpPeer struct {
	IP   string `json:"ip"`
	Host string `json:"host"`
	Port string `json:"port"`
	// Kind classifies the source: ws_relay (Tailscale client),
	// ws_admin (NPM WebSocket pool), lan, internet, unknown.
	Kind string `json:"kind,omitempty"`
}

// ConnSummary aggregates connections by kind for the dashboard hero badges.
type ConnSummary struct {
	Relay int
	Admin int
	LAN   int
	Self  int
	Other int
}

// DerpSnapshot is one entry from the rolling snapshot log on the agent.
type DerpSnapshot struct {
	TS      string                 `json:"ts"`
	Conns   []DerpPeer             `json:"conns"`
	Metrics map[string]interface{} `json:"metrics"`
	Summary *ConnSummary           `json:"summary,omitempty"`
}

// CurrentConns returns the connection count recorded by this snapshot,
// trying both naming conventions the agent has used over time.
func (s *DerpSnapshot) CurrentConns() int {
	if s == nil {
		return 0
	}
	for _, key := range []string{"gauge_current_connections", "current_conns"} {
		if v, ok := s.Metrics[key]; ok {
			switch n := v.(type) {
			case float64:
				return int(n)
			case int:
				return n
			case int64:
				return int(n)
			}
		}
	}
	return 0
}

// ---------- collect / fetch ----------

// collectDerpStatus is the orchestrator: it seeds a DerpStatus with the
// known derper config, then hits each of the 6 debug endpoints in turn,
// enriching the struct. Order matters: /debug/ and /debug/vars come
// first so Running/STUNListening are decided before /active-conn and
// /all-recent paint their data.
func (s *Service) collectDerpStatus() DerpStatus {
	// DERP server runs on the host (not in the skygate container), so
	// systemctl/ss from inside the container can't see it. Instead we
	// query the derper's own debug endpoint at 192.0.2.1:8443/debug/
	// which is reachable from the container via the host bridge.
	st := DerpStatus{
		DERPPort:   "443",
		STUNPort:   "3478",
		Version:    "1.70.0",
		Hostname:   "derp.example.com",
		RegionCode: "mow",
		RegionID:   "900",
		RegionName: "Moscow Custom",
		// WhiteIP is filled in by parseDerperDebugHTML when reachable,
		// or by the best-effort outbound-iface probe at the end of
		// collectDerpStatus. Left empty here (not hardcoded) to avoid
		// leaking operator-specific egress IPs into the binary.
		WhiteIP: "",
		// B237.2: WhiteIPSource defaults to "" (no IP yet
		// resolved); the orchestrator fills it in below.
		WhiteIPSource: "",
	}

	// Try derper debug endpoints (in priority order)
	derpURL := "http://192.0.2.1:8443"
	if v := s.DerpBaseURL; v != "" {
		derpURL = v
	}

	// 1. /debug/  -> HTML, contains Uptime, Version, etc.
	if html, err := httpGet(derpURL+"/debug/", 3*time.Second); err == nil {
		parseDerperDebugHTML(&st, html)
	}

	// 2. /debug/vars -> JSON, real metrics
	if body, err := httpGet(derpURL+"/debug/vars", 3*time.Second); err == nil {
		parseDerperVars(&st, body)
	}

	// 3. Plain / -> quick liveness check
	if _, err := httpGet(derpURL+"/", 3*time.Second); err == nil {
		st.SocketListening = true
	}

	// 4. STUN UDP check (skygate is in container; check via long TCP probe is misleading).
	//    We trust the derper stats: if stun.counter_requests > 0, STUN is alive.
	if body, err := httpGet(derpURL+"/debug/vars", 3*time.Second); err == nil {
		var j struct {
			STUN struct {
				CounterRequests struct {
					Success int `json:"success"`
				} `json:"counter_requests"`
			} `json:"stun"`
		}
		if json.Unmarshal(body, &j) == nil && j.STUN.CounterRequests.Success > 0 {
			st.STUNListening = true
		}
	}

	// 5. Active connections (current TCP/UDP peers with reverse DNS)
	if body, err := httpGet(derpURL+"/active-conn", 3*time.Second); err == nil {
		var ac struct {
			TCP     []DerpPeer `json:"tcp"`
			UDPSTUN []DerpPeer `json:"udp_stun"`
		}
		if json.Unmarshal(body, &ac) == nil {
			st.ActiveTCP = classifyDerpPeers(ac.TCP)
			st.ActiveUDP = classifyDerpPeers(ac.UDPSTUN)
			st.ConnSummary = summarizeDerpPeers(append(append([]DerpPeer{}, st.ActiveTCP...), st.ActiveUDP...))
		}
	}

	// 6. Snapshot history (last 30 records from /var/log/derper-snapshot.log)
	if body, err := httpGet(derpURL+"/all-recent", 3*time.Second); err == nil {
		lines := strings.Split(string(body), "\n")
		start := 0
		if len(lines) > 30 {
			start = len(lines) - 30
		}
		for _, line := range lines[start:] {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var snap DerpSnapshot
			if json.Unmarshal([]byte(line), &snap) == nil {
				// Apply classification to each conn (snapshot script
				// in v0.3.4+ already includes kind, but be defensive
				// about older entries that don't).
				snap.Conns = classifyDerpPeers(snap.Conns)
				snap.Summary = summarizeDerpPeers(snap.Conns)
				st.Snapshot = append(st.Snapshot, snap)
			}
		}
	}

	// Hostname (white IP) — the public IP Tailscale clients dial.
	// B237.2: prefer DNS lookup of the derper's hostname (the
	// source of truth for "where clients reach us"). The
	// skygate container's own egress IP (was the pre-B237.2
	// behaviour via detectEgressIP) is usually 172.18.0.x on
	// the docker bridge — misleading on /admin/derp because
	// that IP is unreachable from the public internet.
	// Resolution order:
	//   1. SKYGATE_DERP_HOSTNAME env var (the operator's
	//      configured DERP hostname) + net.LookupHost
	//   2. The derper status page's "TLS hostname" field
	//      (already parsed into st.Hostname above)
	//   3. Last resort: detectEgressIP() (skygate container's
	//      own egress — usually wrong, but better than empty)
	if st.WhiteIP == "" {
		if ip, src, ok := resolvePublicDERPIP(st.Hostname); ok {
			st.WhiteIP = ip
			st.WhiteIPSource = src
		}
	}

	return st
}

// detectEgressIP returns the outbound IPv4 of this process by dialing a
// discard socket and reading the local address. Best-effort: returns "" on
// any error. Used as a fallback when the derper debug endpoint is unreachable
// and we still want to show a "White IP" hint on the DERP status page.
func detectEgressIP() (string, error) {
	conn, err := net.DialTimeout("udp", "192.0.2.1:80", 2*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local == nil {
		return "", fmt.Errorf("no local addr")
	}
	ip := local.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("no ipv4 addr")
	}
	return ip.String(), nil
}

// resolvePublicDERPIP returns the public IP that Tailscale
// clients actually use to reach the derper. Three sources
// tried in order:
//
//  1. SKYGATE_DERP_HOSTNAME env var. The operator's
//     configured DERP hostname (e.g. "derp.skynas.ru").
//     We DNS-resolve it via net.LookupHost — the A record
//     is the operator's authoritative answer for "what
//     public IP does this DERP live at?". This is the
//     only source that's reachable even when skygate's
//     own derper is on a different host (the typical
//     setup — skygate runs in a container, derper runs
//     as systemd unit on the host).
//
//  2. The `derperHostname` parameter (parsed from
//     st.Hostname, the TLS hostname field of the derper's
//     own /debug HTML). Same DNS-lookup logic; useful as
//     a fallback when SKYGATE_DERP_HOSTNAME is not set.
//
//  3. detectEgressIP() — the skygate container's own
//     egress interface. Returns the source IP of a UDP
//     dial. This is the OLD pre-B237.2 behaviour. It
//     usually returns the docker-bridge IP (172.18.0.x)
//     when skygate is containerised, which is NOT the
//     public IP and misleading on the /admin/derp page.
//     Kept as a last-resort fallback so the field is
//     never empty.
//
// Returns (ip, source, ok). `source` is one of
// "dns:env" / "dns:derper" / "egress" so the template
// can show a small annotation.
//
// B237.2 — closes the "derper status page shows
// 172.18.0.3 as the public IP" gap (operator's 2026-09-04
// report: "на скрине указан неверный ip адрес (он
// относится к контейнеру, а не публичному адресу
// ресурса)").
func resolvePublicDERPIP(derperHostname string) (ip, source string, ok bool) {
	candidates := []struct {
		hostname string
		label    string
	}{}
	if env := strings.TrimSpace(os.Getenv("SKYGATE_DERP_HOSTNAME")); env != "" {
		candidates = append(candidates, struct {
			hostname string
			label    string
		}{env, "dns:env"})
	}
	if h := strings.TrimSpace(derperHostname); h != "" && h != "derp.example.com" {
		// Skip the placeholder value from the seed
		// (the collectDerpStatus function seeds the
		// struct with "derp.example.com" when the
		// derper's /debug endpoint is unreachable).
		candidates = append(candidates, struct {
			hostname string
			label    string
		}{h, "dns:derper"})
	}
	for _, c := range candidates {
		if resolved, err := net.LookupHost(c.hostname); err == nil && len(resolved) > 0 {
			// Pick the first IPv4 A record (Tailscale
			// clients dial IPv4 by default; IPv6
			// would also work but the /admin/derp
			// page can't display both cleanly).
			for _, addr := range resolved {
				ip := net.ParseIP(addr).To4()
				if ip != nil {
					return ip.String(), c.label, true
				}
			}
			// Only IPv6 — return the first one.
			return resolved[0], c.label + " (v6)", true
		}
	}
	// Last resort: skygate container's own egress.
	if egress, err := detectEgressIP(); err == nil {
		return egress, "egress", true
	}
	return "", "", false
}

func httpGet(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// derper checks Host header against its TLS hostname. When we
	// query it over plain HTTP from inside the skygate container (to
	// 192.0.2.1:8443) we must present the public hostname, otherwise
	// /debug/ returns http.StatusForbidden Forbidden.
	req.Host = "derper.example.com"
	req.Header.Set("Host", "derper.example.com")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// parseDerperDebugHTML extracts Uptime, Version, TLS hostname, machine from the
// derper /debug/ HTML page.
func parseDerperDebugHTML(st *DerpStatus, html []byte) {
	text := string(html)
	if m := regexp.MustCompile(`Uptime:</b>\s*([^<]+)`).FindStringSubmatch(text); len(m) > 1 {
		st.UpTime = strings.TrimSpace(m[1])
	}
	if m := regexp.MustCompile(`Version:</b>\s*([^<]+)`).FindStringSubmatch(text); len(m) > 1 {
		v := strings.TrimSpace(m[1])
		// strip "-ERR-BuildInfo" suffix
		if i := strings.Index(v, "-ERR-"); i > 0 {
			v = v[:i]
		}
		st.Version = v
	}
	if m := regexp.MustCompile(`TLS hostname:</b>\s*([^<]+)`).FindStringSubmatch(text); len(m) > 1 {
		st.Hostname = strings.TrimSpace(m[1])
	}
	if m := regexp.MustCompile(`Machine:</b>\s*([^<]+)`).FindStringSubmatch(text); len(m) > 1 {
		st.Machine = strings.TrimSpace(m[1])
	}
}

// parseDerperVars pulls metrics out of /debug/vars JSON.
func parseDerperVars(st *DerpStatus, body []byte) {
	var v struct {
		ProcessStartUnixTime float64 `json:"process_start_unix_time"`
		DERP                 struct {
			Accepts            int   `json:"accepts"`
			BytesReceived      int64 `json:"bytes_received"`
			BytesSent          int64 `json:"bytes_sent"`
			CurrentConnections int   `json:"gauge_current_connections"`
			CurrentHomeConns   int   `json:"gauge_current_home_connections"`
			ClientsTotal       int   `json:"gauge_clients_total"`
			ClientsLocal       int   `json:"gauge_clients_local"`
			PacketsReceived    int   `json:"packets_received"`
			PacketsSent        int   `json:"packets_sent"`
			PacketsDropped     int   `json:"packets_dropped"`
		} `json:"derp"`
		STUN struct {
			CounterRequests struct {
				Success int `json:"success"`
			} `json:"counter_requests"`
		} `json:"stun"`
		GoSyncMutexWaitSeconds float64 `json:"go_sync_mutex_wait_seconds"`
		GoVersion              string  `json:"go_version"`
		Memstats               struct {
			Alloc uint64 `json:"Alloc"`
			Sys   uint64 `json:"Sys"`
			NumGC uint32 `json:"NumGC"`
		} `json:"memstats"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return
	}
	// Memory in MB
	if v.Memstats.Alloc > 0 {
		st.Memory = fmt.Sprintf("%.1f MB heap", float64(v.Memstats.Alloc)/1024/1024)
	}
	// Stash extra metrics in extra fields via concat
	st.Connections = v.DERP.CurrentConnections
	st.Accepts = v.DERP.Accepts
	st.BytesIn = v.DERP.BytesReceived
	st.BytesOut = v.DERP.BytesSent
	st.PacketsIn = v.DERP.PacketsReceived
	st.PacketsOut = v.DERP.PacketsSent
	st.Clients = v.DERP.ClientsTotal
	st.STUNRequests = v.STUN.CounterRequests.Success
	// Derive started-at from process_start_unix_time
	if v.ProcessStartUnixTime > 0 {
		st.StartedAt = time.Unix(int64(v.ProcessStartUnixTime), 0).Format("2006-01-02 15:04:05 MST")
		// Recompute uptime if we got it from vars
		d := time.Since(time.Unix(int64(v.ProcessStartUnixTime), 0)).Round(time.Second)
		if st.UpTime == "" || st.UpTime == "n/a" {
			st.UpTime = d.String()
		}
	}
	// Go version
	if v.GoVersion != "" {
		st.GoVersion = v.GoVersion
	}
	// If we got DERP responses, it's running
	if v.DERP.Accepts >= 0 {
		st.Running = true
	}
	if v.STUN.CounterRequests.Success > 0 {
		st.STUNListening = true
	}
}

// ---------- classify / summarize ----------

// derpTailscaleNet is the Tailscale IP range (100.64.100.0/10).
// Used to classify DERP peer connections as "ws_relay"
// (Tailscale client) vs "ws_admin" (NPM) vs "lan" (other
// LAN clients). The 100.64.100.0/10 range is a Tailscale
// standard and is NOT operator-specific.
var derpTailscaleNet = net.IPNet{IP: net.ParseIP("100.64.100.0").To4(), Mask: net.CIDRMask(10, 32)}

// derpPeerNPM and derpLANNet are read from config at
// initialization time (via initDerpClassifier) so the
// operator's NPM address and LAN CIDR stay in .env
// rather than being hardcoded here. The defaults
// (192.0.2.x, RFC 5737) are documentation IPs that
// never match real traffic — operators MUST set
// SKYGATE_DERP_PEER_NPM and SKYGATE_DERP_LAN_NET in
// .env for the /admin/derp classifier to label their
// own traffic correctly.
var (
	derpPeerNPM = "192.0.2.67"
	derpLANNet  = net.IPNet{IP: net.ParseIP("192.0.2.0").To4(), Mask: net.CIDRMask(24, 32)}
)

// InitDerpClassifier applies env-driven overrides at
// startup. Called from main.go after config.Load().
// Exposed as a public function so cmd/skygate can wire it
// without an import cycle.
func InitDerpClassifier(npm, lanNet string) error {
	if npm != "" {
		derpPeerNPM = npm
	}
	if lanNet != "" {
		_, ipnet, err := net.ParseCIDR(lanNet)
		if err != nil {
			return fmt.Errorf("SKYGATE_DERP_LAN_NET: %w", err)
		}
		// Ensure it's an IPv4 net (4-byte form) so
		// .Contains() works against net.ParseIP output.
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			ipnet.IP = ip4
		}
		derpLANNet = *ipnet
	}
	return nil
}

// classifyDerpPeer labels a connection source.
//   ws_relay - Tailscale client (100.64.100.0/10)
//   ws_admin - Nginx Proxy Manager WebSocket pool (SKYGATE_DERP_PEER_NPM)
//   lan      - other LAN client (SKYGATE_DERP_LAN_NET)
//   local    - loopback (already filtered by the snapshot script)
//   unknown  - anything else
func classifyDerpPeer(ip string) string {
	if ip == derpPeerNPM {
		return "ws_admin"
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "unknown"
	}
	if parsed.IsLoopback() {
		return "local"
	}
	if derpTailscaleNet.Contains(parsed) {
		return "ws_relay"
	}
	if derpLANNet.Contains(parsed) {
		return "lan"
	}
	if !parsed.IsPrivate() {
		return "ws_relay"
	}
	return "unknown"
}

// classifyDerpPeers fills the Kind field in-place; returns the same slice
// for chaining.
func classifyDerpPeers(peers []DerpPeer) []DerpPeer {
	for i := range peers {
		if peers[i].Kind == "" {
			peers[i].Kind = classifyDerpPeer(peers[i].IP)
		}
	}
	return peers
}

// summarizeDerpPeers counts connections per kind for the dashboard hero.
// Always returns a non-nil pointer so the template can check per-kind
// counts and decide whether to show "derper: N conn (transient)" when
// ss sees zero connections but derper reports some.
func summarizeDerpPeers(peers []DerpPeer) *ConnSummary {
	s := &ConnSummary{}
	for _, p := range peers {
		switch p.Kind {
		case "ws_relay":
			s.Relay++
		case "ws_admin":
			s.Admin++
		case "lan":
			s.LAN++
		case "self":
			s.Self++
		default:
			s.Other++
		}
	}
	return s
}
