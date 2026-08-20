// 2026-08-20 (B154, v1.5.0) — in-app auto-rotate scheduler for
// personal API tokens.
//
// Pre-B154: the /my/tokens page (B153) lets an operator
// check the "auto-rotate" checkbox when creating a token,
// but the flag was never honoured — the column was stored,
// displayed, and ignored. Tokens with auto_rotate=1 just
// expired silently, and the operator had to manually
// re-create them. The Tailscale device's "key expiring"
// warning (operator report 2026-08-20) was a symptom of
// exactly this gap: a token that should have been
// auto-managed was about to expire because nothing
// actually rotated it.
//
// B154 (this file) wires a background goroutine that
// scans personal_api_tokens for auto_rotate=1 rows whose
// expires_at is within the next 7 days, and extends each
// one's expiry by AutoExtendDuration (30d by default).
//
// IMPORTANT design choice: this is **auto-extend**, not
// full rotation. The token's hash DOES NOT change, so any
// integration (e.g. a Tailscale client, an AI-assistant
// token, a curl script in cron) that has the existing
// token keeps working — only the expiry moves forward.
// This is the right default for AI-assistant integrations,
// where regenerating the token would require the user to
// re-paste the new raw token into the assistant's config
// and that operation often has no UI affordance.
//
// If a future B-check needs full rotation (generate a new
// raw token + revoke the old), the structure here is
// already split (ListAPITokensForAutoRotate returns the
// rowset; runOnce does the per-token work) so a sibling
// `runRotation` function can be added without disturbing
// the auto-extend path.
//
// Design (mirrors internal/update/scheduler.go B130,
// internal/backup/verify_scheduler.go B142, and
// internal/mesh/cleanup_scheduler.go B143):
//
//   - Start(ctx, deps) launches the goroutine; Cancel
//     via ctx.
//   - Tick interval: 30s. Same precision as B130/B142/B143
//     — an HH:MM schedule fires within 30s of the target
//     minute.
//   - On each tick:
//       1. Read tokens.auto_rotate_enabled from
//          global_settings (operator can flip from
//          /my/tokens page or via env-var without
//          restarting the skygate process).
//       2. Read tokens.auto_rotate_schedule (cron
//          expression; default "0 3 * * *" = 3 AM daily —
//          same default as the B130 update scheduler so
//          a single nightly cron window handles all
//          lifecycle jobs).
//       3. Check if (a) the schedule is due this
//          tick, (b) the schedule wasn't already
//          triggered for this HH:MM today (we track
//          tokens.auto_rotate_last_run for same-minute
//          dedup).
//   - On trigger:
//       - Run RunAutoExtend (defined in this file) —
//         SELECTs the candidate rows
//         (auto_rotate=1 AND expires_at>0 AND
//         expires_at<=now+7d), UPDATEs each one's
//         expires_at to (now+30d), writes a summary to
//         the audit log via db.AppendAudit, and sends
//         a Telegram alert with the count + the
//         per-token label list.
//       - Write tokens.auto_rotate_last_run (unix
//         seconds) so the /my/tokens page can show
//         "Last auto-rotate: <date>" + the dedup
//         check works across process restarts.
//
// Failure modes:
//   - DB read error on the schedule keys → tick is a
//     no-op (logged), next tick is fresh. Same as
//     B130/B142/B143.
//   - No tokens are due → tick logs nothing, no
//     audit entry, no Telegram alert. Silent happy
//     path = nothing to do.
//   - ListAPITokensForAutoRotate fails → tick is a
//     no-op (logged), audit log gets an error entry,
//     Telegram gets a failure alert. Next tick
//     retries.
//   - UPDATE fails mid-batch → the failed token is
//     logged + Telegram'd, the rest of the batch
//     continues. The summary alert at the end still
//     includes the failure count.
//
// Concurrency:
//   - inFlightRotate mutex (process-local) prevents
//     two parallel RunAutoExtend calls (e.g. the
//     3 AM cron + a manual trigger via the /my/tokens
//     page). The scheduler + a manual trigger are
//     both short (<1s for 100 tokens), so a parallel
//     run is unlikely but possible — the mutex
//     prevents double-Telegram.

package tokenrotate

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"skygate/internal/backup"
	"skygate/internal/db"
)

