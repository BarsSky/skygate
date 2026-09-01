// Package cluster — invite.go owns the cluster_invite signed
// token format and the helpers that issue + verify them.
//
// v1.5.0+ / B200 — Phase 2.2 of docs/internal/cluster-management.md.
//
// Token format (version "sgn1"):
//
//	sgn1.<base64url(payload_json)>.<base64url(hmac_sha256)>
//
// payload_json is a small JSON object:
//
//	{
//	  "inv": "<cluster_invite.id>",
//	  "cid": "<cluster_id>",
//	  "rol": "<role>",
//	  "th":  "<target_hostname>",
//	  "exp": <unix_seconds>
//	}
//
// hmac_sha256 is HMAC-SHA256(SKYGATE_SECRET_KEY, payload_json_bytes).
// The signature proves the token was issued by THIS skygate
// instance. The payload encodes the invite row id so the
// receiver can look it up in cluster_invite to check
// status, used_at, and expires_at. We do NOT trust the
// payload's "exp" alone — the cluster_invite.expires_at
// is the source of truth (the admin can revoke a pending
// invite at any time even if the signature's "exp" hasn't
// been reached yet).
//
// Why a separate "sgn1" prefix:
//   - Future-proofs the format. If we need to migrate to
//     asymmetric keys (Ed25519) or rotate the secret, the
//     new format gets a new prefix ("sgn2") and the old
//     tokens keep working until they expire.
//   - Lets the verifier reject obviously-malformed input
//     (no dot, wrong prefix, wrong arity) without a
//     crypto operation.
//
// Why HMAC-SHA256 (not SHA256 alone, not Ed25519):
//   - HMAC needs the same secret on both sides, which is
//     fine here: the new node already runs skygate, and
//     skygate ships with the same SKYGATE_SECRET_KEY via
//     the join bootstrap. Ed25519 would require a key
//     distribution step that HMAC doesn't.
//   - SHA256 alone is vulnerable to length-extension attacks
//     against a non-secret message. HMAC-SHA256 is not.

package cluster

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// randRead fills b with cryptographically-secure random
// bytes. Wrapped in a package-level var so tests can stub
// it (e.g. to a fixed source for deterministic invite IDs).
var randRead = rand.Read

// ErrInviteNotFound is returned by LookupInvite when no
// cluster_invite row matches the given id.
var ErrInviteNotFound = errors.New("cluster invite not found")

// ErrInviteAlreadyUsed is returned by MarkInviteUsed when
// the invite already has used_at set.
var ErrInviteAlreadyUsed = errors.New("cluster invite already used")

// InvitePayload is the JSON object encoded inside the sgn1
// token. Keep it small — the token goes in QR codes,
// terminal copy-paste, and (eventually) bootstrap scripts.
type InvitePayload struct {
	Inv string `json:"inv"` // cluster_invite.id
	CID string `json:"cid"` // cluster_id
	Rol string `json:"rol"` // role (free-form, e.g. "skygate" / "skygate-standby")
	TH  string `json:"th"`  // target_hostname (the host this invite is intended for)
	Exp int64  `json:"exp"` // unix_seconds; informational, NOT the source of truth
}

// IssueInvite creates a new cluster_invite row and returns
// the signed token (and the row id). The caller is expected
// to display the token in the UI as a one-time copy-paste
// string — the token is NOT recoverable from the row alone
// (the signature is deterministic from payload + secret, but
// the secret should never leave the server).
//
// The returned ID is also the "inv" field of the token
// payload. The signature is HMAC-SHA256(secret,
// canonical_json(payload)).
//
// `ttlHours` is the lifetime of the invite (typically 24).
// `role` is free-form (e.g. "skygate" for the primary
// skygate, "skygate-standby" for a standby node). It is
// only a hint — the verifier doesn't enforce it; the
// joiner declares its own role when it joins.
func IssueInvite(d *sql.DB, clusterID, role, targetHostname string, ttlHours int, secret string) (inviteID, token string, expiresAt time.Time, err error) {
	if secret == "" {
		return "", "", time.Time{}, errors.New("cluster: empty secret — set SKYGATE_SECRET_KEY")
	}
	if ttlHours <= 0 {
		ttlHours = 24
	}
	now := time.Now().UTC()
	expiresAt = now.Add(time.Duration(ttlHours) * time.Hour)
	inviteID = generateInviteID()

	// Build the payload + sign.
	payload := InvitePayload{
		Inv: inviteID,
		CID: clusterID,
		Rol: role,
		TH:  targetHostname,
		Exp: expiresAt.Unix(),
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("marshal payload: %w", err)
	}
	sig := signPayload(secret, canonical)
	token = buildToken(canonical, sig)

	// Persist the row. We store the payload (so the
	// verifier doesn't have to re-decode from the
	// token) AND the signature (for audit) — both are
	// recoverable from the token, but storing them makes
	// /admin/cluster's "View signature" future feature
	// trivial.
	_, err = d.Exec(`
		INSERT INTO cluster_invite (
			id, cluster_id, role, target_hostname,
			issued_at, expires_at, signature, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')
	`, inviteID, clusterID, role, targetHostname,
		now, expiresAt, base64.RawURLEncoding.EncodeToString(sig))
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("insert invite: %w", err)
	}
	return inviteID, token, expiresAt, nil
}

