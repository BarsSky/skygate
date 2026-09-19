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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	neturl "net/url"
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
	// B252.1: the cert auto-renewal section needs the derp_cert_sync rows.
	// A DB error here must NOT take the whole /admin/derp page down (the
	// page's primary job is the derp status snapshot), so it degrades to an
	// empty list plus a log line.
	var certSync []DerpCertSyncConfig
	if db := s.dbc(); db != nil {
		if rows, err := loadAllCertSyncConfigs(r.Context(), db); err != nil {
			log.Printf("admin.derp: load cert-sync configs: %v", err)
		} else {
			certSync = rows
		}
	}
	// Flash from the "Sync now" POST (redirect + ?ok=/?err=; never a JSON
	// body — see the B180 raw-JSON regression).
	flash, flashErr := r.URL.Query().Get("ok"), r.URL.Query().Get("err")
	s.Backend.RenderWithLayout(w, r, "admin/derp.html", c, map[string]any{
		"DerpStatus": s.collectDerpStatus(),
		"CertSync":   certSync,
		"FlashOk":    flash,
		"FlashErr":   flashErr,
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
	// B265 — real UDP/STUN reachability. STUNListening is now set
	// by an actual STUN Binding Request round trip (derp_stun.go)
	// instead of by reading derper's /debug/vars counter, which is
	// HTTP 403 for any non-loopback/non-Tailscale source (i.e. the
	// skygate container in every deployment). STUNBlocked is true
	// when the round trip failed, so the page can distinguish
	// "probed and dead" from "not probed".
	STUNBlocked  bool
	STUNRTT      string
	STUNReflex   string
	STUNErr      string
	// DebugAccessDenied records that derper answered /debug/* with
	// 403 "debug access denied" (upstream tsweb.AllowDebugAccess
	// rejects the container's source IP). The rich metrics
	// (connections, bytes, clients, STUN counters) are unavailable
	// in that case and the page says so instead of rendering zeros
	// as if they were measurements.
	DebugAccessDenied bool
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
	WhiteIPSource string
	UpTime        string
	StartedAt     string
	PID           string
	Memory        string
	GoVersion     string
	Machine       string
	Connections   int
	Accepts       int
	BytesIn       int64
	BytesOut      int64
	PacketsIn     int
	PacketsOut    int
	Clients       int
	STUNRequests  int
	RecentLog     string

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
	// query the derper's own debug endpoint over HTTPS, using the
	// hostname the bundled derp_relays row has registered (cert CN
	// matches that hostname).
	//
	// 2026-09-15 (B-bug-fix): DERPPort / STUNPort were hardcoded
	// "443" / "3478" which silently masked a broken derper running
	// on :8443 (the operator had NPM terminating TLS on :443). See
	// derp_status_resolve.go for the resolution order.
	//
	// 2026-09-17 (B260): the pre-fix derpURL was hardcoded to
	// "http://192.0.2.1:8443" — 192.0.2.1 is RFC 5737 TEST-NET-1
	// (not routable) and the scheme was plain HTTP (derper on :443
	// requires TLS post-B-derper-cert). All 6 derper debug probes
	// silently failed, so /admin/derp always showed "stopped"
	// regardless of derper's actual state. B260 builds the URL
	// from the bundled row's hostname + port + the https:// scheme,
	// with TLS SNI matching the cert CN.
	derpPort := resolveDERPPort(s.dbc())
	if derpPort == "" {
		derpPort = "443"
	}
	derpHost := resolveDERPHostname(s.dbc())
	if derpHost == "" {
		derpHost = "127.0.0.1" // probe loopback; fails cleanly if no derper
	}
	stunPort := resolveSTUNPort(s.dbc())
	if stunPort == "" {
		stunPort = "3478"
	}
	st := DerpStatus{
		DERPPort:   derpPort,
		STUNPort:   stunPort,
		Version:    "1.70.0",
		Hostname:   derpHost,
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
	//
	// B260: scheme is https (post-B-derper-cert derper on :443
	// speaks TLS). The hostname comes from the bundled row so
	// SNI=cert CN.
	//
	// B260.2.4 (2026-09-17): removed the `s.DerpBaseURL` probe
	// override. The `SKYGATE_DERP_BASE_URL` env var is the
	// derpmap-fetcher URL (the URL the auto-sync in
	// /admin/derp/sync polls, pointing at the docker derpmap
	// container's HTTP server, default port 8766) — NOT the
	// derper URL. The pre-B260.2.4 code conflated them and the
	// probe ended up hitting the derpmap container's JSON
	// endpoint (8766) instead of the derper's TLS endpoint
	// (443), so /admin/derp always rendered "DERPER-SERVICE:
	// stopped" despite derper being up. Tests + non-standard
	// deployments now override the probe by seeding a custom
	// bundled row in the test fixture, not by env var.
	derpURL := "https://" + derpHost + ":" + derpPort

	// 1. /debug/  -> HTML, contains Uptime, Version, etc.
	if html, err := httpGet(derpURL+"/debug/", 3*time.Second); err == nil {
		parseDerperDebugHTML(&st, html)
	}

	// 2. /debug/vars -> JSON, real metrics
	if body, err := httpGet(derpURL+"/debug/vars", 3*time.Second); err == nil {
		if isDebugAccessDenied(body) {
			st.DebugAccessDenied = true
		} else {
			parseDerperVars(&st, body)
		}
	}

	// 3. Plain / -> quick liveness check
	if _, err := httpGet(derpURL+"/", 3*time.Second); err == nil {
		st.SocketListening = true
	}

	// 4. STUN UDP check (B265).
	//    Pre-B265 this step read `stun.counter_requests.success`
	//    from /debug/vars — the same URL step 2 already failed on
	//    (403 "debug access denied" for the container's source
	//    IP). The tile therefore rendered "closed" (red) on every
	//    deployment where the operator hardened derper by leaving
	//    --debug off, even though STUN was healthy. B265 replaces
	//    the counter-read with a real RFC 5389 Binding Request
	//    round trip over UDP — the same packet Tailscale clients
	//    use to score a relay for home-DERP selection, so the tile
	//    now means "clients on this path can reach STUN".
	if !st.STUNListening {
		probeSTUNForStatus(&st, s.dbc(), derpHost, stunPort)
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

	// 7. WebSocket upgrade fallback for the "Running" boolean. Steps
	//    1, 2, 4, 5, 6 all gate on /debug/* endpoints which are
	//    per-design disabled when derper is launched without the
	//    `--debug` flag (the operator's choice for prod hardening —
	//    /active-conn and /all-recent leak client connection info).
	//    For those deployments we can't tell from /debug/vars whether
	//    derper is running. The WebSocket upgrade probe is the
	//    canonical derper liveness check that BOTH:
	//    - works regardless of derper's --debug config (the
	//      `/derp` WebSocket endpoint is always-on for any
	//      functional derper — it's what Tailscale clients dial),
	//    - proves derper is actively serving (a derper that hasn't
	//      finished initialization won't return 101).
	//    We do this AFTER the richer /debug/* probes so the rich
	//    metrics win when available; this is just a safety net
	//    for the no-derper-debug deployment case.
	if !st.Running {
		if isRunning, err := derperLivenessWebSocketProbe(derpURL, 3*time.Second); err == nil && isRunning {
			st.Running = true
			// The / probe already set SocketListening; with debug
			// disabled we don't have STUN/Connections/Bytes — those
			// stay at zero, which is honest.
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
		// B260.2.5 (2026-09-17): replace the net.Resolver +
		// custom-Dial approach from B260.2.4 with a raw UDP
		// DNS query against 1.1.1.1. The B260.2.4 custom
		// resolver silently fell back to the OS resolver
		// chain (Go's net.Resolver.LookupHost on Linux
		// honours /etc/nsswitch.conf + systemd-resolved +
		// the Docker `extra_hosts` block even with custom
		// Dial — verified live on VM 2026-09-17 16:21 MSK:
		// the page rendered "192.168.13.69 (dns:env)"
		// instead of "95.165.170.190"). A raw UDP DNS query
		// sidesteps the entire OS resolver chain — we
		// speak DNS protocol directly to 1.1.1.1:53 and
		// parse the response.
		if ips, err := dnsLookupVia1111(c.hostname); err == nil && len(ips) > 0 {
			for _, ip := range ips {
				if v4 := ip.To4(); v4 != nil {
					return v4.String(), c.label, true
				}
			}
			// Only IPv6 in the response — return the first.
			return ips[0].String(), c.label + " (v6)", true
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
	// B260: HTTP probe for /admin/derp status. The previous
	// version hard-coded `req.Host = "derper.example.com"` (a
	// literal placeholder that doesn't match any real
	// deployment) and only supported plain HTTP. Post-B-derper-cert
	// the bundled derper listens on :443 with TLS, so we need
	// to (a) strip the port from the Host header (TLS servers
	// check Host against the cert SNI), and (b) handle the
	// https:// scheme with a TLS-enabled transport.
	//
	// When the URL host is an IP (rare — only when the operator
	// hasn't set SKYGATE_DERP_HOSTNAME / bundled hostname),
	// we can't verify the cert's hostname and use
	// InsecureSkipVerify=true. This is acceptable for a status
	// probe (NOT for production traffic) because:
	//   - the operator has separate observability for cert
	//     health (NPM renewal alerts, derper systemd logs);
	//   - the TLS handshake itself still proves the server
	//     is reachable and speaking TLS;
	//   - the response body is JSON/HTML parsed by trusted
	//     helpers in this file, not user-supplied data.
	u, parseErr := neturl.Parse(url)
	if parseErr != nil {
		return nil, parseErr
	}
	hostnameOnly := u.Hostname() // strips :port for Host header + SNI
	if hostnameOnly != "" {
		req.Host = hostnameOnly
		req.Header.Set("Host", hostnameOnly)
	}
	if u.Scheme == "https" {
		skipVerify := false
		if net.ParseIP(hostnameOnly) != nil {
			// URL host is a literal IP — cert CN mismatch
			// is unavoidable; InsecureSkipVerify is the only
			// way to complete the TLS handshake.
			skipVerify = true
		}
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: skipVerify,
				ServerName:         hostnameOnly,
			},
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// derperLivenessWebSocketProbe does a minimal WebSocket upgrade
// request against derper's `/derp` endpoint and returns true iff
// derper responds with HTTP 101 Switching Protocols (the canonical
// "I'm ready to speak the DERP protocol" signal).
//
// This is the fallback used by collectDerpStatus when /debug/*
// endpoints are disabled (the operator's deliberate choice for
// prod hardening — /active-conn + /all-recent leak client
// connection info). The WebSocket upgrade endpoint is always on
// for any functional derper (it's what Tailscale clients dial),
// so this probe is the most robust liveness check we can do
// without touching derper's debug config.
//
// B260.1 (2026-09-17): added because the operator runs derper
// without --debug, and parseDerperVars (which gates `Running`)
// returns early on the 403 JSON-parse failure from /debug/vars.
// Pre-B260.1 the /admin/derp page always showed "DERPER-SERVICE:
// stopped" even when derper was up.
func derperLivenessWebSocketProbe(rawURL string, timeout time.Duration) (bool, error) {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return false, err
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", u.String()+"/derp", nil)
	if err != nil {
		return false, err
	}
	// Strip :port for Host header + SNI matching the cert CN.
	hostnameOnly := u.Hostname()
	if hostnameOnly != "" {
		req.Host = hostnameOnly
		req.Header.Set("Host", hostnameOnly)
	}
	// Minimal WebSocket upgrade headers — these are different from
	// `Sec-WebSocket-Key` + `Sec-WebSocket-Version` required by RFC
	// 6455, but derper doesn't actually complete a handshake — it
	// just checks `Upgrade: websocket` and returns 101 Switching
	// Protocols to signal "ready". We don't send frames so we
	// never need the Sec-WebSocket-* headers.
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	if u.Scheme == "https" {
		skipVerify := false
		if net.ParseIP(hostnameOnly) != nil {
			skipVerify = true
		}
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: skipVerify,
				ServerName:         hostnameOnly,
			},
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	// B260.1: 101 Switching Protocols is the canonical "derper is
	// alive and ready for DERP frames" response. We don't drain
	// the body because derper starts streaming immediately and
	// the deadline is the http.Client.Timeout.
	return resp.StatusCode == http.StatusSwitchingProtocols, nil
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
//
//	ws_relay - Tailscale client (100.64.100.0/10)
//	ws_admin - Nginx Proxy Manager WebSocket pool (SKYGATE_DERP_PEER_NPM)
//	lan      - other LAN client (SKYGATE_DERP_LAN_NET)
//	local    - loopback (already filtered by the snapshot script)
//	unknown  - anything else
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

// dnsLookupVia1111 performs a raw UDP DNS A-record query for
// `hostname` against Cloudflare's 1.1.1.1 public resolver.
// Returns the list of A-record IPs.
//
// B260.2.5 (2026-09-17): the B260.2.4 net.Resolver + custom Dial
// approach was not enough — Go's LookupHost silently fell back
// to the OS resolver chain (which respects /etc/nsswitch.conf,
// systemd-resolved, AND the Docker `extra_hosts` block) even
// with PreferGo: true + custom Dial. A raw UDP DNS query
// sidesteps the entire OS resolution chain — we speak DNS
// protocol directly to 1.1.1.1:53 and parse the response.
//
// Why 1.1.1.1 specifically:
//   - It's reachable from the skygate container's NAT egress
//     (verified 2026-09-17 — UDP:53 to 1.1.1.1 succeeds)
//   - It's not blocked by any network policy we know of
//   - It's not the operator's LAN DNS (so it can't have
//     split-horizon DNS overrides that return the LAN IP)
//   - It's fast (~5ms p50 from anywhere on the public internet)
//
// We deliberately do NOT retry or fall back to 8.8.8.8 — if
// 1.1.1.1 is unreachable, the failure surfaces on the /admin/derp
// page (Public IP: "(unresolved)") which is more honest than
// silently returning the LAN IP via the OS resolver chain.
func dnsLookupVia1111(hostname string) ([]net.IP, error) {
	// Build the DNS query: standard query, RD=1, 1 question,
	// 0 answers/NS/AR. Question: <hostname encoded as DNS labels>
	// type A (1) class IN (1).
	txid := []byte{0xab, 0xcd}
	flags := []byte{0x01, 0x00} // RD=1
	// 2026-09-18: pre-fix this was
	//   header := append(append(append(txid, flags...), []byte{...}...),)
	// — the outermost append had no values, which `go vet` rejects
	// ("append with no values") and which kept the CI job red at
	// .github/workflows/ci.yml:39. Build the header explicitly instead:
	// same 12 bytes (ID 2 + flags 2 + QDCOUNT/ANCOUNT/NSCOUNT/ARCOUNT 8),
	// no aliasing of the txid literal, and vet-clean.
	header := make([]byte, 0, 12)
	header = append(header, txid...)
	header = append(header, flags...)
	header = append(header, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
	var question []byte
	for _, label := range strings.Split(hostname, ".") {
		if label == "" {
			continue
		}
		question = append(question, byte(len(label)))
		question = append(question, []byte(label)...)
	}
	question = append(question, 0x00)       // root label
	question = append(question, 0x00, 0x01) // type A
	question = append(question, 0x00, 0x01) // class IN
	pkt := append(header, question...)

	// Dial UDP to 1.1.1.1:53 with a 3-second deadline.
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.Dial("udp", "1.1.1.1:53")
	if err != nil {
		return nil, fmt.Errorf("dial 1.1.1.1:53: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}
	if _, err := conn.Write(pkt); err != nil {
		return nil, fmt.Errorf("write DNS query: %w", err)
	}
	resp := make([]byte, 2048)
	n, err := conn.Read(resp)
	if err != nil {
		return nil, fmt.Errorf("read DNS response: %w", err)
	}
	if n < 12 {
		return nil, fmt.Errorf("DNS response too short: %d bytes", n)
	}

	// Skip header (12 bytes) + question section.
	idx := 12
	for idx < n {
		if resp[idx] == 0 {
			idx++
			break
		}
		if resp[idx]&0xc0 == 0xc0 {
			idx += 2
			break
		}
		idx += int(resp[idx]) + 1
	}
	idx += 4 // skip QTYPE + QCLASS

	// Walk answer section. RCODE in flags byte (offset 3):
	// 0 = NoError, 3 = NXDomain. Anything non-zero means failed.
	rcode := resp[3] & 0x0f
	if rcode != 0 {
		return nil, fmt.Errorf("DNS RCODE=%d for %s", rcode, hostname)
	}
	ancount := int(resp[6])<<8 | int(resp[7])
	var ips []net.IP
	for i := 0; i < ancount && idx+10 < n; i++ {
		// Skip name (label sequence or pointer).
		if resp[idx]&0xc0 == 0xc0 {
			idx += 2
		} else {
			for idx < n && resp[idx] != 0 {
				idx += int(resp[idx]) + 1
			}
			idx++
		}
		atype := int(resp[idx])<<8 | int(resp[idx+1])
		aclass := int(resp[idx+2])<<8 | int(resp[idx+3])
		_ = aclass
		rdlen := int(resp[idx+8])<<8 | int(resp[idx+9])
		idx += 10
		if atype == 1 && rdlen == 4 {
			ips = append(ips, net.IPv4(resp[idx], resp[idx+1], resp[idx+2], resp[idx+3]))
		}
		idx += rdlen
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no A records for %s", hostname)
	}
	return ips, nil
}
