package db

// 2026-08-18: v1.3.20.6 (B136) — per-user display prefs
// (font_family / font_scale / selection_bg) unit tests.
// Skipped when SKYGATE_TEST_PG_DSN is unset (v1.3.0 is
// PG-only, no SQLite fallback).

import (
	"database/sql"
	"testing"
)

// helper: create a fresh portal_users row in the test schema.
func insertTestUser(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	var id int64
	err := d.QueryRow(
		`INSERT INTO portal_users (username, password_hash, is_admin) VALUES ($1, 'x', 0) RETURNING id`,
		username,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	return id
}

// TestGetUserDisplayPrefs_Defaults — a fresh user (no explicit
// font_family / font_scale / selection_bg set) returns the V057
// defaults: manrope / 0 / "".
func TestGetUserDisplayPrefs_Defaults(t *testing.T) {
	d := OpenForTest(t)
	if d == nil {
		return
	}
	defer d.Close()
	uid := insertTestUser(t, d, "disp_defaults")
	prefs := GetUserDisplayPrefs(d, uid)
	if prefs.FontFamily != FontFamilyManrope {
		t.Errorf("default FontFamily: got %q want %q", prefs.FontFamily, FontFamilyManrope)
	}
	if prefs.FontScale != 0 {
		t.Errorf("default FontScale: got %d want 0", prefs.FontScale)
	}
	if prefs.SelectionBg != "" {
		t.Errorf("default SelectionBg: got %q want \"\"", prefs.SelectionBg)
	}
}

// TestGetUserDisplayPrefs_Explicit — a user with all 3 prefs
// explicitly set reads back exactly what was written.
func TestGetUserDisplayPrefs_Explicit(t *testing.T) {
	d := OpenForTest(t)
	if d == nil {
		return
	}
	defer d.Close()
	uid := insertTestUser(t, d, "disp_explicit")
	if err := SetUserDisplayPrefs(d, uid, DisplayPrefs{
		FontFamily:  FontFamilyGeist,
		FontScale:   2,
		SelectionBg: "#ffcc00",
	}); err != nil {
		t.Fatalf("set prefs: %v", err)
	}
	prefs := GetUserDisplayPrefs(d, uid)
	if prefs.FontFamily != FontFamilyGeist {
		t.Errorf("FontFamily: got %q want %q", prefs.FontFamily, FontFamilyGeist)
	}
	if prefs.FontScale != 2 {
		t.Errorf("FontScale: got %d want 2", prefs.FontScale)
	}
	if prefs.SelectionBg != "#ffcc00" {
		t.Errorf("SelectionBg: got %q want #ffcc00", prefs.SelectionBg)
	}
}

// TestSetUserDisplayPrefs_RejectsUnknownFamily — unknown
// font_family strings fall back to FontFamilyManrope.
func TestSetUserDisplayPrefs_RejectsUnknownFamily(t *testing.T) {
	d := OpenForTest(t)
	if d == nil {
		return
	}
	defer d.Close()
	uid := insertTestUser(t, d, "disp_unknown")
	if err := SetUserDisplayPrefs(d, uid, DisplayPrefs{
		FontFamily:  "comicsans", // definitely not in our list
		FontScale:   1,
		SelectionBg: "",
	}); err != nil {
		t.Fatalf("set prefs: %v", err)
	}
	prefs := GetUserDisplayPrefs(d, uid)
	if prefs.FontFamily != FontFamilyManrope {
		t.Errorf("unknown family did not fall back: got %q want %q",
			prefs.FontFamily, FontFamilyManrope)
	}
}

// TestClampFontScale — out-of-range values default to 0.
// The pre-B136 plan called for clamp to min/max, but
// 0-on-error is safer for invalid scale values (the user
// can always re-pick their preferred size).
func TestClampFontScale(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{-5, 0}, // below min
		{-2, -2},
		{-1, -1},
		{0, 0},
		{1, 1},
		{2, 2},
		{5, 0},  // above max
		{99, 0}, // way above max
	}
	for _, c := range cases {
		got := ClampFontScale(c.in)
		if got != c.want {
			t.Errorf("ClampFontScale(%d): got %d want %d", c.in, got, c.want)
		}
	}
}

// TestIsValidFontFamily — all 5 known families return true,
// garbage returns false.
func TestIsValidFontFamily(t *testing.T) {
	for _, s := range []string{
		FontFamilyManrope, FontFamilyInter, FontFamilyGeist,
		FontFamilySora, FontFamilySystem,
	} {
		if !IsValidFontFamily(s) {
			t.Errorf("IsValidFontFamily(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "Comic Sans", "Helvetica", "Roboto"} {
		if IsValidFontFamily(s) {
			t.Errorf("IsValidFontFamily(%q) = true, want false", s)
		}
	}
}

// TestSetUserDisplayPrefs_PartialUpdate — setting a DisplayPrefs
// with only FontScale set doesn't reset FontFamily or SelectionBg
// to zero values (this is what the handler relies on for the
// "submit the form, change one field" UX).
func TestSetUserDisplayPrefs_PartialUpdate(t *testing.T) {
	d := OpenForTest(t)
	if d == nil {
		return
	}
	defer d.Close()
	uid := insertTestUser(t, d, "disp_partial")
	// Initial: set everything
	if err := SetUserDisplayPrefs(d, uid, DisplayPrefs{
		FontFamily:  FontFamilySora,
		FontScale:   1,
		SelectionBg: "rgba(255,0,0,0.3)",
	}); err != nil {
		t.Fatalf("initial set: %v", err)
	}
	// Now set ONLY FontScale (handler passes the existing prefs
	// back through the helper, so the test mimics that)
	cur := GetUserDisplayPrefs(d, uid)
	cur.FontScale = -1
	if err := SetUserDisplayPrefs(d, uid, cur); err != nil {
		t.Fatalf("partial set: %v", err)
	}
	prefs := GetUserDisplayPrefs(d, uid)
	if prefs.FontFamily != FontFamilySora {
		t.Errorf("FontFamily drifted on partial update: got %q want %q",
			prefs.FontFamily, FontFamilySora)
	}
	if prefs.SelectionBg != "rgba(255,0,0,0.3)" {
		t.Errorf("SelectionBg drifted on partial update: got %q",
			prefs.SelectionBg)
	}
	if prefs.FontScale != -1 {
		t.Errorf("FontScale: got %d want -1", prefs.FontScale)
	}
}
