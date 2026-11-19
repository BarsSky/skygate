package headscale

// relay_exec_b501_test.go — B501 (2026-11-19).
//
// The reconnect action runs ONE fixed command on a relay and then reports the state that
// came back. Two properties are worth a test and a contract around them:
//
//  1. the command is a CONSTANT — no operator input ever reaches a shell, and the only way
//     to change what runs is to change the source (`TestB501_ReconnectCommandIsFixed`);
//  2. "the command exited 0" is NOT "the relay is back": the result is parsed from the
//     relay's own `tailscale status --json`, because a daemon that restarted into a broken
//     state exits 0 and says so only in that JSON (B451's rule, applied to a restart).
//
// `ParseRelaySelfState` is pure, so the tolerance it needs — a resumed `tailscale up` may
// print a login URL before the JSON — is testable without a relay.

import (
	"strings"
	"testing"
)

// TestB501_ReconnectCommandIsFixed pins the shape AND the absence of interpolation.
func TestB501_ReconnectCommandIsFixed(t *testing.T) {
	cmd := RelayReconnectCommand
	for _, want := range []string{"systemctl restart tailscaled", "rc-service tailscaled restart", "tailscale down", "tailscale up", "tailscale status --json"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("the reconnect command lost %q: %s", want, cmd)
		}
	}
	// No shell interpolation of anything an operator typed: no $ , no backticks.
	for _, bad := range []string{"$", "`"} {
		if strings.Contains(cmd, bad) {
			t.Fatalf("the reconnect command interpolates %q — operator input must never reach the shell: %s", bad, cmd)
		}
	}
	// The status read must come AFTER the restart, or the region is the stale one.
	restartAt := strings.Index(cmd, "systemctl restart tailscaled")
	statusAt := strings.Index(cmd, "tailscale status --json")
	if restartAt < 0 || statusAt < 0 || statusAt < restartAt {
		t.Fatalf("the status read does not follow the restart: %s", cmd)
	}
}

// TestB501_ParseRelaySelfState is the "is the relay really back?" half.
func TestB501_ParseRelaySelfState(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		want     RelaySelfState
		wantErr  bool
		errMatch string
	}{
		{
			name: "a healthy status",
			raw:  `{"Self":{"HostName":"karolina","Relay":"mow","Online":true,"BackendState":"Running"}}`,
			want: RelaySelfState{HostName: "karolina", Relay: "mow", Online: true, BackendState: "Running"},
		},
		{
			// `tailscale up` on an expired key prints a login URL first — the JSON is
			// still the answer, so the parser must find it instead of failing on line 1.
			name: "a login URL before the JSON",
			raw:  "To authenticate, visit:\n\n\thttps://head.example.com/register/abc123\n\n" + `{"Self":{"HostName":"karolina","Relay":"","Online":false,"BackendState":"NeedsLogin"}}`,
			want: RelaySelfState{HostName: "karolina", Online: false, BackendState: "NeedsLogin"},
		},
		{
			name:    "no JSON at all",
			raw:     "systemctl: command not found\n",
			wantErr: true,
		},
		{
			name:    "JSON without a Self block",
			raw:     `{"Peer":{}}`,
			wantErr: true,
		},
		{
			name:    "truncated JSON",
			raw:     `{"Self":{"HostName":`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRelaySelfState(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
