// File: internal/feature/admin/derp_dashboard.go
// B189 (v1.5.2) — DERP Health Dashboard.
// B228 (v1.5.2+) — "hide unavailable" filter (operator
//   2026-09-03: 28+ rows of "degraded, —" на странице
//   делают dashboard бесполезным — нужно сразу отбросить
//   недоступные и оставить только healthy, отсортированные
//   по latency. По умолчанию фильтр ВКЛ (соответствует
//   предпочтению operator'а), тогглер в UI позволяет
//   включить "show all" для отладки).
//
// Renders /admin/derp/dashboard with one row per known
// DERP server (own + Tailscale's 28 public regions) showing
// the most recent probe's latency, health, and error.
// Probing happens in the background (derphealth.StartCron
// in cmd/skygate/main.go, 5-min interval); this handler
// just reads the cached derp_health table.
//
// Query params:
//   - show_unavailable=1 — show all rows (including
//     degraded / unprobed). Default is 0 (filter
//     hides degraded rows so the operator sees only
//     useful healthy DERPs sorted by latency).
//
// POST /admin/derp/dashboard/refresh — force a fresh probe
// cycle and re-render the page.
//
// Live view, not policy enforcement. See internal/derphealth/
// for the probe + persistence logic.

package admin

import (
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"skygate/internal/derphealth"
	"skygate/internal/headscale"
)

