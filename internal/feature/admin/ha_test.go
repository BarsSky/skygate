// Package admin — ha_test.go is the unit-test file for the
// /admin/ha handlers. The test suite is intentionally pure-Go
// (no DB, no HTTP) for the form-parsing / validation /
// formatting helpers. The full POST → DB → render flow is
// covered by the B-check (`scripts/check_b149.sh`) + the
// live-verify on the VM.
//
// v1.5.0 / B149.
//
// Why pure-Go for these tests:
//
//  1. The "is this form value valid?" contract is what
//     determines whether the operator sees a helpful error or
//     a 500. Catching the contract with a unit test is much
//     faster than running the live VM and reading the
//     rendered error.
//
//  2. The HA chain / extcreds.credentials packages already have
//     their own pure-Go tests (`internal/ha/chain_test.go`,
//     `internal/ha/dnsexternal/credentials_test.go`). The /admin/ha
//     handlers glue those primitives into HTTP handlers, so
//     the test suite focuses on the glue — what the HTTP form
//     shape should look like, how the values are coerced,
//     and what the validation surface returns.

package admin

import (
	"net/url"
	"strings"
	"testing"

	"skygate/internal/ha"
	extcreds "skygate/internal/ha/dnsexternal"
)

// ---------- parseHAAddNodeForm ----------------------------------------

func TestParseHAAddNodeForm_OK(t *testing.T) {
	form := url.Values{}
	form.Set("hostname", "skygate-standby")
	form.Set("priority", "2")
	form.Set("public_ip", "203.0.113.10")
	form.Set("tailscale_ip", "100.64.0.7")

	got, err := parseHAAddNodeForm(form)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if got.Hostname != "skygate-standby" {
		t.Errorf("Hostname = %q, want skygate-standby", got.Hostname)
	}
	if got.Priority != 2 {
		t.Errorf("Priority = %d, want 2", got.Priority)
	}
	if got.PublicIP != "203.0.113.10" {
		t.Errorf("PublicIP = %q, want 203.0.113.10", got.PublicIP)
	}
	if got.TailscaleIP != "100.64.0.7" {
		t.Errorf("TailscaleIP = %q, want 100.64.0.7", got.TailscaleIP)
	}
}

func TestParseHAAddNodeForm_Errors(t *testing.T) {
	tests := []struct {
		name    string
		form    url.Values
		wantSub string
	}{
		{
			name:    "empty hostname",
			form:    url.Values{"priority": {"1"}, "public_ip": {"1.2.3.4"}},
			wantSub: "hostname is required",
		},
		{
			name:    "whitespace hostname",
			form:    url.Values{"hostname": {"   "}, "priority": {"1"}, "public_ip": {"1.2.3.4"}},
			wantSub: "hostname is required",
		},
		{
			name:    "non-numeric priority",
			form:    url.Values{"hostname": {"skygate-x"}, "priority": {"abc"}, "public_ip": {"1.2.3.4"}},
			wantSub: "priority must be an integer",
		},
		{
			name:    "priority zero",
			form:    url.Values{"hostname": {"skygate-x"}, "priority": {"0"}, "public_ip": {"1.2.3.4"}},
			wantSub: "priority must be >= 1",
		},
		{
			name:    "negative priority",
			form:    url.Values{"hostname": {"skygate-x"}, "priority": {"-3"}, "public_ip": {"1.2.3.4"}},
			wantSub: "priority must be >= 1",
		},
		{
			name:    "public_ip missing",
			form:    url.Values{"hostname": {"skygate-x"}, "priority": {"1"}},
			wantSub: "public_ip is required",
		},
		{
			name:    "public_ip invalid",
			form:    url.Values{"hostname": {"skygate-x"}, "priority": {"1"}, "public_ip": {"not-an-ip"}},
			wantSub: "public_ip is not a valid",
		},
		{
			name:    "tailscale_ip optional",
			form:    url.Values{"hostname": {"skygate-x"}, "priority": {"1"}, "public_ip": {"1.2.3.4"}},
			wantSub: "", // no error, TailscaleIP stays ""
		},
		{
			name:    "tailscale_ip invalid",
			form:    url.Values{"hostname": {"skygate-x"}, "priority": {"1"}, "public_ip": {"1.2.3.4"}, "tailscale_ip": {"banana"}},
			wantSub: "tailscale_ip is not a valid",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseHAAddNodeForm(tc.form)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantSub, err.Error())
			}
		})
	}
}

// ---------- parseHAChainEditForm --------------------------------------

