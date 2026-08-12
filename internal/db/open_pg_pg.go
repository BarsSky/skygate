//go:build never

// 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — DISABLED.
// This file was the legacy build-tag wrapper around OpenPostgres used
// by the v0.27.0-v1.2.7 two-backend era. v1.3.0 (commit b1baa4a)
// removed the SQLite build path entirely; OpenDSN now opens pgx
// directly (see db.go line 96-113). The openPostgres wrapper below
// is dead code: it has no callers (grep 2026-08-12 — only this
// file references it) and the build tag (`//go:build postgres`) was
// a relic of the build-tag system that was also removed in v1.3.0.
//
// The file is kept in the tree (with `//go:build never`) for one
// release as a sentinel in case a forgotten call site resurfaces
// during the Phase 3 documentation pass. After v1.3.2 this file
// will be deleted entirely.
//
// DO NOT USE. Use db.OpenDSN(dsn string) directly.
package db
