// B308 (v1.5.73) — the admin handler for the domain re-resolve interval.
//
// The page writes global_settings.dns_domain_resolve_interval_sec, and the
// updater reads it on every tick. The two must agree on the key (a rename on one
// side silently restores the churn) and the stored value must be the CLAMPED one,
// so the number the operator sees is the number the runtime uses.
package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/feature/exit_rules"
)

func TestPostAdminSystemTestsDNSInterval_B308(t *testing.T) {
	src, _, _ := setupClaimTestDB(t)
	be := &captureBackend{admin: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	svc := &Service{DB: src, Backend: be}

	cases := []struct {
		name      string
		formValue string
		want      string
	}{
		{"six-hours", "21600", "21600"},
		{"one-hour", "3600", "3600"},
		{"zero-means-every-tick", "0", "0"},
		{"too-small-is-clamped-up", "60", "300"},
		{"too-large-is-clamped-down", "99999999", "604800"},
		{"empty-restores-the-default", "", "21600"},
		{"garbage-restores-the-default", "abc", "21600"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := b305Post(t, svc.PostAdminSystemTestsDNSInterval, "/admin/system_tests/dns-interval",
				url.Values{"interval_sec": {tc.formValue}})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303 (body %q)", rec.Code, rec.Body.String())
			}
			got, err := db.GetGlobalSetting(src.db, exit_rules.SettingDomainResolveIntervalSec, "")
			if err != nil {
				t.Fatalf("read back setting: %v", err)
			}
			if got != tc.want {
				t.Errorf("stored %q, want %q (the page must store the value the updater reads)", got, tc.want)
			}
			// Whatever landed must parse back to the same policy the flash names.
			d := exit_rules.ParseDomainResolveInterval(got)
			if loc := rec.Header().Get("Location"); loc == "" {
				t.Error("no redirect location")
			} else if !strings.Contains(decodedQuery(t, loc, "dns_interval"), exit_rules.DomainResolveIntervalLabel(d)) {
				t.Errorf("flash %q does not name the effective interval %q",
					decodedQuery(t, loc, "dns_interval"), exit_rules.DomainResolveIntervalLabel(d))
			}
		})
	}
}

func TestPostAdminSystemTestsDNSIntervalRejectsNonAdmin_B308(t *testing.T) {
	src, _, _ := setupClaimTestDB(t)
	be := &captureBackend{admin: nil} // no session
	svc := &Service{DB: src, Backend: be}
	rec := b305Post(t, svc.PostAdminSystemTestsDNSInterval, "/admin/system_tests/dns-interval",
		url.Values{"interval_sec": {"60"}})
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	got, _ := db.GetGlobalSetting(src.db, exit_rules.SettingDomainResolveIntervalSec, "")
	if got != "" {
		t.Errorf("a non-admin changed the setting to %q", got)
	}
}


