// ha_dns_b472_test.go — B472 (2026-11-14). The four sentences and the two pure shapes behind the
// External-DNS card, executed instead of grepped:
//
//  1. THE TEST BUTTON'S ANSWER. The handler used to compute a sentence and drop it (`_ = ok`).
//     `dnsTestMessage` is now the single place that words the four outcomes, so each one can be
//     asserted — including the case that used to be invisible: an unknown status must be reported
//     as itself, never as "ok".
//  2. THE NAMES FIELD. Empty means "the name the page knows about" (skygate), never "the whole
//     zone" — the flash is one line, and a zone holds hundreds of records.
//  3. THE ZONE SUMMARY. Only A/AAAA become an address list; everything else is counted, and a
//     name with no A record says so instead of rendering an empty string.
package admin

import (
	"errors"
	"strings"
	"testing"

	extcreds "skygate/internal/ha/dnsexternal"
	"skygate/internal/i18n"
)

func b472Catalog(t *testing.T) string {
	t.Helper()
	if i18n.GlobalCatalog == nil {
		i18n.GlobalCatalog = i18n.New()
	}
	return "ru"
}

// TestB472_TestMessageCoversEveryStatus — each outcome gets its own sentence, and success is
// distinguishable from failure by a flag rather than by parsing the text.
func TestB472_TestMessageCoversEveryStatus(t *testing.T) {
	lang := b472Catalog(t)
	cases := []struct {
		name string
		res  extcreds.TestConnectionResult
		err  error
		want string
		ok   bool
	}{
		{"ok", extcreds.TestConnectionResult{Status: "ok", LatencyMS: 42, HTTPStatus: 200}, nil, "42", true},
		{"auth", extcreds.TestConnectionResult{Status: "auth_error", Message: "bad password"}, nil, "bad password", false},
		{"network", extcreds.TestConnectionResult{Status: "network_error", Message: "timeout"}, nil, "timeout", false},
		// The catalogue is loaded in this test, so the expectation is the TRANSLATED key's text —
		// comparing against the key name would only prove the fallback works.
		{"not configured", extcreds.TestConnectionResult{Status: "not_configured"}, nil, i18n.T(lang, "ha.dns_test_not_configured"), false},
		{"unknown", extcreds.TestConnectionResult{Status: "weird", Message: "hm"}, nil, "weird", false},
		{"error", extcreds.TestConnectionResult{}, errors.New("boom"), "boom", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := dnsTestMessage(lang, tc.res, tc.err)
			if ok != tc.ok {
				t.Errorf("success = %v, want %v (%q)", ok, tc.ok, msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("message %q does not mention %q", msg, tc.want)
			}
			if strings.Contains(msg, "%!") {
				t.Errorf("message %q has a broken format verb — the i18n arguments do not match the key", msg)
			}
		})
	}
}

// TestB472_TestMessageKeepsOneLine — the flash travels in a query parameter and renders inside one
// banner, so a multi-line provider message must be clipped to its first line (and bounded).
func TestB472_TestMessageKeepsOneLine(t *testing.T) {
	lang := b472Catalog(t)
	msg, _ := dnsTestMessage(lang, extcreds.TestConnectionResult{
		Status:  "auth_error",
		Message: "ERROR: auth failed\nstack: line 2\nmore",
	}, nil)
	if strings.Contains(msg, "\n") {
		t.Errorf("message %q contains a newline — it renders as broken markup in the banner", msg)
	}
	long := strings.Repeat("x", 900)
	msg, _ = dnsTestMessage(lang, extcreds.TestConnectionResult{Status: "auth_error", Message: long}, nil)
	if len(msg) > 420 {
		t.Errorf("message is %d bytes; the flash must stay small", len(msg))
	}
}

// TestB472_ParseDNSNames — the default, deduplication, case folding and the cap.
func TestB472_ParseDNSNames(t *testing.T) {
	if got := parseDNSNames(""); len(got) != 1 || got[0] != "skygate" {
		t.Errorf("empty field = %v, want [skygate] (never the whole zone)", got)
	}
	got := parseDNSNames(" Skygate , head;;skygate,derp ")
	want := []string{"skygate", "head", "derp"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("parsed %v, want %v (deduped, lowercased, order kept)", got, want)
	}
	many := strings.Repeat("n,", 40)
	if n := len(parseDNSNames(many)); n > 12 {
		t.Errorf("parsed %d names; the flash must be bounded", n)
	}
}

// TestB472_RecordsSummary — A/AAAA become addresses with the TTL, other types are counted, and a
// missing A record is SAID, not rendered as an empty gap.
func TestB472_RecordsSummary(t *testing.T) {
	lang := b472Catalog(t)
	perName := map[string][]extcreds.RegRecord{
		"skygate": {
			{Rectype: "A", Content: "45.152.198.217", TTL: 300},
			{Rectype: "A", Content: "10.0.0.5", TTL: 300},
			{Rectype: "MX", Content: "mail.example.com"},
		},
		"head": {{Type: "A", Content: "192.0.2.10"}}, // the alternate spelling of the field names
		"derp": nil,                                  // no record at all
	}
	got := dnsRecordsSummary(lang, []string{"skygate", "head", "derp"}, perName)
	for _, want := range []string{"skygate: 10.0.0.5, 45.152.198.217", "TTL 300", "192.0.2.10", "derp"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q is missing %q", got, want)
		}
	}
	if !strings.Contains(got, "ha.dns_records_no_a") && !strings.Contains(got, "A-записи нет") {
		t.Errorf("a name without an A record is not called out: %q", got)
	}
	if strings.Contains(got, "mail.example.com") {
		t.Errorf("a non-address record leaked into the address list: %q", got)
	}
}
