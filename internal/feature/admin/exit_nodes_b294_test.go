// internal/feature/admin/exit_nodes_b294_test.go — B294 (2026-09-23).
//
// The operator's prefix-assignment page on `aro` showed
//
//	владелец не объявляет: 0   никто не объявляет: 19   объявляет несколько: 0
//
// with every row «АНОНС: нет · нет маршрута» — because the live headscale read
// (`ListAllNodes`) had failed with `dial tcp 127.0.0.1:8081: connect: connection
// refused` and the loader discarded the error, leaving the advertisement map empty.
// Absence of EVIDENCE was rendered as a negative FACT: the page told the operator
// that nothing is advertised, and no action on that page could ever change it,
// because the real problem was the API address.
package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrefixStatsCarriesTheLiveReadFailure_B294: the failure must reach the page.
func TestPrefixStatsCarriesTheLiveReadFailure_B294(t *testing.T) {
	src, err := os.ReadFile("exit_nodes.go")
	if err != nil {
		t.Fatalf("read exit_nodes.go: %v", err)
	}
	code := string(src)
	for _, want := range []string{
		"stats.LiveReadErr = herr.Error()",
		"stats.LiveReadHint = headscale.ACLReadHintFor(s.HSGlobalFn(), herr)",
		"cannot read the advertised routes from headscale",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("exit_nodes.go no longer contains %q — a failed headscale read is silent again", want)
		}
	}
	if !strings.Contains(code, "LiveReadErr string") || !strings.Contains(code, "LiveReadHint string") {
		t.Error("PrefixDriftStats lost the live-read fields the template renders")
	}
}

// TestExitNodesTemplateShowsTheLiveReadFailure_B294: the table must say WHY the
// АНОНС column is empty, not just render «нет» 19 times.
func TestExitNodesTemplateShowsTheLiveReadFailure_B294(t *testing.T) {
	tmpl, err := os.ReadFile(filepath.Join("..", "..", "handlers", "templates", "admin", "exit_nodes.html"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	src := string(tmpl)
	for _, want := range []string{
		"{{if .LiveReadErr}}",
		"exit_nodes.prefix_owner.live_read_failed",
		"{{if .LiveReadHint}}",
		"exit_nodes.prefix_owner.live_read_failed_help",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("exit_nodes.html no longer contains %q — an unreadable headscale is invisible again", want)
		}
	}

	cat, err := os.ReadFile(filepath.Join("..", "..", "i18n", "catalog_exit_nodes.go"))
	if err != nil {
		t.Fatalf("read catalogue: %v", err)
	}
	if n := strings.Count(string(cat), `"exit_nodes.prefix_owner.live_read_failed"`); n != 2 {
		t.Errorf("live_read_failed is defined %d time(s), want 2 (RU + EN)", n)
	}
	if n := strings.Count(string(cat), `"exit_nodes.prefix_owner.live_read_failed_help"`); n != 2 {
		t.Errorf("live_read_failed_help is defined %d time(s), want 2 (RU + EN)", n)
	}
}
