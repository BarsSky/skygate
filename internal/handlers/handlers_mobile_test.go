package handlers

// handlers_mobile_test.go — regression tests for the v1.3.9
// mobile-friendly fixes (B105). Two contracts:
//
//   1. The 7 admin templates that previously lacked
//      `<div class="table-wrap">` around their `<table>`
//      now have it — without the wrap, wide tables overflow
//      the card boundary on narrow viewports. /admin/devices
//      got the wrap in v0.33.1.7 (B50); the rest in v1.3.9.
//
//   2. The .title-row mobile padding (60px) prevents the
//      .sidebar-toggle hamburger button from overlapping
//      the page title. The pre-fix CSS had a comment
//      claiming the padding existed but never actually
//      applied it. The hamburger is fixed at top:12px,
//      left:12px, 40×40px; the .title-row needs padding-left
//      ≥ 40 + 8 + 12 = 60px to clear it.
//
// Together these tests catch the "removed a CSS rule but
// forgot to verify" regression. The companion shell check
// scripts/check_b105.sh pins the same contracts at deploy
// time (this test runs in the unit test cycle; the shell
// check runs in verify_pre_deploy.sh).

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// adminTemplatesWithTableWrap — the 7 admin templates that
// previously lacked the wrap. Adding a new admin table
// without the wrap is a regression.
var adminTemplatesWithTableWrap = []string{
	"templates/admin/audit.html",
	"templates/admin/exit_nodes.html",
	"templates/admin/headscale.html",
	"templates/admin/invites.html",
	"templates/admin/meshes.html",
	"templates/admin/subnets.html",
	"templates/admin/user_subnet.html",
}

// TestB105_AdminTablesHaveTableWrap — every admin template
// in the list must contain `<div class="table-wrap">` AND
// the wrap must appear BEFORE the first `<table>` (otherwise
// the wrap is a no-op for the wide table — the rule only
// applies to its children).
func TestB105_AdminTablesHaveTableWrap(t *testing.T) {
	for _, p := range adminTemplatesWithTableWrap {
		data, err := templatesFS.ReadFile(p)
		if err != nil {
			t.Fatalf("B105 FAIL: read %s: %v", p, err)
		}
		html := string(data)
		if !strings.Contains(html, `class="table-wrap"`) {
			t.Errorf("B105 FAIL: %s missing <div class=\"table-wrap\"> (mobile horizontal scroll broken)", p)
			continue
		}
		// The wrap must appear BEFORE the first <table>.
		// Use a simple index comparison — the .table-wrap
		// div is a few lines above the table.
		wrapIdx := strings.Index(html, `class="table-wrap"`)
		tableIdx := strings.Index(html, "<table>")
		if wrapIdx < 0 || tableIdx < 0 {
			continue // already errored above
		}
		if wrapIdx > tableIdx {
			t.Errorf("B105 FAIL: %s has <table> BEFORE .table-wrap (wrap is below the table, no-op)", p)
		}
	}
}

// TestB105_TitleRowMobilePadding — .title-row must have
// padding-left:60px on mobile (inside the @media (max-width:768px)
// block). Without this the hamburger at top:12px,left:12px,40×40px
// overlaps the page title (h2).
func TestB105_TitleRowMobilePadding(t *testing.T) {
	if repoRoot == "" {
		t.Fatal("B105 FAIL: could not find repo root (go.mod not found)")
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, "static/css/themes.css"))
	if err != nil {
		t.Fatalf("B105 FAIL: read themes.css: %v", err)
	}
	css := string(data)
	// Find the @media (max-width:768px) block. We use a
	// simple brace-counting parser — the CSS doesn't have
	// nested media queries that would break it.
	mediaStart := strings.Index(css, "@media (max-width:768px)")
	if mediaStart < 0 {
		t.Fatal("B105 FAIL: themes.css missing @media (max-width:768px) block")
	}
	// Find the opening { after @media.
	openBrace := strings.Index(css[mediaStart:], "{")
	if openBrace < 0 {
		t.Fatal("B105 FAIL: @media block missing opening brace")
	}
	// Brace-count to find the matching }.
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
		t.Fatal("B105 FAIL: @media block has unmatched braces")
	}
	block := css[mediaStart+openBrace : i]
	// The rule: .title-row { ... padding-left: 60px ... }
	// (any other properties are OK; the padding-left must be 60px).
	re := regexp.MustCompile(`\.title-row\s*\{([^}]*)\}`)
	m := re.FindStringSubmatch(block)
	if m == nil {
		t.Error("B105 FAIL: .title-row rule missing inside @media (max-width:768px)")
		return
	}
	body := m[1]
	if !regexp.MustCompile(`padding-left\s*:\s*60px`).MatchString(body) {
		t.Errorf("B105 FAIL: .title-row inside @media (max-width:768px) is missing padding-left:60px (hamburger overlaps page title); got: %s", strings.TrimSpace(body))
	}
}

// _ ensures io/fs is imported — used if a future test needs
// to walk the embedded FS.
var _ = fs.WalkDir
