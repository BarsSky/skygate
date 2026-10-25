package exitnode

import (
	"strings"
	"testing"
)

// TestParseHealth_B417_1 pins the read-only probe's contract: the K=V shape survives a
// host that prints nothing, and every fact lands in its field.
func TestParseHealth_B417_1(t *testing.T) {
	out := "ip_forward=1\nmasq=2\nforward=3\ntailscale=up\nHEALTH_DONE=1\n"
	h := ParseHealth(out)
	if h.IPForward != "1" || h.Masq != 2 || h.Forward != 3 || !h.TailscaleUp {
		t.Fatalf("ParseHealth = %+v", h)
	}
	// An empty / unparsable answer must not look healthy.
	empty := ParseHealth("")
	if empty.IPForward != "?" || empty.Masq != 0 || empty.Forward != 0 || empty.TailscaleUp {
		t.Fatalf("an empty report must be unusable, got %+v", empty)
	}
	// Unknown lines are ignored (a future probe may add facts).
	if h := ParseHealth("future=1\nip_forward=1\n"); h.IPForward != "1" {
		t.Fatalf("unknown keys must not break the parse: %+v", h)
	}
}

// TestHealthVerdict_B417_1 is the operator-facing sentence: the FIRST missing piece, in
// the order that blocks traffic.
func TestHealthVerdict_B417_1(t *testing.T) {
	good := Health{IPForward: "1", Masq: 1, Forward: 1, TailscaleUp: true}
	if v := HealthVerdict(good); v != "" {
		t.Fatalf("a forwarding relay must have no verdict, got %q", v)
	}
	cases := []struct {
		name string
		h    Health
		want string
	}{
		{"ssh", Health{Err: "ssh refused"}, "SSH"},
		{"ip_forward", Health{IPForward: "0", Masq: 1, Forward: 1, TailscaleUp: true}, "ip_forward"},
		{"nat", Health{IPForward: "1", Masq: 0, Forward: 1, TailscaleUp: true}, "MASQUERADE"},
		{"forward", Health{IPForward: "1", Masq: 1, Forward: 0, TailscaleUp: true}, "FORWARD"},
		{"tailscale", Health{IPForward: "1", Masq: 1, Forward: 1}, "tailscale"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HealthVerdict(c.h); !strings.Contains(got, c.want) {
				t.Fatalf("verdict = %q, want it to name %q", got, c.want)
			}
		})
	}
	// An unknown ip_forward (the command is missing) is NOT "1" — it must be reported.
	if v := HealthVerdict(Health{IPForward: "?", Masq: 1, Forward: 1, TailscaleUp: true}); v == "" {
		t.Fatal("an unknown ip_forward must not be treated as forwarding")
	}
}

// TestBuildHealthScript_B417_1 pins the safety of the probe: read-only commands only.
func TestBuildHealthScript_B417_1(t *testing.T) {
	s := BuildHealthScript()
	for _, want := range []string{"sysctl -n net.ipv4.ip_forward", "iptables -t nat -S POSTROUTING", "iptables -S FORWARD", "tailscale status", "HEALTH_DONE=1"} {
		if !strings.Contains(s, want) {
			t.Errorf("health script is missing %q", want)
		}
	}
	// ">" appears in `>/dev/null` (redirection of the PROBE, not a write to the host's
	// files), so the mutation check looks for a redirect to a real path instead.
	for _, forbidden := range []string{"iptables -A", "iptables -I", "iptables -D", "iptables -F", "sysctl -w", "tailscale up", "tailscale set"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("health script must never mutate the host, found %q", forbidden)
		}
	}
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, ">"); i >= 0 {
			rest := strings.TrimSpace(line[i+1:])
			if !strings.HasPrefix(rest, "/dev/null") && !strings.HasPrefix(rest, "&") {
				t.Errorf("health script redirects somewhere other than /dev/null: %q", line)
			}
		}
	}
}

func TestHealthSortOrders_B417_1(t *testing.T) {
	in := []HealthLine{
		{Host: "b", OK: true},
		{Host: "c", OK: false, Problem: "no nat"},
		{Host: "a", OK: true},
	}
	out := HealthSortOrders(in)
	if out[0].Host != "c" || out[1].Host != "a" || out[2].Host != "b" {
		t.Fatalf("broken relays must come first, then a stable host order: %+v", out)
	}
	if in[0].Host != "b" {
		t.Fatal("sort must not mutate the caller's slice")
	}
}
