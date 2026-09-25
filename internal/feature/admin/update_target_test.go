// update_target_test.go — B76: normalizeUpdateTarget.
//
// What B76 fixed (v0.33.1.24): the "Push update" button on /admin/update is
// usable right after an orchestrator deploy, i.e. while the running build is
// the orchestrator's own `skygate-pre-update-<sha>` tag. The pre-fix helper was
//
//	if !strings.HasPrefix(target, "v") {
//	    target = "v" + target
//	}
//
// so that tag became `vskygate-pre-update-<sha>` — not a ref git knows. The
// resulting `git checkout` exit 1 looked like "the update broke" and triggered
// an automatic rollback of a perfectly healthy instance.
//
// History: the original tests were deleted with the v1.3.0 SQLite→PG purge and
// the file became a t.Skip stub (a nested-block stub, so the package still
// compiled and nothing noticed). The tests below pin the CURRENT behaviour of
// the helper as it is written in update.go — a pure function, no DB, no
// network, no sleeps.

package admin

import (
	"strings"
	"testing"
)

// TestNormalizeUpdateTarget pins every input shape the helper has to answer for.
// The table is deliberately explicit about the raw-SHA and non-main-branch rows,
// which are the two shapes a reader is likely to assume are exempt: they are not,
// and the helper's doc comment now says so (B325.1 corrected a comment that
// claimed "a SHA/branch ref is left alone").
func TestNormalizeUpdateTarget(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty_stays_empty", "", ""},
		{"clean_release_tag_untouched", "v1.5.35", "v1.5.35"},
		{"bare_semver_gets_v", "1.5.35", "v1.5.35"},
		{"four_part_semver_gets_v", "0.33.1.24", "v0.33.1.24"},
		{
			// THE B76 case: the orchestrator's pre-update tag. It must survive
			// unchanged, because `git checkout vskygate-pre-update-<sha>` is not
			// a ref and the failure used to trigger a false rollback.
			"pre_update_tag_untouched",
			"skygate-pre-update-4f2a1b9",
			"skygate-pre-update-4f2a1b9",
		},
		{"pre_update_prefix_alone_untouched", "skygate-pre-update", "skygate-pre-update"},
		{"branch_main_untouched", "main", "main"},
		{"branch_main_prefixed_untouched", "main-2", "main-2"},
		{"detached_head_untouched", "HEAD", "HEAD"},
		{"describe_label_untouched", "v1.5.0-3-gabc1234", "v1.5.0-3-gabc1234"},
		{"bare_describe_label_gets_v", "1.5.0-3-gabc1234+abc1234", "v1.5.0-3-gabc1234+abc1234"},
		{"build_label_with_plus_untouched", "ve2d0b9e+e2d0b9e", "ve2d0b9e+e2d0b9e"},
		{
			// Current behaviour for a RAW commit SHA: the "v" IS prepended. That
			// looks wrong for a SHA, but the two consumers strip it again —
			// update.GitRefForBuildLabel (Docker: isAllHex strips "v<hex>") and
			// update.NativeReleaseTagFor (native: a bare SHA is refused as "not
			// a release tag"). Pinned so a future "fix" here is a conscious one:
			// it would change what the Docker path checks out.
			"raw_sha_gets_v_and_the_consumers_strip_it",
			"e2d0b9e",
			"ve2d0b9e",
		},
		{
			// Current behaviour for a branch that is neither "main" nor "HEAD".
			// The helper has no branch detection, so the target becomes
			// "vdevelop" — the corrected doc comment in update.go states this
			// explicitly, and the reachable path (an admin POST) ends in a failed
			// `git checkout` plus the orchestrator's rollback, not a mis-deploy.
			"other_branch_gets_a_v_prefix",
			"develop",
			"vdevelop",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeUpdateTarget(tc.in); got != tc.want {
				t.Errorf("normalizeUpdateTarget(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeUpdateTarget_PreUpdateTagIsNeverVPrefixed is the B76 regression
// guard on its own, without the table: the exact live string, plus the
// "vskygate" shape the pre-fix code produced. It fails loudly with the symptom
// (git checkout) rather than only a string mismatch.
func TestNormalizeUpdateTarget_PreUpdateTagIsNeverVPrefixed(t *testing.T) {
	const live = "skygate-pre-update-4f2a1b9"

	got := normalizeUpdateTarget(live)
	if got != live {
		t.Fatalf("normalizeUpdateTarget(%q) = %q, want it untouched — a normalised pre-update tag is not a git ref, `git checkout` exits 1 and the orchestrator rolls back a healthy instance", live, got)
	}
	if strings.HasPrefix(got, "vskygate") {
		t.Errorf("result %q has the pre-B76 `vskygate-…` shape", got)
	}
	// The same guard through the path the handlers take: the form value is
	// trimmed first (PostAdminUpdateApply / PostAdminUpdatePush), then
	// normalised.
	if got := normalizeUpdateTarget(strings.TrimSpace(" " + live + " ")); got != live {
		t.Errorf("trimmed form value: normalizeUpdateTarget(...) = %q, want %q", got, live)
	}
}

// TestNormalizeUpdateTarget_Idempotent: applying the helper twice must equal
// applying it once. The orchestrator re-reads the target from the state file on
// a retry/rollback pass, so a non-idempotent helper would keep growing "v"
// prefixes ("vv1.5.35") until `git checkout` failed.
func TestNormalizeUpdateTarget_Idempotent(t *testing.T) {
	inputs := []string{
		"",
		"v1.5.35",
		"1.5.35",
		"0.33.1.24",
		"skygate-pre-update-4f2a1b9",
		"main",
		"HEAD",
		"e2d0b9e",
		"ve2d0b9e+e2d0b9e",
		"develop",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			once := normalizeUpdateTarget(in)
			twice := normalizeUpdateTarget(once)
			if twice != once {
				t.Errorf("normalizeUpdateTarget(%q) is not idempotent: once=%q twice=%q", in, once, twice)
			}
		})
	}
}
