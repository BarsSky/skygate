// internal/feature/admin/derp_reach_b289_test.go — B289 (2026-09-22).
//
// The live `skygate-host` failure, in one sentence: the operator's own derper
// (region 900, derp.skynas.ru:443) was healthy, and
// `/admin/derp/relays/derpmap.json` answered `{"Regions":[]}` — so headscale's
// merged DERP map contained no local region and every client silently used the
// public Tailscale relays.
//
// Why: the B265 reachability guard dials the relay HOSTNAME from inside the
// skygate container, and the container inherits the host's `/etc/hosts`, where
// `derp.skynas.ru -> 127.0.0.1` (AGENTS deployment trap #2). Inside the
// container that is its own loopback, the dial is refused, and every bundled
// node is dropped. The relay was reachable all along at 192.168.13.69:443.
//
// These tests pin the fix: a name that resolves to loopback is a CONFIGURATION
// artifact, the operator's probe host is tried first in that case, and a node
// that answers on ANY candidate is published.
package admin

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
)

// TestB289_HostResolvesToLoopback pins the detector that distinguishes "the
// relay is down" from "this container cannot resolve the relay".
func TestB289_HostResolvesToLoopback(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", true},
		{"192.0.2.1", false}, // literal non-loopback address
		{"", true},           // nothing to resolve = treat as unusable
	}
	for _, c := range cases {
		if got := hostResolvesToLoopback(c.host); got != c.want {
			t.Errorf("hostResolvesToLoopback(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// TestB289_ProbeHostIsTriedFirstWhenTheNameIsLeaked is the regression: with the
// hostname resolving to loopback, the operator's probe host must be probed
// BEFORE it (otherwise the candidate list wastes the timeout on loopback and the
// node is dropped even though the relay answers).
func TestB289_ProbeHostIsTriedFirstWhenTheNameIsLeaked(t *testing.T) {
	got := derpReachabilityCandidates(nil, "localhost", "192.0.2.69")
	if len(got) < 2 {
		t.Fatalf("candidates = %v, want at least [probeHost, host]", got)
	}
	if got[0] != "192.0.2.69" {
		t.Errorf("candidates = %v, want the operator's probe host first when the relay name resolves to loopback", got)
	}
	if got[1] != "localhost" {
		t.Errorf("candidates = %v, want the relay name as the fallback", got)
	}

	// The opposite order when the name resolves properly: the name clients use
	// is the most meaningful probe.
	got = derpReachabilityCandidates(nil, "192.0.2.10", "192.0.2.69")
	if len(got) < 2 || got[0] != "192.0.2.10" || got[1] != "192.0.2.69" {
		t.Errorf("candidates = %v, want [relay host, probe host] when the relay name resolves normally", got)
	}

	// No probe host configured: just the relay name (and never a panic).
	got = derpReachabilityCandidates(nil, "localhost", "")
	if len(got) != 1 || got[0] != "localhost" {
		t.Errorf("candidates = %v, want only the relay name when no probe host is configured", got)
	}
}

// TestB289_ProbeAcceptsTheFirstAnsweringCandidate: the fallback probe must
// report reachability when a LATER candidate answers, and name the address that
// answered (that string is what the journal and the page show).
func TestB289_ProbeAcceptsTheFirstAnsweringCandidate(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}

	// First candidate: a port nothing listens on. Second: the live TLS server.
	dead := []string{"127.0.0.1"}
	st, via := probeDERPNodeReachableAny(dead, 1, 500*time.Millisecond)
	if st.Reachable {
		t.Fatal("probe reported reachable for 127.0.0.1:1")
	}
	if via != "" {
		t.Errorf("via = %q, want empty when nothing answered", via)
	}

	live := []string{"127.0.0.1"}
	st, via = probeDERPNodeReachableAny(live, port, 1500*time.Millisecond)
	if !st.Reachable {
		t.Fatalf("probe did not reach the live TLS server on port %d: %s", port, st.Err)
	}
	if via != net.JoinHostPort("127.0.0.1", portStr) {
		t.Errorf("via = %q, want %q", via, net.JoinHostPort("127.0.0.1", portStr))
	}
}

// TestB289_DerpmapPublishesALeakedRelayViaTheProbeHost is the end-to-end
// regression on a real (migrated, in-memory) database: a bundled relay whose
// hostname resolves to loopback inside this process, with the operator's probe
// host pointing at the live TLS listener. Before B289 the endpoint answered
// {"Regions":[]} (exactly what the live host served); now the region is
// published, with the public hostname intact.
func TestB289_DerpmapPublishesALeakedRelayViaTheProbeHost(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	// The operator's hint: where the CONTAINER can reach the relay. Ports come
	// from the relay URL, so the hint is a host only.
	t.Setenv("SKYGATE_DERP_PROBE_HOST", host)

	_, d, err := skygatedb.OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO derp_relays (hostname, url, region_id, region_code, region_name, is_bundled, enabled, sort_order)
	                     VALUES ('relay.example.com', 'https://relay.example.com:` + portStr + `', 900, 'mow', 'Moscow Custom', 1, 1, 10)`); err != nil {
		t.Fatalf("seed derp_relays: %v", err)
	}

	svc := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/derp/relays/derpmap.json", nil)
	svc.GetAdminDerpRelaysDerpmap(rec, req)

	if rec.Code != 200 {
		t.Fatalf("derpmap status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Regions map[string]struct {
			Nodes []struct {
				HostName string `json:"HostName"`
				DERPPort int    `json:"DERPPort"`
			} `json:"Nodes"`
		} `json:"Regions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("derpmap is not valid JSON: %v\n%s", err, rec.Body.String())
	}
	reg, ok := out.Regions["900"]
	if !ok || len(reg.Nodes) == 0 {
		t.Fatalf("region 900 is not published — this is the live failure (empty map, clients never learn about the local relay): %s", rec.Body.String())
	}
	if reg.Nodes[0].HostName != "relay.example.com" {
		t.Errorf("published HostName = %q, want the public name (clients resolve it themselves)", reg.Nodes[0].HostName)
	}
	if reg.Nodes[0].DERPPort != port {
		t.Errorf("published DERPPort = %d, want %d (the relay URL's port)", reg.Nodes[0].DERPPort, port)
	}
}

// TestB289_MapStatusExplainsASkippedRelay: the operator-visible half. A relay
// that cannot be reached must come back with the probed addresses and the
// reason, so "локальный DERP не работает" is not a journal-only fact.
func TestB289_MapStatusExplainsASkippedRelay(t *testing.T) {
	t.Setenv("SKYGATE_DERP_PROBE_HOST", "")
	mapStatusMu.Lock()
	mapStatusCached, mapStatusAt = nil, time.Time{}
	mapStatusMu.Unlock()

	_, d, err := skygatedb.OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO derp_relays (hostname, url, region_id, region_code, region_name, is_bundled, enabled, sort_order)
	                     VALUES ('localhost', 'https://localhost:1', 900, 'mow', 'Moscow Custom', 1, 1, 10),
	                            ('controlplane.tailscale.com', 'https://controlplane.tailscale.com/derpmap/default', 901, '', '', 0, 1, 100)`); err != nil {
		t.Fatalf("seed derp_relays: %v", err)
	}
	mapStatusTimeout = 200 * time.Millisecond
	t.Cleanup(func() { mapStatusTimeout = 1500 * time.Millisecond })

	svc := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	statuses := svc.derpRelayMapStatuses()
	if len(statuses) != 2 {
		t.Fatalf("statuses = %d, want 2", len(statuses))
	}
	byRegion := map[int]RelayMapStatus{}
	for _, st := range statuses {
		byRegion[st.RegionID] = st
	}
	if st := byRegion[900]; st.Published {
		t.Errorf("region 900 reported as published, but nothing listens on localhost:1")
	} else if st.Reason == "" || !strings.Contains(st.Reason, "unreachable") || !strings.Contains(st.Reason, "localhost") {
		t.Errorf("region 900 reason = %q, want it to name the probed addresses and the failure", st.Reason)
	}
	if st := byRegion[901]; st.Published {
		t.Errorf("a derpmap-document row was reported as published")
	} else if st.Reason == "" {
		t.Errorf("region 901 has no reason — the operator cannot tell why it is skipped")
	}
}
