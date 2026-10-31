package derphealth

// per_relay_b457_test.go — B457 (2026-10-31): the health table must hold one row per
// RELAY, and the page must be able to name WHICH relay of a region is broken.
//
// THE DEFECT. `derp_health` was created in v0.62 with `region_id INTEGER PRIMARY KEY`,
// i.e. one verdict per region, and a region is not a relay. The operator's own report
// («два релея почему то одинаковых висят но один рабочий второй с ошибкой по таблице
// ниже не понять какой») describes exactly the shape the reference host had: two
// enabled rows for region 900, `derp.skynas.ru:443` and a stale `:8443`. B317 made the
// prober persist only the best row per region, which was the best answer available at
// the time and still made the second relay UNREPRESENTABLE — no page could name it.
//
// These tests pin the fix at the storage boundary (a real migrated SQLite database),
// not just at the probe boundary.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// TestB457_TwoRelaysOfOneRegionCoexist — the core of the block: the same region, the
// same hostname, two ports, two independent verdicts, and a re-probe that touches only
// its own row.
func TestB457_TwoRelaysOfOneRegionCoexist(t *testing.T) {
	src := newB317DB(t)
	persist := PersistToDB(src.DB)
	ctx := context.Background()

	live := DERPInfo{RegionID: 900, Host: "derp.example.com", URL: "https://derp.example.com:443", IsOwn: true, RegionCode: "mow"}
	stale := DERPInfo{RegionID: 900, Host: "derp.example.com", URL: "https://derp.example.com:8443", IsOwn: true, RegionCode: "mow"}

	if err := persist(ctx, live, 20, true, nil); err != nil {
		t.Fatalf("persist live: %v", err)
	}
	if err := persist(ctx, stale, 0, false, fmt.Errorf("dial tcp 192.0.2.10:8443: connect: connection refused")); err != nil {
		t.Fatalf("persist stale: %v", err)
	}

	var rows int
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health WHERE region_id = 900`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Fatalf("region 900 holds %d rows, want 2 — two relays must keep two verdicts", rows)
	}

	// The two rows must be DISTINGUISHABLE by verdict: that is the whole point.
	var healthy, broken int
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health WHERE region_id = 900 AND healthy = 1`).Scan(&healthy); err != nil {
		t.Fatalf("count healthy: %v", err)
	}
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health WHERE region_id = 900 AND healthy = 0 AND last_error <> ''`).Scan(&broken); err != nil {
		t.Fatalf("count broken: %v", err)
	}
	if healthy != 1 || broken != 1 {
		t.Fatalf("healthy=%d broken=%d, want 1/1 (the page must be able to name the broken relay)", healthy, broken)
	}

	// A re-probe of the working relay updates ITS row only, and never resurrects the
	// stale one — the pre-B457 failure was one row overwriting the other.
	if err := persist(ctx, live, 33, true, nil); err != nil {
		t.Fatalf("re-persist live: %v", err)
	}
	var lat sql.NullInt64
	var errText string
	if err := src.DB.QueryRow(`SELECT latency_ms, last_error FROM derp_health WHERE url = 'https://derp.example.com:443'`).
		Scan(&lat, &errText); err != nil {
		t.Fatalf("read live row: %v", err)
	}
	if !lat.Valid || lat.Int64 != 33 || errText != "" {
		t.Fatalf("the working relay was not updated: latency=%v error=%q", lat, errText)
	}
	if err := src.DB.QueryRow(`SELECT latency_ms, last_error FROM derp_health WHERE url = 'https://derp.example.com:8443'`).
		Scan(&lat, &errText); err != nil {
		t.Fatalf("read stale row: %v", err)
	}
	if lat.Valid || !strings.Contains(errText, "connection refused") {
		t.Fatalf("the stale relay's verdict was overwritten by its neighbour: latency=%v error=%q", lat, errText)
	}
}

// TestB457_TwoRegionsSameURLStayApart — the key includes the region, so the same relay
// configured in two regions is two measurements rather than one shared verdict.
func TestB457_TwoRegionsSameURLStayApart(t *testing.T) {
	src := newB317DB(t)
	persist := PersistToDB(src.DB)
	ctx := context.Background()
	u := "https://derp.example.com"
	if err := persist(ctx, DERPInfo{RegionID: 900, Host: "derp.example.com", URL: u}, 10, true, nil); err != nil {
		t.Fatalf("persist 900: %v", err)
	}
	if err := persist(ctx, DERPInfo{RegionID: 901, Host: "derp.example.com", URL: u}, 0, false, fmt.Errorf("timeout")); err != nil {
		t.Fatalf("persist 901: %v", err)
	}
	var rows int
	if err := src.DB.QueryRow(`SELECT count(*) FROM derp_health`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Fatalf("%d rows, want 2 (the region is part of the relay's identity)", rows)
	}
}

// TestB457_EndpointFromURL pins what makes two relays of one hostname distinguishable
// on the page: without the port the operator reads two identical lines.
func TestB457_EndpointFromURL(t *testing.T) {
	cases := map[string]string{
		"https://derp.example.com":      "derp.example.com",
		"https://derp.example.com:443":  "derp.example.com",
		"https://derp.example.com:8443": "derp.example.com:8443",
		"http://derp.example.com:80":    "derp.example.com",
		"http://192.0.2.10:18443":       "192.0.2.10:18443",
		"":                              "",
		"not a url":                     "",
		"https://controlplane.example.com/derpmap/default": "controlplane.example.com",
	}
	for in, want := range cases {
		if got := EndpointFromURL(in); got != want {
			t.Errorf("EndpointFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
