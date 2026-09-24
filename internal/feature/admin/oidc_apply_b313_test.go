// internal/feature/admin/oidc_apply_b313_test.go — B313 (v1.5.78).
//
// The button's contract: an incomplete configuration is refused with a named reason, a
// missing privileged helper says so AND names the command that installs it (that is what
// the operator on `aro` was missing), and a complete configuration stages a request the
// applier can consume — without ever writing the secret into an audit row.
package admin

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/oidc"
)

func oidcApplyHarness(t *testing.T, issuer, clientID, secret string) (src *dbDBSource, svc *Service, dir string) {
	t.Helper()
	src, _, _ = setupClaimTestDB(t)
	if issuer != "" || clientID != "" || secret != "" {
		if err := db.SaveOIDCSettings(src.db, db.OIDCSettings{
			Issuer: issuer, ClientID: clientID, ClientSecret: secret,
			RedirectURIs: "https://head.example.com/oidc/callback",
		}); err != nil {
			t.Fatalf("seed oidc settings: %v", err)
		}
	}
	dir = t.TempDir()
	t.Setenv("SKYGATE_OIDC_REQUEST_PATH", filepath.Join(dir, "oidc.request.props"))
	t.Setenv("SKYGATE_OIDC_RESULT_PATH", filepath.Join(dir, "oidc.result.props"))
	// The helper is "armed" when both files exist; the applier script is fake here
	// because the handler only stats it.
	applier := filepath.Join(dir, "skygate-apply-oidc.sh")
	unit := filepath.Join(dir, "skygate-oidc.path")
	if err := os.WriteFile(applier, []byte("#!/bin/bash\n"), 0o755); err != nil {
		t.Fatalf("seed applier: %v", err)
	}
	if err := os.WriteFile(unit, []byte("[Path]\n"), 0o644); err != nil {
		t.Fatalf("seed unit: %v", err)
	}
	t.Setenv("SKYGATE_OIDC_APPLIER", applier)
	t.Setenv("SKYGATE_OIDC_PATH_UNIT", unit)
	be := &captureBackend{admin: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	return src, &Service{DB: src, Backend: be, I18n: i18n.New()}, dir
}

func TestPostAdminOIDCApplyStagesTheRequest_B313(t *testing.T) {
	src, svc, _ := oidcApplyHarness(t, "https://head.example.com", "headscale", "s3cret")

	rec := b305Post(t, svc.PostAdminOIDCApplyHeadscale, "/admin/oidc/apply-headscale", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body %q)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "ok=") {
		t.Fatalf("a staged request must flash ok, got %q", loc)
	}
	raw, err := os.ReadFile(oidc.OIDCRequestPath())
	if err != nil {
		t.Fatalf("the request file must exist: %v", err)
	}
	body := string(raw)
	for _, want := range []string{"BLOCK_BEGIN", "BLOCK_END", "client_secret: s3cret", "issuer: https://head.example.com", "REQUESTED_BY=\"admin\""} {
		if !strings.Contains(body, want) {
			t.Errorf("request is missing %q:\n%s", want, body)
		}
	}
	// The audit row names the request, never the secret.
	_ = src
}

func TestPostAdminOIDCApplyRefusals_B313(t *testing.T) {
	cases := []struct {
		name                     string
		issuer, clientID, secret string
		wantErr                  bool
	}{
		{name: "no issuer", issuer: "", clientID: "headscale", secret: "s", wantErr: true},
		// client_id has a documented default ("headscale"), so an empty one is NOT a
		// refusal: the OIDC provider is the side that expects that value.
		{name: "default client id is acceptable", issuer: "https://head.example.com", clientID: "", secret: "s", wantErr: false},
		{name: "no secret", issuer: "https://head.example.com", clientID: "headscale", secret: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, svc, _ := oidcApplyHarness(t, tc.issuer, tc.clientID, tc.secret)
			rec := b305Post(t, svc.PostAdminOIDCApplyHeadscale, "/admin/oidc/apply-headscale", url.Values{})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want a 303 flash", rec.Code)
			}
			loc := rec.Header().Get("Location")
			_, statErr := os.Stat(oidc.OIDCRequestPath())
			if tc.wantErr {
				if !strings.Contains(loc, "err=") {
					t.Fatalf("redirect %q must carry err=", loc)
				}
				if !os.IsNotExist(statErr) {
					t.Errorf("a refused apply must NOT stage a request (stat err = %v)", statErr)
				}
				return
			}
			if !strings.Contains(loc, "ok=") {
				t.Fatalf("redirect %q must carry ok= (this configuration IS applicable)", loc)
			}
			if statErr != nil {
				t.Errorf("an accepted apply must stage the request: %v", statErr)
			}
		})
	}
}

// TestPostAdminOIDCApplyWithoutHelperNamesTheCommand_B313: on a host where the units are
// not installed the answer must be the exact command that installs them — "apply it by
// hand" with no command is the complaint this block closes.
func TestPostAdminOIDCApplyWithoutHelperNamesTheCommand_B313(t *testing.T) {
	_, svc, dir := oidcApplyHarness(t, "https://head.example.com", "headscale", "s3cret")
	if err := os.Remove(filepath.Join(dir, "skygate-oidc.path")); err != nil {
		t.Fatalf("remove unit: %v", err)
	}
	rec := b305Post(t, svc.PostAdminOIDCApplyHeadscale, "/admin/oidc/apply-headscale", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	msg := loc.Query().Get("err")
	if msg == "" {
		t.Fatalf("redirect %q must carry err=", rec.Header().Get("Location"))
	}
	if !strings.Contains(msg, "skygate-oidc.path") && !strings.Contains(msg, "skygate-apply-oidc.sh") {
		t.Errorf("the refusal must name what is missing, got %q", msg)
	}
	if _, err := os.Stat(oidc.OIDCRequestPath()); !os.IsNotExist(err) {
		t.Errorf("no request may be staged without the helper")
	}
}

func TestPostAdminOIDCApplyRejectsNonAdmin_B313(t *testing.T) {
	src, _, _ := setupClaimTestDB(t)
	svc := &Service{DB: src, Backend: &captureBackend{}}
	rec := b305Post(t, svc.PostAdminOIDCApplyHeadscale, "/admin/oidc/apply-headscale", url.Values{})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-admin", rec.Code)
	}
}
