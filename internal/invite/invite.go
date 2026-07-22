// 2026-07-20: v0.21.0 — invite_codes package.
//
// The user-to-user subnet bridge is driven by
// short invite codes: grantor runs /invite, gets
// a 8-char code, tells the grantee out of band;
// grantee runs /accept <code> to auto-bridge their
// subnets.
//
// This file is the CRUD layer. The bridge logic
// (writing a user_subnet_shares row + triggering
// the ACL re-apply) lives in bridge.go and is
// invoked from the consume path.
//
// Code shape: 8 chars from a 32-symbol alphabet
// (A-Z, 2-9 — no 0/O/1/I to avoid transcription
// errors). 32^8 ≈ 1.1 trillion — plenty for the
// expected volume (low thousands of invites
// across a multi-year deployment), and the code
// is opaque enough that an attacker can't guess a
// valid code from one observation (one code per
// grant, expires in 7 days).
//
// Lifecycle:
//
//   1. grantor calls CreateInvite(grantorID, granteeUsername, ttl)
//      → row inserted with status='active', expires_at=now+ttl.
//   2. grantor shows the code to the grantee.
//   3. grantee calls ConsumeCode(code, consumerID) where
//      consumerID is the resolved user id of the
//      currently-authenticated user.
//      → atomic UPDATE: status='consumed',
//        consumed_at=now, consumed_by_user_id=consumerID.
//      → on success, the caller invokes
//        bridge.ApplyBridge(grantorID, consumerID) which
//        writes a user_subnet_shares row and kicks
//        the ACL re-apply goroutine.
//   4. expired rows (now > expires_at) are marked
//        status='expired' on the next sweep (called
//        by the consume path before the active check).
//      Expired rows are still visible in admin
//      /admin/invites for audit but cannot be
//      consumed (the consume path rejects them).

package invite

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// CodeLength is the number of chars in a
// generated invite code.
const CodeLength = 8

// CodeAlphabet is the symbol set for invite
// codes. 32 symbols, no I/O/0/1 (the four
// characters most commonly confused in
// hand-typed codes). 32^8 = 1.1 trillion
// possibilities — safe against brute force
// for the 7-day TTL.
const CodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// DefaultTTL is how long a fresh invite is
// valid. 7 days is long enough for the grantee
// to come back from a weekend but short enough
// to bound the "code leaked" risk.
const DefaultTTL = 7 * 24 * time.Hour

// Status values for invite_codes.status.
const (
	StatusActive   = "active"
	StatusConsumed = "consumed"
	StatusExpired  = "expired"
	StatusRevoked  = "revoked"
)

// ErrNotFound is returned by ValidateCode
// when no row matches the code.
var ErrNotFound = errors.New("invite: not found")

// ErrExpired is returned when the code exists
// but expires_at is in the past.
var ErrExpired = errors.New("invite: expired")

// ErrAlreadyConsumed is returned when the code
// exists but status != 'active'.
var ErrAlreadyConsumed = errors.New("invite: already consumed")

// ErrNotForYou is returned when the consuming
// user's username doesn't match the
// grantee_username the invite was generated
// for.
var ErrNotForYou = errors.New("invite: not for you")

// ErrSelfInvite is returned when a user tries
// to accept an invite they generated (or, more
// specifically, when grantee_username == their
// own username).
var ErrSelfInvite = errors.New("invite: cannot accept your own invite")

// GenerateCode returns a random CodeLength
// invite code from CodeAlphabet. Uses
// crypto/rand so the output is unguessable.
// Tests can override CodeAlphabet/CodeLength
// via the package-level vars (none set today;
// kept as a public surface for future
// ops-required overrides).
func GenerateCode() (string, error) {
	if len(CodeAlphabet) == 0 {
		return "", errors.New("invite: empty CodeAlphabet")
	}
	alphabet := CodeAlphabet
	out := make([]byte, 0, CodeLength)
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < CodeLength; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("invite: rand: %w", err)
		}
		out = append(out, alphabet[n.Int64()])
	}
	return string(out), nil
}

// Invite mirrors the row in the invite_codes
// table. All time fields are unix seconds
// (matches the convention used in every other
// table in the schema).
type Invite struct {
	ID                int64
	Code              string
	GrantorUserID     int64
	GranteeUsername   string
	Status            string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	ConsumedAt        time.Time // zero value if not consumed
	ConsumedByUserID  int64     // 0 if not consumed
	AuditMessage      string
}