// Storage key constants. Centralised here (not in the
// scheduler block) so the keys can be referenced from
// the B-check script + the manual trigger helper
// without an indirection. Same convention as
// internal/mesh/cleanup_scheduler.go (B143).
const (
	// KeyAutoRotateEnabled is the master switch for
	// the in-app auto-rotate scheduler. "1" =
	// enabled, "0" / missing = disabled. The
	// scheduler reads this on every tick so the
	// operator can flip from /my/tokens page (B153
	// follow-up UI) without restarting skygate.
	KeyAutoRotateEnabled = "tokens.auto_rotate_enabled"

	// KeyAutoRotateSchedule is the cron-style
	// schedule expression. Empty string → the
	// scheduler uses DefaultAutoRotateSchedule
	// (3 AM daily).
	KeyAutoRotateSchedule = "tokens.auto_rotate_schedule"

	// KeyAutoRotateLastRun is the unix-second
	// timestamp of the last successful trigger.
	// Updated after every trigger (even the
	// 0-token-tick), used for the same-minute dedup
	// check + the "Last run" display in the
	// /my/tokens page (future B154.1 follow-up).
	KeyAutoRotateLastRun = "tokens.auto_rotate_last_run"
)

// DefaultAutoRotateSchedule is the default 5-field
// cron expression. 3 AM daily — same window as the
// B130 update scheduler, so all lifecycle jobs share
// a single 03:00 trigger. Operators can override via
// SKYGATE_TOKEN_AUTO_ROTATE_SCHEDULE or the
// /my/tokens page (post-B154.1).
const DefaultAutoRotateSchedule = "0 3 * * *"

// DefaultAutoExtendDuration is the default
// extension window: 30 days. Same as the per-row
// Renew button's default (B153), so a token with
// auto_rotate=1 that's renewed by the cron job has
// the same lifetime as one the operator renews
// manually. Operators can shorten it via the
// /my/tokens page (post-B154.1) if they want
// shorter auto-rotation cycles.
const DefaultAutoExtendDuration = 30 * 24 * time.Hour

// AutoRotateTickInterval is the B154 scheduler's
// tick rate. 30s matches B130/B142/B143.
const AutoRotateTickInterval = 30 * time.Second

// DefaultAutoRotateCutoff is the default "how many
// days before expiry should a token be extended".
// 7 days gives the operator a full week to notice
// a "this token got auto-extended" Telegram alert
// before the user sees an expiry warning on the
// device.
const DefaultAutoRotateCutoff = 7 * 24 * time.Hour

// AutoRotateResult is the structured output of
// RunAutoExtend. The /my/tokens page renders the
// summary + the per-token list (post-B154.1); the
// audit log gets a single line; the Telegram
// alert gets a compact one-liner.
type AutoRotateResult struct {
	// Extended is the number of tokens whose
	// expires_at was successfully bumped to a
	// future date.
	Extended int
	// Failed is the number of tokens whose UPDATE
	// returned an error. Logged + Telegram'd per
	// row.
	Failed int
	// Tokens is the per-token detail. Each entry
	// has the old expiry + the new expiry + the
	// outcome, so the audit log and the Telegram
	// alert can list them by label.
	Tokens []AutoRotateTokenResult
	// TriggeredAt is the time the trigger fired
	// (used as the "last run" timestamp the page
	// renders).
	TriggeredAt time.Time
}

// AutoRotateTokenResult is the per-row outcome
// captured in AutoRotateResult.Tokens. The
// result is intentionally small — we only need
// the label + the new expiry + whether the
// UPDATE succeeded.
type AutoRotateTokenResult struct {
	ID          int64
	Label       string
	OldExpiry   time.Time
	NewExpiry   time.Time
	UpdatedRows int64
	Err         error
}

// SchedulerDeps groups the dependencies a
// Scheduler needs. Same pattern as
// backup.VerifySchedulerDeps (B142) and
// mesh.CleanupSchedulerDeps (B143).
type SchedulerDeps struct {
	// DB is used to read the schedule keys from
	// global_settings (every tick) and to run
	// the auto-extend UPDATE on trigger.
	DB *sql.DB
	// Notifier sends Telegram alerts when the
	// auto-extend actually extends tokens
	// (Extended > 0) and on hard failure. May be
	// nil (in which case the scheduler is silent
	// — the operator still sees the rotation in
	// the audit log). The scheduler accepts a
	// NotifierSink (any type with a SendAlert
	// method) to avoid a cycle with the telegram
	// package.
	Notifier NotifierSink
}

