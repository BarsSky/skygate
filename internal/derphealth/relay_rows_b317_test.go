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

// TestB317_ProbeAllPersistsOneVerdictPerRegion is the end-to-end half: a region
// with one working endpoint and one dead one must produce exactly ONE persist call,
// carrying the WORKING result — the live shape of `:443` + a stale `:8443`.
func TestB317_ProbeAllPersistsOneVerdictPerRegion(t *testing.T) {
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

	// A guaranteed-dead address on loopback (port 1).
	derps := []DERPInfo{
		{RegionID: 900, Host: "localhost", URL: "https://localhost:1", IsOwn: true},
		{RegionID: 900, Host: "localhost", URL: "https://localhost:" + u.Port(), IsOwn: true},
	}
	type call struct {
		url     string
		lat     int
		healthy bool
	}
	var calls []call
	var n atomic.Int64
	persist := func(ctx context.Context, d DERPInfo, lat int, healthy bool, err error) error {
		n.Add(1)
		calls = append(calls, call{d.URL, lat, healthy})
		return nil
	}
	results := ProbeAll(context.Background(), derps, &http.Client{Timeout: 3 * time.Second}, persist)
	if len(results) != 2 {
		t.Fatalf("every row must still be probed (the CLI prints them), got %d results", len(results))
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("persist called %d times for one region, want 1 (derp_health is keyed by region_id)", got)
	}
	if len(calls) != 1 || !calls[0].healthy || calls[0].lat <= 0 {
		t.Fatalf("the persisted verdict is not the working endpoint: %+v", calls)
	}
	if !strings.HasSuffix(calls[0].url, u.Port()) {
		t.Fatalf("the persisted row is the dead one: %+v", calls)
	}
}

// errPort is a plain sentinel for the pure-function table above.
var errPort = &portError{}

type portError struct{}

func (*portError) Error() string { return "dial tcp: connection refused" }

// TestB317_PruneDropsRowsForRegionsNoLongerInTheMap is the other half of "a derpmap
// document is not a relay": once region 901 stops being probed, its last verdict must
// not keep sitting on the dashboard reading "public, healthy, 117 ms" for ever.
func TestB317_PruneDropsRowsForRegionsNoLongerInTheMap(t *testing.T) {
	src := newB317DB(t)
	seed := func(region, healthy int, lat int) {
		if _, err := src.DB.Exec(`INSERT INTO derp_health
			(region_id, is_own, host, url, name, region_code, region_name, locality, country,
			 latency_ms, last_check, healthy, last_error, probes_total, probes_failed)
			VALUES (?, 0, ?, ?, '', '', '', '', '', ?, 0, ?, '', 1, 0)`,
			region, "h"+string(rune('0'+region%10)), "https://h", lat, healthy); err != nil {
			t.Fatalf("seed derp_health: %v", err)
		}
	}
	seed(900, 1, 20)
	seed(901, 1, 117)
	seed(4, 1, 121)

	// The 901 row is gone from the map (the derpmap document is no longer probed).
	kept := []DERPInfo{
		{RegionID: 900, URL: "https://derp.example.com:443"},
		{RegionID: 900, URL: "https://derp.example.com:8443"},
		{RegionID: 4, URL: "https://derp4f.tailscale.com"},
	}
	n, err := PruneMissingRegions(context.Background(), src.DB, kept)
	if err != nil {
		t.Fatalf("PruneMissingRegions: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1 (region 901)", n)
	}
	var left int
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health`).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 2 {
		t.Fatalf("%d rows left, want 2 (900 and 4)", left)
	}
	var stillThere int
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health WHERE region_id = 901`).Scan(&stillThere); err != nil {
		t.Fatalf("count 901: %v", err)
	}
	if stillThere != 0 {
		t.Fatal("the region-901 row survived the prune")
	}

	// An EMPTY list is "the map fetch failed", never "everything is gone".
	if _, err := PruneMissingRegions(context.Background(), src.DB, nil); err != nil {
		t.Fatalf("PruneMissingRegions(nil): %v", err)
	}
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health`).Scan(&left); err != nil {
		t.Fatalf("count after nil prune: %v", err)
	}
	if left != 2 {
		t.Fatalf("an empty probe list wiped the table (%d rows left) — a failed fetch must not empty the dashboard", left)
	}
}
