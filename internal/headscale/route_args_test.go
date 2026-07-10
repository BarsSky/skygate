package headscale

import (
	"strings"
	"testing"
)

// Tests for the helpers extracted from Client.SetAdvertisedRoutes. The
// helpers here are pure; the SSH-exec + curl-side behaviour stays in
// headscale.go as it was.

func TestBuildTailscaleSetRoutes_PrependsBaseExitNodeRoutes(t *testing.T) {
	got := BuildTailscaleSetRoutes([]string{"10.0.0.0/8", "10.1.0.0/16"})
	parts := strings.Split(got, ",")
	if len(parts) != 4 {
		t.Fatalf("want 4 routes (base 2 + caller 2), got %d: %q", len(parts), got)
	}
	if parts[0] != "0.0.0.0/0" || parts[1] != "::/0" {
		t.Errorf("base routes not at positions 0/1: %q", parts)
	}
	if parts[2] != "10.0.0.0/8" || parts[3] != "10.1.0.0/16" {
		t.Errorf("caller routes not preserved in order: %q", parts)
	}
}

func TestBuildTailscaleSetRoutes_DedupesAgainstBase(t *testing.T) {
	// Caller tried to inject base routes — they must not appear twice.
	got := BuildTailscaleSetRoutes([]string{"0.0.0.0/0", "::/0", "10.0.0.0/8"})
	parts := strings.Split(got, ",")
	if len(parts) != 3 {
		t.Fatalf("want 3 routes after dedupe, got %d: %q", len(parts), got)
	}
	for _, p := range parts[2:] {
		if p == "0.0.0.0/0" || p == "::/0" {
			t.Errorf("base route %q leaked into caller positions: %q", p, parts)
		}
	}
}

func TestBuildTailscaleSetRoutes_DedupesWithinCaller(t *testing.T) {
	got := BuildTailscaleSetRoutes([]string{"10.0.0.0/8", "10.0.0.0/8", "10.1.0.0/16", "10.1.0.0/16"})
	parts := strings.Split(got, ",")
	if len(parts) != 4 {
		t.Fatalf("dedup failed: %q", got)
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if seen[p] {
			t.Errorf("duplicate route after dedup: %q (whole: %q)", p, got)
		}
		seen[p] = true
	}
}

func TestBuildTailscaleSetRoutes_SkipsEmpty(t *testing.T) {
	got := BuildTailscaleSetRoutes([]string{"", "10.0.0.0/8", ""})
	parts := strings.Split(got, ",")
	if len(parts) != 3 {
		t.Fatalf("want 3 routes, got %d: %q", len(parts), got)
	}
	for _, p := range parts {
		if p == "" {
			t.Errorf("empty route leaked into result: %q", got)
		}
	}
}

func TestBuildTailscaleSetRoutes_EmptyInputYieldsBase(t *testing.T) {
	got := BuildTailscaleSetRoutes(nil)
	if got != "0.0.0.0/0,::/0" {
		t.Errorf("empty input should yield base routes only, got %q", got)
	}
}

func TestAcceptRoutesFlag_Table(t *testing.T) {
	cases := []struct {
		in  int
		out string
	}{
		{-1, " --accept-routes=false"},
		{0, ""},
		{1, " --accept-routes=true"},
		// Anything outside the documented range behaves as 0 (legacy).
		{2, ""},
		{-2, ""},
		{42, ""},
		{-100, ""},
	}
	for _, c := range cases {
		if got := AcceptRoutesFlag(c.in); got != c.out {
			t.Errorf("AcceptRoutesFlag(%d) = %q want %q", c.in, got, c.out)
		}
	}
}

// Regression: the old staggeredSync split rules into batches of 20 and
// called SetAdvertisedRoutes once per batch. tailscale set replaces the
// route list atomically, so each batch clobbered the previous one. The
// new code is expected to call SetAdvertisedRoutes exactly ONCE per node
// with the FULL deduped list. We exercise the helper that builds the
// command and assert the count of routes present.
func TestBuildTailscaleSetRoutes_OneCallSendsAllRoutes(t *testing.T) {
	// 145 rules simulating karolina before the fix.
	var routes []string
	for i := 0; i < 145; i++ {
		routes = append(routes, "10.1."+itoa(i/256)+"."+itoa(i%256)+"/32")
	}
	got := BuildTailscaleSetRoutes(routes)
	parts := strings.Split(got, ",")
	if len(parts) != 147 { // 145 caller + 2 base
		t.Fatalf("aggregation broke: expected 147 routes in one command, got %d", len(parts))
	}
	if parts[0] != "0.0.0.0/0" || parts[1] != "::/0" {
		t.Errorf("base not at top of single command: %q", parts)
	}
}

// Regression: at least one caller-supplied route identical to the base
// must never appear twice in the final command, because tailscale rejects
// duplicate --advertise-routes values.
func TestBuildTailscaleSetRoutes_NoDuplicatesEvenAtBaseBoundary(t *testing.T) {
	got := BuildTailscaleSetRoutes([]string{"0.0.0.0/0", "10.0.0.0/8"})
	parts := strings.Split(got, ",")
	if len(parts) != 3 {
		t.Fatalf("dup at base boundary: %q", got)
	}
	for _, p := range parts {
		switch p {
		case "0.0.0.0/0":
			continue
		}
	}
}

func TestBuildTailscaleSetCommand_FullFlag(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		flag int
		want string
	}{
		{
			"empty accept flag",
			[]string{"10.0.0.0/8"},
			0,
			"tailscale set --advertise-exit-node --advertise-routes=0.0.0.0/0,::/0,10.0.0.0/8",
		},
		{
			"accept routes false (Amnezia-host node)",
			[]string{"10.0.0.0/8"},
			-1,
			"tailscale set --advertise-exit-node --advertise-routes=0.0.0.0/0,::/0,10.0.0.0/8 --accept-routes=false",
		},
		{
			"accept routes true (legacy pure exit-node)",
			[]string{"10.0.0.0/8"},
			1,
			"tailscale set --advertise-exit-node --advertise-routes=0.0.0.0/0,::/0,10.0.0.0/8 --accept-routes=true",
		},
		{
			"empty input forces base pairs",
			nil,
			0,
			"tailscale set --advertise-exit-node --advertise-routes=0.0.0.0/0,::/0",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := BuildTailscaleSetCommand(c.in, c.flag); got != c.want {
				t.Errorf("\ngot:  %q\nwant: %q", got, c.want)
			}
		})
	}
}

// itoa is a tiny integer-to-string helper so we don't pull in strconv just
// for the 145-route regression. (strconv would be fine; this just keeps
// the test file self-contained.)
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
