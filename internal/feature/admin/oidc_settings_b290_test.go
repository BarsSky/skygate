// internal/feature/admin/oidc_settings_b290_test.go — B290 (2026-09-22).
//
// The operator's report: «нет опять же удобного выставления включения OIDC — пока
// нет в env строчки нельзя никак настроить, но из описания непонятно что и как
// добавлять, и удобнее было бы иметь автономный механизм включения без правки в
// ручную env».
//
// The form had existed since v0.75, and the DB row was read at boot — but three
// things made the feature feel absent:
//
//  1. saving said "restart skygate (/admin/update) to apply" — nothing the
//     operator could observe changed;
//  2. the page rendered the ENV values, not the effective ones, so a saved row
//     was invisible (and a DB `enabled=0` was ignored by the boot code, which
//     only honoured the env off-switch);
//  3. the client_secret was stored in the clear.
//
// These tests pin the resolution rules, the encryption round trip and the
// immediate-apply contract.
package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/config"
	"skygate/internal/db"
)

// newOIDCTestService builds a Service over a migrated SQLite DB with an admin
// caller and a captured OIDC applier.
func newOIDCTestService(t *testing.T, cfg *config.Config) (*Service, *dbDBSource, *[]string) {
	t.Helper()
	src, _, _ := setupClaimTestDB(t)
	applied := []string{}
	svc := &Service{
		Backend: &claimStubBackend{admin: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}},
		DB:      src,
		Cfg:     cfg,
		OIDCApplier: func(issuer, clientID, clientSecret, redirectURIs string, enabled bool) {
			applied = append(applied, issuer+"|"+clientID+"|"+clientSecret+"|"+redirectURIs+"|"+boolWord(enabled))
		},
	}
	return svc, src, &applied
}

func TestB290_EffectiveSettingsPreferTheUIOverEnv(t *testing.T) {
	cfg := &config.Config{
		OIDCIssuerURL:    "https://env.example.com",
		OIDCClientID:     "env-client",
		OIDCClientSecret: "env-secret",
		OIDCRedirectURIs: "https://env.example.com/oidc/callback",
	}
	svc, src, _ := newOIDCTestService(t, cfg)

	// No row yet: env wins, and the default client_id applies when env is empty.
	eff := svc.effectiveOIDCSettings()
	if eff.Issuer != "https://env.example.com" || eff.Source["issuer"] != "env" {
		t.Fatalf("issuer = %q (source %q), want the env value", eff.Issuer, eff.Source["issuer"])
	}
	if eff.SecretSource != "env" || eff.ClientSecret != "env-secret" {
		t.Errorf("secret source = %q, want env", eff.SecretSource)
	}
	if !eff.Enabled {
		t.Error("provider should be enabled from the env issuer alone")
	}

	// A saved row WINS for every field it sets.
	if err := db.SaveOIDCSettings(src.db, db.OIDCSettings{
		Enabled: true, Issuer: "https://ui.example.com", ClientID: "ui-client",
		ClientSecret: "ui-secret", RedirectURIs: "https://ui.example.com/oidc/callback",
	}); err != nil {
		t.Fatalf("SaveOIDCSettings: %v", err)
	}
	eff = svc.effectiveOIDCSettings()
	if eff.Issuer != "https://ui.example.com" || eff.Source["issuer"] != "ui" {
		t.Errorf("issuer = %q (source %q), want the UI value", eff.Issuer, eff.Source["issuer"])
	}
	if eff.ClientID != "ui-client" || eff.Source["client_id"] != "ui" {
		t.Errorf("client_id = %q (source %q), want the UI value", eff.ClientID, eff.Source["client_id"])
	}
	if eff.SecretSource != "ui" || eff.ClientSecret != "ui-secret" {
		t.Errorf("secret = %q (source %q), want the UI value", eff.ClientSecret, eff.SecretSource)
	}
	if eff.Source["redirect_uris"] != "ui" {
		t.Errorf("redirect source = %q, want ui", eff.Source["redirect_uris"])
	}

	// A saved row with enabled=0 turns the provider off (the form's own switch) —
	// the boot path honours it too (main.go reads the same field).
	if err := db.SaveOIDCSettings(src.db, db.OIDCSettings{Enabled: false, Issuer: "https://ui.example.com"}); err != nil {
		t.Fatalf("SaveOIDCSettings(disabled): %v", err)
	}
	eff = svc.effectiveOIDCSettings()
	if eff.Enabled {
		t.Error("a saved row with enabled=0 must disable the provider")
	}
	if eff.Source["enabled"] != "ui" {
		t.Errorf("enabled source = %q, want ui", eff.Source["enabled"])
	}
}

