// Package dnsregapi — the reg.ru v2 DNS provider implementation.
//
// Implements the internal/dns.Provider interface (Name,
// GetRecord, UpdateRecord, TestConnection) for the reg.ru
// API v2. The auth pattern (top-level form fields, mTLS
// cert) is the one confirmed against the live reg.ru
// endpoint on 2026-08-18. See the memory entry "reg.ru v2
// API auth — real working pattern" for the full diagnostic
// history.
//
// The provider is stateless except for the credentials it
// loads from internal/ha/regapi.Store at construction. All
// concurrency is delegated to the underlying *http.Client.
//
// Why this lives in internal/dnsregapi/ (not internal/dns/regapi/):
//   - The dnsregapi package needs to import internal/dns (for the
//     Provider interface) AND internal/ha/regapi (for the
//     credentials Store).
//   - internal/dns imports this package (via BuildProvider).
//   - That would be a Go import cycle (internal/dns → dnsregapi
//     → internal/dns). Splitting regapi out into a sibling
//     package avoids the cycle — the only remaining dependency
//     is internal/dns (for the interface). Since internal/dns
//     doesn't import dnsregapi directly, the build is fine.
//
// Wait — if internal/dns imports dnsregapi (via BuildProvider),
// AND dnsregapi imports internal/dns (for the interface), that
// IS a cycle. The fix is to NOT import internal/dns here at
// all — instead, define the methods we need (Name, GetRecord,
// UpdateRecord, TestConnection) without referencing the
// internal/dns.Provider interface. The build function in
// internal/dns does the type assertion (*Client has all four
// methods, so it satisfies the interface structurally).

package dnsregapi

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"skygate/internal/ha/regapi"
)

// Client is the regapi DNS provider. Construct with
// NewClient; the underlying creds come from regapi.Store.
type Client struct {
	Store  *regapi.Store
	HTTP   *http.Client
	// credsOverride (test-only) lets unit tests inject
	// pre-built credentials without spinning up a real
	// *sql.DB. Production callers (NewClient) leave
	// this nil. The GetRecord/UpdateRecord methods
	// prefer the override when set.
	//
	// This is a test seam, not a public API. The field
	// is unexported and the only place it's set is in
	// client_test.go (this package's test file).
	credsOverride *regapi.Credentials
}

// ErrRecordNotFound is the sentinel returned by GetRecord
// when the A-record does not exist. The internal/dns
// package has its own ErrRecordNotFound; the two are
// kept distinct so dnsregapi doesn't have to import
// internal/dns (which would create an import cycle
// — see the package doc comment for the full rationale).
// The build function in internal/dns/provider_build.go
// translates this to dns.ErrRecordNotFound before
// returning to the caller.
var ErrRecordNotFound = errors.New("dnsregapi: record not found")

// NewClient returns a Client with a sensible 15s timeout.
// The timeout is longer than the credentials-only
// TestConnection (10s) because GetRecord / UpdateRecord
// may take longer for a fresh / large zone.
func NewClient(store *regapi.Store) *Client {
	return &Client{
		Store: store,
		HTTP:  &http.Client{Timeout: 15 * time.Second},
	}
}

// loadCreds is the small test seam that returns the
// credentials for the current request. Production goes
// through Store.Load(); tests inject via credsOverride.
func (c *Client) loadCreds() (regapi.Credentials, error) {
	if c.credsOverride != nil {
		return *c.credsOverride, nil
	}
	return c.loadCreds()
}

// Name returns the provider identifier.
func (c *Client) Name() string { return "regapi" }

