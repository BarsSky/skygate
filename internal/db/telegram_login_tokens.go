// Package db — telegram_login_tokens helpers.
//
// Этап 12 (2026-07-13): login-by-key. The user opens /my/telegram in
// the skygate web UI, clicks "Generate login key", receives a one-time
// token, pastes it into the bot via /login <token>. The bot calls
// ConsumeTelegramLoginToken, which atomically marks the row as used
// and returns the portal_user_id; the dispatcher then UPSERTs
// telegram_bindings with that user_id.
//
// All the helpers in this file are pure DB access; rate-limit checks
// (max active tokens per user) and token format/minting live in the
// callers (internal/handlers/handlers_my_telegram.go for generation,
// internal/telegram/commands.go for consumption). Keeping the DB
// layer format-agnostic means a future "scan QR" alternative doesn't
// need to touch this file.

package db

import (
	"database/sql"
	"errors"
	"time"
)

// ErrTelegramLoginTokenNotFound is returned by ConsumeTelegramLoginToken
// when no row matches the token. Callers (the bot dispatcher) turn
// this into "🔒 invalid or expired key" — never leak which one.
var ErrTelegramLoginTokenNotFound = errors.New("telegram: login token not found")

// ErrTelegramLoginTokenExpired is returned by ConsumeTelegramLoginToken
// when the row exists but expires_at < now. Like the above, we don't
// tell the bot which it was.
var ErrTelegramLoginTokenExpired = errors.New("telegram: login token expired")

// ErrTelegramLoginTokenAlreadyUsed is returned when the row exists
// and isn't expired but used_at > 0. The bot turns this into
// "🔒 key already used — generate a new one in /my/telegram".
var ErrTelegramLoginTokenAlreadyUsed = errors.New("telegram: login token already used")

// TelegramLoginToken is the typed view of one row in
// telegram_login_tokens. created_at / expires_at / used_at are
// unix seconds (int64). used_by_chat_id is 0 until consumed.
type TelegramLoginToken struct {
	Token         string
	PortalUserID  int64
	CreatedAt     int64
	ExpiresAt     int64
	UsedAt        int64
	UsedByChatID  int64
	RequestIP     string
}

// CreateTelegramLoginToken inserts a fresh row. The caller is
// responsible for minting a sufficiently-random token (see
// mintLoginToken in handlers_my_telegram.go — 16 chars from a
// 32-symbol alphabet, ~ 32^16 ≈ 1.2e24 possibilities, way past
// the 5-minute TTL threat model). TTL is supplied by the caller
// (the web handler reads telegram.login_token_ttl_seconds from
// global_settings so an operator can tune it without a redeploy).
//
// requestIP is recorded for audit only — the bot never sees it.
func CreateTelegramLoginToken(d *sql.DB, token string, portalUserID int64, ttlSeconds int, requestIP string) error {
	now := unixNow()
	_, err := d.Exec(qInsertTelegramLoginToken,
		token, portalUserID, now+int64(ttlSeconds), requestIP)
	return err
}

// CountActiveTelegramLoginTokensByUser returns how many UNUSED,
// NOT-EXPIRED tokens a user has at the moment. Used by the web
// handler to enforce a per-user cap (3 by default — enough for
// "phone + laptop + spare" without letting a user spam the table).
func CountActiveTelegramLoginTokensByUser(d *sql.DB, userID int64) (int, error) {
	var n int
	err := d.QueryRow(qCountActiveTelegramLoginTokensByUser, userID).Scan(&n)
	return n, err
}

// ListTelegramLoginTokensByUser returns the most recent N tokens
// for a user, newest first. Used by /my/telegram to show the user
// which keys they've generated (and which are still live vs used
// vs expired) so they can spot suspicious activity.
func ListTelegramLoginTokensByUser(d *sql.DB, userID int64, limit int) ([]TelegramLoginToken, error) {
	rows, err := d.Query(qListTelegramLoginTokensByUser, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TelegramLoginToken
	for rows.Next() {
		var t TelegramLoginToken
		if err := rows.Scan(&t.Token, &t.PortalUserID, &t.CreatedAt, &t.ExpiresAt,
			&t.UsedAt, &t.UsedByChatID, &t.RequestIP); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ConsumeTelegramLoginToken atomically marks a token as used by
// chatID and returns the row that was consumed. Returns one of:
//   - ErrTelegramLoginTokenNotFound       (token doesn't exist)
//   - ErrTelegramLoginTokenExpired        (row exists, expires_at < now)
//   - ErrTelegramLoginTokenAlreadyUsed    (row exists, used_at > 0)
//
// "Atomically" is qualified: the SQL is a single UPDATE so a
// concurrent /login from two different chats for the same token
// cannot both succeed. Whichever UPDATE lands first flips used_at
// from 0 to "now"; the second UPDATE matches zero rows and the
// helper reports ErrTelegramLoginTokenAlreadyUsed. This is the
// property that makes the token one-time without needing a
// transaction.
//
// On success the returned TelegramLoginToken has the just-written
// used_at and used_by_chat_id (the UPDATE only flips used_at=0
// rows, so the row we read after the UPDATE has the new values
// — but we re-read anyway to return the freshest snapshot to the
// caller, which wants to log "consumed by chat X at Y").
func ConsumeTelegramLoginToken(d *sql.DB, token string, chatID int64) (*TelegramLoginToken, error) {
	// 1. Read first to give a precise error (not-found vs expired
	//    vs already-used) — the bot's reply can be specific without
	//    leaking which it was, because the bot only ever sees the
	//    error class, never the token itself.
	row := d.QueryRow(qSelectTelegramLoginToken, token)
	var t TelegramLoginToken
	if err := row.Scan(&t.Token, &t.PortalUserID, &t.CreatedAt, &t.ExpiresAt,
		&t.UsedAt, &t.UsedByChatID, &t.RequestIP); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrTelegramLoginTokenNotFound
		}
		return nil, err
	}
	now := unixNow()
	if t.ExpiresAt < now {
		return nil, ErrTelegramLoginTokenExpired
	}
	if t.UsedAt > 0 {
		return nil, ErrTelegramLoginTokenAlreadyUsed
	}
	// 2. Atomic consume. The WHERE used_at = 0 guard is what makes
	//    this safe against a TOCTOU between the read above and this
	//    UPDATE — if another goroutine won the race, our UPDATE
	//    affects 0 rows and we report ErrTelegramLoginTokenAlreadyUsed.
	res, err := d.Exec(qConsumeTelegramLoginToken, chatID, token)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrTelegramLoginTokenAlreadyUsed
	}
	// 3. Re-read so the caller sees the post-consume snapshot.
	//    Not strictly required (the input row is correct modulo
	//    used_at / used_by_chat_id) but it makes the returned
	//    value consistent with what the DB now holds.
	if err := d.QueryRow(qSelectTelegramLoginToken, token).Scan(
		&t.Token, &t.PortalUserID, &t.CreatedAt, &t.ExpiresAt,
		&t.UsedAt, &t.UsedByChatID, &t.RequestIP); err != nil {
		// Non-fatal: return what we have.
		t.UsedAt = now
		t.UsedByChatID = chatID
	}
	return &t, nil
}

