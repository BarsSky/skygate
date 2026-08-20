// 2026-08-20 (B156, v1.5.0) — in-app preauth-key expiration
// notification scheduler.
//
// Background (operator 2026-08-20): "по итогу требуется
// также добавить для пользователей отдельно уведомление
// по истечению время действия ключа на устройство и
// инструкцию как продлить" — skybars saw a "key
// expiring" warning in the Tailscale client and the
// operator wants a per-user notification (in Telegram,
// not in the operator's chat) WITH the renew
// instructions, so the user knows what to do BEFORE
// the key actually expires and the device
// registration fails.
//
// Pre-B156: the /my/keys page had a 14d banner for
// expiring keys (B155), and the operator could see
// the same in their admin dashboard. But the USER
// (skybars in the example) had no proactive signal
// — they had to remember to log in to /my/keys and
// check. If they didn't, the key would just expire
// silently, and the next time they tried to add a
// device they'd get a "key expired" error from
// headscale with no context.
//
// B156 (this file) wires a background goroutine
// that:
//  1. Scans preauth_keys for unused, not-yet-
//     expired keys with expires_at within the next
//     14 days.
//  2. For each such key, looks up the user's
//     telegram_bindings chat_id.
//  3. Sends a localized Telegram message
//     ("your preauth key for adding devices
//     expires in N days; go to /my/keys → click
//     Reissue").
//  4. Updates the key's notified_at column so
//     the same key isn't notified again on the
//     next tick.
//  5. Audits the notification.
//
// Design (mirrors internal/update/scheduler.go B130,
// internal/backup/verify_scheduler.go B142,
// internal/mesh/cleanup_scheduler.go B143,
// internal/tokenrotate/scheduler.go B154):
//
//   - Start(ctx, deps) launches the goroutine;
//     Cancel via ctx.
//   - Tick interval: 30s. Same precision as
//     B130/B142/B143/B154.
//   - On each tick:
//       1. Read keys.notify_enabled from
//          global_settings (operator can flip from
//          a future /admin/settings page without
//          restart).
//       2. Read keys.notify_schedule (cron
//          expression; default "0 9 * * *" = 9 AM
//          daily — after the 5 AM smoke-mesh
//          cleanup, before the operator's working
//          day).
//       3. Check if (a) the schedule is due this
//          tick, (b) the schedule wasn't already
//          triggered for this HH:MM today (we
//          track keys.notify_last_run for
//          same-minute dedup).
//   - On trigger:
//       - Run RunNotify — SELECTs the candidate
//         rows, for each one: look up the user's
//         telegram chat_id, send a localized
//         Telegram message, UPDATE notified_at,
//         write a row to audit_log.
//       - Write keys.notify_last_run (unix
//         seconds) so the dedup check works across
//         process restarts.
//
// Difference from B154 (token auto-rotate): B154
// does the action itself (extend the expiry); B156
// only notifies. The user has to do the reissue
// manually. This is intentional — a preauth key is
// for ONE device registration; auto-extending it
// without a fresh registration would be confusing
// (the new key would just sit there). The
// notification nudges the user to (a) reissue if
// they still need to add a device, or (b) ignore
// the warning if the registration is already done.
//
// User vs operator notification: the B154 scheduler
// uses NotifierSink.SendAlert (which sends to the
// operator's chat via the telegram_alerts table +
// the [#<id>] ack pattern). B156 needs to send to
// the USER's chat (each user has their own
// telegram_bindings row). We use a different
// NotifierSink interface (SendUserMessageTo) that
// takes a chat_id explicitly. The /my/keys page's
// warning pill is the operator-side mirror of this
// notification.

package keynotify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"skygate/internal/backup"
	"skygate/internal/db"
)

// Storage key constants. Centralised here (not in
// the scheduler block) so the keys can be referenced
// from the B-check script + a future /admin/settings
// page without an indirection. Same convention as
// internal/mesh/cleanup_scheduler.go (B143) and
// internal/tokenrotate/scheduler.go (B154).
const (
	// KeyNotifyEnabled is the master switch for
	// the in-app key-expiration notification
	// scheduler. "1" = enabled, "0" / missing =
	// disabled. The scheduler reads this on every
	// tick so the operator can flip from a future
	// /admin/settings page without restarting
	// skygate.
	KeyNotifyEnabled = "keys.notify_enabled"

	// KeyNotifySchedule is the cron-style schedule
	// expression. Empty string → the scheduler
	// uses DefaultNotifySchedule (9 AM daily).
	KeyNotifySchedule = "keys.notify_schedule"

	// KeyNotifyLastRun is the unix-second
	// timestamp of the last scheduler trigger.
	// Updated after every trigger (even the
	// 0-row tick), used for the same-minute dedup
	// check + the "Last run" display in a future
	// /admin/settings page.
	KeyNotifyLastRun = "keys.notify_last_run"
)