func TestB290_EnvOffSwitchBeatsTheForm(t *testing.T) {
	cfg := &config.Config{OIDCIssuerURL: "https://ui.example.com", OIDCEnabledEnv: "false"}
	svc, src, _ := newOIDCTestService(t, cfg)
	if err := db.SaveOIDCSettings(src.db, db.OIDCSettings{Enabled: true, Issuer: "https://ui.example.com"}); err != nil {
		t.Fatalf("SaveOIDCSettings: %v", err)
	}
	eff := svc.effectiveOIDCSettings()
	if !eff.EnvDisabled {
		t.Error("EnvDisabled must be reported so the page can explain why the switch does nothing")
	}
	if eff.Enabled {
		t.Error("SKYGATE_OIDC_ENABLED=false must beat the DB row")
	}
}

func TestB290_SecretIsEncryptedAtRestAndStillReadable(t *testing.T) {
	cfg := &config.Config{}
	svc, src, _ := newOIDCTestService(t, cfg)
	svc.SecretKeyHex = "00000000000000000000000000000000000000000000000000000000000000ff"

	if err := db.SaveOIDCSettingsEncrypted(src.db, db.OIDCSettings{
		Enabled: true, Issuer: "https://gate.example.com", ClientID: "headscale", ClientSecret: "s3cr3t-value",
	}, svc.SecretKeyHex); err != nil {
		t.Fatalf("SaveOIDCSettingsEncrypted: %v", err)
	}
	raw, err := db.GetOIDCSettings(src.db)
	if err != nil {
		t.Fatalf("GetOIDCSettings: %v", err)
	}
	if strings.Contains(raw.ClientSecret, "s3cr3t-value") {
		t.Fatalf("the stored client_secret is PLAINTEXT (%q) — a DB dump would leak it", raw.ClientSecret)
	}
	if !db.OIDCSecretIsEncrypted(raw.ClientSecret) {
		t.Errorf("stored secret = %q, want the enc:v1: marker", raw.ClientSecret)
	}
	back, err := db.GetOIDCSettingsDecrypted(src.db, svc.SecretKeyHex)
	if err != nil {
		t.Fatalf("GetOIDCSettingsDecrypted: %v", err)
	}
	if back.ClientSecret != "s3cr3t-value" {
		t.Errorf("decrypted secret = %q, want s3cr3t-value", back.ClientSecret)
	}

	// A legacy plaintext row (written before B290) still reads.
	if _, err := src.db.Exec(`UPDATE oidc_settings SET client_secret = 'legacy-plain' WHERE id = 1`); err != nil {
		t.Fatalf("update legacy secret: %v", err)
	}
	legacy, err := db.GetOIDCSettingsDecrypted(src.db, svc.SecretKeyHex)
	if err != nil {
		t.Fatalf("GetOIDCSettingsDecrypted(legacy): %v", err)
	}
	if legacy.ClientSecret != "legacy-plain" {
		t.Errorf("legacy plaintext secret = %q, want it passed through", legacy.ClientSecret)
	}

	// A wrong key is a NAMED error, never a silent empty secret (which would take
	// the OIDC provider down without saying why).
	other := &Service{SecretKeyHex: "11111111111111111111111111111111111111111111111111111111111111ff"}
	if err := db.SaveOIDCSettingsEncrypted(src.db, db.OIDCSettings{Enabled: true, Issuer: "x", ClientID: "c", ClientSecret: "top-secret"}, svc.SecretKeyHex); err != nil {
		t.Fatalf("SaveOIDCSettingsEncrypted: %v", err)
	}
	_ = other
	if _, err := db.GetOIDCSettingsDecrypted(src.db, "22222222222222222222222222222222222222222222222222222222222222ff"); err == nil {
		t.Error("decrypting with the wrong key must return an error")
	}
}

