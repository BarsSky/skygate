package admin

// devices_owner_b498_test.go — B498 (2026-11-19).
//
// The two live defects this block fixes were both VISIBLE and both wrong:
//
//  1. `olesya` and `a71` rendered a relay badge while `nothing-phone-2`,
//     `skybars-secure` and `s24-fe--ned` rendered a dash — because the owner was
//     derived by cutting `tag:dev-<user>-<host>` at the LAST hyphen, so any
//     hostname containing a hyphen resolved to uid 0. A dash also hid the assign
//     control.
//  2. the badge that olesya/a71 showed was a PER-USER preference, which never
//     applies to a tagged node, so the page presented a user setting as if it
//     belonged to those two devices — «почему продолжает выставляться неверная
//     настройка?»
//
// These tests pin both: the resolver reads the ownership row, and the user-level
// row lands in ONE page-level notice instead of N device cells.

import (
	"testing"

	"skygate/internal/db"
)

// TestB498_OwnerSurvivesAHyphenatedHostname is the exact regression: the tag
// strings below are the live ones, and every device must resolve to the same
// owner the ownership row names.
func TestB498_OwnerSurvivesAHyphenatedHostname(t *testing.T) {
	uidByUsername := map[string]int64{"skyadmin": 1, "michail": 6}
	names := map[int64]string{1: "skyadmin", 6: "michail", 99: "infra", 10: "daniil"}

	cases := []struct {
		host     string
		username string
		taggedBy int64
		wantUID  int64
		wantName string
	}{
		// the two that already worked (no hyphen in the hostname)
		{"olesya", "michail", 6, 6, "michail"},
		{"a71", "skyadmin", 1, 1, "skyadmin"},
		// the three that rendered a dash — a hyphen in the hostname broke the split
		{"nothing-phone-2", "michail", 6, 6, "michail"},
		{"skybars-secure", "skyadmin", 1, 1, "skyadmin"},
		{"s24-fe--ned", "skyadmin", 1, 1, "skyadmin"},
		// headscale's synthetic owner (B287): the human who tagged it is the only
		// attribution left on the row
		{"workpc", "tagged-devices", 10, 10, ""},
		// a row nobody can attribute stays a dash rather than a guess
		{"orphan", "", 0, 0, ""},
		// a stale tagged_by id that names no portal user is not an attribution
		{"stale", "", 777, 0, ""},
	}
	for _, tc := range cases {
		row := db.NodeOwner{
			Hostname: tc.host, Username: tc.username, TaggedByUserID: tc.taggedBy,
			Tag: "tag:dev-" + tc.username + "-" + tc.host,
		}
		gotUID, _ := b498OwnerUID(row, uidByUsername, names)
		if gotUID != tc.wantUID {
			t.Fatalf("%s: owner uid = %d, want %d", tc.host, gotUID, tc.wantUID)
		}
	}
}

// TestB498_OwnerIsCaseAndSpaceInsensitive — headscale and node_owner_map disagree
// about case and padding, and a key that differs by either is an owner that
// silently disappears.
func TestB498_OwnerIsCaseAndSpaceInsensitive(t *testing.T) {
	uid, name := b498OwnerUID(
		db.NodeOwner{Hostname: "x", Username: "  SkyAdmin "},
		map[string]int64{"skyadmin": 1},
		map[int64]string{1: "skyadmin"},
	)
	if uid != 1 || name != "skyadmin" {
		t.Fatalf("owner = (%d,%q), want (1,skyadmin)", uid, name)
	}
}

// TestB498_UserPrefIsStatedOnceNotPerDevice is the second half: the per-user row
// that governs nothing is reported ONCE for the page, and only when it would have
// pinned and has no untagged device left to apply to.
func TestB498_UserPrefIsStatedOnceNotPerDevice(t *testing.T) {
	// michail owns three tagged devices; skyadmin owns one untagged + one tagged;
	// daniil owns nothing on this page.
	userByHost := map[string]int64{
		"olesya": 6, "nothing-phone-2": 6, "basic": 6,
		"skybars": 1, "old-laptop": 1,
	}
	tagByHost := map[string]string{
		"olesya": "tag:dev-michail-olesya", "nothing-phone-2": "tag:dev-michail-nothing-phone-2",
		"basic":   "tag:dev-michail-basic",
		"skybars": "tag:dev-skyadmin-skybars", "old-laptop": "",
	}
	names := map[int64]string{1: "skyadmin", 6: "michail", 10: "daniil"}
	prefs := []db.ExitNodePref{
		{UserID: 6, Username: "michail", ExitNodeTag: "tag:dev-infra-karolina", ViaEnabled: true},
		{UserID: 1, Username: "skyadmin", ExitNodeTag: "tag:dev-infra-emilia", ViaEnabled: true},
		{UserID: 10, Username: "daniil", ExitNodeTag: "tag:dev-infra-karolina", ViaEnabled: true},
	}
	got := b498LostUserPrefs(prefs, userByHost, tagByHost, names)
	if len(got) != 1 {
		t.Fatalf("reported %d user rows, want exactly 1 (only michail): %+v", len(got), got)
	}
	if got[0].Username != "michail" || got[0].Relay != "karolina" || got[0].Devices != 3 {
		t.Fatalf("michail row = %+v, want {michail karolina 3}", got[0])
	}

	// skyadmin has one UNTAGGED device, so the row still has something to apply to
	// and must NOT be reported as lost.
	for _, l := range got {
		if l.Username == "skyadmin" {
			t.Fatalf("skyadmin reported as lost although it owns an untagged device")
		}
	}
	// daniil owns no device at all: absence of evidence is not a finding.
	for _, l := range got {
		if l.Username == "daniil" {
			t.Fatalf("daniil reported with no devices on the page")
		}
	}
}

// TestB498_UserPrefWithStrictModeOffIsNotReported — a row that does not even try to
// pin is a stored setting with strict mode off, not a lost preference. Reporting it
// would recreate the noise this block removes.
func TestB498_UserPrefWithStrictModeOffIsNotReported(t *testing.T) {
	got := b498LostUserPrefs(
		[]db.ExitNodePref{{UserID: 6, Username: "michail", ExitNodeTag: "tag:dev-infra-karolina", ViaEnabled: false}},
		map[string]int64{"olesya": 6},
		map[string]string{"olesya": "tag:dev-michail-olesya"},
		map[int64]string{6: "michail"},
	)
	if len(got) != 0 {
		t.Fatalf("a via-disabled user row was reported as lost: %+v", got)
	}
}

// TestB498_UserPrefNameFallsBackToThePortalTable — `user_exit_node_prefs.username`
// is a denormalised copy and can be empty; the page must not print «→ karolina»
// without saying WHO.
func TestB498_UserPrefNameFallsBackToThePortalTable(t *testing.T) {
	got := b498LostUserPrefs(
		[]db.ExitNodePref{{UserID: 6, Username: "", ExitNodeTag: "tag:dev-infra-karolina", ViaEnabled: true}},
		map[string]int64{"olesya": 6},
		map[string]string{"olesya": "tag:dev-michail-olesya"},
		map[int64]string{6: "michail"},
	)
	if len(got) != 1 || got[0].Username != "michail" {
		t.Fatalf("rows = %+v, want one row named michail", got)
	}
}
