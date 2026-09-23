// internal/feature/admin/exit_nodes_b295_test.go — B295 (2026-09-23).
//
// The page half of B295: when headscale does not answer, the prefix card must not
// answer «состояние политики неизвестно» if the last APPLIED snapshot proves the
// generated policy is already in force — and when it does report an "in sync"
// verdict from that snapshot, it must say so, because the operator has to know the
// verdict did not come from headscale.
package admin

import (
	"os"
	"strings"
	"testing"
)

func TestPrefixDriftStatsCarriesTheVerdictSource_B295(t *testing.T) {
	src, err := os.ReadFile("exit_nodes.go")
	if err != nil {
		t.Fatalf("read exit_nodes.go: %v", err)
	}
	code := string(src)
	for _, want := range []string{
		"db.LastAppliedACLVersion(s.dbc())",
		"db.GetACLConfig(s.dbc(), version)",
		"headscale.PolicyEquivalent(gen, snapshot)",
		"stats.PolicyVia = headscale.SnapshotInSyncHint(version)",
		"PolicyVia string",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("exit_nodes.go no longer contains %q — a restart-window read failure is «unknown» again", want)
		}
	}
	// The blind path must survive: a read failure WITHOUT a usable snapshot is
	// still reported as an error (never silently rendered as in-sync).
	if !strings.Contains(code, `stats.PolicyErr = "read live policy: " + err.Error()`) {
		t.Error("the page no longer reports a read failure when no snapshot can prove the verdict")
	}
}

func TestExitNodesTemplateShowsTheVerdictSource_B295(t *testing.T) {
	tmpl, err := os.ReadFile("../../handlers/templates/admin/exit_nodes.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	if !strings.Contains(string(tmpl), "{{if .PolicyVia}}") {
		t.Error("exit_nodes.html does not render PolicyVia — the operator cannot tell a snapshot verdict from a live one")
	}
}