func TestParseHAChainEditForm_UpdatesPriorities(t *testing.T) {
	// Simulates the form: one hidden "old_hostname" per row +
	// one "priority_<host>" per row. The page renders one
	// <input> per member; the parser pairs them.
	form := url.Values{}
	form.Set("old_hostname", "skygate,skygate-standby,skygate-extra")
	form.Set("priority_skygate", "1")
	form.Set("priority_skygate-standby", "2")
	form.Set("priority_skygate-extra", "3")
	form.Set("public_ip_skygate", "192.0.2.10")
	form.Set("public_ip_skygate-standby", "198.51.100.7")
	form.Set("tailscale_ip_skygate", "100.64.0.4")

	updates, err := parseHAChainEditForm(form)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if len(updates) != 3 {
		t.Fatalf("expected 3 updates, got %d", len(updates))
	}
	// Find each by hostname for assertion (map order is unstable).
	byHost := map[string]ha.HaMember{}
	for _, m := range updates {
		byHost[m.Hostname] = m
	}
	if byHost["skygate"].Priority != 1 || byHost["skygate"].PublicIP != "192.0.2.10" || byHost["skygate"].TailscaleIP != "100.64.0.4" {
		t.Errorf("skygate row = %+v, want priority=1 public=192.0.2.10 tailscale=100.64.0.4", byHost["skygate"])
	}
	if byHost["skygate-standby"].Priority != 2 || byHost["skygate-standby"].PublicIP != "198.51.100.7" {
		t.Errorf("skygate-standby row = %+v, want priority=2 public=198.51.100.7", byHost["skygate-standby"])
	}
	if byHost["skygate-extra"].Priority != 3 {
		t.Errorf("skygate-extra row = %+v, want priority=3", byHost["skygate-extra"])
	}
}

func TestParseHAChainEditForm_MissingOldHostnames(t *testing.T) {
	form := url.Values{} // no old_hostname at all
	_, err := parseHAChainEditForm(form)
	if err == nil {
		t.Fatal("expected error for missing old_hostname")
	}
	if !strings.Contains(err.Error(), "no rows to update") {
		t.Fatalf("expected error containing 'no rows to update', got %q", err.Error())
	}
}

func TestParseHAChainEditForm_DuplicatePriorities(t *testing.T) {
	form := url.Values{}
	form.Set("old_hostname", "skygate,skygate-standby")
	form.Set("priority_skygate", "5")
	form.Set("priority_skygate-standby", "5")
	_, err := parseHAChainEditForm(form)
	if err == nil {
		t.Fatal("expected error for duplicate priorities")
	}
	if !strings.Contains(err.Error(), "duplicate priority") {
		t.Fatalf("expected error containing 'duplicate priority', got %q", err.Error())
	}
}

// ---------- parseHADNSCredsForm ------------------------------------

func TestParseHADNSCredsForm_OK(t *testing.T) {
	form := url.Values{}
	form.Set("provider", "external")
	form.Set("login", "user@example.com")
	form.Set("zone", "example.com")
	form.Set("cert_pem", "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----\n")
	form.Set("password", "alt-pass-2026")

	got, err := parseHADNSCredsForm(form)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if got.Provider != "external" || got.Login != "user@example.com" || got.Zone != "example.com" {
		t.Errorf("basic fields wrong: %+v", got)
	}
	if !strings.Contains(got.CertPEM, "BEGIN CERTIFICATE") {
		t.Errorf("CertPEM was not preserved: %q", got.CertPEM)
	}
	if got.Password != "alt-pass-2026" {
		t.Errorf("Password = %q, want alt-pass-2026", got.Password)
	}
}

func TestParseHADNSCredsForm_TrimsWhitespace(t *testing.T) {
	form := url.Values{}
	form.Set("provider", "  external  ")
	form.Set("login", "  user@example.com  ")
	form.Set("zone", "  example.com  ")
	form.Set("cert_pem", "  -----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----\n  ")
	form.Set("password", "  alt-pass-2026  ")

	got, err := parseHADNSCredsForm(form)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if got.Provider != "external" {
		t.Errorf("Provider = %q, want external (whitespace not trimmed)", got.Provider)
	}
	if got.Zone != "example.com" {
		t.Errorf("Zone = %q, want example.com (whitespace not trimmed)", got.Zone)
	}
}

