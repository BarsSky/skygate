package handlers

// layout_v1_3_19_2_b121_test.go — regression tests for the
// v1.3.19.2 follow-up (B121) that:
//   - added the new "Mint" theme (silver + mint-green palette)
//   - improved Linear/NVIDIA/Sentry form contrast (forms were
//     blending into the page bg on dark themes)
//   - added custom thin scrollbar styles for all themes
//     (the browser default scrollbar in dark themes was a
//     visually jarring 15-17px wide white block on a dark
//     background — B121 makes it a thin 8px themed bar).
//
// 5 contracts:
//   1. themes.css has the `[data-theme="mint"]` block with
//      the new mint palette (--bg silver, --accent mint).
//   2. layout.html theme-picker has a Mint option linking
//      to /settings/theme?theme=mint.
//   3. themes.css has custom scrollbar styles (Firefox
//      `scrollbar-width: thin` + WebKit ::-webkit-scrollbar).
//   4. themes.css has the dark-theme form contrast bump
//      (input border-width 1.5px + box-shadow inset).
//   5. db.IsValidTheme("mint") returns true (the constant
//      + label are wired in internal/db/db.go).

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestB121_MintThemeInThemesCSS — B121 pin: themes.css has
// the [data-theme="mint"] block with the silver+mint palette.
func TestB121_MintThemeInThemesCSS(t *testing.T) {
	if repoRoot == "" {
		t.Fatal("B121 FAIL: could not find repo root")
	}
	data, err := os.ReadFile(repoRoot + "/static/css/themes.css")
	if err != nil {
		t.Fatalf("B121 FAIL: read themes.css: %v", err)
	}
	css := string(data)

	// The [data-theme="mint"]{...} block must exist.
	if !strings.Contains(css, `[data-theme="mint"]`) {
		t.Error("B121 FAIL: themes.css missing [data-theme=\"mint\"] block. " +
			"The Mint theme (B121) was added to internal/db/db.go " +
			"but themes.css has no matching CSS rule — the theme " +
			"would render with default variables.")
	}

	// The mint palette has these expected values. Each
	// verification is a separate sub-check so a future
	// palette tweak doesn't fail the whole test.
	wantPalette := map[string]string{
		"--bg":          "#f5f7f6", // silver with subtle mint tint
		"--bg-card":     "#ffffff", // pure white for card lift
		"--border":      "#d4dad6", // soft silver border
		"--accent":      "#10b981", // mint/emerald accent
		"--accent-fg":   "#ffffff", // white text on mint
		"--accent-hover": "#059669", // deeper mint
	}
	// Extract the [data-theme="mint"]{...} block.
	blockRe := regexp.MustCompile(`(?s)\[data-theme="mint"\]\s*\{[^}]*\}`)
	m := blockRe.FindString(css)
	if m == "" {
		t.Fatal("B121 FAIL: [data-theme=\"mint\"] block not found or empty")
	}
	for key, want := range wantPalette {
		// Look for `--key:want` (with optional spaces).
		pat := regexp.MustCompile(regexp.QuoteMeta(key) + `\s*:\s*` + regexp.QuoteMeta(want))
		if !pat.MatchString(m) {
			t.Errorf("B121 FAIL: mint palette %q = %q not found in [data-theme=\"mint\"] block. "+
				"Got block: %s", key, want, strings.TrimSpace(m))
		}
	}
}

// TestB121_MintOptionInLayoutHTML — B121 pin: layout.html's
// theme-picker has a Mint option linking to
// /settings/theme?theme=mint. The icon should be fa-leaf
// (mint = leaf) per the design. Without this, the Mint
// theme is registered in DB but the user can't pick it
// from the UI.
func TestB121_MintOptionInLayoutHTML(t *testing.T) {
	data, err := templatesFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	layout := string(data)

	// 1. The /settings/theme?theme=mint link exists.
	if !strings.Contains(layout, `/settings/theme?theme=mint`) {
		t.Error("B121 FAIL: layout.html theme-picker missing /settings/theme?theme=mint link. " +
			"Users can't pick the Mint theme from the UI.")
	}
	// 2. The Mint option has the leaf icon (matching the
	//    other themes' icons: fa-moon/fa-sun/fa-bolt/fa-microchip).
	if !strings.Contains(layout, `fa-leaf`) {
		t.Error("B121 FAIL: layout.html Mint option missing fa-leaf icon. " +
			"Use a thematic icon — fa-leaf for Mint (matches the natural theme).")
	}
	// 3. The Mint option's display name is "Mint".
	//    Find the link line and check the next text node.
	linkRe := regexp.MustCompile(`<a[^>]*href="/settings/theme\?theme=mint"[^>]*>.*?</a>`)
	linkMatch := linkRe.FindString(layout)
	if linkMatch == "" {
		t.Fatal("B121 FAIL: could not find /settings/theme?theme=mint <a> element in layout.html")
	}
	if !strings.Contains(linkMatch, "Mint") {
		t.Errorf("B121 FAIL: Mint <a> option missing 'Mint' text label. Got: %s", linkMatch)
	}
}

