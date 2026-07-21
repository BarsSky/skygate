// Tests for the integration config (v0.11.0). The flow is the same
// as the existing global_settings readers / writers: the read
// path falls through to env vars on miss, the write path
// upserts. Tests pin both paths.

package db

import (
	"database/sql"
	"testing"
)

// 2026-07-15: Этап 14 v14 (v0.11.0) — first-class tests for the
// IntegrationConfig Load / Save helpers. The existing global_settings
// tests (telegram_login_tokens_test.go) cover the schema, so we
// focus on the integration logic: env-var fallback, CSV
// splitting, mode-mapping.

func TestLoadIntegrations_EmptyByDefault(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	cfg, err := LoadIntegrations(d, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DERPExternalURLs != nil {
		t.Errorf("DERPExternalURLs: got %v, want nil", cfg.DERPExternalURLs)
	}
	if cfg.BundledDERP {
		t.Errorf("BundledDERP: got true, want false")
	}
	if cfg.HeadplaneMode != "bundled" {
		t.Errorf("HeadplaneMode: got %q, want bundled", cfg.HeadplaneMode)
	}
	if cfg.HeadplaneExternalURL != "" {
		t.Errorf("HeadplaneExternalURL: got %q, want empty", cfg.HeadplaneExternalURL)
	}
}

func TestLoadIntegrations_EnvFallback(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	env := map[string]string{
		"DERP_ENABLED":           "true",
		"DERP_EXTERNAL_URLS":     "https://derp1.example.com, https://derp2.example.com",
		"HEADPLANE_ENABLED":      "true",
		"HEADPLANE_EXTERNAL_URL": "",
	}
	cfg, err := LoadIntegrations(d, env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.BundledDERP {
		t.Errorf("BundledDERP: env had DERP_ENABLED=true, got false")
	}
	if got, want := cfg.DERPExternalURLs, []string{"https://derp1.example.com", "https://derp2.example.com"}; !equalStrings(got, want) {
		t.Errorf("DERPExternalURLs: got %v, want %v", got, want)
	}
	if cfg.HeadplaneMode != "bundled" {
		t.Errorf("HeadplaneMode: HEADPLANE_ENABLED=true (no external URL) should default to bundled, got %q", cfg.HeadplaneMode)
	}
}

func TestLoadIntegrations_ExternalURLFlipsModeToExternal(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	env := map[string]string{
		"HEADPLANE_ENABLED":      "true",
		"HEADPLANE_EXTERNAL_URL": "https://headplane.example.com",
	}
	cfg, err := LoadIntegrations(d, env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HeadplaneMode != "external" {
		t.Errorf("HeadplaneMode: external URL set should flip to external, got %q", cfg.HeadplaneMode)
	}
	if cfg.HeadplaneExternalURL != "https://headplane.example.com" {
		t.Errorf("HeadplaneExternalURL: got %q, want https://headplane.example.com", cfg.HeadplaneExternalURL)
	}
}

func TestLoadIntegrations_HEADPLANE_ENABLEDFalseMapsToOff(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	env := map[string]string{
		"HEADPLANE_ENABLED": "false",
	}
	cfg, err := LoadIntegrations(d, env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HeadplaneMode != "off" {
		t.Errorf("HeadplaneMode: HEADPLANE_ENABLED=false should map to off, got %q", cfg.HeadplaneMode)
	}
}

func TestLoadIntegrations_DBOverridesEnv(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	// Env says bundled with no external URLs.
	env := map[string]string{
		"DERP_ENABLED":           "true",
		"DERP_EXTERNAL_URLS":     "https://env-derp.example.com",
		"HEADPLANE_ENABLED":      "true",
		"HEADPLANE_EXTERNAL_URL": "",
	}
	// DB overrides: bundled off, two external URLs, headplane off.
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('derp.bundled_enabled', '0')`)
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('derp.external_urls', 'https://db1.example.com,https://db2.example.com')`)
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('headplane.mode', 'off')`)

	cfg, err := LoadIntegrations(d, env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BundledDERP {
		t.Errorf("BundledDERP: DB says 0, got true")
	}
	if got, want := cfg.DERPExternalURLs, []string{"https://db1.example.com", "https://db2.example.com"}; !equalStrings(got, want) {
		t.Errorf("DERPExternalURLs: got %v, want %v (DB should win over env)", got, want)
	}
	if cfg.HeadplaneMode != "off" {
		t.Errorf("HeadplaneMode: DB says off, got %q", cfg.HeadplaneMode)
	}
}

func TestLoadIntegrations_DBDerivedURLOverridesModeOff(t *testing.T) {
	// Edge case: operator set headplane.mode = "off" but
	// also pasted an external URL (saved before the v0.11.0
	// form validation). The URL presence re-flips mode to
	// "external" — better to point at the operator's URL
	// than to silently drop it.
	d := openNodeOwnerMapTestDB(t)
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('headplane.mode', 'off')`)
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('headplane.external_url', 'https://headplane.example.com')`)
	cfg, err := LoadIntegrations(d, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HeadplaneMode != "external" {
		t.Errorf("HeadplaneMode: external URL present should override mode=off, got %q", cfg.HeadplaneMode)
	}
}

func TestSaveIntegrations_RoundTrip(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	in := &IntegrationConfig{
		DERPExternalURLs:      []string{"https://derp1.example.com", "https://derp2.example.com"},
		BundledDERP:           true,
		HeadplaneMode:         "external",
		HeadplaneExternalURL:  "https://headplane.example.com",
	}
	if err := SaveIntegrations(d, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := LoadIntegrations(d, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !equalStrings(out.DERPExternalURLs, in.DERPExternalURLs) {
		t.Errorf("DERPExternalURLs round-trip: got %v, want %v", out.DERPExternalURLs, in.DERPExternalURLs)
	}
	if out.BundledDERP != in.BundledDERP {
		t.Errorf("BundledDERP round-trip: got %v, want %v", out.BundledDERP, in.BundledDERP)
	}
	if out.HeadplaneMode != in.HeadplaneMode {
		t.Errorf("HeadplaneMode round-trip: got %q, want %q", out.HeadplaneMode, in.HeadplaneMode)
	}
	if out.HeadplaneExternalURL != in.HeadplaneExternalURL {
		t.Errorf("HeadplaneExternalURL round-trip: got %q, want %q", out.HeadplaneExternalURL, in.HeadplaneExternalURL)
	}
}

func TestSaveIntegrations_EmptyClearViapersistedEmpty(t *testing.T) {
	// Operator clears the DERP URL list (saved an empty
	// slice). The DB row should exist with empty value,
	// NOT a no-op. This distinguishes "operator cleared" from
	// "operator never set" (the env-var fallback only
	// triggers in the latter case).
	d := openNodeOwnerMapTestDB(t)
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('derp.external_urls', 'https://old.example.com')`)
	in := &IntegrationConfig{HeadplaneMode: "bundled"} // DERPExternalURLs nil
	if err := SaveIntegrations(d, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := LoadIntegrations(d, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if out.DERPExternalURLs != nil {
		t.Errorf("DERPExternalURLs: got %v, want nil (operator cleared)", out.DERPExternalURLs)
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "a", []string{"a"}},
		{"two", "a,b", []string{"a", "b"}},
		{"with spaces", " https://a , https://b ", []string{"https://a", "https://b"}},
		{"empty entries", "a,,b,", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitCSV(c.in)
		if !equalStrings(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// openNodeOwnerMapTestDB returns a fresh in-memory DB with the
// global_settings table (added in v0.21). Reuses the helper from
// node_owner_map_test.go since the schema is a superset of what
// the integration tests need.
//
// 2026-07-15: Этап 14 v14 (v0.11.0) — wrapper for test readability.
func openIntegrationsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openNodeOwnerMapTestDB(t)
}