// NotifierSink is the subset of the
// telegram.Notifier interface that the scheduler
// needs. Same pattern as backup.NotifierSink
// (B142), update.NotifierSink (B130), and
// mesh.CleanupNotifierSink (B143) — a local
// interface so the tokenrotate package doesn't
// import the telegram package.
type NotifierSink interface {
	SendAlert(text string) int64
}

// inFlightRotate prevents parallel RunAutoExtend
// calls (e.g. the 3 AM cron + a manual trigger).
// The mutex is process-local; a long-running
// auto-extend (e.g. a 1000-token batch) blocks
// the next call, but that's desirable — we don't
// want to issue overlapping UPDATEs that
// double-alert.
var inFlightRotateMu sync.Mutex

// Start launches the in-app auto-rotate scheduler
// goroutine. Returns immediately; the goroutine
// runs until ctx is cancelled.
//
// Disabled state (deps.DB == nil) returns
// immediately without starting a goroutine, so
// main.go can call this unconditionally from the
// wire-up block. Same pattern as
// mesh.StartCleanupScheduler (B143).
func Start(ctx context.Context, deps SchedulerDeps) {
	if deps.DB == nil {
		log.Printf("auto-rotate-scheduler: disabled (nil DB)")
		return
	}
	ticker := time.NewTicker(AutoRotateTickInterval)
	go func() {
		defer ticker.Stop()
		// Tick once on startup so a freshly-restored
		// container doesn't have to wait 30s for the
		// first check (matches B130/B142/B143).
		autoRotateTick(ctx, deps)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				autoRotateTick(ctx, deps)
			}
		}
	}()
}

// autoRotateTick is one scheduler iteration. All
// side effects are guarded by recoverable error
// checks — a single failed tick is logged and
// swallowed; the next tick starts clean. Same
// pattern as update.tick (B130), verifyTick
// (B142), and cleanupTick (B143).
func autoRotateTick(ctx context.Context, deps SchedulerDeps) {
	// 1. Read the enabled flag. The flag is
	//    intentionally NOT a deps field — the
	//    operator can flip it from the /my/tokens
	//    page (B153) without restarting the
	//    skygate process, and the scheduler picks
	//    it up on the next tick.
	enabled, err := readEnabled(deps.DB)
	if err != nil {
		log.Printf("auto-rotate-scheduler: read enabled flag failed: %v", err)
		return
	}
	if !enabled {
		return // master switch off, scheduler silent
	}

	// 2. Read the schedule. Empty string → fall
	//    back to DefaultAutoRotateSchedule.
	sched, err := readSchedule(deps.DB)
	if err != nil {
		log.Printf("auto-rotate-scheduler: read schedule failed: %v", err)
		return
	}
	if sched == "" {
		sched = DefaultAutoRotateSchedule
	}
	parsed, err := backup.ParseSchedule(sched)
	if err != nil {
		log.Printf("auto-rotate-scheduler: parse schedule %q failed: %v", sched, err)
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
		log.Printf("auto-rotate-scheduler: read last-run failed: %v", err)
		return
	}
	if lastRun > 0 && sameRotationMinute(time.Unix(lastRun, 0), now) {
		return
	}

	// 4. Check if the schedule is due this
	//    tick.
	if !autoRotateIsDueThisTick(parsed, now) {
		return
	}

	// 5. ALL conditions met. Spawn the
	//    auto-extend.
	if _, err := RunAutoExtend(ctx, deps, now); err != nil {
		log.Printf("auto-rotate-scheduler: RunAutoExtend returned error: %v", err)
	}
}