// GetAdminDerpDashboard renders the dashboard. Reads from
// derp_health; the cron has been writing there for at
// least one tick (or RunOnceNow has been called), so the
// page always has at least 1 row of fresh data.
func (s *Service) GetAdminDerpDashboard(w http.ResponseWriter, r *http.Request) {
	// 2026-08-31 (TD-18.2): the layout reads {{.UnreadCount}}
	// for the notification bell badge (B157). renderWithLayout
	// only auto-injects UnreadCount when c != nil. Pre-fix the
	// handler passed nil for c, so the template's `{{if gt
	// .UnreadCount 0}}` failed with "invalid type for
	// comparison" and the whole page (theme + body) was
	// unrendered. Now we extract the JWT claims from the
	// request via Backend.CurrentUser(r) like every other
	// admin handler (see internal/feature/admin/admin_pages.go
	// GetAdminAudit, internal/feature/admin/acl_import.go
	// GetAdminACLsImport, etc).
	c := s.Backend.CurrentUser(r)
	rows, err := s.dbc().QueryContext(r.Context(), `
		SELECT region_id, is_own, host, url, name, region_code, region_name,
		       locality, country, latency_ms, last_check, healthy,
		       last_error, probes_total, probes_failed
		  FROM derp_health
		 ORDER BY is_own DESC, region_id ASC
	`)
	if err != nil {
		log.Printf("derp dashboard: query: %v", err)
		s.Backend.RenderWithLayout(w, r, "admin/derp_dashboard.html", c,
			map[string]any{"Error": "db query failed"})
		return
	}
	defer rows.Close()

	var all []derphealth.HealthRow
	for rows.Next() {
		var r derphealth.HealthRow
		var healthy, isOwn int
		var latency sql.NullInt64
		var lastCheck int64
		if err := rows.Scan(&r.RegionID, &isOwn, &r.Host, &r.URL,
			&r.Name, &r.RegionCode, &r.RegionName, &r.Locality, &r.Country,
			&latency, &lastCheck, &healthy, &r.LastError,
			&r.ProbesTotal, &r.ProbesFailed); err != nil {
			log.Printf("derp dashboard: scan: %v", err)
			continue
		}
		r.IsOwn = isOwn == 1
		r.Healthy = healthy == 1
		if latency.Valid {
			r.LatencyMs = int(latency.Int64)
		}
		r.LastCheck = time.Unix(lastCheck, 0).UTC()
		all = append(all, r)
	}

	// B228: query-param filter — drop degraded / unprobed
	// rows by default so the operator sees only useful
	// healthy DERPs sorted by latency. The pre-B228 page
	// showed 28+ "degraded, —" rows which buried the one
	// actually-healthy own DERP (the operator's exact
	// 2026-09-03 report).
	//
	// `?show_unavailable=1` opts in to the pre-B228 view
	// (useful for debugging a regional outage where the
	// operator wants to see WHICH regions are down).
	//
	// "Healthy" in this filter = (r.Healthy && r.LatencyMs > 0).
	// The LatencyMs > 0 guard is the "probe actually ran and
	// returned a real number" check; rows where the probe
	// failed show LatencyMs=0 (not a real measurement) and
	// should be hidden even though r.Healthy might be set
	// from a previous tick (defensive — don't surface stale
	// "healthy" claims).
	totalCount := len(all)
	showUnavailable := r.URL.Query().Get("show_unavailable") == "1"
	visible := all
	if !showUnavailable {
		visible = visible[:0]
		for _, r := range all {
			if r.Healthy && r.LatencyMs > 0 {
				visible = append(visible, r)
			}
		}
	}

	// Sort the FULL set: pre-B228 own-first + latency.
	// Kept verbatim so the ?show_unavailable=1 view is
	// byte-identical to the pre-B228 page (B228 is a
	// pure filter + re-sort on the healthy subset, NOT a
	// sort change on the full view).
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].IsOwn != all[j].IsOwn {
			return all[i].IsOwn
		}
		li, lj := all[i].LatencyMs, all[j].LatencyMs
		if li == 0 && lj == 0 {
			return all[i].RegionID < all[j].RegionID
		}
		if li == 0 {
			return false
		}
		if lj == 0 {
			return true
		}
		return li < lj
	})

	// Sort the VISIBLE set: primary = latency ASC (fastest
	// first), tiebreaker = IsOwn DESC (own DERP wins on
	// equal latency), then RegionID ASC for stable ordering
	// across reloads. Only applied when the filter is
	// active — the show_unavailable=1 view reads from
	// `all` (the pre-B228-sorted full slice).
	if !showUnavailable {
		sort.SliceStable(visible, func(i, j int) bool {
			li, lj := visible[i].LatencyMs, visible[j].LatencyMs
			if li != lj {
				return li < lj
			}
			if visible[i].IsOwn != visible[j].IsOwn {
				return visible[i].IsOwn
			}
			return visible[i].RegionID < visible[j].RegionID
		})
	}

	// Pick the "recommended" DERP from the FULL set (so the
	// recommended banner survives even when the filtered view
	// is empty — operator still sees "here's the best, but
	// it's currently degraded"). First healthy+probed, own
	// first (the pre-B228 behaviour).
	var recommendedID int
	for _, r := range all {
		if r.Healthy && r.LatencyMs > 0 {
			if r.IsOwn {
				recommendedID = r.RegionID
				break
			}
			if recommendedID == 0 {
				recommendedID = r.RegionID
			}
		}
	}

	s.Backend.RenderWithLayout(w, r, "admin/derp_dashboard.html", c,
		map[string]any{
			"DERPs":           visible,
			"TotalCount":      totalCount,
			"VisibleCount":    len(visible),
			"ShowUnavailable": showUnavailable,
			"Recommended":     recommendedID,
			"Refreshed":       time.Now().UTC(),
			// B272.2: policy-file permissions. Tag application (and therefore
			// every per-device ACL rule) is impossible while headscale cannot
			// read its own policy file, so the page states it with the fixes.
			"PolicyAudit": headscale.AuditHeadscalePolicy(),
		})
}

// PostAdminDerpDashboardRefresh forces a fresh probe cycle
// (RunOnceNow) and re-renders the page. Used by the
// "Re-probe all" button. Bounded by ProbeAllTimeout so a
// slow probe doesn't block the page forever.
func (s *Service) PostAdminDerpDashboardRefresh(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	ctx := r.Context()
	results, err := derphealth.RunOnceNow(ctx, s.dbc(), nil)
	if err != nil {
		log.Printf("derp dashboard refresh: %v", err)
		s.Backend.RenderWithLayout(w, r, "admin/derp_dashboard.html", c,
			map[string]any{"Error": "refresh failed: " + err.Error()})
		return
	}
	ok, bad := 0, 0
	for _, r := range results {
		if r.Healthy {
			ok++
		} else {
			bad++
		}
	}
	// Redirect back to the dashboard so the page re-renders
	// with the fresh data.
	http.Redirect(w, r, "/admin/derp/dashboard?refreshed=1&ok="+itoa(ok)+"&bad="+itoa(bad),
		http.StatusSeeOther)
}

