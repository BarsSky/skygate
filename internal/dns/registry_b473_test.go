// registry_b473_test.go — B473: the provider registry is a contract, not a comment.
//
// THE OPERATOR'S QUESTION was «как можно адаптировать под разных провайдеров?». The answer this block
// implements is "one registry that the panel, the adapter and the runbook all read". That only works if
// the registry is COMPLETE: a spec whose field has no catalogue entry renders an English page with a
// Russian-only label (or worse, the raw key), and a spec that overstates its capabilities makes the
// panel offer an operation the adapter answers ErrUnsupported for. Both are asserted here instead of
// being discovered by an operator mid-switch.
package dns

import (
	"context"
	"testing"

	extcreds "skygate/internal/ha/dnsexternal"
	"skygate/internal/i18n"
)

// extRow builds a credential row the way the store hands one to BuildProvider.
func extRow(provider, zone string, fields map[string]string) extcreds.ProviderRow {
	return extcreds.ProviderRow{ID: 1, Provider: provider, Zone: zone, Fields: fields, Enabled: true, Active: true}
}

// TestB473_EveryFieldLabelAndHelpIsTranslated — RU and EN both define every key a spec names.
//
// This is the registry's half of AGENTS.md rule 10. `TestCatalogsParity` only compares KEY SETS, so it
// cannot see a key that no catalogue mentions; a label compiled into the Go struct is invisible to both
// tests, which is why the registry stores keys and this test reads them.
func TestB473_EveryFieldLabelAndHelpIsTranslated(t *testing.T) {
	ru := i18n.New()
	for _, lang := range []string{"ru", "en"} {
		for _, spec := range Specs() {
			if spec.Name == "" || spec.Label == "" {
				t.Errorf("spec %+v has no name or label", spec)
			}
			if len(spec.Fields) == 0 {
				t.Errorf("provider %q declares no fields — the panel would render a form that asks nothing", spec.Name)
			}
			for _, f := range spec.Fields {
				if f.Name == "" {
					t.Errorf("provider %q has a field with no name", spec.Name)
				}
				for _, key := range []string{f.LabelKey, f.HelpKey} {
					if key == "" {
						t.Errorf("provider %q field %q has no i18n key", spec.Name, f.Name)
						continue
					}
					if got := ru.T(lang, key); got == key || got == "" {
						t.Errorf("provider %q field %q: key %q is missing from the %s catalogue", spec.Name, f.Name, key, lang)
					}
				}
			}
		}
	}
}

// TestB473_LookupSpecAcceptsTheHistoricalAlias — `external` and `regru` are the same provider. Two rows
// for one registrar is the failure the alias exists to prevent, so the lookup must fold them.
func TestB473_LookupSpecAcceptsTheHistoricalAlias(t *testing.T) {
	a, ok := LookupSpec("external")
	if !ok {
		t.Fatal("the historical alias `external` no longer resolves")
	}
	b, ok := LookupSpec(" regru ")
	if !ok {
		t.Fatal("`regru` (with whitespace) does not resolve")
	}
	if a.Name != b.Name || a.Name != "regru" {
		t.Errorf("alias resolved to %q, honest name to %q — they must be the same spec", a.Name, b.Name)
	}
	if _, ok := LookupSpec("no-such-provider"); ok {
		t.Error("an unknown provider resolved to a spec — the save path would accept it")
	}
	// B474 shipped Cloudflare, which is what the seam was for: a provider that is NOT reg.ru resolves
	// from the same registry and therefore renders on the same page.
	if spec, ok := LookupSpec("cloudflare"); !ok {
		t.Error("cloudflare is missing from the registry — B474's adapter would be unreachable from the panel")
	} else if spec.Name != "cloudflare" || len(spec.Fields) == 0 {
		t.Errorf("the cloudflare spec is incomplete: %+v", spec)
	}
}

// TestB473_CapabilitiesAreTheAdaptersTruth — what the spec advertises is what BuildProvider's adapter
// answers. reg.ru does A + TTL and does NOT do AAAA/TXT in this client; claiming otherwise would make
// the panel offer a button whose handler returns ErrUnsupported.
func TestB473_CapabilitiesAreTheAdaptersTruth(t *testing.T) {
	spec, ok := LookupSpec("regru")
	if !ok {
		t.Fatal("regru is not in the registry")
	}
	row := extRow("regru", "example.com", map[string]string{"login": "user@example.com", "password": "x"})
	p, err := BuildProvider("regru", BuildDeps{Row: &row})
	if err != nil || p == nil {
		t.Fatalf("BuildProvider(regru, row) = %v, %v", p, err)
	}
	got := p.Capabilities()
	if got != spec.Caps {
		t.Errorf("adapter capabilities %+v disagree with the registry %+v — the panel would lie", got, spec.Caps)
	}
	if !got.A || !got.TTL {
		t.Errorf("reg.ru supports A records and TTL; capabilities say %+v", got)
	}
	if got.AAAA || got.TXT {
		t.Errorf("neither AAAA nor TXT is implemented by the reg.ru client, capabilities say %+v", got)
	}
	if err := p.CreateTXT(context.Background(), "example.com", "_acme-challenge", "v", 60); err == nil {
		t.Error("CreateTXT answered success although reg.ru cannot do it — a DNS-01 attempt would believe it")
	}
}

// TestB473_RowSelectsTheCredentials — a row is what makes a second provider possible, so building from
// one must not need the legacy settings keys (the DB is deliberately nil here: anything that tried to
// read global_settings would panic instead of silently falling back).
func TestB473_RowSelectsTheCredentials(t *testing.T) {
	row := extRow("regru", "example.com", map[string]string{"login": "other@example.com", "password": "y"})
	p, err := BuildProvider("regru", BuildDeps{Row: &row})
	if err != nil || p == nil {
		t.Fatalf("BuildProvider from a row = %v, %v", p, err)
	}
	if p.Name() == "" {
		t.Error("the provider built from a row has no name")
	}
	// No row and no DB: the pre-B473 path needs the legacy store, and saying so is better than a nil
	// pointer deep inside a scheduler tick.
	if _, err := BuildProvider("regru", BuildDeps{}); err == nil {
		t.Error("building reg.ru with neither a row nor a DB must fail loudly")
	}
}
