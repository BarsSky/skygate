package db

import (
	"database/sql"
	"errors"
)

// preauth_keys  —  helpers
//
// 2026-07-11: refactor v0.6.0 (Этап 10 part 3). Before this file
// the same SQL strings were duplicated across 5 handler files:
//
//   handlers_my_preauth.go        — POST /my/preauth (1 call site: INSERT)
//   handlers_my_keys.go           — /my/keys list + expire (3 call sites: SELECT+SELECT+UPDATE)
//   handlers_dashboard.go         — countMyPreAuthKeys (1 call site: SELECT)
//   handlers_node_ownership.go    — backfillNodeOwnership (2 call sites: SELECT+UPDATE)
//   handlers_admin_users.go       — user-delete cascade (1 call site: DELETE)
//
// Eight call sites total. Six helpers:
//
//   Read (2):  ListPreauthKeysByUser  + GetPreauthKeyByID
//   Write (3): InsertPreauthKey + ExpirePreauthKey + MarkPreauthKeyUsedByHSID
//   Cascade (1): DeletePreauthKeysByUserID
//
// One row type (PreauthKey) covers every column the handlers ever
// looked at. Callers ignore the fields they don't need — the struct
// is a value copy of the SELECT result and zero values are well-
// defined (Used=false, HeadscalePreauthID="", ExpiresAt=0). The
// alternative — one struct per SELECT column set — would mean three
// structs for what is functionally one row.
//
// The full SELECT (qSelectPreauthByUserDetailed) is reused for both
// the /my/keys list AND the dashboard / node_ownership callers. The
// SELECT cost difference is negligible on a per-user table, and
// keeping one query means one index, one EXPLAIN, one place to add
// a column later.

// PreauthKey is a row in preauth_keys.
//
// Zero values carry the same meaning as the schema defaults:
//   HeadscalePreauthID == ""  → not linked to a headscale preauth
//                               (e.g. keys issued before the API
//                               response field started populating)
//   ExpiresAt          == 0   → no expiry (TTL issued = forever,
//                               or a freshly-issued 1h key whose
//                               expires_at is the future ts)
//
// CreatedAt is always populated by the schema default
// (strftime('%s','now')), so a 0 here means the row predates that
// default or was inserted without it — a rare but possible state.
type PreauthKey struct {
	ID                 int64
	UserID             int64
	Key                string
	HeadscalePreauthID string
	Used               bool
	ExpiresAt          int64
	CreatedAt          int64
}

// ErrPreauthKeyNotFound is returned by GetPreauthKeyByID when no
// row matches (id, userID). Callers can errors.Is against this to
// map "no such key" to a 404 (the /my/keys/{id}/expire flow does
// this). The multi-row ListPreauthKeysByUser never returns this —
// it returns an empty slice for "user has no keys".
var ErrPreauthKeyNotFound = errors.New("db: preauth_key not found")

// ListPreauthKeysByUser returns every preauth_keys row for userID,
// newest first. Used by:
//
//   - GetMyKeys        (full render of /my/keys)
//   - countMyPreAuthKeys (dashboard split into used/active/expired)
//   - backfillNodeOwnership (temporal match for orphan nodes)
//
// Returns an empty slice (not nil) when the user has zero keys —
// matches the personal_api_tokens convention and lets the templates
// iterate without nil checks.
func ListPreauthKeysByUser(d *sql.DB, userID int64) ([]PreauthKey, error) {
	rows, err := d.Query(qSelectPreauthByUserDetailed, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PreauthKey{}
	for rows.Next() {
		var k PreauthKey
		var usedI int
		if err := rows.Scan(&k.ID, &k.Key, &usedI, &k.ExpiresAt, &k.CreatedAt, &k.HeadscalePreauthID); err != nil {
			return nil, err
		}
		k.Used = usedI == 1
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetPreauthKeyByID returns one row scoped to (id, userID). The
// user_id filter is enforced so a user can't probe another user's
// keys by guessing an id.
//
// Returns ErrPreauthKeyNotFound when no row matches; callers can
// errors.Is against that to map to 404. Other errors (db down, etc.)
// pass through unchanged.
//
// Used by PostMyKeyExpire to fetch headscale_preauth_id before
// calling headscale.ExpirePreauthKey.
func GetPreauthKeyByID(d *sql.DB, id, userID int64) (PreauthKey, error) {
	var k PreauthKey
	var usedI int
	err := d.QueryRow(qSelectPreauthFullByID, id, userID).Scan(
		&k.ID, &k.UserID, &k.Key, &k.HeadscalePreauthID, &usedI, &k.ExpiresAt, &k.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PreauthKey{}, ErrPreauthKeyNotFound
	}
	if err != nil {
		return PreauthKey{}, err
	}
	k.Used = usedI == 1
	return k, nil
}

// InsertPreauthKey writes a new preauth_keys row. Called by
// PostMyPreauth after a.HS.CreatePreauthKey produces the key. The
// caller passes the headscale preauth id (which may be empty for
// pre-API-key-id releases — old rows in the DB look like that).
//
// Returns the new row's id (mostly for tests; the handler ignores it).
func InsertPreauthKey(d *sql.DB, userID int64, key string, expiresAt int64, headscaleID string) (int64, error) {
	res, err := d.Exec(qInsertPreauthKey, userID, key, expiresAt, headscaleID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ExpirePreauthKey sets expires_at on a single row, scoped to userID.
// No-op (returns nil) if the row doesn't exist for that user.
//
// Called by PostMyKeyExpire after headscale.ExpirePreauthKey returns
// success — the local row's expires_at is moved to "now" so the
// dashboard's 3-way split reclassifies it as Expired on the next
// render. The row is NOT deleted; it stays around as audit history.
func ExpirePreauthKey(d *sql.DB, id, userID, expiresAt int64) error {
	_, err := d.Exec(qUpdatePreauthExpires, expiresAt, id, userID)
	return err
}

// MarkPreauthKeyUsedByHSID flips used=1 for any row whose
// headscale_preauth_id matches AND used=0. The "AND used=0" guard
// is a no-op for performance (no extra rows updated) AND prevents
// moving a used=1 row's used column back to 1 in some future bug.
//
// Called by backfillNodeOwnership when a headscale node attaches
// to a skygate-issued key — the local row is brought into sync
// with headscale's reality. Idempotent.
//
// Best-effort by convention — callers log a warning and move on,
// because a transient DB hiccup on a "mark used" update should not
// break the /my/devices page load.
func MarkPreauthKeyUsedByHSID(d *sql.DB, headscaleID string) error {
	if headscaleID == "" {
		// Defensive: don't run an UPDATE with an empty WHERE that
		// would touch every unused row. Should never happen in
		// practice (callers check n.PreAuthKeyID != "" first).
		return nil
	}
	_, err := d.Exec(qMarkPreauthUsed, headscaleID)
	return err
}

// DeletePreauthKeysByUserID removes every preauth_keys row for
// userID. Called by PostAdminDeleteUser as part of the user-delete
// cascade.
//
// The pre-Этап-10 handler used an inline DELETE and the audit log
// didn't count rows. We return rows affected so the audit message
// can include "keys=N" alongside the existing "tokens=N" detail.
func DeletePreauthKeysByUserID(d *sql.DB, userID int64) (int64, error) {
	res, err := d.Exec(qDeletePreauthByUser, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
