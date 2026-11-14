// ha_dns_form_b473_test.go — B473: the panel's DNS card is SCHEMA-DRIVEN, and the two rules that make
// that safe are pinned here instead of being re-derived by every reader of the template:
//
//  1. A MASKED SECRET IS NEVER PREFILLED and a field that already HAS a value is not `required` in the
//     browser. Together they are what makes "edit the zone without retyping the password" possible;
//     separately either one locks the operator out of their own registrar (the browser refuses the
//     empty value, or the empty submission wipes the stored secret).
//  2. UNKNOWN FIELDS ARE DROPPED, so a hand-made request cannot smuggle an arbitrary key into the
//     encrypted JSON — the row's shape is the SPEC's shape.
//
// The third thing asserted here is the registry's own wiring: the badges the page shows are the
// provider's real capabilities, because a chip that says "TXT supported" when the adapter answers
// ErrUnsupported is worse than no chip at all.
package admin

import (
	"net/url"
	"testing"

	"skygate/internal/dns"
	extcreds "skygate/internal/ha/dnsexternal"
	"skygate/internal/i18n"
)

// b473Catalog installs the catalogue (the label/help resolution goes through i18n.T) and returns "ru".
func b473Catalog(t *testing.T) string {
	t.Helper()
	if i18n.GlobalCatalog == nil {
		i18n.GlobalCatalog = i18n.New()
	}
	return "ru"
}

// b473Regru returns the shipped spec — the one the tests reason about, whatever else the registry holds.
func b473Regru(t *testing.T) dns.Spec {
	t.Helper()
	spec, ok := dns.LookupSpec("regru")
	if !ok {
		t.Fatal("regru is not in the provider registry")
	}
	return spec
}

// TestB473_StoredSecretIsNotPrefilled — the row's password reaches neither the HTML nor the required
// flag, and the non-secret fields DO come back so the operator does not retype them.
func TestB473_StoredSecretIsNotPrefilled(t *testing.T) {
	lang := b473Catalog(t)
	spec := b473Regru(t)
	row := extcreds.ProviderRow{
		Provider: "regru", Zone: "example.com", Enabled: true, Active: true,
		Fields: map[string]string{"login": "user@example.com", "password": "s3cret", "cert_pem": "-----BEGIN-----"},
	}
	fields := dnsFieldsForSpec(lang, spec, row)
	byName := map[string]dnsCardField{}
	for _, f := range fields {
		byName[f.Name] = f
	}
	for _, secret := range []string{"password", "cert_pem"} {
		f := byName[secret]
		if f.Value != "" {
			t.Errorf("%q was prefilled with a stored secret (%q) — the page can then re-render a token it was never given", secret, f.Value)
		}
		if !f.Stored {
			t.Errorf("%q does not say a value is stored: the operator sees an empty field and thinks it was lost", secret)
		}
		if f.Required {
			t.Errorf("%q is marked `required` although a value is stored — the browser would block the very submission that KEEPS it", secret)
		}
		if f.Kind != "password" && f.Kind != "textarea" {
			t.Errorf("%q would be rendered as %q; a secret must stay masked", secret, f.Kind)
		}
	}
	if got := byName["login"].Value; got != "user@example.com" {
		t.Errorf("the login was not prefilled (got %q) — it is not a secret", got)
	}
	if byName["login"].Required {
		t.Error("a stored login is still `required` for the browser")
	}
	if got := byName["cert_pem"].Kind; got != "textarea" {
		t.Errorf("the certificate renders as %q; a pasted PEM block needs a textarea", got)
	}
}

// TestB473_EmptyRowKeepsRequiredFields — on a fresh install nothing is stored, so the required fields
// must be enforced by the browser (and the labels must be words, not catalogue keys).
func TestB473_EmptyRowKeepsRequiredFields(t *testing.T) {
	lang := b473Catalog(t)
	fields := dnsFieldsForSpec(lang, b473Regru(t), extcreds.ProviderRow{})
	byName := map[string]dnsCardField{}
	for _, f := range fields {
		byName[f.Name] = f
		if f.Stored {
			t.Errorf("field %q claims a stored value on an empty row", f.Name)
		}
		if f.Label == "" || f.Label == f.Name && f.Name != "login" {
			t.Errorf("field %q rendered the label %q — a key leaked to the page", f.Name, f.Label)
		}
	}
	if !byName["login"].Required || !byName["password"].Required {
		t.Error("login/password are not required on an empty row — a save would store half a credential")
	}
	if byName["cert_pem"].Required {
		t.Error("the certificate became required again: the browser would refuse the empty value reg.ru wants")
	}
}

