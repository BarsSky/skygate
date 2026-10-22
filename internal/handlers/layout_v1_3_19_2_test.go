package handlers

// layout_v1_3_19_2_test.go — the v1.3.19.2 follow-up (B120) asked for the
// admin-breadcrumb to stop hiding under the fixed sidebar. This file pins the
// answer, and the answer changed once — deliberately:
//
//   - B120 (2026-08-17) fixed it by MIRRORING the .shell offset onto the
//     breadcrumb: `main .admin-breadcrumb{margin-left:220px}`, 52px when the
//     sidebar collapses, 0 on mobile.
//   - B411 (2026-10-22) moved the reservation to <main> itself, because the old
//     `main .shell{margin-left:220px}` overrode only the LEFT margin of a
//     `margin:0 auto` box: `margin-right:auto` then swallowed every spare pixel,
//     so the content was pinned to the sidebar while the empty band grew on the
//     right (measured: 520px at 1920, 1160px at 2560). With the offset on
//     <main>, .shell centres in the space that actually exists and the
//     breadcrumb — a sibling inside <main> — inherits the same left edge, so the
//     mirror is not just unnecessary, it would DOUBLE the offset.
//
// The B120 INTENT is unchanged and is still pinned here: the breadcrumb must not
// sit under the sidebar, at either sidebar width, and it must fill the row. What
// changed is which element carries the number.
//
// Contracts:
//   A. <main> carries the desktop reservation (220px), and it is NOT on .shell
//      (a `main .shell{margin-left:<px>}` rule would re-create the empty band).
//   B. `.sidebar.collapsed ~ main{margin-left:52px}` — collapsing moves the
//      content column, and nothing has to be mirrored per child.
//   C. The breadcrumb has no offset of its own (0 / 100%), i.e. it cannot be
//      offset twice; the mobile @media block resets <main> to 0.
//   D. layout.html still renders .admin-breadcrumb BEFORE .shell (siblings), so
//      the inherited left edge applies to both.
//   E. B411 wide-screen step: the shell may grow past 1180px on a wide display
//      (otherwise the centring fix would still leave 1400px+ unused at 3440).

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// loadThemesEmbedded reads the stylesheet the binary actually SERVES. B392
// measured that the runtime serves internal/staticfs/static/css/themes.css while
// the Go tests historically read the root mirror — so a fix applied to one side
// left the gate green and the panel broken. Assertions here run against BOTH and
// fail if they disagree, which is what B392's shell contract also demands.
func loadThemesBoth(t *testing.T) (embedded, mirror string) {
	t.Helper()
	if repoRoot == "" {
		t.Fatal("B411 FAIL: could not find repo root (go.mod not found in any parent directory)")
	}
	emb, err := os.ReadFile(repoRoot + "/internal/staticfs/static/css/themes.css")
	if err != nil {
		t.Fatalf("B411 FAIL: read the embedded themes.css: %v", err)
	}
	mir, err := os.ReadFile(repoRoot + "/static/css/themes.css")
	if err != nil {
		t.Fatalf("B411 FAIL: read the root themes.css mirror: %v", err)
	}
	if string(emb) != string(mir) {
		t.Fatalf("B411 FAIL: the embedded stylesheet and the root mirror differ (%d vs %d bytes) — the panel serves the embedded one, so the mirror must be synced (B392)",
			len(emb), len(mir))
	}
	return string(emb), string(mir)
}

// ruleBody returns the declaration block of the first CSS rule whose selector
// matches re, ignoring whitespace differences. "" means "no such rule".
func ruleBody(t *testing.T, css string, re *regexp.Regexp) string {
	t.Helper()
	loc := re.FindStringIndex(css)
	if loc == nil {
		return ""
	}
	open := strings.Index(css[loc[0]:loc[1]], "{")
	if open == -1 {
		return ""
	}
	start := loc[0] + open + 1
	end := strings.Index(css[start:], "}")
	if end == -1 {
		return ""
	}
	return css[start : start+end]
}

// TestB411_MainCarriesTheSidebarReservation — contract A.
func TestB411_MainCarriesTheSidebarReservation(t *testing.T) {
	css, _ := loadThemesBoth(t)

	mainRule := regexp.MustCompile(`(?m)^[ \t]*main[ \t]*\{[^}]*\}`)
	body := ruleBody(t, css, mainRule)
	if !strings.Contains(body, "margin-left:220px") {
		t.Error("B411 FAIL: <main> does not carry `margin-left:220px`. The sidebar reservation moved here from .shell in B411; without it the content sits under the fixed sidebar.")
	}

	// The old rule must be gone: it is what pushed every spare pixel to the right.
	old := regexp.MustCompile(`(?m)^[ \t]*main[ \t]+\.shell[ \t]*\{[^}]*margin-left[ \t]*:[ \t]*[0-9]+px`)
	if old.MatchString(css) {
		t.Error("B411 FAIL: `main .shell{margin-left:<N>px}` is back. It overrides only the left half of the shell's `margin:0 auto`, so all spare width goes to the RIGHT — the empty band the operator reported (520px at 1920, 1160px at 2560).")
	}
}

