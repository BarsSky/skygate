package handlers

// handlers_derp.go — DERP (relay) admin HTTP handlers and the DERP data
// types they pass to templates. The data fetching / parsing / classifying
// logic lives in sibling files:
//
//   - handlers_derp_collect.go  — collectDerpStatus + httpGet + parseDerper*
//   - handlers_derp_classify.go — classifyDerpPeer(s) + summarizeDerpPeers
//
// Extracted from handlers.go (originally 438 lines, split during Этап 8).

import "net/http"

// GetAdminDERP renders the /admin/derp page from a freshly collected
// DerpStatus snapshot.
func (a *App) GetAdminDERP(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	a.renderWithLayout(w, r, "admin/derp.html", c, map[string]any{
		"DerpStatus": a.collectDerpStatus(),
	})
}

// GetAdminDERPRefresh forces a refresh - same page.
func (a *App) GetAdminDERPRefresh(w http.ResponseWriter, r *http.Request) {
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
