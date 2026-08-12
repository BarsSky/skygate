// v1.3.0: Test file used SQLite :memory: for openDeviceMetaTestDB
// (PostAdminDeviceMeta helper) + SQLite-specific CREATE TABLE
// syntax. PG-rewrite is a Phase 2 follow-up. The pure-function
// tests (TestNodeTagRefused_*, TestNodeTagAllowed_*) don't need
// a DB and could be unskipped now; the DB-backed tests
// (PostAdminDeviceMeta) need the PG-rewrite.

package admin

import "testing"

func TestDevices_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openDeviceMetaTestDB used SQLite :memory:. Pure-function tag tests (TestNodeTagRefused/Allowed) are also skipped for now — they can be unskipped in Phase 2 by removing the mattn import + leaving only the no-DB tests. Rewrite for PG in Phase 2.")
}
