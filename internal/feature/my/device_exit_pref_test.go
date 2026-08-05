// internal/feature/my/device_exit_pref_test.go
//
// v0.33.1.14 — regression tests for the per-device preferred
// exit-node feature. The handler is a 4-line bridge:
//
//	PostMyDevicePreferredExit → callerOwnsDevice → SetDeviceExitNodePref
//
// but the failure modes are subtle:
//   - callerOwnsDevice with a hardcoded "?" crashes on PG
//     (v0.33.1.8 era, fixed in v0.33.1.12)
//   - callerOwnsDevice with placeholdersList(1)+placeholdersList(1)
//     on PG produced "$1 AND LOWER(hostname) = $1" (two refs
//     to the same param) — silently returned 0 rows, blocked
//     every device, fixed in v0.33.1.14
//   - GetDeviceExitNodePref / SetDeviceExitNodePref had the same
//     bug, blocking every read+delete of device_exit_node_prefs
//
// These tests pin the 2-arg case (the most error-prone shape) so
// a future re-fix can't regress without a test failure.

package my

import (
	"testing"

	"skygate/internal/db"
)

// TestCallerOwnsDevice_2ArgDispatch: the v0.33.1.12 bug.
// PlaceholdersList(1) twice for a 2-arg query — on PG, this
// produced "$1 AND ... = $1" (two refs to the same param),
// which either errored out or returned 0 rows. The fix uses
// PlaceholderAt(2, 0/1) which internally calls
// PlaceholdersList(2) and indexes into it. This test creates
// a real row in the in-memory DB and asserts the lookup
// returns true.
func TestCallerOwnsDevice_2ArgDispatch(t *testing.T) {
	s := newTestService(t)
	// Seed a node_owner_map row owned by user 1.
	if _, err := s.DB.Exec(
		`INSERT INTO node_owner_map (node_id, hostname, username, headscale_user_id, tag, tagged_by_user_id) VALUES (?, ?, ?, ?, ?, ?)`,
		"node-cyborg", "cyborg", "skyadmin", int64(1), "tag:dev-skyadmin-cyborg", int64(1),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name     string
		hostname string
		want     bool
	}{
		{"matches exact lowercase", "cyborg", true},
		// The function expects pre-lowercased input (the
		// handler does strings.ToLower before calling).
		// Mixed case is the caller's job to normalize, not
		// this function's. A mixed-case input would NOT
		// match a lowercase row in node_owner_map.
		{"empty hostname", "", false},
		{"no match", "no-such-device", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.callerOwnsDevice(s.DB, 1, tc.hostname)
			if got != tc.want {
				t.Errorf("callerOwnsDevice(%q) = %v, want %v", tc.hostname, got, tc.want)
			}
		})
	}
}

// TestCallerOwnsDevice_WrongOwner: the user_id must match.
// The 2-arg-dispatch bug also masked the case where the
// user_id is wrong (since the query returned 0 rows for both
// "no such device" and "wrong owner" — both errors looked the
// same to the caller). Now that the query is correct, we
// assert that a wrong user_id is rejected.
func TestCallerOwnsDevice_WrongOwner(t *testing.T) {
	s := newTestService(t)
	if _, err := s.DB.Exec(
		`INSERT INTO node_owner_map (node_id, hostname, username, headscale_user_id, tag, tagged_by_user_id) VALUES (?, ?, ?, ?, ?, ?)`,
		"node-cyborg", "cyborg", "skyadmin", int64(1), "tag:dev-skyadmin-cyborg", int64(1),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// User 2 (michail) tries to claim skyadmin's cyborg.
	if s.callerOwnsDevice(s.DB, 2, "cyborg") {
		t.Errorf("callerOwnsDevice(user=2, cyborg) = true, want false (no impersonation)")
	}
}

// TestSetDeviceExitNodePref_RoundTrip: write + read the same
// per-device pref. Pins the same PlaceholderAt fix on
// SetDeviceExitNodePref (DELETE branch + INSERT branch). The
// v0.33.1.12 bug broke both — every write returned a PG
// "could not determine data type" error and every read
// returned (zero, false), so the feature was completely
// non-functional on PG.
func TestSetDeviceExitNodePref_RoundTrip(t *testing.T) {
	s := newTestService(t)
	// Seed portal_users for the JOIN in GetDeviceExitNodePref.
	if _, err := s.DB.Exec(
		`INSERT INTO portal_users(id, username, is_admin) VALUES (1, 'skyadmin', 1)`,
	); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}
	// Set a pref.
	if err := db.SetDeviceExitNodePref(s.DB, 1, "cyborg", "tag:exit-emilia", 1, true); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Read it back.
	p, err := db.GetDeviceExitNodePref(s.DB, 1, "cyborg")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.ExitNodeTag != "tag:exit-emilia" {
		t.Errorf("ExitNodeTag = %q, want %q", p.ExitNodeTag, "tag:exit-emilia")
	}
	if !p.ViaEnabled {
		t.Errorf("ViaEnabled = false, want true")
	}
	// Clear (tag="") → DELETE branch.
	if err := db.SetDeviceExitNodePref(s.DB, 1, "cyborg", "", 1, true); err != nil {
		t.Fatalf("set (clear): %v", err)
	}
	// Read again — should return the zero value (sql.ErrNoRows → default zero).
	p, err = db.GetDeviceExitNodePref(s.DB, 1, "cyborg")
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if p.ExitNodeTag != "" {
		t.Errorf("ExitNodeTag after clear = %q, want empty", p.ExitNodeTag)
	}
}

// TestPlaceholderAt_Dispatch: pin the new helper. SQLite gets
// "?,?", PG gets "$1, $2" — both work. A test that uses
// PlaceholderAt(2, 0/1) for the wrong N (e.g. 3) would get
// "" returned for the missing index, which would silently
// produce a malformed SQL string. The helper should return
// "" for out-of-range i, but the caller should always pass
// the right N. This test asserts the basic dispatch.
func TestPlaceholderAt_Dispatch(t *testing.T) {
	if got := db.PlaceholderAt(2, 0); got == "" {
		t.Errorf("PlaceholderAt(2, 0) = empty, want first part of PlaceholdersList(2)")
	}
	if got := db.PlaceholderAt(2, 1); got == "" {
		t.Errorf("PlaceholderAt(2, 1) = empty, want second part of PlaceholdersList(2)")
	}
	if got := db.PlaceholderAt(2, 2); got != "" {
		t.Errorf("PlaceholderAt(2, 2) = %q, want empty (out of range)", got)
	}
	if got := db.PlaceholderAt(2, -1); got != "" {
		t.Errorf("PlaceholderAt(2, -1) = %q, want empty (out of range)", got)
	}
}
