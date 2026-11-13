// regru_api.go — the ONE description of how the reg.ru API is spoken.
//
// WHY THIS FILE EXISTS (B468, 2026-11-13). Before this block the API base URL was a
// PLACEHOLDER in the shipped client — literally
//
//	https://api.<your-domain>/api/<provider-v2-path>/zone/get_resource_records
//
// — and the shell live-test built `https://api.${SKYGATE_DNS_REGAPI_ZONE}/api/regru2`, i.e. it
// interpolated the DNS ZONE (skynas.ru) into the HOST, asking for api.skynas.ru. Neither could
// ever reach the registrar, so the credentials the operator saved could not be used by any code
// path, and the failure looked like a credential problem ("NO_AUTH"/resolution error) instead of
// an unimplemented client.
//
// The real endpoints live under https://api.reg.ru/api/regru2/. This file holds the base URL,
// the form-encoded request shape and the response/error parsing, because TWO packages have to
// agree on all three: internal/dnsexternal (the dns.Provider implementation) and this package's
// Store.TestConnection (which /admin/ha renders). Splitting them is how the previous drift
// happened. SKYGATE_REGAPI_BASE_URL overrides the base for tests and for a private/staging CA
// endpoint.
package dnsexternal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// RegAPIBaseURL is the registrar's API root. The zone is passed INSIDE input_data, never in the
// host — that inversion is the bug this constant exists to prevent.
const RegAPIBaseURL = "https://api.reg.ru/api/regru2"

// RegAPIBaseURLEnv overrides the base URL (tests, a proxy, a staging endpoint).
const RegAPIBaseURLEnv = "SKYGATE_REGAPI_BASE_URL"

// RegAPIBase returns the base URL to use: the env override, else the shipped endpoint.
func RegAPIBase() string {
	if v := strings.TrimSpace(os.Getenv(RegAPIBaseURLEnv)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return RegAPIBaseURL
}

// RegAPIEndpoints, so a caller cannot invent a third spelling of a path.
const (
	RegAPIPathZoneRecords = "/zone/get_resource_records"
	RegAPIPathZoneUpdate  = "/zone/update_records"
	RegAPIPathUserNop     = "/user/nop"
)

// RegAPIResponse is the envelope every reg.ru call answers with. HTTP 200 with
// `"result":"error"` is the normal failure mode, so the ack — not the status code — decides.
type RegAPIResponse struct {
	Result    string          `json:"result"`
	ErrorCode string          `json:"error_code"`
	ErrorText string          `json:"error_text"`
	Answer    json.RawMessage `json:"answer"`
}

// OK reports whether the call succeeded.
func (r RegAPIResponse) OK() bool {
	if strings.EqualFold(r.Result, "error") {
		return false
	}
	// Older responses carry only error_code/error_text; treat a non-zero code as a failure.
	switch strings.TrimSpace(r.ErrorCode) {
	case "", "0":
		return true
	}
	return false
}

// Err renders the registrar's own words, so "the API refused" is never all the operator gets.
func (r RegAPIResponse) Err(op string) error {
	if r.OK() {
		return nil
	}
	code := strings.TrimSpace(r.ErrorCode)
	text := strings.TrimSpace(r.ErrorText)
	if text == "" {
		text = "(no error_text)"
	}
	if code == "" {
		return fmt.Errorf("%s: the registrar refused: %s", op, text)
	}
	return fmt.Errorf("%s: the registrar refused (%s): %s", op, code, text)
}

// PostRegAPI performs one form-encoded call against the shipped (or env-overridden) base.
func PostRegAPI(ctx context.Context, client *http.Client, path, login, password, inputJSON string) (RegAPIResponse, error) {
	return PostRegAPIAt(ctx, client, RegAPIBase(), path, login, password, inputJSON)
}

// PostRegAPIAt performs one form-encoded call against an explicit base (tests, a proxy).
// username/password go at the TOP level (putting them inside input_data is the "NO_AUTH" trap
// this project already paid for); input_data carries the JSON body.
func PostRegAPIAt(ctx context.Context, client *http.Client, base, path, login, password, inputJSON string) (RegAPIResponse, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(login) == "" || strings.TrimSpace(password) == "" {
		return RegAPIResponse{}, fmt.Errorf("regapi: login and password are both required")
	}
	form := url.Values{}
	form.Set("username", login)
	form.Set("password", password)
	form.Set("input_format", "json")
	form.Set("output_format", "json")
	form.Set("output_content_type", "plain")
	form.Set("input_data", inputJSON)

	endpoint := strings.TrimRight(base, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return RegAPIResponse{}, fmt.Errorf("regapi: build request for %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return RegAPIResponse{}, fmt.Errorf("regapi: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return RegAPIResponse{}, fmt.Errorf("regapi: POST %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out RegAPIResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return RegAPIResponse{}, fmt.Errorf("regapi: parse %s response: %w (body starts %q)", path, err, firstLine(body))
	}
	return out, nil
}

// firstLine keeps a parse error readable (the body can be one long line).
func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// RegRecord is one DNS record as the registrar reports it. The field names are reg.ru's
// (`subname` / `rectype`), which is exactly what the old client got wrong: it looked for
// `name` / `type`, matching nothing and reporting "record not found" for a record that was
// right there.
//
// The alternate spellings are accepted as well (`name` / `type`): one registrar vintage shipped
// them, and a record list that silently parses as empty is a failover that silently stops
// switching DNS. recordName/recordType pick whichever the answer carried.
type RegRecord struct {
	Subname string `json:"subname,omitempty"`
	Rectype string `json:"rectype,omitempty"`
	Content string `json:"content,omitempty"`
	TTL     int    `json:"ttl,omitempty"`
	Prio    int    `json:"prio,omitempty"`

	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// recordName is the subdomain this record belongs to, in whichever spelling the answer used.
func (r RegRecord) recordName() string {
	if strings.TrimSpace(r.Subname) != "" {
		return strings.TrimSpace(r.Subname)
	}
	return strings.TrimSpace(r.Name)
}

// recordType is the record type (A, MX, TXT…) in whichever spelling the answer used.
func (r RegRecord) recordType() string {
	if strings.TrimSpace(r.Rectype) != "" {
		return strings.TrimSpace(r.Rectype)
	}
	return strings.TrimSpace(r.Type)
}

// ZoneRecords extracts the record list from a get_resource_records answer. Both shapes the
// registrar has shipped are accepted (`answer.domains[].rrs[]` and the older
// `answer.domains[].records[]`), because a provider that silently stops seeing records is a
// failover that silently stops switching DNS.
func ZoneRecords(raw json.RawMessage) ([]RegRecord, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var answer struct {
		Domains []struct {
			Dname   string      `json:"dname"`
			RRS     []RegRecord `json:"rrs"`
			Records []RegRecord `json:"records"`
		} `json:"domains"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("regapi: parse zone records: %w", err)
	}
	var out []RegRecord
	for _, d := range answer.Domains {
		out = append(out, d.RRS...)
		out = append(out, d.Records...)
	}
	return out, nil
}

// FindA returns the content of the A record for subname ("" or "@" means the zone apex).
func FindA(records []RegRecord, subname string) (string, bool) {
	want := strings.ToLower(strings.TrimSpace(subname))
	for _, r := range records {
		if !strings.EqualFold(r.recordType(), "A") {
			continue
		}
		have := strings.ToLower(r.recordName())
		if want == "@" || want == "" {
			if have == "" || have == "@" {
				return strings.TrimSpace(r.Content), true
			}
			continue
		}
		if have == want {
			return strings.TrimSpace(r.Content), true
		}
	}
	return "", false
}