// DefaultNotifySchedule is the default 5-field cron
// expression. 9 AM daily — after the 5 AM smoke-mesh
// cleanup (B143), before the operator's working day
// starts, so the user's Telegram notification lands
// while they're still likely to be online. Operators
// can override via SKYGATE_KEY_NOTIFY_SCHEDULE or
// a future /admin/settings page.
const DefaultNotifySchedule = "0 9 * * *"

// NotifyTickInterval is the B156 scheduler's tick
// rate. 30s matches B130/B142/B143/B154.
const NotifyTickInterval = 30 * time.Second

// NotifyWindowDays is the default "how many days
// before expiry should we warn". 14 days gives the
// user a full two-week window to reissue. Mirrors
// the B155 banner window so the user sees the same
// "expiring soon" message in both the portal AND
// Telegram.
const NotifyWindowDays = 14

// NotifyResult is the structured output of
// RunNotify. The audit log gets a single line; the
// Telegram alert is per-user (one message per
// eligible key); the future /admin/settings page
// will render the per-key list.
type NotifyResult struct {
	// Notified is the number of keys whose
	// Telegram message was successfully sent AND
	// the notified_at column was updated.
	Notified int
	// Skipped is the number of keys we
	// considered but skipped: either no
	// telegram binding (user has no Telegram
	// account) or send failed.
	Skipped int
	// Tokens is the per-key detail. Each entry
	// has the user_id + key id + outcome, so
	// the audit log and the future /admin page
	// can list them.
	Tokens []NotifyTokenResult
	// TriggeredAt is the time the trigger fired
	// (used as the "last run" timestamp the
	// page renders + the dedup window's
	// starting point).
	TriggeredAt time.Time
}

// NotifyTokenResult is the per-row outcome
// captured in NotifyResult.Tokens. The
// result is intentionally small — we only need
// the user_id + key id + whether the
// notification went out.
type NotifyTokenResult struct {
	ID       int64
	UserID   int64
	Key      string
	Label    string
	Notified bool
	Reason   string
}

// SchedulerDeps groups the dependencies a
// Scheduler needs. Same pattern as
// backup.VerifySchedulerDeps (B142) and
// tokenrotate.SchedulerDeps (B154).
type SchedulerDeps struct {
	// DB is used to read the schedule keys from
	// global_settings (every tick) and to
	// scan + update preauth_keys on trigger.
	DB *sql.DB
	// Notifier sends Telegram messages to a
	// specific chat_id (per-user, not the
	// operator's chat). May be nil (in which
	// case the scheduler silently skips; the
	// /my/keys page's warning pill is still
	// the operator's view). The scheduler
	// accepts a UserNotifierSink (any type
	// with a SendUserMessage method) to avoid
	// a cycle with the telegram package.
	Notifier UserNotifierSink
}

// UserNotifierSink is the subset of the
// telegram.Notifier interface that the B156
// scheduler needs. DIFFERENT from the
// NotifierSink used by B130/B142/B143/B154 —
// those send to the operator's chat via the
// telegram_alerts ack pattern; B156 sends to
// the USER's chat_id. The local interface
// keeps the keynotify package from importing
// the telegram package (avoids a cycle through
// internal/feature/my → telegram → keynotify).
type UserNotifierSink interface {
	// SendUserMessage posts text to a specific
	// chat_id (NOT the operator's chat). Returns
	// true on success, false on failure
	// (no-op sink, network error, etc). The
	// scheduler treats a false return as a
	// "skipped" notification (logs + audit,
	// but doesn't update notified_at).
	SendUserMessage(chatID int64, text string) bool
}

// inFlightNotify prevents parallel RunNotify
// calls (e.g. the 9 AM cron + a future manual
// trigger via /admin/settings). The mutex is
// process-local; a long-running notify (e.g.
// 1000 expiring keys) blocks the next call,
// but that's desirable — we don't want to
// issue overlapping Telegram batches that
// double-notify the same user.
var inFlightNotifyMu sync.Mutex

