// system_tests_notconfigured_b405_test.go — 2026-10-21 (B405).
//
// The three-answer rule, pinned at the two places it can rot:
//
//  1. the constructor must produce a SKIP that NAMES the feature, what is missing
//     and how to fix it (a bare "not configured" is as useless as a false red row);
//  2. the new optional-feature diagnostic must actually be reachable — a test that
//     reports SKIP for a configured-but-broken provider would be the same bug in
//     the other direction.
//
// The DB round trip is a real in-memory SQLite DB (the dialect helpers dispatch on
// the active dialect, which opening the DB sets), not a mock.
package admin

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
)

func TestB405_NotConfiguredIsASkipThatNamesTheFix(t *testing.T) {
	status, out := systemTestNotConfigured("the OIDC provider", "no settings row", "set SKYGATE_OIDC_ISSUER")
	if status != SystemTestSkip {
		t.Fatalf("status = %v, want SKIP — an unconfigured optional feature is not a failure", status)
	}
	for _, want := range []string{"the OIDC provider", notConfiguredMarker, "no settings row", "set SKYGATE_OIDC_ISSUER", "nothing is wrong"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q is missing %q", out, want)
		}
	}
}

func TestB405_NotConfiguredWithoutAFixStillSkips(t *testing.T) {
	status, out := systemTestNotConfigured("the mesh feature", "no meshes", "")
	if status != SystemTestSkip {
		t.Fatalf("status = %v, want SKIP", status)
	}
	if strings.Contains(out, "to enable it:") {
		t.Errorf("an empty fix must not leave a dangling 'to enable it:' in %q", out)
	}
}

// b405OIDCDB opens an in-memory SQLite DB carrying just oidc_settings.
func b405OIDCDB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := db.OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS oidc_settings (
		id INTEGER PRIMARY KEY DEFAULT 1,
		enabled INTEGER NOT NULL DEFAULT 0,
		issuer TEXT NOT NULL DEFAULT '',
		client_id TEXT NOT NULL DEFAULT '',
		client_secret TEXT NOT NULL DEFAULT '',
		redirect_uris TEXT NOT NULL DEFAULT '',
		key_dir TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create oidc_settings: %v", err)
	}
	svc := &Service{DB: db.FixedDBSource{DB: d}}
	SetTestService(svc)
	t.Cleanup(func() { SetTestService(nil) })
	return d
}

func b405RunOIDC(t *testing.T) (SystemTestStatus, string) {
	t.Helper()
	for _, def := range TestRegistry {
		if def.Name == "integrations.oidc" {
			return def.Run(context.Background())
		}
	}
	t.Fatal("integrations.oidc is not registered in TestRegistry — the new diagnostic is unreachable")
	return SystemTestStatus(""), ""
}

func TestB405_OIDCWithoutARowSkipsNotFails(t *testing.T) {
	b405OIDCDB(t)
	status, out := b405RunOIDC(t)
	if status != SystemTestSkip {
		t.Fatalf("status = %v, want SKIP on an installation that never configured OIDC (output: %s)", status, out)
	}
	if !strings.Contains(out, notConfiguredMarker) {
		t.Errorf("the skip must carry the %q marker so the page and the contract agree; got %q", notConfiguredMarker, out)
	}
}

func TestB405_OIDCEnabledButIncompleteFails(t *testing.T) {
	d := b405OIDCDB(t)
	if _, err := d.Exec(`INSERT INTO oidc_settings (id, enabled, issuer, client_id, redirect_uris, key_dir)
		VALUES (1, 1, 'https://head.example.com', '', '', '')`); err != nil {
		t.Fatalf("seed oidc_settings: %v", err)
	}
	status, out := b405RunOIDC(t)
	if status != SystemTestFail {
		t.Fatalf("status = %v, want FAIL — the operator asked for OIDC and logins will not work (output: %s)", status, out)
	}
	for _, want := range []string{"client_id", "redirect_uris", "key_dir"} {
		if !strings.Contains(out, want) {
			t.Errorf("the failure must name %q; got %q", want, out)
		}
	}
}

func TestB405_OIDCCompletePasses(t *testing.T) {
	d := b405OIDCDB(t)
	if _, err := d.Exec(`INSERT INTO oidc_settings (id, enabled, issuer, client_id, redirect_uris, key_dir)
		VALUES (1, 1, 'https://head.example.com', 'skygate', 'https://head.example.com/oidc/callback', '/var/lib/skygate/oidc-keys')`); err != nil {
		t.Fatalf("seed oidc_settings: %v", err)
	}
	status, out := b405RunOIDC(t)
	if status != SystemTestPass {
		t.Fatalf("status = %v, want PASS (output: %s)", status, out)
	}
	if !strings.Contains(out, "https://head.example.com") {
		t.Errorf("the pass must report the issuer it verified; got %q", out)
	}
}

func TestB405_OIDCDisabledRowSkips(t *testing.T) {
	d := b405OIDCDB(t)
	if _, err := d.Exec(`INSERT INTO oidc_settings (id, enabled, issuer) VALUES (1, 0, 'https://head.example.com')`); err != nil {
		t.Fatalf("seed oidc_settings: %v", err)
	}
	status, out := b405RunOIDC(t)
	if status != SystemTestSkip {
		t.Fatalf("status = %v, want SKIP for a disabled row (output: %s)", status, out)
	}
}
