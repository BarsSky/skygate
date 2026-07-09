package handlers

// handlers_derp.go — DERP (relay) admin page and supporting types.
// Extracted from handlers.go.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"skygate/internal/headscale"
)








func (a *App) GetAdminDERP(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	a.renderWithLayout(w, "admin/derp.html", c, map[string]any{
		"DerpStatus": a.collectDerpStatus(),
	})
}

// GetAdminDERPRefresh forces a refresh - same page.
func (a *App) GetAdminDERPRefresh(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/derp", http.StatusFound)
}

func httpGet(url string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// derper checks Host header against its TLS hostname. When we
	// query it over plain HTTP from inside the skygate container (to
	// 192.168.13.69:8443) we must present the public hostname, otherwise
	// /debug/ returns 403 Forbidden.
	req.Host = "derp.skynas.ru"
	req.Header.Set("Host", "derp.skynas.ru")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// parseDerperDebugHTML extracts Uptime, Version, TLS hostname, machine from the
// derper /debug/ HTML page.
func parseDerperDebugHTML(s *DerpStatus, html []byte) {
	text := string(html)
	if m := regexp.MustCompile(`Uptime:</b>\s*([^<]+)`).FindStringSubmatch(text); len(m) > 1 {
		s.UpTime = strings.TrimSpace(m[1])
	}
	if m := regexp.MustCompile(`Version:</b>\s*([^<]+)`).FindStringSubmatch(text); len(m) > 1 {
		v := strings.TrimSpace(m[1])
		// strip "-ERR-BuildInfo" suffix
		if i := strings.Index(v, "-ERR-"); i > 0 {
			v = v[:i]
		}
		s.Version = v
	}
	if m := regexp.MustCompile(`TLS hostname:</b>\s*([^<]+)`).FindStringSubmatch(text); len(m) > 1 {
		s.Hostname = strings.TrimSpace(m[1])
	}
	if m := regexp.MustCompile(`Machine:</b>\s*([^<]+)`).FindStringSubmatch(text); len(m) > 1 {
		s.Machine = strings.TrimSpace(m[1])
	}
}

// parseDerperVars pulls metrics out of /debug/vars JSON.
func parseDerperVars(s *DerpStatus, body []byte) {
	var v struct {
		ProcessStartUnixTime float64 `json:"process_start_unix_time"`
		DERP                 struct {
			Accepts              int   `json:"accepts"`
			BytesReceived        int64 `json:"bytes_received"`
			BytesSent            int64 `json:"bytes_sent"`
			CurrentConnections   int   `json:"gauge_current_connections"`
			CurrentHomeConns     int   `json:"gauge_current_home_connections"`
			ClientsTotal         int   `json:"gauge_clients_total"`
			ClientsLocal         int   `json:"gauge_clients_local"`
			PacketsReceived      int   `json:"packets_received"`
			PacketsSent          int   `json:"packets_sent"`
			PacketsDropped       int   `json:"packets_dropped"`
		} `json:"derp"`
		STUN struct {
			CounterRequests struct {
				Success int `json:"success"`
			} `json:"counter_requests"`
		} `json:"stun"`
		GoSyncMutexWaitSeconds float64 `json:"go_sync_mutex_wait_seconds"`
		GoVersion              string  `json:"go_version"`
		Memstats               struct {
			Alloc      uint64 `json:"Alloc"`
			Sys        uint64 `json:"Sys"`
			NumGC      uint32 `json:"NumGC"`
		} `json:"memstats"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return
	}
	// Memory in MB
	if v.Memstats.Alloc > 0 {
		s.Memory = fmt.Sprintf("%.1f MB heap", float64(v.Memstats.Alloc)/1024/1024)
	}
	// Stash extra metrics in extra fields via concat
	s.Connections = v.DERP.CurrentConnections
	s.Accepts = v.DERP.Accepts
	s.BytesIn = v.DERP.BytesReceived
	s.BytesOut = v.DERP.BytesSent
	s.PacketsIn = v.DERP.PacketsReceived
	s.PacketsOut = v.DERP.PacketsSent
	s.Clients = v.DERP.ClientsTotal
	s.STUNRequests = v.STUN.CounterRequests.Success
	// Derive started-at from process_start_unix_time
	if v.ProcessStartUnixTime > 0 {
		s.StartedAt = time.Unix(int64(v.ProcessStartUnixTime), 0).Format("2006-01-02 15:04:05 MST")
		// Recompute uptime if we got it from vars
		d := time.Since(time.Unix(int64(v.ProcessStartUnixTime), 0)).Round(time.Second)
		if s.UpTime == "" || s.UpTime == "n/a" {
			s.UpTime = d.String()
		}
	}
	// Go version
	if v.GoVersion != "" {
		s.GoVersion = v.GoVersion
	}
	// If we got DERP responses, it's running
	if v.DERP.Accepts >= 0 {
		s.Running = true
	}
	if v.STUN.CounterRequests.Success > 0 {
		s.STUNListening = true
	}
}

// PreauthKeyStats breaks down a user's preauth keys by lifecycle state.
// Total == Used + Active + Expired. Active means "still usable right now":
// unused AND expiration (if set) is in the future. Expired means unused
// but past its expiration. Used means a headscale node consumed it.
type PreauthKeyStats struct {
	Total   int
	Used    int
	Active  int
	Expired int
}

// countMyPreAuthKeys classifies every preauth key the user has been
// issued. preauth_keys.user_id references portal_users.id (NOT headscale
// username). The split lets the dashboard show "1 used, 0 active, 1
// expired" instead of a single number that requires the user to
// remember what each key was for.
//
// Side effect: a key is considered "used" when either our local
// `used` column is set OR any headscale node currently lists that
// key as its preAuthKey. The node-side check is the source of truth
// - if the node is gone (deleted, expired server-side) but our
// local row was never flipped, we flip it here. This keeps the

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

func (a *App) collectDerpStatus() DerpStatus {
	// DERP server runs on the host (not in the skygate container), so
	// systemctl/ss from inside the container can't see it. Instead we
	// query the derper's own debug endpoint at 192.168.13.69:8443/debug/
	// which is reachable from the container via the host bridge.
	s := DerpStatus{
		DERPPort:   "443",
		STUNPort:   "3478",
		Version:    "1.70.0",
		Hostname:   "derp.skynas.ru",
		RegionCode: "mow",
		RegionID:   "900",
		RegionName: "Moscow Custom",
		WhiteIP:    "95.165.170.190",
	}

	// Try derper debug endpoints (in priority order)
	derpURL := "http://192.168.13.69:8443"
	if v := a.DerpBaseURL; v != "" {
		derpURL = v
	}

	// 1. /debug/  -> HTML, contains Uptime, Version, etc.
	if html, err := httpGet(derpURL+"/debug/", 3*time.Second); err == nil {
		parseDerperDebugHTML(&s, html)
	}

	// 2. /debug/vars -> JSON, real metrics
	if body, err := httpGet(derpURL+"/debug/vars", 3*time.Second); err == nil {
		parseDerperVars(&s, body)
	}

	// 3. Plain / -> quick liveness check
	if _, err := httpGet(derpURL+"/", 3*time.Second); err == nil {
		s.SocketListening = true
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
			s.STUNListening = true
		}
	}

	// 5. Active connections (current TCP/UDP peers with reverse DNS)
	if body, err := httpGet(derpURL+"/active-conn", 3*time.Second); err == nil {
		var ac struct {
			TCP     []DerpPeer `json:"tcp"`
			UDPSTUN []DerpPeer `json:"udp_stun"`
		}
		if json.Unmarshal(body, &ac) == nil {
			s.ActiveTCP = classifyDerpPeers(ac.TCP)
			s.ActiveUDP = classifyDerpPeers(ac.UDPSTUN)
			s.ConnSummary = summarizeDerpPeers(append(append([]DerpPeer{}, s.ActiveTCP...), s.ActiveUDP...))
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
				s.Snapshot = append(s.Snapshot, snap)
			}
		}
	}

	// Hostname (white IP) from outbound interface (best-effort, no actual HTTP needed)
	s.WhiteIP = "95.165.170.190"

	return s
}

// firstTagOrFallback returns the node's first tag, or "tag:untagged"
// if the node has no tags. Used to populate node_owner_map.tag for
// rows that come from strategies that don't otherwise carry a tag
// (specifically the temporal fallback in C, which fires for both
// tagged and untagged nodes).
func firstTagOrFallback(n headscale.NodeView) string {
	if len(n.Tags) > 0 {
		return n.Tags[0]
	}
	return "tag:untagged"
}

// derpPeerNPM is the IP of Nginx Proxy Manager, which keeps persistent
// WebSocket connections to the derper for the /admin/derp page.
const derpPeerNPM = "192.168.13.67"

var (
	derpTailscaleNet = net.IPNet{IP: net.ParseIP("100.64.0.0").To4(), Mask: net.CIDRMask(10, 32)}
	derpLANNet       = net.IPNet{IP: net.ParseIP("192.168.13.0").To4(), Mask: net.CIDRMask(24, 32)}
)

// classifyDerpPeer labels a connection source.
//   ws_relay - Tailscale client (100.64.0.0/10 or any public IP hitting derper)
//   ws_admin - Nginx Proxy Manager WebSocket pool (192.168.13.67)
//   lan      - other LAN client (192.168.13.0/24)
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
