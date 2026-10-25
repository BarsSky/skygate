package handlers

import (
	"strings"
	"testing"
)

// B418 (2026-10-25) — THE MOBILE DRAWER MUST BE THE VISIBLE VIEWPORT, AND IT MUST OWN
// THE FOOTER IT COVERS.
//
// Operator, with a phone screenshot: «на экране смартфона меню в раскрытом состоянии
// перекрывает футтер, и также на смартфоне кнопки в меню в самом низу (смена темы языка
// выход из системы) появляются только если пролистать страницу в самый низ».
//
// Two measured causes, both in the mobile `@media (max-width:768px)` block:
//
//  1. the drawer kept `height:100vh`, and on a phone `100vh` is the LARGE viewport
//     (browser UI hidden) — so `.sidebar-user` (theme / language / logout) was painted
//     BELOW the visible edge, and because the drawer is position:fixed, scrolling the
//     page could not bring it into view until the URL bar collapsed;
//  2. the dim layer was `main::before` — a pseudo-element of <main>, while the <footer>
//     is a SIBLING of <main>, so the footer was still under the finger and a tap on it
//     (or on the dim area) did not close the drawer.
//
// These tests read the real stylesheet (the mirror the Go tests always read) and the real
// layout template, so a later rule that re-introduces `height:100vh` on the drawer, or a
// drawer block that shrinks away, fails here rather than on the operator's phone.

// mobileDrawerBlock returns the drawer rules from the mobile media query. It is a
// substring slice rather than a CSS parser on purpose: the contract is about the RULES
// THE MOBILE BREAKPOINT DECLARES, and the shell contract greps the same shape.
func mobileDrawerBlock(t *testing.T, css string) string {
	t.Helper()
	start := strings.Index(css, "@media (max-width:768px){")
	if start < 0 {
		start = strings.Index(css, "@media (max-width: 768px) {")
	}
	if start < 0 {
		t.Fatal("B418 FAIL: the mobile breakpoint @media (max-width:768px) is gone")
	}
	end := strings.Index(css[start:], ".sidebar nav a{padding:12px 14px")
	if end < 0 {
		end = len(css) - start
	}
	return css[start : start+end]
}

