// B237.21 (v1.5.2+) — unit tests for the regapi-credentials
// CLI subcommand.
//
// Scope: the pure helpers + the error-path handling.
// The full happy path (open DB, encrypt, write) needs a
// live PG; the integration test in the deploy-e2e job
// covers that. The pure-unit tests here pin the contract
// the operator can rely on (env-var fallback precedence,
// password-file vs --password, masking in show, etc.)
// so a future refactor doesn't regress them silently.

package main

import (
	"os"
	"strings"
	"testing"
)

func TestMaskSecret_Short(t *testing.T) {
	// Short passwords: mask the whole thing (don't
	// leak the length).
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"x", "*"},
		{"ab", "**"},
		{"12345678", "********"},
	}
	for _, c := range cases {
		if got := maskSecret(c.in); got != c.want {
			t.Errorf("maskSecret(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskSecret_Long(t *testing.T) {
	// Long passwords: first 2 + stars + last 2.
	cases := []struct {
		in   string
		want string
	}{
		{"123456789", "12*****89"},
		{"abcdefghijklmnop", "ab************op"},
		{"verysecretpassword12345", "ve*******************45"},
	}
	for _, c := range cases {
		if got := maskSecret(c.in); got != c.want {
			t.Errorf("maskSecret(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCertSummary(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "(empty — no cert configured)"},
		{"a", "(1 bytes, PEM-format)"},
		{strings.Repeat("x", 1400), "(1400 bytes, PEM-format)"},
	}
	for _, c := range cases {
		if got := certSummary(c.in); got != c.want {
			t.Errorf("certSummary(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEnvOr(t *testing.T) {
	// Save + restore the env var we touch (CI runs the
	// test in parallel with other tests that may set it).
	const testKey = "SKYGATE_TEST_ENVOR_KEY_B237_21"
	// Clean up after the test
	t.Cleanup(func() { os.Unsetenv(testKey) })

	// 1. Env var unset → returns def
	os.Unsetenv(testKey)
	if got := envOr(testKey, "default-value"); got != "default-value" {
		t.Errorf("envOr(unset) = %q, want default-value", got)
	}

	// 2. Env var set → returns env value
	os.Setenv(testKey, "from-env")
	if got := envOr(testKey, "default-value"); got != "from-env" {
		t.Errorf("envOr(set) = %q, want from-env", got)
	}

	// 3. Env var set to empty string → returns def
	// (matches os.Getenv's behavior: "" counts as
	// "set but empty" for the purpose of this helper,
	// because the operator's intent is usually "I
	// want the env to take effect when set" — the
	// default only kicks in when the env is unset.
	// The slight quirk: setting SKYGATE_FOO="" makes
	// the helper fall back to def, which is the
	// SAME as unsetting. This matches what most
	// shell scripts do with ${VAR:-default}.)
	os.Setenv(testKey, "")
	if got := envOr(testKey, "default-value"); got != "default-value" {
		t.Errorf("envOr(set-to-empty) = %q, want default-value", got)
	}
}

func TestRunRegAPICredsSubcommand_UnknownVerb(t *testing.T) {
	// The dispatcher should reject unknown verbs with
	// a clear error message that lists the valid verbs.
	err := runRegAPICredsSubcommand([]string{"bogus"})
	if err == nil {
		t.Fatal("expected error for unknown verb")
	}
	if !strings.Contains(err.Error(), "unknown verb") {
		t.Errorf("error should mention 'unknown verb', got: %v", err)
	}
	if !strings.Contains(err.Error(), "set") || !strings.Contains(err.Error(), "show") ||
		!strings.Contains(err.Error(), "test") || !strings.Contains(err.Error(), "delete") {
		t.Errorf("error should list the valid verbs, got: %v", err)
	}
}

func TestRunRegAPICredsSubcommand_MissingVerb(t *testing.T) {
	// The dispatcher should reject empty verb with
	// a clear error message that lists the valid verbs.
	err := runRegAPICredsSubcommand([]string{})
	if err == nil {
		t.Fatal("expected error for missing verb")
	}
	if !strings.Contains(err.Error(), "missing verb") {
		t.Errorf("error should mention 'missing verb', got: %v", err)
	}
	if !strings.Contains(err.Error(), "set") {
		t.Errorf("error should list 'set' as a valid verb, got: %v", err)
	}
}
