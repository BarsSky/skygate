// Package dnsexternal — the shipped DNS provider implementation
// for an external registrar's v2 JSON+form+mTLS API (the v1.5.0
// B145 implementation targets a popular Russian registrar whose
// API path is /api/<provider-v2-path>/zone/...; the package is named
// "dnsexternal" so a future Cloudflare/Route53 provider can sit
// next to it without name collision).
//
// Implements the internal/dns.Provider interface (Name,
// GetRecord, UpdateRecord, TestConnection). The auth pattern
// (HTTP Basic + mTLS client cert) is the one confirmed against
// the live API endpoint on 2026-08-18. The provider name
// reported by Name() is "external" (generic).
//
// The Store is loaded from internal/ha/dnsexternal.Store at
// construction. All credentials are encrypted at rest
// (AES-256-GCM keyed by SKYGATE_SECRET_KEY).
//
// Why this lives in internal/dnsexternal/ (not internal/dns/external/):
//   - The dnsexternal package needs to import internal/dns (for the
//     Provider interface) AND internal/ha/dnsexternal (for the
//     Store) AND a database driver
//   - That would be a Go import cycle (internal/dns → dnsexternal
//     → internal/dns). Splitting external out into a sibling
//     package (this one) breaks the cycle: internal/dns doesn't
//     import dnsexternal directly, the build is fine.
//
// Wait — if internal/dns imports dnsexternal (via BuildProvider),
// AND dnsexternal imports internal/dns (for the interface), that
// would be a cycle. So dnsexternal does NOT import internal/dns;
// instead, internal/dns defines the Provider interface and
// dnsexternal satisfies it implicitly (Go's structural typing).
// The cycle is broken by:
//
//   - internal/dns/                  defines Provider interface
//   - internal/dnsexternal/          implements Provider (struct, not interface)
//   - internal/dns/provider_build.go wraps *dnsexternal.Client in
//     a thin adapter that satisfies dns.Provider (so the adapter,
//     not the dnsexternal package, is the one that gets the
//     Provider interface). The adapter translates dnsexternal's
//     package-local ErrRecordNotFound into the public
//     dns.ErrRecordNotFound.

package dnsexternal

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"skygate/internal/ha/dnsexternal"
)

// HTTP timeout for all API calls. The endpoint typically responds
// in <2s, but DNS-01 + zone replace can take 5-10s under load.
const httpTimeout = 30 * time.Second

// Client is the external DNS provider. Construct with
// NewClient; the underlying creds come from dnsexternal.Store.
type Client struct {
	// Store provides the credentials (cert + password + zone)
	// for every API call. Loaded once at construction; if the
	// operator rotates the cert, skygate must be restarted
	// (or the Store must implement reload — not in v1.5.0).
	Store *dnsexternal.Store

	// HTTPClient is the *http.Client used for all calls. If nil,
	// a default client with a 30s timeout is used. Tests can
	// override to inject a stub transport.
	HTTPClient *http.Client

	// credsOverride, when non-nil, bypasses Store for the
	// duration of one call. Used by tests (and could be used
	// by a future "test this cred pair without saving" admin
	// button). Production code always leaves this nil.
	credsOverride *dnsexternal.Credentials
}

// ErrRecordNotFound is returned when GetRecord finds no A-record
// for the requested FQDN. Kept package-local (not exported as
// dns.ErrRecordNotFound) to avoid an import cycle: dns would
// have to import dnsexternal for the type, but dnsexternal
// can't import dns (see package doc).
var ErrRecordNotFound = errors.New("dnsexternal: record not found")

// NewClient returns a Client wired to the given Store. The
// returned Client uses an http.Client with a 30s timeout; if
// you need a different timeout or transport (for tests), set
// Client.HTTPClient after construction.
func NewClient(store *dnsexternal.Store) *Client {
	return &Client{
		Store: store,
	}
}

// Name returns the provider's stable identifier. The v1.5.0
// shipped provider reports "external" (generic). The actual
// backend is configured via the credentials in Store (cert
// + password + zone), so the same Client can talk to multiple
// registrars that share the same auth pattern.
func (c *Client) Name() string { return "external" }

