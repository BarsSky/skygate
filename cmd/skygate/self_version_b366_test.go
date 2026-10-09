// B366-B — the cluster registration must report the BUILD, not "unknown".
//
// Measured live (2026-10-09) after a fresh v1.5.107 join on the reference
// standby: `cluster_node.skygate_version = unknown` and the audit row said
// `{"skygate_version": "unknown"}`. `skygate join` / `skygate init` read
// SKYGATE_VERSION — which nothing in the CLI sets — and fell back to the literal
// "unknown", while the real identity was already in this package's ldflags
// variables. The B365 promise ("the version of the JOIN must land on the row")
// was therefore only half kept: one placeholder replaced another.
package main

import (
	"strings"
	"testing"
)

// withBuildVars swaps the ldflags variables for one test and restores them.
// They are plain package vars, so this is safe as long as the test does not run
// in parallel.
func withBuildVars(t *testing.T, v, c string, fn func()) {
	t.Helper()
	oldV, oldC := version, commit
	version, commit = v, c
	defer func() { version, commit = oldV, oldC }()
	fn()
}

func TestB366_SelfVersionReportsTheBuild(t *testing.T) {
	t.Setenv("SKYGATE_VERSION", "")
	withBuildVars(t, "v1.5.108", "abc1234", func() {
		got := selfVersion()
		if got != "v1.5.108+abc1234" {
			t.Errorf("selfVersion() = %q, want the build identity %q (the same shape /healthz shows)",
				got, "v1.5.108+abc1234")
		}
		if got == "unknown" {
			t.Error(`selfVersion() returned "unknown" — the exact placeholder the join registered on the live standby`)
		}
	})
}

func TestB366_SelfVersionNeverInventsUnknown(t *testing.T) {
	cases := []struct {
		name    string
		version string
		commit  string
		want    string
	}{
		{"a release build", "v1.5.108", "abc1234", "v1.5.108+abc1234"},
		{"no commit injected", "v1.5.108", "unknown", "v1.5.108"},
		{"empty commit", "v1.5.108", "", "v1.5.108"},
		{"a git-describe version keeps its own hash", "v1.5.108-4-gdeadbee", "deadbee", "v1.5.108-4-gdeadbee"},
		{"a dev build without ldflags", "", "", "dev"},
		{"whitespace is not a version", "  ", "  ", "dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SKYGATE_VERSION", "")
			withBuildVars(t, tc.version, tc.commit, func() {
				if got := selfVersion(); got != tc.want {
					t.Errorf("selfVersion() = %q, want %q", got, tc.want)
				}
			})
		})
	}
}

// TestB366_EnvOverrideStillWins keeps the documented escape hatch: a packager
// that injects the identity a different way must not be overruled.
func TestB366_EnvOverrideStillWins(t *testing.T) {
	t.Setenv("SKYGATE_VERSION", "  v9.9.9-packaged  ")
	withBuildVars(t, "v1.5.108", "abc1234", func() {
		if got := selfVersion(); got != "v9.9.9-packaged" {
			t.Errorf("selfVersion() = %q, want the SKYGATE_VERSION override (trimmed)", got)
		}
	})
}

// TestB366_HealthzBuildStringMatchesTheRegisteredOne keeps the two surfaces on
// ONE rule: the string the cluster row carries is the string the panel shows.
func TestB366_HealthzBuildStringMatchesTheRegisteredOne(t *testing.T) {
	t.Setenv("SKYGATE_VERSION", "")
	withBuildVars(t, "v1.5.108", "abc1234", func() {
		if buildVersionString() != selfVersion() {
			t.Errorf("buildVersionString() = %q but selfVersion() = %q — the registered version and the "+
				"displayed build must come from the same rule", buildVersionString(), selfVersion())
		}
		if !strings.Contains(buildVersionString(), "v1.5.108") {
			t.Errorf("buildVersionString() = %q, want it to contain the build version", buildVersionString())
		}
	})
}
