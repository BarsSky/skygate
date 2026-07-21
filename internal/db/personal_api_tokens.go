package db

import (
	"database/sql"
	"time"
)

// personal_api_tokens  —  helpers
//
// 2026-07-11: refactor v0.6.0 (Этап 10 part 2). Before this file the
// same SQL strings were duplicated across 3 handler files:
//
//   handlers_api_tokens.go  — list / create / revoke (3 call sites)
//   handlers.go             — Bearer auth lookup + last_used touch (2 sites)
//   handlers_admin_users.go — user-delete cascade (was MISSING — see below)
//
// Five read+write helpers + one cascade helper.
//
// Why no GetAPITokenByID? Lookups by primary key (id) are only used
// for the user-scoped delete (DELETE … WHERE id=? AND user_id=?) and
// for nothing else. Splitting that into a Get-then-Delete pair would
// just add a round-trip and a TOCTOU window. DeleteAPITokenByUser
// returns rows affected so the caller can detect "already gone" if
// it cares.
//
// Why is DeleteAPITokensByUserID here at all? The pre-Этап-10
// admin delete handler cleaned up preauth_keys and audit_log but
// silently left personal_api_tokens rows behind — they'd be
// orphaned forever. Portal users are personal (each token belongs
// to a single user), so we cascade here. The PostAdminDeleteUser
// handler now calls this helper in the same transaction-shaped
// sequence it uses for the other two cleanups.

// APIToken is the row shape used by ListAPITokensByUser. LastUsed
// is a time.Time so callers can use IsZero() for the "never used"
// display, and Format("2006-01-02 15:04") for the table cell.
// CreatedAt is always populated (DEFAULT in the schema).
//
// 2026-07-16: v0.15.5 — added ExpiresAt (TTL) + AutoRotate
// fields. ExpiresAt is the unix timestamp at which the
// token stops being valid; the Bearer-auth path (handlers.go
// AuthenticateBearer) rejects tokens past ExpiresAt. AutoRotate
// is the operator's "rotate this before it expires" flag —
// the actual rotation job is a v0.16.0 follow-up, but the
// column is in v0.15.5 so the UI can read it.
type APIToken struct {
	ID         int64
	Label      string
	LastUsed   time.Time
	CreatedAt  time.Time
	ExpiresAt  time.Time
	AutoRotate bool
}

// APITokenLookup is the row shape used by ListAPITokenHashesForLookup.
// The Bearer-auth path needs the username and is_admin flag to build
// the JWT claims, and the bcrypt token_hash to compare against the
// incoming raw token. This is a linear scan by design — bcrypt is
// not a keyed hash, so we have to CompareHashAndPassword every
// candidate. The portal typically has a small handful of tokens so
// the O(N) cost is fine; if it ever grows, add a HMAC SHA-256 column
// for an indexed pre-filter.
//
// 2026-07-16: v0.15.5 — added ExpiresAt so the lookup
// returns the expiry timestamp; AuthenticateBearer compares
// it against time.Now() and returns 401 if past.
type APITokenLookup struct {
	UserID    int64
	Username  string
	IsAdmin   bool
	TokenHash string
	// ExpiresAt is the unix timestamp at which the token
	// stops being valid. 0 = never (the pre-v0.15.5
	// behaviour). The Bearer-auth path treats 0 as
	// "no expiry" and skips the time check.
	ExpiresAt int64
}

