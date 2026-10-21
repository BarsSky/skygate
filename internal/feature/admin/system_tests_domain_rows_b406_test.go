// system_tests_domain_rows_b406_test.go — 2026-10-21 (B406).
//
// The containment rule, pinned where it can rot:
//
//	an enabled row that is a RANGE charges the address (104.16.0.0/12 covers
//	104.16.5.7), an exact /32 covers only itself, and a row of another device
//	contributes nothing at all.
//
// The first case is the whole block: the test used to demand an exact /32 while
// the CDN path writes the provider's range, so healthy rules were reported
// "stale or missing" (8 of 12 on the operator's installation).
package admin

import (
	"context"
	"net/netip"
	"testing"

	"skygate/internal/db"
)

func TestB406_RangeRowCoversAddress(t *testing.T) {
	prefixes := b406Prefixes([]string{"104.16.0.0/12"})
	if !b406Covers(prefixes, netip.MustParseAddr("104.16.5.7")) {
		t.Fatal("a /12 row must cover an address inside it — this is the report the block exists for")
	}
	if b406Covers(prefixes, netip.MustParseAddr("104.32.0.1")) {
		t.Fatal("an address outside the /12 must NOT be covered")
	}
}

func TestB406_ExactHostRowCoversOnlyItself(t *testing.T) {
	prefixes := b406Prefixes([]string{"203.0.113.7/32"})
	if !b406Covers(prefixes, netip.MustParseAddr("203.0.113.7")) {
		t.Fatal("a /32 row must cover its own address")
	}
	if b406Covers(prefixes, netip.MustParseAddr("203.0.113.8")) {
		t.Fatal("a /32 row must not cover a neighbour")
	}
}

func TestB406_NonCIDRValuesAreIgnored(t *testing.T) {
	prefixes := b406Prefixes([]string{"", "discord.com", "not-a-cidr", "104.16.0.0/12"})
	if len(prefixes) != 1 {
		t.Fatalf("prefixes = %v, want only the CIDR row", prefixes)
	}
}

func TestB406_InvalidAddressIsNeverCovered(t *testing.T) {
	prefixes := b406Prefixes([]string{"0.0.0.0/0"})
	if b406Covers(prefixes, netip.Addr{}) {
		t.Fatal("an invalid address must not be reported as covered — that would hide a real gap")
	}
}

// b406RuleDB creates the one table the loader reads.
func b406RuleDB(t *testing.T) *db.FixedDBSource {
	t.Helper()
	_, d, err := db.OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS device_rules (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id INTEGER NOT NULL DEFAULT 0,
		target_type TEXT NOT NULL DEFAULT '',
		target_value TEXT NOT NULL DEFAULT '',
		parent_domain TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1
	)`); err != nil {
		t.Fatalf("create device_rules: %v", err)
	}
	for _, row := range []struct {
		device  int64
		enabled int
		value   string
	}{
		{7, 1, "104.16.0.0/12"},
		{7, 0, "198.51.100.0/24"}, // disabled: must not count
		{7, 1, "203.0.113.7/32"},
		{8, 1, "192.0.2.0/24"}, // another device: must not count
	} {
		if _, err := d.Exec(`INSERT INTO device_rules (device_id, target_type, target_value, enabled)
			VALUES (?, 'subnet', ?, ?)`, row.device, row.value, row.enabled); err != nil {
			t.Fatalf("seed %s: %v", row.value, err)
		}
	}
	return &db.FixedDBSource{DB: d}
}

func TestB406_LoaderReadsOnlyThisDeviceAndOnlyEnabledRows(t *testing.T) {
	src := b406RuleDB(t)
	got, err := b406DevicePrefixes(context.Background(), src.Current(), 7)
	if err != nil {
		t.Fatalf("b406DevicePrefixes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("prefixes = %v, want the two enabled rows of device 7", got)
	}
	for _, want := range []string{"104.16.0.0/12", "203.0.113.7/32"} {
		if !b406Covers(got, netip.MustParseAddr(firstAddrIn(t, want))) {
			t.Errorf("%s is not covered by the loaded set %v", want, got)
		}
	}
	if b406Covers(got, netip.MustParseAddr("198.51.100.5")) {
		t.Error("a DISABLED row charged an address")
	}
	if b406Covers(got, netip.MustParseAddr("192.0.2.5")) {
		t.Error("another device's row charged an address")
	}
}

// firstAddrIn returns the first host address of a prefix, as a string.
func firstAddrIn(t *testing.T, prefix string) string {
	t.Helper()
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		t.Fatalf("parse %s: %v", prefix, err)
	}
	return p.Addr().String()
}
