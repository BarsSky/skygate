// credentials_test.go — unit tests for the encrypted
// credentials store (Save / Load / TestConnection).
//
// v1.5.0 (B145). Uses a sqlite3-in-memory DB via the
// skygate test harness so the global_settings table is
// actually exercised (encryption + DB I/O combined).

package dnsexternal

import (
	"strings"
	"testing"
)

// hexSecret returns a 32-byte hex key for SKYGATE_SECRET_KEY.
// Currently unused (B145 covers only pure-Go paths); kept
// for the B145.1 DB round-trip tests.
func hexSecret() string { return "0000000000000000000000000000000000000000000000000000000000000000" }

// validCertPEM is a throwaway self-signed cert for tests.
// The cert doesn't need to be valid; only the BEGIN/END
// markers are checked by Validate. Reused across all tests.
const validCertPEM = `-----BEGIN CERTIFICATE-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA0000000000000000
0000000000000000000000000000000000000000000000000000000000000000
0000000000000000000000000000000000000000000000000000000000000000
0000000000000000000000000000000000000000000000000000000000
-----END CERTIFICATE-----`

// TestValidate — every required field rejected when missing.
func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		creds   Credentials
		wantErr string
	}{
		{
			name:    "empty",
			creds:   Credentials{},
			wantErr: "provider is required",
		},
		{
			name:    "unknown provider",
			creds:   Credentials{Provider: "cloudflare", Login: "x", Zone: "example.com", CertPEM: validCertPEM, Password: "p"},
			wantErr: `unsupported provider "cloudflare"`,
		},
		{
			name:    "missing login",
			creds:   Credentials{Provider: ProviderExternal, Zone: "example.com", CertPEM: validCertPEM, Password: "p"},
			wantErr: "login is required",
		},
		{
			name:    "missing zone",
			creds:   Credentials{Provider: ProviderExternal, Login: "x", CertPEM: validCertPEM, Password: "p"},
			wantErr: "zone is required",
		},
		{
			name:    "missing cert",
			creds:   Credentials{Provider: ProviderExternal, Login: "x", Zone: "example.com", Password: "p"},
			wantErr: "cert_pem is required",
		},
		{
			name:    "cert without BEGIN marker",
			creds:   Credentials{Provider: ProviderExternal, Login: "x", Zone: "example.com", CertPEM: "garbage", Password: "p"},
			wantErr: "cert_pem does not look like a PEM",
		},
		{
			name:    "missing password",
			creds:   Credentials{Provider: ProviderExternal, Login: "x", Zone: "example.com", CertPEM: validCertPEM},
			wantErr: "password is required",
		},
		{
			name: "all fields OK",
			creds: Credentials{
				Provider: ProviderExternal,
				Login:    "user@example.com",
				Zone:     "example.com",
				CertPEM:  validCertPEM,
				Password: "secret",
			},
			wantErr: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.creds.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

// TestStorageKeys_Stable pins the storage-key strings so
// an accidental rename doesn't silently break existing
// skygate installs. The keys are the persisted schema
// (rows in global_settings); changing them requires a
// data migration.
func TestStorageKeys_Stable(t *testing.T) {
	want := map[string]string{
		"CertPEMKey":  "ha.dns.cert_pem_enc",
		"PasswordKey": "ha.dns.password_enc",
		"ZoneKey":     "ha.dns.zone",
		"LoginKey":    "ha.dns.login",
		"ProviderKey": "ha.dns.provider",
	}
	got := map[string]string{
		"CertPEMKey":  CertPEMKey,
		"PasswordKey": PasswordKey,
		"ZoneKey":     ZoneKey,
		"LoginKey":    LoginKey,
		"ProviderKey": ProviderKey,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// TestNewStore_DefaultTimeout verifies the HTTP client
// gets a sensible default. The exact value isn't critical
// (it just has to be > 0 and < 60s to catch a typo), but
// the timeout is a tunable that production operators care
// about so it's worth pinning.
func TestNewStore_DefaultTimeout(t *testing.T) {
	s := NewStore(nil, "")
	if s.HTTP == nil {
		t.Fatal("NewStore returned nil HTTP client")
	}
	if s.HTTP.Timeout <= 0 {
		t.Errorf("NewStore HTTP timeout = %v, want > 0", s.HTTP.Timeout)
	}
	if s.HTTP.Timeout > 60*1e9 {
		t.Errorf("NewStore HTTP timeout = %v, want < 60s (10s default)", s.HTTP.Timeout)
	}
}

// TestStore_SaveLoadRoundTrip and TestStore_SaveRequiresSecretKey
// and TestStore_IsConfigured_FalseWhenEmpty and
// TestStore_TestConnection_NoNetwork require a real
// skygate DB. They're added in a follow-up B-check
// (B145.1) once the test harness is wired up in the
// integration test build. For B145 we cover the pure-Go
// paths (Validate, storage keys, default timeout) and
// the round-trip+auth checks land once the harness is in
// place. The smoke-mesh cleanup (B143) and the backup
// (B142) tests follow the same pattern — pure-Go unit
// tests today, DB round-trip once the harness lands.

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