// TestB473_UnknownSubmissionFieldsAreDropped — the SPEC decides what can be stored.
func TestB473_UnknownSubmissionFieldsAreDropped(t *testing.T) {
	spec := b473Regru(t)
	form := url.Values{
		"f_login":       {"user@example.com"},
		"f_password":    {"pw"},
		"f_smuggled":    {"evil"},
		"f_region":      {"eu"},
		"zone":          {"example.com"}, // the zone is NOT a credential field: it is its own column
		"f_cert_pem":    {"   "},         // whitespace only = "not supplied"
		"f_placeholder": {"x"},
	}
	got := dnsFormFieldsFromRequest(spec, form)
	for _, banned := range []string{"smuggled", "region", "zone", "placeholder"} {
		if _, ok := got[banned]; ok {
			t.Errorf("field %q reached the field map although the spec does not declare it", banned)
		}
	}
	if got["login"] != "user@example.com" || got["password"] != "pw" {
		t.Errorf("declared fields were dropped: %v", got)
	}
	if _, ok := got["cert_pem"]; ok {
		t.Error("a whitespace-only value was stored as a certificate")
	}
}

// TestB473_EmptySubmissionKeepsStoredFields — rule 1 at the merge level: the value that decides is the
// STORED one, and only a supplied value replaces it.
func TestB473_EmptySubmissionKeepsStoredFields(t *testing.T) {
	spec := b473Regru(t)
	stored := map[string]string{"login": "user@example.com", "password": "old", "cert_pem": "pem"}
	merged := mergeDNSFields(spec, stored, map[string]string{"password": ""})
	if merged["password"] != "old" {
		t.Errorf("an empty password field wiped the stored one (got %q) — that locks the operator out of the registrar", merged["password"])
	}
	if merged["login"] != "user@example.com" || merged["cert_pem"] != "pem" {
		t.Errorf("fields the submission did not mention were lost: %v", merged)
	}
	merged = mergeDNSFields(spec, stored, map[string]string{"password": "new"})
	if merged["password"] != "new" {
		t.Errorf("a supplied password was not taken (got %q)", merged["password"])
	}
	if extra := mergeDNSFields(spec, stored, map[string]string{"unknown": "x"}); len(extra) != len(stored) {
		t.Errorf("an undeclared field was merged into the row: %v", extra)
	}
}

// TestB473_MissingRequiredNamesTheField — the flash says WHICH field is empty, in the operator's
// language, instead of a generic validation error.
func TestB473_MissingRequiredNamesTheField(t *testing.T) {
	lang := b473Catalog(t)
	spec := b473Regru(t)
	missing := dnsMissingRequired(lang, spec, map[string]string{"login": "user@example.com"})
	if len(missing) != 1 {
		t.Fatalf("expected exactly one missing field, got %v", missing)
	}
	if missing[0] == "password" || missing[0] == "" {
		t.Errorf("the missing field is reported as %q — it must be the human label", missing[0])
	}
	if len(dnsMissingRequired(lang, spec, map[string]string{"login": "a", "password": "b"})) != 0 {
		t.Error("a complete submission was reported as incomplete")
	}
}

