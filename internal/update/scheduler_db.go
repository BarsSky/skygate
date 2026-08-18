// 2026-08-18 (B130) — db shim for the update scheduler.
// The scheduler (scheduler.go) reads/writes global_settings
// for the schedule + last-run keys. Importing internal/db
// directly from internal/update would create a package
// cycle (internal/db doesn't import internal/update, but
// the test fixtures in internal/db/.../test_*.go sometimes
// do, and a direct import would block refactors later).
//
// The pattern matches the one in
// internal/headscale_version/monitor.go and
// internal/release/monitor_runner.go: the scheduler defines
// the function variables (getGlobalSetting, setGlobalSetting)
// and this file's init() binds them to the real db helpers.
// In tests, init() can be skipped via build tags OR a
// re-init function can rebind the variables.

package update

import (
	"database/sql"

	"skygate/internal/db"
)

func init() {
	getGlobalSetting = func(d *sql.DB, key, def string) (string, error) {
		return db.GetGlobalSetting(d, key, def)
	}
	setGlobalSetting = func(d *sql.DB, key, value string) error {
		return db.SetGlobalSetting(d, key, value)
	}
}
