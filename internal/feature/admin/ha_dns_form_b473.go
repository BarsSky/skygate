// ha_dns_form_b473.go — B473: the /admin/ha DNS card becomes SCHEMA-DRIVEN.
//
// WHY. The card's fields were reg.ru's, hardcoded in the template and in the save handler
// (`login`, `password`, `cert_pem`, `zone`). That is the second half of the reason the block could not
// be adapted to another provider: even with a provider registry, the form could not ASK for a
// Cloudflare token or an RFC 2136 TSIG key. Now the form renders whatever `dns.Spec.Fields` says and
// this file stores exactly what the spec asks for, keyed by field name.
//
// TWO RULES THAT MATTER
//
//  1. A MASKED SECRET IS MERGED, NEVER WIPED. Password-kind inputs render empty (the stored value is
//     never sent back to the browser); an empty submission therefore means "keep what is stored",
//     merged from the existing row for the same (provider, zone). Without this, editing the zone would
//     silently clear the alternative password — the one failure that locks the operator out of their
//     own registrar.
//  2. UNKNOWN FIELDS ARE DROPPED. Only names the spec declares can reach the encrypted JSON, so a
//     hand-made request cannot smuggle an arbitrary key into the credential row.
package admin

import (
	"fmt"
	"net/http"
	"strings"

	"skygate/internal/dns"
	extcreds "skygate/internal/ha/dnsexternal"
	"skygate/internal/i18n"
)

// dnsFormFieldsFromRequest reads one submission into the provider's own field map.
//
// It accepts BOTH spellings the page has used: `f_<name>` (the schema-driven form) and the bare
// `<name>` (the pre-B473 form), so a page cached in an operator's browser still saves correctly during
// an upgrade.
func dnsFormFieldsFromRequest(spec dns.Spec, form interface{ Get(string) string }) map[string]string {
	out := map[string]string{}
	for _, f := range spec.Fields {
		v := strings.TrimSpace(form.Get("f_" + f.Name))
		if v == "" {
			v = strings.TrimSpace(form.Get(f.Name))
		}
		if v == "" {
			continue // empty = "not supplied" (see rule 1)
		}
		out[f.Name] = v
	}
	return out
}

// mergeDNSFields keeps every stored field the submission did not supply. Rule 1, in one place.
func mergeDNSFields(spec dns.Spec, stored, submitted map[string]string) map[string]string {
	out := map[string]string{}
	for _, f := range spec.Fields {
		if v, ok := submitted[f.Name]; ok && v != "" {
			out[f.Name] = v
			continue
		}
		if v, ok := stored[f.Name]; ok {
			out[f.Name] = v
		}
	}
	return out
}

// dnsMissingRequired names the REQUIRED fields a submission (merged with what is stored) leaves empty,
// so the flash can say which one instead of a generic validation error.
func dnsMissingRequired(lang string, spec dns.Spec, fields map[string]string) []string {
	var missing []string
	for _, f := range spec.Fields {
		if f.Required && strings.TrimSpace(fields[f.Name]) == "" {
			missing = append(missing, dnsFieldLabel(lang, f))
		}
	}
	return missing
}

// dnsFieldLabel resolves a field's label for the request language, falling back to the field NAME.
//
// The registry holds KEYS, not prose (see internal/dns/registry_b473.go), so this is the one place
// that turns them into words. A key present in neither catalogue comes back from i18n.T as itself;
// showing "login" beats showing "ha.dns_field_login".
func dnsFieldLabel(lang string, f dns.FieldSpec) string {
	if f.LabelKey == "" {
		return f.Name
	}
	if v := strings.TrimSpace(i18n.T(lang, f.LabelKey)); v != "" && v != f.LabelKey {
		return v
	}
	return f.Name
}

// dnsFieldHelp is dnsFieldLabel's counterpart for the hint under an input. An absent key yields "",
// because a hint that reads "ha.dns_field_login_help" is noise, while a missing LABEL is unusable.
func dnsFieldHelp(lang string, f dns.FieldSpec) string {
	if f.HelpKey == "" {
		return ""
	}
	if v := strings.TrimSpace(i18n.T(lang, f.HelpKey)); v != "" && v != f.HelpKey {
		return v
	}
	return ""
}

