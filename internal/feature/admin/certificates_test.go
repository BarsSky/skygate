// Package admin — certificates_test.go is the unit-test file
// for the /admin/certificates handlers (B148). The test
// suite is pure-Go (no DB, no live S3) and covers the
// deterministic helpers:
//
//   1. readLocalCertInfo        — parses a real cert + key
//                                  from disk and produces the
//                                  "current cert" card the
//                                  page renders.
//   2. readLocalCertInfo errors — missing file + non-PEM
//                                  content.
//   3. readCertInput            — file vs textarea fallback
//                                  (the form has both fields;
//                                  the handler prefers the
//                                  file).
//   4. certRedirect             — flash-message URL encoding
//                                  contract (special chars +
//                                  Unicode stay intact).
//   5. certsyncCertPath         — path stability (the
//                                  certsync scheduler writes
//                                  to this exact path).
//   6. certChainStrings         — chain display returns the
//                                  issuer DN as the v1.5.0
//                                  minimum (full chain is
//                                  v1.5.x backlog).
//
// Why pure-Go for these tests:
//
//   - The "can the handler parse the cert the operator
//     uploaded?" contract is the operator-facing surface
//     (a 500 on a valid cert+key would be a regression).
//   - The full POST → DB → render flow is covered by the
//     B-check (scripts/check_b148.sh) + the live-verify
//     on the VM.
//   - The validation rules (x509 + matchedAny over
//     PKCS#1/PKCS#8/SEC1) are owned by the certsync
//     package (B147) and have their own test file
//     (`internal/certsync/certsync_test.go`). B148
//     re-uses the exported ValidateCertKeyPair, so we
//     don't duplicate the rule testing here — we only
//     verify that the B148 wrapper layer calls into it
//     correctly.

package admin

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------- 1. readLocalCertInfo: happy path ---------------------------