// ---------- B237: own-derp derpmap.json endpoint ----------

// derpMapNode mirrors the Tailscale "derpmap/default" JSON
// shape. We only emit the fields headscale cares about;
// the rest (lat/long, capabilities, etc.) are not in
// headscale's parser. JSON tag names match the Tailscale
// field names verbatim so headscale's `json.Unmarshal` of
// the response works without any custom adapter.
type derpMapNode struct {
	Name             string `json:"Name"`
	RegionID         int    `json:"RegionID"`
	HostName         string `json:"HostName"` // FQDN clients dial
	DERPPort         int    `json:"DERPPort"` // public port (443)
	STUNPort         int    `json:"STUNPort"` // 3478
	STUNOnly         bool   `json:"STUNOnly"`
	InsecureForTests bool   `json:"InsecureForTests"`
}

type derpMapRegion struct {
	RegionID   int           `json:"RegionID"`
	RegionCode string        `json:"RegionCode"`
	RegionName string        `json:"RegionName"`
	Nodes      []derpMapNode `json:"Nodes"`
}

type derpMapResponse struct {
	Regions map[string]derpMapRegion `json:"Regions"`
}

// shortNameFromHostname derives the Tailscale-style short
// label from a hostname: "derp.skynas.ru" → "skynas-1"
// (region_code + "-1" — Tailscale uses the short label
// for the per-node display name). Falls back to a
// hash-derived label when the hostname doesn't look like
// the canonical derp<region_code>.tailscale.com form.
//
// Pure function — easy to unit test.
func shortNameFromHostname(hostname, regionCode string) string {
	if regionCode != "" {
		// Prefer the region_code-1 form (matches Tailscale
		// convention: "1f" for region 1, "22w" for 22).
		return regionCode + "-1"
	}
	// Fallback: take the first label of the hostname.
	// "derp.skynas.ru" → "derp". Not ideal (collisions
	// possible across multiple own relays) but better
	// than an empty Name field.
	for i := 0; i < len(hostname); i++ {
		if hostname[i] == '.' {
			return hostname[:i]
		}
	}
	return hostname
}

// publicDERPPortFromURL extracts the public DERP port
// from the URL. Default 443 if the URL has no explicit
// port (e.g. https://derp.skynas.ru → 443). If the URL
// has a port (e.g. https://derp.skynas.ru:8443), we use
// it — this is the case when the operator exposes the
// derper on a non-standard port (no NPM in the middle).
//
// Pure function.
func publicDERPPortFromURL(rawURL string) int {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return 443
	}
	if _, port, err := net.SplitHostPort(u.Host); err == nil && port != "" {
		if p, perr := strconv.Atoi(port); perr == nil {
			return p
		}
	}
	return 443
}

// isDerpMapURL reports whether a derp_relays URL points at a DERP
// MAP DOCUMENT rather than at a single relay node.
//
// B265 (2026-09-19). The legacy global_settings key
// `derp.external_urls` held comma-separated derpmap URLs
// (`https://controlplane.tailscale.com/derpmap/default`), and
// AutoMigrateDerpRelays copied that value into derp_relays as a
// REGION ROW (region_id 901). GetAdminDerpRelaysDerpmap then
// published it to every client as a relay node, producing a
// phantom region in headscale's map:
//
//	"901": { "RegionCode": "", "RegionName": "",
//	         "Nodes": [{ "HostName": "controlplane.tailscale.com",
//	                     "DERPPort": 443 }] }
//
// The public Tailscale derpmap has no region 901, and
// controlplane.tailscale.com is the control-plane API host, not a
// relay — `tailscale netcheck` listed it as a trailing nameless
// region it could never measure, and the dashboard's
// "Recommended DERP" banner (pre-B265 IsOwn fix) pointed at it.
//
// headscale already merges the public map itself via
// `derp.urls: [https://controlplane.tailscale.com/derpmap/default]`
// (see the operator's config.yaml), so a derpmap URL must NEVER be
// emitted as a node.
func isDerpMapURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true // unparseable → not a usable relay node
	}
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/derpmap") ||
		strings.Contains(u.Path, "/derpmap/") {
		return true
	}
	// A relay URL that names a path other than "" / "derp" is not a
	// DERP endpoint either (the protocol lives on /derp and /).
	switch strings.TrimRight(u.Path, "/") {
	case "", "/derp", "/derp/probe", "/derp/latency-check":
		return false
	}
	return true
}