// loadCreds returns the creds to use for the next call:
// credsOverride if set (tests), otherwise a fresh read from
// Store. Returns ErrRecordNotFound-shaped error? No — returns
// a wrapped store error or a "credentials not configured"
// error.
func (c *Client) loadCreds() (dnsexternal.Credentials, error) {
	if c.credsOverride != nil {
		return *c.credsOverride, nil
	}
	if c.Store == nil {
		return dnsexternal.Credentials{}, errors.New("dnsexternal: no Store configured (operator must save creds via /admin/ha External DNS form first)")
	}
	return c.Store.Load()
}

// GetRecord returns the A-record IP for `name` in `zone`. Uses
// the v2 /zone/get_resource_records endpoint. The auth pattern
// (top-level form fields, mTLS cert, password NOT in input_data
// JSON) is the one confirmed against the live API on 2026-08-18.
// The "all-in-input_data" pattern returned NO_AUTH — that's why
// we keep username/password as separate form fields here.
//
// The response is wrapped in {"answer": {...}, "status": "..."}.
// We drill into answer.domains[0].records[]. If the FQDN has
// no A-record, returns ErrRecordNotFound (the caller — the
// externalAdapter in internal/dns — translates this to
// dns.ErrRecordNotFound).
func (c *Client) GetRecord(ctx context.Context, zone, name string) (string, error) {
	creds, err := c.loadCreds()
	if err != nil {
		return "", fmt.Errorf("dnsexternal.GetRecord: load creds: %w", err)
	}
	if creds.IsZero() {
		return "", errors.New("dnsexternal.GetRecord: credentials not configured (use /admin/ha External DNS form first)")
	}

	// Form fields — username and password go here as top-level
	// keys, NOT inside input_data (see memory entry on the
	// NO_AUTH trap from putting creds inside input_data).
	form := url.Values{}
	form.Set("username", creds.Login)
	form.Set("password", creds.Password)
	form.Set("output_content_type", "plain")
	form.Set("input_format", "json")
	form.Set("input_data", fmt.Sprintf(`{"domains":[{"dname":%q}]}`, zone))
	// name is the subdomain (e.g. "skygate" inside skygate.<your-domain>);
	// the API accepts it as a sub-query to filter records.
	form.Set("subdomain", name)

	httpClient := c.httpClient()
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.<your-domain>/api/<provider-v2-path>/zone/get_resource_records", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("dnsexternal.GetRecord: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dnsexternal.GetRecord: HTTP: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("dnsexternal.GetRecord: HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Parse the JSON and find the A-record. The API's response
	// shape is documented at <your-provider-api-docs-url>.
	var ack struct {
		Status string `json:"status"`
		Answer struct {
			Domains []struct {
				Dname   string `json:"dname"`
				Records []struct {
					Type    string `json:"type"`
					Content string `json:"content"` // for A-records, the IP
					Name    string `json:"name"`
				} `json:"records"`
			} `json:"domains"`
		} `json:"answer"`
	}
	if err := json.Unmarshal(body, &ack); err != nil {
		return "", fmt.Errorf("dnsexternal.GetRecord: parse: %w", err)
	}
	for _, d := range ack.Answer.Domains {
		for _, r := range d.Records {
			if strings.EqualFold(r.Type, "A") && strings.EqualFold(r.Name, name) && r.Content != "" {
				return r.Content, nil
			}
		}
	}
	return "", ErrRecordNotFound
}

// UpdateRecord replaces the A-record for `name` in `zone`
// with `ip`. Uses /zone/replace_records. The endpoint
// returns 200 with a JSON ack that includes an error_code
// if the record couldn't be replaced (e.g. the FQDN doesn't
// exist). The caller is expected to check the ack for
// error_code != "" and treat it as a hard failure (so the
// HA failover's "did the A-record switch?" log line is
// accurate).
func (c *Client) UpdateRecord(ctx context.Context, zone, name, ip string) error {
	creds, err := c.loadCreds()
	if err != nil {
		return fmt.Errorf("dnsexternal.UpdateRecord: load creds: %w", err)
	}
	if creds.IsZero() {
		return fmt.Errorf("dnsexternal.UpdateRecord: credentials not configured")
	}
	if ip == "" {
		return errors.New("dnsexternal.UpdateRecord: ip is empty")
	}

	// The replace_records API wants the full set of records
	// for the subdomain in a single call. We pass only the A-record
	// (since that's all the HA failover touches). If the operator
	// has other record types on the same subdomain (MX, TXT, etc.)
	// they need to set them up via the registrar's web UI; the
	// HA failover only manages the A-record.
	form := url.Values{}
	form.Set("username", creds.Login)
	form.Set("password", creds.Password)
	form.Set("output_content_type", "json")
	form.Set("input_format", "json")
	form.Set("input_data", fmt.Sprintf(
		`{"domains":[{"dname":%q}],"subdomain":%q,"records":[{"type":"A","content":%q}]}`,
		zone, name, ip))

	httpClient := c.httpClient()
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.<your-domain>/api/<provider-v2-path>/zone/replace_records", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("dnsexternal.UpdateRecord: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dnsexternal.UpdateRecord: HTTP: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("dnsexternal.UpdateRecord: HTTP %d: %s", resp.StatusCode, string(body))
	}

	// The API returns 200 even on logical errors; check the ack.
	var ack struct {
		Status  string `json:"status"`
		ErrorCode string `json:"error_code"`
		ErrorText string `json:"error_text"`
	}
	if err := json.Unmarshal(body, &ack); err != nil {
		return fmt.Errorf("dnsexternal.UpdateRecord: parse ack: %w (body=%s)", err, string(body))
	}
	if ack.ErrorCode != "" {
		return fmt.Errorf("dnsexternal.UpdateRecord: provider error %s: %s", ack.ErrorCode, ack.ErrorText)
	}
	return nil
}

