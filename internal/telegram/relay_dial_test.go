// relay_dial_test.go — B356.1 (2026-10-07).
//
// These tests pin the PROPERTIES of the relay inventory and the tunnel argv, not the
// implementation: a stored value that could become an ssh OPTION never reaches a
// candidate, the tailnet address wins over a public one (B310), the operator's
// preferred relay is first and the proven ones come next (B353), and the token cannot
// survive any string this block produces.
//
// They never run ssh: the argv is asserted as data (headscale.SSHTunnelArgv is pure),
// and the transport-level behaviour lives in egress_fallback_test.go with an injected
// dialer.
package telegram

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

func TestB356_1_UnsafeTargetNeverBecomesACandidate(t *testing.T) {
	rows := []db.ExitServer{
		{Hostname: "evil-option", Enabled: true, SSHTarget: "-oProxyCommand=sh -c id"},
		{Hostname: "evil-shell", Enabled: true, SSHTarget: "root@100.64.0.9; rm -rf /"},
		{Hostname: "evil-subst", Enabled: true, SSHTarget: "root@100.64.0.9$(id)"},
		{Hostname: "good", Enabled: true, SSHTarget: "root@100.64.0.2:18022"},
	}
	inv := buildRelayCandidates(rows, []string{"/ssh-sync/skygate_sync"}, nil, func(string) bool { return true })
	if len(inv.Candidates) != 1 {
		t.Fatalf("only the well-formed row may be a candidate, got %d: %+v", len(inv.Candidates), inv.Candidates)
	}
	if inv.Candidates[0].Hostname != "good" {
		t.Fatalf("the wrong row survived: %+v", inv.Candidates[0])
	}
	if len(inv.Skipped) != 3 {
		t.Fatalf("every refused row must be named, got %d skips: %v", len(inv.Skipped), inv.Skipped)
	}
	// The argv builder is the last gate, and it must refuse the same values even if a
	// future caller forgets the inventory check (B266 class).
	for _, bad := range []string{"-oProxyCommand=sh -c id", "root@100.64.0.9; rm -rf /", "root@100.64.0.9$(id)", " ", "--"} {
		if _, err := headscale.SSHTunnelArgv("/ssh-sync/skygate_sync", bad, "api.telegram.org", 443); err == nil {
			t.Errorf("SSHTunnelArgv accepted the unsafe relay target %q", bad)
		}
	}
	// An unusable key path is refused by name too — never silently replaced by another
	// identity.
	for _, badKey := range []string{"", "/keys/my key", "/keys/a;rm -rf /", "/keys/$(id)"} {
		if _, err := headscale.SSHTunnelArgv(badKey, "root@100.64.0.2", "api.telegram.org", 443); err == nil {
			t.Errorf("SSHTunnelArgv accepted the unusable key path %q", badKey)
		}
	}
}

func TestB356_1_TunnelArgvIsATunnelNotARemoteCommand(t *testing.T) {
	args, err := headscale.SSHTunnelArgv("/ssh-sync/skygate_sync", "root@100.64.0.2:18022", "api.telegram.org", 443)
	if err != nil {
		t.Fatalf("a normal relay must produce an argv: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-i /ssh-sync/skygate_sync",
		"-o BatchMode=yes",
		"-o StrictHostKeyChecking=accept-new",
		"-o ProxyCommand=none",
		"-o IdentitiesOnly=yes",
		"-p 18022",
		"-W api.telegram.org:443",
		"-- root@100.64.0.2",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("tunnel argv lost %q: %v", want, args)
		}
	}
	// -W is an OPTION: after `--` ssh would run it as a REMOTE COMMAND and nothing
	// would be tunnelled at all. This ordering is the whole feature.
	iW, iDD := -1, -1
	for i, a := range args {
		switch {
		case a == "-W" && iW < 0:
			iW = i
		case a == "--" && iDD < 0:
			iDD = i
		}
	}
	if iW < 0 || iDD < 0 || iW > iDD {
		t.Fatalf("-W must precede `--` (otherwise ssh runs it on the relay): %v", args)
	}
	// The destination must be the LAST argv element of the -W pair, so no remote
	// command can follow it.
	if args[len(args)-1] != "root@100.64.0.2" {
		t.Fatalf("the relay must be the final argument (nothing may follow it as a command): %v", args)
	}
	if d, err := headscale.NormalizeTunnelDest("api.telegram.org", 0); err == nil {
		t.Errorf("port 0 must be refused, got %q", d)
	}
}

