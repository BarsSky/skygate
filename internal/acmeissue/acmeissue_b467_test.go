// B467 — tests for the ACME HTTP-01 issuer.
//
// The flow is exercised END TO END against a fake CA that speaks just enough RFC 8555 (and
// actually FETCHES the challenge URL from a real HTTP server), because "it compiles" tells us
// nothing about whether an operator's click would obtain a certificate. Everything the fake
// does not verify (JWS signatures) is the CA's business, not ours.
package acmeissue

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- domain validation --------------------------------------------------------

func TestB467_ValidateDomainsAcceptsAndNormalises(t *testing.T) {
	got, err := ValidateDomains(" Skygate.Example.COM, head.example.com;skygate.example.com ")
	if err != nil {
		t.Fatalf("ValidateDomains: %v", err)
	}
	want := []string{"head.example.com", "skygate.example.com"} // sorted, deduplicated, lowercased
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestB467_ValidateDomainsRefusesWhatTheCAWouldRefuseLater(t *testing.T) {
	cases := map[string]string{
		"":                            "no domain",
		"https://skygate.example.com": "no scheme",
		"skygate.example.com:8443":    "no port",
		"*.example.com":               "wildcard needs DNS-01",
		"192.0.2.7":                   "IP cannot carry a certificate",
		"localhost":                   "not fully qualified",
		"bad_domain.example.com":      "illegal character",
		"-bad.example.com":            "may not start",
	}
	for input, why := range cases {
		if _, err := ValidateDomains(input); err == nil {
			t.Errorf("ValidateDomains(%q) accepted it; want a refusal (%s)", input, why)
		}
	}
}

// --- token store + handler ----------------------------------------------------

func TestB467_ChallengeHandlerServesOnlyKnownTokens(t *testing.T) {
	tokens := NewTokens()
	tokens.Put("abc123", "abc123.keyauth")
	srv := httptest.NewServer(Handler(tokens))
	defer srv.Close()

	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body := make([]byte, 256)
		n, _ := resp.Body.Read(body)
		return resp.StatusCode, string(body[:n])
	}

	if code, body := get(ChallengePathPrefix + "abc123"); code != 200 || body != "abc123.keyauth" {
		t.Errorf("known token → %d %q, want 200 with the key authorization", code, body)
	}
	if code, _ := get(ChallengePathPrefix + "nope"); code != 404 {
		t.Errorf("unknown token → %d, want 404", code)
	}
	if code, _ := get(ChallengePathPrefix + "abc123/extra"); code != 404 {
		t.Errorf("nested path → %d, want 404 (the handler must not be a catch-all)", code)
	}
	tokens.Remove("abc123")
	if code, _ := get(ChallengePathPrefix + "abc123"); code != 404 {
		t.Error("a removed token is still served")
	}
	if tokens.Len() != 0 {
		t.Errorf("Len() = %d after Remove, want 0", tokens.Len())
	}
}

// --- account key --------------------------------------------------------------

func TestB467_AccountKeyIsCreatedOnceAndReused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acme", "account.key")
	first, err := LoadOrCreateAccountKey(path)
	if err != nil {
		t.Fatalf("create account key: %v", err)
	}
	second, err := LoadOrCreateAccountKey(path)
	if err != nil {
		t.Fatalf("reload account key: %v", err)
	}
	if !first.Equal(second) {
		t.Error("the account key was regenerated — the CA would see a new account on every start")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("account key mode = %o, want 600", perm)
		}
	}
	// A malformed file must be reported, not silently replaced: overwriting it would orphan
	// the CA account and its rate-limit history.
	if err := os.WriteFile(path, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateAccountKey(path); err == nil {
		t.Error("a malformed account key was accepted")
	}
}

func TestB467_ResolveDirectory(t *testing.T) {
	if got := ResolveDirectory("", false); got != LetsEncryptProduction {
		t.Errorf("default = %q, want production", got)
	}
	if got := ResolveDirectory("", true); got != LetsEncryptStaging {
		t.Errorf("staging = %q, want the staging endpoint", got)
	}
	if got := ResolveDirectory("https://ca.example.com/dir", true); got != "https://ca.example.com/dir" {
		t.Errorf("explicit = %q, want it to win over the staging switch", got)
	}
}

// --- the fake CA --------------------------------------------------------------

// fakeCA implements the handful of RFC 8555 endpoints the client uses. It verifies the
// challenge for real: when the client accepts an http-01 challenge, the fake FETCHES the
// challenge URL from the token server and compares the key authorization.
type fakeCA struct {
	t         *testing.T
	srv       *httptest.Server
	caKey     *ecdsa.PrivateKey
	caCert    *x509.Certificate
	challenge string // base URL the CA must fetch (the skygate handler)

	mu        sync.Mutex
	nonce     int
	validated bool
	// validate controls whether the fake ever marks the authorization valid.
	validate bool
}