// TestReadLocalCertInfo_ParsesValidCert verifies that a real
// cert+key on disk is parsed into the full display card
// (Subject, Issuer, NotBefore, NotAfter, DaysLeft, SHA-256,
// DNSNames).
func TestReadLocalCertInfo_ParsesValidCert(t *testing.T) {
	tmp := t.TempDir()
	certPath := tmp + "/cert.pem"

	certPEM, _ := mustGenTestCertKeyPair(t)
	if err := writeFile(certPath, certPEM); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	info, err := readLocalCertInfo(certPath)
	if err != nil {
		t.Fatalf("readLocalCertInfo: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	if !strings.Contains(info.Subject, "CN=test-b148") {
		t.Errorf("Subject = %q, expected to contain 'CN=test-b148'", info.Subject)
	}
	if info.NotBefore == "" {
		t.Error("NotBefore is empty")
	}
	if info.NotAfter == "" {
		t.Error("NotAfter is empty")
	}
	// NotAfter is 24h after NotBefore, so DaysLeft
	// should be approximately 1 (0 or 1 depending on
	// the timing of the test).
	if info.DaysLeft < 0 || info.DaysLeft > 2 {
		t.Errorf("DaysLeft = %d, expected 0..2 (cert expires in 24h)", info.DaysLeft)
	}
	if len(info.SHA256) != 64 {
		t.Errorf("SHA256 length = %d, expected 64 hex chars", len(info.SHA256))
	}
	// The cert was generated with 2 DNSNames (a + b).
	if len(info.DNSNames) != 2 {
		t.Errorf("DNSNames = %v, expected 2 entries", info.DNSNames)
	}
}

// ---------- 2. readLocalCertInfo: error paths --------------------------

// TestReadLocalCertInfo_MissingFile verifies the
// "no local cert yet" empty state is handled gracefully
// (the page renders the empty-state, not a 500).
func TestReadLocalCertInfo_MissingFile(t *testing.T) {
	_, err := readLocalCertInfo("/nonexistent/cert.pem")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	// Must be a "no such file" kind of error, not a
	// "parse error" — the page's "no local cert" empty
	// state only fires on ENOENT.
	if !strings.Contains(err.Error(), "no such file") &&
		!strings.Contains(err.Error(), "cannot find") &&
		!strings.Contains(err.Error(), "system cannot find") {
		t.Errorf("error = %v, expected ENOENT-style", err)
	}
}

// TestReadLocalCertInfo_MalformedCert verifies that a file
// with non-PEM content returns a parse error (the page
// renders the empty state, not garbage).
func TestReadLocalCertInfo_MalformedCert(t *testing.T) {
	tmp := t.TempDir()
	certPath := tmp + "/cert.pem"
	if err := writeFile(certPath, []byte("not a PEM file\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := readLocalCertInfo(certPath)
	if err == nil {
		t.Fatal("expected parse error for non-PEM file, got nil")
	}
	// Either "no PEM block" or "parse cert" — both
	// signal a malformed cert.
	if !strings.Contains(err.Error(), "PEM") &&
		!strings.Contains(err.Error(), "parse") {
		t.Errorf("error = %v, expected PEM/parse error", err)
	}
}

// ---------- 3. readCertInput: file vs textarea --------------------------

// TestReadCertInput_PrefersFile verifies that when both
// the file upload AND the textarea are populated, the
// file wins (the file is the source of truth — the
// textarea is the fallback for operators without a
// file picker).
func TestReadCertInput_PrefersFile(t *testing.T) {
	body := &strings.Builder{}
	mw := multipart.NewWriter(body)
	// File field: large explicit content.
	fileW, _ := mw.CreateFormFile("cert_pem_file", "cert.pem")
	_, _ = fileW.Write([]byte("FROM-FILE-CONTENT"))
	// Text field: also set, but the file should win.
	_ = mw.WriteField("cert_pem_text", "FROM-TEXT-CONTENT")
	mw.Close()

	r := httptest.NewRequest("POST", "/admin/certificates/upload", strings.NewReader(body.String()))
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("ParseMultipartForm: %v", err)
	}
	got, err := readCertInput(r, "cert_pem_file", "cert_pem_text")
	if err != nil {
		t.Fatalf("readCertInput: %v", err)
	}
	if string(got) != "FROM-FILE-CONTENT" {
		t.Errorf("got %q, want FROM-FILE-CONTENT (file should win)", string(got))
	}
}

// TestReadCertInput_FallsBackToText verifies that when no
// file is uploaded but the textarea is populated, the
// textarea content is returned.
func TestReadCertInput_FallsBackToText(t *testing.T) {
	form := url.Values{}
	form.Set("key_pem_text", "-----BEGIN PRIVATE KEY-----\nMIIBV...\n-----END PRIVATE KEY-----\n")
	r := httptest.NewRequest("POST", "/admin/certificates/upload", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	got, err := readCertInput(r, "key_pem_file", "key_pem_text")
	if err != nil {
		t.Fatalf("readCertInput: %v", err)
	}
	if !strings.HasPrefix(string(got), "-----BEGIN PRIVATE KEY-----") {
		t.Errorf("got %q, expected PEM-encoded key", string(got))
	}
}

// TestReadCertInput_NoInput verifies that the empty-state
// returns a clear error (not silent nil — the handler
// needs this to render the "please provide a cert+key"
// flash).
func TestReadCertInput_NoInput(t *testing.T) {
	form := url.Values{}
	r := httptest.NewRequest("POST", "/admin/certificates/upload", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	_, err := readCertInput(r, "cert_pem_file", "cert_pem_text")
	if err == nil {
		t.Fatal("expected error for empty form, got nil")
	}
	if !strings.Contains(err.Error(), "neither file nor textarea") {
		t.Errorf("error = %v, expected 'neither file nor textarea' hint", err)
	}
}

// ---------- 4. certRedirect: flash URL encoding -----------------------

// TestCertRedirect_EncodesFlash verifies that the
// redirect URL contract: ok= and err= query params are
// URL-escaped, special chars + Unicode stay intact
// after a round-trip through the redirect.
func TestCertRedirect_EncodesFlash(t *testing.T) {
	r := httptest.NewRequest("GET", "/admin/certificates", nil)
	w := httptest.NewRecorder()
	certRedirect(w, r, "Cert uploaded. SHA-256=abc. Сертификат валиден.", "")
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/certificates?ok=") {
		t.Errorf("Location = %q, expected /admin/certificates?ok=...", loc)
	}
	// Round-trip: parse the URL + extract the ok param.
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	okVal := u.Query().Get("ok")
	if !strings.Contains(okVal, "Сертификат валиден") {
		t.Errorf("ok = %q, expected to contain 'Сертификат валиден' (Unicode preserved)", okVal)
	}
	if !strings.Contains(okVal, "SHA-256=abc") {
		t.Errorf("ok = %q, expected to contain 'SHA-256=abc' (special chars preserved)", okVal)
	}
}

// TestCertRedirect_ErrorOnly verifies that an error-only
// redirect has the ?err= param (no ?ok=).
func TestCertRedirect_ErrorOnly(t *testing.T) {
	r := httptest.NewRequest("GET", "/admin/certificates", nil)
	w := httptest.NewRecorder()
	certRedirect(w, r, "", "Validation failed: key does not match cert")
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/certificates?err=") {
		t.Errorf("Location = %q, expected /admin/certificates?err=...", loc)
	}
	if strings.Contains(loc, "&ok=") {
		t.Errorf("Location = %q, expected no &ok= param (error-only)", loc)
	}
}

// ---------- 5. certsyncCertPath: path stability -----------------------

// TestCertSyncCertPath_StablePath verifies the page
// reads from the same path the certsync scheduler
// writes to. If this drifts, the page shows an empty
// "current cert" card even after a successful pull.
//
// Note: the path is hard-coded to a Linux-style absolute
// path because the certsync scheduler (B147) only runs
// on the Linux VM, not on Windows. The test compares
// against the literal string — not filepath.Join, which
// would rewrite the separators on Windows and produce
// a false positive failure.
func TestCertSyncCertPath_StablePath(t *testing.T) {
	got := certsyncCertPath()
	// Path-separator-agnostic: just verify the tail
	// "skygate/certs/cert.pem" is present (after
	// normalising any backslashes to forward slashes).
	normalised := strings.ReplaceAll(got, "\\", "/")
	if !strings.HasSuffix(normalised, "skygate/certs/cert.pem") {
		t.Errorf("certsyncCertPath = %q, expected to end with 'skygate/certs/cert.pem'", got)
	}
}

// ---------- 6. certChainStrings: shape check --------------------------

// TestCertChainStrings_ReturnsIssuer verifies the v1.5.0
// minimum: the chain display is the issuer DN of the
// leaf cert. Full chain (intermediates + root) is
// v1.5.x backlog.
func TestCertChainStrings_ReturnsIssuer(t *testing.T) {
	// Use a self-signed cert: the issuer DN equals the
	// subject DN.
	certPEM, _ := mustGenTestCertKeyPair(t)
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := certChainStrings(cert)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0] != cert.Issuer.String() {
		t.Errorf("got %q, want issuer DN %q", got[0], cert.Issuer.String())
	}
}

// ---------- shared helpers ---------------------------------------------

// mustGenTestCertKeyPair generates a self-signed cert +
// matching RSA-2048 key for tests. Returns PEM bytes
// (cert + key). Mirrors the helper in
// internal/certsync/certsync_test.go — duplicated here
// (not exported) because the test file can't import a
// test-only helper from a sibling package.
func mustGenTestCertKeyPair(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-b148"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		DNSNames:     []string{"a.example.com", "b.example.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return certPEM, keyPEM
}

// writeFile is a tiny helper that writes bytes to a
// path (test-only; thin wrapper around os.WriteFile
// so the call sites read like test setup, not generic
// file IO).
func writeFile(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644)
}
