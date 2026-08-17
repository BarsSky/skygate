package handlers

// layout_v1_3_19_2_test.go — regression test for the v1.3.19.2
// follow-up (B120) that fixed the admin-breadcrumb being
// hidden under the fixed-position sidebar on PC.
//
// The bug: <main> contains the .admin-breadcrumb as a
// SIBLING of .shell (not inside it). The CSS rule
// `main .shell { margin-left: 220px; }` only applies to
// .shell — .admin-breadcrumb had no left offset, so its
// leftmost 220px sat under the sidebar. On PC, the operator
// saw only the right fragments of "Админ › Devices & Nodes
// › Devices" — the start was hidden.
//
// The fix: mirror the .shell margin-left pattern for
// .admin-breadcrumb. Plus a mobile @media reset so the
// breadcrumb fills the viewport when the sidebar is a
// drawer (<768px).
//
// This test pins 3 contracts on themes.css:
//   1. Desktop expanded sidebar (220px): breadcrumb
//      margin-left = 220px (matches the .shell rule).
//   2. Desktop collapsed sidebar (52px): breadcrumb
//      margin-left = 52px (the `~` selector that targets
//      .sidebar.collapsed ~ main .admin-breadcrumb).
//   3. Mobile (<768px): breadcrumb margin-left = 0
//      (the sidebar becomes a drawer, no column reserved).
//
// + 1 structural pin: layout.html renders the
// .admin-breadcrumb BEFORE the .shell (so the margin-left
// pattern applies to BOTH elements, not just the breadcrumb
// inside .shell).

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestB120_AdminBreadcrumbOffsetDesktopExpanded — B120
// pin: the .admin-breadcrumb has margin-left:220px on
// desktop (matching the .shell rule). Pre-fix, the
// breadcrumb sat at left=0 and was covered by the
// position:fixed sidebar.
func TestB120_AdminBreadcrumbOffsetDesktopExpanded(t *testing.T) {
	css := loadThemesCSS(t)

	// The breadcrumb offset rule for expanded sidebar.
	// Format: `main .admin-breadcrumb{margin-left:220px;...}`
	// We allow optional spaces + width:calc(...) after the
	// 220px value.
	want := regexp.MustCompile(`main\s+\.admin-breadcrumb\s*\{[^}]*margin-left\s*:\s*220px`)
	if !want.MatchString(css) {
		t.Error("B120 FAIL: themes.css missing `main .admin-breadcrumb { margin-left: 220px }` rule. " +
			"The breadcrumb is covered by the fixed sidebar on PC when the sidebar is expanded.")
	}
}

// TestB120_AdminBreadcrumbOffsetDesktopCollapsed — B120
// pin: the breadcrumb has margin-left:52px when the
// sidebar is collapsed (matches the .shell rule under
// .sidebar.collapsed). Without this, toggling the sidebar
// would shift .shell left but leave the breadcrumb
// floating over the now-narrower sidebar.
func TestB120_AdminBreadcrumbOffsetDesktopCollapsed(t *testing.T) {
	css := loadThemesCSS(t)

	// Format: `.sidebar.collapsed ~ main .admin-breadcrumb{margin-left:52px;...}`
	want := regexp.MustCompile(`\.sidebar\.collapsed\s*~\s*main\s+\.admin-breadcrumb\s*\{[^}]*margin-left\s*:\s*52px`)
	if !want.MatchString(css) {
		t.Error("B120 FAIL: themes.css missing `.sidebar.collapsed ~ main .admin-breadcrumb { margin-left: 52px }` rule. " +
			"Collapsing the sidebar would leave the breadcrumb floating over the now-narrower sidebar.")
	}
}

// TestB120_AdminBreadcrumbOffsetMobile — B120 pin: the
// breadcrumb's margin-left is reset to 0 in the mobile
// @media block (the sidebar becomes a drawer at <768px,
// so the column reservation no longer makes sense).
// Without this, the breadcrumb would be shifted 220px
// right on a phone, wasting half the viewport.
func TestB120_AdminBreadcrumbOffsetMobile(t *testing.T) {
	css := loadThemesCSS(t)

	// Look for the @media (max-width:768px) block + a
	// breadcrumb override with margin-left:0.
	mediaStart := strings.Index(css, "@media (max-width:768px)")
	if mediaStart == -1 {
		t.Fatal("B120 FAIL: themes.css missing @media (max-width:768px) block (the mobile breakpoint from B97)")
	}
	// Extract the rest of the CSS from the media start
	// and look for a breadcrumb rule with margin-left:0.
	mediaRest := css[mediaStart:]
	overrideRe := regexp.MustCompile(`main\s+\.admin-breadcrumb\s*\{[^}]*margin-left\s*:\s*0\s*!important`)
	if !overrideRe.MatchString(mediaRest) {
		t.Error("B120 FAIL: @media (max-width:768px) block missing `main .admin-breadcrumb { margin-left: 0 !important }` reset. " +
			"On mobile, the sidebar is a drawer, so the breadcrumb should fill the viewport (no 220px column reserved).")
	}
}

// TestB120_AdminBreadcrumbIsSiblingOfShell — structural
// pin: layout.html renders the .admin-breadcrumb BEFORE
// the .shell (so the margin-left rule applies to BOTH,
// not just the breadcrumb if it was inside .shell). If a
// future refactor moves the breadcrumb inside .shell, the
// fixed margin-left would still apply (it cascades), but
// the visual would change because .shell has its own
// max-width:1180px container.
func TestB120_AdminBreadcrumbIsSiblingOfShell(t *testing.T) {
	data, err := templatesFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	layout := string(data)

	// Find the <nav class="admin-breadcrumb"> index.
	bcIdx := strings.Index(layout, `class="admin-breadcrumb"`)
	if bcIdx == -1 {
		t.Fatal("B120 FAIL: layout.html missing .admin-breadcrumb element")
	}
	// Find the <div class="shell"> index.
	shellIdx := strings.Index(layout, `<div class="shell">`)
	if shellIdx == -1 {
		t.Fatal("B120 FAIL: layout.html missing <div class=\"shell\"> container")
	}
	if bcIdx > shellIdx {
		t.Errorf("B120 FAIL: .admin-breadcrumb (idx %d) is AFTER <div class=\"shell\"> (idx %d). "+
			"The B120 CSS fix assumes the breadcrumb is a SIBLING of .shell, both inside <main>. "+
			"If the breadcrumb moves inside .shell, the margin-left cascade still works but the visual changes (the breadcrumb would inherit .shell's max-width:1180px).",
			bcIdx, shellIdx)
	}
}

// loadThemesCSS is a small helper that reads the static
// CSS file from the repo root. Returns "" + t.Fatal on
// any read error (the caller can decide whether that's a
// hard test failure or a skip).
func loadThemesCSS(t *testing.T) string {
	t.Helper()
	if repoRoot == "" {
		t.Fatal("B120 FAIL: could not find repo root (go.mod not found in any parent directory)")
	}
	data, err := os.ReadFile(repoRoot + "/static/css/themes.css")
	if err != nil {
		t.Fatalf("B120 FAIL: read themes.css: %v", err)
	}
	return string(data)
}