// saveDNSCredentials is the whole save path: spec lookup, merge, store as a ROW, make it active.
// It replaces the flat reg.ru-shaped parser (parseHADNSCredsForm), which could only ever store one
// provider's four answers.
func (s *Service) saveDNSCredentials(lang, provider, rawZone string, form interface{ Get(string) string }) (int64, error) {
	spec, ok := dns.LookupSpec(provider)
	if !ok {
		return 0, fmt.Errorf("unknown DNS provider %q", provider)
	}
	zone := normalizeDNSZone(rawZone)
	if zone == "" {
		return 0, fmt.Errorf("zone is required")
	}
	submitted := dnsFormFieldsFromRequest(spec, form)
	stored := map[string]string{}
	if existing, found, err := s.DNSCredsStore.ProviderRowByKey(spec.Name, zone); err != nil {
		return 0, err
	} else if found {
		stored = existing.Fields
	}
	fields := mergeDNSFields(spec, stored, submitted)
	if missing := dnsMissingRequired(lang, spec, fields); len(missing) > 0 {
		return 0, fmt.Errorf("required field(s) empty: %s", strings.Join(missing, ", "))
	}
	id, err := s.DNSCredsStore.SaveProviderRow(spec.Name, zone, fields)
	if err != nil {
		return 0, err
	}
	if err := s.DNSCredsStore.SetActiveRow(id); err != nil {
		return 0, err
	}
	return id, nil
}

// activeDNSProvider builds the PROVIDER for the active credential row — the one object the panel's
// «Test» and «Read the zone» need, whatever the provider is.
func (s *Service) activeDNSProvider() (dns.Provider, extcreds.ProviderRow, error) {
	row, err := s.DNSCredsStore.ActiveProviderRow()
	if err != nil {
		return nil, extcreds.ProviderRow{}, err
	}
	p, berr := dns.BuildProvider(row.Provider, dns.BuildDeps{
		DB:        s.dbc(),
		SecretKey: s.SecretKeyHex,
		Row:       &row,
	})
	if berr != nil {
		return nil, row, berr
	}
	if p == nil {
		return nil, row, fmt.Errorf("provider %q produced no client", row.Provider)
	}
	return p, row, nil
}

// dnsCardField is one rendered credential input.
//
// The template must not make POLICY decisions, so each one arrives as a field:
//   - a secret is never prefilled (Value stays ""), because the page must not be able to re-render a
//     token it was never given;
//   - a field with a STORED value is NOT `required` in the browser, because an empty submission means
//     "keep the stored one" (rule 1) and a browser-side check would block exactly that;
//   - Required therefore means "required AND nothing stored", which is the only combination where a
//     client-side check helps.
type dnsCardField struct {
	Name        string
	Label       string
	Help        string
	Kind        string // the input type: "text", "password" or "email"
	Placeholder string
	Value       string // non-secret prefill; always "" for FieldPassword
	Required    bool
	Stored      bool // a value exists in the row → the hint says "leave empty to keep it"
}

// dnsCapBadge is one capability chip («A», «AAAA», «TXT», «TTL»). Shown for every provider that offers
// the badge, and shown GREY for the missing ones: a provider without TTL is a fact the operator needs
// BEFORE the switch, not a failure afterwards.
type dnsCapBadge struct {
	Key   string // A | AAAA | TXT | TTL
	Label string
	Title string // supported / not supported, in the request language
	OK    bool
}

// dnsCardGroup is one provider's block of inputs in the shared form. Every group is rendered, but only
// the editing one is enabled: an operator who picks another provider in the select sees ITS fields
// instead of a reg.ru-shaped form that cannot express a token.
type dnsCardGroup struct {
	Name      string
	Label     string
	Fields    []dnsCardField
	Caps      []dnsCapBadge
	IsEditing bool
	HasRow    bool // a stored credential exists for this provider → the badge row says "configured"
}

// dnsCardView is what the template renders for the card: the provider select, one group per provider,
// and the shared zone.
type dnsCardView struct {
	Specs []dns.Spec
	// Groups is one entry per spec, in registry order. The template renders all of them and reveals
	// the matching one (server-side `hidden` for the editing group, so a no-JS client still gets a
	// working — if single-provider — form).
	Groups  []dnsCardGroup
	Editing string // the provider id whose group is visible on load
	// ActiveLabel is the human name of the provider the switch will actually use — the fact the
	// operator needs when several rows are configured.
	ActiveLabel string
	HasActive   bool
	// Zone is the shared zone input's value (the active row's, else the first stored row's).
	Zone    string
	HasRows bool
}

// dnsFieldIsSecret reports whether a field must never be sent back to the browser. It is a property of
// the KIND, so a new masked kind is covered by adding it here rather than by remembering every caller.
func dnsFieldIsSecret(k dns.FieldKind) bool {
	return k == dns.FieldPassword || k == dns.FieldTextarea
}

// dnsCapBadges renders the capability chips for one provider in the request language.
func dnsCapBadges(lang string, caps dns.Capabilities) []dnsCapBadge {
	mk := func(key, labelKey string, ok bool) dnsCapBadge {
		title := "ha.dns_cap_no"
		if ok {
			title = "ha.dns_cap_yes"
		}
		return dnsCapBadge{Key: key, Label: i18n.T(lang, labelKey), Title: i18n.T(lang, title), OK: ok}
	}
	return []dnsCapBadge{
		mk("A", "ha.dns_cap_a", caps.A),
		mk("AAAA", "ha.dns_cap_aaaa", caps.AAAA),
		mk("TXT", "ha.dns_cap_txt", caps.TXT),
		mk("TTL", "ha.dns_cap_ttl", caps.TTL),
	}
}

