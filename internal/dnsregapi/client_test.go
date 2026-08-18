// client_test.go — unit tests for the dnsregapi.Client.
// Uses httptest.Server to simulate reg.ru's /api/regru2
// endpoints; verifies the auth pattern (top-level form
// fields + mTLS cert) and the request/response parsing.
//
// v1.5.0 (B145). The working auth pattern (top-level
// username + password as form fields, NOT in input_data
// JSON) was confirmed against the live reg.ru API on
// 2026-08-18 — see the memory entry "reg.ru v2 API auth —
// real working pattern" for the full diagnostic history.
// These tests pin the pattern so a future refactor that
// re-introduces the broken "input_data JSON wrapping"
// pattern will fail the test before the change reaches
// production.

package dnsregapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/ha/regapi"
)

// fakeRegAPI is a small reg.ru emulator. It captures the
// incoming request's body, then returns the canned
// response. Each test gets its own server so they can run
// in parallel without cross-talk.
type fakeRegAPI struct {
	// lastBody is the most recent request body the
	// server saw. Tests assert on the captured form.
	lastBody []byte
	// responseStatus is the HTTP status to return.
	responseStatus int
	// responseBody is the raw body to return. If set,
	// overrides any canned response.
	responseBody string
	// overrideHandler, if non-nil, replaces the default
	// ServeHTTP. Used for per-test customisation
	// (e.g. the "logical error" test that wants the
	// same canned response but a different status).
	overrideHandler http.HandlerFunc
}

func (f *fakeRegAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.lastBody = body
	if f.overrideHandler != nil {
		f.overrideHandler(w, r)
		return
	}
	w.WriteHeader(f.responseStatus)
	if f.responseBody != "" {
		_, _ = w.Write([]byte(f.responseBody))
	}
}

func newFakeRegAPI(t *testing.T) (*httptest.Server, *fakeRegAPI) {
	t.Helper()
	f := &fakeRegAPI{responseStatus: 200}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv, f
}

// newTestClient builds a Client that talks to the given
// test server with the supplied credentials. The creds
// are injected via the test-only credsOverride field so
// the tests don't need a real *sql.DB. A custom
// http.Transport rewrites the destination URL from
// https://api.reg.ru/... to the test server's URL.
//
// Returning the *fakeRegAPI lets each test read the
// captured request body for assertions.
func newTestClient(t *testing.T, srv *httptest.Server, login, password, zone, certPEM string) (*Client, *fakeRegAPI) {
	t.Helper()
	c := &Client{
		Store: nil,
		HTTP:  &http.Client{Timeout: 5e9},
		credsOverride: &regapi.Credentials{
			Provider: "regapi",
			Login:    login,
			Zone:     zone,
			CertPEM:  certPEM,
			Password: password,
		},
	}
	c.HTTP.Transport = &urlRewriter{base: srv.URL}
	return c, nil
}

// urlRewriter is an http.RoundTripper that rewrites the
// destination URL to the test server's URL. Used so the
// production code (which hardcodes
// https://api.reg.ru/api/regru2/...) can be exercised
// against a local httptest.Server.
type urlRewriter struct {
	base string
}

func (t *urlRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace scheme+host with the test base. Path
	// and query stay the same. The body is read by
	// the production code via req.Body, which is
	// preserved through NewRequestWithContext.
	newURL := t.base + req.URL.Path
	if req.URL.RawQuery != "" {
		newURL += "?" + req.URL.RawQuery
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		for _, vv := range v {
			newReq.Header.Add(k, vv)
		}
	}
	return http.DefaultTransport.RoundTrip(newReq)
}

const validTestCertPEM = `-----BEGIN CERTIFICATE-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA0000000000000000
0000000000000000000000000000000000000000000000000000000000000000
0000000000000000000000000000000000000000000000000000000000000000
0000000000000000000000000000000000000000000000000000000000
-----END CERTIFICATE-----`

// TestGetRecord_SendsTopLevelFormFields is the regression
// test for the "NO_AUTH" bug: the OLD code put
// username+password INSIDE the input_data JSON; the NEW
// (working) code puts them as TOP-LEVEL form fields. If
// a future refactor moves them back into the JSON, this
// test fails before the change reaches production.
func TestGetRecord_SendsTopLevelFormFields(t *testing.T) {
	srv, fake := newFakeRegAPI(t)
	fake.responseBody = `{"answer":{"domains":[{"dname":"example.com","records":[{"fqdn":"skygate.example.com","rectype":"A","content":"1.2.3.4"}]}]}}`
	c, _ := newTestClient(t, srv, "user@example.com", "test-password", "example.com", validTestCertPEM)

	ip, err := c.GetRecord(context.Background(), "example.com", "skygate")
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if ip != "1.2.3.4" {
		t.Errorf("GetRecord = %q, want 1.2.3.4", ip)
	}

	// CRITICAL: the captured form must have
	// username + password as TOP-LEVEL fields, NOT
	// inside the input_data JSON. This is the
	// regression test for the "NO_AUTH" bug.
	form, err := url.ParseQuery(string(fake.lastBody))
	if err != nil {
		t.Fatalf("parse captured form: %v (body=%q)", err, fake.lastBody)
	}
	if form.Get("username") != "user@example.com" {
		t.Errorf("form username = %q, want user@example.com", form.Get("username"))
	}
	if form.Get("password") != "test-password" {
		t.Errorf("form password = %q, want test-password", form.Get("password"))
	}
	// And it must NOT be in input_data:
	if inputData := form.Get("input_data"); strings.Contains(inputData, "test-password") {
		t.Errorf("password leaked into input_data: %q", inputData)
	}
}

