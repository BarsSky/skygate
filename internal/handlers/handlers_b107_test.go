package handlers

// handlers_b107_test.go — regression tests for the v1.3.9
// admin breadcrumb + collapsed section icons fixes (B107).
//
// Two contracts pinned:
//
//   1. .admin-breadcrumb gets padding-left:60px on mobile
//      (inside @media (max-width:768px)) so the breadcrumb
//      clears the hamburger at top:12px,left:12px,40×40px.
//      The pre-fix CSS only added padding-left:60px to
//      .title-row, but .admin-breadcrumb is a SIBLING nav
//      (not a child of .title-row) and didn't inherit the
//      offset. Operator-reported: "Админ" was half-hidden
//      behind the hamburger on /admin/devices.
//
//   2. .sidebar.collapsed .sidebar-section>summary gets
//      padding:0;gap:0;justify-content:center + the caret
//      (::before) gets display:none, so the summary becomes
//      a single 16px icon that fits in the 52px collapsed
//      sidebar. Pre-fix: the summary's 8px 10px padding
//      + 10px gap + 10px caret + 16px icon = 56px content
//      overflowed the 52px sidebar by 4px, triggering a
//      horizontal scroll bar. Operator-reported: "когда
//      меню скрыто иконки групп не переходят в скрытый
//      режим и появляется снизу горизонтальный скролл".
//
// The collapsed summary fix is in the MAIN CSS (not @media)
// because collapsed state can happen on ANY viewport via
// the in-sidebar .toggle button click. The breadcrumb fix
// is in @media (max-width:768px) because the hamburger is
// mobile-only.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestB107_AdminBreadcrumbMobileOffset — .admin-breadcrumb
// has padding-left:60px INSIDE @media (max-width:768px).
func TestB107_AdminBreadcrumbMobileOffset(t *testing.T) {
	if repoRoot == "" {
		t.Fatal("B107 FAIL: could not find repo root (go.mod not found)")
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, "static/css/themes.css"))
	if err != nil {
		t.Fatalf("B107 FAIL: read themes.css: %v", err)
	}
	css := string(data)

	// Extract the @media (max-width:768px) block.
	mediaStart := strings.Index(css, "@media (max-width:768px)")
	if mediaStart < 0 {
		t.Fatal("B107 FAIL: themes.css missing @media (max-width:768px) block")
	}
	openBrace := strings.Index(css[mediaStart:], "{")
	if openBrace < 0 {
		t.Fatal("B107 FAIL: @media block missing opening brace")
	}
	depth := 1
	i := mediaStart + openBrace + 1
	for i < len(css) && depth > 0 {
		switch css[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		i++
	}
	if depth != 0 {
		t.Fatal("B107 FAIL: @media block has unmatched braces")
	}
	block := css[mediaStart+openBrace : i]

	// The .admin-breadcrumb padding-left:60px rule.
	re := regexp.MustCompile(`\.admin-breadcrumb[[:space:]]*\{([^}]*)\}`)
	m := re.FindStringSubmatch(block)
	if m == nil {
		t.Error("B107 FAIL: .admin-breadcrumb rule missing inside @media (max-width:768px)")
		return
	}
	if !regexp.MustCompile(`padding-left[[:space:]]*:[[:space:]]*60px`).MatchString(m[1]) {
		t.Errorf("B107 FAIL: .admin-breadcrumb inside @media (max-width:768px) is missing padding-left:60px (breadcrumb hidden behind the hamburger); got: %s", strings.TrimSpace(m[1]))
	}
}

// TestB107_CollapsedSectionSummaryFits52px — when the sidebar
// is collapsed (52px wide), the section summary becomes a
// single 16px centered icon (no caret, no padding, no gap).
// Otherwise the summary at 56px overflows the 52px sidebar
// and triggers a horizontal scroll bar.
func TestB107_CollapsedSectionSummaryFits52px(t *testing.T) {
	if repoRoot == "" {
		t.Fatal("B107 FAIL: could not find repo root (go.mod not found)")
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, "static/css/themes.css"))
	if err != nil {
		t.Fatalf("B107 FAIL: read themes.css: %v", err)
	}
	css := string(data)

	// The collapsed summary fix is in the MAIN CSS (not @media).
	// Test for the three required rules:
	//   a) .sidebar.collapsed .sidebar-section>summary has padding:0
	//   b) .sidebar.collapsed .sidebar-section>summary has justify-content:center
	//   c) .sidebar.collapsed .sidebar-section>summary::before has display:none

	collapsedSummaryRe := regexp.MustCompile(`\.sidebar\.collapsed[[:space:]]+\.sidebar-section>summary\{([^}]*)\}`)
	m := collapsedSummaryRe.FindStringSubmatch(css)
	if m == nil {
		t.Error("B107 FAIL: .sidebar.collapsed .sidebar-section>summary rule missing in main CSS")
		return
	}
	body := m[1]
	if !regexp.MustCompile(`padding[[:space:]]*:[[:space:]]*0`).MatchString(body) {
		t.Errorf("B107 FAIL: .sidebar.collapsed .sidebar-section>summary missing padding:0 (icons overflow 52px sidebar); got: %s", strings.TrimSpace(body))
	}
	if !regexp.MustCompile(`justify-content[[:space:]]*:[[:space:]]*center`).MatchString(body) {
		t.Errorf("B107 FAIL: .sidebar.collapsed .sidebar-section>summary missing justify-content:center (icon should be centered); got: %s", strings.TrimSpace(body))
	}

	caretRe := regexp.MustCompile(`\.sidebar\.collapsed[[:space:]]+\.sidebar-section>summary::before\{([^}]*)\}`)
	allCarets := caretRe.FindAllStringSubmatch(css, -1)
	if len(allCarets) == 0 {
		t.Error("B107 FAIL: .sidebar.collapsed .sidebar-section>summary::before rule missing in main CSS")
		return
	}
	// Find the rule body that has `display:none` (hides the caret
	// on collapsed). The pre-existing rule has
	// `transform:rotate(0deg)` (resets the open-state rotation);
	// both are needed but the test specifically pins the new
	// `display:none` rule that fixes the 4px overflow.
	foundHide := false
	for _, m := range allCarets {
		if regexp.MustCompile(`display[[:space:]]*:[[:space:]]*none`).MatchString(m[1]) {
			foundHide = true
			break
		}
	}
	if !foundHide {
		t.Error("B107 FAIL: .sidebar.collapsed .sidebar-section>summary::before missing display:none rule (caret adds 10px width, summary overflows 52px sidebar)")
	}
}
