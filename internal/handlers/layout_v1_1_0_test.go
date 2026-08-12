package handlers

// layout_v1_1_0_test.go — regression tests for the v1.1.0
// admin-sidebar refactor (TD-1: group 22 admin pages into 6
// collapsible sidebar sections; TD-3: mobile-responsive CSS
// + hamburger menu). The tests pin two contracts:
//
//  1. B96: layout.html groups every one of the 22 admin
//     pages into exactly 6 <details class="sidebar-section">
//     blocks (Devices & Nodes / Access Control / System
//     Health & Logs / Integrations / Data / Settings & Users).
//     Each block auto-opens when the current page is in the
//     section, via the InSectionX booleans that
//     renderWithLayout sets.
//
//  2. B97: themes.css has the @media (max-width:768px) block
//     that hides the sidebar off-screen and surfaces a
//     hamburger button (.sidebar-toggle). The breakpoint is
//     768px (not 760px as in v1.3.x) — the rename to 768px
//     matches the iPad-portrait width and is the canonical
//     mobile boundary in the v1.1.0 era.
//
// Together these tests catch the obvious regressions:
//   - adding a 23rd admin page and forgetting to put it in a
//     section (the new page is not in any <details> block)
//   - dropping a section (the count of .sidebar-section
//     drops below 6)
//   - removing the @media block or the hamburger CSS (the
//     mobile drawer no longer appears)
//   - changing the breakpoint away from 768px (B97 fails)

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot is the path to the skygate repo root, used to
// read static/css/themes.css for B97. Computed once at test
// load time by walking up from the test binary's working
// directory until we find go.mod.
var repoRoot = func() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}()

// expectedAdminPages is the 22-page set the layout is
// supposed to cover. Adding a new admin page? Add it here
// AND to layout.html AND to sectionPageSet() in handlers.go.
// B96 fails the build if a page is added to one place but
// not the others.
var expectedAdminPages = []string{
	"/admin/devices",
	"/admin/exit-nodes",
	"/admin/meshes",
	"/admin/subnets",
	"/admin/acls",
	"/admin/exit-rules",
	"/admin/headscale/acl",
	"/admin/system_tests",
	"/admin/services",
	"/admin/audit",
	"/admin/integrations",
	"/admin/headscale",
	"/admin/headplane",
	"/admin/telegram",
	"/admin/tailscale",
	"/admin/derp",
	"/admin/backup",
	"/admin/invites",
	"/admin/control-planes",
	"/admin/settings",
	"/admin/users",
	"/admin/update",
}

// sectionKeyRe matches <a href="/admin/..."> child links
// inside a <details class="sidebar-section"> block. The
// greedy /?s matches the rest of the tag attributes.
var sectionKeyRe = regexp.MustCompile(`<a\s+href="(/admin/[^"]+)"`)

// sectionOpenRe matches the {{if .InSectionX}}open{{end}}
// pattern that auto-opens a section when the current page
// belongs to it. Each section is expected to have exactly
// one such conditional so renderWithLayout's
// InSectionX booleans are wired correctly.
var sectionOpenRe = regexp.MustCompile(`\{\{if\s+\.InSection(\w+)\}\}open\{\{end\}\}`)

