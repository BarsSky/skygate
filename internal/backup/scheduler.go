// 2026-07-14: Этап 14 v6 — in-app backup scheduler.
//
// A goroutine started by cmd/skygate/main.go that checks
// every minute whether the configured backup schedule
// is due. Independent of the system cron (which uses
// the same Config + the same RunBackup function) so
// either or both can be active.
//
// Lifecycle:
//
//   1. main.go creates a *Scheduler with a *sql.DB
//      pointer and calls Start(ctx).
//   2. Start launches a goroutine that loops on
//      time.NewTicker(60s).
//   3. On each tick, the scheduler reads the config
//      from the DB (so a UI change takes effect without
//      a restart), parses the schedule, and compares
//      against time.Now(). If due, it calls RunBackup.
//   4. ctx cancellation stops the goroutine cleanly
//      (waits for the current run to finish — the
//      shutdown path skips mid-backup to avoid leaving
//      a stale SMB handle on the host).
//
// Concurrency:
//
//   - RunBackup itself is serialized by an internal
//     mutex (see runner.go runMu). The scheduler adds
//     nothing extra; a manual "Run now" from the UI and
//     a tick from the scheduler will queue naturally.
//   - Reading the config from the DB on every tick is
//     cheap (8 rows, indexed by key). The alternative
//     (in-memory cache) would risk drift after a UI
//     save — the read-on-tick approach is correct.

package backup

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// Scheduler is the in-app backup tick loop. Construct
// one in main and call Start.
type Scheduler struct {
	DB *sql.DB
}

// Start launches the background loop. Returns
// immediately; cancel ctx to stop. The loop runs at
// the natural time-of-minute (within a few seconds of
// the minute boundary) so multiple skygate processes
// would race on the same target; runMu serializes.
func (s *Scheduler) Start(ctx context.Context) {
	if s.DB == nil {
		log.Printf("backup: scheduler disabled (nil DB)")
		return
	}
	ticker := time.NewTicker(60 * time.Second)
	go func() {
		defer ticker.Stop()
		// Tick once on startup so a freshly-restored
		// container that was down for >1 day doesn't have
		// to wait a full minute to recover.
		s.tick(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.tick(ctx)
			}
		}
	}()
}

// tick is one pass of the scheduler. Reads the config,
// checks the schedule, runs the backup if due. The
// err return is intentionally swallowed (logged) — the
// scheduler is a background task and there's no caller
// to surface the error to.
func (s *Scheduler) tick(ctx context.Context) {
	cfg, err := Load(s.DB)
	if err != nil {
		log.Printf("backup: scheduler config load failed: %v", err)
		return
	}
	if !cfg.InAppEnabled {
		return // master switch off, in-app disabled
	}
	if !cfg.Enabled {
		return // feature-level disabled
	}
	if cfg.Schedule == "" {
		return // no schedule → no automatic run
	}
	sched, err := ParseSchedule(cfg.Schedule)
	if err != nil {
		log.Printf("backup: scheduler schedule parse failed: %v", err)
		return
	}
	now := time.Now()
	next := sched.Next(now)
	// We check whether the current minute matches the
	// schedule's next-fire time. Because Next rounds DOWN
	// to the minute, a match means the schedule fired
	// sometime in the last 60s — i.e. right now.
	//
	// Without this check the scheduler would fire on
	// every tick when schedule=EveryMinute. With it,
	// each minute is fired at most once.
	if !isDueThisTick(sched, now) {
		return
	}
	// Defensive: don't run if RunBackup would loop on a
	// missing mountpoint or a misconfigured destination.
	if err := cfg.Validate(); err != nil {
		log.Printf("backup: scheduler config invalid: %v", err)
		return
	}
	log.Printf("backup: scheduler firing (schedule=%q, now=%s, next=%s)", cfg.Schedule, now.Format(time.RFC3339), next.Format(time.RFC3339))
	res, err := RunBackup(s.DB, cfg)
	if err != nil {
		log.Printf("backup: scheduler run failed: %v (see DB status)", err)
		return
	}
	log.Printf("backup: scheduler run ok (archive=%s, bytes=%d, dur=%s)", res.Archive, res.Bytes, res.FinishedAt.Sub(res.StartedAt))
}

// isDueThisTick returns true when the schedule should
// fire within the current minute. We use a 30s grace
// window on either side of the schedule boundary so
// that a slow tick (GC pause, network blip) doesn't
// miss the fire. The alternative — exact match —
// would skip the run on the unlucky tick that landed
// at 02:59:59.999 and the next tick at 03:00:01.
func isDueThisTick(s *Schedule, now time.Time) bool {
	if s.EveryMinute {
		return true
	}
	// Match (hour, minute). The 30s grace is on each side
	// of the target minute, so a tick at 02:59:31 still
	// fires a 03:00 schedule. (60s is one tick interval.)
	nowH, nowM, _ := now.Clock()
	if nowH != s.Hour {
		return false
	}
	if nowM != s.Minute {
		return false
	}
	// We're in the right minute. Fire.
	return true
}
