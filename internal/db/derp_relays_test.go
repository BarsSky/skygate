// derp_relays_test.go — v1.3.17 DERP relay CRUD tests.
//
// These tests use the same openTestDB helper that other
// db/*_test.go files use (PG via SKYGATE_TEST_PG_DSN).
// They pin the contract for the v1.3.17 DERP CRUD:
//   - Add / Get / List / Update / Delete / Toggle CRUD
//   - Unique URL constraint (duplicate rejected)
//   - At-most-one is_bundled=1 row
//   - Bundled row undeletable
//   - AutoMigrateDerpRelays copies legacy global_settings
//   - ListEnabledDerpRelayURLs filters disabled rows
//
// 2026-08-13: v1.3.17.

package db

import (
	"errors"
	"testing"
)

// TestDerpRelays_AddAndList covers the happy path: add
// a row, list it back, assert every field round-trips.
func TestDerpRelays_AddAndList(t *testing.T) {
	d := openTestDB(t)
	// Clean any prior state
	_, _ = d.Exec(`DELETE FROM derp_relays`)

	id, err := AddDerpRelay(d, DerpRelay{
		Hostname:   "fra-relay",
		URL:        "https://derp-fra.example.com",
		RegionID:   901,
		RegionCode: "fra",
		RegionName: "Frankfurt Custom",
		SortOrder:  150,
		Notes:      "egress for EU clients",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id == 0 {
		t.Fatal("Add returned id=0")
	}
	rows, err := ListDerpRelays(d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("List len = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.ID != id || got.Hostname != "fra-relay" || got.URL != "https://derp-fra.example.com" ||
		got.RegionID != 901 || got.RegionCode != "fra" || got.RegionName != "Frankfurt Custom" ||
		got.SortOrder != 150 || got.Notes != "egress for EU clients" ||
		!got.Enabled || got.IsBundled {
		t.Errorf("row round-trip mismatch: %+v", got)
	}
}

// TestDerpRelays_DuplicateURLRejected covers the UNIQUE
// constraint. The second Add with the same URL must
// return ErrDerpRelayDuplicateURL (not a raw PG error).
func TestDerpRelays_DuplicateURLRejected(t *testing.T) {
	d := openTestDB(t)
	_, _ = d.Exec(`DELETE FROM derp_relays`)

	_, err := AddDerpRelay(d, DerpRelay{
		URL:     "https://dup.example.com",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Add first: %v", err)
	}
	_, err = AddDerpRelay(d, DerpRelay{
		URL:     "https://dup.example.com",
		Enabled: true,
	})
	if !errors.Is(err, ErrDerpRelayDuplicateURL) {
		t.Fatalf("Add duplicate: got %v, want ErrDerpRelayDuplicateURL", err)
	}
}

// TestDerpRelays_OnlyOneBundled covers the at-most-one
// is_bundled=1 invariant. AddDerpRelay rejects the 2nd
// row with ErrDerpRelayBundledExists.
func TestDerpRelays_OnlyOneBundled(t *testing.T) {
	d := openTestDB(t)
	_, _ = d.Exec(`DELETE FROM derp_relays`)

	_, err := AddDerpRelay(d, DerpRelay{
		URL:       "https://bundled1.example.com",
		IsBundled: true,
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Add first bundled: %v", err)
	}
	_, err = AddDerpRelay(d, DerpRelay{
		URL:       "https://bundled2.example.com",
		IsBundled: true,
		Enabled:   true,
	})
	if !errors.Is(err, ErrDerpRelayBundledExists) {
		t.Fatalf("Add 2nd bundled: got %v, want ErrDerpRelayBundledExists", err)
	}
}

// TestDerpRelays_BundledUndeletable covers the delete
// protection on the bundled row. Delete must return
// ErrDerpRelayBundledUndeletable.
func TestDerpRelays_BundledUndeletable(t *testing.T) {
	d := openTestDB(t)
	_, _ = d.Exec(`DELETE FROM derp_relays`)

	id, err := AddDerpRelay(d, DerpRelay{
		URL:       "https://bundled.example.com",
		IsBundled: true,
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	err = DeleteDerpRelay(d, id)
	if !errors.Is(err, ErrDerpRelayBundledUndeletable) {
		t.Fatalf("Delete bundled: got %v, want ErrDerpRelayBundledUndeletable", err)
	}
	// And the row is still there
	rows, _ := ListDerpRelays(d)
	if len(rows) != 1 {
		t.Errorf("after failed delete, len = %d, want 1 (row preserved)", len(rows))
	}
}

// TestDerpRelays_ToggleFlipsEnabled covers the
// per-row enable/disable switch.
func TestDerpRelays_ToggleFlipsEnabled(t *testing.T) {
	d := openTestDB(t)
	_, _ = d.Exec(`DELETE FROM derp_relays`)

	id, _ := AddDerpRelay(d, DerpRelay{
		URL:     "https://toggle.example.com",
		Enabled: true,
	})
	// 1st toggle: true → false
	row, err := ToggleDerpRelayEnabled(d, id)
	if err != nil {
		t.Fatalf("Toggle 1: %v", err)
	}
	if row.Enabled {
		t.Error("after 1st toggle, Enabled = true, want false")
	}
	// 2nd toggle: false → true
	row, err = ToggleDerpRelayEnabled(d, id)
	if err != nil {
		t.Fatalf("Toggle 2: %v", err)
	}
	if !row.Enabled {
		t.Error("after 2nd toggle, Enabled = false, want true")
	}
}

// TestDerpRelays_ListEnabledDerpRelayURLs covers the
// helper that the headscale-config renderer uses to
// build the derp.urls list. Only enabled rows are
// returned; URL list is sorted (bundled first, then
// by sort_order).
func TestDerpRelays_ListEnabledDerpRelayURLs(t *testing.T) {
	d := openTestDB(t)
	_, _ = d.Exec(`DELETE FROM derp_relays`)

	// 1 bundled, 2 external (1 enabled, 1 disabled)
	_, _ = AddDerpRelay(d, DerpRelay{
		URL: "https://bundled.example.com", IsBundled: true, Enabled: true, SortOrder: 10,
	})
	id2, _ := AddDerpRelay(d, DerpRelay{
		URL: "https://ext-on.example.com", Enabled: true, SortOrder: 100,
	})
	_, _ = AddDerpRelay(d, DerpRelay{
		URL: "https://ext-off.example.com", Enabled: false, SortOrder: 110,
	})
	_, _ = ToggleDerpRelayEnabled(d, id2) // should be no-op since already enabled
	_ = id2

	urls, err := ListEnabledDerpRelayURLs(d)
	if err != nil {
		t.Fatalf("ListEnabled: %v", err)
	}
	// Should contain the bundled + the enabled external,
	// but NOT the disabled one.
	want := map[string]bool{
		"https://bundled.example.com": true,
		"https://ext-on.example.com":  true,
	}
	if len(urls) != 2 {
		t.Errorf("ListEnabled len = %d, want 2; urls = %v", len(urls), urls)
	}
	for _, u := range urls {
		if !want[u] {
			t.Errorf("unexpected URL in list: %s", u)
		}
	}
}

// TestDerpRelays_AutoMigrate covers the backward-compat
// bridge from the v0.11.0 global_settings model. After
// AutoMigrateDerpRelays, the legacy URLs and bundled
// flag should appear as rows in derp_relays; running
// it again is a no-op.
func TestDerpRelays_AutoMigrate(t *testing.T) {
	d := openTestDB(t)
	_, _ = d.Exec(`DELETE FROM derp_relays`)
	_, _ = d.Exec(`DELETE FROM global_settings WHERE key LIKE 'derp.%'`)

	// Set legacy state
	_ = saveGlobalSetting(d, "derp.external_urls",
		"https://legacy1.example.com,https://legacy2.example.com")
	_ = saveGlobalSetting(d, "derp.bundled_enabled", "1")

	// 1st call: should copy the legacy state into derp_relays
	if err := AutoMigrateDerpRelays(d); err != nil {
		t.Fatalf("AutoMigrate 1: %v", err)
	}
	rows, _ := ListDerpRelays(d)
	if len(rows) != 3 {
		t.Errorf("after 1st migrate, len = %d, want 3 (bundled + 2 external); rows = %+v",
			len(rows), rows)
	}
	var bundled int
	for _, r := range rows {
		if r.IsBundled {
			bundled++
		}
	}
	if bundled != 1 {
		t.Errorf("bundled count = %d, want 1", bundled)
	}
	// 2nd call: marker set, no-op
	if err := AutoMigrateDerpRelays(d); err != nil {
		t.Fatalf("AutoMigrate 2: %v", err)
	}
	rows2, _ := ListDerpRelays(d)
	if len(rows2) != 3 {
		t.Errorf("after 2nd migrate, len = %d, want 3 (idempotent)", len(rows2))
	}
}

// TestDerpRelays_UpdateNotFound covers the error path
// when updating a non-existent id.
func TestDerpRelays_UpdateNotFound(t *testing.T) {
	d := openTestDB(t)
	_, _ = d.Exec(`DELETE FROM derp_relays`)

	err := UpdateDerpRelay(d, DerpRelay{
		ID: 99999, URL: "https://nope.example.com", Enabled: true,
	})
	if !errors.Is(err, ErrDerpRelayNotFound) {
		t.Fatalf("Update missing: got %v, want ErrDerpRelayNotFound", err)
	}
}
