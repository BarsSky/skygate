// B342 — pure-function tests for the panel-side onboarding block (RR-15a).
//
// The rendered block is a shell script the operator pastes as root on a host
// that is NOT this one, so the two properties worth pinning are exactly the two
// that cannot be seen from the page: the ORDER (tailnet → install → join →
// service → approve, because the standby needs the tailnet to reach the
// primary's PostgreSQL) and the place the credentials appear (the invite token
// and the tailnet preauth key belong in the COMMANDS and nowhere near a URL,
// because a URL ends up in an access log and in the browser's history).
package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClusterOnboardSteps_OrderAndContent_B342(t *testing.T) {
	p := clusterOnboardPayload{
		Hostname:   "svyatoslava",
		InviteTok:  "sgn1.PAYLOAD.SIGNATURE",
		ExpiresAt:  "2026-10-03 12:00 UTC",
		APIURL:     "https://skygate.example.com",
		TSKey:      "hskey-auth-abc123",
		ControlURL: "https://head.example.com",
		Version:    "v1.5.94+deadbee",
	}
	steps := clusterOnboardSteps(p)
	if len(steps) != 5 {
		t.Fatalf("expected 5 steps, got %d", len(steps))
	}
	wantTitles := []string{
		"cluster.onboard_step_tailnet",
		"cluster.onboard_step_install",
		"cluster.onboard_step_join",
		"cluster.onboard_step_service",
		"cluster.onboard_step_approve",
	}
	for i, s := range steps {
		if s.Num != i+1 {
			t.Errorf("step %d has Num=%d", i, s.Num)
		}
		if s.TitleKey != wantTitles[i] {
			t.Errorf("step %d title = %q, want %q", i, s.TitleKey, wantTitles[i])
		}
		if s.NoteKey == "" {
			t.Errorf("step %d has no note", i)
		}
		if i < 4 && strings.TrimSpace(s.Command) == "" {
			t.Errorf("step %d has no command", i)
		}
	}

	tailnet, install, join := steps[0].Command, steps[1].Command, steps[2].Command
	if !strings.Contains(tailnet, "--authkey=hskey-auth-abc123") {
		t.Errorf("tailnet step must carry the minted preauth key: %q", tailnet)
	}
	if !strings.Contains(tailnet, "--netfilter-mode=nodivert") {
		t.Errorf("tailnet step must use nodir, not off (the iptables trap): %q", tailnet)
	}
	if !strings.Contains(tailnet, "--hostname=svyatoslava") {
		t.Errorf("tailnet step must carry the hostname: %q", tailnet)
	}
	// B342.2 (measured on the standby host): a fresh host has no Tailscale client,
	// and the block promised to be self-sufficient — `tailscale up` alone fails
	// with "command not found" on step 1.
	if !strings.Contains(tailnet, "command -v tailscale >/dev/null ||") ||
		!strings.Contains(tailnet, "tailscale.com/install.sh") {
		t.Errorf("tailnet step must install the client when it is missing (idempotently): %q", tailnet)
	}
	if !strings.Contains(install, "v1.5.94") {
		t.Errorf("install step must pin the primary's version: %q", install)
	}
	if strings.Contains(install, "+deadbee") {
		t.Errorf("install step must use the release TAG, not the build label: %q", install)
	}
	if !strings.Contains(install, "sha256sum -c") {
		t.Errorf("install step must verify the checksum before installing: %q", install)
	}
	if !strings.Contains(join, "sgn1.PAYLOAD.SIGNATURE") {
		t.Errorf("join step must carry the invite token: %q", join)
	}
	if !strings.Contains(join, "--write-dsn-to=/etc/skygate/dbs.env") {
		t.Errorf("join step must bootstrap the DSN: %q", join)
	}
	if !strings.Contains(join, "--api-url=https://skygate.example.com") {
		t.Errorf("join step must name the primary's API URL: %q", join)
	}
}