// autoRotateIsDueThisTick returns true when the
// parsed schedule should fire within the current
// minute. Same logic as verifyIsDueThisTick
// (B142) and cleanupIsDueThisTick (B143) —
// reuses the existing backup.Schedule type.
func autoRotateIsDueThisTick(s *backup.Schedule, now time.Time) bool {
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

// RunAutoExtend is the actual SELECT + UPDATE +
// audit + alert sequence. Called from
// autoRotateTick on a due tick, and also from a
// future manual trigger in the /my/tokens page
// (post-B154.1).
//
// Returns the AutoRotateResult for the caller's
// logs/UI, and an error if the master SELECT
// failed (a per-row UPDATE error is captured in
// the per-row AutoRotateTokenResult.Err, not in
// the top-level error).
func RunAutoExtend(ctx context.Context, deps SchedulerDeps, now time.Time) (AutoRotateResult, error) {
	inFlightRotateMu.Lock()
	defer inFlightRotateMu.Unlock()

	res := AutoRotateResult{TriggeredAt: now}

	// 1. SELECT the candidate rows. cutoff is
	//    now+7d, so any token with expires_at
	//    in the next week is eligible.
	cutoff := now.Add(DefaultAutoRotateCutoff).Unix()
	tokens, err := db.ListAPITokensForAutoRotate(deps.DB, cutoff)
	if err != nil {
		// Master SELECT failed — bail out, send
		// a failure alert, write the last-run so
		// the next tick isn't dedup-skipped.
		_ = writeLastRun(deps.DB, now)
		sendAlert(deps.Notifier, fmt.Sprintf("❌ auto-rotate: SELECT failed: %v (will retry next tick)", err))
		return res, err
	}
	if len(tokens) == 0 {
		// Silent happy path: nothing to do.
		// Still write last-run so the dedup
		// check + the /my/tokens page's "Last
		// auto-rotate" timestamp advance.
		_ = writeLastRun(deps.DB, now)
		return res, nil
	}

	// 2. UPDATE each row. The new expiry is
	//    (now + DefaultAutoExtendDuration) =
	//    30 days from the trigger time. We use
	//    `now` for the math, not the original
	//    expires_at, so a token that's already
	//    expired (or about to) gets a clean
	//    30d-from-now window.
	newExpiry := now.Add(DefaultAutoExtendDuration).Unix()
	res.Tokens = make([]AutoRotateTokenResult, 0, len(tokens))
	for _, t := range tokens {
		row := AutoRotateTokenResult{
			ID:        t.ID,
			Label:     t.Label,
			OldExpiry: t.ExpiresAt,
			NewExpiry: time.Unix(newExpiry, 0),
		}
		rows, err := db.UpdateAPITokenExpiryByID(deps.DB, t.ID, newExpiry)
		if err != nil {
			row.Err = err
			res.Failed++
			log.Printf("auto-rotate-scheduler: id=%d label=%q UPDATE failed: %v", t.ID, t.Label, err)
		} else if rows == 0 {
			// The row was concurrently deleted
			// between SELECT and UPDATE. Not
			// really a failure, but treat it as
			// "skipped" (not counted in Extended).
			row.Err = fmt.Errorf("row not found (concurrent delete?)")
		} else {
			row.UpdatedRows = rows
			res.Extended++
		}
		res.Tokens = append(res.Tokens, row)
	}

	// 3. Write the last-run timestamp BEFORE
	//    the audit/alert so the /my/tokens page
	//    shows the timestamp even if the alert
	//    send hangs.
	_ = writeLastRun(deps.DB, now)

	// 4. Audit log: one row per trigger, with
	//    the per-token detail in the body.
	auditLine := formatAuditLine(res)
	if err := db.AppendAuditLogNoUser(deps.DB, "auto-rotate", "token.auto_rotate", auditLine); err != nil {
		log.Printf("auto-rotate-scheduler: audit insert failed: %v", err)
	}

	// 5. Telegram alert. Only sent if at
	//    least one token was extended or
	//    failed (a 0/0/0 result is a
	//    successful no-op, which the audit
	//    log already captured).
	if res.Extended > 0 || res.Failed > 0 {
		sendAlert(deps.Notifier, formatTelegramSummary(res))
	}
	return res, nil
}

// --- helpers ---

// readEnabled returns the in-app auto-rotate
// master switch. The DB key is
// tokens.auto_rotate_enabled; missing key or
// "0" = disabled, "1" = enabled.
func readEnabled(d *sql.DB) (bool, error) {
	s, err := getGlobalSetting(d, KeyAutoRotateEnabled, "")
	if err != nil {
		return false, err
	}
	return s == "1" || s == "true", nil
}

// readSchedule returns the cron expression from
// the DB. Empty string is OK — the caller falls
// back to DefaultAutoRotateSchedule.
func readSchedule(d *sql.DB) (string, error) {
	return getGlobalSetting(d, KeyAutoRotateSchedule, "")
}

// readLastRun returns the unix-second timestamp
// of the last scheduler trigger. 0 on missing
// row / parse error.
func readLastRun(d *sql.DB) (int64, error) {
	s, err := getGlobalSetting(d, KeyAutoRotateLastRun, "")
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
// /my/tokens page (post-B154.1) reads this same
// key to render "Last auto-rotate: <date>".
func writeLastRun(d *sql.DB, t time.Time) error {
	return setGlobalSetting(d, KeyAutoRotateLastRun, strconv.FormatInt(t.Unix(), 10))
}

// sameRotationMinute returns true if a and b
// are in the same calendar minute. Used to
// deduplicate: if the last successful trigger
// was at 03:00:15 today, the next tick at
// 03:00:45 today must NOT fire again. Mirrors
// mesh.sameCleanupMinute (B143) and
// update.sameMinute (B130).
func sameRotationMinute(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	return a.Year() == b.Year() &&
		a.Month() == b.Month() &&
		a.Day() == b.Day() &&
		a.Hour() == b.Hour() &&
		a.Minute() == b.Minute()
}

// formatAuditLine produces the per-trigger audit
// log detail string. Format:
//
//	auto-rotate: extended=<N> failed=<M> at=<RFC3339>
//	tokens:
//	  id=<I> label="<L>" old=<unix> new=<unix>
//	  ...
//
// Newlines are OK in the audit_log.detail
// column (TEXT in SQLite, TEXT in PG).
func formatAuditLine(r AutoRotateResult) string {
	out := fmt.Sprintf("auto-rotate: extended=%d failed=%d at=%s\ntokens:",
		r.Extended, r.Failed, r.TriggeredAt.UTC().Format(time.RFC3339))
	for _, t := range r.Tokens {
		line := fmt.Sprintf("\n  id=%d label=%q old=%d new=%d",
			t.ID, t.Label,
			t.OldExpiry.Unix(), t.NewExpiry.Unix())
		if t.Err != nil {
			line += fmt.Sprintf(" err=%q", t.Err.Error())
		}
		out += line
	}
	return out
}

// formatTelegramSummary produces the one-line
// Telegram alert. Compact (Telegram has a 4096
// char limit per message, but we want this
// short enough to read on a phone).
//
// Format: "🔄 auto-rotate: extended N token(s):
// <label1>, <label2>, ... [❌ M failed]"
func formatTelegramSummary(r AutoRotateResult) string {
	// Build the per-token list. Cap at 10
	// tokens in the alert to keep the message
	// scannable; if more, append "+N more"
	// (the audit log has the full list).
	const maxListed = 10
	listed := 0
	labels := ""
	failedLabels := ""
	for _, t := range r.Tokens {
		if t.Err != nil {
			if len(failedLabels) > 0 {
				failedLabels += ", "
			}
			failedLabels += t.Label
			continue
		}
		if listed >= maxListed {
			continue
		}
		if len(labels) > 0 {
			labels += ", "
		}
		labels += fmt.Sprintf("%s (→ %s)", t.Label, t.NewExpiry.Format("2006-01-02"))
		listed++
	}
	if r.Extended > maxListed {
		labels += fmt.Sprintf(" +%d more", r.Extended-maxListed)
	}
	msg := fmt.Sprintf("🔄 auto-rotate: extended %d token(s): %s", r.Extended, labels)
	if r.Failed > 0 {
		msg += fmt.Sprintf("\n❌ %d failed: %s", r.Failed, failedLabels)
	}
	return msg
}

// sendAlert is a nil-safe wrapper around
// NotifierSink.SendAlert. The whole scheduler
// degrades to silent (no Telegram) when the
// notifier is nil, but it never panics.
func sendAlert(n NotifierSink, text string) {
	if n == nil {
		return
	}
	_ = n.SendAlert(text)
}

// getGlobalSetting + setGlobalSetting are thin
// wrappers around the same-name db helpers, kept
// here so the scheduler doesn't import
// internal/db directly (avoids a package cycle
// through internal/feature/admin → internal/db
// → internal/tokenrotate).
//
// These call out via a function variable so the
// test suite can override them. In production
// they're set by init() in scheduler_db.go.
var (
	getGlobalSetting func(d *sql.DB, key, def string) (string, error)
	setGlobalSetting func(d *sql.DB, key, value string) error
)
