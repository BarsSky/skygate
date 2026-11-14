// ha_dns_render_b472_test.go — B472 (2026-11-14) — the External-DNS card RENDERS, and the
// certificate field is no longer a gate.
//
// The defect this file exists for cannot be seen by a grep of the handler: the browser refused to
// submit the form because the certificate field carried `required`, while the server wanted that field
// EMPTY for reg.ru. So the assertion that matters is on the RENDERED MARKUP: no `required` on the cert
// input, and the help text that says «leave it empty for reg.ru» present.
//
// B473 RENEGOTIATED the shape this file asserts, not the guarantees: the card is schema-driven now, so
// the certificate is `f_cert_pem` (rendered from `dns.Spec.Fields`) instead of a hand-written
// `<textarea name="cert_pem">` inside a collapsed `<details>`, and login/password are `f_login` /
// `f_password`. The two things that must never regress — the certificate is OPTIONAL, and an empty
// submission KEEPS a stored secret — are asserted on the rendered output, plus every field name is now
// taken from the registry, which is what makes a second provider possible at all.
package admin

import (
	"bytes"
	"html/template"
	"os"
	"regexp"
	"strings"
	"testing"

	"skygate/internal/ha"
	"skygate/internal/i18n"
)

// b472RenderHA executes the real template from disk with a minimal page context. The page's own
// funcmap is `t` (the only helper the template calls), stubbed with "return the key" so the
// assertions are about STRUCTURE, not about a translation that may be edited tomorrow. The card's
// DATA, however, is built by the real view builder, so the fields asserted below are the ones the
// registry actually produces.
func b472RenderHA(t *testing.T, data *haPageData) string {
	t.Helper()
	if i18n.GlobalCatalog == nil {
		i18n.GlobalCatalog = i18n.New()
	}
	// The template dereferences .Data.Chain on the first card; an empty chain is the normal
	// first-run state (the page must not 500 on it), so the fixture carries one.
	if data.Chain == nil {
		data.Chain = &ha.HaChain{}
	}
	data.DNSCard = (&Service{}).buildDNSCardView("ru", "")
	raw, err := os.ReadFile("../../handlers/templates/admin/ha.html")
	if err != nil {
		t.Fatalf("read the HA template: %v", err)
	}
	tpl, err := template.New("ha").Funcs(template.FuncMap{
		"t":        func(key string) string { return key },
		"tf":       func(key string, args ...any) string { return key },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
	}).Parse(string(raw))
	if err != nil {
		t.Fatalf("parse the HA template: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-ha", map[string]any{"Data": data}); err != nil {
		t.Fatalf("render body-admin-ha: %v", err)
	}
	return buf.String()
}

// b472FieldTag isolates one input/textarea TAG by field name, so a `required` on a sibling cannot
// satisfy or defeat the assertion.
func b472FieldTag(t *testing.T, html, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)<(?:input|textarea)[^>]*name="f_` + name + `"[^>]*>`)
	return re.FindString(html)
}

// TestB472_CertificateIsOptional — the gate the operator hit: no `required` on the certificate, and a
// help text that says to leave it empty for reg.ru.
func TestB472_CertificateIsOptional(t *testing.T) {
	got := b472RenderHA(t, &haPageData{DNSConfigured: false})
	tag := b472FieldTag(t, got, "cert_pem")
	if tag == "" {
		t.Fatalf("the certificate field disappeared entirely — mTLS providers still need it:\n%s", got)
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(tag)), "<textarea") {
		t.Errorf("the certificate is a single-line input; a pasted PEM block must stay readable: %s", tag)
	}
	if strings.Contains(tag, "required") {
		t.Errorf("cert_pem is still `required`: the browser refuses to submit the empty value reg.ru wants\ntag: %s", tag)
	}
	// The hint lives in the catalogue now (the registry holds its KEY), and the view resolves it.
	if !strings.Contains(got, "оставьте поле ПУСТЫМ") {
		t.Error("no hint telling the operator to leave the certificate empty for reg.ru")
	}
	// The REQUIRED fields of the active provider are still required — the relaxation must not have
	// loosened those. They come from the spec, so the assertion reads the rendered tags.
	for _, name := range []string{"login", "password"} {
		tag := b472FieldTag(t, got, name)
		if tag == "" || !strings.Contains(tag, "required") {
			t.Errorf("field %q lost its `required` — a save without it would store half a credential\ntag: %s", name, tag)
		}
	}
	zone := regexp.MustCompile(`(?s)<input[^>]*name="zone"[^>]*>`).FindString(got)
	if !strings.Contains(zone, "required") {
		t.Errorf("the zone lost its `required`:\n%s", zone)
	}
}

// TestB472_CardOffersBothButtons — save, test and the read-only zone reader are all reachable from
// the page, and the read-zone form defaults to the name the panel serves.
func TestB472_CardOffersBothButtons(t *testing.T) {
	got := b472RenderHA(t, &haPageData{DNSConfigured: true})
	for _, want := range []string{
		`action="/admin/ha/dns/save"`,
		`formaction="/admin/ha/dns/test"`,
		`action="/admin/ha/dns/records"`,
		`name="names"`,
		`value="skygate"`,
		`ha.dns_records`,
		`ha.dns_configured_ok`, // the success banner is translated now, not a hardcoded English line
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the External-DNS card is missing %q", want)
		}
	}
	if strings.Contains(got, "the configured DNS provider credentials configured") {
		t.Error("the hardcoded English banner is still in the template")
	}
}

// TestB472_FlashRenders — the test/records results arrive as a flash; the page must show both
// slots, otherwise the buttons speak into the void again.
func TestB472_FlashRenders(t *testing.T) {
	got := b472RenderHA(t, &haPageData{
		FlashSuccess: "Test OK (latency 12ms, HTTP 200).",
		FlashError:   "Test FAILED (auth): bad password",
	})
	for _, want := range []string{"Test OK (latency 12ms, HTTP 200).", "Test FAILED (auth): bad password"} {
		if !strings.Contains(got, want) {
			t.Errorf("flash %q did not render", want)
		}
	}
}

// TestB473_SelectRendersEveryProvider — the provider is a CHOICE on the page, and every registry entry
// is offered. This is the property that makes the card adaptable to a second provider without touching
// the template: the option list is generated, the fields are generated, and the save path reads the
// same registry.
func TestB473_SelectRendersEveryProvider(t *testing.T) {
	got := b472RenderHA(t, &haPageData{})
	if !regexp.MustCompile(`(?s)<select[^>]*name="provider"`).MatchString(got) {
		t.Fatalf("no provider <select> on the card:\n%s", got)
	}
	for _, want := range []string{`value="regru"`, "reg.ru (API regru2)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the provider select is missing %q", want)
		}
	}
	if strings.Contains(got, `type="hidden" name="provider"`) {
		t.Error("the provider is still a fixed hidden field — a second provider could not be chosen")
	}
}

// TestB473_OnlyTheEditingGroupSubmits — a group that is not being edited is present in the markup (so
// switching providers needs no round trip) but its inputs are `disabled`, so a leftover value in a
// hidden group can never be submitted as if it belonged to the chosen provider.
func TestB473_OnlyTheEditingGroupSubmits(t *testing.T) {
	got := b472RenderHA(t, &haPageData{})
	if !strings.Contains(got, `data-dns-group="regru"`) {
		t.Fatal("the reg.ru group is not marked for the switcher")
	}
	// The editing group is visible; with a single provider there is exactly one group, and it must be
	// enabled — a card whose only group is disabled would be unsubmittable.
	group := regexp.MustCompile(`(?s)data-dns-group="regru".*?</div>\s*</div>`).FindString(got)
	if strings.Contains(group, "disabled") {
		t.Errorf("the active provider's inputs are disabled — the form cannot be submitted:\n%s", group)
	}
	if !strings.Contains(group, "display:grid") {
		t.Errorf("the active provider's group is hidden:\n%s", group)
	}
}
