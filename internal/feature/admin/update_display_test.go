package admin

// update_display_test.go — regression tests for the v1.1.0
// version-display fix.
//
// The bug (live on the operator's VM as of 2026-08-12):
//   BuildVersion = "v1.0.0-15-gd6f7b6b+d6f7b6b"
//   → /admin/update "Current" field showed the same string
//   → operator saw "v1.0.0-15-gd6f7b6b+d6f7b6b → v1.3.2"
//     which reads like "downgrade from v1.3.2 to v1.0.0-15-g..."
//
// Root cause: entrypoint.sh passes both
//   -X main.version=$(git describe --tags --always)
//   -X main.commit=$(git rev-parse --short HEAD)
// and BuildVersion = version + "+" + commit produced
// "v1.0.0-15-gd6f7b6b+d6f7b6b" — the commit hash twice
// (once in the "-g..." suffix from git describe, once in
// "+..." suffix from main.commit). The semver comparison
// strips the "+..." part so IsNewer is correct, but the
// display shows the ugly concatenation.
//
// Fix:
//   1. cmd/skygate/main.go no longer adds "+<commit>" if
//      `version` already contains a "-g<hex>" suffix.
//   2. internal/feature/admin/update.go strips the "+<commit>"
//      for the displayed "Current" label (defense-in-depth)
//      and shows the "15-gd6f7b6b" suffix in a separate
//      "build:" subtitle below the version.
//
// These tests pin the displayVersionForUpdate helper
// (the rendering layer's defense-in-depth) and the build
// label format contract.

import "testing"

func TestDisplayVersionForUpdate(t *testing.T) {
	cases := []struct {
		name           string
		build          string
		wantCurrent    string
		wantSubtitle   string
	}{
		{
			// The live broken case on operator's VM as of
			// 2026-08-12. Must not contain the duplicate
			// "+d6f7b6b" suffix in Current.
			name:         "live_broken_v1.0.0-15-gd6f7b6b+d6f7b6b",
			build:        "v1.0.0-15-gd6f7b6b+d6f7b6b",
			wantCurrent:  "v1.0.0-15-gd6f7b6b",
			wantSubtitle: "15-gd6f7b6b",
		},
		{
			// Clean tag (no build suffix, no "+commit").
			name:         "clean_tag_v1.1.0",
			build:        "v1.1.0",
			wantCurrent:  "v1.1.0",
			wantSubtitle: "",
		},
		{
			// Clean tag with "+commit" but no "-g" suffix
			// (custom build without git describe). Common
			// for `go build -ldflags "-X main.version=v1.1.0
			// -X main.commit=abc1234"` from CI.
			name:         "tag_with_plus_commit_only",
			build:        "v1.1.0+78d4559",
			wantCurrent:  "v1.1.0",
			wantSubtitle: "",
		},
		{
			// "dev" placeholder (no ldflags, dev machine).
			// Must get a leading "v".
			name:         "dev_no_ldflags",
			build:        "dev",
			wantCurrent:  "vdev",
			wantSubtitle: "",
		},
		{
			// Empty BuildVersion (shouldn't happen, but be safe).
			name:         "empty",
			build:        "",
			wantCurrent:  "v",
			wantSubtitle: "",
		},
		{
			// Numeric-only (no "v" prefix). This is the legacy
			// pre-v0.32 format. Should be normalized to "v...".
			name:         "legacy_no_v",
			build:        "1.0.0",
			wantCurrent:  "v1.0.0",
			wantSubtitle: "",
		},
		{
			// v1.1.0 itself (the latest tag on origin as of
			// 2026-08-12). Real-world case.
			name:         "tag_v1.1.0",
			build:        "v1.1.0",
			wantCurrent:  "v1.1.0",
			wantSubtitle: "",
		},
		{
			// Future version with deeper build suffix.
			// v1.4.0-3-gabc1234+abc1234 → "v1.4.0-3-gabc1234"
			// + "3-gabc1234" subtitle.
			name:         "deep_build_v1.4.0",
			build:        "v1.4.0-3-gabc1234+abc1234",
			wantCurrent:  "v1.4.0-3-gabc1234",
			wantSubtitle: "3-gabc1234",
		},
		{
			// v2.0.0-beta.1+meta (prerelease + custom suffix).
			// subtitle should still be empty because the
			// suffix is "beta.1" which doesn't contain "g".
			name:         "prerelease_no_g",
			build:        "v2.0.0-beta.1+meta",
			wantCurrent:  "v2.0.0-beta.1",
			wantSubtitle: "",
		},
		{
			// v2.0.0-beta.1-3-gdeadbeef+deadbeef. Both "-g"
			// and "v" prefix present. Subtitle = full
			// "beta.1-3-gdeadbeef" suffix.
			name:         "prerelease_with_g",
			build:        "v2.0.0-beta.1-3-gdeadbeef+deadbeef",
			wantCurrent:  "v2.0.0-beta.1-3-gdeadbeef",
			wantSubtitle: "beta.1-3-gdeadbeef",
		},
		{
			// Defensive: "+" without commit. Should not crash.
			// (Shouldn't happen in practice — entrypoint.sh
			// either passes a real commit or "unknown".)
			name:         "lone_plus",
			build:        "v1.0.0+",
			wantCurrent:  "v1.0.0",
			wantSubtitle: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCurrent, gotSubtitle := displayVersionForUpdate(tc.build)
			if gotCurrent != tc.wantCurrent {
				t.Errorf("Current = %q, want %q", gotCurrent, tc.wantCurrent)
			}
			if gotSubtitle != tc.wantSubtitle {
				t.Errorf("Subtitle = %q, want %q", gotSubtitle, tc.wantSubtitle)
			}
		})
	}
}

// TestDisplayVersionForUpdate_Idempotent — calling the
// helper on its own output is a no-op. This catches any
// "Current gets prepended v on second call" regression.
func TestDisplayVersionForUpdate_Idempotent(t *testing.T) {
	inputs := []string{
		"v1.0.0-15-gd6f7b6b+d6f7b6b",
		"v1.1.0+78d4559",
		"v1.1.0",
		"dev",
		"",
	}
	for _, in := range inputs {
		cur1, sub1 := displayVersionForUpdate(in)
		cur2, sub2 := displayVersionForUpdate(cur1 + "+" + "x") // synthetic +commit
		if cur2 != cur1 {
			t.Errorf("non-idempotent for %q: first=%q, second=%q", in, cur1, cur2)
		}
		if sub2 != sub1 {
			t.Errorf("non-idempotent subtitle for %q: first=%q, second=%q", in, sub1, sub2)
		}
	}
}
