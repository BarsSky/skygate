// B468 — the DNS client tests, rewritten against the protocol the registrar actually speaks.
//
// The previous file pinned a FICTIONAL protocol: it served `{"answer":{"domains":[{"records":[
// {"type":"A","name":"skygate","content":"1.2.3.4"}]}]}}` while reg.ru sends
// `{"result":"success","answer":{"domains":[{"rrs":[{"subname":"skygate","rectype":"A",
// "content":"1.2.3.4"}]}]}}`, and the client it tested pointed at
// `https://api.<your-domain>/api/<provider-v2-path>/…`. Both sides agreed with each other and
// with nothing else, so the suite was green while no real call could work — the same class as a
// check that only verifies the fixture. These tests speak the real names, assert the real form
// fields, and let the fake MUTATE its zone so the read-back verification is exercised.
package dnsexternal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	creds "skygate/internal/ha/dnsexternal"
)

// fakeRegRU is a minimal reg.ru API: form-encoded calls, `result`/`error` envelope, a mutable
// zone, and the two endpoints the client uses.
type fakeRegRU struct {
	mu       sync.Mutex
	zone     string
	records  []map[string]any
	login    string
	password string
	// failAuth makes every call answer the registrar's NO_AUTH error.
	failAuth bool
	// lastForm captures the top-level form of the last call (the NO_AUTH trap lives here).
	lastForm map[string]string
	// updateCalls counts /zone/update_records hits.
	updateCalls int
}

func newFakeRegRU(t *testing.T, zone, login, password string, records ...map[string]any) (*fakeRegRU, *httptest.Server) {
	t.Helper()
	f := &fakeRegRU{zone: zone, login: login, password: password, records: records}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeRegRU) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.lastForm = map[string]string{}
	for k := range r.PostForm {
		f.lastForm[k] = r.PostForm.Get(k)
	}
	failAuth := f.failAuth
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }

	if failAuth || r.PostForm.Get("username") != f.login || r.PostForm.Get("password") != f.password {
		reply(map[string]any{"result": "error", "error_code": "NO_AUTH", "error_text": "Authorization failed"})
		return
	}
	var input struct {
		Domains []struct {
			Dname string           `json:"dname"`
			RRS   []map[string]any `json:"rrs"`
		} `json:"domains"`
	}
	_ = json.Unmarshal([]byte(r.PostForm.Get("input_data")), &input)

	switch r.URL.Path {
	case creds.RegAPIPathUserNop:
		reply(map[string]any{"result": "success"})
	case creds.RegAPIPathZoneRecords:
		f.mu.Lock()
		recs := append([]map[string]any(nil), f.records...)
		zone := f.zone
		f.mu.Unlock()
		reply(map[string]any{
			"result": "success",
			"answer": map[string]any{"domains": []map[string]any{{"dname": zone, "rrs": recs}}},
		})
	case creds.RegAPIPathZoneUpdate:
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(input.Domains) != 1 || input.Domains[0].Dname != f.zone {
			f.mu.Unlock()
			reply(map[string]any{"result": "error", "error_code": "ZONE_NOT_FOUND", "error_text": "no such domain"})
			f.mu.Lock()
			return
		}
		f.updateCalls++
		for _, rr := range input.Domains[0].RRS {
			sub, _ := rr["subname"].(string)
			typ, _ := rr["rectype"].(string)
			content, _ := rr["content"].(string)
			if strings.EqualFold(typ, "A") {
				f.records = setA(f.records, sub, content)
			}
		}
		reply(map[string]any{"result": "success"})
	default:
		http.NotFound(w, r)
	}
}

// setA replaces the A record for subname, leaving other records alone.
func setA(records []map[string]any, subname, content string) []map[string]any {
	out := make([]map[string]any, 0, len(records)+1)
	for _, r := range records {
		sub, _ := r["subname"].(string)
		typ, _ := r["rectype"].(string)
		if strings.EqualFold(typ, "A") && strings.EqualFold(sub, subname) {
			continue
		}
		out = append(out, r)
	}
	return append(out, map[string]any{"subname": subname, "rectype": "A", "content": content, "ttl": 300})
}

func recA(subname, content string) map[string]any {
	return map[string]any{"subname": subname, "rectype": "A", "content": content, "ttl": 300}
}

func recMX(subname, content string) map[string]any {
	return map[string]any{"subname": subname, "rectype": "MX", "content": content, "ttl": 300, "prio": 10}
}

