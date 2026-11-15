// cloudflare_b474.go — B474: the first provider that is NOT reg.ru, and therefore the first proof that
// the B473 seam actually is one.
//
// WHAT THIS ADAPTER IS FOR. B473 introduced `dns.Provider` + a field registry + `dns_provider_creds`
// rows, and left exactly one provider in the registry, so the seam was only ever exercised by the
// provider it was extracted FROM. Cloudflare is the honest test: it authenticates with an API TOKEN
// (no login, no alternative password, no zone in the credential), and — unlike the shipped reg.ru
// client — it really can do AAAA, TXT and TTL. If the seam were fake, this file could not exist without
// touching the storage, the template or the save path. It touches none of them: one adapter here, one
// Spec in the registry, one `case` in BuildProvider.
//
// THE SHAPE OF THE API (v4). Every response is an envelope: `{"success":bool,"errors":[{code,message}],
// "result":…}`, so a 200 with `success:false` is a failure and must be reported as one — the mistake
// that makes a panel say "ok" while nothing changed. The zone is addressed by an ID that must be looked
// up from its NAME, which is why `zoneID` exists and why its error is worth reading: an API token
// scoped to "Zone.DNS:Edit" for one zone cannot LIST zones, and the honest answer to that is to name
// the required permission instead of a bare 403.
//
// Records are addressed by their own ids, so an update is read-modify-write: find the record, PATCH it.
// A missing record is NOT an error for UpdateRecord — the HA failover treats "no record yet" as "create
// it" — and for DeleteTXT a missing record is success, because DNS-01 cleanup is retried and a second
// delete must not fail the run.
package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// cloudflareBaseURL is the production endpoint. `BuildDeps.BaseURL` overrides it (the tests point it at
// an httptest server; an operator behind an egress proxy could point it at a mirror).
const cloudflareBaseURL = "https://api.cloudflare.com/client/v4"

// cloudflareTokenEnv is the environment fallback for an install that has not saved a credential row
// yet. The panel path writes a row and never reads this; it exists so the CLI/bootstrap can test a
// token before anything is stored.
const cloudflareTokenEnv = "SKYGATE_CLOUDFLARE_TOKEN"

// cloudflareMaxPages bounds the pagination of ListRecords. A zone with more than 100×50 records is not
// what "read the zone" is for, and an unbounded loop against a rate-limited API is how a page load
// becomes an incident.
const cloudflareMaxPages = 50

// cloudflareTTLsAllowed is the set the API accepts. Sending 45 gets a 400 whose text talks about
// "900, 1800, 3600 …", which reads like a bug in the panel; validating here names the real rule.
var cloudflareTTLsAllowed = []int{1, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400}

// cloudflareProvider is the Cloudflare v4 API behind the dns.Provider contract.
type cloudflareProvider struct {
	token   string
	baseURL string
	http    *http.Client
}

// newCloudflareProvider builds the adapter. A nil client gets a bounded one: an unbounded HTTP client
// against an API that can answer slowly is how a background failover hangs forever.
func newCloudflareProvider(token, baseURL string, hc *http.Client) *cloudflareProvider {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = cloudflareBaseURL
	}
	return &cloudflareProvider{token: strings.TrimSpace(token), baseURL: strings.TrimRight(baseURL, "/"), http: hc}
}

func (p *cloudflareProvider) Name() string { return cloudflareSpec.Name }

// Capabilities — Cloudflare really does all four, unlike the shipped reg.ru client. The registry is the
// single source, so the panel's badges and this answer cannot disagree.
func (p *cloudflareProvider) Capabilities() Capabilities { return cloudflareSpec.Caps }

// ---------- transport ----------

// cfEnvelope is the v4 response envelope. `success:false` with HTTP 200 is a normal Cloudflare answer
// (a permission error, a validation error), so it is decoded and reported, never treated as success.
type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (e cfEnvelope) message() string {
	if len(e.Errors) == 0 {
		return "no error detail in the response"
	}
	parts := make([]string, 0, len(e.Errors))
	for _, err := range e.Errors {
		parts = append(parts, fmt.Sprintf("%d: %s", err.Code, strings.TrimSpace(err.Message)))
	}
	return strings.Join(parts, "; ")
}

