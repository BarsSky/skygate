package handlers

// handlers_derp_collect.go — fetches and parses the local custom DERP
// relay's debug endpoints. Owns:
//
//   - httpGet               — small GET helper that pins Host: derper.skynas.ru
//   - parseDerperDebugHTML  — extract Uptime/Version/Hostname/Machine from /debug/
//   - parseDerperVars       — extract runtime metrics from /debug/vars
//   - collectDerpStatus     — orchestrator: hit all 6 endpoints, build DerpStatus
//
// Extracted from handlers_derp.go (Этап 8).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

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
	req.Host = "derper.skynas.ru"
	req.Header.Set("Host", "derper.skynas.ru")
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

// collectDerpStatus is the orchestrator: it seeds a DerpStatus with the
// known derper config, then hits each of the 6 debug endpoints in turn,
// enriching the struct. Order matters: /debug/ and /debug/vars come
// first so Running/STUNListening are decided before /active-conn and
// /all-recent paint their data.
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