// RevokeInvite marks the cluster_invite row status=revoked.
// Used invites (used_at IS NOT NULL) are NOT revokable —
// the join already happened. Already-revoked invites are
// idempotent (no error).
func RevokeInvite(d *sql.DB, inviteID string) error {
	res, err := d.Exec(`
		UPDATE cluster_invite
		   SET status = 'revoked'
		 WHERE id = $1
		   AND status = 'pending'
		   AND used_at IS NULL
	`, inviteID)
	if err != nil {
		return fmt.Errorf("update invite: %w", err)
	}
	_ = res // we don't care about rowsAffected here — the
	// "already revoked or used" case is a no-op (200 OK
	// on the admin page), not an error.
	return nil
}

// LookupInvite returns the cluster_invite row with the
// given id. Returns ErrInviteNotFound if the row is missing
// or the id is empty.
func LookupInvite(d *sql.DB, inviteID string) (*InviteRow, error) {
	if inviteID == "" {
		return nil, ErrInviteNotFound
	}
	row := d.QueryRow(`
		SELECT id, cluster_id, role, target_hostname,
		       issued_at, expires_at, used_at, used_by_node_id,
		       signature, status
		  FROM cluster_invite
		 WHERE id = $1
	`, inviteID)
	out := &InviteRow{}
	var usedByNode sql.NullString
	var usedAtTime sql.NullTime
	if err := row.Scan(
		&out.ID, &out.ClusterID, &out.Role, &out.TargetHostname,
		&out.IssuedAt, &out.ExpiresAt, &usedAtTime, &usedByNode,
		&out.Signature, &out.Status,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrInviteNotFound
		}
		return nil, err
	}
	if usedAtTime.Valid {
		t := usedAtTime.Time
		out.UsedAt = &t
	}
	if usedByNode.Valid {
		out.UsedByNodeID = usedByNode.String
	}
	return out, nil
}

// InviteRow is the in-memory shape of one cluster_invite
// row. The DB column types (TIMESTAMPTZ → time.Time, TEXT
// → string) are decoded by LookupInvite.
type InviteRow struct {
	ID             string
	ClusterID      string
	Role           string
	TargetHostname string
	IssuedAt       time.Time
	ExpiresAt      time.Time
	UsedAt         *time.Time
	UsedByNodeID   string
	Signature      string
	Status         string // "pending" / "used" / "revoked" / "expired"
}

// IsPending returns true if the invite is still usable
// (status=pending AND expires_at > now AND used_at is null).
// This is the gate the verifier (and the admin UI) use to
// decide whether an invite is actionable.
func (r *InviteRow) IsPending(now time.Time) bool {
	if r == nil {
		return false
	}
	return r.Status == "pending" && r.UsedAt == nil && r.ExpiresAt.After(now)
}

// ---------- Pure helpers (testable without DB) -------------------------

// buildToken returns the sgn1-prefixed token string from
// the canonical payload bytes and the HMAC-SHA256 signature
// (raw 32 bytes, NOT base64-encoded yet).
func buildToken(canonicalJSON, sig []byte) string {
	payload := base64.RawURLEncoding.EncodeToString(canonicalJSON)
	sigEnc := base64.RawURLEncoding.EncodeToString(sig)
	return "sgn1." + payload + "." + sigEnc
}

// signPayload returns HMAC-SHA256(secret, msg).
func signPayload(secret string, msg []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(msg)
	return mac.Sum(nil)
}

// VerifyToken decodes a sgn1 token, checks the HMAC, and
// returns the decoded payload. Does NOT consult the DB —
// the caller (typically the join bootstrap on the new
// node) decides whether to look up the cluster_invite
// row to check status / expires_at / used_at.
//
// Returns an error on:
//   - empty input
//   - missing "sgn1." prefix
//   - wrong arity (not exactly 2 dots)
//   - invalid base64
//   - HMAC mismatch (constant-time compared)
func VerifyToken(secret, token string) (*InvitePayload, error) {
	if secret == "" {
		return nil, errors.New("cluster: empty secret — set SKYGATE_SECRET_KEY")
	}
	if token == "" {
		return nil, errors.New("cluster: empty token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "sgn1" {
		return nil, fmt.Errorf("cluster: malformed token (want sgn1.<payload>.<sig>, got %q)", truncate(token, 32))
	}
	canonical, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("cluster: payload b64: %w", err)
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("cluster: sig b64: %w", err)
	}
	wantSig := signPayload(secret, canonical)
	// constant-time compare — hmac.Equal handles it
	if !hmac.Equal(gotSig, wantSig) {
		return nil, errors.New("cluster: signature mismatch")
	}
	var p InvitePayload
	if err := json.Unmarshal(canonical, &p); err != nil {
		return nil, fmt.Errorf("cluster: payload json: %w", err)
	}
	return &p, nil
}

// generateInviteID returns a 16-char hex string (8 random
// bytes) — short enough to be readable in the admin UI,
// long enough to avoid collisions in a single cluster
// (2^32 space; birthday paradox at ~65k invites).
//
// We use crypto/rand because the ID is half of the auth
// material (the HMAC is the other half). math/rand would
// be predictable.
func generateInviteID() string {
	var b [8]byte
	if _, err := randRead(b[:]); err != nil {
		// extremely unlikely on Linux; fall back to a
		// time-based id so we never return an empty string
		return fmt.Sprintf("inv-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b[:])
}

// truncate returns the first n bytes of s followed by "..."
// if it was longer. Used in error messages to avoid
// leaking full tokens in logs.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
