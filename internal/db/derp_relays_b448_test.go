// derp_relays_b448_test.go — B448 (2026-10-30).
//
// «нет возможности удалить релей что выключен». Two defects, both at the data layer:
//
//  1. DeleteDerpRelay refused EVERY row with is_bundled=1, and the reference install holds
//     TWO of them (the public :443 relay and the LAN :8443 entry point, both auto-
//     registered), so the disabled duplicate could not be removed from the panel at all —
//     the operator's only way out was SQL on the live database.
//
//  2. IsBundledDerpRelayEnabled read `SELECT enabled … WHERE is_bundled = 1 LIMIT 1` with
//     no ORDER BY. With one bundled row enabled and the other disabled that is a coin
//     flip, so the on/off state of the derper container (and the switch on the page) could
//     disagree with the operator's choice.
package db

import (
	"database/sql"
	"errors"
	"testing"
)

// b448BundledPair inserts TWO bundled rows for one host.
//
// The second one goes in DIRECTLY: AddDerpRelay refuses it by design (the at-most-one guard,
// TestDerpRelays_OnlyOneBundled), and the live install ended up with two anyway — through the
// auto-registration paths. The rule has to cope with the world as it is, not with the world
// the guard intended.
func b448BundledPair(t *testing.T, firstEnabled, secondEnabled bool) (*sql.DB, int64, int64) {
	t.Helper()
	d := openTestDB(t)
	_, _ = d.Exec(`DELETE FROM derp_relays`)

	first, err := AddDerpRelay(d, DerpRelay{
		Hostname:  "relay.example.com",
		URL:       "https://relay.example.com:443",
		RegionID:  900,
		IsBundled: true,
		Enabled:   firstEnabled,
	})
	if err != nil {
		t.Fatalf("Add first bundled: %v", err)
	}
	var second int64
	if err := d.QueryRow(`INSERT INTO derp_relays
		(hostname, url, region_id, region_code, region_name, is_bundled, enabled, sort_order, notes, created_at, updated_at)
		VALUES ('relay.example.com', 'https://relay.example.com:8443', 900, 'mow', 'Moscow', 1, $1, 11, '', 0, 0)
		RETURNING id`, boolToInt(secondEnabled)).Scan(&second); err != nil {
		t.Fatalf("insert second bundled row: %v", err)
	}
	return d, first, second
}

// TestB448_BundledDuplicateIsDeletable is the operator's exact complaint: the DISABLED
// duplicate must go, and the last bundled row must stay (it is the container switch).
func TestB448_BundledDuplicateIsDeletable(t *testing.T) {
	d, first, second := b448BundledPair(t, true, false)

	if err := DeleteDerpRelay(d, second); err != nil {
		t.Fatalf("delete the disabled bundled duplicate: %v — the operator still has no way out", err)
	}
	rows, _ := ListDerpRelays(d)
	if len(rows) != 1 || rows[0].ID != first {
		t.Fatalf("after deleting the duplicate, rows = %+v, want the surviving bundled row only", rows)
	}

	if err := DeleteDerpRelay(d, first); !errors.Is(err, ErrDerpRelayBundledUndeletable) {
		t.Fatalf("delete the LAST bundled row: got %v, want ErrDerpRelayBundledUndeletable", err)
	}
	// And the refusal left the row alone.
	rows, _ = ListDerpRelays(d)
	if len(rows) != 1 {
		t.Fatalf("after the refused delete, len = %d, want 1 (row preserved)", len(rows))
	}
}

// TestB448_DeleteOrdinaryRowStillWorks guards the fix against over-reach: a non-bundled row
// never had a restriction and must keep none.
func TestB448_DeleteOrdinaryRowStillWorks(t *testing.T) {
	d := openTestDB(t)
	_, _ = d.Exec(`DELETE FROM derp_relays`)
	id, err := AddDerpRelay(d, DerpRelay{URL: "https://external.example.com", Enabled: true})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := DeleteDerpRelay(d, id); err != nil {
		t.Fatalf("delete an ordinary row: %v", err)
	}
}

// TestB448_BundledSwitch is the second half: with a duplicated pair the
// container switch must read the operator's choice regardless of which row the planner
// returns first.
func TestB448_BundledSwitch(t *testing.T) {
	cases := []struct {
		name          string
		first, second bool
		want          bool
		why           string
	}{
		{"on_off", true, false, true, "the live shape: the disabled duplicate must not mask the enabled relay"},
		{"off_on", false, true, true, "symmetry: the other row is just as much a bundled row"},
		{"both_off", false, false, false, "the operator switched every bundled relay off — the container must be off"},
		{"both_on", true, true, true, "trivially on"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _, _ := b448BundledPair(t, c.first, c.second)
			got, err := IsBundledDerpRelayEnabled(d)
			if err != nil {
				t.Fatalf("IsBundledDerpRelayEnabled: %v", err)
			}
			if got != c.want {
				t.Fatalf("IsBundledDerpRelayEnabled = %v, want %v (%s)", got, c.want, c.why)
			}
		})
	}
}

// TestB448_NoBundledRowMeansDisabled pins the empty case: the old code answered
// sql.ErrNoRows → false, and the counting query must keep that answer (it returns 0).
func TestB448_NoBundledRowMeansDisabled(t *testing.T) {
	d := openTestDB(t)
	_, _ = d.Exec(`DELETE FROM derp_relays`)
	if _, err := AddDerpRelay(d, DerpRelay{URL: "https://external.example.com", Enabled: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := IsBundledDerpRelayEnabled(d)
	if err != nil {
		t.Fatalf("IsBundledDerpRelayEnabled: %v", err)
	}
	if got {
		t.Fatal("no bundled row at all, yet the container switch reads ON")
	}
}