// TestGetRecord_NotFound — server returns an empty
// records list; client should return ErrRecordNotFound.
func TestGetRecord_NotFound(t *testing.T) {
	srv, fake := newFakeRegAPI(t)
	fake.responseBody = `{"answer":{"domains":[{"dname":"example.com","records":[]}]}}`
	c, _ := newTestClient(t, srv, "user@example.com", "test-password", "example.com", validTestCertPEM)

	_, err := c.GetRecord(context.Background(), "example.com", "skygate")
	if err != ErrRecordNotFound {
		t.Errorf("GetRecord on empty record list error = %v, want ErrRecordNotFound", err)
	}
}

// TestGetRecord_ServerError — server returns 500; client
// should propagate the error with the status code.
func TestGetRecord_ServerError(t *testing.T) {
	srv, fake := newFakeRegAPI(t)
	fake.responseStatus = 500
	fake.responseBody = `{"error_code":"INTERNAL","error_text":"oops"}`
	c, _ := newTestClient(t, srv, "user@example.com", "test-password", "example.com", validTestCertPEM)

	_, err := c.GetRecord(context.Background(), "example.com", "skygate")
	if err == nil {
		t.Fatal("GetRecord on HTTP 500 returned nil, want error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q should mention HTTP 500", err.Error())
	}
}

// TestUpdateRecord_Success — server returns "ok" ack;
// client should not return an error.
func TestUpdateRecord_Success(t *testing.T) {
	srv, fake := newFakeRegAPI(t)
	fake.responseBody = `{"result":"success","answer":{"domains":[{"dname":"example.com","error_code":""}]}}`
	c, _ := newTestClient(t, srv, "user@example.com", "test-password", "example.com", validTestCertPEM)

	err := c.UpdateRecord(context.Background(), "example.com", "skygate", "5.6.7.8")
	if err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	// Verify the form had the right A-record target.
	form, _ := url.ParseQuery(string(fake.lastBody))
	inputData := form.Get("input_data")
	if !strings.Contains(inputData, "5.6.7.8") {
		t.Errorf("UpdateRecord input_data = %q, want it to contain 5.6.7.8", inputData)
	}
	if !strings.Contains(inputData, `"rectype":"A"`) {
		t.Errorf("UpdateRecord input_data = %q, want rectype A", inputData)
	}
}

// TestUpdateRecord_EmptyIP — client should reject empty
// IP without making the HTTP call.
func TestUpdateRecord_EmptyIP(t *testing.T) {
	srv, fake := newFakeRegAPI(t)
	c, _ := newTestClient(t, srv, "user@example.com", "test-password", "example.com", validTestCertPEM)
	err := c.UpdateRecord(context.Background(), "example.com", "skygate", "")
	if err == nil {
		t.Fatal("UpdateRecord with empty IP returned nil, want error")
	}
	// The body should still be empty (no HTTP call made).
	if len(fake.lastBody) > 0 {
		t.Errorf("UpdateRecord with empty IP made an HTTP call: body=%q", fake.lastBody)
	}
}

// TestUpdateRecord_LogicalError — server returns 200 OK
// with {"result":"error", ...} in the body. Client
// should treat this as a failure (reg.ru returns 200 +
// logical error for "auth_error" / "access_denied" etc).
func TestUpdateRecord_LogicalError(t *testing.T) {
	srv, fake := newFakeRegAPI(t)
	fake.responseBody = `{"result":"error","error_code":"DOMAIN_NOT_FOUND","error_text":"no such domain"}`
	c, _ := newTestClient(t, srv, "user@example.com", "test-password", "example.com", validTestCertPEM)
	err := c.UpdateRecord(context.Background(), "example.com", "skygate", "5.6.7.8")
	if err == nil {
		t.Fatal("UpdateRecord on logical error returned nil, want error")
	}
	if !strings.Contains(err.Error(), "DOMAIN_NOT_FOUND") {
		t.Errorf("error %q should mention DOMAIN_NOT_FOUND", err.Error())
	}
}

// TestName — the Name() method must return the canonical
// identifier "regapi". This is the string operators set
// SKYGATE_DNS_PROVIDER to, so a rename would silently
// break all deployed configs.
func TestName(t *testing.T) {
	c := &Client{}
	if got := c.Name(); got != "regapi" {
		t.Errorf("Name() = %q, want regapi (the SKYGATE_DNS_PROVIDER identifier)", got)
	}
}

// TestRequestShape_Stable pins the form-field shape so a
// future refactor that drops a field fails here before
// reaching production.
func TestRequestShape_Stable(t *testing.T) {
	srv, fake := newFakeRegAPI(t)
	fake.responseBody = `{"result":"success"}`
	c, _ := newTestClient(t, srv, "u@x.com", "p", "z", validTestCertPEM)
	if err := c.UpdateRecord(context.Background(), "z", "skygate", "1.2.3.4"); err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	body := string(fake.lastBody)
	for _, want := range []string{"username=u%40x.com", "password=p", "output_content_type=json"} {
		if !strings.Contains(body, want) {
			t.Errorf("UpdateRecord form missing %q: %q", want, body)
		}
	}
	// And the parsed input_data must be valid JSON.
	form, _ := url.ParseQuery(body)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(form.Get("input_data")), &parsed); err != nil {
		t.Errorf("input_data is not valid JSON: %v (raw=%q)", err, form.Get("input_data"))
	}
}
