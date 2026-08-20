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
// map "no such key" to a http.StatusNotFound (the /my/keys/{id}/expire flow does
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
// errors.Is against that to map to http.StatusNotFound. Other errors (db down, etc.)
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
// InsertPreauthKey writes a new preauth_keys row. Called by
// PostMyPreauth after a.HS.CreatePreauthKey produces the key. The
// caller passes the headscale preauth id (which may be empty for
// pre-API-key-id releases — old rows in the DB look like that).
//
// Returns the new row's id (mostly for tests; the handler ignores it).
// v0.32.27: uses RETURNING id (works for both SQLite 3.35+ and PG).
func InsertPreauthKey(d *sql.DB, userID int64, key string, expiresAt int64, headscaleID string) (int64, error) {
	var id int64
	err := d.QueryRow(qInsertPreauthKey, userID, key, expiresAt, headscaleID).Scan(&id)
	return id, err
}

// GetLastPreauthKeyForChatID returns the most-recently-created
// preauth_keys row for the portal user bound to chatID. Used by
// the bot's /add_device platform picker to retrieve the key it
// just issued (the bot keeps the key in the bot's local memory
// for one turn, but the picker callback may arrive a few
// seconds later — long enough that a re-render would miss it,
// short enough that the key is still in the DB unused).
//
// 2026-07-14: Этап 14 v10.
//
// Returns ErrPreauthKeyNotFound when no row matches (the chat
// isn't bound, or the user has never issued a preauth key, or
// the last one was already used and reaped).
func GetLastPreauthKeyForChatID(d *sql.DB, chatID int64) (string, error) {
	var k string
	err := d.QueryRow(
		`SELECT pk.key
		   FROM preauth_keys pk
		   JOIN telegram_bindings tb ON tb.portal_user_id = pk.user_id
		  WHERE tb.chat_id = $1 AND pk.used = 0
		  ORDER BY pk.id DESC
		  LIMIT 1`, chatID,
	).Scan(&k)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrPreauthKeyNotFound
	}
	if err != nil {
		return "", err
	}
	return k, nil
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