// dnsFieldsForSpec turns one provider's field schema + its stored row into the inputs to render.
//
// Pure on purpose: the two rules that matter are decided HERE and can be asserted without a database —
// a secret is never prefilled, and a field that already has a value is not `required` in the browser
// (because an empty submission means "keep", which a client-side check would block).
func dnsFieldsForSpec(lang string, spec dns.Spec, row extcreds.ProviderRow) []dnsCardField {
	out := make([]dnsCardField, 0, len(spec.Fields))
	for _, f := range spec.Fields {
		cf := dnsCardField{
			Name: f.Name, Label: dnsFieldLabel(lang, f), Help: dnsFieldHelp(lang, f),
			Kind: string(f.Kind), Placeholder: f.Placeholder, Required: f.Required,
		}
		if stored := row.Field(f.Name); stored != "" {
			cf.Stored = true
			cf.Required = false
			if !dnsFieldIsSecret(f.Kind) {
				cf.Value = stored
			}
		}
		out = append(out, cf)
	}
	return out
}

// buildDNSCardView resolves the card's data. Never returns an empty card: an unconfigured install still
// renders the form (with the registry's first provider) so the operator can configure one.
//
// `requested` is the provider id from ?provider= — the no-JS way to switch groups. An unknown value is
// ignored rather than honoured, so a hand-made URL cannot make the page render a provider that does not
// exist (the save path would reject it anyway, but the page should not lie first).
func (s *Service) buildDNSCardView(lang, requested string) dnsCardView {
	v := dnsCardView{Specs: dns.Specs()}
	if len(v.Specs) == 0 {
		return v
	}
	// One pass over the rows gives every provider's own stored values: an operator who configured
	// reg.ru, switched to Cloudflare and switched BACK must see «stored, leave empty to keep» on the
	// reg.ru password, not an empty `required` field that locks them out of their own registrar.
	byProvider := map[string]extcreds.ProviderRow{}
	zone := ""
	if s.DNSCredsStore != nil {
		if rows, err := s.DNSCredsStore.ListProviderRows(); err == nil {
			for _, row := range rows {
				p := row.Provider
				if p == extcreds.ProviderExternal {
					p = extcreds.ProviderRegru
				}
				if _, seen := byProvider[p]; !seen {
					byProvider[p] = row
					v.HasRows = true
					if zone == "" {
						zone = row.Zone
					}
				}
				if row.Active {
					v.HasActive = true
					v.ActiveLabel = row.Provider
					zone = row.Zone
				}
			}
		}
	}
	v.Zone = zone

	editing := ""
	if spec, ok := dns.LookupSpec(requested); ok && strings.TrimSpace(requested) != "" {
		editing = spec.Name
	}
	for _, spec := range v.Specs {
		if spec.Label == "" {
			spec.Label = spec.Name
		}
		row, hasRow := byProvider[spec.Name]
		v.Groups = append(v.Groups, dnsCardGroup{
			Name: spec.Name, Label: spec.Label, HasRow: hasRow,
			Fields: dnsFieldsForSpec(lang, spec, row),
			Caps:   dnsCapBadges(lang, spec.Caps),
		})
	}
	// Default group: the operator's explicit choice, else the ACTIVE provider, else the first spec.
	if editing == "" && v.HasActive {
		if spec, ok := dns.LookupSpec(v.ActiveLabel); ok {
			editing = spec.Name
			v.ActiveLabel = spec.Label
		}
	}
	if editing == "" && len(v.Specs) > 0 {
		editing = v.Specs[0].Name
		if v.ActiveLabel == "" {
			v.ActiveLabel = v.Specs[0].Label
		}
	}
	v.Editing = editing
	for i := range v.Groups {
		v.Groups[i].IsEditing = v.Groups[i].Name == editing
	}
	return v
}

// PostAdminHADNSCredsSave — the schema-driven save.
func (s *Service) PostAdminHADNSCredsSave(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	if s.DNSCredsStore == nil {
		haRedirect(w, r, "", s.I18n.T(lang, "ha.dns_store_missing"))
		return
	}
	if err := r.ParseForm(); err != nil {
		haRedirect(w, r, "", err.Error())
		return
	}
	provider := strings.TrimSpace(r.FormValue("provider"))
	id, err := s.saveDNSCredentials(lang, provider, r.FormValue("zone"), r.Form)
	if err != nil {
		haRedirect(w, r, "", err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "ha.dns.creds.save",
		fmt.Sprintf("provider=%s zone=%s row=%d", provider, normalizeDNSZone(r.FormValue("zone")), id))
	haRedirect(w, r, s.I18n.T(lang, "ha.dns_saved"), "")
}
