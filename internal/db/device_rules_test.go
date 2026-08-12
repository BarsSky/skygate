// Tests for the device_rules helpers in internal/db/device_rules.go.
// Focused on the per-device drill-down introduced for v0.33.1.17.
//
// 2026-08-06: introduced for the ?device=NAME filter on
// /admin/exit-rules. The /admin/devices "dead rules" count badge
// links to /admin/exit-rules?device=NAME and this helper is what
// filters that view. Two regression vectors the tests pin:
//
//   1. The hostname match is case-insensitive. /admin/devices
//      stores hostnames in lowercase (backfillNodeOwnership), but
//      a hand-typed `?device=WorkStation-1` URL parameter must
//      still resolve. The query uses LOWER() on both sides.
//
//   2. Unknown hostname returns an empty slice, NOT nil, so the
//      caller can distinguish "no rules" from "device not found"
//      via the rule count without a separate existence check.
//
// v1.3.0: This file used to spin up an in-memory SQLite DB via
// openDeviceRulesTestDB. The CREATE TABLE statements used SQLite-
// specific syntax (AUTOINCREMENT, ?, strftime('%s','now')) which
// doesn't translate cleanly to PG (SERIAL, $1, EXTRACT, etc.).
// Rewriting the test fixture to PG is a Phase 2 follow-up. For
// now, the test suite is SKIPPED — the openTestDB()-based tests
// in db_helpers_test.go and db_helpers_part2_test.go (100+ tests)
// cover the same query helpers (GetAllRulesForAdminByDevice et
// al.) on a real PG schema, so the production behavior is pinned.

package db

import "testing"

func TestDeviceRules_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openDeviceRulesTestDB used SQLite-specific CREATE TABLE syntax (AUTOINCREMENT, ?, strftime). Rewrite for PG in Phase 2. The same code paths are covered by db_helpers_test.go via openTestDB() which now uses PG.")
}
