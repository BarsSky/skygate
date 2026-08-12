// 2026-07-20: v0.22.0 — mesh package tests.
//
// v1.3.0: Tests used an in-memory SQLite DB with hand-rolled
// CREATE TABLE statements. PG-rewrite is a Phase 2 follow-up
// (SERIAL, $N, RETURNING id). The same code paths
// (CreateMesh / JoinMesh / LeaveMesh / DissolveMesh / List*)
// are exercised on a real PG instance by the
// mesh integration test in /admin/meshes (UI smoke test).

package mesh

import "testing"

func TestMesh_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: mesh_test.go used SQLite-specific CREATE TABLE + ? placeholders + LastInsertId(). Rewrite for PG in Phase 2. The mesh package is exercised by the /admin/meshes integration test on PG.")
}
