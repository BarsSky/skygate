// acl_ssh_src_host_test.go — 2026-09-18 regression tests for the
// "skygate cannot Tailscale-SSH into its own exit nodes" fix.
//
// THE BUG
// -------
// The generated policy's `ssh` rule had:
//
//	"src": ["tag:private", "<admin>@<baseDomain>"]
//
// skygate applies `tailscale set --advertise-routes …` on the exit
// nodes by shelling out to `ssh` FROM THE HOST IT RUNS ON. That traffic
// reaches the relay with the skygate host's own tailnet identity, whose
// tag is `tag:dev-infra-skygate-host` — not `tag:private`. Tailscale SSH
// therefore refused every attempt:
//
//	tailscale: tailnet policy does not permit you to SSH to this node
//
// Live consequence (verified on the VM 2026-09-18): emilia worked only
// because its PUBLIC sshd is exposed (<EMILIA_PUBLIC_IP>:22), karolina only
// through a non-intercepting port over the tailnet (100.64.0.2:18022),
// and sharlotta (whose only reachable port 22 is intercepted by
// Tailscale SSH) could not be reached at all.
//
// THE FIX
// -------
// getSkygateHostInfraTags + sshRuleSrc add the host's own
// `tag:dev-infra-skygate*` tag(s) to the rule's src, sourced from the
// node-ownership data (tagsByUser) instead of being hardcoded, so a
// host rename / re-tag is picked up on the next policy regeneration.
//
// These tests pin the helper's contract AND the composition.
package acl

import (
	"reflect"
	"testing"
)

func TestGetSkygateHostInfraTags(t *testing.T) {
	cases := []struct {
		name string
		in   map[string][]string
		want []string
	}{
		{
			name: "nil map",
			in:   nil,
			want: nil,
		},
		{
			name: "no infra bucket",
			in:   map[string][]string{"skyadmin": {"tag:dev-skyadmin-laptop"}},
			want: nil,
		},
		{
			name: "exit nodes only — host tag must NOT be returned",
			in: map[string][]string{"infra": {
				"tag:dev-infra-emilia",
				"tag:dev-infra-karolina",
				"tag:dev-infra-sharlotta",
			}},
			want: nil,
		},
		{
			name: "host tag alone",
			in:   map[string][]string{"infra": {"tag:dev-infra-skygate-host"}},
			want: []string{"tag:dev-infra-skygate-host"},
		},
		{
			name: "mixed — only the skygate host tag is picked",
			in: map[string][]string{"infra": {
				"tag:dev-infra-skygate-host",
				"tag:dev-infra-emilia",
				"tag:dev-infra-skygate-host-1",
			}},
			want: []string{"tag:dev-infra-skygate-host", "tag:dev-infra-skygate-host-1"},
		},
		{
			name: "dedup + sorted",
			in: map[string][]string{"infra": {
				"tag:dev-infra-skygate-b",
				"tag:dev-infra-skygate-a",
				"tag:dev-infra-skygate-b",
			}},
			want: []string{"tag:dev-infra-skygate-a", "tag:dev-infra-skygate-b"},
		},
		{
			name: "empty strings skipped",
			in:   map[string][]string{"infra": {"", "tag:dev-infra-skygate-host", ""}},
			want: []string{"tag:dev-infra-skygate-host"},
		},
		{
			name: "other buckets ignored",
			in: map[string][]string{
				"infra":    {"tag:dev-infra-emilia"},
				"skyadmin": {"tag:dev-infra-skygate-host"},
			},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := getSkygateHostInfraTags(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("getSkygateHostInfraTags() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSSHRuleSrc pins the composed src list: legacy pair first, then the
// host tags — and the regression itself (the host tag MUST be present
// when the host has an infra tag).
func TestSSHRuleSrc(t *testing.T) {
	t.Run("no host tag — legacy pair unchanged", func(t *testing.T) {
		got := sshRuleSrc(map[string][]string{"infra": {"tag:dev-infra-emilia"}}, "skyadmin", "tsnet.example.com")
		want := []string{"tag:private", "skyadmin@tsnet.example.com"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("sshRuleSrc() = %v, want %v", got, want)
		}
	})

	t.Run("regression: host tag is present (2026-09-18 bug)", func(t *testing.T) {
		got := sshRuleSrc(map[string][]string{"infra": {
			"tag:dev-infra-skygate-host",
			"tag:dev-infra-emilia",
		}}, "skyadmin", "tsnet.example.com")

		want := []string{
			"tag:private",
			"skyadmin@tsnet.example.com",
			"tag:dev-infra-skygate-host",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sshRuleSrc() = %v, want %v", got, want)
		}
		// Explicit: without the host tag, Tailscale SSH refuses skygate.
		found := false
		for _, s := range got {
			if s == "tag:dev-infra-skygate-host" {
				found = true
			}
		}
		if !found {
			t.Error("ssh src is missing the skygate host tag — skygate cannot SSH into its exit nodes over the tailnet")
		}
	})
}
