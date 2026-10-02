// 2026-08-18 (B143, v1.4.3) — periodic cleanup of smoke-mesh test data.
//
// Pre-B143: scripts/smoke.sh:511-512 creates "smoke-mesh-<pid>"
// rows in the meshes table on every run. The /admin/meshes
// smoke test step is the only consumer of these rows, and
// they have 0 members by the time smoke.sh exits, so they
// serve no purpose after the smoke run completes. Without
// periodic cleanup, the DB accumulates cruft over time
// (the operator's v0.33.1.36 release had to manually
// DELETE 30 rows in a one-off SQL).
//
// B143 adds DeleteSmokeMeshes + StartCleanupScheduler (the
// daily cron, B130/B142 pattern) so the DB stays clean
// without operator action. The pattern is opt-in via
// global_settings["cleanup.smoke_mesh_enabled"] (default
// false — operator enables from the page or via
// SKYGATE_CLEANUP_SMOKE_MESH_IN_APP_ENABLED=true env var).
//
// Safety: only rows matching name LIKE 'smoke-mesh-%' AND
// with no mesh_members rows are deleted. Real meshes
// (created by users via /my/meshes) never match this name
// pattern, so the cleanup cannot touch them. Even if a
// future mesh name conflicts with the prefix, the
// NOT EXISTS member check prevents deleting an active
// shared network.

package mesh

import (
	"database/sql"
	"fmt"

	"skygate/internal/db"
)

// SmokeMeshNamePrefix is the LIKE pattern the smoke test
// uses. Exported so the test in cleanup_b143_test.go can
// pin the contract (and so an operator-side audit query
// can find the cruft without remembering the exact prefix).
const SmokeMeshNamePrefix = "smoke-mesh-"

// CleanupResult is the return value of DeleteSmokeMeshes.
// IDs is the list of mesh row IDs that were deleted (for
// the audit log message and the Telegram alert). The
// caller (cleanup_scheduler.go) writes the IDs to the
// audit_log so the operator can correlate future
// "this mesh disappeared" reports with the cleanup tick.
type CleanupResult struct {
	IDs   []int64
	Names []string
	Total int
}

