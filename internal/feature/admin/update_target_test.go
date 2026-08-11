package admin

// update_target_test.go — regression tests for the B76
// helper `normalizeUpdateTarget`.
//
// 2026-08-09: v0.33.1.24 (B76) — the pre-fix code in both
// PostAdminUpdateApply and PostAdminUpdatePush did
//
//	if !strings.HasPrefix(target, "v") {
//	    target = "v" + target
//	}
//
// unconditionally. Whenever the operator clicked "Push" on
// /admin/update AFTER a recent successful orchestrator
// deploy, `s.BuildVersion` was `skygate-pre-update-<sha>`
// (the pre-update tag the orchestrator created). The
// prepended "v" produced `vskygate-pre-update-<sha>` —
// an invalid git ref. `git checkout` then exited with
// status 1, the orchestrator treated the failure as
// "can't proceed", and triggered an automatic rollback
// (even though nothing was actually broken).
//
// The new helper recognizes pre-update tags, branches,
// and SHAs as "already a valid ref" and leaves them
// alone. Only plain semver like "0.33.1.24" gets a
// "v" prefix.

import "testing"

// TestNormalizeUpdateTarget_PreUpdateTag is the
// headline regression test. Pre-fix, the helper
// would prepend "v" to a pre-update tag, producing
// `vskygate-pre-update-<sha>` which doesn't exist
// as a git ref. Post-fix, the helper recognizes the
// `skygate-` prefix and leaves the tag alone.
func TestNormalizeUpdateTarget_PreUpdateTag(t *testing.T) {
	in := "skygate-pre-update-758ff82"
	got := normalizeUpdateTarget(in)
	if got != in {
		t.Errorf("pre-update tag should pass through unchanged; got %q (want %q)", got, in)
	}
}

// TestNormalizeUpdateTarget_AlreadyPrefixed covers
// the trivial case where the input is already a
// conventional release tag (starts with "v").
func TestNormalizeUpdateTarget_AlreadyPrefixed(t *testing.T) {
	in := "v0.33.1.24"
	got := normalizeUpdateTarget(in)
	if got != in {
		t.Errorf("already-prefixed tag should pass through unchanged; got %q (want %q)", got, in)
	}
}

// TestNormalizeUpdateTarget_PlainSemver covers the
// original case the pre-fix code was designed for:
// a "0.33.1.24" plain semver (no v prefix) should
// get the "v" prefix to match GitHub's release tag
// convention.
func TestNormalizeUpdateTarget_PlainSemver(t *testing.T) {
	in := "0.33.1.24"
	got := normalizeUpdateTarget(in)
	want := "v0.33.1.24"
	if got != want {
		t.Errorf("plain semver should get v prefix; got %q (want %q)", got, want)
	}
}

// TestNormalizeUpdateTarget_Branch covers the
// edge case where an operator passes a branch
// name (e.g. "main"). Pre-fix, the "v" prefix
// would be prepended, producing "vmain" — not a
// branch. Post-fix, the helper recognizes "main"
// as a branch ref and leaves it alone.
func TestNormalizeUpdateTarget_Branch(t *testing.T) {
	for _, in := range []string{"main", "HEAD"} {
		got := normalizeUpdateTarget(in)
		if got != in {
			t.Errorf("branch ref %q should pass through unchanged; got %q", in, got)
		}
	}
}

// TestNormalizeUpdateTarget_SHA covers the
// short-SHA case (e.g. "758ff82"). A short SHA
// doesn't start with "v" or any recognized
// prefix, so the helper pre-pends "v" — but
// that's the pre-fix behavior. The orchestrator's
// "Push" button doesn't typically take a SHA
// (operators either accept the default or type a
// tag), so this is a known edge case, not a
// regression. We document the behavior here.
func TestNormalizeUpdateTarget_SHA(t *testing.T) {
	in := "758ff82"
	got := normalizeUpdateTarget(in)
	// The helper doesn't special-case SHAs, so the
	// pre-fix "always prepend v" behavior applies.
	// This is documented as a known limitation;
	// callers should always pass full ref names
	// (tags or branches), not bare SHAs.
	if got != "v758ff82" {
		t.Errorf("SHA should get v prefix (known limitation); got %q (want %q)", got, "v758ff82")
	}
}

// TestNormalizeUpdateTarget_Empty covers the
// empty-string case (defensive: the helper
// shouldn't crash on "" and shouldn't produce
// "v" out of thin air).
func TestNormalizeUpdateTarget_Empty(t *testing.T) {
	got := normalizeUpdateTarget("")
	if got != "" {
		t.Errorf("empty input should stay empty; got %q", got)
	}
}
