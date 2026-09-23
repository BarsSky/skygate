package admin

// derp_probe_host_b296_test.go — B296: the probe address is editable from
// /admin/derp/relays and applies to the NEXT probe.
//
// This is the half B289.1 could not close. B289.1 made every probe dial the
// operator's address while still speaking the relay's public hostname (TLS
// SNI), and deploy.sh writes that address into .env — but the container reads
// .env only at CREATION (`env_file`), so applying an edit meant
// `docker compose up --force-recreate` over SSH (AGENTS trap #3), for a value
// the operator can see is missing right on the page.
//
// The tests below drive the real handler and the real derpmap endpoint against
// a migrated in-memory database, and assert the whole promise: with no .env
// value and a relay whose name does not resolve, the region is NOT published;
// after the form saves an address, the very next fetch publishes it; clearing
// the field takes it away again.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	skygatedb "skygate/internal/db"
	"skygate/internal/derpcfg"
)

// newB296DB opens a migrated in-memory SQLite database through the production
// open path. Skips (never fails) when the dialect is unavailable.
func newB296DB(t *testing.T) *skygatedb.FixedDBSource {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return &skygatedb.FixedDBSource{DB: d}
}

// seedB296Relay inserts the operator's bundled relay row. The hostname is
// deliberately NOT resolvable ("relay.example.com" is not in this process's
// resolver), which is the other shape of the live failure: the guard has one
// candidate, it does not resolve, and the node is dropped.
func seedB296Relay(t *testing.T, src *skygatedb.FixedDBSource, hostname, url string) {
	t.Helper()
	if _, err := src.DB.Exec(`INSERT INTO derp_relays
		(hostname, url, region_id, region_code, region_name, is_bundled, enabled, sort_order)
		VALUES (?, ?, 900, 'mow', 'Moscow Custom', 1, 1, 10)`, hostname, url); err != nil {
		t.Fatalf("seed derp_relays: %v", err)
	}
}

// b296DerpmapRegion fetches /admin/derp/relays/derpmap.json and reports whether
// region 900 is published, plus the node the clients would be told about.
func b296DerpmapRegion(t *testing.T, svc *Service) (bool, string, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	svc.GetAdminDerpRelaysDerpmap(rec, httptest.NewRequest("GET", "/admin/derp/relays/derpmap.json", nil))
	if rec.Code != http.StatusOK {
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
		return false, "", 0
	}
	return true, reg.Nodes[0].HostName, reg.Nodes[0].DERPPort
}

// postB296ProbeHost submits the card's form the way the browser does.
func postB296ProbeHost(t *testing.T, svc *Service, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/derp/relays/probe-host", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminDerpRelaysProbeHost(rec, req)
	return rec
}

// TestB296_ProbeHostSavedFromThePageAppliesToTheNextProbe is the block's
// contract: save on the page -> the next map fetch publishes the relay, with no
// restart and no container recreate; clear -> back to the .env/default layer.
func TestB296_ProbeHostSavedFromThePageAppliesToTheNextProbe(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	reachableHost, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	// No .env hint on this host — the operator has only the page.
	t.Setenv(derpcfg.EnvKey, "")

	src := newB296DB(t)
	seedB296Relay(t, src, "relay.example.com", "https://relay.example.com:"+portStr)
	svc := &Service{Backend: &stubBackend{}, DB: *src}

	published, _, _ := b296DerpmapRegion(t, svc)
	if published {
		t.Fatal("precondition failed: an unresolvable relay name must not be published without a probe address")
	}

	rec := postB296ProbeHost(t, svc, url.Values{"probe_host": {reachableHost}, "action": {"save"}})
	if rec.Code != http.StatusFound {
		t.Fatalf("POST status = %d, want 302 (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "pok=1") {
		t.Fatalf("redirect = %q, want a success flash (?pok=1)", loc)
	}
	if v, err := skygatedb.GetGlobalSetting(src.DB, derpcfg.SettingKey, ""); err != nil || v != reachableHost {
		t.Fatalf("stored override = %q (err=%v), want %q under %s", v, err, reachableHost, derpcfg.SettingKey)
	}

	published, name, gotPort := b296DerpmapRegion(t, svc)
	if !published {
		t.Fatal("after saving the probe address the region must be published — the value did not reach the probe")
	}
	if name != "relay.example.com" {
		t.Errorf("published HostName = %q, want the PUBLIC name (clients resolve it themselves)", name)
	}
	if gotPort != port {
		t.Errorf("published DERPPort = %d, want %d (the relay URL's port)", gotPort, port)
	}

	// "Очистить": the override goes away and so does the node (no .env value).
	if rec := postB296ProbeHost(t, svc, url.Values{"action": {"clear"}}); rec.Code != http.StatusFound {
		t.Fatalf("clear status = %d, want 302", rec.Code)
	}
	if v, _ := skygatedb.GetGlobalSetting(src.DB, derpcfg.SettingKey, ""); v != "" {
		t.Fatalf("after clearing, the stored override = %q, want empty", v)
	}
	if published, _, _ := b296DerpmapRegion(t, svc); published {
		t.Error("after clearing the override the unresolvable relay must be dropped again (env is empty)")
	}
}

// TestB296_ARefusedValueIsNotStoredAndSaysWhy pins the validation contract: a
// host:port paste is refused with its own flash code, and nothing reaches the
// DB — an invalid override would break every probe silently.
func TestB296_ARefusedValueIsNotStoredAndSaysWhy(t *testing.T) {
	t.Setenv(derpcfg.EnvKey, "192.0.2.10")
	src := newB296DB(t)
	svc := &Service{Backend: &stubBackend{}, DB: *src}

	rec := postB296ProbeHost(t, svc, url.Values{"probe_host": {"192.0.2.11:443"}, "action": {"save"}})
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "perr=port") {
		t.Fatalf("redirect = %q, want ?perr=port (the port comes from the relay row URL)", loc)
	}
	if v, _ := skygatedb.GetGlobalSetting(src.DB, derpcfg.SettingKey, ""); v != "" {
		t.Fatalf("a refused value must not be stored: found %q", v)
	}
	// The env layer still applies, unchanged.
	if got := derpcfg.DialHost(src.DB); got != "192.0.2.10" {
		t.Fatalf("effective probe host = %q, want the .env value 192.0.2.10", got)
	}
}

// TestB296_TheEnvValueIsShownAsTheFallbackSource keeps the page honest: when
// only .env carries a value, the resolution must say so, so an operator knows
// what the "Очистить" button would fall back to.
func TestB296_TheEnvValueIsShownAsTheFallbackSource(t *testing.T) {
	t.Setenv(derpcfg.EnvKey, "192.0.2.10")
	src := newB296DB(t)
	res := derpcfg.Resolve(src.DB)
	if res.Host != "192.0.2.10" || res.Source != derpcfg.SourceEnv {
		t.Fatalf("Resolve = %+v, want the .env value with source=env", res)
	}
}
