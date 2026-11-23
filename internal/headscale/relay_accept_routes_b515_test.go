// relay_accept_routes_b515_test.go — B515.1 (2026-11-23).
//
// The parser is the half of the read-back that can be tested without a relay, and the half
// where a mistake is silent: reading an ABSENT field as "false" would paint every relay
// green the moment a `tailscale debug prefs` build renamed the field. The cases below are
// therefore about "what counts as evidence" more than about JSON.
package headscale

import "testing"

func TestRouteAllFromPrefs_B515(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
		why     string
	}{
		{
			name: "the live shape (false, forced by B514)",
			raw: `{
	"ControlURL": "https://head.example.com",
	"RouteAll": false,
	"ExitNodeID": "",
	"WantRunning": true,
	"AdvertiseRoutes": ["0.0.0.0/0", "::/0"]
}`,
			want: "false",
			why:  "this is the exact output B514 leaves on a correctly configured relay",
		},
		{
			name: "the loop (true)",
			raw:  `{"RouteAll": true, "AdvertiseRoutes": ["0.0.0.0/0"]}`,
			want: "true",
			why:  "a relay that accepts routes forwards arriving exit traffic back into the tailnet",
		},
		{
			name: "lower-case spelling (newer builds)",
			raw:  `{"routeAll": false}`,
			want: "false",
			why:  "the field name has moved in some releases; reading both is cheaper than a version check",
		},
		{
			name: "pretty printed behind a banner line",
			raw:  "prefs:\n{\n  \"RouteAll\": true\n}",
			want: "true",
			why:  "some builds pretty-print and a wrapper may print a banner first",
		},
		{
			name:    "field absent is NOT false",
			raw:     `{"ControlURL":"https://head.example.com","AdvertiseRoutes":["0.0.0.0/0"]}`,
			want:    "",
			wantErr: true,
			why:     "«I could not read it» must never be reported as «it is off» — that is the whole point of the block",
		},
		{
			name:    "no JSON at all",
			raw:     "tailscale: failed to connect to local tailscaled",
			want:    "",
			wantErr: true,
			why:     "a daemon that cannot be reached is unknown, not healthy",
		},
		{
			name:    "empty output",
			raw:     "   ",
			want:    "",
			wantErr: true,
			why:     "an empty read is a fact about the CHECK, never a verdict about the relay",
		},
		{
			name:    "truncated JSON",
			raw:     `{"RouteAll": tr`,
			want:    "",
			wantErr: true,
			why:     "an interrupted read must not fall back to a default",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, problem := RouteAllFromPrefs(c.raw)
			if got != c.want {
				t.Errorf("RouteAllFromPrefs() = %q, want %q (%s)", got, c.want, c.why)
			}
			if c.wantErr && problem == "" {
				t.Errorf("RouteAllFromPrefs() returned no problem, want one (%s)", c.why)
			}
			if !c.wantErr && problem != "" {
				t.Errorf("RouteAllFromPrefs() problem = %q, want none (%s)", problem, c.why)
			}
		})
	}
}

// TestRouteAllFromPrefs_FalseIsNotUnknown_B515 pins the pair the page's classifier relies
// on: an explicit false is EVIDENCE (ok), and the empty value is not the same thing.
func TestRouteAllFromPrefs_FalseIsNotUnknown_B515(t *testing.T) {
	got, problem := RouteAllFromPrefs(`{"RouteAll": false}`)
	if problem != "" {
		t.Fatalf("an explicit false produced a problem: %s", problem)
	}
	if got != "false" {
		t.Fatalf("an explicit false read as %q, want \"false\"", got)
	}
	empty, problem := RouteAllFromPrefs(`{}`)
	if empty != "" || problem == "" {
		t.Fatalf("an absent field must yield (\"\", reason), got (%q, %q)", empty, problem)
	}
}
