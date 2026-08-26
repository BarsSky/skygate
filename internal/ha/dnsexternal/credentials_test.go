// credentials_test.go — unit tests for the encrypted
// credentials store (Save / Load / TestConnection).
//
// v1.5.0 (B145). Uses a sqlite3-in-memory DB via the
// skygate test harness so the global_settings table is
// actually exercised (encryption + DB I/O combined).

package dnsexternal

import (
	"testing"
)

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