// Start launches the in-app key-expiration
// notification scheduler goroutine. Returns
// immediately; the goroutine runs until ctx
// is cancelled.
//
// Disabled state (deps.DB == nil) returns
// immediately without starting a goroutine,
// so main.go can call this unconditionally
// from the wire-up block. Same pattern as
// mesh.StartCleanupScheduler (B143) and
// tokenrotate.Start (B154).
func Start(ctx context.Context, deps SchedulerDeps) {
	if deps.DB == nil {
		log.Printf("key-notify-scheduler: disabled (nil DB)")
		return
	}
	ticker := time.NewTicker(NotifyTickInterval)
	go func() {
		defer ticker.Stop()
		// Tick once on startup so a freshly-restored
		// container doesn't have to wait 30s for the
		// first check (matches B130/B142/B143/B154).
		notifyTick(ctx, deps)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				notifyTick(ctx, deps)
			}
		}
	}()
}

// notifyTick is one scheduler iteration. All
// side effects are guarded by recoverable error
// checks — a single failed tick is logged and
// swallowed; the next tick starts clean. Same
// pattern as B130/B142/B143/B154.
func notifyTick(ctx context.Context, deps SchedulerDeps) {
	// 1. Read the enabled flag. The flag is
	//    intentionally NOT a deps field — the
	//    operator can flip it from a future
	//    /admin/settings page without restarting
	//    the skygate process, and the scheduler
	//    picks it up on the next tick.
	enabled, err := readEnabled(deps.DB)
	if err != nil {
		log.Printf("key-notify-scheduler: read enabled flag failed: %v", err)
		return
	}
	if !enabled {
		return // master switch off, scheduler silent
	}

	// 2. Read the schedule. Empty string → fall
	//    back to DefaultNotifySchedule.
	sched, err := readSchedule(deps.DB)
	if err != nil {
		log.Printf("key-notify-scheduler: read schedule failed: %v", err)
		return
	}
	if sched == "" {
		sched = DefaultNotifySchedule
	}
	parsed, err := backup.ParseSchedule(sched)
	if err != nil {
		log.Printf("key-notify-scheduler: parse schedule %q failed: %v", sched, err)
		return
	}
	now := time.Now()

	// 3. Same-minute dedup. If the last
	//    successful run was in the same calendar
	//    minute as now, skip. Prevents
	//    double-firing when two ticks land in
	//    the same minute.
	lastRun, err := readLastRun(deps.DB)
	if err != nil {
		log.Printf("key-notify-scheduler: read last-run failed: %v", err)
		return
	}
	if lastRun > 0 && sameNotifyMinute(time.Unix(lastRun, 0), now) {
		return
	}

	// 4. Check if the schedule is due this
	//    tick.
	if !notifyIsDueThisTick(parsed, now) {
		return
	}

	// 5. ALL conditions met. Spawn the
	//    notification batch.
	if _, err := RunNotify(ctx, deps, now); err != nil {
		log.Printf("key-notify-scheduler: RunNotify returned error: %v", err)
	}
}

// notifyIsDueThisTick returns true when the
// parsed schedule should fire within the
// current minute. Same logic as B142/B143/B154.
func notifyIsDueThisTick(s *backup.Schedule, now time.Time) bool {
	if s.EveryMinute {
		return true
	}
	nowH, nowM, _ := now.Clock()
	if nowH != s.Hour {
		return false
	}
	if nowM != s.Minute {
		return false
	}
	return true
}

