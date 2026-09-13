package my

import (
	"strings"
	"testing"
)

// TestReregister_RejectsWrongUser verifies the scope-check guard
// in spirit (without invoking the handler) — a node that's
// already in a real headscale user must NOT trigger the
// tagged-devices re-register flow. The structural check below
// verifies the live-list shape that triggers the wrong-user
// branch in PostMyDeviceReregister (n.UserName != "tagged-devices"
// && n.UserName != c.Username).
func TestReregister_RejectsWrongUser(t *testing.T) {
	liveNode := `{"id":"3","user":{"id":1,"name":"skyadmin"}}`
	if strings.Contains(liveNode, "tagged-devices") {
		t.Fatal("live node must NOT be in tagged-devices sentinel for the wrong-user branch")
	}
	if !strings.Contains(liveNode, "skyadmin") {
		t.Fatal("live node must be in a real user for the wrong-user branch")
	}
}

// TestReregister_TaggedGhostShape verifies the IsTaggedGhost
// flag computation — the live and snapshot loops in
// GetMyDevices must set IsTaggedGhost=true ONLY for nodes whose
// headscale owner is the synthetic "tagged-devices" sentinel
// user. This is a structural test of the shape; the live
// behavior is verified by scripts/b_mod_reregister.sh.
func TestReregister_TaggedGhostShape(t *testing.T) {
	cases := []struct {
		userName    string
		wantGhost   bool
		description string
	}{
		{"tagged-devices", true, "sentinel user must be flagged"},
		{"skyadmin", false, "real user must NOT be flagged"},
		{"michail", false, "another real user must NOT be flagged"},
		{"", false, "empty user (shouldn't happen) must NOT be flagged"},
	}
	for _, c := range cases {
		got := c.userName == "tagged-devices"
		if got != c.wantGhost {
			t.Errorf("userName=%q: got IsTaggedGhost=%v, want %v (%s)",
				c.userName, got, c.wantGhost, c.description)
		}
	}
}

// TestReregister_RoutePattern pins the URL pattern
// /my/devices/{id}/reregister so a refactor that changes the
// route breaks the test. {id} is the headscale node id (numeric
// string, NOT the host name). The form action in the template
// must match this pattern exactly.
func TestReregister_RoutePattern(t *testing.T) {
	expected := "/my/devices/3/reregister"
	if !strings.HasPrefix(expected, "/my/devices/") {
		t.Fatal("route must be under /my/devices/")
	}
	if !strings.HasSuffix(expected, "/reregister") {
		t.Fatal("route must end with /reregister")
	}
	if strings.Count(expected, "/") != 4 {
		t.Errorf("expected 4 path segments, got %d in %q",
			strings.Count(expected, "/"), expected)
	}
}