// TestB473_CapabilityBadgesFollowTheRegistry — the chips are the registry's capabilities, and reg.ru's
// are A + TTL only.
func TestB473_CapabilityBadgesFollowTheRegistry(t *testing.T) {
	lang := b473Catalog(t)
	badges := dnsCapBadges(lang, b473Regru(t).Caps)
	byKey := map[string]dnsCapBadge{}
	for _, b := range badges {
		byKey[b.Key] = b
		if b.Label == "" || b.Label == b.Key {
			t.Errorf("badge %q has no translated label", b.Key)
		}
		if b.Title == "" || b.Title == "ha.dns_cap_yes" && b.OK == false {
			t.Errorf("badge %q has no translated title", b.Key)
		}
	}
	for _, key := range []string{"A", "TTL"} {
		if !byKey[key].OK {
			t.Errorf("badge %q is shown as unsupported although the provider does it", key)
		}
	}
	for _, key := range []string{"AAAA", "TXT"} {
		if byKey[key].OK {
			t.Errorf("badge %q claims support the adapter does not have (CreateTXT answers ErrUnsupported)", key)
		}
	}
}

// TestB473_ViewGroupsCoverEverySpec — the select and the field groups are generated from ONE list, so a
// provider added to the registry appears on the page without a template edit. The active provider is
// the group that is open, and ?provider=<id> overrides it (the no-JS path).
func TestB473_ViewGroupsCoverEverySpec(t *testing.T) {
	lang := b473Catalog(t)
	svc := &Service{} // no credential store: the first-run shape the page must still render
	v := svc.buildDNSCardView(lang, "")
	if len(v.Groups) != len(dns.Specs()) || len(v.Specs) == 0 {
		t.Fatalf("the view rendered %d groups for %d specs", len(v.Groups), len(dns.Specs()))
	}
	open := 0
	for _, g := range v.Groups {
		if g.IsEditing {
			open++
			if g.Name != v.Editing {
				t.Errorf("group %q is open while the view says %q", g.Name, v.Editing)
			}
		}
		if len(g.Fields) == 0 {
			t.Errorf("group %q has no fields — the form would ask nothing", g.Name)
		}
		if len(g.Caps) != 4 {
			t.Errorf("group %q renders %d capability badges, want A/AAAA/TXT/TTL", g.Name, len(g.Caps))
		}
	}
	if open != 1 {
		t.Errorf("%d groups are open at once — exactly one must be editable", open)
	}
	if over := svc.buildDNSCardView(lang, "regru"); over.Editing != "regru" {
		t.Errorf("?provider=regru did not open the reg.ru group (editing=%q)", over.Editing)
	}
	if junk := svc.buildDNSCardView(lang, "no-such-provider"); junk.Editing != v.Editing {
		t.Errorf("an unknown ?provider= changed the open group (editing=%q)", junk.Editing)
	}
}

// TestB473_CardIsSafeWithoutAStore — an install with no credential store still gets a renderable card
// (that is how the operator configures the first provider), and the view builder must not panic on the
// nil store.
func TestB473_CardIsSafeWithoutAStore(t *testing.T) {
	v := (&Service{}).buildDNSCardView(b473Catalog(t), "")
	if v.HasRows || v.HasActive {
		t.Error("the view claims stored rows although the store is nil")
	}
	if v.Zone != "" {
		t.Errorf("zone prefilled to %q without a store", v.Zone)
	}
}

// TestB473_SaveBuildsTheProviderFromTheActiveRow — the panel's Test / Read-the-zone buttons and the save
// path must agree on ONE object: the provider built from the ACTIVE row. Asserted through the shared
// seam (BuildProvider + row), because the round trip itself needs a database and is covered by
// internal/ha/dnsexternal's row tests.
func TestB473_SaveBuildsTheProviderFromTheActiveRow(t *testing.T) {
	row := extcreds.ProviderRow{
		Provider: "regru", Zone: "example.com", Enabled: true, Active: true,
		Fields: map[string]string{"login": "user@example.com", "password": "pw"},
	}
	p, err := dns.BuildProvider(row.Provider, dns.BuildDeps{Row: &row})
	if err != nil || p == nil {
		t.Fatalf("BuildProvider(%q, row) = %v, %v", row.Provider, p, err)
	}
	if p.Name() == "" {
		t.Error("the provider built from the active row has no name")
	}
	// The historical alias must build the SAME provider: a row saved pre-B473 says "external".
	alias := row
	alias.Provider = "external"
	if _, err := dns.BuildProvider(alias.Provider, dns.BuildDeps{Row: &alias}); err != nil {
		t.Errorf("a row stored under the alias `external` no longer builds: %v", err)
	}
}
