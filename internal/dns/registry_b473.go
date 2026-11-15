// registry_b473.go — B473: the per-provider facts the panel, the CLI and the runbook share.
//
// WHY THIS FILE EXISTS. Before B473 a provider was a `switch` in BuildProvider plus a template that
// hardcoded reg.ru's four fields. The operator's question — «как адаптировать под разных провайдеров?»
// — cannot be answered by configuration while the fields, the capabilities and the display label live
// in three different places. This registry is the one place:
//
//   - `Fields` is what the panel asks for (and the store encrypts), so a new provider renders without
//     touching a template;
//   - `Capabilities` is what the code is allowed to assume: a provider without TTL cannot be asked to
//     lower it for the switch, and the panel says so instead of failing later;
//   - `Label` is what the operator reads ("reg.ru (API regru2)"), because the identifier `regru` (and
//     the historical alias `external`) means nothing to a human.
//
// Adding a provider is: an adapter implementing dns.Provider, one Spec here, one case in
// BuildProvider. No migration, no template edit, no new settings key — which is the seam B473 and its
// storage (dns_provider_creds, v0.84) were built for.
package dns

import (
	"strings"

	extcreds "skygate/internal/ha/dnsexternal"
)

// FieldKind tells the panel how to render a provider field.
type FieldKind string

const (
	FieldText     FieldKind = "text"
	FieldPassword FieldKind = "password" // rendered masked; an empty value means "keep the stored one"
	FieldEmail    FieldKind = "email"
	// FieldTextarea is a multi-line secret (a PEM block, an RFC 2136 key). Masked inputs are
	// single-line, and a pasted certificate inside one is unreadable — but it is still a secret, so
	// it is never prefilled either.
	FieldTextarea FieldKind = "textarea"
)

// FieldSpec is one input of a provider's credential form.
//
// The prose lives in the i18n catalogue, NOT here: the registry names the KEYS, and the panel resolves
// them per request language. A Russian label compiled into a Go struct is invisible to the EN locale and
// invisible to the RU+EN parity test (AGENTS.md rule 10), so it would ship as the one untranslated
// string on an otherwise translated page.
type FieldSpec struct {
	Name string
	// LabelKey / HelpKey are i18n keys ("ha.dns_field_login", …). The panel falls back to the field
	// NAME when a key is absent from both catalogues, so a new provider renders something usable
	// before its prose is written — and the B473 contract fails until the keys exist in both.
	LabelKey    string
	HelpKey     string
	Placeholder string
	Kind        FieldKind
	Required    bool
}

// Spec describes one provider end to end: how it is named, what it asks for, what it can do.
type Spec struct {
	// Name is the stable identifier used in SKYGATE_DNS_PROVIDER and in dns_provider_creds.provider.
	Name string
	// Label is the human name shown in the panel.
	Label string
	// Zones reports whether the provider needs the zone (the domain) in the form. Today every
	// provider does; a future provider that derives it from an API token could say no.
	NeedsZone bool
	Fields    []FieldSpec
	Caps      Capabilities
}

// Capabilities is what a provider can do. Kept as a struct (not a bitmask) so a caller reads
// `caps.TTL` and the compiler stops it from inventing a capability.
type Capabilities struct {
	A    bool
	AAAA bool
	TXT  bool
	TTL  bool
}

// Record is one DNS answer, provider-neutral.
type Record struct {
	Name    string
	Type    string
	Content string
	TTL     int
}

// regruSpec is the shipped provider (B468). The certificate field is OPTIONAL and stays so: reg.ru
// authenticates with a login and an alternative password, and a stored client certificate makes the
// client attempt mTLS, which that API rejects.
var regruSpec = Spec{
	Name:      extcreds.ProviderRegru,
	Label:     "reg.ru (API regru2)",
	NeedsZone: true,
	Fields: []FieldSpec{
		{
			Name: "login", LabelKey: "ha.dns_field_login", HelpKey: "ha.dns_field_login_help",
			Kind: FieldEmail, Required: true, Placeholder: "user@example.com",
		},
		{
			Name: "password", LabelKey: "ha.dns_field_regru_password",
			HelpKey: "ha.dns_field_regru_password_help",
			Kind:    FieldPassword, Required: true,
		},
		{
			Name: "cert_pem", LabelKey: "ha.dns_field_cert", HelpKey: "ha.dns_field_cert_help",
			Kind: FieldTextarea, Required: false,
		},
	},
	Caps: Capabilities{A: true, AAAA: false, TXT: false, TTL: true},
}

// cloudflareSpec is the second provider (B474) and the first one that is not reg.ru — which is what
// makes the seam a seam instead of an extraction. One field, because that is all Cloudflare needs: an
// API token scoped to the zone (the zone is its own column, so it is not repeated here). Its
// capabilities are the full set, and unlike reg.ru they are genuinely implemented.
var cloudflareSpec = Spec{
	Name:      "cloudflare",
	Label:     "Cloudflare (API token)",
	NeedsZone: true,
	Fields: []FieldSpec{
		{
			Name: "token", LabelKey: "ha.dns_field_cf_token", HelpKey: "ha.dns_field_cf_token_help",
			Kind: FieldPassword, Required: true,
		},
	},
	Caps: Capabilities{A: true, AAAA: true, TXT: true, TTL: true},
}

// providerSpecs is the display order of the registry. The first entry is what a fresh install offers.
var providerSpecs = []Spec{regruSpec, cloudflareSpec}

// Specs returns every known provider spec, in display order.
func Specs() []Spec { return append([]Spec(nil), providerSpecs...) }

// LookupSpec returns the spec for a provider identifier, accepting the historical aliases.
func LookupSpec(name string) (Spec, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == extcreds.ProviderExternal {
		n = extcreds.ProviderRegru
	}
	for _, s := range providerSpecs {
		if s.Name == n {
			return s, true
		}
	}
	return Spec{}, false
}

// FieldNames returns the field names a spec asks for, in order — the store uses it to drop unknown
// keys from a submitted form (an operator cannot smuggle an arbitrary key into the encrypted JSON).
func (s Spec) FieldNames() []string {
	out := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		out = append(out, f.Name)
	}
	return out
}
