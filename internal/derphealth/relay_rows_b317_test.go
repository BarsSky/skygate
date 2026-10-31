package derphealth

// relay_rows_b317_test.go — B317: one region, one verdict, and a derpmap document
// is not a relay.
//
// # THE LIVE DEFECT THESE PIN
//
// Operator report on skygate-host (2026-09-24): «релей что сейчас рядом со skygate
// не выбирается и не доступен для выбора как основного DERP сервера хотя при этом
// он должен иметь минимальную задержку», with the dashboard recommending
// `region_id %!s(int=901)` (Tailscale's control plane) instead.
//
// The database explained it: `derp_relays` holds TWO enabled rows for region 900 —
//
//	2|900|…|sort_order=10|derp.skynas.ru|https://derp.skynas.ru:443
//	3|900|…|sort_order=11|derp.skynas.ru|https://derp.skynas.ru:8443   ← dead port
//
// and `derp_health` is keyed by region_id, so whichever row was persisted LAST
// owned the region's only verdict. `FetchAllDERPs` collapsed the two rows in a
// `map[int]DERPInfo` loop (the `:8443` row won), the cron wrote
// `dial tcp …:8443: connect: connection refused` every 5 minutes, the dashboard
// hid the relay (it only shows healthy+measured rows) and the "Recommended DERP"
// banner fell through to the public map. Meanwhile `skygate derp-probe` iterated
// the rows in the other order, wrote 20 ms, and made the banner flip — two views of
// one table disagreeing, neither wrong about its own probe.
//
// The third row of the same table is the second half: the legacy
// `derp.external_urls` value (`https://controlplane.tailscale.com/derpmap/default`)
// was migrated into `derp_relays` as region 901, and the health prober dialled it
// and filed the result as a healthy public relay. The public Tailscale map has 28
// regions and no 901 (verified live), so it is not a relay at all.

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
)

func TestB317_UrlIsRelayNode(t *testing.T) {
	relays := []string{
		"https://derp.example.com",
		"https://derp.example.com:8443",
		"https://derp.example.com/",
		"https://derp.example.com/derp",
		"http://192.0.2.10:443",
	}
	for _, u := range relays {
		if !urlIsRelayNode(u) {
			t.Errorf("urlIsRelayNode(%q) = false, want true", u)
		}
	}
	notRelays := []string{
		"", "   ",
		"https://controlplane.tailscale.com/derpmap/default",
		"https://controlplane.tailscale.com/derpmap",
		"https://example.com/derpmap/",
		"https://example.com/some/document.json",
		"not a url",
		"derp.example.com", // no scheme → no host for url.Parse
	}
	for _, u := range notRelays {
		if urlIsRelayNode(u) {
			t.Errorf("urlIsRelayNode(%q) = true, want false", u)
		}
	}
}

