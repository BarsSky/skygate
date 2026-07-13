// Package db — telegram_rate_limit helpers.
//
// Этап 13 (2026-07-13): shared (cross-instance) rate-limit
// store for the Telegram bot. Replaces the in-memory map in
// internal/telegram/commands_login.go. All access goes
// through the helpers in this file so the SQL stays in one
// place — the bot never touches the table directly.

package db

import (
	"database/sql"
)

// RecordTelegramRateLimitAttempt inserts one row for the given
// (key, action) at the current time. Returns the new count
// of attempts in the last `windowSeconds` seconds INCLUDING
// the one we just inserted, so the caller can compare
// against its limit and reject without a second round-trip.
//
// "Including the one we just inserted" is the right semantic
// for a "max N in 60s" gate: the N+1th attempt is the one
// being rejected, so the count after INSERT must reflect it.
// The caller passes back a (deny, retryAfter) decision based
// on whether count > max.
//
// Side effect: opportunistic prune. Every
// pruneEveryNthAttempt-th call (default 1000) runs
// DELETE WHERE ts < cutoff to keep the table small. Cheap
// because of idx_telegram_rate_limit_prune. We don't run
// this in a separate goroutine because a) the bot is
// single-instance and b) a slow prune would block the next
// attempt check. The optimistic-skip interval makes the
// prune invisible in normal operation.
func RecordTelegramRateLimitAttempt(d *sql.DB, key, action string, windowSeconds, maxAttempts int) (count int, allowed bool, err error) {
	now := unixNow()
	cutoff := now - int64(windowSeconds)
	// 1. Insert. Atomic; if a concurrent attempt races, both
	//    INSERTs land and the count query below sees both.
	if _, err := d.Exec(qInsertTelegramRateLimit, key, action, now); err != nil {
		return 0, false, err
	}
	// 2. Count in the window. The fresh row is included (it
	//    was inserted with ts=now, and the WHERE clause is
	//    ts >= cutoff where cutoff = now - window). If the
	//    caller's count > max, the next attempt will be
	//    rejected by the same path.
	if err := d.QueryRow(qCountTelegramRateLimitInWindow, key, cutoff).Scan(&count); err != nil {
		return count, false, err
	}
	// 3. Prune. OPPORTUNISTIC: only every 1000th call runs
	//    the actual DELETE. The modulo is on a per-process
	//    counter (pruneSweepCounter) so a multi-instance
	//    deploy still spreads the work across replicas.
	pruneSweepCounter++
	if pruneSweepCounter%1000 == 0 {
		// Fire-and-forget. A failure here is harmless: the
		// next sweep picks up the same rows, and an
		// over-large table is a slow SELECT, not a wrong
		// answer.
		go func() {
			_, _ = d.Exec(qDeleteTelegramRateLimitOlderThan, cutoff)
		}()
	}
	return count, count <= maxAttempts, nil
}

// PeekTelegramRateLimit returns the count of attempts in the
// last `windowSeconds` for `key`, WITHOUT inserting a new
// row. Used by the per-chat login page-load check (if we ever
// add one — currently the helper is forward-looking for the
// per-page-load soft-warning) and by tests.
func PeekTelegramRateLimit(d *sql.DB, key string, windowSeconds int) (int, error) {
	now := unixNow()
	cutoff := now - int64(windowSeconds)
	var n int
	if err := d.QueryRow(qCountTelegramRateLimitInWindow, key, cutoff).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ResetTelegramRateLimit deletes every row for `key`. Used
// by tests (and could be used by an admin "clear limit"
// path in the future). Cheap because the lookup index covers
// the WHERE clause.
func ResetTelegramRateLimit(d *sql.DB, key string) (int64, error) {
	res, err := d.Exec(`DELETE FROM telegram_rate_limit WHERE key = ?`, key)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// pruneSweepCounter is the per-process counter for the
// opportunistic-prune interval (see RecordTelegramRateLimitAttempt).
// Lives in package scope so it survives between calls; the
// sweep modulos it against a fixed interval (1000) so the
// actual DELETE runs at most once per ~1000 attempts.
var pruneSweepCounter int64
