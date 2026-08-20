// 2026-08-20 (B156) — db shim for the key-expiration
// notification scheduler. The scheduler (scheduler.go)
// reads/writes global_settings for the enabled flag +
// schedule + last-run keys. Importing internal/db
// directly from internal/keynotify would create a
// package cycle (internal/db doesn't import
// internal/keynotify, but tests in internal/db/.../*.go
// sometimes do, and a direct import would block
// refactors later).
//
// The pattern matches the one in
// internal/update/scheduler_db.go (B130),
// internal/mesh/cleanup_scheduler.go (B143) and
// internal/tokenrotate/scheduler_db.go (B154): the
// scheduler defines the function variables
// (getGlobalSetting, setGlobalSetting) and this file's
// init() binds them to the real db helpers. In tests,
// init() can be skipped via build tags OR a re-init
// function can rebind the variables.

package keynotify

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