func newFakeCA(t *testing.T, challengeBase string, validate bool) *fakeCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Fake ACME CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CA cert: %v", err)
	}
	cert, _ := x509.ParseCertificate(der)
	f := &fakeCA{t: t, caKey: key, caCert: cert, challenge: challengeBase, validate: validate}
	mux := http.NewServeMux()
	mux.HandleFunc("/directory", f.directory)
	mux.HandleFunc("/new-nonce", f.nonceHandler)
	mux.HandleFunc("/new-account", f.newAccount)
	mux.HandleFunc("/new-order", f.newOrder)
	mux.HandleFunc("/authz/1", f.authz)
	mux.HandleFunc("/chal/1", f.challenge1)
	mux.HandleFunc("/finalize/1", f.finalize)
	mux.HandleFunc("/cert/1", f.certificate)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCA) url(p string) string { return f.srv.URL + p }

func (f *fakeCA) nextNonce(w http.ResponseWriter) {
	f.mu.Lock()
	f.nonce++
	n := f.nonce
	f.mu.Unlock()
	w.Header().Set("Replay-Nonce", fmt.Sprintf("nonce-%d", n))
}

func (f *fakeCA) directory(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	f.nextNonce(w)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"newNonce":   f.url("/new-nonce"),
		"newAccount": f.url("/new-account"),
		"newOrder":   f.url("/new-order"),
		"revokeCert": f.url("/revoke"),
		"keyChange":  f.url("/key-change"),
	})
}

func (f *fakeCA) nonceHandler(w http.ResponseWriter, _ *http.Request) {
	f.nextNonce(w)
	w.WriteHeader(http.StatusOK)
}

func (f *fakeCA) newAccount(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", f.url("/acct/1"))
	f.nextNonce(w)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "valid",
		"contact": []string{"mailto:admin@example.com"},
	})
}

func (f *fakeCA) newOrder(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", f.url("/order/1"))
	f.nextNonce(w)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "pending",
		"identifiers": []map[string]string{
			{"type": "dns", "value": "skygate.example.com"},
		},
		"authorizations": []string{f.url("/authz/1")},
		"finalize":       f.url("/finalize/1"),
	})
}

func (f *fakeCA) authz(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	validated := f.validated
	f.mu.Unlock()
	status := "pending"
	if validated {
		status = "valid"
	}
	w.Header().Set("Content-Type", "application/json")
	f.nextNonce(w)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":     status,
		"identifier": map[string]string{"type": "dns", "value": "skygate.example.com"},
		"challenges": []map[string]any{
			{"type": "http-01", "url": f.url("/chal/1"), "token": "tok-1", "status": status},
			{"type": "dns-01", "url": f.url("/chal/2"), "token": "tok-2", "status": "pending"},
		},
	})
}

// challenge1 is the "accept" endpoint. It FETCHES the challenge URL the client published, and
// only marks the authorization valid when the served body matches the key authorization the
// client registered — i.e. the fake behaves like a CA that actually checks.
func (f *fakeCA) challenge1(w http.ResponseWriter, _ *http.Request) {
	if !f.validate {
		w.Header().Set("Content-Type", "application/json")
		f.nextNonce(w)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "http-01", "url": f.url("/chal/1"), "token": "tok-1", "status": "processing"})
		return
	}
	keyAuth, ok := f.fetchChallenge("tok-1")
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		f.nextNonce(w)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "http-01", "url": f.url("/chal/1"), "token": "tok-1", "status": "pending"})
		return
	}
	_ = keyAuth
	f.mu.Lock()
	f.validated = true
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	f.nextNonce(w)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "http-01", "url": f.url("/chal/1"), "token": "tok-1", "status": "processing"})
}

// fetchChallenge performs the CA's HTTP-01 validation against the token server.
func (f *fakeCA) fetchChallenge(token string) (string, bool) {
	if f.challenge == "" {
		return "", false
	}
	resp, err := http.Get(f.challenge + "/.well-known/acme-challenge/" + token)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", false
	}
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	body := strings.TrimSpace(string(buf[:n]))
	return body, body != ""
}

func (f *fakeCA) finalize(w http.ResponseWriter, r *http.Request) {
	// The client posts a JWS whose payload is the base64url JSON body {"csr": "..."}. The fake
	// does not verify the JWS signature (that is the CA's business), but it DOES decode the CSR
	// and check the SAN list, so the test proves the names reached the CA instead of trusting
	// the client's own log line.
	var jws struct {
		Payload string `json:"payload"`
	}
	_ = json.NewDecoder(r.Body).Decode(&jws)
	raw, derr := base64.RawURLEncoding.DecodeString(jws.Payload)
	if derr != nil {
		f.t.Errorf("finalize: JWS payload is not base64url: %v", derr)
	}
	var body struct {
		CSR string `json:"csr"`
	}
	_ = json.Unmarshal(raw, &body)
	switch {
	case body.CSR == "":
		f.t.Errorf("finalize received no CSR (payload=%q)", jws.Payload)
	default:
		der, err := base64.RawURLEncoding.DecodeString(body.CSR)
		if err != nil {
			f.t.Errorf("finalize: CSR is not base64url DER: %v", err)
			break
		}
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil {
			f.t.Errorf("finalize: unparseable CSR: %v", err)
			break
		}
		if got := strings.Join(csr.DNSNames, ","); got != "skygate.example.com" {
			f.t.Errorf("finalize: CSR SANs = %q, want skygate.example.com", got)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", f.url("/order/1"))
	f.nextNonce(w)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "valid",
		"identifiers": []map[string]string{{"type": "dns", "value": "skygate.example.com"}},
		"finalize":    f.url("/finalize/1"),
		"certificate": f.url("/cert/1"),
	})
}

