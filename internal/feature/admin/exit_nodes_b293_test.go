// internal/feature/admin/exit_nodes_b293_test.go — B293 (2026-09-23).
//
// «тот exit-node что существует он расположен на той же машине что и headscale и
// skygate … в таком случае доступ как таковой не нужен по ssh».
//
// The page must therefore stop demanding an SSH key for a relay that IS this host,
// show it as a local node, and report a LOCAL apply failure as a failure.
package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExitSyncFailed_LocalTransport_B293: the sync result grammar gained a local
// branch (syncOneExitNode), so the ok/err decision must know it — otherwise a
// failed local apply would render in the green flash box, which is the exact bug
// B292 fixed for the SSH branch.
func TestExitSyncFailed_LocalTransport_B293(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"local=ok via direct approved=21", false},
		{"local=ok via sudo approved=0", false},
		{"local=ok via helper approved=21 out=Success.", false},
		{"local=err=local route apply refused: permission denied — apply the routes locally with one of: (a) run skygate as root approved=21", true},
		{"local=err=privileged routes helper FAILED: tailscale set failed (rc=1) approved=0", true},
		// The B292 branches must keep working (regression guard).
		{"ssh=err=ssh exit-node-vps (key /x): nope approved=21", true},
		{"ssh=ok approved=21", false},
	}
	for _, c := range cases {
		if got := exitSyncFailed(c.msg); got != c.want {
			t.Errorf("exitSyncFailed(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// TestSplitCommaList_B293: the relay's address list carries IPv4 + IPv6 on the
// operator's host ("100.64.0.1,fd7a:115c:a1e0::1"), and the co-location check must
// see both.
func TestSplitCommaList_B293(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"100.64.0.1", 1},
		{"100.64.0.1,fd7a:115c:a1e0::1", 2},
		{" 100.64.0.2 , fd7a::2 ", 2},
		{"", 0},
		{" , ", 0},
	}
	for _, c := range cases {
		if got := splitCommaList(c.in); len(got) != c.want {
			t.Errorf("splitCommaList(%q) = %v (%d entries), want %d", c.in, got, len(got), c.want)
		}
	}
}

// TestExitNodesPageRendersLocalBadge_B293 is the template contract: a local relay
// must be visible as such, and the tooltip must name the transport that will be
// used (the operator has to be able to see WHY no key is needed).
func TestExitNodesPageRendersLocalBadge_B293(t *testing.T) {
	tmpl, err := os.ReadFile(filepath.Join("..", "..", "handlers", "templates", "admin", "exit_nodes.html"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	src := string(tmpl)
	for _, want := range []string{
		"{{if .LocalRelay}}",
		"exit_nodes.local_relay_badge",
		"exit_nodes.local_relay_tip",
		".LocalTransport",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("exit_nodes.html no longer contains %q — a co-located relay is invisible again", want)
		}
	}
}

// TestTelegramEgressDistinguishesLocalSelf_B293: on the operator's host the
// co-located relay IS this machine's own tailnet node, and that changes the
// advice: selecting it as egress cannot route anything (same way out as direct),
// while the api.telegram.org probe becomes REPRESENTATIVE. The template and the
// catalogue must carry that separate message instead of reusing B265's warning.
func TestTelegramEgressDistinguishesLocalSelf_B293(t *testing.T) {
	tmpl, err := os.ReadFile(filepath.Join("..", "..", "handlers", "templates", "admin", "telegram.html"))
	if err != nil {
		t.Fatalf("read telegram template: %v", err)
	}
	if !strings.Contains(string(tmpl), "{{if .State.Egress.LocalSelfRelay}}") ||
		!strings.Contains(string(tmpl), "telegram.egress_local_self") {
		t.Error("telegram.html must render the LocalSelfRelay message (a co-located relay that is THIS host is not the same case as B265's warning)")
	}

	cat, err := os.ReadFile(filepath.Join("..", "..", "i18n", "catalog_telegram.go"))
	if err != nil {
		t.Fatalf("read telegram catalogue: %v", err)
	}
	if n := strings.Count(string(cat), `"telegram.egress_local_self"`); n != 2 {
		t.Errorf("telegram.egress_local_self is defined %d time(s), want 2 (RU + EN)", n)
	}
}