// call performs one API request and decodes the envelope's `result` into out (when out is non-nil).
func (p *cloudflareProvider) call(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("cloudflare: marshal request: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("cloudflare: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("cloudflare: read response: %w", err)
	}
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("cloudflare: %s %s: HTTP %d, unreadable body: %w", method, path, resp.StatusCode, err)
	}
	// The status code and the envelope are checked TOGETHER: a 403 whose body says
	// "Authentication error" must name the token, and a 200 with success:false must not pass.
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("cloudflare: HTTP %d — the API token is not accepted (%s); it needs Zone:Read and Zone.DNS:Edit for this zone", resp.StatusCode, env.message())
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("cloudflare: HTTP 404 — %s (%s)", path, env.message())
	case resp.StatusCode >= 400:
		return fmt.Errorf("cloudflare: HTTP %d: %s", resp.StatusCode, env.message())
	case !env.Success:
		return fmt.Errorf("cloudflare: the API answered success=false: %s", env.message())
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("cloudflare: decode result: %w", err)
		}
	}
	return nil
}

// ---------- addressing ----------

// cfZone is the subset of a zone object this adapter uses.
type cfZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// cfRecord is the subset of a DNS record object this adapter uses.
type cfRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

// zoneID resolves the zone NAME to the API's zone id.
//
// The error is deliberately specific: a token scoped to one zone usually cannot list zones, and the
// generic "403 Forbidden" sends the operator looking for a network problem.
func (p *cloudflareProvider) zoneID(ctx context.Context, zone string) (string, error) {
	zone = strings.ToLower(strings.TrimSpace(zone))
	if zone == "" {
		return "", fmt.Errorf("cloudflare: the zone is required (the domain the account manages, e.g. example.com)")
	}
	var zones []cfZone
	if err := p.call(ctx, http.MethodGet, "/zones?per_page=50&name="+zone, nil, &zones); err != nil {
		return "", err
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, zone) && z.ID != "" {
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("cloudflare: the token cannot see the zone %q — check that the zone is on this Cloudflare account and that the token includes it (Zone:Read)", zone)
}

// fqdn builds the record name the API expects from the interface's bare `name`.
func cfFQDN(name, zone string) string {
	name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(name), "."))
	zone = strings.ToLower(strings.TrimSpace(zone))
	switch {
	case name == "" || name == "@":
		return zone
	case strings.EqualFold(name, zone), strings.HasSuffix(strings.ToLower(name), "."+zone):
		return strings.ToLower(name)
	}
	return strings.ToLower(name + "." + zone)
}

// cfShortName strips the zone suffix, so the panel's summary matches a name the operator typed.
func cfShortName(fqdn, zone string) string {
	f := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(fqdn), "."))
	z := strings.ToLower(strings.TrimSpace(zone))
	if f == z {
		return ""
	}
	return strings.TrimSuffix(f, "."+z)
}