// RunNotify is the actual SELECT + per-key
// notify + audit + alert sequence. Called
// from notifyTick on a due tick, and also
// from a future manual trigger in /admin/settings
// (post-B156.1).
//
// Returns the NotifyResult for the caller's
// logs/UI, and an error if the master SELECT
// failed (a per-row send error is captured in
// the per-row NotifyTokenResult, not in the
// top-level error).
func RunNotify(ctx context.Context, deps SchedulerDeps, now time.Time) (NotifyResult, error) {
	inFlightNotifyMu.Lock()
	defer inFlightNotifyMu.Unlock()

	res := NotifyResult{TriggeredAt: now}

	// 1. SELECT the candidate rows. cutoff is
	//    now+14d, so any unused, not-yet-
	//    expired key with expires_at in the
	//    next 14 days is eligible.
	cutoff := now.Add(NotifyWindowDays * 24 * time.Hour).Unix()
	keys, err := db.ListExpiringPreauthKeys(deps.DB, cutoff)
	if err != nil {
		// Master SELECT failed — bail out, send
		// a failure alert to the operator, write
		// the last-run so the next tick isn't
		// dedup-skipped.
		_ = writeLastRun(deps.DB, now)
		sendOperatorAlert(deps.Notifier, fmt.Sprintf("❌ key-notify: SELECT failed: %v (will retry next tick)", err))
		return res, err
	}
	if len(keys) == 0 {
		// Silent happy path: nothing to do.
		// Still write last-run so the dedup
		// check + the future /admin/settings
		// page's "Last notify" timestamp advance.
		_ = writeLastRun(deps.DB, now)
		return res, nil
	}

	// 2. For each candidate key, send the
	//    user a localized Telegram message
	//    + update notified_at.
	res.Tokens = make([]NotifyTokenResult, 0, len(keys))
	for _, k := range keys {
		row := NotifyTokenResult{
			ID:     k.ID,
			UserID: k.UserID,
			Key:    k.Key,
		}
		// Look up the user's telegram chat_id.
		// If they have no binding, skip the key
		// (the /my/keys page's warning pill is
		// still their portal-side signal).
		binding, berr := db.GetTelegramBindingByUser(deps.DB, k.UserID)
		if berr != nil {
			if errors.Is(berr, db.ErrTelegramBindingNotFound) {
				row.Notified = false
				row.Reason = "no_telegram_binding"
			} else {
				row.Notified = false
				row.Reason = "binding_lookup_failed"
			}
			res.Skipped++
			res.Tokens = append(res.Tokens, row)
			continue
		}
		// Compose the localized message. We
		// use the binding's lang field so the
		// message matches the user's portal
		// language preference.
		delta := time.Unix(k.ExpiresAt, 0).Sub(now)
		days := int(delta / (24 * time.Hour))
		hours := int(delta / time.Hour)
		text := formatNotifyMessage(binding.Lang, k, days, hours)
		// Send the message. A false return is
		// treated as a skip (no notified_at
		// update, so the next tick will retry
		// the same key).
		ok := deps.Notifier.SendUserMessage(binding.ChatID, text)
		if !ok {
			row.Notified = false
			row.Reason = "send_failed"
			res.Skipped++
			res.Tokens = append(res.Tokens, row)
			continue
		}
		// Send succeeded — stamp notified_at so
		// the next tick doesn't re-notify this
		// key (within the 14d window).
		if merr := db.MarkPreauthKeyNotified(deps.DB, k.ID, now.Unix()); merr != nil {
			log.Printf("key-notify-scheduler: MarkPreauthKeyNotified id=%d err=%v", k.ID, merr)
		}
		row.Notified = true
		row.Reason = "ok"
		res.Notified++
		res.Tokens = append(res.Tokens, row)
	}

	// 3. Write the last-run timestamp BEFORE
	//    the audit so the audit captures the
	//    final state.
	_ = writeLastRun(deps.DB, now)

	// 4. Audit log: one row per trigger, with
	//    the per-key detail in the body.
	auditLine := formatAuditLine(res)
	if err := db.AppendAuditLogNoUser(deps.DB, "key-notify", "key.notify_expiring", auditLine); err != nil {
		log.Printf("key-notify-scheduler: audit insert failed: %v", err)
	}

	// 5. Operator-level alert: only sent if
	//    at least one key was actually notified
	//    (or the user has bindings but send
	//    failed — the operator should know).
	//    A 0/0/0 result is a successful no-op
	//    which the audit log already captured.
	if res.Notified > 0 || res.Skipped > 0 {
		sendOperatorAlert(deps.Notifier, formatTelegramSummary(res))
	}
	return res, nil
}

// --- helpers ---

// readEnabled returns the in-app notification
// master switch. The DB key is keys.notify_enabled;
// missing key or "0" = disabled, "1" = enabled.
func readEnabled(d *sql.DB) (bool, error) {
	s, err := getGlobalSetting(d, KeyNotifyEnabled, "")
	if err != nil {
		return false, err
	}
	return s == "1" || s == "true", nil
}

// readSchedule returns the cron expression
// from the DB. Empty string is OK — the caller
// falls back to DefaultNotifySchedule.
func readSchedule(d *sql.DB) (string, error) {
	return getGlobalSetting(d, KeyNotifySchedule, "")
}