func TestB356_1_IPv6OnBothEndsIsBracketed(t *testing.T) {
	if got := renderRelayTarget("root", "fd7a:115c:a1e0::3", "18022"); got != "root@[fd7a:115c:a1e0::3]:18022" {
		t.Fatalf("an IPv6 relay must be bracketed (B353.1), got %q", got)
	}
	if !headscale.IsSafeSSHTarget(renderRelayTarget("root", "fd7a:115c:a1e0::3", "18022")) {
		t.Fatal("the bracketed IPv6 relay must pass the B266 shape gate")
	}
	args, err := headscale.SSHTunnelArgv("/k", renderRelayTarget("root", "fd7a:115c:a1e0::3", ""), "2001:db8::1", 443)
	if err != nil {
		t.Fatalf("IPv6 on both ends must be usable: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-W [2001:db8::1]:443") || !strings.Contains(joined, "-- root@[fd7a:115c:a1e0::3]") {
		t.Fatalf("IPv6 literals must be bracketed on both ends: %v", args)
	}
}

func TestB356_1_TailnetAddressWinsAndProvenOrderDecides(t *testing.T) {
	row := db.ExitServer{
		Hostname:    "karolina",
		Enabled:     true,
		TailscaleIP: "100.64.0.2,fd7a:115c:a1e0::2",
		SSHTarget:   "root@203.0.113.9:18022",
		SSHPort:     "18022",
	}
	target, how := relayAddressFor(row)
	if target != "root@100.64.0.2:18022" || !strings.Contains(how, "tailnet") {
		t.Fatalf("the tailnet address must win (B310), got %q (%s)", target, how)
	}
	// No tailnet address: the operator's target is used verbatim.
	target, how = relayAddressFor(db.ExitServer{Hostname: "x", Enabled: true, SSHTarget: "root@203.0.113.9"})
	if target != "root@203.0.113.9" || !strings.Contains(how, "ssh_target") {
		t.Fatalf("ssh_target must be the second rung, got %q (%s)", target, how)
	}
	// Neither: not a candidate at all.
	if target, _ := relayAddressFor(db.ExitServer{Hostname: "x", Enabled: true}); target != "" {
		t.Fatalf("a row with no address must not become a candidate, got %q", target)
	}

	cands := []relayCandidate{
		{Hostname: "sharlotta"},
		{Hostname: "emilia", Proven: true},
		{Hostname: "karolina", Proven: true},
	}
	got := orderRelayCandidates(cands, "sharlotta")
	if len(got) != 3 || got[0].Hostname != "sharlotta" {
		t.Fatalf("the operator's preferred relay must be first, got %+v", got)
	}
	if got[1].Hostname != "emilia" || got[2].Hostname != "karolina" {
		t.Fatalf("proven relays must follow, hostname-ordered, got %+v", got)
	}
	// No preference: proven first, then hostname.
	got = orderRelayCandidates(cands, "")
	if got[0].Hostname != "emilia" || got[1].Hostname != "karolina" || got[2].Hostname != "sharlotta" {
		t.Fatalf("proven-first order without a preference, got %+v", got)
	}
}

func TestB356_1_DisabledRowAndMissingKeyAreNamed(t *testing.T) {
	rows := []db.ExitServer{
		{Hostname: "off", Enabled: false, SSHTarget: "root@100.64.0.9"},
		{Hostname: "nokey", Enabled: true, SSHTarget: "root@100.64.0.9"},
	}
	inv := buildRelayCandidates(rows, []string{"/missing"}, nil, func(string) bool { return false })
	if len(inv.Candidates) != 0 {
		t.Fatalf("no row may become a candidate, got %+v", inv.Candidates)
	}
	joined := strings.Join(inv.Skipped, " | ")
	for _, want := range []string{"off: disabled", "nokey: no usable ssh private key"} {
		if !strings.Contains(joined, want) {
			t.Errorf("skipped rows must name the reason (%q missing): %s", want, joined)
		}
	}
}

func TestB356_1_OffSwitchOnlyAcceptsAnExplicitOff(t *testing.T) {
	for _, on := range []string{"", " ", "1", "true", "yes", "on", "wat"} {
		if !relayFallbackEnabled(on) {
			t.Errorf("%q must leave the fallback ON (a typo must not disable the net)", on)
		}
	}
	for _, off := range []string{"0", "false", "FALSE", "no", "off", "disabled", " off "} {
		if relayFallbackEnabled(off) {
			t.Errorf("%q must disable the fallback", off)
		}
	}
}

func TestB356_1_TokenNeverSurvivesRedaction(t *testing.T) {
	const token = "123456789:AAFakeTokenForTests_0123456789"
	url := "https://api.telegram.org/bot" + token + "/getUpdates"
	if got := RedactToken(url, token); strings.Contains(got, token) {
		t.Fatalf("the known token survived redaction: %q", got)
	}
	if got := RedactToken(url, ""); strings.Contains(got, token) {
		t.Fatalf("the token PATTERN must be redacted even without the token value: %q", got)
	}
	if got := RedactToken(url, ""); !strings.Contains(got, "bot"+redactedToken) {
		t.Fatalf("the redacted form must still identify the API path: %q", got)
	}
	err := redactError(fmt.Errorf("Get %q: context deadline exceeded (Client.Timeout exceeded while awaiting headers)", url))
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("a transport error must not carry the token: %v", err)
	}
	req, nerr := newTestRequest("GET", url)
	if nerr != nil {
		t.Fatal(nerr)
	}
	if got := describeRequest(req); strings.Contains(got, token) {
		t.Fatalf("describeRequest leaked the token: %q", got)
	} else if !strings.Contains(got, "getUpdates") {
		t.Fatalf("describeRequest must still name the Bot API method: %q", got)
	}
}

