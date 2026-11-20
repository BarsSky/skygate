package handlers

// devices_exitpin_render_b497_test.go — B497 (2026-11-19).
//
// The operator's screenshot was of THIS page: `/admin/devices`, exit-node column,
// one phone showing `emilia` while its own preference was `karolina` and its ACL
// carried no `via` at all. Two things made that possible and neither was visible
// without rendering the page:
//
//  1. an EMPTY cell rendered a `<select>`, whose first `<option>` is a relay name
//     — so «nothing is pinned» looked exactly like «pinned to emilia»;
//  2. a STORED preference the policy ignores (via off / derived / dead relay)
//     rendered exactly like an applied one.
//
// The unit tests in internal/exitpin cover the RULES; this test covers the PAGE:
// it renders the real body template and asserts that the three states produce
// three different cells, and that no relay name is shown for a free choice.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"testing"

	"skygate/internal/exitpin"
)

// b497Row carries the fields templates/admin/devices.html reads from one node
// row. A template edit that needs another field fails HERE, as
// «can't evaluate field X in type handlers.b497Row», instead of on the page.
type b497Row struct {
	ID             string
	Hostname       string
	UserName       string
	IPAddresses    []string
	IsPublicView   bool
	IsPrivateView  bool
	IsExitNode     bool
	Online         bool
	LastSeen       string
	Tags           []string
	OS             string
	DeviceType     string
	DeadRuleCount  int
	Pin            exitpin.Pin
	PinRelay       string
	NeedsOwnerPick bool
	PortalUsername string
	DevTag         string
}

func b497Render(t *testing.T, rows []b497Row, pins map[string]exitpin.Pin) string {
	t.Helper()
	return b498RenderWith(t, rows, pins, nil)
}

// b498Lost mimics admin.lostUserPref — the page-level statement about a per-user
// preference that reaches none of that user's devices (B498).
type b498Lost struct {
	UserID   int64
	Username string
	Relay    string
	Devices  int
}

func b498RenderWith(t *testing.T, rows []b497Row, pins map[string]exitpin.Pin, lost []b498Lost) string {
	t.Helper()
	raw, err := templatesFS.ReadFile("templates/admin/devices.html")
	if err != nil {
		t.Fatalf("read admin/devices.html: %v", err)
	}
	tpl, err := template.New("test").Funcs(template.FuncMap{
		"t":  func(key string) string { return key },
		"tf": func(key string, args ...any) string { return key + "(" + b498Args(args) + ")" },
		// The production funcmap's name for the formatted call is `tf`; keeping the
		// ARGUMENTS visible lets a test prove the template passes the right fields
		// (a wrong field prints the right key and the wrong sentence).
		"tolower":  strings.ToLower,
		"toupper":  strings.ToUpper,
		"safeJS":   func(s string) template.JS { return template.JS(s) },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"th":       func(key string, args ...any) template.HTML { return template.HTML(key) },
		"safeJSON": func(s string) template.JS {
			b, _ := json.Marshal(s)
			return template.JS(b)
		},
	}).Parse(string(raw))
	if err != nil {
		t.Fatalf("parse admin/devices.html: %v", err)
	}
	data := map[string]any{
		"Nodes":        rows,
		"Users":        []any{},
		"FlashSuccess": "", "FlashError": "", "FlashOkRules": "", "FlashOkRulesCount": 0, "FlashACLErr": "",
		"DevTagMap":              map[string]string{},
		"DeviceExitPrefMap":      map[string]string{},
		"DeviceExitViaMap":       map[string]bool{},
		"DeviceExitOptedOutMap":  map[string]bool{},
		"NodePins":               pins,
		"LostUserPrefs":          lost,
		"AvailableExits":         []any{},
		"UserExitPrefMap":        map[string]string{},
		"SkygateUserByName":      map[string]int64{"michail": 6},
		"SkygateUserByHost":      map[string]int64{"olesya": 6, "basic": 6},
		"TransferTargets":        []any{},
		"FirstRunBanner":         false,
		"FirstRunUnadoptedCount": 0,
		"AdoptionCandidates":     []any{},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-devices", data); err != nil {
		t.Fatalf("execute body-admin-devices: %v", err)
	}
	return buf.String()
}

// b498Args renders the tf arguments so a test can assert they were passed at all.
func b498Args(args []any) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, fmt.Sprint(a))
	}
	return strings.Join(parts, "|")
}

