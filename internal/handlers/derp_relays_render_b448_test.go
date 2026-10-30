// derp_relays_render_b448_test.go — B448 follow-up (v1.5.200).
//
// THE PAGE WAS DEAD ON THE LIVE INSTALL, AND NOTHING LOCAL EXECUTED THAT TEMPLATE:
//
//	render: template: layout.html:364:2: executing "layout" at : error calling renderBody:
//	template: derp_relays.html:187:17: executing "body-admin-derp_relays" at <.Deletable>:
//	can't evaluate field Deletable in type db.DerpRelay
//
// The delete decision was added to RelayMapStatus, but the actions cell lives in
// `{{range .Relays}}`, which iterates `[]db.DerpRelay`. `/admin/derp/relays` therefore rendered a
// raw template error for every operator on v1.5.199 — a field the page reads and the handler does
// not pass, which is EXACTLY the class of defect an executing render test exists to catch (the
// sibling exit-rules test caught nothing here because it renders a different page).
//
// This file executes the real `templates/admin/derp_relays.html` with the real view models, for
// both tables, so:
//   - a field the page reads must exist on the model the handler passes (the crash above), and
//   - the delete rule must actually be applied to the rendered buttons.
package handlers

import (
	"bytes"
	"encoding/json"
	"html/template"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/derphealth"
	"skygate/internal/feature/admin"
)

// loadDerpRelaysBody parses the relays page with the same minimal funcmap the other render tests
// use, so an unregistered helper fails here instead of on the page.
func loadDerpRelaysBody(t *testing.T) *template.Template {
	t.Helper()
	data, err := templatesFS.ReadFile("templates/admin/derp_relays.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	tpl, err := template.New("test").Funcs(template.FuncMap{
		"t":        func(key string) string { return key },
		"tf":       func(key string, args ...any) string { return key },
		"safeJS":   func(s string) template.JS { return template.JS(s) },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"safeJSON": func(s string) template.JS {
			b, _ := json.Marshal(s)
			return template.JS(b)
		},
	}).Parse(string(data))
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	return tpl
}

// derpRelaysRenderData is the minimum context `body-admin-derp_relays` dereferences.
func derpRelaysRenderData(rows []db.DerpRelay, statuses []admin.RelayMapStatus) map[string]any {
	return map[string]any{
		"Relays":         admin.DerpRelayRows(rows),
		"MapStatus":      statuses,
		"FlashSuccess":   "",
		"FlashError":     "",
		"LastTestResult": nil,
		"ProbeHost": struct {
			Host   string
			Source string
		}{},
		"ProbeHostEnv":   "",
		"ProbeHostSaved": false,
		"ProbeHostErr":   "",
	}
}

func renderDerpRelays(t *testing.T, rows []db.DerpRelay) string {
	t.Helper()
	tpl := loadDerpRelaysBody(t)
	statuses := []admin.RelayMapStatus{{
		RegionID: 900, Host: "relay.example.com", URL: "https://relay.example.com:443",
		Bundled: true, Enabled: true, Published: true, Via: "192.0.2.10:443",
		Kind: derphealth.RelayKindNode, Endpoint: "relay.example.com:443", Probe: "OK",
	}}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-derp_relays",
		derpRelaysRenderData(rows, statuses)); err != nil {
		t.Fatalf("render body-admin-derp_relays: %v", err)
	}
	return buf.String()
}

// TestDerpRelaysPageRendersTheRelayRows_B448 is the crash itself: the page must render the stored
// rows — including the delete decision — instead of answering a template error.
func TestDerpRelaysPageRendersTheRelayRows_B448(t *testing.T) {
	rows := []db.DerpRelay{
		{ID: 2, Hostname: "relay.example.com", URL: "https://relay.example.com:443", RegionID: 900, IsBundled: true, Enabled: true},
		{ID: 3, Hostname: "relay.example.com", URL: "https://relay.example.com:8443", RegionID: 900, IsBundled: true, Enabled: false},
	}
	got := renderDerpRelays(t, rows)

	for _, want := range []string{
		"https://relay.example.com:443",
		"https://relay.example.com:8443",
		`action="/admin/derp/relays/delete"`, // at least one row offers the delete
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page is missing %q — the relays table rendered nothing useful", want)
		}
	}
}

// TestDerpRelaysPageDeleteFollowsTheRule_B448 checks the RULE on the rendered buttons: two bundled
// rows → both deletable; one bundled row → that one is locked (it is the container switch) and the
// TestDerpRelaysPageRejectsTheRawRowModel_B448 makes the detector prove itself: feeding the page
// `[]db.DerpRelay` — the pre-fix model, exactly what the handler passed on v1.5.199 — must FAIL
// with the field error the live install showed. Without this, the render test above could quietly
// pass on a template that never actually reads `.Deletable`.
func TestDerpRelaysPageRejectsTheRawRowModel_B448(t *testing.T) {
	tpl := loadDerpRelaysBody(t)
	data := derpRelaysRenderData(nil, nil)
	// The raw model, verbatim.
	data["Relays"] = []db.DerpRelay{{ID: 2, URL: "https://relay.example.com:443", IsBundled: true}}

	var buf bytes.Buffer
	err := tpl.ExecuteTemplate(&buf, "body-admin-derp_relays", data)
	if err == nil {
		t.Fatal("the page rendered []db.DerpRelay without an error — the delete rule is not read from the row model at all")
	}
	if !strings.Contains(err.Error(), "Deletable") {
		t.Fatalf("the page failed for a different reason than the reported crash: %v", err)
	}
}

// ordinary row keeps its button.
func TestDerpRelaysPageDeleteFollowsTheRule_B448(t *testing.T) {
	// Two bundled rows: the duplicate pair from the live install.
	got := renderDerpRelays(t, []db.DerpRelay{
		{ID: 2, URL: "https://relay.example.com:443", IsBundled: true, Enabled: true},
		{ID: 3, URL: "https://relay.example.com:8443", IsBundled: true, Enabled: false},
	})
	if n := strings.Count(got, `action="/admin/derp/relays/delete"`); n != 2 {
		t.Errorf("two bundled rows: %d delete forms rendered, want 2 (the disabled duplicate must be removable)", n)
	}
	if strings.Contains(got, "derp.relays_last_bundled_no_delete") {
		t.Error("two bundled rows: a row claims to be the last bundled one")
	}

	// One bundled row plus one ordinary row: the bundled one is the LAST one and must stay.
	got = renderDerpRelays(t, []db.DerpRelay{
		{ID: 2, URL: "https://relay.example.com:443", IsBundled: true, Enabled: true},
		{ID: 9, URL: "https://external.example.com", IsBundled: false, Enabled: true},
	})
	if n := strings.Count(got, `action="/admin/derp/relays/delete"`); n != 1 {
		t.Errorf("one bundled + one ordinary row: %d delete forms rendered, want 1 (only the ordinary row)", n)
	}
	if !strings.Contains(got, "derp.relays_last_bundled_no_delete") {
		t.Error("the last bundled row is not marked as undeletable — the operator would get a refused delete")
	}
}
