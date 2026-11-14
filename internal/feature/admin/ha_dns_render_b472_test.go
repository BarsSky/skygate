// ha_dns_render_b472_test.go — B472 (2026-11-14) — the External-DNS card RENDERS, and the
// certificate field is no longer a gate.
//
// The defect this file exists for cannot be seen by a grep of the handler: the browser refused to
// submit the form because the `<textarea name="cert_pem">` carried `required`, while the server
// wanted that field EMPTY for reg.ru. So the assertion that matters is on the RENDERED MARKUP:
// the certificate textarea must have no `required`, must live inside a collapsed `<details>`, and
// the hint that says «leave it empty» must be there. The read-zone form and the i18n'd success
// banner are asserted alongside, because both were part of the same unusable card.
package admin

import (
	"bytes"
	"html/template"
	"os"
	"regexp"
	"strings"
	"testing"

	"skygate/internal/ha"
)

// b472RenderHA executes the real template from disk with a minimal page context. The page's own
// funcmap is `t` (the only helper the template calls), stubbed with "return the key" so the
// assertions are about STRUCTURE, not about a translation that may be edited tomorrow.
func b472RenderHA(t *testing.T, data *haPageData) string {
	t.Helper()
	// The template dereferences .Data.Chain on the first card; an empty chain is the normal
	// first-run state (the page must not 500 on it), so the fixture carries one.
	if data.Chain == nil {
		data.Chain = &ha.HaChain{}
	}
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

// TestB472_CertificateIsOptional — the gate the operator hit: no `required` on cert_pem, and the
// field is folded away with the explanation of when it IS needed.
func TestB472_CertificateIsOptional(t *testing.T) {
	got := b472RenderHA(t, &haPageData{DNSConfigured: false})
	if !strings.Contains(got, `name="cert_pem"`) {
		t.Fatal("the certificate field disappeared entirely — mTLS providers still need it")
	}
	// The textarea tag itself must not carry `required`. Match the whole tag so a `required` on a
	// sibling input cannot satisfy or defeat the assertion.
	tag := regexp.MustCompile(`(?s)<textarea[^>]*name="cert_pem"[^>]*>`).FindString(got)
	if tag == "" {
		t.Fatalf("could not isolate the cert_pem textarea tag in:\n%s", got)
	}
	if strings.Contains(tag, "required") {
		t.Errorf("cert_pem is still `required`: the browser refuses to submit the empty value reg.ru wants\ntag: %s", tag)
	}
	if !strings.Contains(got, "<details") || !strings.Contains(got, "ha.dns_cert_optional") {
		t.Error("the certificate field is not folded into an optional <details> block")
	}
	if !strings.Contains(got, "ha.dns_cert_hint") {
		t.Error("no hint telling the operator to leave the certificate empty for reg.ru")
	}
	// The other three fields ARE required — the relaxation must not have loosened those.
	for _, name := range []string{"zone", "login", "password"} {
		tag := regexp.MustCompile(`(?s)<input[^>]*name="` + name + `"[^>]*>`).FindString(got)
		if tag == "" || !strings.Contains(tag, "required") {
			t.Errorf("field %q lost its `required` — a save without it would store half a credential", name)
		}
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
