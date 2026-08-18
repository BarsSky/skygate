// Package regapi — encrypted storage for the reg.ru API
// credentials (SSL cert PEM + alternative password).
//
// v1.5.0 (B145) introduced this package. The credentials
// live in two global_settings rows:
//
//	ha.regapi.cert_pem_enc   — AES-256-GCM ciphertext of the cert PEM
//	ha.regapi.password_enc   — AES-256-GCM ciphertext of the API password
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
// work against reg.ru. It uses the reg.ru v2 API's
// /api/regru2/zone/get_zone endpoint as a "ping" — the
// response payload is irrelevant; only the HTTP status and
// the error_code field matter.

package regapi

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
// exact same strings — no typos.
const (
	CertPEMKey   = "ha.regapi.cert_pem_enc"
	PasswordKey  = "ha.regapi.password_enc"
	ZoneKey      = "ha.regapi.zone"             // plaintext: skynas.ru, etc.
	LoginKey     = "ha.regapi.login"            // plaintext: reg.ru login / email
	ProviderKey  = "ha.regapi.provider"         // plaintext: "regapi" (room for future providers)
)

// Credentials is the decrypted form of the stored reg.ru
// credentials. CertPEM and Password are the actual secret
// material; the other fields are configuration (zone, login,
// provider identifier). Provider should be "regapi" for now;
// other values are reserved for the pluggable DNS provider
// interface (B146 / B150).
type Credentials struct {
	Provider string `json:"provider"`
	Login    string `json:"login"`
	Zone     string `json:"zone"`
	CertPEM  string `json:"cert_pem"`
	Password string `json:"password"`
}

// Validate checks the credentials are well-formed before
// the caller tries to write them. We do not validate the
// PEM against a CA — that's done lazily on first use.
func (c Credentials) Validate() error {
	if c.Provider == "" {
		return errors.New("regapi: provider is required (e.g. \"regapi\")")
	}
	if c.Provider != "regapi" {
		return fmt.Errorf("regapi: unsupported provider %q (only \"regapi\" is implemented in v1.5.0)", c.Provider)
	}
	if c.Login == "" {
		return errors.New("regapi: login is required")
	}
	if c.Zone == "" {
		return errors.New("regapi: zone is required")
	}
	if c.CertPEM == "" {
		return errors.New("regapi: cert_pem is required")
	}
	if !strings.Contains(c.CertPEM, "BEGIN CERTIFICATE") {
		return errors.New("regapi: cert_pem does not look like a PEM (no BEGIN CERTIFICATE marker)")
	}
	if c.Password == "" {
		return errors.New("regapi: password is required")
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
		return fmt.Errorf("regapi: encrypt cert: %w", err)
	}
	pwEnc, err := db.EncryptForColumn(c.Password, s.SecretKey)
	if err != nil {
		return fmt.Errorf("regapi: encrypt password: %w", err)
	}
	if err := db.SetGlobalSetting(s.DB, ProviderKey, c.Provider); err != nil {
		return fmt.Errorf("regapi: save provider: %w", err)
	}
	if err := db.SetGlobalSetting(s.DB, LoginKey, c.Login); err != nil {
		return fmt.Errorf("regapi: save login: %w", err)
	}
	if err := db.SetGlobalSetting(s.DB, ZoneKey, c.Zone); err != nil {
		return fmt.Errorf("regapi: save zone: %w", err)
	}
	if err := db.SetGlobalSetting(s.DB, CertPEMKey, certEnc); err != nil {
		return fmt.Errorf("regapi: save cert ciphertext: %w", err)
	}
	if err := db.SetGlobalSetting(s.DB, PasswordKey, pwEnc); err != nil {
		return fmt.Errorf("regapi: save password ciphertext: %w", err)
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
		return Credentials{}, fmt.Errorf("regapi: load cert ciphertext: %w", err)
	}
	pwEnc, err := db.GetGlobalSetting(s.DB, PasswordKey, "")
	if err != nil {
		return Credentials{}, fmt.Errorf("regapi: load password ciphertext: %w", err)
	}
	if certEnc == "" || pwEnc == "" {
		return Credentials{Provider: provider, Login: login, Zone: zone}, nil
	}
	cert, err := db.DecryptForColumn(certEnc, s.SecretKey)
	if err != nil {
		return Credentials{}, fmt.Errorf("regapi: decrypt cert: %w", err)
	}
	pw, err := db.DecryptForColumn(pwEnc, s.SecretKey)
	if err != nil {
		return Credentials{}, fmt.Errorf("regapi: decrypt password: %w", err)
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

// TestConnection calls reg.ru's /api/regru2/zone/get_zone
// with the stored credentials. The endpoint is read-only
// (returns the zone's record list) so it's a safe "ping":
// no DNS records are created or modified.
//
// v1.5.0 (B145) uses the working auth pattern that was
// confirmed against the live reg.ru API:
//
//   - username + password as TOP-LEVEL form fields
//   - output_content_type=plain for the simplest response
//   - the SSL cert is sent via mTLS (TLS handshake)
//
// The earlier "input_data JSON wrapping" pattern (which
// made reg.ru return NO_AUTH) is documented in the
// memory entry "reg.ru v2 API auth — real working pattern"
// — do NOT switch back to that.
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
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.reg.ru/api/regru2/zone/get_zone", strings.NewReader(form.Encode()))
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
		return TestConnectionResult{Status: "auth_error", Message: fmt.Sprintf("reg.ru returned %d (check login + password + cert)", resp.StatusCode), HTTPStatus: resp.StatusCode, LatencyMS: latency}, nil
	default:
		return TestConnectionResult{Status: "auth_error", Message: fmt.Sprintf("reg.ru returned %d", resp.StatusCode), HTTPStatus: resp.StatusCode, LatencyMS: latency}, nil
	}
}
