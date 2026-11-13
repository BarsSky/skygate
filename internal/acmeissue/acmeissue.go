// Package acmeissue — B467: obtain a certificate from Let's Encrypt over HTTP-01, from the
// panel, without a shell.
//
// WHY THIS EXISTS. The operator asked the question this package answers: «есть ли в самом
// skygate удобный процесс формирования сертификатов … по кнопке сформировать сертификат …
// через Let's Encrypt». Measured on v1.5.212 the answer was no twice over: there was no ACME
// client at all (`grep -r acme go.mod` found nothing, `docs/ha.md` Phase 4 said «LE acquisition
// DEFERRED (G4)»), and the one button that existed (/admin/certificates «Загрузить») called a
// callback that nothing wired, so it wrote nothing while saying «Cert uploaded».
//
// WHY HTTP-01 AND NOT DNS-01. DNS-01 is the better fit for a wildcard and needs no reachable
// port, but it requires a TXT record through the operator's DNS provider, and
// `internal/dnsexternal` is a placeholder today: A-records only, no TXT. HTTP-01 needs one
// thing — `http://<domain>/.well-known/acme-challenge/<token>` must reach THIS process — and
// that is a routing question the operator can answer (port 80 forwarded, or the proxy sending
// that path here). This package therefore ships the challenge endpoint and STATES the
// precondition, instead of pretending the CA can reach a port nobody opened.
//
// WHAT IT DELIBERATELY DOES NOT DO. It does not touch the operator's proxy configuration, it
// does not cache certificates (certsync.Publish owns the files), and it does not decide policy
// (when to renew is the caller's business). It also refuses a domain it cannot validate: an
// ACME order costs a rate-limited attempt, and a typo must fail before the CA is asked.
package acmeissue

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

// Directory URLs. The staging endpoint issues certificates signed by an untrusted CA, so it
// is the safe place for a first drill — the operator can see the whole flow work before the
// production rate limits are involved.
const (
	LetsEncryptProduction = acme.LetsEncryptURL
	LetsEncryptStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// ChallengePathPrefix is the path the CA fetches. It is fixed by RFC 8555 and must be served
// from the DOMAIN ROOT — a proxy that forwards only /admin cannot carry a challenge.
const ChallengePathPrefix = "/.well-known/acme-challenge/"

// ResolveDirectory answers "which CA will this attempt talk to": an explicit URL wins, then
// the staging switch, then production. Staging is chosen by a boolean rather than by editing
// the URL so the panel can offer it as a checkbox without a restart.
func ResolveDirectory(explicit string, staging bool) string {
	if v := strings.TrimSpace(explicit); v != "" {
		return v
	}
	if staging {
		return LetsEncryptStaging
	}
	return LetsEncryptProduction
}

// ValidateDomains parses the operator's input into the SAN list.
//
// The rules are the ones the CA applies minus the ones it learns too late: lowercase, no
// scheme, no path, no port, no IP literal, no wildcard (HTTP-01 cannot answer a wildcard
// authorization — that would need DNS-01), at least one dot, and a valid hostname per
// RFC 1123. A rejected value is named, because "invalid domain" after a rate-limited attempt
// teaches nothing.
func ValidateDomains(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == ';'
	})
	if len(fields) == 0 {
		return nil, errors.New("no domain given")
	}
	seen := make(map[string]bool, len(fields))
	var out []string
	for _, f := range fields {
		d := strings.ToLower(strings.TrimSpace(f))
		if d == "" {
			continue
		}
		if strings.ContainsAny(d, "/:") || strings.Contains(d, "://") {
			return nil, fmt.Errorf("%q: give a bare hostname (no scheme, no path, no port)", f)
		}
		if strings.HasPrefix(d, "*.") {
			return nil, fmt.Errorf("%q: a wildcard needs DNS-01, which this installation cannot do (no TXT support) — list the names explicitly", f)
		}
		if net.ParseIP(d) != nil {
			return nil, fmt.Errorf("%q: an IP address cannot carry a certificate from a public CA", f)
		}
		if !strings.Contains(d, ".") {
			return nil, fmt.Errorf("%q: not a fully qualified name", f)
		}
		for _, label := range strings.Split(d, ".") {
			if label == "" || len(label) > 63 {
				return nil, fmt.Errorf("%q: bad label %q", f, label)
			}
			if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return nil, fmt.Errorf("%q: a label may not start or end with '-'", f)
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
					return nil, fmt.Errorf("%q: illegal character %q in a hostname", f, string(r))
				}
			}
		}
		if len(d) > 253 {
			return nil, fmt.Errorf("%q: longer than 253 characters", f)
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no domain given")
	}
	// Deterministic order: the same request must produce the same CSR, so a diff of two
	// attempts shows what actually changed.
	sort.Strings(out)
	return out, nil
}