// DeleteExpiredUnusedPreauthKeysByUser (B159, v1.5.0)
// bulk-deletes every preauth_keys row for userID
// that is BOTH unused AND has a past expiry. Used
// keys are NEVER deleted (they're audit history).
// Never-expiring keys (expires_at=0) are NEVER
// deleted (they have no expiry to clean up).
//
// Returns the number of rows actually removed. The
// caller (PostMyKeysCleanup) renders a flash with
// the count so the user knows what happened.
//
// The 3-WHERE-clause guard matches the operator's
// intent ("подчистить истёкшие ключи"): an unused
// key that hasn't expired yet (still in the 14-day
// warning window) is left alone so the user can
// still reissue it. A used key (consumed by a
// device registration) is left alone for audit.
// A never-expiring key (e.g. an operator-issued
// permanent service key) is left alone because
// it's not "expired".
func DeleteExpiredUnusedPreauthKeysByUser(d *sql.DB, userID, nowUnix int64) (int64, error) {
	res, err := d.Exec(qDeleteExpiredUnusedPreauthByUser, userID, nowUnix)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountExpiredUnusedPreauthKeysByUser (B159, v1.5.0)
// returns the number of rows that
// DeleteExpiredUnusedPreauthKeysByUser would
// remove for userID right now. Used by GetMyKeys
// to decide whether to render the "Clean up
// expired (N)" button (the button only appears
// when there's at least one row to clean —
// otherwise a no-op click would be confusing).
//
// Same WHERE clause as the DELETE so the count
// and the cleanup are always consistent. The
// SELECT cost is negligible (the (user_id,
// expires_at) composite index used by the
// dashboard's split is the same one this query
// hits).
func CountExpiredUnusedPreauthKeysByUser(d *sql.DB, userID, nowUnix int64) (int64, error) {
	var n int64
	err := d.QueryRow(qCountExpiredUnusedPreauthByUser, userID, nowUnix).Scan(&n)
	return n, err
}

// ExpiringPreauthKey is the slim row shape used by
// ListExpiringPreauthKeys (B156). The full PreauthKey
// struct already exists for the /my/keys list, but the
// notification scheduler needs the same fields PLUS
// notified_at to dedup. The B156 column migration
// (V058PG) added notified_at to preauth_keys; we add
// it here too for the scheduler's row view.
//
// Fields match the SELECT in qSelectExpiringPreauthKeys.
// Reusable and NotifiedAt are scanned from the
// post-V058 columns; pre-V058 rows (after a backup
// restore) would have Reusable=0 and NotifiedAt=0,
// which is the safe default (treat as single-use, no
// prior notification).
type ExpiringPreauthKey struct {
	ID                 int64
	UserID             int64
	Key                string
	HeadscalePreauthID string
	ExpiresAt          int64
	CreatedAt          int64
	Reusable           bool
	NotifiedAt         int64
}

// ListExpiringPreauthKeys returns every preauth_key
// that the B156 in-app notification scheduler should
// consider:
//
//   - used = 0 (the key has not been consumed by a
//     device registration — used keys have served
//     their purpose and don't need a "renew" nudge).
//   - expires_at > 0 (the key has a finite TTL — the
//     0 case means "no expiry" / "never").
//   - expires_at <= cutoffUnix (the key is within
//     the next 14 days of expiry, which is the
//     B156 default warning window).
//
// Result is ordered by expires_at ASC so the most-
// urgent keys (the soonest to expire) are processed
// first. The scheduler processes this whole result
// in one tick and dedup-via-notified_at to avoid
// spamming the user (see MarkPreauthKeyNotified +
// ResetPreauthKeyNotified).
//
// The reusable flag is included in the result so
// the scheduler's message can mention "reusable" or
// "single-use" — the user's reissue action differs
// slightly between the two (single-use is the default;
// reusable keys let one key add multiple devices).
//
// B156 (v1.5.0): the new in-app scheduler that calls
// this. Pre-B156 the only consumer of the expires_at
// column was the dashboard's 3-way split (used /
// active / expired) and the /my/keys page's
// per-row warning. The notification flow didn't
// exist — operators learned about expiring keys
// only when the user complained or when a key
// actually expired and the device registration
// failed.
func ListExpiringPreauthKeys(d *sql.DB, cutoffUnix int64) ([]ExpiringPreauthKey, error) {
	rows, err := d.Query(qSelectExpiringPreauthKeys, cutoffUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExpiringPreauthKey{}
	for rows.Next() {
		var k ExpiringPreauthKey
		var usedI, reusableI int
		if err := rows.Scan(&k.ID, &k.UserID, &k.Key, &k.HeadscalePreauthID, &k.ExpiresAt, &k.CreatedAt, &reusableI, &usedI); err != nil {
			return nil, err
		}
		k.Reusable = reusableI == 1
		out = append(out, k)
	}
	return out, rows.Err()
}

// MarkPreauthKeyNotified sets notified_at to the
// current unix timestamp. Called by the B156
// scheduler after a successful Telegram send so the
// same key isn't notified again on the next tick
// (the dedup window is 14d — matches the B155 banner
// window).
//
// We use the unix-seconds value passed in (not
// time.Now().Unix() here) so the caller can stamp
// a consistent "notification time" across all keys
// in a single batch — same convention as the
// B130/B142/B143 schedulers.
func MarkPreauthKeyNotified(d *sql.DB, id int64, unixSecs int64) error {
	_, err := d.Exec(qMarkPreauthKeyNotified, id, unixSecs)
	return err
}

// ResetPreauthKeyNotified sets notified_at back to 0.
// Called by PostMyPreauth (fresh key) and
// PostMyKeyReissue (reissue replaces the old key) so
// the new key starts with no prior notification.
//
// Without this reset, a user who issued a key with
// 30d TTL, got a "expiring in 14d" notification,
// then reissued (which gives a new key with the
// same TTL), would NOT get a new "expiring in 14d"
// notification for the reissued key — the dedup
// would skip it (notified_at is still 0 on the
// NEW row since the migration defaults to 0, but
// the dedup window is keyed on the (user_id, key)
// tuple, not on the row id; in practice the
// new key's id is different and the scheduler
// picks it up, but resetting the OLD key's
// notified_at also frees the OLD key's id from
// the dedup cache so the audit log can show
// "reissue replaced key #N" without the dedup
// machinery mistakenly suppressing the
// notification).
func ResetPreauthKeyNotified(d *sql.DB, id int64) error {
	_, err := d.Exec(qResetPreauthKeyNotified, id)
	return err
}
