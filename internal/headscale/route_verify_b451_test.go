// route_verify_b451_test.go — B451 (2026-10-30). The live numbers ARE the test cases:
// `prefix_owner` gave emilia 135 prefixes; headscale showed her advertising 2 while the apply
// reported `relay_apply_state = ok`. The comparison below is what turns that into an error.
package headscale

import (
	"strings"
	"testing"
)

func TestAdvertisedRoutesFromPrefs_B451(t *testing.T) {
	cases := []struct {
		name, raw string
		want      []string
		wantErr   bool
		why       string
	}{
		{
			name: "real prefs shape",
			raw:  `{"ControlURL":"https://hs.example.com","AdvertiseRoutes":["0.0.0.0/0","::/0","10.0.0.0/8"],"ExitNodeID":""}`,
			want: []string{"0.0.0.0/0", "10.0.0.0/8", "::/0"},
			why:  "the node's own list, normalised (sorted) for comparison",
		},
		{
			name: "pretty printed with a banner line",
			raw:  "prefs:\n{\n  \"AdvertiseRoutes\": [\n    \"0.0.0.0/0\",\n    \"::/0\"\n  ]\n}",
			want: []string{"0.0.0.0/0", "::/0"},
			why:  "builds differ in formatting and a wrapper may print a banner first",
		},
		{
			name: "field absent means nothing beyond the defaults",
			raw:  `{"ControlURL":"https://hs.example.com"}`,
			want: []string{},
			why:  "a missing field is not a parse failure — it is «advertises nothing extra»",
		},
		{
			name: "lower-case spelling (newer builds)",
			raw:  `{"advertiseRoutes":["10.1.0.0/16"]}`,
			want: []string{"10.1.0.0/16"},
			why:  "the field name has moved in some releases; reading both is cheaper than a version check",
		},
		{
			name:    "not JSON at all",
			raw:     "tailscale: unknown command \"debug\"",
			wantErr: true,
			why:     "an old CLI must be reported as «could not read», not as «advertises nothing»",
		},
		{
			name:    "empty output",
			raw:     "",
			wantErr: true,
			why:     "silence is not an answer",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := AdvertisedRoutesFromPrefs(c.raw)
			if c.wantErr {
				if reason == "" {
					t.Fatalf("expected a refusal reason (%s), got none", c.why)
				}
				return
			}
			if reason != "" {
				t.Fatalf("unexpected refusal: %s (%s)", reason, c.why)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("got %v, want %v (%s)", got, c.want, c.why)
			}
		})
	}
}

func TestVerifyAdvertised_B451(t *testing.T) {
	base := []string{"0.0.0.0/0", "::/0"}
	live := []string{"10.49.148.156/32", "10.51.177.107/32"}

	// The live shape: the node advertises the base routes only, the apply asked for 137.
	err := VerifyAdvertised("emilia", ExpectedAdvertisedRoutes(live), base)
	if err == nil {
		t.Fatal("a node advertising 2 routes instead of the requested set was accepted as a success — this IS the reported bug")
	}
	msg := err.Error()
	for _, want := range []string{"emilia", "10.49.148.156/32", "missing=", "requested"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the mismatch must name %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "extra=") {
		t.Errorf("no extra route here, so the message must not claim one: %s", msg)
	}

	// Order-insensitivity: the node may report the same set in any order.
	if err := VerifyAdvertised("emilia", []string{"::/0", "0.0.0.0/0"}, []string{"0.0.0.0/0", "::/0"}); err != nil {
		t.Fatalf("the same set in another order must verify: %v", err)
	}

	// Extra routes are named too: `tailscale set` REPLACES the set, so leftovers are the
	// B352/B374 shape (a relay still advertising prefixes the table moved away).
	withLive := append(append([]string{}, base...), live...)
	got, _ := RouteDiff(ExpectedAdvertisedRoutes(live), withLive)
	if len(got) != 0 {
		t.Fatalf("RouteDiff missing = %v, want none", got)
	}
	_, extra := RouteDiff(ExpectedAdvertisedRoutes(live), append(append([]string{}, withLive...), "203.0.113.0/24"))
	if len(extra) != 1 || extra[0] != "203.0.113.0/24" {
		t.Fatalf("extra = %v, want the leftover prefix", extra)
	}
}

// TestExpectedAdvertisedRoutesKeepsTheBasePrefixes_B451 guards the property the whole file rests
// on: the expected set is EXACTLY what BuildTailscaleSetRoutes sends, base routes included.
func TestExpectedAdvertisedRoutesKeepsTheBasePrefixes_B451(t *testing.T) {
	want := ExpectedAdvertisedRoutes([]string{"10.0.0.0/8"})
	joined := strings.Join(want, ",")
	for _, base := range ExitNodeBaseRoutes {
		if !strings.Contains(joined, base) {
			t.Errorf("expected set lost the exit-node base route %s: %v", base, want)
		}
	}
	// And it agrees with the command that is actually sent.
	cmd := BuildTailscaleSetRoutes([]string{"10.0.0.0/8"})
	for _, r := range want {
		if !strings.Contains(cmd, r) {
			t.Errorf("expected set contains %s, but the applied command does not: %s", r, cmd)
		}
	}
}
