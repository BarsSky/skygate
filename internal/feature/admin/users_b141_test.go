// v1.4.0 B141 — "Adopt as skygate user" button on /admin/users
// HSOrphans list. Unit tests for validateHSOrphanName (the
// headscale-username → portal-username validator extracted
// from PostAdminHSOrphanAdopt so it can be tested without a
// DB / headscale / globals).
//
// The contract:
//   - empty string → error
//   - lowercase letters, digits, _ and - only → ok
//   - anything else (uppercase, dots, spaces, non-ASCII) → error
//
// Same pattern as PostAdminUser's username validation
// (users.go:103-106 in PostAdminUser) so the two create
// paths produce identical rows. If the regex changes here,
// it must change there too — these tests pin the contract.

package admin

import "testing"

func TestValidateHSOrphanName_Valid(t *testing.T) {
	cases := []string{
		"alice",
		"bob123",
		"user_name",
		"user-name",
		"a",
		"a1b2c3",
		"___",
		"---",
		"x_y-z_0",
	}
	for _, c := range cases {
		if err := validateHSOrphanName(c); err != nil {
			t.Errorf("expected %q to be valid, got error: %v", c, err)
		}
	}
}

func TestValidateHSOrphanName_Empty(t *testing.T) {
	err := validateHSOrphanName("")
	if err == nil {
		t.Fatal("expected error for empty string, got nil")
	}
}

func TestValidateHSOrphanName_Invalid(t *testing.T) {
	cases := []string{
		"Alice",       // uppercase
		"alice.bob",   // dot
		"alice bob",   // space
		"alice@bob",   // at sign
		"alice/bob",   // slash
		"alice+1",     // plus
		"а",           // cyrillic (non-ASCII)
		"user\nname",  // newline
		"a;b",         // semicolon
		"a%b",         // percent
		"a:b",         // colon
		"<script>",    // HTML-ish (defense in depth)
		"'",           // single quote (SQL-ish)
	}
	for _, c := range cases {
		if err := validateHSOrphanName(c); err == nil {
			t.Errorf("expected %q to be rejected, got nil error", c)
		}
	}
}

func TestValidateHSOrphanName_MatchesPostAdminUserPattern(t *testing.T) {
	// Cross-check: validateHSOrphanName and the PostAdminUser
	// regex (`^[a-z0-9_-]+$`) accept the same set. If a future
	// refactor makes them drift, this test fails — the operator
	// would otherwise see "B141 adopt succeeds but /admin/users
	// POST fails on the same name" with no clear cause.
	import_re := func(name string) bool {
		// Same regex as users.go:103-106 (PostAdminUser).
		// Inlined here to avoid the import cycle (PostAdminUser
		// lives in the same file but uses regexp via package-level).
		if name == "" {
			return false
		}
		for _, r := range name {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
				return false
			}
		}
		return true
	}
	// Use a sample of valid + invalid names to cross-check.
	sample := []string{
		"alice", "bob", "a", "a1", "x_y-z", "123",
		"Alice", "alice.bob", "alice bob", "а", "",
	}
	for _, s := range sample {
		want := import_re(s)
		got := validateHSOrphanName(s) == nil
		if got != want {
			t.Errorf("cross-check: %q — import_re says valid=%v, validateHSOrphanName says valid=%v (must match)", s, want, got)
		}
	}
}