// Tokens is the HTTP-01 key-authorization store the CA's fetch is answered from.
//
// In memory on purpose: an authorization lives for seconds-to-minutes inside ONE issuance
// request, and a token left on disk would be a credential nobody rotates.
type Tokens struct {
	mu sync.RWMutex
	m  map[string]string
}

// NewTokens returns an empty store.
func NewTokens() *Tokens { return &Tokens{m: map[string]string{}} }

// Put stores the key authorization for one token.
func (t *Tokens) Put(token, keyAuth string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[keyAuthKey(token)] = keyAuth
}

// Remove forgets a token (called once the authorization is valid).
func (t *Tokens) Remove(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, keyAuthKey(token))
}

// Lookup returns the key authorization for a token.
func (t *Tokens) Lookup(token string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.m[keyAuthKey(token)]
	return v, ok
}

// Len reports how many authorizations are outstanding (used by the page's status line and by
// the tests).
func (t *Tokens) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.m)
}

// keyAuthKey normalises a token before it is used as a map key, because the token arrives from
// the network: a path segment, already validated by the mux, but still trimmed.
func keyAuthKey(token string) string { return strings.TrimSpace(token) }

// Handler serves the challenge path. It answers ONLY the exact prefix + one segment, and 404
// otherwise, so it can be mounted at the domain root without becoming a catch-all.
func Handler(t *Tokens) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, ChallengePathPrefix)
		if token == "" || strings.Contains(token, "/") {
			http.NotFound(w, r)
			return
		}
		keyAuth, ok := t.Lookup(token)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(keyAuth))
	})
}