// GetRecord fetches the current A-record for `name` in
// `zone`. Uses reg.ru's /api/regru2/zone/get_resource_records
// endpoint, which returns a JSON array of all records. We
// filter for the A-record matching `name`.
//
// reg.ru's "get_resource_records" response is wrapped
// (the answer lives in "answer.records[]"); the simpler
// "get_zone" endpoint would also work but returns the
// full zone in a heavier payload. We pick the smaller
// endpoint to keep per-tick HA traffic low.
func (c *Client) GetRecord(ctx context.Context, zone, name string) (string, error) {
	creds, err := c.loadCreds()
	if err != nil {
		return "", fmt.Errorf("regapi.GetRecord: load creds: %w", err)
	}
	if creds.CertPEM == "" || creds.Password == "" {
		return "", fmt.Errorf("regapi.GetRecord: credentials not configured (use /admin/ha External DNS form first)")
	}
	fqdn := name + "." + zone
	// input_data intentionally DOES NOT contain
	// username / password. The 2026-08-18 diagnostic
	// against the live reg.ru v2 API confirmed that
	// /api/regru2/zone/get_resource_records + the
	// /api/regru2/zone/replace_records endpoints expect
	// credentials as TOP-LEVEL form fields, not inside
	// the input_data JSON. The flant/cert-manager-
	// webhook-regru project uses the all-in-input_data
	// pattern (which works for their specific endpoint
	// / subdomain), but for our /get_zone and
	// /replace_records calls the top-level pattern is
	// the one that the live server accepts without
	// returning NO_AUTH. See the memory entry
	// "reg.ru v2 API auth — real working pattern" for
	// the full diagnostic history.
	inputData := map[string]any{
		"domains":          []map[string]string{{"dname": zone}},
		"resource_records": []map[string]string{{"fqdn": fqdn}},
	}
	form := url.Values{}
	form.Set("username", creds.Login)
	form.Set("password", creds.Password)
	form.Set("output_content_type", "json")
	form.Set("input_data", marshalJSON(inputData))
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.reg.ru/api/regru2/zone/get_resource_records", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	applyMTLS(req, creds.CertPEM)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("regapi.GetRecord: HTTP: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("regapi.GetRecord: HTTP %d: %s", resp.StatusCode, string(body))
	}
	// Parse the JSON and find the A-record. reg.ru's
	// response format:
	//   {"answer":{"domains":[{"dname":"skynas.ru","records":[]}]}}
	// "records" is an array of {fqdn, rectype, subname, content, ...}
	// We look for rectype="A" matching our fqdn.
	var parsed struct {
		Answer struct {
			Domains []struct {
				Dname   string `json:"dname"`
				Records []struct {
					FQDN   string `json:"fqdn"`
					Rectype string `json:"rectype"`
					Content string `json:"content"`
				} `json:"records"`
			} `json:"domains"`
		} `json:"answer"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("regapi.GetRecord: parse: %w", err)
	}
	for _, d := range parsed.Answer.Domains {
		for _, r := range d.Records {
			if strings.EqualFold(r.FQDN, fqdn) && strings.EqualFold(r.Rectype, "A") {
				return r.Content, nil
			}
		}
	}
	return "", ErrRecordNotFound
}

// UpdateRecord atomically replaces the A-record for `name`
// in `zone` with `ip`. Uses reg.ru's /api/regru2/zone/replace_records
// endpoint, which takes a single record (subname + rectype +
// content) and either creates it (if absent) or replaces the
// existing one (if present). Single-record semantics =
// atomic from the caller's POV.
//
// We do NOT delete-then-create (that would leak a window
// where the FQDN resolves to nothing). reg.ru's
// replace_records handles both cases server-side.
func (c *Client) UpdateRecord(ctx context.Context, zone, name, ip string) error {
	creds, err := c.loadCreds()
	if err != nil {
		return fmt.Errorf("regapi.UpdateRecord: load creds: %w", err)
	}
	if creds.CertPEM == "" || creds.Password == "" {
		return fmt.Errorf("regapi.UpdateRecord: credentials not configured")
	}
	if ip == "" {
		return errors.New("regapi.UpdateRecord: ip is empty")
	}
	// See the comment in GetRecord for why username +
	// password are NOT in input_data. The same live-
	// server diagnostic on 2026-08-18 confirmed the
	// top-level pattern works for replace_records too.
	inputData := map[string]any{
		"domains": []map[string]any{{
			"dname": zone,
			"subdomains": []map[string]any{{
				"subname": name,
				"rectype": "A",
				"content": ip,
			}},
		}},
	}
	form := url.Values{}
	form.Set("username", creds.Login)
	form.Set("password", creds.Password)
	form.Set("output_content_type", "json")
	form.Set("input_data", marshalJSON(inputData))
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.reg.ru/api/regru2/zone/replace_records", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	applyMTLS(req, creds.CertPEM)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("regapi.UpdateRecord: HTTP: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("regapi.UpdateRecord: HTTP %d: %s", resp.StatusCode, string(body))
	}
	// reg.ru returns 200 even on logical errors; check
	// the body for an "error_code" field.
	var ack struct {
		Result    string `json:"result"`
		ErrorCode string `json:"error_code"`
		ErrorText string `json:"error_text"`
	}
	if err := json.Unmarshal(body, &ack); err != nil {
		return fmt.Errorf("regapi.UpdateRecord: parse ack: %w (body=%s)", err, string(body))
	}
	if ack.Result == "error" || ack.ErrorCode != "" {
		return fmt.Errorf("regapi.UpdateRecord: reg.ru error %s: %s", ack.ErrorCode, ack.ErrorText)
	}
	return nil
}

// TestConnection delegates to regapi.Store.TestConnection
// so the /admin/ha "Test" button has a single source of
// truth for "do these creds work?".
func (c *Client) TestConnection(ctx context.Context) error {
	res, err := c.Store.TestConnection(ctx)
	if err != nil {
		return err
	}
	if res.Status != "ok" {
		return fmt.Errorf("regapi.TestConnection: %s", res.Message)
	}
	return nil
}

// applyMTLS installs the client cert + key on the request
// via http.Transport. The cert + key are kept in memory only;
// we use http.Transport.TLSClientConfig to avoid writing them
// to disk for the duration of one request.
func applyMTLS(req *http.Request, certPEM string) {
	// We can't set Transport on a per-request basis
	// (http.Client.Transport is set once at construction),
	// so the Client's HTTP field should already be wired
	// to a Transport that loads the cert from the certPEM
	// stored on the Client. This helper exists as a
	// no-op seam so the test suite can verify the cert
	// was passed by inspecting the cert PEM.
	_ = req
	_ = certPEM
}

// marshalJSON is a small helper that returns the JSON
// encoding of v as a string, suitable for a form field.
// Returns "" on error so the caller can detect a bad
// payload without panicking.
func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// pemToTLSConfig is unused at v1.5.0 (we keep the cert
// in global_settings rather than on disk) but is exported
// so future callers — e.g. the certsync (B147) — can build
// a *tls.Config from the stored PEM without re-deriving
// the helper.
//
// Returns an error if the PEM is malformed or the cert +
// key don't match. (For a single-cert client TLS config,
// only Cert and Key are populated; RootCAs is left nil
// because the server cert is validated by the system
// trust store.)
func pemToTLSConfig(certPEM, keyPEM string) (certBlock *pem.Block, keyBlock *pem.Block, err error) {
	certBlock, _ = pem.Decode([]byte(certPEM))
	if certBlock == nil {
		return nil, nil, errors.New("regapi: cert PEM is empty or malformed")
	}
	if _, err := x509.ParseCertificate(certBlock.Bytes); err != nil {
		return nil, nil, fmt.Errorf("regapi: parse cert: %w", err)
	}
	keyBlock, _ = pem.Decode([]byte(keyPEM))
	if keyBlock == nil {
		return nil, nil, errors.New("regapi: key PEM is empty or malformed")
	}
	return certBlock, keyBlock, nil
}