// ListAPITokensByUser returns every API token belonging to userID,
// ordered newest-first. The /my/tokens page renders this directly.
// The returned slice is non-nil but may be empty (a user with zero
// tokens gets []APIToken{}, not nil — same convention the handler
// used to enforce with `if tokens == nil { tokens = []tRow{} }`).
func ListAPITokensByUser(d *sql.DB, userID int64) ([]APIToken, error) {
	rows, err := d.Query(qSelectAPITokensByUser, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		var lu, cr, exp int64
		var autoR int
		if err := rows.Scan(&t.ID, &t.Label, &lu, &cr, &exp, &autoR); err != nil {
			return nil, err
		}
		if lu > 0 {
			t.LastUsed = time.Unix(lu, 0)
		}
		t.CreatedAt = time.Unix(cr, 0)
		if exp > 0 {
			t.ExpiresAt = time.Unix(exp, 0)
		}
		t.AutoRotate = autoR == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListAPITokenHashesForLookup returns every token's (user_id,
// username, is_admin, token_hash) for the Bearer-auth fast path.
// The handler iterates the result and calls auth.CheckAPIToken on
// each hash until it finds a match, then calls
// TouchAPITokenLastUsed and returns claims. Returns an empty slice
// (not nil) if there are zero tokens in the system.
//
// 2026-07-16: v0.15.5 — added expires_at so the lookup
// returns the expiry timestamp; the handler filters out
// expired tokens before granting claims.
func ListAPITokenHashesForLookup(d *sql.DB) ([]APITokenLookup, error) {
	rows, err := d.Query(qSelectAllAPITokensForLookup)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APITokenLookup{}
	for rows.Next() {
		var a APITokenLookup
		var adminI int
		if err := rows.Scan(&a.UserID, &a.Username, &adminI, &a.TokenHash, &a.ExpiresAt); err != nil {
			return nil, err
		}
		a.IsAdmin = adminI == 1
		out = append(out, a)
	}
	return out, rows.Err()
}

// InsertAPIToken writes a new personal_api_tokens row. Called by
// PostMyToken after auth.GenerateAPIToken produces (raw, hash).
// Returns the new row's id, mostly so tests can verify the insert
// happened. Callers that don't care can ignore it.
//
// 2026-07-16: v0.15.5 — added expiresAt + autoRotate params.
// expiresAt is the unix timestamp at which the token stops
// being valid; 0 = never expires (the pre-v0.15.5 behaviour).
// autoRotate is the operator's "rotate this before expiry"
// flag; the actual rotation job is a v0.16.0 follow-up but
// the column is in v0.15.5 so the UI can store + read it.
func InsertAPIToken(d *sql.DB, userID int64, tokenHash, label string, expiresAt int64, autoRotate bool) (int64, error) {
	autoR := 0
	if autoRotate {
		autoR = 1
	}
	res, err := d.Exec(qInsertAPIToken, userID, tokenHash, label, expiresAt, autoR)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteAPITokenByUser removes the token with the given id IF it
// belongs to userID. Returns rows affected so callers can detect
// "wrong user" or "already revoked". The handler currently ignores
// both cases (it always returns 302 to /my/tokens?revoked=1 even
// when the row didn't exist), but the helper exposes the info
// in case the UI later wants to show an error.
func DeleteAPITokenByUser(d *sql.DB, id, userID int64) (int64, error) {
	res, err := d.Exec(qDeleteAPITokenByUser, id, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TouchAPITokenLastUsed bumps the last_used_at column for the token
// whose hash matches. Called by the Bearer-auth path after a
// successful match. Best-effort by convention — the handler drops
// the returned error, because a transient DB hiccup on a touch
// should not lock the user out.
func TouchAPITokenLastUsed(d *sql.DB, tokenHash string) error {
	_, err := d.Exec(qTouchAPITokenLastUsed, tokenHash)
	return err
}

// DeleteAPITokensByUserID removes every personal_api_tokens row
// owned by userID. Called by PostAdminDeleteUser as part of the
// user-delete cascade. The pre-Этап-10 handler forgot to do this,
// which left orphaned token rows in the DB after admin deletes.
// Returns the number of rows removed (typically 0..N tokens per
// user) for the audit log; callers can ignore it.
func DeleteAPITokensByUserID(d *sql.DB, userID int64) (int64, error) {
	res, err := d.Exec(qDeleteAPITokensByUserID, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