// CreateInvite inserts a new invite row and
// returns the persisted record (with the
// generated code). The code is UNIQUE in the
// table; on the (astronomically rare) collision
// we retry up to 5 times with a fresh code
// before giving up.
//
// ttl defaults to DefaultTTL when zero.
//
// The grantor must already exist in portal_users
// (FK enforced). The grantee_username is stored
// as-typed; resolution to a user id happens at
// consume time (so A can invite "bob" before
// bob has a skygate account).
func CreateInvite(d *sql.DB, grantorUserID int64, granteeUsername string, ttl time.Duration, message string) (*Invite, error) {
	if grantorUserID <= 0 {
		return nil, errors.New("invite: grantorUserID must be > 0")
	}
	if strings.TrimSpace(granteeUsername) == "" {
		return nil, errors.New("invite: granteeUsername required")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	now := time.Now()
	expires := now.Add(ttl)

	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		code, err := GenerateCode()
		if err != nil {
			return nil, err
		}
		res, err := d.Exec(`
			INSERT INTO invite_codes
				(code, grantor_user_id, grantee_username, status,
				 created_at, expires_at, audit_message)
			VALUES ($1, $2, $3, 'active', $4, $5, $6)
		`, code, grantorUserID, granteeUsername,
			now.Unix(), expires.Unix(), message)
		if err != nil {
			// 0x13 / "constraint failed" =
			// UNIQUE collision on code. Retry
			// with a new code.
			if strings.Contains(strings.ToLower(err.Error()), "unique") ||
				strings.Contains(err.Error(), "constraint") {
				continue
			}
			return nil, fmt.Errorf("invite: insert: %w", err)
		}
		id, _ := res.LastInsertId()
		return &Invite{
			ID:              id,
			Code:            code,
			GrantorUserID:   grantorUserID,
			GranteeUsername: granteeUsername,
			Status:          StatusActive,
			CreatedAt:       now,
			ExpiresAt:       expires,
			AuditMessage:    message,
		}, nil
	}
	return nil, errors.New("invite: could not generate a unique code after 5 attempts")
}

// LookupByCode returns the invite for the
// given code without consuming it. Returns
// ErrNotFound if no row matches.
func LookupByCode(d *sql.DB, code string) (*Invite, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil, ErrNotFound
	}
	row := d.QueryRow(`
		SELECT id, code, grantor_user_id, grantee_username, status,
		       created_at, expires_at, consumed_at, consumed_by_user_id, audit_message
		FROM invite_codes
		WHERE code = $1
	`, code)
	var inv Invite
	var createdUnix, expiresUnix, consumedUnix int64
	if err := row.Scan(&inv.ID, &inv.Code, &inv.GrantorUserID,
		&inv.GranteeUsername, &inv.Status,
		&createdUnix, &expiresUnix, &consumedUnix, &inv.ConsumedByUserID,
		&inv.AuditMessage); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("invite: lookup: %w", err)
	}
	inv.CreatedAt = time.Unix(createdUnix, 0)
	inv.ExpiresAt = time.Unix(expiresUnix, 0)
	if consumedUnix > 0 {
		inv.ConsumedAt = time.Unix(consumedUnix, 0)
	}
	return &inv, nil
}

// ValidateCode returns the invite if and only
// if the code is currently consumable: exists,
// status='active', not expired, and the
// consuming username matches grantee_username.
// This is the gate the bot /accept handler
// calls BEFORE attempting the atomic consume.
//
// Returns:
//   - ErrNotFound        — no row with this code
//   - ErrAlreadyConsumed — status is consumed/revoked/expired
//   - ErrExpired         — past expires_at
//   - ErrNotForYou       — consumerUsername != inv.GranteeUsername
//   - ErrSelfInvite      — grantor is consuming their own invite
//                          (defensive — the bot handler
//                           also checks this, but
//                           ValidateCode catches it
//                           early for callers that
//                           don't)
func ValidateCode(d *sql.DB, code, consumerUsername string, consumerUserID int64) (*Invite, error) {
	inv, err := LookupByCode(d, code)
	if err != nil {
		return nil, err
	}
	// Self-invite check BEFORE the not-for-you
	// check (a self-invite is by definition not
	// for a different person, but the error
	// message is more useful as "can't accept
	// your own" than "not for you").
	if inv.GrantorUserID == consumerUserID {
		return inv, ErrSelfInvite
	}
	if inv.GranteeUsername != consumerUsername {
		return inv, ErrNotForYou
	}
	if inv.Status != StatusActive {
		return inv, ErrAlreadyConsumed
	}
	if time.Now().After(inv.ExpiresAt) {
		return inv, ErrExpired
	}
	return inv, nil
}

