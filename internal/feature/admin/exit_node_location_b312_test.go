// internal/feature/admin/exit_node_location_b312_test.go — B312 (v1.5.77).
//
// The handler half of the location feature: the operator can state where a relay is
// (and hand it back to the automatic lookup), a typo is refused with a translated flash
// instead of a raw error page, and the audit row records who decided it.
package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/db"
)

func TestPostAdminExitNodeLocation_B312(t *testing.T) {
	src, _, _ := setupClaimTestDB(t)
	if _, err := src.db.Exec(`INSERT INTO exit_servers (node_id, hostname, ssh_target, ssh_key_path, description, enabled, accept_routes, ssh_port)
	                           VALUES ('11', 'karolina', 'root@203.0.113.9:18022', '', '', 1, 0, '18022')`); err != nil {
		t.Fatalf("seed relay: %v", err)
	}
	be := &captureBackend{admin: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	svc := &Service{DB: src, Backend: be}

	rec := b305Post(t, svc.PostAdminExitNodeLocation, "/admin/exit-nodes/location", url.Values{
		"hostname": {"karolina"},
		"label":    {"Германия · Франкфурт"},
		"country":  {"Германия"},
		"lat":      {"50.11"},
		"lon":      {"8.68"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "ok=") {
		t.Errorf("a successful save must flash ok, got %q", loc)
	}
	got, err := db.GetExitLocation(src.db, "karolina")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !got.Known() || got.Source != "manual" {
		t.Fatalf("stored = %+v, want a manual location", got)
	}
	if got.LatRaw == "" || got.LonRaw == "" {
		t.Errorf("coordinates must be stored when given, got lat=%q lon=%q", got.LatRaw, got.LonRaw)
	}

	// Clearing hands the relay back to the automatic lookup.
	rec = b305Post(t, svc.PostAdminExitNodeLocation, "/admin/exit-nodes/location", url.Values{
		"hostname": {"karolina"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear: status = %d", rec.Code)
	}
	got, _ = db.GetExitLocation(src.db, "karolina")
	if got.Source == "manual" || got.Known() {
		t.Fatalf("after clearing the relay must be unknown/automatic, got %+v", got)
	}
}

func TestPostAdminExitNodeLocationRefusals_B312(t *testing.T) {
	src, _, _ := setupClaimTestDB(t)
	be := &captureBackend{admin: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	svc := &Service{DB: src, Backend: be}

	cases := []struct {
		name string
		form url.Values
		want string
	}{
		{"no hostname", url.Values{"label": {"x"}}, "err="},
		{"unknown relay", url.Values{"hostname": {"nope"}, "label": {"x"}}, "err="},
		{"half a coordinate pair", url.Values{"hostname": {"karolina"}, "label": {"x"}, "lat": {"50"}}, "err="},
		{"unparsable coordinates", url.Values{"hostname": {"karolina"}, "label": {"x"}, "lat": {"abc"}, "lon": {"8"}}, "err="},
		{"out of range", url.Values{"hostname": {"karolina"}, "label": {"x"}, "lat": {"120"}, "lon": {"8"}}, "err="},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := b305Post(t, svc.PostAdminExitNodeLocation, "/admin/exit-nodes/location", tc.form)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want a 303 flash (body %q)", rec.Code, rec.Body.String())
			}
			if loc := rec.Header().Get("Location"); !strings.Contains(loc, tc.want) {
				t.Errorf("redirect %q does not carry %q", loc, tc.want)
			}
			if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "<html") {
				t.Errorf("a refusal must not render a raw error page: %q", rec.Body.String())
			}
		})
	}
}

func TestPostAdminExitNodeLocationRejectsNonAdmin_B312(t *testing.T) {
	src, _, _ := setupClaimTestDB(t)
	svc := &Service{DB: src, Backend: &captureBackend{}}
	rec := b305Post(t, svc.PostAdminExitNodeLocation, "/admin/exit-nodes/location", url.Values{
		"hostname": {"karolina"}, "label": {"x"},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-admin", rec.Code)
	}
}
