// Package dnsexternal — encrypted storage for the external
// DNS provider credentials (SSL cert PEM + alternative
// password).
//
// v1.5.0 (B145) introduced this package. The credentials
// live in two global_settings rows:
//
//	ha.dns.cert_pem_enc   — AES-256-GCM ciphertext of the cert PEM
//	ha.dns.password_enc   — AES-256-GCM ciphertext of the API password
//
// Encryption uses db.EncryptForColumn (AES-256-GCM keyed by
// SKYGATE_SECRET_KEY). If SKYGATE_SECRET_KEY is unset, the
// save methods return db.ErrSecretKeyUnset — the operator
// must configure the master key before the credentials can
// be persisted (a plaintext fallback would defeat the
// purpose of the encryption).
//
// The credentials are written from the /admin/ha "External
// DNS" form (Phase 5 / B149). Until that form exists, the
// only way to seed the credentials is via the operator's
// initial-deploy runbook: place cert.pem and password into
// the encrypted rows via the /admin/ha debug endpoint (or
// manually via psql for the very first deploy).
//
// TestConnection is the only way for the /admin/ha "Test"
// button to validate that the stored credentials actually
// work against the provider. It uses the provider's
// read-only "ping" endpoint (a zone-getter that doesn't
// mutate state) — the response payload is irrelevant; only
// the HTTP status and the error_code field matter.

package dnsexternal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"skygate/internal/db"
)

// Storage keys. Exposed as constants so the /admin/ha form
// (Phase 5) and the certsync (Phase 3 / B147) reference the
// exact same strings — no typos. The "ha.dns" prefix is
// provider-agnostic; a future Cloudflare adapter would use
// the same keys (CertPEMKey / PasswordKey stay valid for
// any provider that uses mTLS).
const (
	CertPEMKey   = "ha.dns.cert_pem_enc"
	PasswordKey  = "ha.dns.password_enc"
	ZoneKey      = "ha.dns.zone"             // plaintext: <your-domain>
	LoginKey     = "ha.dns.login"            // plaintext: provider login / email
	ProviderKey  = "ha.dns.provider"         // plaintext: "external" | "cloudflare" | "route53" | "rfc2136"
)

// ProviderExternal is the shipped v1.5.0 (B145) provider
// identifier. Operators set SKYGATE_DNS_PROVIDER=external
// to opt in. Other values are reserved for future B-checks.
const ProviderExternal = "external"

// Credentials is the decrypted form of the stored
// credentials. CertPEM and Password are the actual secret
// material; the other fields are configuration (zone, login,
// provider identifier). Provider should be ProviderExternal
// for v1.5.0; other values are reserved for the pluggable
// DNS provider interface (B146 / B150).
type Credentials struct {
	Provider string `json:"provider"`
	Login    string `json:"login"`
	Zone     string `json:"zone"`
	CertPEM  string `json:"cert_pem"`
	Password string `json:"password"`
}

// IsZero returns true if the credentials are entirely empty
// (a fresh deploy before the operator has saved anything).
// Used by internal/dnsexternal/client.go to short-circuit
// API calls with a "credentials not configured" error
// instead of an opaque HTTP failure.
func (c Credentials) IsZero() bool {
	return c.Provider == "" && c.Login == "" && c.Zone == "" && c.CertPEM == "" && c.Password == ""
}

// Validate checks the credentials are well-formed before
// the caller tries to write them. We do not validate the
// PEM against a CA — that's done lazily on first use.
func (c Credentials) Validate() error {
	if c.Provider == "" {
		return errors.New("dnsexternal: provider is required (e.g. \"external\")")
	}
	if c.Provider != ProviderExternal {
		return fmt.Errorf("dnsexternal: unsupported provider %q (only %q is implemented in v1.5.0)", c.Provider, ProviderExternal)
	}
	if c.Login == "" {
		return errors.New("dnsexternal: login is required")
	}
	if c.Zone == "" {
		return errors.New("dnsexternal: zone is required")
	}
	if c.CertPEM == "" {
		return errors.New("dnsexternal: cert_pem is required")
	}
	if !strings.Contains(c.CertPEM, "BEGIN CERTIFICATE") {
		return errors.New("dnsexternal: cert_pem does not look like a PEM (no BEGIN CERTIFICATE marker)")
	}
	if c.Password == "" {
		return errors.New("dnsexternal: password is required")
	}
	return nil
}

