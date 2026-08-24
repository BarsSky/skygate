package oidc

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// AuthCodeEntry is the per-code metadata stored
// between the /oidc/authorize redirect and the
// /oidc/token code exchange. RFC 6749 sec 4.1.2
// requires the server to keep at least:
//
//   - the client_id + redirect_uri (so the token
//     request can be validated against them)
//   - the scopes + user identity (so the id_token
//     claims are correct)
//
// We also keep PKCE (RFC 7636) state when the
// client uses it. B161.2 stores everything in
// memory; B162+ may move to a DB-backed store for
// HA deploys (the auth code is short-lived so
// losing it on a restart is acceptable — the
// client retries the flow).
type AuthCodeEntry struct {
	UserID      int64
	Username    string
	Email       string
	ClientID    string
	RedirectURI string
	Scope       string
	Nonce       string
	// PKCE (RFC 7636). Empty when the client
	// doesn't use PKCE; otherwise code_challenge
	// must be verified against the code_verifier
	// in the /token request.
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           time.Time
}

// AuthCodeStore is the in-memory map of pending
// auth codes. The map is protected by a single
// sync.Mutex; the hot path is one Get/Put per
// /authorize + /token request, and a periodic
// sweep to delete expired entries.
//
// B161.2 ships a single-instance store. HA
// deploys would need to swap this for a Redis
// or DB-backed store; the B161.2 methods are
// the same interface, so the swap is a one-file
// change.
type AuthCodeStore struct {
	mu    sync.Mutex
	codes map[string]AuthCodeEntry
	// ttl is how long an auth code is valid.
	// RFC 6749 sec 4.1.2 RECOMMENDS 10 minutes;
	// we use 5 minutes for slightly tighter
	// security (the user is sitting at a screen
	// waiting for the redirect anyway).
	ttl time.Duration
}

// NewAuthCodeStore returns a fresh store.
func NewAuthCodeStore() *AuthCodeStore {
	return &AuthCodeStore{
		codes: make(map[string]AuthCodeEntry),
		ttl:   5 * time.Minute,
	}
}

// Put stores the entry under a fresh random
// auth code. The code is returned to the caller
// (to embed in the redirect). The code is 32
// random bytes (256 bits) — well above the
// RFC 6749 sec 10.10 minimum of 128 bits.
//
// The function panics on rand failure (which
// means the OS CSPRNG is broken — fail closed).
func (s *AuthCodeStore) Put(entry AuthCodeEntry) string {
	code, err := generateAuthCode()
	if err != nil {
		// crypto/rand.Read failure is OS-level
		// (entropy exhausted) and is unrecoverable.
		// We panic because there's no useful
		// recovery path; we'd rather crash the
		// process than issue a low-entropy code
		// that an attacker could guess.
		panic("oidc: crypto/rand failed: " + err.Error())
	}
	entry.ExpiresAt = time.Now().Add(s.ttl)
	s.mu.Lock()
	s.codes[code] = entry
	s.mu.Unlock()
	return code
}

// Get returns the entry for the code, deletes it
// from the store, and returns nil if the code
// doesn't exist OR has expired. RFC 6749 sec
// 4.1.2 requires one-time use — a code is
// consumed on the first /token request, not the
// first VALID /token request. We delete
// immediately to prevent replay even when the
// request fails (e.g. wrong PKCE verifier).
func (s *AuthCodeStore) Get(code string) (AuthCodeEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.codes[code]
	if !ok {
		return AuthCodeEntry{}, false
	}
	delete(s.codes, code)
	if time.Now().After(entry.ExpiresAt) {
		return AuthCodeEntry{}, false
	}
	return entry, true
}

// Sweep removes all expired entries. Called
// periodically (every minute) by a goroutine
// in main.go to bound the memory footprint.
// The store is never emptied (the user could be
// in the middle of a /authorize + /token round
// trip; deleting their entry mid-flight would
// fail the auth silently). The sweep is a
// background cleanup, not a TTL enforcement.
func (s *AuthCodeStore) Sweep() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	removed := 0
	for code, entry := range s.codes {
		if now.After(entry.ExpiresAt) {
			delete(s.codes, code)
			removed++
		}
	}
	return removed
}

// Size returns the current number of entries
// (used by the /readyz endpoint + tests).
func (s *AuthCodeStore) Size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.codes)
}

// generateAuthCode returns 32 random bytes
// base64url-encoded (no padding). 32 bytes =
// 256 bits, well above the OIDC-recommended
// 128 bits (RFC 6749 sec 10.10).
func generateAuthCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