// LoadOrCreateAccountKey reads the ACME account key, or creates one on first use.
//
// The account key identifies this installation to the CA for the lifetime of the host, so it
// lives in the data directory (not /tmp), is 0600, and is ECDSA P-256 by default — small,
// fast, and accepted by every current CA.
func LoadOrCreateAccountKey(path string) (*ecdsa.PrivateKey, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("acme: account key path is empty")
	}
	if raw, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, fmt.Errorf("acme: %s holds no PEM block", path)
		}
		key, perr := x509.ParseECPrivateKey(block.Bytes)
		if perr != nil {
			return nil, fmt.Errorf("acme: parse account key %s: %w", path, perr)
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("acme: read account key %s: %w", path, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("acme: generate account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("acme: marshal account key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("acme: create %s: %w", filepath.Dir(path), err)
	}
	body := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	tmp := path + ".new"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return nil, fmt.Errorf("acme: write account key: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("acme: install account key: %w", err)
	}
	return key, nil
}

// Issuer performs one HTTP-01 issuance.
type Issuer struct {
	// DirectoryURL is the CA endpoint (see ResolveDirectory).
	DirectoryURL string
	// Email is the account contact. Optional: an account without a contact still works, but
	// the CA cannot warn about an expiring certificate.
	Email string
	// Key is the account key (LoadOrCreateAccountKey).
	Key *ecdsa.PrivateKey
	// Tokens answers the CA's fetch. Required.
	Tokens *Tokens
	// Logger receives the step-by-step decisions. Nil = log.Printf.
	Logger func(format string, args ...any)
	// HTTPClient is the CA transport. Nil = the acme package's default (10s timeout).
	HTTPClient *http.Client
}

// Result is what one issuance produced.
type Result struct {
	Domains []string
	CertPEM []byte
	KeyPEM  []byte
	// AccountURL is the CA's account handle (for the status line: it proves which account
	// holds the order, which matters when an operator later asks "who owns this cert").
	AccountURL string
	// Elapsed is how long the whole order took, including the CA's validation round trip.
	Elapsed time.Duration
}

// Issue obtains a certificate for exactly the given domains.
//
// The order is the honest one: register the account, ask for ONE order covering every SAN,
// answer each authorization on the HTTP path, wait for the CA to say it validated, then
// finalize. A failure names the STEP, because «issuance failed» with no step is the reason an
// operator retries into a rate limit.
func (i *Issuer) Issue(ctx context.Context, domains []string) (Result, error) {
	logf := i.Logger
	if logf == nil {
		logf = log.Printf
	}
	res := Result{Domains: append([]string(nil), domains...)}
	started := time.Now()
	defer func() { res.Elapsed = time.Since(started) }()

	if i.Key == nil {
		return res, errors.New("acme: no account key")
	}
	if i.Tokens == nil {
		return res, errors.New("acme: no challenge token store")
	}
	if len(domains) == 0 {
		return res, errors.New("acme: no domains")
	}
	client := &acme.Client{Key: i.Key, DirectoryURL: i.DirectoryURL, HTTPClient: i.HTTPClient}

	acct := &acme.Account{}
	if i.Email != "" {
		acct.Contact = []string{"mailto:" + i.Email}
	}
	reg, err := client.Register(ctx, acct, acme.AcceptTOS)
	switch {
	case err == nil:
		res.AccountURL = reg.URI
		logf("acme: account registered (%s)", reg.URI)
	case errors.Is(err, acme.ErrAccountAlreadyExists):
		// Expected on every attempt after the first: the key IS the account.
		res.AccountURL = string(client.KID)
		logf("acme: account already exists for this key (%s)", client.KID)
	default:
		return res, fmt.Errorf("acme: register account at %s: %w", i.DirectoryURL, err)
	}

	ids := make([]acme.AuthzID, 0, len(domains))
	for _, d := range domains {
		ids = append(ids, acme.AuthzID{Type: "dns", Value: d})
	}
	order, err := client.AuthorizeOrder(ctx, ids)
	if err != nil {
		return res, fmt.Errorf("acme: create order: %w", err)
	}
	logf("acme: order %s created for %s", order.URI, strings.Join(domains, ", "))

	answered := 0
	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return res, fmt.Errorf("acme: read authorization %s: %w", authzURL, err)
		}
		if authz.Status == acme.StatusValid {
			continue // a cached authorization from an earlier order in the same account
		}
		chal := pickHTTP01(authz)
		if chal == nil {
			return res, fmt.Errorf("acme: no http-01 challenge offered for %s (status %s)", authz.Identifier.Value, authz.Status)
		}
		keyAuth, err := client.HTTP01ChallengeResponse(chal.Token)
		if err != nil {
			return res, fmt.Errorf("acme: key authorization for %s: %w", authz.Identifier.Value, err)
		}
		i.Tokens.Put(chal.Token, keyAuth)
		answered++
		if _, err := client.Accept(ctx, chal); err != nil {
			i.Tokens.Remove(chal.Token)
			return res, fmt.Errorf("acme: accept challenge for %s: %w", authz.Identifier.Value, err)
		}
		if _, err := client.WaitAuthorization(ctx, authzURL); err != nil {
			i.Tokens.Remove(chal.Token)
			return res, fmt.Errorf("acme: the CA could not validate %s over HTTP-01 (%s%s): %w",
				authz.Identifier.Value, ChallengePathPrefix, chal.Token, err)
		}
		i.Tokens.Remove(chal.Token)
		logf("acme: %s validated over HTTP-01", authz.Identifier.Value)
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return res, fmt.Errorf("acme: generate certificate key: %w", err)
	}
	csrDER, err := buildCSR(certKey, domains)
	if err != nil {
		return res, fmt.Errorf("acme: build CSR: %w", err)
	}
	// CreateOrderCert posts the CSR to the order's FINALIZE url (RFC 8555 §7.4), not to the
	// order url — passing order.URI here sends the CSR to the order resource and gets a 404
	// back from every CA.
	chain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csrDER, true)
	if err != nil {
		return res, fmt.Errorf("acme: finalize order (validated %d authorization(s)): %w", answered, err)
	}
	if len(chain) == 0 {
		return res, errors.New("acme: the CA returned an empty certificate chain")
	}
	for _, der := range chain {
		res.CertPEM = append(res.CertPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	keyDER, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return res, fmt.Errorf("acme: marshal certificate key: %w", err)
	}
	res.KeyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	logf("acme: certificate issued for %s (%d bytes, %d certificate(s) in the chain)",
		strings.Join(domains, ", "), len(res.CertPEM), len(chain))
	return res, nil
}

// pickHTTP01 returns the http-01 challenge, or nil when the CA did not offer one.
func pickHTTP01(authz *acme.Authorization) *acme.Challenge {
	for _, c := range authz.Challenges {
		if c.Type == "http-01" {
			return c
		}
	}
	return nil
}

// buildCSR creates the certificate request with the SAN list as the only names.
func buildCSR(key *ecdsa.PrivateKey, domains []string) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		// CN is deprecated for SAN-carrying certificates but harmless, and some tooling still
		// reads it — the SAN list is what the CA certifies.
		Subject:  pkix.Name{CommonName: domains[0]},
		DNSNames: domains,
	}
	return x509.CreateCertificateRequest(rand.Reader, tmpl, key)
}

// Fingerprint is the short form the page shows next to an issued certificate: the first 16 hex
// characters of the SHA-256, which is enough to distinguish two certificates and short enough
// to read out loud.
func Fingerprint(certPEM []byte) string {
	sum := sha256.Sum256(certPEM)
	return hex.EncodeToString(sum[:8])
}