// listByType returns the records of one type at one name (empty when there are none).
func (p *cloudflareProvider) listByType(ctx context.Context, zone, name, typ string) ([]cfRecord, error) {
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return nil, err
	}
	q := "/zones/" + id + "/dns_records?per_page=100&name=" + cfFQDN(name, zone)
	if typ != "" {
		q += "&type=" + typ
	}
	var recs []cfRecord
	if err := p.call(ctx, http.MethodGet, q, nil, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// ---------- the contract ----------

// GetRecord reads the A record's content. "No record" is ErrRecordNotFound (a fresh deploy), never a
// zero-value address.
func (p *cloudflareProvider) GetRecord(ctx context.Context, zone, name string) (string, error) {
	recs, err := p.listByType(ctx, zone, name, "A")
	if err != nil {
		return "", err
	}
	for _, r := range recs {
		if strings.EqualFold(r.Type, "A") && strings.TrimSpace(r.Content) != "" {
			return strings.TrimSpace(r.Content), nil
		}
	}
	return "", ErrRecordNotFound
}

// UpdateRecord points the A record at `ip`, creating it when it does not exist yet.
//
// A record that is PROXIED is not a usable target: Cloudflare answers with its own edge addresses and
// the origin address is invisible to clients, so an HA switch would appear to succeed while nothing
// moved. That is reported instead of silently rewriting `content` under the orange cloud.
func (p *cloudflareProvider) UpdateRecord(ctx context.Context, zone, name, ip string) error {
	if net.ParseIP(strings.TrimSpace(ip)) == nil {
		return fmt.Errorf("cloudflare: %q is not an IP address", ip)
	}
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	recs, err := p.listByType(ctx, zone, name, "A")
	if err != nil {
		return err
	}
	body := map[string]any{"type": "A", "name": cfFQDN(name, zone), "content": strings.TrimSpace(ip), "ttl": 1}
	if len(recs) == 0 {
		if err := p.call(ctx, http.MethodPost, "/zones/"+id+"/dns_records", body, nil); err != nil {
			return err
		}
		return nil
	}
	for _, r := range recs {
		if r.Proxied {
			return fmt.Errorf("cloudflare: the A record %s is PROXIED (orange cloud) — Cloudflare answers with its own addresses, so an address switch would have no visible effect; unproxy the record first", cfFQDN(name, zone))
		}
	}
	// Only the content is patched: the record keeps its own TTL (which the switch procedure lowers
	// separately with SetTTL) and its id.
	rec := recs[0]
	if err := p.call(ctx, http.MethodPatch, "/zones/"+id+"/dns_records/"+rec.ID, map[string]any{"content": strings.TrimSpace(ip)}, nil); err != nil {
		return err
	}
	return nil
}

// SetTTL lowers (or raises) the A record's TTL — the switch runbook's "lower it a day before". The value
// is validated against Cloudflare's own accepted set, because a 400 naming an internal code is not an
// answer an operator can act on.
func (p *cloudflareProvider) SetTTL(ctx context.Context, zone, name string, ttl int) error {
	if !p.Capabilities().TTL {
		return ErrUnsupported
	}
	if !cfTTLAllowed(ttl) {
		return fmt.Errorf("cloudflare: ttl %d is not accepted; allowed: %v", ttl, cloudflareTTLsAllowed)
	}
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	recs, err := p.listByType(ctx, zone, name, "A")
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return ErrRecordNotFound
	}
	for _, r := range recs {
		if err := p.call(ctx, http.MethodPatch, "/zones/"+id+"/dns_records/"+r.ID, map[string]any{"ttl": ttl}, nil); err != nil {
			return err
		}
	}
	return nil
}

// cfTTLAllowed reports whether Cloudflare accepts the value.
func cfTTLAllowed(ttl int) bool {
	for _, v := range cloudflareTTLsAllowed {
		if v == ttl {
			return true
		}
	}
	return false
}

// cfResultInfo is the pagination block of a list answer.
type cfResultInfo struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
	Count      int `json:"count"`
}