func TestB290_SaveAppliesWithoutARestart(t *testing.T) {
	svc, src, applied := newOIDCTestService(t, &config.Config{})
	form := url.Values{
		"enabled":       {"1"},
		"issuer":        {"https://gate.example.com/"},
		"client_id":     {"headscale"},
		"client_secret": {"top-secret"},
		"redirect_uris": {"https://head.example.com/oidc/callback"},
		"key_dir":       {"/var/lib/skygate/oidc-keys"},
	}
	req := httptest.NewRequest("POST", "/admin/oidc", strings.NewReader(form.Encode())).WithContext(context.Background())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminOIDC(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "err=") {
		t.Fatalf("save reported an error: %s", loc)
	}
	if len(*applied) != 1 {
		t.Fatalf("OIDCApplier calls = %d, want exactly 1 (saving must apply immediately)", len(*applied))
	}
	got := (*applied)[0]
	if !strings.HasPrefix(got, "https://gate.example.com|headscale|top-secret|https://head.example.com/oidc/callback|true") {
		t.Errorf("applied config = %q, want the saved values with the trailing slash trimmed and enabled=true", got)
	}
	row, err := db.GetOIDCSettingsDecrypted(src.db, svc.SecretKeyHex)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.Issuer != "https://gate.example.com" || row.ClientID != "headscale" || !row.Enabled {
		t.Errorf("stored row = %+v, want the submitted values", row)
	}
}

func TestB290_SaveRefusesAnIncompleteEnable(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
		want string
	}{
		{
			name: "enable without issuer",
			form: url.Values{"enabled": {"1"}, "client_id": {"headscale"}, "client_secret": {"s"}},
			want: "issuer",
		},
		{
			name: "enable without secret",
			form: url.Values{"enabled": {"1"}, "issuer": {"https://gate.example.com"}, "client_id": {"headscale"}},
			want: "secret",
		},
		{
			name: "issuer is not a URL",
			form: url.Values{"enabled": {"1"}, "issuer": {"gate.example.com"}, "client_secret": {"s"}},
			want: "http(s)",
		},
		{
			name: "redirect uri is not a URL",
			form: url.Values{"enabled": {"1"}, "issuer": {"https://gate.example.com"}, "client_secret": {"s"},
				"redirect_uris": {"head.example.com/oidc/callback"}},
			want: "redirect URI",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _, applied := newOIDCTestService(t, &config.Config{})
			req := httptest.NewRequest("POST", "/admin/oidc", strings.NewReader(c.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			svc.PostAdminOIDC(rec, req)
			loc := rec.Header().Get("Location")
			if !strings.Contains(loc, "err=") {
				t.Fatalf("an invalid form was accepted (redirect %q) — the operator gets no reason", loc)
			}
			if !strings.Contains(strings.ToLower(loc), strings.ToLower(c.want)) && !strings.Contains(strings.ToLower(unescape(t, loc)), strings.ToLower(c.want)) {
				t.Errorf("error %q does not mention %q", loc, c.want)
			}
			if len(*applied) != 0 {
				t.Errorf("an invalid form was APPLIED to the running provider: %v", *applied)
			}
		})
	}
}

// TestB290_SaveKeepsTheStoredSecretWhenTheFieldIsEmpty: the form never echoes the
// secret, so an empty field must keep it (otherwise every unrelated save would
// silently break OIDC).
func TestB290_SaveKeepsTheStoredSecretWhenTheFieldIsEmpty(t *testing.T) {
	svc, src, applied := newOIDCTestService(t, &config.Config{})
	svc.SecretKeyHex = "33333333333333333333333333333333333333333333333333333333333333ff"
	if err := db.SaveOIDCSettingsEncrypted(src.db, db.OIDCSettings{
		Enabled: true, Issuer: "https://gate.example.com", ClientID: "headscale", ClientSecret: "kept-secret",
	}, svc.SecretKeyHex); err != nil {
		t.Fatalf("seed: %v", err)
	}
	form := url.Values{
		"enabled":   {"1"},
		"issuer":    {"https://gate.example.com"},
		"client_id": {"headscale"},
		// client_secret deliberately absent
	}
	req := httptest.NewRequest("POST", "/admin/oidc", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminOIDC(rec, req)
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "err=") {
		t.Fatalf("save with an empty secret field was refused: %s", loc)
	}
	if len(*applied) != 1 || !strings.Contains((*applied)[0], "kept-secret") {
		t.Fatalf("applied = %v, want the STORED secret to be reused", *applied)
	}
	row, err := db.GetOIDCSettingsDecrypted(src.db, svc.SecretKeyHex)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.ClientSecret != "kept-secret" {
		t.Errorf("stored secret = %q, want kept-secret", row.ClientSecret)
	}
}

func unescape(t *testing.T, raw string) string {
	t.Helper()
	i := strings.Index(raw, "err=")
	if i < 0 {
		return raw
	}
	dec, err := url.QueryUnescape(raw[i+4:])
	if err != nil {
		return raw
	}
	return dec
}