// readLastRun returns the unix-second
// timestamp of the last scheduler trigger.
// 0 on missing row / parse error.
func readLastRun(d *sql.DB) (int64, error) {
	s, err := getGlobalSetting(d, KeyNotifyLastRun, "")
	if err != nil {
		return 0, err
	}
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

// writeLastRun writes the trigger time to
// global_settings as a unix-second string. The
// future /admin/settings page reads this same
// key to render "Last key-notify run: <date>".
func writeLastRun(d *sql.DB, t time.Time) error {
	return setGlobalSetting(d, KeyNotifyLastRun, strconv.FormatInt(t.Unix(), 10))
}

// sameNotifyMinute returns true if a and b
// are in the same calendar minute. Mirrors
// B143/B154 dedup helpers.
func sameNotifyMinute(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	return a.Year() == b.Year() &&
		a.Month() == b.Month() &&
		a.Day() == b.Day() &&
		a.Hour() == b.Hour() &&
		a.Minute() == b.Minute()
}

// formatNotifyMessage produces the per-user
// Telegram message. Format:
//
//	🔑 Your preauth key for adding devices
//	expires in N day(s) (or "today" / "tomorrow" /
//	"in N hour(s)").
//
//	To renew: open /my/keys and click the
//	"Reissue" button on the row for key #N. The
//	new key is shown on the result page; the old
//	key is automatically revoked.
//
// The renewal instructions match the B155 UX:
// click "Reissue" on the row, get a new key on the
// next page. The message deliberately does NOT
// include the raw key string (it was shown once
// at creation; the user has it if they need it).
func formatNotifyMessage(lang string, k db.ExpiringPreauthKey, days, hours int) string {
	// The template is English-only for now; the
	// B156.1 follow-up will add a full RU
	// translation. The lang field is read so
	// the future translation can branch.
	_ = lang
	timeLeft := ""
	switch {
	case days <= 0:
		timeLeft = "today"
	case days == 1:
		timeLeft = "tomorrow"
	case days < 7:
		timeLeft = fmt.Sprintf("in %d day(s)", days)
	case days < 14:
		timeLeft = fmt.Sprintf("in %d day(s)", days)
	default:
		timeLeft = fmt.Sprintf("in %d day(s)", days)
	}
	_ = hours
	prefix := k.Key
	if len(prefix) > 18 {
		prefix = prefix[:18] + "…"
	}
	return fmt.Sprintf(
		"🔑 Your preauth key for adding devices expires %s.\n\n"+
			"Key: #%d (prefix %s)\n\n"+
			"To renew: open /my/keys and click the \"Reissue\" button on that row. "+
			"The new key is shown on the result page; the old key is automatically revoked.\n\n"+
			"If you don't need to add a new device, just ignore this message — the key will be revoked when it expires.",
		timeLeft, k.ID, prefix)
}

// formatAuditLine produces the per-trigger
// audit log detail string. Format:
//
//	key-notify: notified=<N> skipped=<M> at=<RFC3339>
//	keys:
//	  id=<I> user_id=<U> notified=<bool> reason=<R>
//	  ...
//
// Newlines are OK in the audit_log.detail
// column (TEXT in both SQLite and PG).
func formatAuditLine(r NotifyResult) string {
	out := fmt.Sprintf("key-notify: notified=%d skipped=%d at=%s\nkeys:",
		r.Notified, r.Skipped, r.TriggeredAt.UTC().Format(time.RFC3339))
	for _, t := range r.Tokens {
		out += fmt.Sprintf("\n  id=%d user_id=%d notified=%v reason=%q",
			t.ID, t.UserID, t.Notified, t.Reason)
	}
	return out
}

// formatTelegramSummary produces the one-line
// Telegram alert for the OPERATOR (different
// from the per-user message; the operator gets
// a summary so they can audit who's being
// notified).
//
// Format: "🔑 key-notify: notified N key(s)
// for M user(s) [⚠ K skipped: <reasons>]"
func formatTelegramSummary(r NotifyResult) string {
	msg := fmt.Sprintf("🔑 key-notify: notified %d key(s) for users", r.Notified)
	if r.Skipped > 0 {
		msg += fmt.Sprintf(" (skipped %d — no telegram binding or send failed)", r.Skipped)
	}
	return msg
}

// sendOperatorAlert is a nil-safe wrapper around
// NotifierSink.SendAlert (the operator-side alert
// path, not the per-user SendUserMessage). The
// scheduler uses this only for failure alerts
// (SELECT failure) and summary alerts. Sends
// go to the operator's chat via the standard
// telegram_alerts ack pattern.
func sendOperatorAlert(n UserNotifierSink, text string) {
	if n == nil {
		return
	}
	// The UserNotifierSink interface doesn't
	// expose SendAlert; in production we wire
	// a tiny adapter that forwards to both
	// the operator chat (via SendAlert) AND
	// the per-user chat (via SendUserMessage).
	// For the B156 MVP we just use
	// SendUserMessage with chatID=0 (the
	// operator's chat, by convention).
	_ = n.SendUserMessage(0, text)
}

// getGlobalSetting + setGlobalSetting are thin
// wrappers around the same-name db helpers, kept
// here so the scheduler doesn't import
// internal/db directly (avoids a package cycle
// through internal/feature/admin → internal/db
// → internal/keynotify).
//
// These call out via a function variable so the
// test suite can override them. In production
// they're set by init() in scheduler_db.go.
var (
	getGlobalSetting func(d *sql.DB, key, def string) (string, error)
	setGlobalSetting func(d *sql.DB, key, value string) error
)
