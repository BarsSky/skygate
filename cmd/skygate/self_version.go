// self_version.go — the version string this binary reports about ITSELF when it
// registers with a cluster (B366).
//
// THE DEFECT IT FIXES (measured live on the reference standby, 2026-10-09).
//
// `skygate join` and `skygate init` built the `skygate_version` they send from
// the environment variable SKYGATE_VERSION with a literal "unknown" fallback.
// NOTHING in the CLI sets that variable — the real build identity is injected
// into this package by ldflags (`version`, `commit`, `buildTime`, see the header
// of main.go) — so every join ever run reported skygate_version="unknown" unless
// the operator happened to export it.
//
// The cost is visible exactly where the fix was supposed to help: the cluster
// row went from "(discovered via Tailscale)" to "unknown", which is the same
// placeholder under a different name — the operator still cannot see which build
// joined, and the B365 adoption ("the version of the JOIN must land on the row")
// was only half-kept. Live row after a fresh v1.5.107 join:
//
//	node-disc-svyatoslava | pending | unknown
//	cluster_audit: node_join {"skygate_version": "unknown", ...}
//
// THE RULE: report the build, and let the environment OVERRIDE it (a packager
// that injects the identity differently keeps working). It is a pure function of
// the package variables plus one env var, so it is unit-testable without a
// cluster.
package main

import (
	"os"
	"strings"
)

// selfVersion is the version this process reports about itself to a cluster.
//
// Order: SKYGATE_VERSION (explicit operator/packager override) → the build's own
// version (+commit, in the same shape /healthz and the panel footer use) → "dev".
// It never returns "unknown" for a real build: "unknown" is what the ldflags
// default `commit` carries when the binary was built without it, and it is only
// used to decide whether a "+<commit>" suffix can be added.
func selfVersion() string {
	if v := strings.TrimSpace(os.Getenv("SKYGATE_VERSION")); v != "" {
		return v
	}
	return buildVersionString()
}

// buildVersionString is the single place that turns the ldflags variables into
// the displayed version, so `skygate version`, /healthz, the panel footer and the
// cluster registration cannot drift apart.
func buildVersionString() string {
	v := strings.TrimSpace(version)
	if v == "" {
		v = "dev"
	}
	if strings.Contains(v, "-g") {
		// A `git describe`-style version already carries its own commit, so
		// appending one would print it twice (the pre-existing rule at
		// main.go's app.BuildVersion, kept here).
		return v
	}
	if c := strings.TrimSpace(commit); c != "" && c != "unknown" {
		return v + "+" + c
	}
	return v
}