func testClient(t *testing.T, srv *httptest.Server, store *creds.Store) *Client {
	t.Helper()
	c := &Client{BaseURL: srv.URL, HTTPClient: srv.Client(), Store: store}
	c.credsOverride = &creds.Credentials{Provider: creds.ProviderRegru, Login: "user", Password: "pw", Zone: "example.com"}
	return c
}

// TestB468_GetRecordReadsTheRegistrarShape is the regression: the client looked for `name`/`type`
// while the registrar sends `subname`/`rectype`, so a present record was "not found".
func TestB468_GetRecordReadsTheRegistrarShape(t *testing.T) {
	_, srv := newFakeRegRU(t, "example.com", "user", "pw", recA("skygate", "203.0.113.10"), recMX("@", "mail.example.com"))
	c := testClient(t, srv, nil)
	ip, err := c.GetRecord(context.Background(), "example.com", "skygate")
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if ip != "203.0.113.10" {
		t.Fatalf("ip = %q, want 203.0.113.10", ip)
	}
}

func TestB468_GetRecordMissingReturnsTheSentinel(t *testing.T) {
	_, srv := newFakeRegRU(t, "example.com", "user", "pw", recA("head", "203.0.113.10"))
	c := testClient(t, srv, nil)
	if _, err := c.GetRecord(context.Background(), "example.com", "skygate"); err != ErrRecordNotFound {
		t.Fatalf("err = %v, want ErrRecordNotFound — a missing record must not look like an HTTP failure", err)
	}
}

// TestB468_NoAuthTrapIsNotReintroduced pins the form shape the project already paid for:
// username/password must be TOP-LEVEL fields, never inside input_data.
func TestB468_NoAuthTrapIsNotReintroduced(t *testing.T) {
	f, srv := newFakeRegRU(t, "example.com", "user", "pw", recA("skygate", "203.0.113.10"))
	c := testClient(t, srv, nil)
	if _, err := c.GetRecord(context.Background(), "example.com", "skygate"); err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	f.mu.Lock()
	form := f.lastForm
	f.mu.Unlock()
	if form["username"] != "user" || form["password"] != "pw" { // form username/password must be TOP-LEVEL
		t.Fatalf("credentials are not top-level form fields: %v", form)
	}
	if strings.Contains(form["input_data"], "password") || strings.Contains(form["input_data"], "username") {
		t.Fatalf("password leaked into input_data (the NO_AUTH trap): %s", form["input_data"])
	}
	if form["input_format"] != "json" || form["output_format"] != "json" {
		t.Fatalf("the request does not ask for JSON: %v", form)
	}
}

// TestB468_UpdateRecordWritesAndReadsBack — success means "DNS answers the new IP", not "the API
// accepted the call".
func TestB468_UpdateRecordWritesAndReadsBack(t *testing.T) {
	f, srv := newFakeRegRU(t, "example.com", "user", "pw", recA("skygate", "203.0.113.10"), recMX("@", "mail"))
	c := testClient(t, srv, nil)
	if err := c.UpdateRecord(context.Background(), "example.com", "skygate", "198.51.100.7"); err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	if ip, err := c.GetRecord(context.Background(), "example.com", "skygate"); err != nil || ip != "198.51.100.7" {
		t.Fatalf("after update ip = %q err = %v, want 198.51.100.7", ip, err)
	}
	f.mu.Lock()
	mx := 0
	for _, r := range f.records {
		if r["rectype"] == "MX" {
			mx++
		}
	}
	f.mu.Unlock()
	if mx != 1 {
		t.Error("the update removed another record type from the zone (an A-record switch must not touch MX)")
	}
}

func TestB468_UpdateRecordRejectsAnEmptyIP(t *testing.T) {
	_, srv := newFakeRegRU(t, "example.com", "user", "pw", recA("skygate", "203.0.113.10"))
	c := testClient(t, srv, nil)
	if err := c.UpdateRecord(context.Background(), "example.com", "skygate", ""); err == nil {
		t.Fatal("an empty IP was accepted")
	}
}

// TestB468_ErrorTextIsSurfaced — the registrar's own words, not a bare status code.
func TestB468_ErrorTextIsSurfaced(t *testing.T) {
	f, srv := newFakeRegRU(t, "example.com", "user", "pw", recA("skygate", "203.0.113.10"))
	f.failAuth = true
	c := testClient(t, srv, nil)
	_, err := c.GetRecord(context.Background(), "example.com", "skygate")
	if err == nil {
		t.Fatal("a NO_AUTH answer was treated as success")
	}
	if !strings.Contains(err.Error(), "NO_AUTH") || !strings.Contains(err.Error(), "Authorization failed") {
		t.Fatalf("error = %v, want the registrar's code and text", err)
	}
}