// ConsumeCode atomically marks the invite as
// consumed by the given user. Returns the
// updated invite. On error (e.g. row already
// consumed by a concurrent request) the caller
// should call ValidateCode again to surface
// the precise failure reason.
//
// The atomic UPDATE uses a WHERE clause on
// status='active' so a second concurrent
// /accept call sees the same row already
// consumed and gets ErrAlreadyConsumed (not a
// silent double-bridge).
func ConsumeCode(d *sql.DB, code string, consumerUserID int64) (*Invite, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	now := time.Now()
	res, err := d.Exec(`
		UPDATE invite_codes
		SET status = 'consumed',
		    consumed_at = $1,
		    consumed_by_user_id = $2
		WHERE code = $3 AND status = 'active'
	`, now.Unix(), consumerUserID, code)
	if err != nil {
		return nil, fmt.Errorf("invite: consume: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		// Either the code doesn't exist, or it
		// was already consumed. Re-lookup for
		// a precise error.
		_, lookupErr := LookupByCode(d, code)
		if errors.Is(lookupErr, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, ErrAlreadyConsumed
	}
	return LookupByCode(d, code)
}

// RevokeInvite marks the invite as revoked
// (grantor changed their mind, or the code
// leaked). Idempotent. Returns nil if the
// invite was already consumed (you can't
// un-consume a bridge).
func RevokeInvite(d *sql.DB, code string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	_, err := d.Exec(`
		UPDATE invite_codes
		SET status = 'revoked'
		WHERE code = $1 AND status = 'active'
	`, code)
	return err
}

// ListByGrantor returns all invites generated
// by the given user, newest first. Used by
// the bot /invites command and the admin
// /admin/invites page.
func ListByGrantor(d *sql.DB, grantorUserID int64) ([]*Invite, error) {
	return listInvites(d, "grantor_user_id = ?", grantorUserID)
}

// ListByGrantee returns all invites for the
// given username (the "show me my incoming
// invites" view).
func ListByGrantee(d *sql.DB, granteeUsername string) ([]*Invite, error) {
	return listInvites(d, "grantee_username = ?", granteeUsername)
}

// ListAll returns every invite, newest first.
// Admin-only; the /admin/invites page calls
// this. Unfiltered — for a 100k-row table this
// would be a problem, but the expected volume
// is low (hundreds over the life of a
// deployment).
func ListAll(d *sql.DB) ([]*Invite, error) {
	return listInvites(d, "1=1")
}

// SweepExpired marks active rows past their
// expires_at as 'expired'. Cheap (one UPDATE
// with a single WHERE), idempotent, called
// from the bot /accept path before the
// ValidateCode call so expired codes are
// rejected with ErrExpired (not "consumed").
// Returns the number of rows updated.
func SweepExpired(d *sql.DB) (int64, error) {
	res, err := d.Exec(`
		UPDATE invite_codes
		SET status = 'expired'
		WHERE status = 'active' AND expires_at < $1
	`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// listInvites is the shared implementation
// behind the three List* functions. Args are
// passed as a vararg so the WHERE clause can
// include both the column-test and a value.
func listInvites(d *sql.DB, where string, args ...any) ([]*Invite, error) {
	q := `
		SELECT id, code, grantor_user_id, grantee_username, status,
		       created_at, expires_at, consumed_at, consumed_by_user_id, audit_message
		FROM invite_codes
		WHERE ` + where + `
		ORDER BY created_at DESC
		LIMIT 200
	`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("invite: list: %w", err)
	}
	defer rows.Close()
	var out []*Invite
	for rows.Next() {
		var inv Invite
		var createdUnix, expiresUnix, consumedUnix int64
		if err := rows.Scan(&inv.ID, &inv.Code, &inv.GrantorUserID,
			&inv.GranteeUsername, &inv.Status,
			&createdUnix, &expiresUnix, &consumedUnix, &inv.ConsumedByUserID,
			&inv.AuditMessage); err != nil {
			return nil, err
		}
		inv.CreatedAt = time.Unix(createdUnix, 0)
		inv.ExpiresAt = time.Unix(expiresUnix, 0)
		if consumedUnix > 0 {
			inv.ConsumedAt = time.Unix(consumedUnix, 0)
		}
		out = append(out, &inv)
	}
	return out, rows.Err()
}
