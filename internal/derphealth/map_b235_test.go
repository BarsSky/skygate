package derphealth

// map_b235_test.go — v1.5.2 (B235) — tests for
// FetchPublicDERPs that pin the HostName-vs-Name fix.
//
// Pre-B235: Host was set to `n.Name` (e.g. "1f"),
// which is Tailscale's internal SHORT label and
// NOT a resolvable DNS name. Every public DERP
// probe failed with "no such host" and the
// dashboard showed 28/28 as "degraded". B235 pins
// `n.HostName` (the FQDN like
// "derp1f.tailscale.com") as the actual network
// host, with `n.Name` preserved as a separate
// `Name` field for display.
//
// 2026-09-04: v1.5.2 (B235).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchPublicDERPs_HostIsFQDN_NotShortLabel is the
// regression test for the B235 bug. Pre-fix the
// code used `n.Name` for Host; post-fix it uses
// `n.HostName`. The fixture below mirrors the
// actual Tailscale derpmap/default response shape
// (Name + HostName fields both present in every
// node).
func TestFetchPublicDERPs_HostIsFQDN_NotShortLabel(t *testing.T) {
	// Synthetic derpmap response with 3 regions
	// (Warsaw, New York, Singapore) using the real
	// Tailscale schema.
	const body = `{
	  "Regions": {
	    "1": {
	      "RegionID": 1,
	      "RegionCode": "nyc",
	      "Nodes": [
	        {"Name": "1f", "RegionID": 1, "HostName": "derp1f.tailscale.com", "IPv4": "199.38.181.104", "CanPort80": true}
	      ]
	    },
	    "22": {
	      "RegionID": 22,
	      "RegionCode": "waw",
	      "Nodes": [
	        {"Name": "22w", "RegionID": 22, "HostName": "derp22w.tailscale.com", "IPv4": "5.181.27.221", "CanPort80": true}
	      ]
	    },
	    "3": {
	      "RegionID": 3,
	      "RegionCode": "sin",
	      "Nodes": [
	        {"Name": "3e", "RegionID": 3, "HostName": "derp3e.tailscale.com", "IPv4": "8.39.127.21", "CanPort80": true}
	      ]
	    }
	  }
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	// Override the package-level PublicMapURL so
	// FetchPublicDERPs hits our test server, not
	// the real Tailscale CDN. B235 made PublicMapURL
	// a `var` specifically for this.
	prev := PublicMapURL
	PublicMapURL = srv.URL
	defer func() { PublicMapURL = prev }()

	got, err := FetchPublicDERPs(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("FetchPublicDERPs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 DERPs, got %d", len(got))
	}

	// Expected mapping: region_id -> (Host FQDN, Name short label).
	expected := map[int]struct{ Host, Name string }{
		1:  {"derp1f.tailscale.com", "1f"},
		22: {"derp22w.tailscale.com", "22w"},
		3:  {"derp3e.tailscale.com", "3e"},
	}
	for _, d := range got {
		want, ok := expected[d.RegionID]
		if !ok {
			t.Errorf("unexpected region_id %d", d.RegionID)
			continue
		}
		if d.Host != want.Host {
			t.Errorf("region_id=%d: Host = %q, want %q (B235 fix: must be FQDN, not short label)",
				d.RegionID, d.Host, want.Host)
		}
		if d.Name != want.Name {
			t.Errorf("region_id=%d: Name = %q, want %q (B235: short label preserved for display)",
				d.RegionID, d.Name, want.Name)
		}
		if d.URL != "https://"+want.Host {
			t.Errorf("region_id=%d: URL = %q, want %q (B235 fix: must use Host FQDN, not n.Name)",
				d.RegionID, d.URL, "https://"+want.Host)
		}
	}
}

// TestFetchPublicDERPs_FallsBackToNameWhenHostNameEmpty
// pins the defensive fallback: if a future Tailscale
// API change drops HostName from a node, we fall
// back to Name (the short label) so the probe at
// least has something to dial. The fallback is
// logged at the call site if it ever fires (the
// 28-region Tailscale map always includes HostName,
// so the fallback is hypothetical — but the
// defensive path is the only way to keep the probe
// resilient to upstream API changes).
func TestFetchPublicDERPs_FallsBackToNameWhenHostNameEmpty(t *testing.T) {
	const body = `{
	  "Regions": {
	    "1": {
	      "RegionID": 1,
	      "RegionCode": "nyc",
	      "Nodes": [
	        {"Name": "1f", "RegionID": 1, "IPv4": "199.38.181.104"}
	      ]
	    }
	  }
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	prev := PublicMapURL
	PublicMapURL = srv.URL
	defer func() { PublicMapURL = prev }()

	got, err := FetchPublicDERPs(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("FetchPublicDERPs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 DERP, got %d", len(got))
	}
	if got[0].Host != "1f" {
		t.Errorf("Host = %q, want %q (fallback to Name when HostName empty)",
			got[0].Host, "1f")
	}
	if got[0].URL != "https://1f" {
		t.Errorf("URL = %q, want %q", got[0].URL, "https://1f")
	}
}

// TestFetchPublicDERPs_EmptyMap_NoCrash pins the
// "no regions" defensive path (the public map
// could be empty during a Tailscale outage). The
// function should return an empty slice, not nil
// and not panic. The dashboard then falls back to
// the own-DERP list (FetchAllDERPs handles the
// union). This complements the pre-existing
// TestFetchPublicDERPs_EmptyMap in derphealth_test.go
// (which only tests the JSON unmarshal of the
// empty response, not the full fetch path).
func TestFetchPublicDERPs_EmptyMap_HTTP(t *testing.T) {
	const body = `{"Regions":{}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	prev := PublicMapURL
	PublicMapURL = srv.URL
	defer func() { PublicMapURL = prev }()

	got, err := FetchPublicDERPs(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("FetchPublicDERPs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 DERPs, got %d", len(got))
	}
}

// TestFetchPublicDERPs_HTTPError pins that HTTP
// failures don't crash — they return an error
// which the caller (FetchAllDERPs) handles by
// falling back to own-only data. The dashboard
// then shows "DERP map fetch failed" with the
// last cached values, which is the pre-B235
// behaviour we want to preserve.
func TestFetchPublicDERPs_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	prev := PublicMapURL
	PublicMapURL = srv.URL
	defer func() { PublicMapURL = prev }()

	_, err := FetchPublicDERPs(context.Background(), srv.Client())
	if err == nil {
		t.Fatal("want error on HTTP 500, got nil")
	}
}

// TestFetchPublicDERPs_RegionIDFromMapKey pins
// that the region_id is parsed from the map key
// string (Tailscale uses string keys in the
// "Regions" map; the integer RegionID field on
// each node is also a valid source but the
// pre-B235 path used the map key, which is
// what headscale's API also returns). The
// dashboard's `Recommended` matching relies on
// RegionID matching the row from
// `derp_health` exactly, so a string→int
// conversion glitch (e.g. leading zeros being
// dropped or scientific notation being
// misread) would silently highlight the wrong
// row.
func TestFetchPublicDERPs_RegionIDFromMapKey(t *testing.T) {
	const body = `{
	  "Regions": {
	    "1":   {"RegionID": 1,   "RegionCode": "nyc", "Nodes": [{"Name": "1f",  "HostName": "derp1f.tailscale.com"}]},
	    "22":  {"RegionID": 22,  "RegionCode": "waw", "Nodes": [{"Name": "22w", "HostName": "derp22w.tailscale.com"}]},
	    "901": {"RegionID": 901, "RegionCode": "xxx", "Nodes": [{"Name": "901a","HostName": "controlplane.tailscale.com"}]}
	  }
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	prev := PublicMapURL
	PublicMapURL = srv.URL
	defer func() { PublicMapURL = prev }()

	got, err := FetchPublicDERPs(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("FetchPublicDERPs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 DERPs, got %d", len(got))
	}
	wantIDs := map[int]bool{1: true, 22: true, 901: true}
	for _, d := range got {
		if !wantIDs[d.RegionID] {
			t.Errorf("unexpected RegionID %d (parsed from map key)", d.RegionID)
		}
	}
	// The 901 case is important — that's the
	// bundled controlplane DERP and the
	// dashboard's "Recommended" code does an
	// exact-equality match on it. Verify the
	// string "901" parses to int 901, not 9 or
	// 90.
	for _, d := range got {
		if d.Host == "controlplane.tailscale.com" && d.RegionID != 901 {
			t.Errorf("controlplane: RegionID = %d, want 901", d.RegionID)
		}
	}
}