func TestB356_1_SelectRelayAttemptsSkipsCooldownAndKeepsOneProbe(t *testing.T) {
	cands := []relayCandidate{{Hostname: "a"}, {Hostname: "b"}, {Hostname: "c"}}
	now := nowFixture()
	// "a" failed a second ago (cooling down), "b" failed three minutes ago (its cooldown
	// expired) and it is the WARM relay, "c" never failed.
	cooldown := map[string]time.Time{"a": now.Add(-time.Second), "b": now.Add(-3 * time.Minute)}

	got := selectRelayAttempts(cands, cooldown, now, "b", 2*time.Minute, 3)
	if len(got) != 2 || got[0].Hostname != "b" || got[1].Hostname != "c" {
		t.Fatalf("the cooling-down relay must be skipped and the warm one tried first, got %+v", got)
	}
	// Every relay inside its cooldown, but the least recently failed one is past HALF
	// the cooldown: exactly ONE probe is allowed, so a relay that recovered during the
	// cooldown is found without hot-looping ssh.
	all := map[string]time.Time{
		"a": now.Add(-90 * time.Second),
		"b": now.Add(-30 * time.Second),
		"c": now.Add(-45 * time.Second),
	}
	got = selectRelayAttempts(cands, all, now, "", 2*time.Minute, 3)
	if len(got) != 1 || got[0].Hostname != "a" {
		t.Fatalf("an all-cooling-down list must still allow one probe of the oldest failure, got %+v", got)
	}
	// All cooling down and all inside the half-cooldown window: nothing is probed, so
	// a broken relay cannot be hot-looped.
	fresh := map[string]time.Time{
		"a": now.Add(-30 * time.Second),
		"b": now.Add(-20 * time.Second),
		"c": now.Add(-10 * time.Second),
	}
	if got := selectRelayAttempts(cands, fresh, now, "", 2*time.Minute, 3); len(got) != 0 {
		t.Fatalf("nothing may be probed inside the half-cooldown window, got %+v", got)
	}
	// The bound holds.
	many := []relayCandidate{{Hostname: "a"}, {Hostname: "b"}, {Hostname: "c"}, {Hostname: "d"}}
	if got := selectRelayAttempts(many, nil, now, "", time.Minute, 2); len(got) != 2 {
		t.Fatalf("the per-request attempt count must be bounded, got %+v", got)
	}
}

func TestB356_1_DirectAttemptFitsHonoursTheCallerDeadline(t *testing.T) {
	now := nowFixture()
	if !directAttemptFits(nil, now, 6*time.Second) {
		t.Fatal("no context means the direct attempt always fits")
	}
	ctxNoDeadline := contextWithoutDeadline()
	if !directAttemptFits(ctxNoDeadline, now, 6*time.Second) {
		t.Fatal("a context without a deadline means the direct attempt fits")
	}
	short := shortDeadlineContext(now, 5*time.Second)
	if directAttemptFits(short, now, 6*time.Second) {
		t.Fatal("a 5s budget cannot fit a 6s direct attempt")
	}
	if !directAttemptFits(short, now, 4*time.Second) {
		t.Fatal("...but it does fit a 4s one")
	}
}
