// 2026-08-13: v1.3.9 round 6 (B108) — unit test mirrors
// scripts/check_b108.sh. Defense-in-depth: the Go test catches
// regressions in the `go test ./...` cycle; the shell test catches
// regressions in the `verify-pre` deploy cycle. Same 5 contracts
// pinned in both places.
//
// Operator-reported symptom: clicking a section summary in the
// COLLAPSED sidebar (52px, icons-only) does nothing visible — the
// <details> toggles but the page links inside are hidden by
// `.sidebar.collapsed .sidebar-section[open]>a{display:none}`
// (themes.css line ~195). Fix: JS auto-expands the sidebar on
// section summary click.

package handlers

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// readLayout returns the contents of internal/handlers/templates/layout.html
// (the shared sidebar/breadcrumb shell). Test fails with a clear message
// if the file is missing — protects against the test running from the
// wrong working directory.
func readLayout(t *testing.T) string {
	t.Helper()
	// Try several candidate paths so the test works whether invoked
	// from the repo root or from internal/handlers/.
	candidates := []string{
		"templates/layout.html",
		"internal/handlers/templates/layout.html",
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err == nil {
			return string(b)
		}
	}
	t.Fatalf("B108: could not read layout.html from any candidate path")
	return ""
}

// extractScriptBlock pulls the contents of the first <script>...</script>
// block that appears AFTER </footer> and BEFORE </body>. This matches
// the v1.3.9 round 6 contract: the JS must run on every page, so it
// must be at the document level (not inline onclick on each summary).
func extractScriptBlock(t *testing.T, layout string) string {
	t.Helper()
	// Greedy match between </footer> and </body> to find the script.
	re := regexp.MustCompile(`(?s)</footer>(.*?)</body>`)
	m := re.FindStringSubmatch(layout)
	if m == nil {
		t.Fatal("B108 FAIL: could not find </footer>...</body> region in layout.html")
		return ""
	}
	region := m[1]
	reScript := regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	m2 := reScript.FindStringSubmatch(region)
	if m2 == nil {
		t.Fatal("B108 FAIL: no <script> tag between </footer> and </body>")
		return ""
	}
	return m2[1]
}

// TestB108_ScriptBlockExists pins contract 1: a <script> block lives
// at the document level (between </footer> and </body>), not inline
// onclick on each of the 6 <summary> elements.
func TestB108_ScriptBlockExists(t *testing.T) {
	layout := readLayout(t)
	block := extractScriptBlock(t, layout)
	if strings.TrimSpace(block) == "" {
		t.Fatal("B108 FAIL: <script> block is empty")
	}
}

// TestB108_QueriesSidebar pins contract 2: the script looks up
// #sidebar — the hard-coded id from layout.html line ~38.
func TestB108_QueriesSidebar(t *testing.T) {
	layout := readLayout(t)
	block := extractScriptBlock(t, layout)
	if !strings.Contains(block, "getElementById('sidebar')") {
		t.Fatal("B108 FAIL: script does not call getElementById('sidebar')")
	}
}

// TestB108_IteratesSummaries pins contract 3: the script attaches
// click listeners to all 6 '.sidebar-section>summary' elements
// (Devices & Nodes, Access Control, System Health, Integrations,
// Data, Settings & Users).
func TestB108_IteratesSummaries(t *testing.T) {
	layout := readLayout(t)
	block := extractScriptBlock(t, layout)
	if !strings.Contains(block, "'.sidebar-section>summary'") {
		t.Fatal("B108 FAIL: script does not query '.sidebar-section>summary'")
	}
	if !strings.Contains(block, "addEventListener('click'") {
		t.Fatal("B108 FAIL: script does not attach click listener")
	}
}

// TestB108_RemovesCollapsedClass pins contract 4: on click, the
// script removes the 'collapsed' class from the sidebar. This is
// the actual fix — the sidebar expands to 220px, and the page links
// inside the just-opened <details> become visible (they were
// hidden by `.sidebar.collapsed .sidebar-section[open]>a{display:none}`).
func TestB108_RemovesCollapsedClass(t *testing.T) {
	layout := readLayout(t)
	block := extractScriptBlock(t, layout)
	if !strings.Contains(block, "sidebar.classList.remove('collapsed')") {
		t.Fatal("B108 FAIL: script does not remove 'collapsed' class on click")
	}
}

// TestB108_DoesNotPreventDefault pins contract 5: the script does
// NOT call preventDefault(). The native <details> toggle must still
// happen so the section opens after the sidebar expands. If we
// preventDefault, the section stays closed and the user is still
// stuck.
func TestB108_DoesNotPreventDefault(t *testing.T) {
	layout := readLayout(t)
	block := extractScriptBlock(t, layout)
	if strings.Contains(block, "preventDefault") {
		t.Fatal("B108 FAIL: script calls preventDefault() — would block native <details> toggle")
	}
}