// newB317DB opens a migrated in-memory SQLite database through the production open
// path (the same helper shape the B296 admin tests use), so the query under test
// runs against the real schema.
func newB317DB(t *testing.T) *skygatedb.FixedDBSource {
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

// TestB317_FetchOwnDERPsKeepsEveryRowOfARegionAndSkipsDocuments is the storage half:
// the two live rows of region 900 must BOTH survive the fetch (so the prober can
// measure both and keep the working one), while the region-901 derpmap document
// must not be probed as a relay at all.
func TestB317_FetchOwnDERPsKeepsEveryRowOfARegionAndSkipsDocuments(t *testing.T) {
	src := newB317DB(t)
	seed := []struct {
		region   int
		bundled  int
		sortOrd  int
		hostname string
		url      string
	}{
		{900, 1, 10, "derp.example.com", "https://derp.example.com:443"},
		{900, 1, 11, "derp.example.com", "https://derp.example.com:8443"},
		{901, 0, 100, "controlplane.tailscale.com", "https://controlplane.tailscale.com/derpmap/default"},
	}
	for _, s := range seed {
		if _, err := src.DB.Exec(`INSERT INTO derp_relays
			(hostname, url, region_id, region_code, region_name, is_bundled, enabled, sort_order)
			VALUES (?, ?, ?, 'mow', 'Moscow Custom', ?, 1, ?)`,
			s.hostname, s.url, s.region, s.bundled, s.sortOrd); err != nil {
			t.Fatalf("seed derp_relays: %v", err)
		}
	}

	own, err := FetchOwnDERPs(context.Background(), src.DB)
	if err != nil {
		t.Fatalf("FetchOwnDERPs: %v", err)
	}
	if len(own) != 2 {
		t.Fatalf("got %d own rows, want 2 (both region-900 rows; the derpmap document must be skipped): %+v", len(own), own)
	}
	for _, d := range own {
		if d.RegionID != 900 {
			t.Fatalf("region %d leaked into the probe list (a derpmap document is not a relay)", d.RegionID)
		}
		if !d.IsOwn {
			t.Errorf("region %d: IsOwn=false, want true (is_bundled=1)", d.RegionID)
		}
		if strings.Contains(d.URL, "/derpmap") {
			t.Fatalf("a derpmap document reached the probe list: %s", d.URL)
		}
	}
	// Deterministic order (url ASC) — the same order on every tick, so nothing
	// downstream can depend on map iteration or on physical row order.
	if own[0].URL != "https://derp.example.com:443" || own[1].URL != "https://derp.example.com:8443" {
		t.Fatalf("rows are not in a deterministic order: %s, %s", own[0].URL, own[1].URL)
	}
}

// TestB317_BestPerRegionPrefersHealthThenLatency pins the collapse rule that makes
// a region's verdict independent of which row finished last.
func TestB317_BestPerRegionPrefersHealthThenLatency(t *testing.T) {
	dead := ProbeResult{Info: DERPInfo{RegionID: 900, URL: "https://derp.example.com:8443"}, Err: errPort}
	good := ProbeResult{Info: DERPInfo{RegionID: 900, URL: "https://derp.example.com:443"}, LatencyMs: 20, Healthy: true}
	slower := ProbeResult{Info: DERPInfo{RegionID: 900, URL: "https://derp.example.com:444"}, LatencyMs: 55, Healthy: true}
	other := ProbeResult{Info: DERPInfo{RegionID: 4, URL: "https://derp4.example.com"}, LatencyMs: 121, Healthy: true}

	// A working endpoint beats a dead one, whatever the input order is.
	for _, in := range [][]ProbeResult{{dead, good}, {good, dead}} {
		got := BestPerRegion(in)
		if len(got) != 1 || !got[0].Healthy || got[0].LatencyMs != 20 {
			t.Fatalf("BestPerRegion(%v) = %+v, want the healthy 20 ms row", in, got)
		}
	}
	// Among healthy rows the fastest wins.
	if got := BestPerRegion([]ProbeResult{slower, good}); len(got) != 1 || got[0].LatencyMs != 20 {
		t.Fatalf("fastest healthy row must win, got %+v", got)
	}
	// All dead: the FIRST error is kept, so the shown reason is stable.
	a := ProbeResult{Info: DERPInfo{RegionID: 900, URL: "first"}, Err: errPort}
	b := ProbeResult{Info: DERPInfo{RegionID: 900, URL: "second"}, Err: errPort}
	got := BestPerRegion([]ProbeResult{a, b})
	if len(got) != 1 || got[0].Info.URL != "first" {
		t.Fatalf("with every endpoint dead the first result must be kept, got %+v", got)
	}
	// Distinct regions pass through untouched, in first-appearance order.
	got = BestPerRegion([]ProbeResult{dead, other, good})
	if len(got) != 2 || got[0].Info.RegionID != 900 || got[1].Info.RegionID != 4 {
		t.Fatalf("regions collapsed wrongly: %+v", got)
	}
	if !got[0].Healthy || got[0].LatencyMs != 20 {
		t.Fatalf("region 900 kept the wrong row: %+v", got[0])
	}
}

// TestB457_ProbeAllPersistsEveryRelay — RENEGOTIATED by B457 (2026-10-31).
//
// The B317 shape of this test asserted ONE persist call per region, because
// derp_health was keyed by region_id and only the best row could be stored. That
// collapse is exactly what made "one of the two relays is broken" unanswerable on
// every page, so the schema is now keyed by (region_id, url) and the prober stores
// EVERY relay. This test is the storage contract: a region with one working endpoint
// and one dead one produces TWO persists, each carrying its own verdict — the dead
// one keeps its error, which is what lets the dashboard name WHICH relay is down.
func TestB457_ProbeAllPersistsEveryRelay(t *testing.T) {
	srv := newSNIStrictHealthServer(t, "localhost")
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse %s: %v", srv.URL, err)
	}
	// The fixture uses httptest's own certificate; production keeps the system
	// trust store (see the B289.1 test for the same carve-out).
	prev := ProbeOneTLSConfig
	ProbeOneTLSConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test fixture
	t.Cleanup(func() { ProbeOneTLSConfig = prev })

	// The live shape: ONE hostname, two ports, one of them dead.
	deadURL := "https://localhost:1"
	liveURL := "https://localhost:" + u.Port()
	derps := []DERPInfo{
		{RegionID: 900, Host: "localhost", URL: deadURL, IsOwn: true},
		{RegionID: 900, Host: "localhost", URL: liveURL, IsOwn: true},
	}
	type call struct {
		url     string
		lat     int
		healthy bool
		err     string
	}
	var calls []call
	var n atomic.Int64
	persist := func(ctx context.Context, d DERPInfo, lat int, healthy bool, err error) error {
		n.Add(1)
		e := ""
		if err != nil {
			e = err.Error()
		}
		calls = append(calls, call{d.URL, lat, healthy, e})
		return nil
	}
	results := ProbeAll(context.Background(), derps, &http.Client{Timeout: 3 * time.Second}, persist)
	if len(results) != 2 {
		t.Fatalf("every row must still be probed, got %d results", len(results))
	}
	if got := n.Load(); got != 2 {
		t.Fatalf("persist called %d times for two relays, want 2 (one verdict per relay)", got)
	}
	var sawDead, sawLive bool
	for _, c := range calls {
		switch c.url {
		case deadURL:
			sawDead = true
			if c.healthy {
				t.Errorf("the dead endpoint was persisted as healthy: %+v", c)
			}
			if c.err == "" {
				t.Errorf("the dead endpoint was persisted without its error, so the page cannot name the failure: %+v", c)
			}
		case liveURL:
			sawLive = true
			if !c.healthy || c.lat <= 0 {
				t.Errorf("the live endpoint was persisted as broken: %+v", c)
			}
		}
	}
	if !sawDead || !sawLive {
		t.Fatalf("both relays must reach the store, got %+v", calls)
	}
}

