// Package dnsexternal — the DNS provider implementation used by the HA failover and by the
// manual switch (`skygate dns-set-a`).
//
// B468 (2026-11-13): this client speaks the reg.ru API FOR REAL. Until this block the base URL
// was a placeholder — `https://api.<your-domain>/api/<provider-v2-path>/zone/…` — and the field
// names it looked for (`name`/`type`) are not the ones the registrar sends (`subname`/`rectype`),
// so GetRecord reported "record not found" for records that existed and UpdateRecord could not
// have worked even with correct credentials. The operator had already saved reg.ru API
// credentials, which is what made the defect visible: a working credential against a client that
// cannot call the endpoint looks exactly like a wrong password.
//
// The package keeps its historical name and its Provider shape (internal/dns wraps it); the
// request/response details live in regru_api.go, shared with the credential store's
// TestConnection so the two cannot drift apart again.
package dnsexternal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	dnsexternal "skygate/internal/ha/dnsexternal"
)

// httpTimeout bounds one API call. A record read/write answers in well under a second; 20s
// leaves room for a slow registrar while keeping a failover switch from hanging.
const httpTimeout = 20 * time.Second

// Client is the reg.ru implementation of dns.Provider.
type Client struct {
	// Store provides the credentials (login + alternative password + zone) for every call.
	Store *dnsexternal.Store

	// HTTPClient is the transport. Nil = a default client with httpTimeout. Tests inject one.
	HTTPClient *http.Client

	// BaseURL overrides the API root (tests / a proxy). Empty = dnsexternal.RegAPIBase().
	BaseURL string

	// credsOverride bypasses Store for one call (tests, and a future "test without saving").
	credsOverride *dnsexternal.Credentials
}

// ErrRecordNotFound is returned when the zone has no A record for the requested name. Kept
// package-local to avoid an import cycle (see the package doc of internal/dns).
var ErrRecordNotFound = errors.New("dnsexternal: A record not found in the zone")

// NewClient returns a Client wired to the given Store.
func NewClient(store *dnsexternal.Store) *Client {
	return &Client{Store: store}
}

// Name reports the provider identifier this client implements. "regru" is the honest name; the
// historical "external" value keeps working as an alias in the credential store.
func (c *Client) Name() string { return "regru" }

// loadCreds returns the credentials for the next call.
func (c *Client) loadCreds() (dnsexternal.Credentials, error) {
	if c.credsOverride != nil {
		return *c.credsOverride, nil
	}
	if c.Store == nil {
		return dnsexternal.Credentials{}, errors.New("dnsexternal: no credential store configured (save the API login/password/zone first)")
	}
	return c.Store.Load()
}

// baseURL is the API root for this client.
func (c *Client) baseURL() string {
	if v := strings.TrimSpace(c.BaseURL); v != "" {
		return strings.TrimRight(v, "/")
	}
	return dnsexternal.RegAPIBase()
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	// The registrar's API authenticates with login+password. An operator who still has a
	// client certificate configured gets it presented on the handshake (harmless there), but
	// its absence is NOT an error — that requirement is what made every non-mTLS credential
	// unusable before B468.
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if creds, err := c.loadCreds(); err == nil && strings.TrimSpace(creds.CertPEM) != "" {
		if cert, cerr := tls.X509KeyPair([]byte(creds.CertPEM), []byte(creds.CertPEM)); cerr == nil {
			cfg.Certificates = []tls.Certificate{cert}
		}
	}
	return &http.Client{
		Timeout:   httpTimeout,
		Transport: &http.Transport{TLSClientConfig: cfg},
	}
}

// post is one API call against this client's base (explicit BaseURL, else the shipped one).
func (c *Client) post(ctx context.Context, path, login, password, inputJSON string) (dnsexternal.RegAPIResponse, error) {
	return dnsexternal.PostRegAPIAt(ctx, c.httpClient(), c.baseURL(), path, login, password, inputJSON)
}