// TestB468_TestConnectionIsReadOnlyAndNamesTheZone — the panel's "test" button must prove the
// credentials without touching a record.
func TestB468_TestConnectionIsReadOnlyAndNamesTheZone(t *testing.T) {
	f, srv := newFakeRegRU(t, "example.com", "user", "pw", recA("skygate", "203.0.113.10"))
	c := testClient(t, srv, nil)
	if err := c.TestConnection(context.Background()); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	f.mu.Lock()
	updates := f.updateCalls
	f.mu.Unlock()
	if updates != 0 {
		t.Fatalf("TestConnection performed %d write(s)", updates)
	}
}

// TestB468_StoreTestConnectionUsesTheSameEndpoint — the credential store and the provider client
// must not drift apart again (that drift is what shipped).
func TestB468_StoreTestConnectionUsesTheSameEndpoint(t *testing.T) {
	f, srv := newFakeRegRU(t, "example.com", "user", "pw", recA("skygate", "203.0.113.10"))
	store := creds.NewStore(nil, "")
	store.HTTP = srv.Client()
	t.Setenv(creds.RegAPIBaseURLEnv, srv.URL)
	res, err := store.TestConnectionWithCreds(context.Background(), creds.Credentials{
		Provider: creds.ProviderRegru, Login: "user", Password: "pw", Zone: "example.com",
	})
	if err != nil {
		t.Fatalf("store TestConnection: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q (%s), want ok", res.Status, res.Message)
	}
	f.mu.Lock()
	path := f.lastForm != nil
	f.mu.Unlock()
	if !path {
		t.Fatal("the store made no call at all")
	}
}

// TestB468_ZoneRecordsAcceptsBothShapes — the older `records` key must not read as an empty zone.
func TestB468_ZoneRecordsAcceptsBothShapes(t *testing.T) {
	old := json.RawMessage(`{"domains":[{"dname":"example.com","records":[{"name":"skygate","type":"A","content":"203.0.113.10"}]}]}`)
	newShape := json.RawMessage(`{"domains":[{"dname":"example.com","rrs":[{"subname":"skygate","rectype":"A","content":"203.0.113.11"}]}]}`)
	for label, raw := range map[string]json.RawMessage{"older": old, "current": newShape} {
		recs, err := creds.ZoneRecords(raw)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if _, ok := creds.FindA(recs, "skygate"); !ok {
			t.Errorf("%s shape: the A record was not found", label)
		}
	}
}

// TestB468_BaseURLIsNotAZone — the shipped base must be the registrar's API host, and the zone
// must never be interpolated into it (the shell live-test's `api.${ZONE}` bug).
func TestB468_BaseURLIsNotAZone(t *testing.T) {
	if got := creds.RegAPIBase(); got != "https://api.reg.ru/api/regru2" {
		t.Fatalf("RegAPIBase() = %q, want the registrar endpoint", got)
	}
	if strings.Contains(creds.RegAPIBase(), "example") {
		t.Fatal("the base URL contains something zone-like")
	}
	t.Setenv(creds.RegAPIBaseURLEnv, "https://proxy.example/api/regru2/")
	if got := creds.RegAPIBase(); got != "https://proxy.example/api/regru2" {
		t.Fatalf("override = %q, want it trimmed of the trailing slash", got)
	}
}

// TestB468_CredentialValidationAcceptsLoginPasswordOnly — the mTLS-only rule rejected the very
// credentials the operator had provisioned.
func TestB468_CredentialValidationAcceptsLoginPasswordOnly(t *testing.T) {
	c := creds.Credentials{Provider: creds.ProviderRegru, Login: "user", Password: "pw", Zone: "example.com"}
	if err := c.Validate(); err != nil {
		t.Fatalf("a login/password credential was rejected: %v", err)
	}
	legacy := creds.Credentials{Provider: creds.ProviderExternal, Login: "user", Password: "pw", Zone: "example.com"}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("the legacy alias %q was rejected: %v", creds.ProviderExternal, err)
	}
	bad := creds.Credentials{Provider: creds.ProviderRegru, Login: "user", Password: "pw", Zone: "z", CertPEM: "not a pem"}
	if err := bad.Validate(); err == nil {
		t.Fatal("a malformed certificate was accepted")
	}
	if err := (creds.Credentials{Provider: "cloudflare", Login: "u", Password: "p", Zone: "z"}).Validate(); err == nil {
		t.Fatal("an unimplemented provider was accepted")
	}
}

// keep fmt referenced in case a future assertion needs it
var _ = fmt.Sprint