// DeleteTelegramLoginToken removes a single token. Idempotent.
// Used by the web handler when the user wants to revoke a key
// they generated but haven't used yet.
func DeleteTelegramLoginToken(d *sql.DB, token string) error {
	_, err := d.Exec(qDeleteTelegramLoginToken, token)
	return err
}

// PruneExpiredTelegramLoginTokens deletes every row whose
// expires_at < nowSeconds. Called by the web handler after
// generating a new key (keeps the table small) and by a future
// background sweep (not in this commit). Cheap because of
// idx_telegram_login_tokens_expiry.
func PruneExpiredTelegramLoginTokens(d *sql.DB, nowSeconds int64) (int64, error) {
	res, err := d.Exec(qDeleteExpiredTelegramLoginTokens, nowSeconds)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteTelegramLoginTokensByUser removes every row pointing at
// userID. Called by the admin user-delete path
// (handlers_admin_users.go:PostAdminUserDelete) so a deleted user
// doesn't leave orphan tokens that an attacker could then try to
// consume.
func DeleteTelegramLoginTokensByUser(d *sql.DB, userID int64) error {
	_, err := d.Exec(qDeleteTelegramLoginTokensByUser, userID)
	return err
}

// LoadTelegramStrictMode reads the strict_mode flag from
// global_settings. Returns false on any error (missing key, DB
// error, unparseable value) so a freshly-migrated DB or a
// corruption never causes a 500; the bot just falls back to the
// legacy behaviour, which is the safer failure mode for
// availability.
//
// The bot calls this on every message (cheap, single indexed
// read) so an operator can flip the toggle in /admin/telegram
// without restarting skygate.
func LoadTelegramStrictMode(d *sql.DB) bool {
	var v string
	err := d.QueryRow(`SELECT value FROM global_settings WHERE key = 'telegram.strict_mode'`).Scan(&v)
	if err != nil {
		return false
	}
	return v == "1"
}

// SaveTelegramStrictMode writes the strict_mode flag. Called by
// the admin /admin/telegram page when the operator toggles the
// checkbox. Stored as "0" / "1" (string) to keep global_settings
// uniformly TEXT.
func SaveTelegramStrictMode(d *sql.DB, enabled bool) error {
	v := "0"
	if enabled {
		v = "1"
	}
	_, err := d.Exec(`INSERT INTO global_settings(key, value, updated_at)
		VALUES ('telegram.strict_mode', ?, strftime('%s','now'))
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = strftime('%s','now')`, v)
	return err
}

// LoadTelegramLoginTokenTTL reads telegram.login_token_ttl_seconds
// from global_settings. Returns defaultTTL (300s = 5 min) on any
// error so a freshly-migrated DB has a sane default.
//
// The web handler uses this to render the "valid for 5 min"
// countdown next to a freshly-generated key. The bot doesn't
// need this value — the row's expires_at was set at create time
// using the same source, so the bot reads the absolute deadline
// from the row, not a config.
func LoadTelegramLoginTokenTTL(d *sql.DB) int {
	var v string
	err := d.QueryRow(`SELECT value FROM global_settings WHERE key = 'telegram.login_token_ttl_seconds'`).Scan(&v)
	if err != nil {
		return defaultLoginTokenTTL
	}
	// Parse the integer. We deliberately don't return an error —
	// a typo in the DB (e.g. "five minutes") should fall back to
	// the default, not 500 the web page.
	var n int
	for _, r := range v {
		if r < '0' || r > '9' {
			return defaultLoginTokenTTL
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return defaultLoginTokenTTL
	}
	return n
}

// defaultLoginTokenTTL is the fallback used when the
// global_settings row is missing or unparseable. 300s = 5 min —
// long enough for a human to copy the key, switch to Telegram,
// and type /login, short enough that a leaked screenshot doesn't
// outlive its usefulness.
const defaultLoginTokenTTL = 300

// unixNow returns the current time as unix seconds. Centralised
// so all the helpers in this file see a consistent "now" — the
// rate-limit window for /login attempts (consumed in
// internal/telegram/commands.go) reads this same value through
// the row's expires_at, so a one-second drift between "create"
// and "consume" never accidentally marks a fresh token as
// expired.
func unixNow() int64 {
	return time.Now().Unix()
}