// GetRecord returns the A-record IP for `name` in `zone`.
func (c *Client) GetRecord(ctx context.Context, zone, name string) (string, error) {
	creds, err := c.loadCreds()
	if err != nil {
		return "", fmt.Errorf("dnsexternal.GetRecord: load creds: %w", err)
	}
	if creds.IsZero() {
		return "", errors.New("dnsexternal.GetRecord: credentials not configured (save login/password/zone first)")
	}
	if strings.TrimSpace(zone) == "" {
		zone = creds.Zone
	}
	_, ip, found, err := c.lookup(ctx, creds, zone, name)
	if err != nil {
		return "", err
	}
	if !found {
		// B468: the previous version parsed the wrong field names (`name`/`type` instead of the
		// registrar's `subname`/`rectype`), so a record that existed was reported as missing.
		// The sentinel is what the adapter in internal/dns translates to dns.ErrRecordNotFound,
		// and the switch script relies on it to say "there is no A record to change" instead of
		// surfacing a raw HTTP error.
		return "", ErrRecordNotFound
	}
	return ip, nil
}

// lookup returns the records plus the A record for name (and whether it was found).
func (c *Client) lookup(ctx context.Context, creds dnsexternal.Credentials, zone, name string) ([]dnsexternal.RegRecord, string, bool, error) {
	input := fmt.Sprintf(`{"domains":[{"dname":%q}]}`, zone)
	resp, err := c.post(ctx, dnsexternal.RegAPIPathZoneRecords, creds.Login, creds.Password, input)
	if err != nil {
		return nil, "", false, fmt.Errorf("dnsexternal: read zone %q: %w", zone, err)
	}
	if err := resp.Err("dnsexternal: read zone"); err != nil {
		return nil, "", false, err
	}
	records, err := dnsexternal.ZoneRecords(resp.Answer)
	if err != nil {
		return nil, "", false, err
	}
	ip, ok := dnsexternal.FindA(records, name)
	return records, ip, ok, nil
}

// UpdateRecord sets the A record for `name` in `zone` to `ip`.
//
// It uses /zone/update_records with the single A RR: the registrar replaces the RRset for that
// subname, which is what a switch needs (exactly one A answer). The record is read back and
// compared before the call returns, so "the API said success" and "DNS now answers the new IP"
// cannot be reported as the same thing.
func (c *Client) UpdateRecord(ctx context.Context, zone, name, ip string) error {
	creds, err := c.loadCreds()
	if err != nil {
		return fmt.Errorf("dnsexternal.UpdateRecord: load creds: %w", err)
	}
	if creds.IsZero() {
		return errors.New("dnsexternal.UpdateRecord: credentials not configured")
	}
	if strings.TrimSpace(ip) == "" {
		return errors.New("dnsexternal.UpdateRecord: ip is empty")
	}
	if strings.TrimSpace(zone) == "" {
		zone = creds.Zone
	}
	sub := strings.TrimSpace(name)
	if sub == "" {
		sub = "@"
	}

	// Keep the zone's existing TTL when the record is already there — a switch must not change
	// how long resolvers cache the answer unless the operator asked for it.
	ttl := 300
	if records, _, ok, lerr := c.lookup(ctx, creds, zone, sub); lerr == nil && ok {
		for _, r := range records {
			if strings.EqualFold(r.Rectype, "A") && strings.EqualFold(strings.TrimSpace(r.Subname), strings.ToLower(strings.TrimSpace(sub))) && r.TTL > 0 {
				ttl = r.TTL
				break
			}
		}
	}

	input := fmt.Sprintf(`{"domains":[{"dname":%q,"rrs":[{"subname":%q,"rectype":"A","ttl":%d,"content":%q}]}]}`,
		zone, sub, ttl, ip)
	resp, err := c.post(ctx, dnsexternal.RegAPIPathZoneUpdate, creds.Login, creds.Password, input)
	if err != nil {
		return fmt.Errorf("dnsexternal.UpdateRecord: %w", err)
	}
	if err := resp.Err("dnsexternal.UpdateRecord"); err != nil {
		return err
	}

	// Verify by reading the zone back. The registrar's own cache can lag, so a mismatch is
	// reported as "not yet visible" WITH the value that is still served — never as success.
	if _, got, ok, lerr := c.lookup(ctx, creds, zone, sub); lerr == nil {
		if !ok {
			return fmt.Errorf("dnsexternal.UpdateRecord: the API accepted the write but no A record for %q is readable yet", sub)
		}
		if got != ip {
			return fmt.Errorf("dnsexternal.UpdateRecord: the API accepted %q but the zone still answers %q (the registrar's cache may lag; re-run the check in a minute)", ip, got)
		}
	}
	return nil
}