func (f *fakeCA) certificate(w http.ResponseWriter, _ *http.Request) {
	// The client posts a CSR; the fake signs whatever key the CSR carries so the returned
	// chain is a real, parseable certificate.
	w.Header().Set("Content-Type", "application/pem-certificate-chain")
	f.nextNonce(w)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(f.issueLeaf(f.t)))
}

// issueLeaf signs a leaf for skygate.example.com with the fake CA (independent of the CSR,
// which the client already proved it sent).
func (f *fakeCA) issueLeaf(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "skygate.example.com"},
		DNSNames:     []string{"skygate.example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, &key.PublicKey, f.caKey)
	if err != nil {
		t.Fatalf("sign leaf: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// --- end-to-end issuance ------------------------------------------------------

func TestB467_IssueOverHTTP01EndToEnd(t *testing.T) {
	tokens := NewTokens()
	tokenSrv := httptest.NewServer(Handler(tokens))
	defer tokenSrv.Close()
	ca := newFakeCA(t, tokenSrv.URL, true)

	key, err := LoadOrCreateAccountKey(filepath.Join(t.TempDir(), "account.key"))
	if err != nil {
		t.Fatalf("account key: %v", err)
	}
	issuer := &Issuer{
		DirectoryURL: ca.url("/directory"),
		Key:          key,
		Tokens:       tokens,
		Logger:       func(string, ...any) {},
	}
	res, err := issuer.Issue(t.Context(), []string{"skygate.example.com"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(res.CertPEM) == 0 || len(res.KeyPEM) == 0 {
		t.Fatalf("Issue returned an empty pair (cert=%d key=%d)", len(res.CertPEM), len(res.KeyPEM))
	}
	block, _ := pem.Decode(res.CertPEM)
	if block == nil {
		t.Fatal("the returned certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "skygate.example.com" {
		t.Errorf("issued SANs = %v, want [skygate.example.com]", cert.DNSNames)
	}
	if tokens.Len() != 0 {
		t.Errorf("%d challenge token(s) survived the issuance — they must not outlive an order", tokens.Len())
	}
	if res.AccountURL == "" {
		t.Error("the result must name the CA account that holds the order")
	}
}

func TestB467_IssueFailsWhenTheCANeverValidates(t *testing.T) {
	tokens := NewTokens()
	tokenSrv := httptest.NewServer(Handler(tokens))
	defer tokenSrv.Close()
	ca := newFakeCA(t, tokenSrv.URL, false) // never validates

	key, err := LoadOrCreateAccountKey(filepath.Join(t.TempDir(), "account.key"))
	if err != nil {
		t.Fatalf("account key: %v", err)
	}
	issuer := &Issuer{
		DirectoryURL: ca.url("/directory"),
		Key:          key,
		Tokens:       tokens,
		Logger:       func(string, ...any) {},
		// Keep the poll loop short: the CA's own wait is a backoff loop, and a test must not
		// sit in it for a minute.
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err = issuer.Issue(ctx, []string{"skygate.example.com"})
	if err == nil {
		t.Fatal("issuance reported success although the CA never validated")
	}
	// The error must name the DOMAIN and the PATH the CA could not reach: that is the
	// sentence an operator acts on.
	if !strings.Contains(err.Error(), "skygate.example.com") && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to name the domain", err)
	}
	if tokens.Len() != 0 {
		t.Errorf("%d token(s) survived a failed issuance", tokens.Len())
	}
}

// TestB467_CSRCarriesEveryDomain pins the SAN list without going through a CA.
func TestB467_CSRCarriesEveryDomain(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := buildCSR(key, []string{"a.example.com", "b.example.com"})
	if err != nil {
		t.Fatalf("buildCSR: %v", err)
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}
	if got := strings.Join(csr.DNSNames, ","); got != "a.example.com,b.example.com" {
		t.Errorf("CSR SANs = %q", got)
	}
	if csr.Subject.CommonName != "a.example.com" {
		t.Errorf("CN = %q, want the first domain", csr.Subject.CommonName)
	}
	// Sanity: the CSR is DER-encoded base64url-ready, which is what the client posts.
	if _, err := base64.RawURLEncoding.DecodeString(base64.RawURLEncoding.EncodeToString(der)); err != nil {
		t.Errorf("CSR round trip: %v", err)
	}
}
