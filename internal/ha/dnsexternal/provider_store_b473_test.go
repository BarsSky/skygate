// provider_store_b473_test.go — B473 (2026-11-14).
//
// The storage half of the multi-provider seam, executed on a real (SQLite) database:
//
//  1. A row round-trips: save -> list -> get -> active -> delete, with the provider-specific fields
//     encrypted as one JSON object (so a Cloudflare token never needs a column).
//  2. The historical alias and the honest name are the SAME row: `external` and `regru` cannot become
//     two credentials for one zone (the UNIQUE index would silently decide which one wins).
//  3. THE LEGACY ROW IS IMPORTED EXACTLY ONCE: an install upgrading from v1.5.x has `ha.dns.*` filled
//     and this table empty, so the operator must not retype anything — and an operator who deletes
//     every row must NOT be re-imported into a resurrected credential.
package dnsexternal

import (
	"strings"
	"testing"

	"skygate/internal/db"
)

func b473Store(t *testing.T) *Store {
	t.Helper()
	d, err := db.OpenFastSQLiteForTest(t.TempDir() + "/b473.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return NewStore(d, strings.Repeat("ab", 32))
}

// TestB473_RowRoundTrip — save, list, get, active, delete.
func TestB473_RowRoundTrip(t *testing.T) {
	s := b473Store(t)
	id, err := s.SaveProviderRow(ProviderRegru, "Example.COM", map[string]string{
		"login": "user@example.com", "password": "alt-secret",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if id == 0 {
		t.Fatal("save returned id 0")
	}
	rows, err := s.ListProviderRows()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Zone != "example.com" {
		t.Errorf("zone stored as %q, want lowercased example.com", rows[0].Zone)
	}
	if rows[0].Field("password") != "alt-secret" {
		t.Errorf("password did not round-trip through the ciphertext: %q", rows[0].Field("password"))
	}
	if !rows[0].Active || !rows[0].Enabled {
		t.Errorf("the only row must be enabled and active: %+v", rows[0])
	}
	got, err := s.GetProviderRow(id)
	if err != nil || got.Field("login") != "user@example.com" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if err := s.DeleteProviderRow(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows, _ = s.ListProviderRows()
	if len(rows) != 0 {
		t.Fatalf("rows after delete = %d, want 0", len(rows))
	}
}

// TestB473_AliasIsTheSameRow — `external` is a historical spelling of `regru`, not a second provider.
func TestB473_AliasIsTheSameRow(t *testing.T) {
	s := b473Store(t)
	if _, err := s.SaveProviderRow(ProviderExternal, "example.com", map[string]string{"login": "a"}); err != nil {
		t.Fatalf("save via alias: %v", err)
	}
	if _, err := s.SaveProviderRow(ProviderRegru, "example.com", map[string]string{"login": "b"}); err != nil {
		t.Fatalf("save via honest name: %v", err)
	}
	rows, err := s.ListProviderRows()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (one credential per zone, whichever name was used)", len(rows))
	}
	if rows[0].Field("login") != "b" {
		t.Errorf("the second save did not update the row: login=%q", rows[0].Field("login"))
	}
}

// TestB473_LegacyImportedExactlyOnce — the upgrade path, and the "operator deleted everything" path.
func TestB473_LegacyImportedExactlyOnce(t *testing.T) {
	s := b473Store(t)
	// The pre-B473 state: five settings keys, empty table.
	for k, v := range map[string]string{
		ProviderKey: ProviderExternal,
		LoginKey:    "legacy@example.com",
		ZoneKey:     "Example.COM",
	} {
		if err := db.SetGlobalSetting(s.DB, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	pwEnc, err := db.EncryptForColumn("legacy-alt-password", s.SecretKey)
	if err != nil {
		t.Fatalf("encrypt legacy password: %v", err)
	}
	if err := db.SetGlobalSetting(s.DB, PasswordKey, pwEnc); err != nil {
		t.Fatalf("seed password: %v", err)
	}

	imported, err := s.ImportLegacyOnce()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !imported {
		t.Fatal("the legacy row was not imported")
	}
	rows, err := s.ListProviderRows()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the imported one", len(rows))
	}
	if rows[0].Provider != ProviderRegru || rows[0].Zone != "example.com" {
		t.Errorf("imported row = %s/%s, want regru/example.com", rows[0].Provider, rows[0].Zone)
	}
	if rows[0].Field("password") != "legacy-alt-password" {
		t.Errorf("the legacy password did not survive the import: %q", rows[0].Field("password"))
	}

	// Second call: nothing more to do (the flag is set in the same pass).
	if again, err := s.ImportLegacyOnce(); err != nil || again {
		t.Fatalf("second import = (%v, %v), want (false, nil)", again, err)
	}

	// The operator deletes every row: the legacy settings are HISTORY and must not come back.
	if err := s.DeleteProviderRow(rows[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if resurrected, err := s.ImportLegacyOnce(); err != nil || resurrected {
		t.Fatalf("import after deletion = (%v, %v), want (false, nil) — a deleted credential must not be resurrected", resurrected, err)
	}
	rows, _ = s.ListProviderRows()
	if len(rows) != 0 {
		t.Fatalf("rows = %d after deletion, want 0", len(rows))
	}
}

// TestB473_ActiveSelectionIsHonoured — the operator's choice beats "the first enabled row".
func TestB473_ActiveSelectionIsHonoured(t *testing.T) {
	s := b473Store(t)
	idA, err := s.SaveProviderRow(ProviderRegru, "a.example.com", map[string]string{"login": "a"})
	if err != nil {
		t.Fatalf("save A: %v", err)
	}
	idB, err := s.SaveProviderRow(ProviderRegru, "b.example.com", map[string]string{"login": "b"})
	if err != nil {
		t.Fatalf("save B: %v", err)
	}
	if err := s.SetActiveRow(idB); err != nil {
		t.Fatalf("set active: %v", err)
	}
	active, err := s.ActiveProviderRow()
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if active.ID != idB {
		t.Errorf("active row = %d, want %d", active.ID, idB)
	}
	if err := s.SetActiveRow(idA); err != nil {
		t.Fatalf("set active A: %v", err)
	}
	if active, _ = s.ActiveProviderRow(); active.ID != idA {
		t.Errorf("active row = %d after switching, want %d", active.ID, idA)
	}
}

// TestB473_EmptyStateIsNotAnError — a fresh install has no row, and that is `IsNoActiveRow`, not a
// failure that the panel would render as a malfunction.
func TestB473_EmptyStateIsNotAnError(t *testing.T) {
	s := b473Store(t)
	_, err := s.ActiveProviderRow()
	if err == nil {
		t.Fatal("expected the empty state")
	}
	if !IsNoActiveRow(err) {
		t.Fatalf("empty state reported as %v, which the panel would show as a malfunction", err)
	}
}
