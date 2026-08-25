// v1.5.2 (B188) — regression tests for NormalizeExitNodeTag.
//
// NormalizeExitNodeTag is the runtime equivalent of "given
// a device's hostname, return its real headscale tag (as
// stored in node_owner_map)". It's the single point of
// translation between the legacy "tag:exit-<host>" form
// (which the form templates historically synthesised and
// which is NOT a real headscale tag) and the canonical
// "tag:dev-infra-<host>" form (the post-B118 / v0.33.1.39
// convention that policy.tagOwners actually carries).
//
// The /my/devices, /admin/devices, and /my/exit-nodes
// POST handlers call this before the DB write so the
// persisted value is always canonical, regardless of what
// the template sends. The /admin/users/{id}/subnet/
// preferred-exit handler also falls back to this when it
// detects a legacy-form tag in a hand-crafted POST.
//
// These tests pin the contract:
//   1. returns the canonical tag for a known hostname
//   2. returns the canonical tag regardless of case
//   3. returns "" for an empty hostname (caller wants to
//      clear the pref — no resolution needed)
//   4. returns "" for an unknown hostname (handler
//      refuses the write to avoid inserting a ghost tag)
//   5. returns "" on sql.ErrNoRows, not an error
//
// Runs on a live PG instance via openTestDB (skipped when
// SKYGATE_TEST_PG_DSN is unset).

package db

import (
	"database/sql"
	"testing"
)

// b188SeedNodeOwner inserts a single node_owner_map row.
// The helper uses ON CONFLICT DO NOTHING on node_id so
// repeated calls with the same node_id are no-ops. The
// caller picks a unique node_id per test (we use the
// test name + a counter suffix).
func b188SeedNodeOwner(t *testing.T, d *sql.DB, nodeID int, hostname, tag string) {
	t.Helper()
	// node_owner_map has a unique-ish PK on node_id (it's
	// TEXT but practically unique per node). We pass the
	// integer as a string for portability.
	if _, err := d.Exec(
		`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname, os, device_type)
		 VALUES ($1, 99, 'infra', $2, 99, 1, $3, 'linux', 'exit-node')
		 ON CONFLICT (node_id) DO NOTHING`,
		nodeIDToString(nodeID), tag, hostname,
	); err != nil {
		t.Fatalf("b188SeedNodeOwner node_id=%d hostname=%q: %v", nodeID, hostname, err)
	}
}

// nodeIDToString converts an int to the string form
// node_owner_map expects. Kept as a tiny helper so the
// test reads as data not as fmt.Sprintf plumbing.
func nodeIDToString(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}

// TestNormalizeExitNodeTag_KnownHostname — the happy
// path. Insert a node_owner_map row, look it up by
// hostname, assert the canonical tag comes back.
func TestNormalizeExitNodeTag_KnownHostname(t *testing.T) {
	d := openTestDB(t)
	b188SeedNodeOwner(t, d, 90001, "emilia", "tag:dev-infra-emilia")

	got, err := NormalizeExitNodeTag(d, "emilia")
	if err != nil {
		t.Fatalf("NormalizeExitNodeTag: %v", err)
	}
	if got != "tag:dev-infra-emilia" {
		t.Errorf("got %q, want %q", got, "tag:dev-infra-emilia")
	}
}

// TestNormalizeExitNodeTag_CaseInsensitive — the lookup
// is case-insensitive (LOWER(hostname) in SQL). Real
// form data is lowercase (the templates lowercase the
// input), but a hand-crafted POST with mixed case must
// still resolve.
func TestNormalizeExitNodeTag_CaseInsensitive(t *testing.T) {
	d := openTestDB(t)
	b188SeedNodeOwner(t, d, 90002, "karolina", "tag:dev-infra-karolina")

	cases := []string{"karolina", "Karolina", "KAROLINA", "kArOlInA"}
	for _, c := range cases {
		got, err := NormalizeExitNodeTag(d, c)
		if err != nil {
			t.Fatalf("NormalizeExitNodeTag(%q): %v", c, err)
		}
		if got != "tag:dev-infra-karolina" {
			t.Errorf("input %q: got %q, want %q", c, got, "tag:dev-infra-karolina")
		}
	}
}

// TestNormalizeExitNodeTag_EmptyHostname — the caller
// wants to clear the pref. The function is a no-op (no
// SQL) and returns "". The handler then writes "" to
// the DB (SetDeviceExitNodePref interprets "" as DELETE).
func TestNormalizeExitNodeTag_EmptyHostname(t *testing.T) {
	d := openTestDB(t)
	got, err := NormalizeExitNodeTag(d, "")
	if err != nil {
		t.Fatalf("NormalizeExitNodeTag(\"\"): %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// TestNormalizeExitNodeTag_UnknownHostname — a hostname
// that doesn't exist in node_owner_map (typo, deleted
// device, untagged new device). Returns "" with no
// error. The handler treats "" as a hard reject and
// refuses the DB write (see device_exit_pref.go:79-83).
func TestNormalizeExitNodeTag_UnknownHostname(t *testing.T) {
	d := openTestDB(t)
	got, err := NormalizeExitNodeTag(d, "nonexistent-device-xyz")
	if err != nil {
		t.Fatalf("NormalizeExitNodeTag: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string (unknown hostname)", got)
	}
}

// TestNormalizeExitNodeTag_RealNodeOwnerMapEntry —
// end-to-end against the seed we just inserted. This
// is the same scenario the templates will exercise
// after the B188 deploy (the user picks "emilia" from
// the dropdown → form posts hostname=emilia → handler
// calls NormalizeExitNodeTag → writes tag:dev-infra-emilia).
func TestNormalizeExitNodeTag_RealNodeOwnerMapEntry(t *testing.T) {
	d := openTestDB(t)
	// Three real-looking nodes, one per infra bucket entry.
	b188SeedNodeOwner(t, d, 90010, "emilia", "tag:dev-infra-emilia")
	b188SeedNodeOwner(t, d, 90011, "karolina", "tag:dev-infra-karolina")
	b188SeedNodeOwner(t, d, 90012, "sharlotta", "tag:dev-infra-sharlotta")
	b188SeedNodeOwner(t, d, 90013, "skygate-host-1", "tag:dev-infra-skygate-host-1")

	for hostname, want := range map[string]string{
		"emilia":         "tag:dev-infra-emilia",
		"karolina":       "tag:dev-infra-karolina",
		"sharlotta":      "tag:dev-infra-sharlotta",
		"skygate-host-1": "tag:dev-infra-skygate-host-1",
	} {
		got, err := NormalizeExitNodeTag(d, hostname)
		if err != nil {
			t.Fatalf("NormalizeExitNodeTag(%q): %v", hostname, err)
		}
		if got != want {
			t.Errorf("hostname %q: got %q, want %q", hostname, got, want)
		}
	}
}