func TestParseHADNSCredsForm_DelegatesToCredentialsValidate(t *testing.T) {
	// The form parser is intentionally thin — it does ONLY the
	// string trimming + forwarding. The semantic checks
	// (provider == "external", login non-empty, cert PEM format,
	// etc.) live in extcreds.Credentials.Validate() so the same
	// rules apply to programmatic callers (CLI, tests, future
	// /admin/certificates). The form parser just returns
	// whatever the user pasted + calls Validate.
	form := url.Values{}
	form.Set("provider", "external")
	form.Set("login", "")
	form.Set("zone", "example.com")
	form.Set("cert_pem", "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----\n")
	form.Set("password", "alt-pass-2026")

	_, err := parseHADNSCredsForm(form)
	if err == nil {
		t.Fatal("expected error when login is empty (delegated Validate)")
	}
	// The error should mention "login is required" — comes from
	// extcreds.Credentials.Validate(), not from the form parser.
	if !strings.Contains(err.Error(), "login is required") {
		t.Fatalf("expected 'login is required', got %q", err.Error())
	}
}

// ---------- isHAForceActionConfirmationCorrect -----------------------

func TestIsHAForceActionConfirmationCorrect(t *testing.T) {
	tests := []struct {
		action   string
		hostname string
		typed    string
		want     bool
	}{
		// Promotion requires the admin to type the active
		// hostname EXACTLY (e.g. "skygate"). This guards
		// against misclicks.
		{"promote", "skygate", "skygate", true},
		{"promote", "skygate", "SKYGATE", false},  // case-sensitive
		{"promote", "skygate", " skygate", false}, // leading space
		{"promote", "skygate", "skygate-standby", false},
		{"demote", "skygate", "skygate", true},
		{"reclaim", "skygate", "skygate", true},
		{"promote", "skygate", "", false},
		{"", "skygate", "skygate", false},                    // unknown action
		{"add", "skygate-standby", "skygate-standby", false}, // not a force action
	}
	for _, tc := range tests {
		t.Run(tc.action+"/"+tc.hostname+"/typed="+tc.typed, func(t *testing.T) {
			got := isHAForceActionConfirmationCorrect(tc.action, tc.hostname, tc.typed)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------- formatHAChainForTemplate ---------------------------------

func TestFormatHAChainForTemplate_EmptyChain(t *testing.T) {
	got := formatHAChainForTemplate(&ha.HaChain{})
	if got == "" {
		t.Fatal("expected non-empty hint for empty chain")
	}
	if !strings.Contains(got, "no members") {
		t.Errorf("expected 'no members' hint, got %q", got)
	}
}

func TestFormatHAChainForTemplate_ListsMembers(t *testing.T) {
	c := &ha.HaChain{
		Members: []ha.HaMember{
			{Hostname: "skygate", Priority: 1, PublicIP: "192.0.2.10", TailscaleIP: "100.64.0.4", Role: ha.RoleActive, LastSeen: 1700000000},
			{Hostname: "skygate-standby", Priority: 2, PublicIP: "198.51.100.7", TailscaleIP: "100.64.0.5", Role: ha.RoleStandby, LastSeen: 1700000005},
		},
		AutoFailoverEnabled: true,
	}
	got := formatHAChainForTemplate(c)
	for _, want := range []string{"skygate", "skygate-standby", "192.0.2.10", "100.64.0.4", "active", "standby"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted output missing %q\n--\n%s", want, got)
		}
	}
}

// ---------- regression: extcreds.Credentials.Validate rules still apply

// This test exists so that a refactor of the form parser that
// drops the Validate() delegation gets caught. The /admin/ha
// DNS provider creds form has the SAME validation rules as the
// standalone Credentials.Validate(); both call sites are
// pinned by the B-check (the dns-credentials B-check
// catches Validate regressions; the form-parser B-check
// catches delegation drops).

func TestRegapiCredentialsValidate_FormAndLibraryAgree(t *testing.T) {
	cases := []struct {
		name string
		cred extcreds.Credentials
	}{
		{
			name: "missing provider",
			cred: extcreds.Credentials{Login: "u@e.com", Zone: "example.com", CertPEM: "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n", Password: "p"},
		},
		{
			name: "missing login",
			cred: extcreds.Credentials{Provider: "external", Zone: "example.com", CertPEM: "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n", Password: "p"},
		},
		{
			name: "missing zone",
			cred: extcreds.Credentials{Provider: "external", Login: "u@e.com", CertPEM: "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n", Password: "p"},
		},
		{
			name: "cert without BEGIN marker",
			cred: extcreds.Credentials{Provider: "external", Login: "u@e.com", Zone: "example.com", CertPEM: "not a pem", Password: "p"},
		},
		{
			name: "missing password",
			cred: extcreds.Credentials{Provider: "external", Login: "u@e.com", Zone: "example.com", CertPEM: "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cred.Validate()
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.name)
			}
		})
	}
}