// errPort is a plain sentinel for the pure-function table above.
var errPort = &portError{}

type portError struct{}

func (*portError) Error() string { return "dial tcp: connection refused" }

// TestB457_PruneDropsTheRelayItDidNotSeeAndKeepsItsNeighbour — RENEGOTIATED by B457.
//
// The B317 shape asserted that a region missing from the map lost its row, because one
// region WAS one row. With one row per relay, a region-level prune would delete the
// relay it simply did not see: the live case had a stale `:8443` row the map no longer
// lists next to a working `:443` one, and a region-only delete takes both. The prune is
// therefore keyed by (region_id, url) and this test pins the difference.
func TestB457_PruneDropsTheRelayItDidNotSeeAndKeepsItsNeighbour(t *testing.T) {
	src := newB317DB(t)
	seed := func(region int, host, u string, lat int) {
		if _, err := src.DB.Exec(`INSERT INTO derp_health
			(region_id, is_own, host, url, name, region_code, region_name, locality, country,
			 latency_ms, last_check, healthy, last_error, probes_total, probes_failed)
			VALUES (?, 0, ?, ?, '', '', '', '', '', ?, 0, 1, '', 1, 0)`,
			region, host, u, lat); err != nil {
			t.Fatalf("seed derp_health: %v", err)
		}
	}
	seed(900, "derp.example.com", "https://derp.example.com:443", 20)
	seed(900, "derp.example.com", "https://derp.example.com:8443", 0)
	seed(901, "controlplane.tailscale.com", "https://controlplane.tailscale.com/derpmap/default", 117)

	// The map no longer lists the stale :8443 row and never listed the 901 document.
	kept := []DERPInfo{
		{RegionID: 900, Host: "derp.example.com", URL: "https://derp.example.com:443"},
		{RegionID: 4, Host: "derp4f.tailscale.com", URL: "https://derp4f.tailscale.com"},
	}
	n, err := PruneMissingRelays(context.Background(), src.DB, kept)
	if err != nil {
		t.Fatalf("PruneMissingRelays: %v", err)
	}
	if n != 2 {
		t.Fatalf("pruned %d rows, want 2 (the stale :8443 and the 901 document)", n)
	}
	var live int
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health WHERE url = 'https://derp.example.com:443'`).Scan(&live); err != nil {
		t.Fatalf("count live: %v", err)
	}
	if live != 1 {
		t.Fatal("the WORKING relay of region 900 was deleted — a region-level prune takes its neighbour too")
	}
	var left int
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health`).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 1 {
		t.Fatalf("%d rows left, want 1 (the working :443 relay)", left)
	}
	// A stale :8443 row that IS in the list again survives — the prune must be about
	// "not in the map", never about "this relay looks disabled".
	if _, err := src.DB.Exec(`INSERT INTO derp_health
		(region_id, is_own, host, url, name, region_code, region_name, locality, country,
		 latency_ms, last_check, healthy, last_error, probes_total, probes_failed)
		VALUES (900, 0, 'derp.example.com', 'https://derp.example.com:8443', '', '', '', '', '', NULL, 0, 0, '', 1, 1)`); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if _, err := PruneMissingRelays(context.Background(), src.DB, []DERPInfo{
		{RegionID: 900, URL: "https://derp.example.com:443"},
		{RegionID: 900, URL: "https://derp.example.com:8443"},
	}); err != nil {
		t.Fatalf("PruneMissingRelays (both listed): %v", err)
	}
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health`).Scan(&left); err != nil {
		t.Fatalf("count 2: %v", err)
	}
	if left != 2 {
		t.Fatalf("a listed pair must be kept entirely, got %d rows", left)
	}

	// An EMPTY list is "the map fetch failed", never "everything is gone".
	if _, err := PruneMissingRelays(context.Background(), src.DB, nil); err != nil {
		t.Fatalf("PruneMissingRelays(nil): %v", err)
	}
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health`).Scan(&left); err != nil {
		t.Fatalf("count after nil prune: %v", err)
	}
	if left != 2 {
		t.Fatalf("an empty probe list wiped the table (%d rows left) — a failed fetch must not empty the dashboard", left)
	}
}
