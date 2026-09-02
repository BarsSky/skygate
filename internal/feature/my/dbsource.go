// Package my — dbsource.go defines the DBSource
// interface for the my Service. B210 (v1.5.0+,
// 2026-09-02) — fixes the B203 hot-reload regression
// for the /my/* user-facing routes (devices, keys,
// preauth, exit-nodes, audit, account audit export,
// device-pref, meshes, notifications, dashboard,
// telegram-link, settings).
//
// The regression
//
// The my Service captured `*sql.DB` at construction
// time. After the B203 watchdog's first swap on
// container start, the captured pointer points at a
// closed pool → "sql: database is closed" → every
// /my/* handler 500s. The /my/devices page is the
// most visible (it was the user's primary complaint
// in the B210 report).
//
// The fix
//
// Replace `DB *sql.DB` (frozen pointer) with
// `DB DBSource` (live getter). `s.dbc()` returns the
// current pool on every call. The ResettableDB from
// internal/db (B203) satisfies DBSource directly.
// main.go passes the ResettableDB (named `d`) instead
// of `app.DB`. The BackfillNodeOwnership callback also
// gets DBSource-typed so the same swap-safety applies
// to the lazy-backfill path on /my/devices and
// /dashboard.
//
// Why this regression slipped past B-check + live-verify
//
// Same root cause as B208.1 / B210 auth. The B-checks
// for the my package (B123, B132, B170, B171, B184,
// B185) call handlers in sequence and the B203
// watchdog's first hot-reload fires ~5s after the
// container starts — after the B-checks have finished.
// Live-verifies on the agent surfaced the device-page
// regression via the empty-tab user symptom.

package my

import "database/sql"

// DBSource is the minimum surface the my Service
// needs to obtain the current *sql.DB. The
// ResettableDB wrapper from internal/db (B203)
// satisfies it via its Current() method.
type DBSource interface {
	Current() *sql.DB
}

// dbc returns the my Service's current *sql.DB. Used
// at every call site that previously captured
// `*sql.DB` (s.DB.Query, db.ListXxx(s.DB, ...), etc.)
// so the watchdog's hot-reload is followed
// transparently.
func (s *Service) dbc() *sql.DB {
	if s.DB == nil {
		return nil
	}
	return s.DB.Current()
}