func TestB497_DevicesPageDistinguishesTheThreeStates(t *testing.T) {
	applied := exitpin.Pin{Source: exitpin.SourceDevice, Tag: "tag:dev-infra-karolina", Applies: true}
	// B498: the yellow case is a DEVICE pin the policy ignores (strict mode off).
	// A per-user row is not a per-device state any more — it moved to the page-level
	// notice, because it never was that device's setting.
	withheld := exitpin.Pin{Source: exitpin.SourceDevice, Tag: "tag:dev-infra-karolina", Reason: exitpin.ReasonViaOff}
	free := exitpin.Pin{Source: exitpin.SourceNone, Reason: exitpin.ReasonFreeChoice}

	rows := []b497Row{
		{Hostname: "basic", UserName: "michail", Pin: applied, PinRelay: "karolina"},
		{Hostname: "olesya", UserName: "michail", Pin: withheld, PinRelay: "karolina"},
		{Hostname: "a71", UserName: "michail", Pin: free},
	}
	body := b497Render(t, rows, map[string]exitpin.Pin{
		"basic": applied, "olesya": withheld, "a71": free,
	})

	// 1) An applied pin says which relay AND that it comes from the device.
	if !strings.Contains(body, "karolina") {
		t.Fatalf("an applied pin did not render its relay")
	}
	if !strings.Contains(body, "exitpin.source.device") {
		t.Fatalf("the applied pin did not name its source (exitpin.source.device)")
	}
	// 2) A stored-but-ignored DEVICE preference says WHY it is not applied — the
	//    whole point of B497. (B498: the per-user row no longer renders per device
	//    at all; see TestB498_UserPrefIsStatedOnceOnThePage.)
	if !strings.Contains(body, "exitpin.reason.via_off") {
		t.Fatalf("a device pin that does not apply rendered without its reason")
	}
	// 3) A free choice renders the dash and the explicit «выбирает само».
	if !strings.Contains(body, "exitpin.source.none") {
		t.Fatalf("a free choice did not say «the device chooses for itself»")
	}
	if !strings.Contains(body, "exitpin.reason.free_choice") {
		t.Fatalf("a free choice did not carry its reason tooltip")
	}
}

// TestB497_EmptyCellDoesNotShowARelayName is the regression for the screenshot
// itself: with nothing pinned the page must not print a relay name at all.
func TestB497_EmptyCellDoesNotShowARelayName(t *testing.T) {
	free := exitpin.Pin{Source: exitpin.SourceNone, Reason: exitpin.ReasonFreeChoice}
	body := b497Render(t,
		[]b497Row{{Hostname: "olesya", UserName: "michail", Pin: free}},
		map[string]exitpin.Pin{"olesya": free},
	)
	if strings.Contains(body, "tag:dev-infra-emilia") || strings.Contains(body, ">emilia<") {
		t.Fatalf("an unpinned device rendered a relay name — that is the ambiguity B497 removes")
	}
	// The assign control exists, but only inside a collapsed <details>, so no
	// <option> is displayed as if it had been chosen.
	cell := b497ExitCell(body, "olesya")
	if !strings.Contains(cell, "<details") {
		t.Fatalf("the assign control is not collapsed behind <details>: %s", cell)
	}
	if idx := strings.Index(cell, "<details"); idx < 0 || strings.Index(cell, "<select") < idx {
		t.Fatalf("a <select> is rendered OUTSIDE <details> — its first option would look pinned: %s", cell)
	}
}

// TestB498_UserPrefIsStatedOnceOnThePage — the operator's second complaint, as a
// test. A per-user preference must appear ONCE, in the page-level notice, naming
// the person, the relay and how many of that person's devices it fails to reach —
// and it must NOT appear inside any device cell, where it read as «this phone is
// pinned to karolina».
func TestB498_UserPrefIsStatedOnceOnThePage(t *testing.T) {
	free := exitpin.Pin{Source: exitpin.SourceNone, Reason: exitpin.ReasonFreeChoice}
	rows := []b497Row{
		{Hostname: "olesya", UserName: "michail", Pin: free},
		{Hostname: "nothing-phone-2", UserName: "michail", Pin: free},
	}
	body := b498RenderWith(t, rows, map[string]exitpin.Pin{
		"olesya": free, "nothing-phone-2": free,
	}, []b498Lost{{UserID: 6, Username: "michail", Relay: "karolina", Devices: 3}})

	// The notice exists and carries the three facts the sentence needs.
	if !strings.Contains(body, "devices.lost_user_pref_title") {
		t.Fatalf("the page-level notice is missing")
	}
	if !strings.Contains(body, "michail|karolina|3") {
		t.Fatalf("the notice did not pass (username, relay, device count) into the sentence")
	}
	// …and no device cell repeats the relay name for a free-choice device.
	for _, host := range []string{"olesya", "nothing-phone-2"} {
		cell := b497ExitCell(body, host)
		if strings.Contains(cell, "karolina") {
			t.Fatalf("%s's cell rendered the user-level relay name: %s", host, cell)
		}
	}
}

// b497ExitCell returns the markup of the exit-node cell of one row, so an
// assertion about «this cell» cannot be satisfied by another row's text.
func b497ExitCell(body, hostname string) string {
	// The exit cell follows the dev-tag cell and precedes the status cell; slice
	// from the row's hostname marker to the next <tr> and keep the whole row,
	// which is enough to prove ordering (a pin name from another row cannot leak
	// in: every row is rendered from its own .Pin).
	start := strings.Index(body, hostname)
	if start < 0 {
		return ""
	}
	end := strings.Index(body[start:], "</tr>")
	if end < 0 {
		return body[start:]
	}
	return body[start : start+end]
}
