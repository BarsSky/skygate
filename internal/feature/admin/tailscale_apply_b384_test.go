package admin

// tailscale_apply_b384_test.go — B384's pure halves, pinned. The ladder itself needs a
// real daemon (it is exercised on the host and by the shell contract); everything that
// can be decided without one is decided here.

import (
	"os"
	"testing"
)

func TestAdvertiseTagForHostname_B384(t *testing.T) {
	cases := []struct{ in, want string }{
		{"skygate-host", "tag:dev-infra-skygate-host"},
		{"SKYGATE-HOST", "tag:dev-infra-skygate-host"},
		{"  skygate-host  ", "tag:dev-infra-skygate-host"},
		{"aro", "tag:dev-infra-aro"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := AdvertiseTagForHostname(c.in); got != c.want {
			t.Errorf("AdvertiseTagForHostname(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The retired placeholder must never come back through this path: B320/B321 made
	// `skygate-host` canonical, and a tag built from `skygate-host-1` names a node that
	// does not exist while the real node stays untagged.
	if got := AdvertiseTagForHostname("skygate-host-1"); got == "tag:dev-infra-skygate-host-1" {
		t.Errorf("the derivation still produces the legacy tag %q", got)
	}
}

func TestTailscalePrefDriftKind_B384(t *testing.T) {
	// The first string is the REAL failure text from the reference host (2026-10-14):
	// every boot and every tick, because the state carried NoStatefulFiltering=true.
	drift := "Error: changing settings via 'tailscale up' requires mentioning all\n" +
		"non-default flags. To proceed, either re-run your command with --reset or\n" +
		"use the command below to explicitly mention the current values:\n" +
		"  tailscale up --advertise-exit-node=false --netfilter-mode=on"
	cases := []struct{ name, in, want string }{
		{"the live drift text", drift, "drift"},
		// B384.1: the SECOND live text, one tick after v1.5.123 was applied on Tailscale
		// 1.102.3 — this client's `set` has no --advertise-tags, so the first rung failed
		// on every boot and every tick until the flag was made conditional.
		{"the unsupported-flag text", "flag provided but not defined: -advertise-tags", "bad-flag"},
		{"headscale refused a tag", "requested tags [tag:dev-infra-x] are invalid or not permitted", "tags-not-permitted"},
		{"the node needs auth", "Logged out.", "logged-out"},
		{"the ceiling fired", "context deadline exceeded", "timeout"},
		{"an unrelated failure", "failed to connect to local tailscaled", ""},
	}
	for _, c := range cases {
		if got := tailscalePrefDriftKind(c.in); got != c.want {
			t.Errorf("%s: tailscalePrefDriftKind(...) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHostAndTagsMatch_B384(t *testing.T) {
	cases := []struct {
		name              string
		wantHost          string
		wantTags, gotTags []string
		gotHost           string
		want              bool
	}{
		{"exact", "skygate-host", []string{"tag:dev-infra-skygate-host"}, []string{"tag:dev-infra-skygate-host"}, "skygate-host", true},
		{"case-insensitive host and tag", "SkyGate-Host", []string{"TAG:dev-infra-skygate-host"}, []string{"tag:dev-infra-skygate-host"}, "skygate-host", true},
		{"tag order is not part of the intent", "skygate-host",
			[]string{"tag:dev-infra-skygate-host", "tag:private"},
			[]string{"tag:private", "tag:dev-infra-skygate-host"}, "skygate-host", true},
		{"wrong host", "skygate-host", []string{"tag:dev-infra-skygate-host"}, []string{"tag:dev-infra-skygate-host"}, "skygate-host-1", false},
		{"a missing tag", "skygate-host",
			[]string{"tag:dev-infra-skygate-host", "tag:private"}, []string{"tag:dev-infra-skygate-host"}, "skygate-host", false},
		{"an extra tag is also a mismatch", "skygate-host",
			[]string{"tag:dev-infra-skygate-host"}, []string{"tag:dev-infra-skygate-host", "tag:private"}, "skygate-host", false},
	}
	for _, c := range cases {
		if got := hostAndTagsMatch(c.wantHost, c.wantTags, c.gotHost, c.gotTags); got != c.want {
			t.Errorf("%s: hostAndTagsMatch = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSplitTagList_B384(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"tag:dev-infra-skygate-host,tag:private", []string{"tag:dev-infra-skygate-host", "tag:private"}},
		{"tag:b tag:a", []string{"tag:a", "tag:b"}},
		{" tag:a ,, tag:b ", []string{"tag:a", "tag:b"}},
		{"", nil},
	}
	for _, c := range cases {
		got := splitTagList(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitTagList(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitTagList(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

// The explicit operator intent must win verbatim — that is the knob the release notes
// and the entrypoint's fallback both name.
func TestDesiredAdvertiseTags_ExplicitWins_B384(t *testing.T) {
	t.Setenv("SKYGATE_TS_ADVERTISE_TAGS", "tag:operator-set,tag:private")
	got := desiredAdvertiseTags("skygate-host")
	if len(got) != 2 || got[0] != "tag:operator-set" || got[1] != "tag:private" {
		t.Fatalf("explicit SKYGATE_TS_ADVERTISE_TAGS was not honoured: %v", got)
	}
	os.Unsetenv("SKYGATE_TS_ADVERTISE_TAGS")
}

// B384.1 — the `set` rung must ask the client what it supports instead of discovering it
// by failing. The live help text below is the tail of `tailscale set --help` on 1.102.3
// (the version the image ships): no --advertise-tags, while `up` has it.
func TestParseSetSupportsAdvertiseTags_B3841(t *testing.T) {
	without := `"tailscale set" allows changing specific preferences.
FLAGS
  --accept-routes, --accept-routes=false
  --advertise-routes value
  --exit-node value
  --hostname value`
	with := without + "\n  --advertise-tags value\n    	ACL tags to advertise for the node"
	cases := []struct {
		name, help string
		want       bool
	}{
		{"the live 1.102.3 help (no --advertise-tags)", without, false},
		{"a client whose set does support it", with, true},
		{"empty output (no client / unreadable help)", "", false},
	}
	for _, c := range cases {
		if got := parseSetSupportsAdvertiseTags(c.help); got != c.want {
			t.Errorf("%s: parseSetSupportsAdvertiseTags = %v, want %v", c.name, got, c.want)
		}
	}
}