// ListRecords reads the whole zone, paginated, newest page last. Names are returned SHORT (without the
// zone suffix) so they match what the operator typed in the panel's names field.
func (p *cloudflareProvider) ListRecords(ctx context.Context, zone string) ([]Record, error) {
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for page := 1; page <= cloudflareMaxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("%s/zones/%s/dns_records?per_page=100&page=%d", p.baseURL, id, page), nil)
		if err != nil {
			return nil, fmt.Errorf("cloudflare: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+p.token)
		req.Header.Set("Accept", "application/json")
		resp, err := p.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("cloudflare: list records: %w", err)
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, fmt.Errorf("cloudflare: read response: %w", rerr)
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("cloudflare: list records: HTTP %d", resp.StatusCode)
		}
		var envelope struct {
			Success    bool            `json:"success"`
			Result     []cfRecord      `json:"result"`
			ResultInfo cfResultInfo    `json:"result_info"`
			Errors     json.RawMessage `json:"errors"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("cloudflare: decode records: %w", err)
		}
		if !envelope.Success {
			return nil, fmt.Errorf("cloudflare: list records answered success=false")
		}
		for _, r := range envelope.Result {
			out = append(out, Record{
				Name: cfShortName(r.Name, zone), Type: strings.ToUpper(r.Type),
				Content: strings.TrimSpace(r.Content), TTL: r.TTL,
			})
		}
		if envelope.ResultInfo.TotalPages <= page || len(envelope.Result) == 0 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Type < out[j].Type
	})
	return out, nil
}

// CreateTXT writes the DNS-01 challenge record. Multiple TXT records may share one name (one per
// challenge), which is why nothing is deleted here — DeleteTXT removes the exact value.
func (p *cloudflareProvider) CreateTXT(ctx context.Context, zone, name, value string, ttl int) error {
	if !p.Capabilities().TXT {
		return ErrUnsupported
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("cloudflare: the TXT value is empty")
	}
	if ttl <= 0 {
		ttl = 60 // the shortest TTL Cloudflare accepts: a challenge record must not outlive the order
	}
	if !cfTTLAllowed(ttl) {
		ttl = 60
	}
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	body := map[string]any{"type": "TXT", "name": cfFQDN(name, zone), "content": strings.TrimSpace(value), "ttl": ttl}
	return p.call(ctx, http.MethodPost, "/zones/"+id+"/dns_records", body, nil)
}

// DeleteTXT removes the TXT records at `name` whose content equals `value`.
//
// "Nothing to delete" is SUCCESS: a DNS-01 cleanup is retried, and a second delete that failed would
// turn a finished order into a failed one. Records with a different value are left alone — they may
// belong to another challenge in the same zone.
func (p *cloudflareProvider) DeleteTXT(ctx context.Context, zone, name, value string) error {
	if !p.Capabilities().TXT {
		return ErrUnsupported
	}
	id, err := p.zoneID(ctx, zone)
	if err != nil {
		return err
	}
	recs, err := p.listByType(ctx, zone, name, "TXT")
	if err != nil {
		return err
	}
	want := strings.TrimSpace(value)
	for _, r := range recs {
		if want != "" && strings.TrimSpace(r.Content) != want {
			continue
		}
		if err := p.call(ctx, http.MethodDelete, "/zones/"+id+"/dns_records/"+r.ID, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// TestConnection verifies the TOKEN itself. `/user/tokens/verify` works for every token type and needs
// no zone, which is what the contract offers here; a zone-scoped permission problem surfaces on the
// first real read, where it can name the zone.
func (p *cloudflareProvider) TestConnection(ctx context.Context) error {
	if p.token == "" {
		return fmt.Errorf("cloudflare: no API token configured (save one on /admin/ha, provider «Cloudflare») or set %s", cloudflareTokenEnv)
	}
	var res struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := p.call(ctx, http.MethodGet, "/user/tokens/verify", nil, &res); err != nil {
		return err
	}
	if !strings.EqualFold(res.Status, "active") {
		return fmt.Errorf("cloudflare: the token is %q, not active", res.Status)
	}
	return nil
}

// cloudflareTokenFromEnv is the documented fallback (see cloudflareTokenEnv).
func cloudflareTokenFromEnv() string { return strings.TrimSpace(os.Getenv(cloudflareTokenEnv)) }

// errCloudflareNoToken is the actionable message for a missing credential.
func errCloudflareNoToken() error {
	return errors.New("dns: the cloudflare provider needs an API token — save one on /admin/ha (provider «Cloudflare (API token)») or set " + cloudflareTokenEnv)
}

// Compile-time proof that the adapter satisfies the contract (a missing method must fail HERE, not in
// the middle of a failover).
var _ Provider = (*cloudflareProvider)(nil)