// derpNodeStatus is the per-node reachability probe result used by
// the derpmap endpoint to decide whether a node is worth
// publishing. B265: the live VM had TWO enabled bundled rows for
// region 900 (id=2 → `https://derp.skynas.ru:443`, id=3 →
// `https://derp.skynas.ru:8443`) and nothing listened on 8443, so
// every client received a region with one working and one dead
// node — and both named `mow-1`.
type derpNodeStatus struct {
	Reachable bool
	Err       string
}

func derpNodeKey(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// probeDERPNodeReachable does a TCP+TLS dial to the node's DERP
// port and reports whether the handshake completed. Short timeout
// (2s) because /admin/derp/relays/derpmap.json is fetched by
// headscale every derp.update_frequency and must not stall.
//
// InsecureSkipVerify is scoped to this reachability probe only (we
// assert "something speaks TLS here", and the operator's own derper
// uses a manual cert whose CN we don't have to trust for a liveness
// check). No data is read from the connection.
func probeDERPNodeReachable(host string, port int, timeout time.Duration) derpNodeStatus {
	addr := derpNodeKey(host, port)
	d := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{
		InsecureSkipVerify: true, // liveness only — no data exchanged
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return derpNodeStatus{Reachable: false, Err: err.Error()}
	}
	_ = conn.Close()
	return derpNodeStatus{Reachable: true}
}

// derpReachabilityCandidates is the ordered list of addresses the
// reachability guard tries for one relay row (B289).
//
// WHY THIS EXISTS — the live `skygate-host` VM, 2026-09-22: the bundled
// derper (region 900, `derp.skynas.ru:443`) was healthy and answered TLS
// with the right certificate, STUN was bound, and `192.168.13.69:443`
// was reachable from inside the skygate container. The guard still
// skipped EVERY region-900 node, because it dialled the hostname and the
// container inherits the host's `/etc/hosts`, where
// `derp.skynas.ru -> 127.0.0.1` (AGENTS deployment trap #2). Inside the
// container that is its own loopback, so the dial failed with
// `connect: connection refused`. The consequence was silent and total:
// `/admin/derp/relays/derpmap.json` answered `{"Regions":[]}`,
// headscale merged nothing but the public Tailscale map, and no client
// ever learned that a local relay exists — while the journal (the only
// place that said so) logged three "skipping region=900 … unreachable"
// lines per fetch.
//
// A name that resolves to loopback/unspecified inside the container is a
// CONFIGURATION artifact, not proof that the relay is down, so the
// operator's explicit probe host (`SKYGATE_DERP_PROBE_HOST`, the same
// hint the STUN probe already uses) is tried FIRST in that case. The
// node still advertises its public `HostName` — clients resolve that
// themselves from outside.
func derpReachabilityCandidates(db *sql.DB, host string, probeHost string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	leaked := hostResolvesToLoopback(host)
	if leaked {
		// The container cannot reach the relay by name; try the
		// operator's hint before wasting the timeout on loopback.
		add(probeHost)
		add(host)
	} else {
		add(host)
		add(probeHost)
	}
	// The other bundled hostnames may carry the one name that resolves
	// (a stale :8443 row is a different URL, same host).
	for _, h := range bundledDERPHostnamesFromDB(db) {
		add(h)
	}
	return out
}

// hostResolvesToLoopback reports whether `host` resolves, from THIS
// process, to a loopback or unspecified address — the signature of a
// host-resolver leak into a container.
func hostResolvesToLoopback(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	ips, err := net.LookupIP(h)
	if err != nil || len(ips) == 0 {
		return false // unresolvable is a real failure, not a leak
	}
	for _, ip := range ips {
		if !ip.IsLoopback() && !ip.IsUnspecified() {
			return false
		}
	}
	return true
}

// probeDERPNodeReachableAny tries every candidate address and returns the
// status plus the address that answered (empty when none did).
func probeDERPNodeReachableAny(candidates []string, port int, timeout time.Duration) (derpNodeStatus, string) {
	var last derpNodeStatus
	for _, h := range candidates {
		st := probeDERPNodeReachable(h, port, timeout)
		if st.Reachable {
			return st, net.JoinHostPort(h, strconv.Itoa(port))
		}
		last = st
	}
	if last.Err == "" && len(candidates) == 0 {
		last.Err = "no candidate address to probe"
	}
	return last, ""
}

// GetAdminDerpRelaysDerpmap serves the combined DERP map
// (own + bundled 901) as a Tailscale-shaped JSON. headscale
// is configured to fetch this URL via its `derp.urls`
// setting — the response is concatenated with the public
// Tailscale derpmap by headscale's derp.Map.Updater.
//
// Endpoint: GET /admin/derp/relays/derpmap.json
// Auth: NONE — the URL is documented only for the
// headscale config (it lives on the same docker network,
// so no public exposure). The response is just a list of
// DERP regions the operator's own infra is willing to
// serve; nothing secret about it. CORS: the response
// includes Access-Control-Allow-Origin: * so a headscale
// running on a different origin can fetch it (currently
// irrelevant — headscale runs in the same docker network
// and uses the service DNS name).
//
// B237 — 2026-09-04.
func (s *Service) GetAdminDerpRelaysDerpmap(w http.ResponseWriter, r *http.Request) {
	// B265: ?probe=0 disables the per-node reachability probe and
	// publishes every row as-is. Used by tests and by an operator
	// debugging why a node disappeared from the map. The default
	// (probe ON) is the safe one: a relay map with a dead node
	// costs every client a wasted dial.
	probeNodes := r.URL.Query().Get("probe") != "0"
	rows, err := s.dbc().QueryContext(r.Context(), `
		SELECT region_id, region_code, region_name, hostname, url
		  FROM derp_relays
		 WHERE enabled = 1
		 ORDER BY is_bundled DESC, sort_order ASC, region_id ASC
	`)
	if err != nil {
		log.Printf("derpmap: query: %v", err)
		http.Error(w, `{"error":"db query failed"}`, http.StatusInternalServerError)
		w.Header().Set("Content-Type", "application/json")
		return
	}
	defer rows.Close()

	out := derpMapResponse{Regions: map[string]derpMapRegion{}}
	// B289: what the guard decided, per row, so "the local region is
	// missing from the map" stops being a journal-only fact.
	type mapSkip struct {
		RegionID int
		Host     string
		Reason   string
	}
	var skipped []mapSkip
	probeHost := strings.TrimSpace(os.Getenv("SKYGATE_DERP_PROBE_HOST"))
	for rows.Next() {
		var rid int
		var rc, rn, host, urlStr string
		if err := rows.Scan(&rid, &rc, &rn, &host, &urlStr); err != nil {
			log.Printf("derpmap: scan: %v", err)
			continue
		}
		// B265: never publish a derpmap DOCUMENT as a relay node.
		// The legacy `derp.external_urls` migration turned
		// `https://controlplane.tailscale.com/derpmap/default`
		// into region 901, which clients then dialled as if it were
		// a DERP server (it is the control-plane API host). The
		// public map is merged by headscale itself via derp.urls.
		if isDerpMapURL(urlStr) {
			log.Printf("derpmap: skipping region=%d host=%s — url %q is a derpmap document, not a relay node", rid, host, urlStr)
			skipped = append(skipped, mapSkip{rid, host, "url is a derpmap document"})
			continue
		}
		if strings.TrimSpace(host) == "" && strings.TrimSpace(urlStr) == "" {
			continue
		}
		port := publicDERPPortFromURL(urlStr)
		// B265: probe the node before publishing it. A relay map
		// with a dead node costs every client a wasted dial and can
		// make netcheck score the region lower. The live VM had a
		// second bundled row for region 900 on :8443 with nothing
		// listening.
		//
		// B289: try every address the row is known by, because the
		// hostname alone can be a container-local artifact — see
		// derpReachabilityCandidates for the live failure it caused.
		if probeNodes {
			candidates := derpReachabilityCandidates(s.dbc(), host, probeHost)
			reach, via := probeDERPNodeReachableAny(candidates, port, 2*time.Second)
			if !reach.Reachable {
				log.Printf("derpmap: skipping region=%d host=%s port=%d — node unreachable (tried %v): %s",
					rid, host, port, candidates, reach.Err)
				skipped = append(skipped, mapSkip{rid, host, fmt.Sprintf("unreachable (tried %s): %s", strings.Join(candidates, ", "), reach.Err)})
				continue
			}
			if via != "" && !strings.HasPrefix(via, host+":") {
				log.Printf("derpmap: region=%d host=%s port=%d answered via %s (the hostname did not resolve to the relay from inside this container — check extra_hosts / SKYGATE_DERP_PROBE_HOST)", rid, host, port, via)
			}
		}
		node := derpMapNode{
			Name:             shortNameFromHostname(host, rc),
			RegionID:         rid,
			HostName:         host,
			DERPPort:         port,
			STUNPort:         3478,
			STUNOnly:         false,
			InsecureForTests: false,
		}
		// De-dup by region_id (the DB allows multiple rows
		// with the same region_id, which would produce a
		// malformed derpmap). Last one wins; the operator
		// can avoid this by using distinct region_ids
		// (enforced by the form, but defensive here).
		reg, ok := out.Regions[itoa(rid)]
		if !ok {
			reg = derpMapRegion{
				RegionID:   rid,
				RegionCode: rc,
				RegionName: rn,
			}
		}
		// B265: keep node names unique inside a region. Two bundled
		// rows with the same hostname and region_code produced two
		// nodes both named `mow-1` (verified in the client's netmap),
		// which makes per-node latency bookkeeping impossible to
		// read. Suffix the 2nd..Nth node with its port.
		for _, existing := range reg.Nodes {
			if existing.Name == node.Name {
				node.Name = node.Name + "-" + strconv.Itoa(port)
				break
			}
		}
		reg.Nodes = append(reg.Nodes, node)
		out.Regions[itoa(rid)] = reg
	}
	if err := rows.Err(); err != nil {
		log.Printf("derpmap: rows err: %v", err)
	}
	// B289: a BUNDLED region that ends up with no published node means the
	// operator's own relay is invisible to every client — the live
	// `skygate-host` state that produced "локальный DERP никак не
	// заработает" with a healthy derper and an empty map. That is an ERROR
	// with a named cause, not a debug line.
	for _, rid := range bundledRegionIDs(s.dbc()) {
		if reg, ok := out.Regions[itoa(rid)]; !ok || len(reg.Nodes) == 0 {
			var why []string
			for _, sk := range skipped {
				if sk.RegionID == rid {
					why = append(why, fmt.Sprintf("%s: %s", sk.Host, sk.Reason))
				}
			}
			if len(why) == 0 {
				continue
			}
			log.Printf("derpmap: ERROR region=%d is BUNDLED but publishes NO node — every client will fall back to the public DERP map. Causes: %s", rid, strings.Join(why, " | "))
		}
	}
	// CORS so headscale on a different host can fetch.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("derpmap: encode: %v", err)
	}
}

// bundledRegionIDs returns the region_ids of the enabled is_bundled=1 rows
// (the operator's own relays). B289.
func bundledRegionIDs(d *sql.DB) []int {
	if d == nil {
		return nil
	}
	rows, err := d.Query(`SELECT DISTINCT region_id FROM derp_relays WHERE enabled = 1 AND is_bundled = 1`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var rid int
		if rows.Scan(&rid) == nil {
			out = append(out, rid)
		}
	}
	return out
}

// ---------- /B237 ----------

// itoa is a tiny local helper to avoid pulling in strconv
// just for the redirect query string.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
