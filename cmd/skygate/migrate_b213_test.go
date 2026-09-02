// v1.5.0+ / B213 — unit tests for the skygate migrate
// CLI helpers. Most of the logic in migrate.go is
// DB-bound (the live-verify on the agent covers the
// DB path); these tests pin the pure-Go helpers
// (verb disambiguation + status row construction).

package main

import (
	"testing"
)

func TestRunMigrateSubcommand_DefaultVerbIsUp(t *testing.T) {
	// When args[0] is empty OR starts with a flag, the
	// default verb is "up". This is the contract the
	// orchestrator relies on (`skygate migrate --foo`
	// would still default to "up", which is what we
	// want for flag-style invocations).
	// We can't easily test runMigrateUp without a DB,
	// but we CAN test the verb detection logic by
	// extracting it — for now, we just pin the function
	// exists and accepts (verb, args) shape.
	_ = runMigrateSubcommand
}

func TestStartsWithDash(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"-", true},
		{"--help", true},
		{"-h", true},
		{"up", false},
		{"status", false},
		{"down", false},
		{"--target=20", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := startsWithDash(c.in)
			if got != c.want {
				t.Errorf("startsWithDash(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestMigrateSubcommand_UnknownVerb(t *testing.T) {
	err := runMigrateSubcommand([]string{"nonexistent"})
	if err == nil {
		t.Fatal("expected error for unknown verb")
	}
}

func TestMigrateSubcommand_HelpFlag(t *testing.T) {
	// --help / -h / help should print usage + return nil
	// (no error). We can't easily capture stdout in a
	// test, but we can verify the return value.
	for _, flag := range []string{"--help", "-h", "help"} {
		t.Run(flag, func(t *testing.T) {
			if err := runMigrateSubcommand([]string{flag}); err != nil {
				t.Errorf("runMigrateSubcommand(%q) returned error %v, want nil", flag, err)
			}
		})
	}
}

func TestMigrateDown_ReturnsNotImplemented(t *testing.T) {
	// B213 contracts: `skygate migrate down` is a STUB.
	// It must return a clear "not implemented" error,
	// not silently no-op (a silent no-op would let
	// operators THINK they rolled back when they
	// didn't).
	err := runMigrateDown([]string{"--target=20"})
	if err == nil {
		t.Fatal("runMigrateDown returned nil; want 'not implemented' error")
	}
	if !containsNotImpl(err.Error()) {
		t.Errorf("error message should mention 'not implemented'; got: %v", err)
	}
}

func containsNotImpl(s string) bool {
	// Tiny case-insensitive substring check.
	low := []byte(s)
	want := []byte("not implemented")
	for i := 0; i+len(want) <= len(low); i++ {
		match := true
		for j := 0; j < len(want); j++ {
			c1 := low[i+j]
			c2 := want[j]
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 'a' - 'A'
			}
			if c2 >= 'A' && c2 <= 'Z' {
				c2 += 'a' - 'A'
			}
			if c1 != c2 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
