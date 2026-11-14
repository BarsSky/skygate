// ha_dns_form_b472.go — B472 (2026-11-14): the External-DNS card must be usable.
//
// THE OPERATOR'S QUESTION, verbatim: «по кредам куда задать на самом skygate есть поле ввода?»
// The answer was "yes, /admin/ha has one — but it cannot save the reg.ru credentials", for three
// reasons that this file and the template fix:
//
//  1. THE FORM DEMANDED A CLIENT CERTIFICATE. `cert_pem` was `required` in the template, left over
//     from the mTLS model B468 removed. `Credentials.Validate()` has since accepted — and for
//     reg.ru expects — an EMPTY certificate, and a stored one makes the client try mTLS
//     (`internal/dnsexternal/client.go`: `tls.X509KeyPair`). So the browser refused to submit the
//     one shape the server wanted, and the only way to satisfy it was to store a lie that then
//     broke the client. The hint under the form said «вставьте SSL cert (PEM) + alt-пароль» —
//     i.e. it instructed the operator to do exactly that.
//
//  2. THE TEST BUTTON SAID NOTHING. `PostAdminHADNSCredsTest` computed a sentence and threw it
//     away (`_ = ok` followed by an empty redirect), while four i18n keys for those very sentences
//     existed and were unused. A button that answers nothing is worse than no button: the operator
//     concludes the panel is broken, not the credentials.
//
//  3. THERE WAS NO WAY TO SEE WHAT DNS SAYS. The CLI has `dns-records`; the panel had nothing, so
//     the runbook's first question («что сейчас в A-записи?») required a shell — on the host that
//     is about to be stopped. This file adds `POST /admin/ha/dns/records`.
//
// Everything here is read-only except the save (which lives in ha.go): the test hits the
// provider's record-list endpoint, and the records view is the same `RecordsFor` the CLI prints.
package admin

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"skygate/internal/dns"
	extcreds "skygate/internal/ha/dnsexternal"
	"skygate/internal/i18n"
)

// dnsTestMessage turns a TestConnection outcome into the sentence the operator reads.
//
// Pure on purpose: the handler becomes "call, flash, redirect", and the wording (including the
// four outcomes and the error case) is testable without a provider, a store or a request.
func dnsTestMessage(lang string, res extcreds.TestConnectionResult, err error) (msg string, success bool) {
	if err != nil && res.Status == "" {
		return i18n.Tf(lang, "ha.dns_test_error", err.Error()), false
	}
	switch res.Status {
	case "ok":
		return i18n.Tf(lang, "ha.dns_test_ok", res.LatencyMS, res.HTTPStatus), true
	case "auth_error":
		return i18n.Tf(lang, "ha.dns_test_auth_error", firstLineOf(res.Message)), false
	case "network_error":
		return i18n.Tf(lang, "ha.dns_test_network_error", firstLineOf(res.Message)), false
	case "not_configured":
		return i18n.T(lang, "ha.dns_test_not_configured"), false
	}
	// Any future status is reported as itself rather than swallowed: an unknown answer that reads
	// as "ok" is how a broken integration stays invisible.
	return i18n.Tf(lang, "ha.dns_test_other", res.Status, firstLineOf(res.Message)), false
}

// firstLineOf keeps a provider's multi-line answer to one line: the flash is a query parameter
// and the page shows it inside a banner, where an embedded newline reads as broken markup.
func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// parseDNSNames is the names field of the records form: comma/space separated, de-duplicated, and
// bounded. An empty list means "the one the panel knows about" (skygate), never "the whole zone":
// a zone can hold hundreds of records and the flash is a one-line banner.
func parseDNSNames(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == ';'
	}) {
		n := strings.ToLower(strings.TrimSpace(f))
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		if len(out) >= 12 {
			break
		}
	}
	if len(out) == 0 {
		out = []string{"skygate"}
	}
	return out
}

// normalizeDNSZone makes the zone field tolerant of what people actually paste: a full URL, a
// hostname with a trailing dot, capitals, stray spaces. What it does NOT do is guess which domain
// the operator meant — the registrar answers "zone not found" for a domain the account does not
// manage, and that is the honest error (the «Read the zone» button surfaces it verbatim).
//
// The live confusion it exists for: the field was filled with `reg.ru` — the REGISTRAR's own
// domain — instead of the zone the account manages.
func normalizeDNSZone(raw string) string {
	z := strings.TrimSpace(raw)
	if z == "" {
		return ""
	}
	if i := strings.Index(z, "://"); i >= 0 {
		z = z[i+3:]
	}
	if i := strings.IndexAny(z, "/?#"); i >= 0 {
		z = z[:i]
	}
	// a zone is a name, never host:port
	if i := strings.LastIndex(z, ":"); i > 0 && !strings.Contains(z[i:], "]") {
		z = z[:i]
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(z), "."))
}

