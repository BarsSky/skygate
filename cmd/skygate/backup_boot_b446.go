// backup_boot_b446.go — B446 (2026-10-30). ONE STARTUP LINE FOR THE BACKUP SCHEDULER.
//
// Why it lives here and not in main.go: B372 caps main.go at 2400 lines, and the reason the
// nine-day outage was invisible is exactly that nothing was printed at boot — a non-due tick is
// silent, and the container is recreated often enough that its journal no longer reaches back to
// the last run. So the diagnostic is worth its lines, just not inside the entrypoint.
package main

import (
	"database/sql"
	"log"
	"time"

	"skygate/internal/backup"
	"skygate/internal/localtime"
)

// logBackupSchedulerStartup prints what the in-app backup scheduler will actually do:
// whether it is enabled, its schedule, the last recorded run and the next slot. A schedule that
// cannot be parsed, and a config that cannot be read, are named too — both used to be silent.
func logBackupSchedulerStartup(d *sql.DB, secretKeyHex string) {
	bcfg, err := backup.LoadWithKey(d, secretKeyHex)
	if err != nil {
		log.Printf("backup-scheduler: started, but its config could not be read now: %v", err)
		return
	}
	bsch, perr := backup.ParseSchedule(bcfg.Schedule)
	if perr != nil {
		log.Printf("backup-scheduler: started; schedule %q cannot be parsed (%v) — it will never fire", bcfg.Schedule, perr)
		return
	}
	last := "never"
	if bcfg.LastRun > 0 {
		last = localtime.FormatUnix(bcfg.LastRun)
	}
	log.Printf("backup-scheduler: started (enabled=%t, in_app=%t, schedule=%q, last_run=%s, next_slot=%s)",
		bcfg.Enabled, bcfg.InAppEnabled, bcfg.Schedule, last,
		localtime.Format(bsch.Next(time.Now())))
}