// TestB411_CollapsedSidebarMovesTheColumn — contract B.
func TestB411_CollapsedSidebarMovesTheColumn(t *testing.T) {
	css, _ := loadThemesBoth(t)

	want := regexp.MustCompile(`\.sidebar\.collapsed[ \t]*~[ \t]*main[ \t]*\{[^}]*margin-left[ \t]*:[ \t]*52px`)
	if !want.MatchString(css) {
		t.Error("B411 FAIL: missing `.sidebar.collapsed ~ main{margin-left:52px}`. Collapsing the sidebar would leave the content column at 220px while the sidebar is 52px wide.")
	}
}

// TestB411_BreadcrumbHasNoOffsetOfItsOwn — contract C (the B120 intent, now
// enforced the other way round: a second offset is a bug).
func TestB411_BreadcrumbHasNoOffsetOfItsOwn(t *testing.T) {
	css, _ := loadThemesBoth(t)

	desk := regexp.MustCompile(`(?m)^[ \t]*main[ \t]+\.admin-breadcrumb[ \t]*\{[^}]*\}`)
	body := ruleBody(t, css, desk)
	if body == "" {
		t.Fatal("B411 FAIL: missing the desktop `main .admin-breadcrumb{...}` rule")
	}
	if strings.Contains(body, "margin-left:220px") || strings.Contains(body, "52px") {
		t.Errorf("B411 FAIL: the breadcrumb carries its own sidebar offset (%q) — <main> already provides it, so this doubles the shift and pushes the breadcrumb into the middle of the page.", body)
	}
	if !strings.Contains(body, "margin-left:0") {
		t.Error("B411 FAIL: the desktop breadcrumb rule should state `margin-left:0` explicitly, so the inherited <main> offset is the only one in play.")
	}
	if !strings.Contains(body, "width:100%") {
		t.Error("B411 FAIL: the breadcrumb should span its row (`width:100%`) now that it is offset by <main> rather than by a calc() of its own.")
	}

	// Mobile: the reservation is reset on <main>, not on .shell.
	mediaStart := strings.Index(css, "@media (max-width:768px)")
	if mediaStart == -1 {
		t.Fatal("B411 FAIL: themes.css missing the @media (max-width:768px) block (the mobile breakpoint from B97)")
	}
	mediaRest := css[mediaStart:]
	mobileReset := regexp.MustCompile(`(?m)^[ \t]*main[ \t]*\{[^}]*margin-left[ \t]*:[ \t]*0[ \t]*!important`)
	if !mobileReset.MatchString(mediaRest) {
		t.Error("B411 FAIL: the mobile block does not reset <main> to margin-left:0!important — on a phone the whole page would stay shifted 220px right.")
	}
}

// TestB411_BreadcrumbIsStillASiblingOfShell — contract D (unchanged from B120,
// re-pinned here because the inheritance argument depends on it).
func TestB411_BreadcrumbIsStillASiblingOfShell(t *testing.T) {
	data, err := templatesFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	layout := string(data)
	bcIdx := strings.Index(layout, `class="admin-breadcrumb"`)
	if bcIdx == -1 {
		t.Fatal("B411 FAIL: layout.html missing .admin-breadcrumb")
	}
	shellIdx := strings.Index(layout, `<div class="shell">`)
	if shellIdx == -1 {
		t.Fatal("B411 FAIL: layout.html missing <div class=\"shell\">")
	}
	if bcIdx > shellIdx {
		t.Errorf("B411 FAIL: .admin-breadcrumb (idx %d) is AFTER <div class=\"shell\"> (idx %d); the two must stay siblings inside <main> so both inherit its offset.", bcIdx, shellIdx)
	}
}

// TestB411_WideScreensGetMoreRoom — contract E.
func TestB411_WideScreensGetMoreRoom(t *testing.T) {
	css, _ := loadThemesBoth(t)

	base := regexp.MustCompile(`(?m)^\.shell\{[^}]*max-width:1180px`)
	if !base.MatchString(css) {
		t.Fatal("B411 FAIL: the base `.shell{...max-width:1180px...}` reading width is gone — this block only widens it on large displays, it does not replace it")
	}
	wide := regexp.MustCompile(`@media \(min-width:1600px\)\{main \.shell\{max-width:1[4-9][0-9][0-9]px\}\}`)
	if !wide.MatchString(css) {
		t.Error("B411 FAIL: missing the `@media (min-width:1600px){main .shell{max-width:…}}` step — without it a 3440px display still gets a 1180px column and the right-hand band survives the centring fix.")
	}
}
