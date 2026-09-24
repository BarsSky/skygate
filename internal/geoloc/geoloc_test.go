// internal/geoloc/geoloc_test.go — B312 (v1.5.77).
//
// The order the operator asked for is a data question, so the data questions are
// pinned here: what the API's answer means, which tier two relays share, how far apart
// they are, and which addresses are worth asking about at all (a tailnet address is
// NOT a location).
package geoloc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse_B312(t *testing.T) {
	ok := []byte(`{"status":"success","country":"Germany","countryCode":"DE","city":"Frankfurt am Main","lat":50.11,"lon":8.68}`)
	loc, err := Parse(ok)
	if err != nil {
		t.Fatalf("Parse(success) = %v", err)
	}
	if loc.Country != "Germany" || loc.City != "Frankfurt am Main" || !loc.HasCoords {
		t.Fatalf("Parse(success) = %+v", loc)
	}
	if loc.Label != "Germany · Frankfurt am Main" {
		t.Errorf("Label = %q", loc.Label)
	}
	if d := DistanceKM(loc, Location{Lat: 52.52, Lon: 13.40, HasCoords: true}); d < 400 || d > 500 {
		t.Errorf("Frankfurt→Berlin = %.0f km, want ~420", d)
	}

	// A refusal is not a location: the caller leaves the relay unknown instead of
	// inventing a place.
	if _, err := Parse([]byte(`{"status":"fail","message":"reserved range"}`)); err == nil {
		t.Errorf("a refused lookup must be an error")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Errorf("unreadable body must be an error")
	}
	// A success WITHOUT coordinates (some services omit them) must still give a label
	// but must not pretend to have a position.
	loc, err = Parse([]byte(`{"status":"success","country":"Netherlands","city":"Amsterdam"}`))
	if err != nil {
		t.Fatalf("Parse(no coords) = %v", err)
	}
	if loc.HasCoords {
		t.Errorf("missing coordinates must not be reported as (0,0): %+v", loc)
	}
}

func TestSameArea_B312(t *testing.T) {
	frankfurt := Location{Label: "Germany · Frankfurt", Country: "Germany", City: "Frankfurt", Lat: 50.11, Lon: 8.68, HasCoords: true}
	berlin := Location{Label: "Germany · Berlin", Country: "Germany", City: "Berlin", Lat: 52.52, Lon: 13.40, HasCoords: true}
	paris := Location{Label: "France · Paris", Country: "France", City: "Paris", Lat: 48.85, Lon: 2.35, HasCoords: true}
	unknown := Location{}

	if got := SameArea(frankfurt, berlin); got != AreaCountry {
		t.Errorf("Frankfurt vs Berlin = %d, want AreaCountry", got)
	}
	if got := SameArea(frankfurt, paris); got != AreaUnknown {
		t.Errorf("Frankfurt vs Paris = %d, want AreaUnknown", got)
	}
	if got := SameArea(frankfurt, Location{Label: "GERMANY · FRANKFURT"}); got != AreaSameCity {
		t.Errorf("a manually typed label must still match case-insensitively, got %d", got)
	}
	if got := SameArea(frankfurt, unknown); got != AreaUnknown {
		t.Errorf("an unknown relay shares nothing, got %d", got)
	}
	if d := DistanceKM(frankfurt, unknown); d != -1 {
		t.Errorf("distance to an unknown relay must be -1, got %v", d)
	}
}

func TestAddressForLookup_B312(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":   "203.0.113.7",
		"100.64.0.2":    "", // a tailnet address says nothing about the location
		"127.0.0.1":     "",
		"192.168.13.69": "",
		"10.0.0.5":      "",
		"":              "",
		"[203.0.113.7]": "203.0.113.7",
	}
	for in, want := range cases {
		if got := AddressForLookup(in); got != want {
			t.Errorf("AddressForLookup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookup_DisabledAndEndpointOverride_B312(t *testing.T) {
	t.Setenv("SKYGATE_GEO_LOOKUP", "off")
	if !LookupDisabled() {
		t.Fatalf("SKYGATE_GEO_LOOKUP=off must disable the lookup")
	}
	if _, err := Lookup(context.Background(), "203.0.113.7"); err == nil {
		t.Errorf("a disabled lookup must not call anything")
	}

	// An operator-provided endpoint is used verbatim (the %s is the address), which is
	// what makes an install behind a blocked network still able to use this feature
	// through its own service.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "203.0.113.7") {
			t.Errorf("the lookup must carry the address, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"success","country":"Testland","countryCode":"TL","city":"Testville","lat":1.5,"lon":2.5}`))
	}))
	defer srv.Close()
	t.Setenv("SKYGATE_GEO_LOOKUP", "on")
	t.Setenv("SKYGATE_GEO_LOOKUP_URL", srv.URL+"/%s")
	loc, err := Lookup(context.Background(), "203.0.113.7")
	if err != nil {
		t.Fatalf("Lookup via a stub endpoint = %v", err)
	}
	if loc.Country != "Testland" || loc.City != "Testville" || !loc.HasCoords {
		t.Fatalf("Lookup via a stub endpoint = %+v", loc)
	}
	if !strings.Contains(EndpointLabel(), "127.0.0.1") && !strings.Contains(EndpointLabel(), "http") {
		t.Errorf("EndpointLabel = %q", EndpointLabel())
	}
}

func TestCoordsRoundtrip_B312(t *testing.T) {
	lat, lon := FormatCoords(50.11, 8.68, true)
	if lat == "" || lon == "" {
		t.Fatalf("FormatCoords must render both values")
	}
	gotLat, gotLon, has := ParseCoords(lat, lon)
	if !has || gotLat != 50.11 || gotLon != 8.68 {
		t.Fatalf("ParseCoords(%q,%q) = %v,%v,%v", lat, lon, gotLat, gotLon, has)
	}
	if _, _, has := ParseCoords("", ""); has {
		t.Errorf("empty coordinates must read as unknown")
	}
	if a, b := FormatCoords(0, 0, false); a != "" || b != "" {
		t.Errorf("unknown coordinates must store as empty, got %q,%q", a, b)
	}
}