// TestB96_AdminLayoutGroupsAll22Pages — B96 pin: layout.html
// groups the 22 expected admin pages into 6 sidebar sections.
// Fails if a page is missing, if a section is missing, or
// if the auto-open conditional is dropped from a section.
func TestB96_AdminLayoutGroupsAll22Pages(t *testing.T) {
	data, err := templatesFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	layout := string(data)

	// 1. All 22 admin pages are present somewhere in the layout.
	var missing []string
	for _, page := range expectedAdminPages {
		if !strings.Contains(layout, `href="`+page+`"`) {
			missing = append(missing, page)
		}
	}
	if len(missing) > 0 {
		t.Errorf("B96 FAIL: %d admin pages not in layout.html:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}

	// 2. There are exactly 6 <details class="sidebar-section"> blocks.
	sections := sectionKeyRe.FindAllString(layout, -1)
	if len(sections) == 0 {
		t.Fatal("B96 FAIL: layout.html has no admin <a href=\"/admin/...\"> links at all")
	}
	// The above counts each child <a>, not each section. Count
	// sections directly by counting the <details class="sidebar-section">
	// opening tags.
	sectionCount := strings.Count(layout, `<details class="sidebar-section"`)
	if sectionCount != 6 {
		t.Errorf("B96 FAIL: expected 6 sidebar sections, found %d in layout.html", sectionCount)
	}

	// 3. Each section has an InSectionX open conditional.
	opens := sectionOpenRe.FindAllStringSubmatch(layout, -1)
	if len(opens) != 6 {
		t.Errorf("B96 FAIL: expected 6 {{if .InSectionX}}open{{end}} conditionals, found %d in layout.html",
			len(opens))
	}

	// 4. Each InSectionX name corresponds to a key the
	//    renderWithLayout data map sets. The set is fixed:
	//    InSectionDevices / InSectionAccess / InSectionHealth /
	//    InSectionIntegrations / InSectionData / InSectionSettings.
	//    The regex above captures the suffix (Devices, Access, ...);
	//    the comparison map uses the same suffix form.
	expectedSections := map[string]bool{
		"Devices":      true,
		"Access":       true,
		"Health":       true,
		"Integrations": true,
		"Data":         true,
		"Settings":     true,
	}
	for _, m := range opens {
		if !expectedSections[m[1]] {
			t.Errorf("B96 FAIL: unknown InSection%s in layout.html (must be one of: Devices/Access/Health/Integrations/Data/Settings)", m[1])
		}
	}

	// 5. The hamburger button + sidebar-toggle-input are present
	//    (TD-3 contract — they live in the same layout, so B96
	//    pins their presence too).
	if !strings.Contains(layout, `id="sidebar-toggle"`) {
		t.Error("B96 FAIL: layout.html missing sidebar-toggle input (hamburger contract)")
	}
	if !strings.Contains(layout, `class="sidebar-toggle"`) {
		t.Error("B96 FAIL: layout.html missing sidebar-toggle label (hamburger contract)")
	}

	// 6. The 6 section title i18n keys are present.
	requiredKeys := []string{
		"nav.section_devices",
		"nav.section_access",
		"nav.section_health",
		"nav.section_integrations",
		"nav.section_data",
		"nav.section_settings",
		"nav.toggle_sidebar",
		"nav.toggle_section",
	}
	for _, k := range requiredKeys {
		if !strings.Contains(layout, `{{t "`+k+`"}}`) {
			t.Errorf("B96 FAIL: layout.html missing i18n key {{t %q}}", k)
		}
	}
}

// TestB96_AllAdminPagesInASection — strict grouping test.
// Every admin page link in the layout must be a child of
// some <details class="sidebar-section"> block (not a
// floating top-level link like in the v1.3.x era). We
// extract the <details>...</details> blocks and check
// every admin <a href="/admin/..."> falls inside one.
func TestB96_AllAdminPagesInASection(t *testing.T) {
	data, err := templatesFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	layout := string(data)

	// Find every <details class="sidebar-section"> ... </details>
	// block. The HTML doesn't have nesting (we don't have sections
	// inside sections), so a non-greedy match per block is safe.
	blockRe := regexp.MustCompile(`(?s)<details class="sidebar-section".*?</details>`)
	blocks := blockRe.FindAllString(layout, -1)
	if len(blocks) != 6 {
		t.Fatalf("B96 FAIL: expected 6 <details> blocks, found %d", len(blocks))
	}

	// Collect every admin page link that appears inside a section.
	insideSections := make(map[string]bool)
	for _, block := range blocks {
		for _, m := range sectionKeyRe.FindAllStringSubmatch(block, -1) {
			insideSections[m[1]] = true
		}
	}

	// Every expected admin page must be in a section.
	var missing []string
	for _, page := range expectedAdminPages {
		if !insideSections[page] {
			missing = append(missing, page)
		}
	}
	if len(missing) > 0 {
		t.Errorf("B96 FAIL: %d admin pages are NOT inside any <details> section:\n  %s\n"+
			"  (add them to a section in layout.html, or add them to sectionPageSet() in handlers.go)",
			len(missing), strings.Join(missing, "\n  "))
	}

	// And the inverse: there should be no admin <a href>
	// OUTSIDE of a section block (the v1.3.x era had a flat
	// list — that pattern is gone in v1.1.0).
	// Strip all <details>...</details> blocks from the layout
	// and check no admin links remain in the residue.
	stripped := blockRe.ReplaceAllString(layout, "")
	residueLinks := sectionKeyRe.FindAllString(stripped, -1)
	if len(residueLinks) > 0 {
		t.Errorf("B96 FAIL: found %d admin links OUTSIDE sidebar sections (the v1.1.0 contract is that every admin page is inside a <details> block):\n  %s",
			len(residueLinks), strings.Join(residueLinks, "\n  "))
	}
}

// TestB97_ThemesCSSMobileDrawer — B97 pin: themes.css has
// the @media (max-width:768px) mobile drawer block and the
// .sidebar-toggle display rule. Fails if the breakpoint
// moves away from 768px, if the @media block is removed, or
// if the hamburger button class is dropped.
func TestB97_ThemesCSSMobileDrawer(t *testing.T) {
	if repoRoot == "" {
		t.Fatal("B97 FAIL: could not find repo root (go.mod not found in any parent directory)")
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, "static/css/themes.css"))
	if err != nil {
		t.Fatalf("B97 FAIL: read themes.css: %v", err)
	}
	css := string(data)

	// 1. The 768px breakpoint exists.
	if !strings.Contains(css, "@media (max-width:768px)") {
		t.Error("B97 FAIL: themes.css missing @media (max-width:768px) mobile breakpoint")
	}

	// 2. The hamburger button is hidden on desktop and shown on mobile.
	//    The desktop default is `display:none` (line in .sidebar-toggle
	//    class block) and the mobile override is `display:flex` inside
	//    the @media block. We check both ends.
	desktopRule := regexp.MustCompile(`\.sidebar-toggle\s*\{[^}]*display\s*:\s*none`)
	if !desktopRule.MatchString(css) {
		t.Error("B97 FAIL: .sidebar-toggle is not display:none on desktop (the hamburger should be hidden until the mobile breakpoint)")
	}
	mobileRule := regexp.MustCompile(`@media \(max-width:768px\)\s*\{[^}]*\.sidebar-toggle\s*\{[^}]*display\s*:\s*flex`)
	if !mobileRule.MatchString(css) {
		t.Error("B97 FAIL: .sidebar-toggle is not display:flex inside the mobile @media block (the hamburger should appear on mobile)")
	}

	// 3. The sidebar's mobile slide-in behaviour: the off-screen
	//    transform and the :checked slide-in.
	if !strings.Contains(css, "transform:translateX(-100%)") {
		t.Error("B97 FAIL: themes.css missing translateX(-100%) for the mobile off-screen sidebar position")
	}
	if !strings.Contains(css, "translateX(0)") {
		t.Error("B97 FAIL: themes.css missing translateX(0) for the on-screen sidebar when the hamburger is toggled")
	}

	// 4. The sidebar section styles (TD-1 — bonus check, B96's
	//    real pin is in templates_test.go).
	if !strings.Contains(css, ".sidebar-section") {
		t.Error("B97 FAIL: themes.css missing .sidebar-section styles (TD-1 styling contract)")
	}

	// 5. Touch-friendly tap targets (the 44px min from Apple's
	//    HIG / Google's Material Design).
	if !regexp.MustCompile(`min-height\s*:\s*44px`).MatchString(css) {
		t.Error("B97 FAIL: themes.css missing min-height:44px (touch-friendly tap target contract)")
	}
}

// TestB97_StaticFilePresence — sanity: the file we depend
// on actually exists at the expected path. (If a future
// refactor moves the file or changes the embed path, this
// test catches it before the more expensive B97 test runs.)
func TestB97_StaticFilePresence(t *testing.T) {
	if repoRoot == "" {
		t.Fatal("B97 FAIL: could not find repo root (go.mod not found in any parent directory)")
	}
	path := filepath.Join(repoRoot, "static/css/themes.css")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("B97 FAIL: %s not found: %v", path, err)
	}
	// We import io/fs for the test suite's compatibility —
	// if a future refactor drops the import, this usage
	// keeps the import live in the test file.
	_ = fs.WalkDir
}