// stripCSSComments removes /* … */ spans. The drawer rule's own banner NAMES the
// declaration it must not carry, so an assertion that reads prose would fail on its own
// explanation (the B250 lesson: check the declaration, not the sentence about it).
func stripCSSComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "/*") {
			end := strings.Index(s[i:], "*/")
			if end < 0 {
				break
			}
			i += end + 2
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// mobileDrawerRule returns just the `.sidebar{…}` rule of the mobile block — the block
// also carries `body:has(…){overflow:hidden}`, so a substring search over the whole
// block cannot tell "the drawer clips" from "the page behind is locked".
func mobileDrawerRule(t *testing.T, block string) string {
	t.Helper()
	start := strings.Index(block, "\n  .sidebar{")
	if start < 0 {
		t.Fatal("B418 FAIL: the mobile block has no `.sidebar{…}` rule")
	}
	rest := block[start:]
	end := strings.Index(rest, "\n  }")
	if end < 0 {
		t.Fatal("B418 FAIL: the mobile `.sidebar{…}` rule is not closed")
	}
	return stripCSSComments(rest[:end])
}

func TestB418_MobileDrawerIsTheVisibleViewport(t *testing.T) {
	css := readThemesCSS(t)
	drawer := mobileDrawerRule(t, mobileDrawerBlock(t, css))

	// The drawer must be anchored to the viewport, not sized with 100vh.
	if !strings.Contains(drawer, "top:0;bottom:0;height:auto") {
		t.Error("B418 FAIL: the mobile drawer is not anchored with top:0;bottom:0;height:auto — " +
			"on a phone 100vh is the LARGE viewport, so its bottom block is painted below the visible edge")
	}
	if !strings.Contains(drawer, "max-height:100dvh") {
		t.Error("B418 FAIL: the mobile drawer does not cap itself at 100dvh (dynamic viewport)")
	}
	// …and it must NOT clip: the bell / theme / language popovers hang past the 280px
	// edge (B392 measured the cut at ~38 % of the panel). Containment comes from the
	// flex chain (nav flex:1 + min-height:0, `.sidebar-user` flex-shrink:0), not from
	// `overflow:hidden`.
	if strings.Contains(drawer, "overflow:hidden") {
		t.Error("B418 FAIL: the drawer clips with overflow:hidden — the bell/theme/language " +
			"popovers lose their right part on a phone (the B392 «перекрывается вкладкой» cut)")
	}
	if !strings.Contains(drawer, "padding-bottom:env(safe-area-inset-bottom") {
		t.Error("B418 FAIL: no safe-area padding — the bottom buttons sit under the home indicator")
	}
}

// TestB418_BottomBlockCannotBePushedOut is the flexbox half: the nav is the only
// scroller, so it must be allowed to shrink (min-height:0) and the user block must be
// shrink-proof. Without both, a long menu pushes theme/language/logout off the drawer.
func TestB418_BottomBlockCannotBePushedOut(t *testing.T) {
	css := readThemesCSS(t)
	block := mobileDrawerBlock(t, css)
	if !strings.Contains(block, ".sidebar nav{min-height:0}") {
		t.Error("B418 FAIL: `.sidebar nav{min-height:0}` is missing — a flex item refuses to " +
			"shrink below its content and pushes the bottom block out of the drawer")
	}
	if !strings.Contains(css, ".sidebar nav{display:flex;flex-direction:column;gap:2px;padding:8px;flex:1;overflow-y:auto}") {
		t.Error("B418 FAIL: `.sidebar nav` is no longer the drawer's scroller (flex:1 + overflow-y:auto)")
	}
	if !strings.Contains(css, ".sidebar-user{padding:8px;border-top:1px solid var(--border);flex-shrink:0}") {
		t.Error("B418 FAIL: `.sidebar-user` lost flex-shrink:0 — the theme/language/logout block can be squeezed away")
	}
}

// TestB418_BackdropOwnsTheFooter pins the second half: the dim layer is a real label
// (so it can close the drawer and block the footer), and the old pseudo-element overlay
// is gone.
func TestB418_BackdropOwnsTheFooter(t *testing.T) {
	css := readThemesCSS(t)
	block := mobileDrawerBlock(t, css)
	if !strings.Contains(block, ".sidebar-backdrop{display:none;position:fixed;inset:0;") &&
		!strings.Contains(block, ".sidebar-backdrop{") {
		t.Error("B418 FAIL: no .sidebar-backdrop rule")
	}
	if !strings.Contains(block, ".sidebar-toggle-input:checked ~ .sidebar-backdrop{display:block}") {
		t.Error("B418 FAIL: the backdrop is not driven by the same checkbox as the drawer")
	}
	if strings.Contains(block, "~ main::before") {
		t.Error("B418 FAIL: the old `main::before` overlay is back — it cannot close the drawer and " +
			"leaves the <footer> (a SIBLING of <main>) reachable while the menu is open")
	}
	if !strings.Contains(block, "body:has(.sidebar-toggle-input:checked){overflow:hidden}") {
		t.Error("B418 FAIL: the page behind the drawer can still scroll (that scroll used to reveal " +
			"the drawer's own bottom block)")
	}

	layout := readLayout(t)
	if !strings.Contains(layout, `<label for="sidebar-toggle" class="sidebar-backdrop"`) {
		t.Error("B418 FAIL: layout.html has no backdrop label — the drawer cannot be closed by tapping " +
			"outside it, and the footer stays clickable underneath")
	}
}

// TestB418_StylesheetURLIsVersioned pins the reason a fix can look like it did not
// land: the served stylesheet has no content hash and is cacheable for a day, so an
// unversioned URL means a phone may render the PREVIOUS release's CSS after an update.
func TestB418_StylesheetURLIsVersioned(t *testing.T) {
	layout := readLayout(t)
	want := `href="/static/css/themes.css?v={{.Version}}"`
	if !strings.Contains(layout, want) {
		t.Error("B418 FAIL: the themes.css link lost the build version — a phone can reuse a " +
			"day-old copy of the file every UI release edits, so the update looks like it did nothing")
	}
	if strings.Contains(layout, `href="/static/css/themes.css"`) {
		t.Error("B418 FAIL: the unversioned themes.css URL is back")
	}
}

// TestB418_DrawerStaysAboveContent pins the stacking order the operator described as
// «меню перекрывает футтер»: the drawer above the backdrop, the backdrop above the page.
func TestB418_DrawerStaysAboveContent(t *testing.T) {
	css := readThemesCSS(t)
	if !strings.Contains(css, "z-index:100;display:flex;flex-direction:column") {
		t.Fatal("B418: the sidebar no longer declares z-index:100 as part of its base rule")
	}
	if !strings.Contains(css, ".sidebar-backdrop{") || !strings.Contains(css, "z-index:90") {
		t.Fatal("B418: the backdrop must sit at z-index:90 (above the page, below the drawer's 100)")
	}
}
