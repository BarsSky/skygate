package admin

// derp_duplicate_rows_b317_test.go — B317: the dashboard must NAME the region whose
// enabled rows compete for one health verdict.
//
// Live shape on skygate-host (2026-09-24): region 900 had two enabled rows
// (`…:443` and a stale `…:8443`), derp_health keeps one row per region, and the dead
// row's verdict is what the table showed — so the operator's own relay disappeared
// from the dashboard and the "Recommended DERP" banner fell through to a public
// region ~100 ms away. The prober now records the best measured row per region; this
// banner is the other half: it tells the operator the stale row exists, because only
// they can decide to disable it.

import (
	"strings"
	"testing"
)

func TestB317_EnabledRelayRowsByRegionNamesOnlyRegionsWithSeveralRows(t *testing.T) {
	src := newB296DB(t)
	seed := []struct {
		hostname string
		url      string
		region   int
		sortOrd  int
		enabled  int
	}{
		{"derp.example.com", "https://derp.example.com:443", 900, 10, 1},
		{"derp.example.com", "https://derp.example.com:8443", 900, 11, 1},
		{"controlplane.tailscale.com", "https://controlplane.tailscale.com/derpmap/default", 901, 100, 1},
		{"old.example.com", "https://old.example.com:443", 900, 12, 0}, // disabled → ignored
	}
	for _, s := range seed {
		if _, err := src.DB.Exec(`INSERT INTO derp_relays
			(hostname, url, region_id, region_code, region_name, is_bundled, enabled, sort_order)
			VALUES (?, ?, ?, 'mow', 'Moscow Custom', 0, ?, ?)`,
			s.hostname, s.url, s.region, s.enabled, s.sortOrd); err != nil {
			t.Fatalf("seed derp_relays: %v", err)
		}
	}

	got, err := enabledRelayRowsByRegion(src.DB)
	if err != nil {
		t.Fatalf("enabledRelayRowsByRegion: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d regions, want only region 900 (a single-row region is not a duplicate): %v", len(got), got)
	}
	urls, ok := got[900]
	if !ok {
		t.Fatalf("region 900 missing from %v", got)
	}
	if len(urls) != 2 {
		t.Fatalf("region 900 has %d enabled urls, want 2: %v", len(urls), urls)
	}
	// Deterministic order (sort_order ASC) so the banner cannot shuffle between renders.
	if urls[0] != "https://derp.example.com:443" || urls[1] != "https://derp.example.com:8443" {
		t.Fatalf("urls are not in a stable order: %v", urls)
	}
	for _, u := range urls {
		if strings.Contains(u, "old.example.com") {
			t.Fatalf("a DISABLED row was reported as a competing row: %v", urls)
		}
	}
}

func TestB317_EnabledRelayRowsByRegionIsEmptyWithoutDuplicates(t *testing.T) {
	src := newB296DB(t)
	if _, err := src.DB.Exec(`INSERT INTO derp_relays
		(hostname, url, region_id, region_code, region_name, is_bundled, enabled, sort_order)
		VALUES ('derp.example.com', 'https://derp.example.com:443', 900, 'mow', 'Moscow Custom', 1, 1, 10)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := enabledRelayRowsByRegion(src.DB)
	if err != nil {
		t.Fatalf("enabledRelayRowsByRegion: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a healthy single-row config must not raise the banner: %v", got)
	}
}