// dnsRecordsSummary renders "name: A [ttl]" lines from the zone answer. Only A/AAAA records are
// interesting here (the switch moves an address); everything else is counted, not listed.
//
// B473: it takes provider-neutral `dns.Record`s, so the same one-line summary serves every provider
// instead of the reg.ru record struct.
func dnsRecordsSummary(lang string, names []string, perName map[string][]dns.Record) string {
	parts := make([]string, 0, len(names))
	for _, name := range names {
		addrs := []string{}
		ttl := 0
		others := 0
		for _, r := range perName[name] {
			typ := strings.ToUpper(strings.TrimSpace(r.Type))
			switch typ {
			case "A", "AAAA":
				addrs = append(addrs, strings.TrimSpace(r.Content))
				if r.TTL > 0 {
					ttl = r.TTL
				}
			default:
				others++
			}
		}
		line := name + ": " + i18n.T(lang, "ha.dns_records_no_a")
		if len(addrs) > 0 {
			sort.Strings(addrs)
			line = name + ": " + strings.Join(addrs, ", ")
			if ttl > 0 {
				line += fmt.Sprintf(" (TTL %d)", ttl)
			}
		}
		if others > 0 {
			line += fmt.Sprintf(" (+%d %s)", others, i18n.T(lang, "ha.dns_records_other"))
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "; ")
}

// PostAdminHADNSRecords handles POST /admin/ha/dns/records — the panel twin of `dns-records`.
//
// B473: it goes through the PROVIDER interface (`ListRecords`), built from the active credential ROW,
// so it works for any provider the registry knows instead of importing the reg.ru client directly.
func (s *Service) PostAdminHADNSRecords(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	if s.DNSCredsStore == nil {
		haRedirect(w, r, "", i18n.T(lang, "ha.dns_store_missing"))
		return
	}
	if err := r.ParseForm(); err != nil {
		haRedirect(w, r, "", err.Error())
		return
	}
	names := parseDNSNames(r.FormValue("names"))
	provider, row, err := s.activeDNSProvider()
	if err != nil {
		haRedirect(w, r, "", i18n.T(lang, "ha.dns_test_not_configured"))
		return
	}
	recs, lerr := provider.ListRecords(r.Context(), row.Zone)
	if lerr != nil {
		s.Backend.Audit(c.UserID, c.Username, "ha.dns.records_failed",
			fmt.Sprintf("provider=%s zone=%s err=%v", row.Provider, row.Zone, lerr))
		haRedirect(w, r, "", i18n.Tf(lang, "ha.dns_records_err", firstLineOf(lerr.Error())))
		return
	}
	summary := dnsRecordsSummaryByNames(lang, names, recs)
	s.Backend.Audit(c.UserID, c.Username, "ha.dns.records",
		fmt.Sprintf("provider=%s zone=%s names=%v result=%s", row.Provider, row.Zone, names, summary))
	haRedirect(w, r, i18n.Tf(lang, "ha.dns_records_ok", row.Zone, summary), "")
}

// dnsRecordsSummaryByNames filters a whole-zone `ListRecords` answer down to the names the operator
// asked about. Split from dnsRecordsSummary (which takes a per-name map) so the provider-agnostic
// path needs no per-name round trips: one zone read, then the same one-line summary.
func dnsRecordsSummaryByNames(lang string, names []string, all []dns.Record) string {
	want := map[string]bool{}
	for _, n := range names {
		want[strings.ToLower(strings.TrimSpace(n))] = true
	}
	perName := map[string][]dns.Record{}
	for _, rec := range all {
		short := strings.ToLower(strings.TrimSpace(rec.Name))
		// Providers may answer with the FQDN or the bare subname; accept both for the same zone.
		if !want[short] {
			base := short
			if i := strings.IndexByte(short, '.'); i > 0 {
				base = short[:i]
			}
			if !want[base] {
				continue
			}
			short = base
		}
		perName[short] = append(perName[short], rec)
	}
	return dnsRecordsSummary(lang, names, perName)
}