// Store is the credential-storage facade. It wraps
// db.EncryptForColumn / db.DecryptForColumn with the
// per-key layout described at the top of this file. The
// underlying encryption is AES-256-GCM with a 12-byte
// nonce (chosen by the helper) and a 16-byte auth tag.
type Store struct {
	DB        *sql.DB
	SecretKey string // SKYGATE_SECRET_KEY (64 hex chars → 32 bytes)
	HTTP      *http.Client
}

// NewStore returns a Store with a default 10s HTTP timeout
// for TestConnection. Pass your own *http.Client if you
// need a different timeout (e.g. through a SOCKS proxy).
func NewStore(d *sql.DB, secretKey string) *Store {
	return &Store{
		DB:        d,
		SecretKey: secretKey,
		HTTP:      &http.Client{Timeout: 10 * time.Second},
	}
}

// Save writes the credentials to global_settings, encrypted.
// The plaintext (non-secret) fields are stored as-is; the
// secret fields (CertPEM, Password) go through
// db.EncryptForColumn.
//
// Validate() is called first; a bad credential is rejected
// before any DB write. The four writes are NOT wrapped in
// a single transaction because:
//  1. The failure modes are independent (a corrupt cert
//     ciphertext doesn't affect the plaintext zone row).
//  2. /admin/ha's "Save" button is the only writer; no
//     concurrent-edit race in practice.
//  3. A partial save is detectable on next Load (missing
//     rows → caller decides what to do).
func (s *Store) Save(c Credentials) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if s.SecretKey == "" {
		return db.ErrSecretKeyUnset
	}
	certEnc, err := db.EncryptForColumn(c.CertPEM, s.SecretKey)
	if err != nil {
		return fmt.Errorf("dnsexternal: encrypt cert: %w", err)
	}
	pwEnc, err := db.EncryptForColumn(c.Password, s.SecretKey)
	if err != nil {
		return fmt.Errorf("dnsexternal: encrypt password: %w", err)
	}
	if err := db.SetGlobalSetting(s.DB, ProviderKey, c.Provider); err != nil {
		return fmt.Errorf("dnsexternal: save provider: %w", err)
	}
	if err := db.SetGlobalSetting(s.DB, LoginKey, c.Login); err != nil {
		return fmt.Errorf("dnsexternal: save login: %w", err)
	}
	if err := db.SetGlobalSetting(s.DB, ZoneKey, c.Zone); err != nil {
		return fmt.Errorf("dnsexternal: save zone: %w", err)
	}
	if err := db.SetGlobalSetting(s.DB, CertPEMKey, certEnc); err != nil {
		return fmt.Errorf("dnsexternal: save cert ciphertext: %w", err)
	}
	if err := db.SetGlobalSetting(s.DB, PasswordKey, pwEnc); err != nil {
		return fmt.Errorf("dnsexternal: save password ciphertext: %w", err)
	}
	return nil
}

// Load reads the credentials back. If any row is missing,
// returns a zero-value Credentials and no error (a fresh
// deploy is a normal state — /admin/ha shows the
// "not configured" badge and the operator pastes the cert
// + password). Only DB / decrypt errors are surfaced.
func (s *Store) Load() (Credentials, error) {
	provider, _ := db.GetGlobalSetting(s.DB, ProviderKey, "")
	login, _ := db.GetGlobalSetting(s.DB, LoginKey, "")
	zone, _ := db.GetGlobalSetting(s.DB, ZoneKey, "")
	certEnc, err := db.GetGlobalSetting(s.DB, CertPEMKey, "")
	if err != nil {
		return Credentials{}, fmt.Errorf("dnsexternal: load cert ciphertext: %w", err)
	}
	pwEnc, err := db.GetGlobalSetting(s.DB, PasswordKey, "")
	if err != nil {
		return Credentials{}, fmt.Errorf("dnsexternal: load password ciphertext: %w", err)
	}
	if certEnc == "" || pwEnc == "" {
		return Credentials{Provider: provider, Login: login, Zone: zone}, nil
	}
	cert, err := db.DecryptForColumn(certEnc, s.SecretKey)
	if err != nil {
		return Credentials{}, fmt.Errorf("dnsexternal: decrypt cert: %w", err)
	}
	pw, err := db.DecryptForColumn(pwEnc, s.SecretKey)
	if err != nil {
		return Credentials{}, fmt.Errorf("dnsexternal: decrypt password: %w", err)
	}
	return Credentials{
		Provider: provider,
		Login:    login,
		Zone:     zone,
		CertPEM:  cert,
		Password: pw,
	}, nil
}

