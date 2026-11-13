// B469 — the renewal decision, tested as a decision.
//
// The rule the project keeps re-learning: "not due" and "could not read the certificate" are
// different facts. A loop that treats an unreadable file as "fresh" postpones renewal forever and
// the first symptom is an expiry alert on the day the site breaks.
package acmeissue

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// selfSignedFor builds a certificate with the given validity window and SANs (no CA needed: the
// decision only reads NotAfter and DNSNames).
func selfSignedFor(t *testing.T, now time.Time, notAfter time.Time, dns ...string) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: dns[0]},
		DNSNames:     dns,
		NotBefore:    now.Add(-80 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	key := mustKey(t)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestB469_NeedsRenewalDecidesFromNotAfter pins the three real cases and the boundary.
func TestB469_NeedsRenewalDecidesFromNotAfter(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	cert := selfSignedFor(t, now, now.Add(45*24*time.Hour), "skygate.example.com", "head.example.com")

	st := NeedsRenewal(cert, now, 30*24*time.Hour)
	if st.Due {
		t.Fatalf("45 days left with a 30-day window is due: %s", st.Describe())
	}
	if st.Reason != "fresh" || st.DaysLeft != 45 {
		t.Errorf("status = %+v, want reason=fresh days_left=45", st)
	}
	if len(st.Domains) != 2 {
		t.Errorf("domains = %v, want the certificate's two SANs", st.Domains)
	}

	due := selfSignedFor(t, now, now.Add(10*24*time.Hour), "skygate.example.com")
	st = NeedsRenewal(due, now, 30*24*time.Hour)
	if !st.Due || st.Reason != "due" {
		t.Errorf("10 days left must be due: %s", st.Describe())
	}

	expired := selfSignedFor(t, now, now.Add(-24*time.Hour), "skygate.example.com")
	st = NeedsRenewal(expired, now, 30*24*time.Hour)
	if !st.Due {
		t.Error("an already-expired certificate must be due")
	}
	if st.DaysLeft >= 0 {
		t.Errorf("days_left = %d, want negative for an expired certificate", st.DaysLeft)
	}
}

// TestB469_MissingAndUnreadableAreDifferentFacts is the whole point: neither may read as "fresh".
func TestB469_MissingAndUnreadableAreDifferentFacts(t *testing.T) {
	now := time.Now().UTC()
	st := NeedsRenewal(nil, now, 30*24*time.Hour)
	if st.Reason != "no-cert" || !st.Due {
		t.Errorf("empty file: %+v, want reason=no-cert and due (nothing to renew, but not fresh)", st)
	}
	// no-cert is due=true so the caller reaches the "nothing to continue" branch instead of
	// silently reporting a healthy loop.
	st = NeedsRenewal([]byte("this is not a certificate"), now, 30*24*time.Hour)
	if st.Reason != "unreadable" || st.Detail == "" || st.Due {
		t.Errorf("garbage: %+v, want reason=unreadable with a detail and NOT due (a parse failure is not a reason to hit the CA)", st)
	}
	if !strings.Contains(st.Describe(), "unreadable") || !strings.Contains(st.Describe(), "detail=") {
		t.Errorf("Describe() hides the reason: %q", st.Describe())
	}
}

// TestB469_DisabledWindowIsNamed — a zero window is a configuration error, not "never renew
// silently".
func TestB469_DisabledWindowIsNamed(t *testing.T) {
	st := NeedsRenewal(nil, time.Now().UTC(), 0)
	if st.Reason != "disabled" || st.Due {
		t.Errorf("zero window: %+v, want reason=disabled and not due", st)
	}
}

// TestB469_WindowBoundaryIsInclusive — exactly at the window edge the renewal is due: a 90-day
// certificate renews on day 60, not on day 60-and-a-bit.
func TestB469_WindowBoundaryIsInclusive(t *testing.T) {
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	cert := selfSignedFor(t, now, now.Add(30*24*time.Hour), "skygate.example.com")
	if st := NeedsRenewal(cert, now, 30*24*time.Hour); !st.Due {
		t.Errorf("exactly at the window edge the renewal must be due: %s", st.Describe())
	}
}

// mustKey returns a fresh EC key (the decision under test only needs a valid signer).
func mustKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}
