// internal/feature/exit_rules/location_b312_test.go — B312 (v1.5.77).
//
// Two halves are pinned here: the automatic detection fills a relay's location once
// (and never overwrites the operator's own answer), and the fallback preference picks
// the CLOSEST healthy relay — same city beats same country beats further away, and a
// relay whose location is unknown is never preferred for a reason nobody can see.
package exit_rules

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	skygatedb "skygate/internal/db"
	"skygate/internal/geoloc"
)

func locB312(label, country, city string, lat, lon float64) geoloc.Location {
	return geoloc.Location{Label: label, Country: country, City: city, Lat: lat, Lon: lon, HasCoords: true}
}

// TestNearestHealthyRelay_B312: the tiers, in the operator's own words — "приоритет
// расставлять на сходный признак расположения".
func TestNearestHealthyRelay_B312(t *testing.T) {
	karolina := locB312("Germany · Frankfurt", "Germany", "Frankfurt", 50.11, 8.68)
	emilia := locB312("Netherlands · Amsterdam", "Netherlands", "Amsterdam", 52.37, 4.90)
	sharlotta := locB312("Germany · Berlin", "Germany", "Berlin", 52.52, 13.40)

	cases := []struct {
		name string
		locs map[string]geoloc.Location
		want string
	}{
		{
			name: "same city wins over same country",
			locs: map[string]geoloc.Location{
				"karolina":  karolina,
				"emilia":    emilia,
				"sharlotta": locB312("Germany · Frankfurt", "Germany", "Frankfurt", 50.12, 8.69),
			},
			want: "sharlotta",
		},
		{
			name: "same country wins over a different one",
			locs: map[string]geoloc.Location{
				"karolina":  karolina,
				"emilia":    emilia,
				"sharlotta": sharlotta,
			},
			want: "sharlotta",
		},
		{
			name: "no shared fact means no preference (the engine decides)",
			locs: map[string]geoloc.Location{
				"karolina": karolina,
				"emilia":   emilia,
			},
			want: "",
		},
		{
			name: "an unknown origin cannot justify a choice",
			locs: map[string]geoloc.Location{
				"emilia":    emilia,
				"sharlotta": sharlotta,
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locOf := func(relay string) geoloc.Location { return tc.locs[relay] }
			got := NearestHealthyRelay("karolina", []string{"emilia", "sharlotta"}, locOf)
			if got != tc.want {
				t.Fatalf("NearestHealthyRelay = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNearestHealthyRelay_DistanceBreaksTheTieInsideATier.
func TestNearestHealthyRelay_DistanceBreaksTheTieInsideATier(t *testing.T) {
	karolina := locB312("Germany · Frankfurt", "Germany", "Frankfurt", 50.11, 8.68)
	near := locB312("Germany · Mainz", "Germany", "Mainz", 49.99, 8.27)
	far := locB312("Germany · Berlin", "Germany", "Berlin", 52.52, 13.40)
	locOf := func(relay string) geoloc.Location {
		switch relay {
		case "karolina":
			return karolina
		case "near":
			return near
		default:
			return far
		}
	}
	if got := NearestHealthyRelay("karolina", []string{"far", "near"}, locOf); got != "near" {
		t.Fatalf("NearestHealthyRelay = %q, want near (Mainz is ~25 km away, Berlin ~440)", got)
	}
}

// TestRefreshExitNodeLocations_B312 stores what the lookup answered and never touches a
// manual value.
func TestRefreshExitNodeLocations_B312(t *testing.T) {
	d := newB276DB(t)
	// Two relays: one with a public address to look up, one already pinned by hand.
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, ssh_target, ssh_key_path, description, enabled, accept_routes, ssh_port)
	                      VALUES ('11', 'karolina', 'root@203.0.113.9:18022', '', '', 1, 0, '18022')`); err != nil {
		t.Fatalf("seed karolina: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, ssh_target, ssh_key_path, description, enabled, accept_routes, ssh_port)
	                      VALUES ('3', 'emilia', 'root@emilia.example.com', '', '', 1, 0, '')`); err != nil {
		t.Fatalf("seed emilia: %v", err)
	}
	if _, err := skygatedb.SetExitLocationManual(d, "emilia", "Netherlands · Amsterdam", "Netherlands", "52.37", "4.90"); err != nil {
		t.Fatalf("manual location: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","country":"Germany","countryCode":"DE","city":"Frankfurt am Main","lat":50.11,"lon":8.68}`))
	}))
	defer srv.Close()
	t.Setenv("SKYGATE_GEO_LOOKUP", "on")
	t.Setenv("SKYGATE_GEO_LOOKUP_URL", srv.URL+"/%s")

	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	resetLocationRefreshForTest()
	if n := s.RefreshExitNodeLocations(); n != 1 {
		t.Fatalf("RefreshExitNodeLocations = %d, want 1 (only karolina is due)", n)
	}

	loc, err := skygatedb.GetExitLocation(d, "karolina")
	if err != nil {
		t.Fatalf("read karolina: %v", err)
	}
	if !loc.Known() || loc.Source != "auto" || !strings.Contains(loc.Label, "Frankfurt") {
		t.Fatalf("karolina location = %+v, want an auto-detected Frankfurt", loc)
	}
	if loc.CheckedAt == 0 {
		t.Errorf("the attempt must be timestamped (that is what keeps the refresh cheap)")
	}

	// The manual row is untouched, and the lookup never even visited it.
	manual, _ := skygatedb.GetExitLocation(d, "emilia")
	if manual.Source != "manual" || !strings.Contains(manual.Label, "Amsterdam") {
		t.Fatalf("emilia location = %+v, want the manual Amsterdam", manual)
	}

	// A second pass inside the refresh window does nothing at all (no API call).
	if n := s.RefreshExitNodeLocations(); n != 0 {
		t.Errorf("a second immediate pass must be a no-op, got %d", n)
	}
}

// TestRefreshExitNodeLocations_OfflineLeavesTheRelayUnknown: the lookup is optional. A
// failure must not fail anything and must not invent a place.
func TestRefreshExitNodeLocations_OfflineLeavesTheRelayUnknown(t *testing.T) {
	d := newB276DB(t)
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, ssh_target, ssh_key_path, description, enabled, accept_routes, ssh_port)
	                      VALUES ('11', 'karolina', 'root@203.0.113.9:18022', '', '', 1, 0, '18022')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Setenv("SKYGATE_GEO_LOOKUP", "off")
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	resetLocationRefreshForTest()
	if n := s.RefreshExitNodeLocations(); n != 0 {
		t.Fatalf("a disabled lookup must store nothing, got %d", n)
	}
	loc, _ := skygatedb.GetExitLocation(d, "karolina")
	if loc.Known() {
		t.Fatalf("karolina must stay unknown, got %+v", loc)
	}
}
