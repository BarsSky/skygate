// The scheduled auto-update wire-up for the skygate binary (B375).
//
// WHY THIS IS A FUNCTION AND NOT A BLOCK IN main()
// ------------------------------------------------
// B372 moved the TAIL of main() out and pinned a ceiling that may only fall
// (cmd/skygate/main.go had grown to 2568 lines of one linear function). B375 had
// to change the scheduler wire-up in the MIDDLE of the boot path — it sits with
// no `defer` around it, so it is safe to lift the same way the tail was — and
// lifting it keeps that contract satisfied instead of raising it.
//
// WHAT CHANGED FOR THE OPERATOR (the live defect this block exists to fix)
// -----------------------------------------------------------------------
// Measured on the reference deployment (PostgreSQL, 2026-10-09/10):
//
//   - global_settings held update_schedule_enabled = 1 / update_schedule_time =
//     08:00 — the operator had set exactly that in /admin/update;
//   - the container env held SKYGATE_UPDATE_SCHEDULE_ENABLED=false and .env did
//     not carry the variable at all, so config.go's default "false" won;
//   - the journal held ZERO `update-scheduler:` lines since the container
//     started — the goroutine had NEVER been created.
//
// Pre-B375 this whole block sat behind `if cfg.UpdateScheduleEnabled`, i.e.
// behind the ENV value, while internal/update/scheduler.go's own header
// documents the DB-persisted toggle as the authority. main()'s else-branch then
// told the operator that the /admin/update page could enable it — false: that
// page writes global_settings, it cannot start a goroutine.
//
// Post-B375 the scheduler is ARMED UNCONDITIONALLY and the DATABASE decides
// whether it acts. cfg.UpdateScheduleEnabled / cfg.UpdateScheduleTime stay in
// SchedulerCfg as the fallback for an install that has never opened the panel,
// so an untouched deployment behaves exactly as before. The arming is recorded
// with SetSchedulerArmed, and the boot sentence is built by SchedulerBootLog —
// a pure function whose truth is pinned by a unit test, because main() itself is
// not testable.

package main

import (
	"context"
	"log"

	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/handlers"
	"skygate/internal/update"
)

// wireUpdateScheduler starts the time-of-day auto-update scheduler (B130) — the
// runtime side of the B129 Schedule section on /admin/update. It arms the
// scheduler unconditionally, so the only thing left that decides whether an
// update happens is the database (plus the release check inside the tick).
func wireUpdateScheduler(ctx context.Context, d *db.ResettableDB, app *handlers.App, cfg *config.Config) {
	schedChecker := &update.Checker{
		Owner:          cfg.GitHubOwner,
		Repo:           cfg.GitHubRepo,
		Channel:        cfg.UpdateChannel,
		GitHubToken:    cfg.GitHubToken,
		CurrentVersion: app.BuildVersion,
	}
	schedCfg := update.SchedulerCfg{
		UpdateScheduleEnabled: cfg.UpdateScheduleEnabled,
		UpdateScheduleTime:    cfg.UpdateScheduleTime,
	}
	// Read the DB the SAME way the tick does, then say what is true.
	schedRead := update.ReadScheduleAnswers(d.DB, schedCfg)
	schedMsg, schedActive := update.SchedulerBootLog(true, schedRead)
	log.Printf("%s", schedMsg)
	update.SetSchedulerArmed(true, schedMsg)
	update.Start(ctx, update.SchedulerDeps{
		DB:           d.DB,
		State:        update.NewStateStore(""), // path resolved internally
		Checker:      schedChecker,
		BuildVersion: app.BuildVersion,
		Notifier:     schedulerNotifierSink(app.Notifier),
		RepoPath:     cfg.RepoPath,
		Cfg:          schedCfg,
	})
	if !schedActive {
		// The scheduler exists but will not fire, because the database says so
		// (the live B375 shape, in the benign direction). Stated as its own line
		// so a journal reader cannot mistake "armed" for "running".
		log.Printf("⏰ update-scheduler: armed but idle — enabled=%v, time=%s (the schedule is switched on /admin/update)",
			schedRead.Enabled, schedRead.Time)
	}
}
