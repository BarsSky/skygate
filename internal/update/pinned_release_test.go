// pinned_release_test.go — B346: the pinned release.
//
// The pin is the answer to "versions must match across instances", so these
// tests are deliberately about the REJECTIONS: a pin that is not a release
// tag would let two instances claim the same pin and still run different
// code, which is exactly the state the setting exists to eliminate.
package update

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNormalizePinnedRelease_B346(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"clean_tag", "v1.5.94", "v1.5.94", true},
		{"bare_semver", "1.5.94", "v1.5.94", true},
		{"four_part", "0.33.1.24", "v0.33.1.24", true},
		{"four_part_with_v", "v0.33.1.24", "v0.33.1.24", true},
		{"surrounding_space_trimmed", "  v1.5.94\n", "v1.5.94", true},
		{"empty_is_not_a_pin", "", "", false},
		{"whitespace_only", "   ", "", false},
		// The shapes that must never be stored.
		{"branch", "main", "", false},
		{"raw_sha", "4b2186b", "", false},
		{"describe_label", "v1.5.94-21-gd028f842", "", false},
		{"build_label", "v1.5.94+4b2186b", "", false},
		{"pre_update_tag", "skygate-pre-update-4f2a1b9", "", false},
		{"dev_sha_label", "ve2d0b9e+e2d0b9e", "", false},
		{"two_part_is_not_a_release", "v1.5", "", false},
		{"prefix_only", "v", "", false},
		{"trailing_dot", "v1.5.94.", "", false},
		{"space_inside", "v1.5. 94", "", false},
		{"leading_zero_is_allowed_but_still_semver_shaped", "v01.05.094", "v01.05.094", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NormalizePinnedRelease(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Errorf("NormalizePinnedRelease(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestReleaseOfBuildLabel_B346(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The live primary: ldflags `vX.Y.Z+<sha>`.
		{"live_primary", "v1.5.94+4b2186b", "v1.5.94"},
		// A git-describe build.
		{"describe", "v1.5.94-21-gd028f842", "v1.5.94"},
		{"bare", "v1.5.94", "v1.5.94"},
		{"without_v", "1.5.94", "v1.5.94"},
		{"four_part_with_sha", "v0.33.1.24+abc1234", "v0.33.1.24"},
		// Must stay "unknown" — never equal to a pin.
		{"dev_sha_label", "ve2d0b9e+e2d0b9e", ""},
		{"raw_sha", "4b2186b", ""},
		{"empty", "", ""},
		{"pre_update_tag", "skygate-pre-update-4f2a1b9", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReleaseOfBuildLabel(tc.in); got != tc.want {
				t.Errorf("ReleaseOfBuildLabel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestPinnedReleaseFromDB_B346 pins the three DB shapes the workers depend
// on: a stored release is returned canonicalised, an unset/blank row means
// "follow latest", and a value no instance could check out is ignored (not
// returned verbatim, which would start a job that cannot succeed).
func TestPinnedReleaseFromDB_B346(t *testing.T) {
	db := newSettingsTestDB(t)
	// The rows are written with a direct upsert rather than db.SetGlobalSetting:
	// that helper splices db.nowUnixSQL() into the statement, and the fragment
	// is chosen by the process-wide ActiveDialect(), which only db.Open sets
	// (a bare sql.Open in a unit test leaves it on the PostgreSQL form, whose
	// EXTRACT(EPOCH FROM now()) is a syntax error on SQLite — measured here).
	// The READ path under test is unaffected: getGlobalSetting only selects.
	set := func(v string) {
		if _, err := db.Exec(`INSERT INTO global_settings (key, value, updated_at) VALUES (?, ?, 0)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, PinnedReleaseKey, v); err != nil {
			t.Fatalf("store pin %q: %v", v, err)
		}
	}

	if got := PinnedReleaseFromDB(db); got != "" {
		t.Errorf("unset row: got %q, want \"\"", got)
	}
	set("1.5.94")
	if got := PinnedReleaseFromDB(db); got != "v1.5.94" {
		t.Errorf("stored \"1.5.94\": got %q, want \"v1.5.94\"", got)
	}
	set("   ")
	if got := PinnedReleaseFromDB(db); got != "" {
		t.Errorf("blank row: got %q, want \"\"", got)
	}
	set("main")
	if got := PinnedReleaseFromDB(db); got != "" {
		t.Errorf("row holding a branch: got %q, want \"\" (a branch is not a release)", got)
	}
	set("skygate-pre-update-4f2a1b9")
	if got := PinnedReleaseFromDB(db); got != "" {
		t.Errorf("row holding a pre-update tag: got %q, want \"\"", got)
	}
	if got := PinnedReleaseFromDB(nil); got != "" {
		t.Errorf("nil DB: got %q, want \"\"", got)
	}
}

// TestPinnedTargetFor_B346 is the scheduled updater's decision table. The two
// rows that matter most are the drift DOWN (the pin is older than the build —
// applied on purpose) and "already on the pin" (no job at all), because a
// wrong answer there is either an endless reschedule loop or a missed repair.
func TestPinnedTargetFor_B346(t *testing.T) {
	cases := []struct {
		name    string
		pin     string
		build   string
		wantTag string
		wantRun bool
	}{
		{"unset_pin_never_drives_the_schedule", "", "v1.5.94+4b2186b", "", false},
		{"branch_is_not_a_pin", "main", "v1.5.94+4b2186b", "", false},
		{"already_on_the_pin_no_job", "v1.5.95", "v1.5.95+abc1234", "", false},
		{"already_on_the_pin_describe_label", "v1.5.95", "v1.5.95-3-gabc1234", "", false},
		{"pin_newer_than_build_applies", "v1.5.95", "v1.5.94+4b2186b", "v1.5.95", true},
		{"pin_OLDER_than_build_still_applies", "v1.5.90", "v1.5.94+4b2186b", "v1.5.90", true},
		{"pin_written_without_v", "1.5.95", "v1.5.94+4b2186b", "v1.5.95", true},
		// A dev/untagged build cannot be compared with the pin, so the pin is
		// applied: "unknown" must never be read as "matches".
		{"untagged_dev_build_is_not_a_match", "v1.5.95", "ve2d0b9e+e2d0b9e", "v1.5.95", true},
		{"empty_build_is_not_a_match", "v1.5.95", "", "v1.5.95", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tag, run := PinnedTargetFor(tc.pin, tc.build)
			if tag != tc.wantTag || run != tc.wantRun {
				t.Errorf("PinnedTargetFor(%q, %q) = (%q, %v), want (%q, %v)", tc.pin, tc.build, tag, run, tc.wantTag, tc.wantRun)
			}
		})
	}
}

// newSettingsTestDB is a minimal global_settings table — the same shape the
// real migration creates (key TEXT PRIMARY KEY, value TEXT, updated_at
// INTEGER), because the real write path upserts updated_at via now_unix().
func newSettingsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:pinned_release_b346?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS global_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create global_settings: %v", err)
	}
	return db
}