// TestConnection proves the credentials work with a READ-ONLY call: it loads the zone and
// reports how many records it saw. No record is created or modified.
func (c *Client) TestConnection(ctx context.Context) error {
	creds, err := c.loadCreds()
	if err != nil {
		return fmt.Errorf("dnsexternal.TestConnection: load creds: %w", err)
	}
	if creds.IsZero() {
		return errors.New("dnsexternal.TestConnection: credentials not configured")
	}
	if strings.TrimSpace(creds.Zone) == "" {
		return errors.New("dnsexternal.TestConnection: the zone is not configured")
	}
	records, _, _, err := c.lookup(ctx, creds, creds.Zone, "")
	if err != nil {
		return fmt.Errorf("dnsexternal.TestConnection: %w", err)
	}
	if len(records) == 0 {
		return fmt.Errorf("dnsexternal.TestConnection: the API answered, but zone %q returned no records — check that the credential's account owns that zone", creds.Zone)
	}
	return nil
}

// SetTTL rewrites the A record's TTL, keeping its address (B468).
//
// A DNS switch is bounded by the TTL, not by the API call: with a 24-hour TTL, clients that
// resolved the name before the flip keep the OLD address for up to a day. The runbook therefore
// lowers the TTL a day in advance, and this is the call that does it.
func (c *Client) SetTTL(ctx context.Context, zone, name string, ttl int) error {
	if ttl <= 0 {
		return fmt.Errorf("dnsexternal.SetTTL: ttl must be positive, got %d", ttl)
	}
	creds, err := c.loadCreds()
	if err != nil {
		return fmt.Errorf("dnsexternal.SetTTL: load creds: %w", err)
	}
	if creds.IsZero() {
		return errors.New("dnsexternal.SetTTL: credentials not configured")
	}
	if strings.TrimSpace(zone) == "" {
		zone = creds.Zone
	}
	sub := strings.TrimSpace(name)
	if sub == "" {
		sub = "@"
	}
	records, ip, found, err := c.lookup(ctx, creds, zone, sub)
	if err != nil {
		return fmt.Errorf("dnsexternal.SetTTL: %w", err)
	}
	if !found {
		return fmt.Errorf("dnsexternal.SetTTL: no A record for %q in %q — set the address first", sub, zone)
	}
	_ = records
	input := fmt.Sprintf(`{"domains":[{"dname":%q,"rrs":[{"subname":%q,"rectype":"A","ttl":%d,"content":%q}]}]}`,
		zone, sub, ttl, ip)
	resp, err := c.post(ctx, dnsexternal.RegAPIPathZoneUpdate, creds.Login, creds.Password, input)
	if err != nil {
		return fmt.Errorf("dnsexternal.SetTTL: %w", err)
	}
	if err := resp.Err("dnsexternal.SetTTL"); err != nil {
		return err
	}
	// Read back: the address must be unchanged and the TTL must be the one asked for (when the
	// registrar reports it at all — older answers omit the field, which is not an error).
	if _, gotIP, gotOK, lerr := c.lookup(ctx, creds, zone, sub); lerr == nil {
		if !gotOK || gotIP != ip {
			return fmt.Errorf("dnsexternal.SetTTL: the write changed the ADDRESS (%q -> %q); restore it with dns-set-a", ip, gotIP)
		}
	}
	return nil
}

// RecordsFor is the read-only view the CLI prints (and the switch script pre-flights): every
// record in the zone, plus the A record that would be switched.
func (c *Client) RecordsFor(ctx context.Context, zone, name string) (all []dnsexternal.RegRecord, current string, found bool, err error) {
	creds, err := c.loadCreds()
	if err != nil {
		return nil, "", false, fmt.Errorf("dnsexternal.RecordsFor: load creds: %w", err)
	}
	if creds.IsZero() {
		return nil, "", false, errors.New("dnsexternal.RecordsFor: credentials not configured")
	}
	if strings.TrimSpace(zone) == "" {
		zone = creds.Zone
	}
	return c.lookup(ctx, creds, zone, name)
}