// TestB121_CustomScrollbarStyles — B121 pin: themes.css has
// the thin themed scrollbar (Firefox `scrollbar-width: thin`
// + WebKit ::-webkit-scrollbar). The pre-fix browser default
// in dark themes was a 15-17px wide white block that visually
// broke the page.
func TestB121_CustomScrollbarStyles(t *testing.T) {
	if repoRoot == "" {
		t.Fatal("B121 FAIL: could not find repo root")
	}
	data, err := os.ReadFile(repoRoot + "/static/css/themes.css")
	if err != nil {
		t.Fatalf("B121 FAIL: read themes.css: %v", err)
	}
	css := string(data)

	// 1. Firefox scrollbar-width: thin (the standard property).
	if !strings.Contains(css, "scrollbar-width:thin") &&
		!strings.Contains(css, "scrollbar-width: thin") {
		t.Error("B121 FAIL: themes.css missing `scrollbar-width: thin` (Firefox standard scrollbar).")
	}
	// 2. Firefox scrollbar-color (track + thumb) using theme vars.
	if !regexp.MustCompile(`scrollbar-color\s*:\s*var\(--border-strong\)\s+transparent`).MatchString(css) {
		t.Error("B121 FAIL: themes.css missing `scrollbar-color: var(--border-strong) transparent` " +
			"(Firefox standard scrollbar coloring — thumb in --border-strong, transparent track).")
	}
	// 3. WebKit ::-webkit-scrollbar width: 8px (thin).
	if !regexp.MustCompile(`::-webkit-scrollbar\s*\{[^}]*width\s*:\s*8px`).MatchString(css) {
		t.Error("B121 FAIL: themes.css missing `*::-webkit-scrollbar { width: 8px }` " +
			"(WebKit/Chromium scrollbar width).")
	}
	// 4. WebKit thumb uses var(--border) with border-radius.
	if !regexp.MustCompile(`::-webkit-scrollbar-thumb\s*\{[^}]*background\s*:\s*var\(--border\)`).MatchString(css) {
		t.Error("B121 FAIL: themes.css missing `*::-webkit-scrollbar-thumb { background: var(--border) }` " +
			"(WebKit thumb uses the theme's --border color).")
	}
}

// TestB121_DarkThemeFormContrastBump — B121 pin: linear/
// nvidia/sentry inputs have a 1.5px border + inset shadow so
// forms stand out from the page bg. The pre-fix 1px border
// in the same color as --border-strong barely contrasted
// with the dark --bg, and the operator reported "forms
// blend together" on Linear.
//
// Note: the rule is structured as ONE combined selector list
// for all 3 dark themes (linear + nvidia + sentry share one
// block), so the test verifies each theme's name appears in
// the selector list of a block containing the 1.5px border
// + inset shadow declarations.
func TestB121_DarkThemeFormContrastBump(t *testing.T) {
	if repoRoot == "" {
		t.Fatal("B121 FAIL: could not find repo root")
	}
	data, err := os.ReadFile(repoRoot + "/static/css/themes.css")
	if err != nil {
		t.Fatalf("B121 FAIL: read themes.css: %v", err)
	}
	css := string(data)

	themes := []string{"linear", "nvidia", "sentry"}
	for _, theme := range themes {
		// Find a CSS block that:
		//   1. has a [data-theme="<theme>"] input selector
		//   2. contains `border-width:1.5px` (the bump)
		//   3. contains `box-shadow:inset` (the depth)
		//
		// We use a non-greedy regex that matches the whole
		// selector list + block, since the block can be
		// shared across themes via comma-separated selectors.
		pat := regexp.MustCompile(
			`\[data-theme="` + theme + `"\][^{}]*input\[type=text\][^{}]*\{[^}]*border-width\s*:\s*1\.5px[^}]*box-shadow\s*:\s*inset`)
		if !pat.MatchString(css) {
			t.Errorf("B121 FAIL: [data-theme=\"%s\"] input rule missing `border-width: 1.5px` + `box-shadow: inset`. "+
				"Pre-fix, the 1px border in --border-strong was barely visible against the dark --bg, "+
				"causing forms to blend into the page background.", theme)
		}
	}
}