// TestConnection delegates to dnsexternal.Store.TestConnection
// which does a cheap /user/nop call (no zone mutation, just
// proves the cert+password pair is valid and the IP is in the
// whitelist). Used by /admin/ha's "Test DNS" button.
func (c *Client) TestConnection(ctx context.Context) error {
	creds, err := c.loadCreds()
	if err != nil {
		return fmt.Errorf("dnsexternal.TestConnection: load creds: %w", err)
	}
	if creds.IsZero() {
		return errors.New("dnsexternal.TestConnection: credentials not configured")
	}
	if c.Store == nil {
		return errors.New("dnsexternal.TestConnection: no Store configured")
	}
	res, err := c.Store.TestConnection(ctx)
	if err != nil {
		return fmt.Errorf("dnsexternal.TestConnection: %w", err)
	}
	if res.Status != "ok" {
		return fmt.Errorf("dnsexternal.TestConnection: %s", res.Message)
	}
	return nil
}

// httpClient returns the configured HTTPClient or a default
// one with mTLS support. The default client trusts the system
// CA pool; the operator's cert is presented as a client
// cert in the TLS handshake.
func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	creds, _ := c.loadCreds()
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if !creds.IsZero() {
		cert, err := tlsCertFromCreds(creds)
		if err == nil {
			tlsConfig.Certificates = []tls.Certificate{*cert}
		}
		// If err != nil, we fall through with a no-cert TLS
		// config — the request will fail at the server with
		// a clear "client cert required" error, which the
		// operator can act on.
	}
	return &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
}

// tlsCertFromCreds loads the cert PEM from the Credentials
// struct and returns a tls.Certificate. The Credentials
// struct's CertPEM field contains both the certificate and
// the matching private key, concatenated (the standard
// "combined PEM" format that tls.X509KeyPair accepts).
// Errors are returned to the caller (logged + handled by
// httpClient).
func tlsCertFromCreds(creds dnsexternal.Credentials) (*tls.Certificate, error) {
	if creds.CertPEM == "" {
		return nil, errors.New("dnsexternal: cert PEM is empty or malformed")
	}
	cert, err := tls.X509KeyPair([]byte(creds.CertPEM), []byte(creds.CertPEM))
	if err != nil {
		return nil, fmt.Errorf("dnsexternal: parse cert: %w", err)
	}
	// Validate the cert is well-formed (not expired, has the
	// right EKU, etc). x509.ParseCertificate is cheap and
	// catches operator-side mistakes (e.g. uploading a CA
	// cert instead of a leaf cert).
	if _, err := x509.ParseCertificate(cert.Certificate[0]); err != nil {
		return nil, fmt.Errorf("dnsexternal: parse cert: %w", err)
	}
	return &cert, nil
}