// DeleteSmokeMeshes removes every meshes row whose name
// starts with SmokeMeshNamePrefix AND has zero members
// (NOT EXISTS subquery against mesh_members). The
// mesh_members CASCADE on the FK handles the (defense-in-
// depth) case where a row has members that slipped through.
//
// Returns the list of deleted IDs + names so the caller can
// log them. A 0-length result is the normal "nothing to
// clean" case — the scheduler treats it as "scheduler ran
// ok, nothing to alert on".
//
// The whole operation runs in a single transaction so a
// failure mid-way doesn't leave the DB in a half-deleted
// state.
func DeleteSmokeMeshes(d *sql.DB) (CleanupResult, error) {
	var res CleanupResult
	tx, err := d.Begin()
	if err != nil {
		return res, fmt.Errorf("DeleteSmokeMeshes: begin: %w", err)
	}
	// Rollback is a no-op after a successful Commit, so
	// deferring it is safe (and ensures cleanup if we
	// return early on error).
	defer tx.Rollback()

	// Step 1: SELECT the candidate rows. We do this in
	// the same tx so the list reflects a consistent
	// snapshot (no rows disappearing between the SELECT
	// and the DELETE).
	rows, err := tx.Query(`
		SELECT m.id, m.name
		FROM meshes m
		WHERE m.name LIKE $1
		  AND NOT EXISTS (
		    SELECT 1 FROM mesh_members mm WHERE mm.mesh_id = m.id
		  )
		ORDER BY m.id`,
		SmokeMeshNamePrefix+"%",
	)
	if err != nil {
		return res, fmt.Errorf("DeleteSmokeMeshes: select: %w", err)
	}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return res, fmt.Errorf("DeleteSmokeMeshes: scan: %w", err)
		}
		res.IDs = append(res.IDs, id)
		res.Names = append(res.Names, name)
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("DeleteSmokeMeshes: rows: %w", err)
	}
	rows.Close()
	res.Total = len(res.IDs)
	if res.Total == 0 {
		// Nothing to delete. Commit the empty tx
		// (just a SELECT, no writes) and return.
		if err := tx.Commit(); err != nil {
			return res, fmt.Errorf("DeleteSmokeMeshes: commit (empty): %w", err)
		}
		return res, nil
	}

	// Step 2: DELETE in a single statement. The id list is passed as N
	// separate placeholders in an IN (…) list rather than as one
	// PostgreSQL array parameter.
	//
	// `WHERE id = ANY($1::bigint[])` — the pre-2026-10-01 form — is
	// PostgreSQL-only syntax, and this code runs from
	// mesh.StartCleanupScheduler on EVERY install kind, so on SQLite the
	// statement died with
	//
	//	SQL logic error: near "[]": syntax error (1)
	//
	// (the B282 dialect-leak class; measured, not inferred — reverting the
	// fix makes TestDeleteSmokeMeshes_SQLite fail with exactly that text).
	// It was invisible until it mattered: the Total==0 early return above
	// means the DELETE is only reached when there is something to clean,
	// so B143's cleanup was silently dead on every SQLite install exactly
	// when it had work to do.
	//
	// `$N` is the universal placeholder form — SQLite accepts it as a
	// named parameter and modernc.org/sqlite binds it by Go argument
	// ordinal — so one statement text serves both backends
	// (internal/db/placeholders.go states the rule). The name-LIKE
	// subquery is repeated so a row that gained a member between the
	// SELECT and the DELETE is still protected (defense in depth).
	idPlaceholders := db.PlaceholdersList(len(res.IDs))
	likePlaceholder := db.PlaceholderAt(len(res.IDs)+1, len(res.IDs))
	args := make([]any, 0, len(res.IDs)+1)
	for _, id := range res.IDs {
		args = append(args, id)
	}
	args = append(args, SmokeMeshNamePrefix+"%")

	delRes, err := tx.Exec(fmt.Sprintf(`
		DELETE FROM meshes
		WHERE id IN (%s)
		  AND name LIKE %s
		  AND NOT EXISTS (
		    SELECT 1 FROM mesh_members mm WHERE mm.mesh_id = meshes.id
		  )`, idPlaceholders, likePlaceholder), args...)
	if err != nil {
		return res, fmt.Errorf("DeleteSmokeMeshes: delete: %w", err)
	}
	n, err := delRes.RowsAffected()
	if err != nil {
		return res, fmt.Errorf("DeleteSmokeMeshes: rows affected: %w", err)
	}
	if int(n) != res.Total {
		// Race: some rows were deleted between SELECT
		// and DELETE (a manual cleanup or a parallel
		// smoke run). Not an error — just log the
		// delta. The Total field still reflects the
		// SELECT count (which is what the caller
		// reports in the audit/Telegram message);
		// the actual DB write affected n rows.
		res.Names = res.Names[:n]
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("DeleteSmokeMeshes: commit: %w", err)
	}
	return res, nil
}

// FormatCleanupMessage returns a human-readable summary
// of a cleanup tick. Used by both the scheduler (for
// the audit_log message and the optional Telegram
// alert) and the manual subcommand. Empty result → short
// "no smoke-mesh cruft" message.
func FormatCleanupMessage(res CleanupResult) string {
	if res.Total == 0 {
		return "cleanup: no smoke-mesh cruft found"
	}
	if res.Total == 1 {
		return fmt.Sprintf("cleanup: removed 1 smoke-mesh row (id=%d name=%q)", res.IDs[0], res.Names[0])
	}
	// Truncate the name list at 5 for readability (a
	// single run that deletes 50 rows would otherwise
	// produce a 1KB+ audit message).
	preview := res.Names
	if len(preview) > 5 {
		preview = append([]string{}, preview[:5]...)
		preview = append(preview, fmt.Sprintf("... (%d more)", res.Total-5))
	}
	return fmt.Sprintf("cleanup: removed %d smoke-mesh rows (ids=%v names=%v)",
		res.Total, res.IDs, preview)
}
