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

	dnsproviders "skygate/internal/dnsexternal"
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

// dnsRecordsSummary renders "name: A [ttl]" lines from the zone answer. Only A/AAAA records are
// interesting here (the switch moves an address); everything else is counted, not listed.
func dnsRecordsSummary(lang string, names []string, perName map[string][]extcreds.RegRecord) string {
	parts := make([]string, 0, len(names))
	for _, name := range names {
		addrs := []string{}
		ttl := 0
		others := 0
		for _, r := range perName[name] {
			typ := strings.ToUpper(strings.TrimSpace(r.Rectype))
			if typ == "" {
				typ = strings.ToUpper(strings.TrimSpace(r.Type))
			}
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
// Read-only: it loads the zone and prints the A/AAAA records for the requested names.
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
	creds, err := s.DNSCredsStore.Load()
	if err != nil || creds.Login == "" || creds.Password == "" || creds.Zone == "" {
		haRedirect(w, r, "", i18n.T(lang, "ha.dns_test_not_configured"))
		return
	}
	client := dnsproviders.NewClient(s.DNSCredsStore)
	perName := make(map[string][]extcreds.RegRecord, len(names))
	for _, name := range names {
		recs, _, _, rerr := client.RecordsFor(r.Context(), creds.Zone, name)
		if rerr != nil {
			s.Backend.Audit(c.UserID, c.Username, "ha.dns.records_failed",
				fmt.Sprintf("zone=%s names=%v err=%v", creds.Zone, names, rerr))
			haRedirect(w, r, "", i18n.Tf(lang, "ha.dns_records_err", firstLineOf(rerr.Error())))
			return
		}
		perName[name] = recs
	}
	summary := dnsRecordsSummary(lang, names, perName)
	s.Backend.Audit(c.UserID, c.Username, "ha.dns.records",
		fmt.Sprintf("zone=%s names=%v result=%s", creds.Zone, names, summary))
	haRedirect(w, r, i18n.Tf(lang, "ha.dns_records_ok", creds.Zone, summary), "")
}