func TestClusterOnboardSteps_CredentialsNeverInAURL_B342(t *testing.T) {
	tok := "sgn1.SECRET.SIG"
	key := "hskey-auth-SECRET"
	p := clusterOnboardPayload{
		Hostname: "svyatoslava", InviteTok: tok, TSKey: key,
		APIURL: "https://skygate.example.com", ControlURL: "https://head.example.com",
		Version: "v1.5.94",
	}
	steps := clusterOnboardSteps(p)
	seenToken, seenKey := 0, 0
	for _, s := range steps {
		c := s.Command
		if strings.Contains(c, tok) {
			seenToken++
		}
		if strings.Contains(c, key) {
			seenKey++
		}
		// A credential must never be smuggled into a query string: those end up
		// in access logs, in the Referer header and in shell history.
		for _, bad := range []string{"?boot=", "token=" + tok, "?token=", "authkey=" + key + "&"} {
			if strings.Contains(c, bad) {
				t.Errorf("step %d smuggles a credential into a URL form: %q", s.Num, c)
			}
		}
	}
	if seenToken != 1 {
		t.Errorf("the invite token must appear in exactly one step, got %d", seenToken)
	}
	if seenKey != 1 {
		t.Errorf("the preauth key must appear in exactly one step, got %d", seenKey)
	}
}

func TestClusterOnboardSteps_NoKeyStillRendersTailnet_B342(t *testing.T) {
	// The preauth key is best-effort (headscale may be unreachable, or the
	// host's tailnet may already be up). The block must still be usable.
	steps := clusterOnboardSteps(clusterOnboardPayload{
		Hostname: "svyatoslava", InviteTok: "sgn1.A.B", APIURL: "https://p.example.com",
		ControlURL: "https://head.example.com",
	})
	if strings.Contains(steps[0].Command, "--authkey=") {
		t.Errorf("no key was minted, so no --authkey may be rendered: %q", steps[0].Command)
	}
	if !strings.Contains(steps[0].Command, "tailscale up") {
		t.Errorf("the tailnet step must survive a missing key: %q", steps[0].Command)
	}
}

func TestReleaseTagFromBuild_B342(t *testing.T) {
	cases := map[string]string{
		"v1.5.94+deadbee": "v1.5.94",
		"v1.5.94":         "v1.5.94",
		"v1.5.94 abc":     "v1.5.94",
		"":                "",
		"dev":             "",
		"1.5.94":          "",
		"v":               "",
		"v1.5.94-rc1":     "",
		"v1.5.94/evil":    "",
		"v..":             "v..",
	}
	for in, want := range cases {
		if got := releaseTagFromBuild(in); got != want {
			t.Errorf("releaseTagFromBuild(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReleaseTagFromBuild_EmptyFallsBackToLatest_B342(t *testing.T) {
	cmd := clusterOnboardInstallCommand("dev")
	if !strings.Contains(cmd, "releases/latest") {
		t.Errorf("an unknown version must resolve `latest` rather than invent a tag: %q", cmd)
	}
	if strings.Contains(cmd, "releases/download/v") {
		t.Errorf("an unknown version must NOT produce a vX.Y.Z download URL: %q", cmd)
	}
}

func TestShellQuote_B342(t *testing.T) {
	if got := shellQuote("svyatoslava-2"); got != "svyatoslava-2" {
		t.Errorf("safe value was quoted: %q", got)
	}
	if got := shellQuote("a b"); got != "'a b'" {
		t.Errorf("value with a space must be quoted, got %q", got)
	}
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Errorf("single quote must be escaped, got %q", got)
	}
	if got := shellQuote(""); got != "''" {
		t.Errorf("empty must render as an empty quoted word, got %q", got)
	}
}

func TestPrimaryURLFromRequest_B342(t *testing.T) {
	r := httptest.NewRequest("GET", "http://192.0.2.10:8080/admin/cluster", nil)
	if got := primaryURLFromRequest(r); got != "http://192.0.2.10:8080" {
		t.Errorf("plain request: got %q", got)
	}
	r2 := httptest.NewRequest("GET", "http://192.0.2.10:8080/admin/cluster", nil)
	r2.Header.Set("X-Forwarded-Proto", "https")
	if got := primaryURLFromRequest(r2); got != "https://192.0.2.10:8080" {
		t.Errorf("proxied request must follow X-Forwarded-Proto: got %q", got)
	}
}
