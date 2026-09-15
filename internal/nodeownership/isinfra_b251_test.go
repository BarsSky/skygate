// isinfra_b251_test.go — B251 tests for the strict-equality
// hostname match in isInfraNode. Pre-B251 used
// strings.HasPrefix("skygate-host-") which also matched
// suffixed forms. B251 narrows to strict equality on
// "skygate-host" so a single reserved name maps to the
// skygate VM.
//
// These tests are pure-function (no DB / no headscale) so
// they're cheap and pin the rule for future refactors.

package nodeownership

import (
	"testing"

	"skygate/internal/headscale"
)

func TestIsInfraNode_B251_ReservedNameOnly(t *testing.T) {
	cases := []struct {
		name string
		view headscale.NodeView
		want bool
	}{
		{
			name: "reserved name (B251 canonical)",
			view: headscale.NodeView{Hostname: "skygate-host", Tags: []string{"tag:private"}},
			want: true,
		},
		{
			name: "legacy skygate-host-1 (pre-B251)",
			view: headscale.NodeView{Hostname: "skygate-host-1", Tags: []string{"tag:private"}},
			want: false,
		},
		{
			name: "post-B227.1 renamed skygate-host-1-1",
			view: headscale.NodeView{Hostname: "skygate-host-1-1", Tags: []string{"tag:private"}},
			want: false,
		},
		{
			name: "user typo skygate-host-test",
			view: headscale.NodeView{Hostname: "skygate-host-test", Tags: []string{"tag:private"}},
			want: false,
		},
		{
			name: "unrelated hostname a71",
			view: headscale.NodeView{Hostname: "A71", Tags: []string{"tag:private"}},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isInfraNode(c.view)
			if got != c.want {
				t.Errorf("isInfraNode(%q) = %v, want %v", c.view.Hostname, got, c.want)
			}
		})
	}
}

func TestIsInfraNode_B251_ExitNodeTagStillMatches(t *testing.T) {
	// Pre-B251 rule 3 ("tag:exit-node") must stay — exit-nodes
	// (emilia/karolina/sharlotta) are infra-owned regardless of
	// hostname. Verifies we didn't accidentally narrow rule 3.
	cases := []struct {
		name string
		view headscale.NodeView
		want bool
	}{
		{
			name: "exit-node tag, hostname unrelated",
			view: headscale.NodeView{Hostname: "emilia", Tags: []string{"tag:dev-infra-emilia", "tag:exit-node", "tag:private"}},
			want: true,
		},
		{
			name: "tag:dev-infra-* tag, hostname unrelated",
			view: headscale.NodeView{Hostname: "random", Tags: []string{"tag:dev-infra-random", "tag:private"}},
			want: true,
		},
		{
			name: "no infra markers, no exit tag",
			view: headscale.NodeView{Hostname: "random", Tags: []string{"tag:private"}},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isInfraNode(c.view)
			if got != c.want {
				t.Errorf("isInfraNode(hostname=%q, tags=%v) = %v, want %v",
					c.view.Hostname, c.view.Tags, got, c.want)
			}
		})
	}
}