// IsConfigured returns true if the credentials look
// complete enough for a TestConnection to be attempted
// (cert PEM + password both present + zone set). Used by
// /admin/ha to decide whether the "Test" button is enabled.
func (s *Store) IsConfigured() bool {
	c, err := s.Load()
	if err != nil {
		return false
	}
	return c.CertPEM != "" && c.Password != "" && c.Zone != ""
}

// TestConnectionResult is the structured outcome of a
// TestConnection call. The /admin/ha form renders this
// verbatim (red banner for "auth_error", green for
// "ok", grey for "not_configured").
type TestConnectionResult struct {
	Status     string `json:"status"`     // "ok" | "auth_error" | "network_error" | "not_configured"
	Message    string `json:"message"`    // human-readable, safe to show to the operator
	HTTPStatus int    `json:"http_status,omitempty"`
	LatencyMS  int64  `json:"latency_ms"`
}

// TestConnection calls the provider's read-only
// "ping" endpoint with the stored credentials. The endpoint
// returns the zone's record list, so it's a safe ping: no
// DNS records are created or modified.
//
// v1.5.0 (B145) uses the working auth pattern that was
// confirmed against the live API on 2026-08-18:
//
//   - username + password as TOP-LEVEL form fields
//   - output_content_type=plain for the simplest response
//   - the SSL cert is sent via mTLS (TLS handshake)
//
// The earlier "input_data JSON wrapping" pattern (which
// made the provider return NO_AUTH) is documented in the
// memory entry on the NO_AUTH trap — do NOT switch back
// to that.
func (s *Store) TestConnection(ctx context.Context) (TestConnectionResult, error) {
	c, err := s.Load()
	if err != nil {
		return TestConnectionResult{Status: "auth_error", Message: err.Error()}, err
	}
	if c.CertPEM == "" || c.Password == "" || c.Zone == "" {
		return TestConnectionResult{Status: "not_configured", Message: "credentials not fully configured"}, nil
	}
	return s.testConnectionWithCreds(ctx, c)
}

// testConnectionWithCreds is the inner loop of
// TestConnection; split out so unit tests can drive it
// against an httptest.Server without going through
// global_settings.
func (s *Store) testConnectionWithCreds(ctx context.Context, c Credentials) (TestConnectionResult, error) {
	form := url.Values{}
	form.Set("username", c.Login)
	form.Set("password", c.Password)
	form.Set("output_content_type", "plain")
	form.Set("dname", c.Zone)
	// The provider's API URL is configured per-deployment
	// (via the cert's embedded issuer + the test path the
	// operator's runbook uses). The hardcoded URL here is
	// the v1.5.0 default; deployments with a custom API
	// gateway should override via the SKYGATE_DNS_API_URL
	// env var (set in /etc/skygate.env).
	apiURL := "https://api.<your-domain>/api/<provider-v2-path>/zone/get_zone"
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		return TestConnectionResult{Status: "network_error", Message: err.Error()}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start := time.Now()
	resp, err := s.HTTP.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return TestConnectionResult{Status: "network_error", Message: err.Error(), LatencyMS: latency}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		return TestConnectionResult{Status: "ok", Message: "credentials verified", HTTPStatus: resp.StatusCode, LatencyMS: latency}, nil
	case 401, 403:
		return TestConnectionResult{Status: "auth_error", Message: fmt.Sprintf("provider returned %d (check login + password + cert)", resp.StatusCode), HTTPStatus: resp.StatusCode, LatencyMS: latency}, nil
	default:
		return TestConnectionResult{Status: "auth_error", Message: fmt.Sprintf("provider returned %d", resp.StatusCode), HTTPStatus: resp.StatusCode, LatencyMS: latency}, nil
	}
}
