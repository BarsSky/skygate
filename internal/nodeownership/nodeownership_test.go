// nodeownership_test.go — Backfill: the per-portal-user ownership pass.
//
// Backfill is the first stage of the B77 tick (AutoBackfill → runOneTick →
// Backfill). The original tests used the deleted openBackfillTestDB
// (hand-rolled SQLite :memory:) and were replaced by a t.Skip stub in the
// v1.3.0 SQLite→PG purge; the function's only remaining "coverage" was the live
// 5-minute loop on production.
//
// The tests below drive the REAL function against a REAL migrated database
// (db.OpenWithDialect + db.ApplyMigrations, see newAutoTestDB in auto_test.go)
// and a fake headscale client, and pin the two things an operator depends on:
// the guard that makes a call with no user a no-op, and the OIDC strategy that
// finally gives a node a dev-tag (plus the GC of rows whose node is gone).

package nodeownership

import (
	"testing"

	"skygate/internal/headscale"
)

// TestBackfill_ZeroUserOrUsernameIsANoOp: the guard at the top of Backfill.
// Both inputs come from the database (portal_users.id / .username) and an empty
// pair must not reach headscale or the DB — a zero portal user id used to
// produce rows owned by nobody.
func TestBackfill_ZeroUserOrUsernameIsANoOp(t *testing.T) {
	hs := newAutoTestLister(headscale.NodeView{ID: "2", Hostname: "workpc", UserName: "daniil"})

	// db is nil on purpose: the guard must return BEFORE the first DB call, so
	// a nil source must not be dereferenced.
	Backfill(nil, hs, hs.nodes, 0, "daniil", nil)
	Backfill(nil, hs, hs.nodes, 1, "", nil)

	invalidations, lists, batches, ownerCalls := hs.counts()
	if invalidations != 0 || lists != 0 || batches != 0 || ownerCalls != 0 {
		t.Errorf("a zero user id / empty username must not touch headscale (invalidations=%d lists=%d batches=%d tagOwnerCalls=%d)",
			invalidations, lists, batches, ownerCalls)
	}
	if got := hs.tagsFor(2); len(got) != 0 {
		t.Errorf("AddTag was called (%q) for a zero user id / empty username", got)
	}
}

// TestBackfill_AttributesOIDCNodeAndCollectsStaleRows runs one real pass over a
// migrated database: a node registered through the OIDC flow (no preauth key,
// no tags, headscale user name == portal username — Strategy E) is attributed to
// its portal user and gets its per-device dev-tag, while the snapshot row of a
// node that no longer exists in headscale is collected.
func TestBackfill_AttributesOIDCNodeAndCollectsStaleRows(t *testing.T) {
	// Without the base domain the dev-tag has no expressible owner, so Backfill
	// skips the EnsureTagOwner step (and headscale would refuse the tag).
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")

	src := newAutoTestDB(t)
	userID := seedAutoTestUser(t, src.DB, "daniil", 7)
	// A row whose node is gone from headscale: the GC pass must drop it
	// (otherwise a deleted device stays on the user's dashboard forever).
	seedAutoTestOwner(t, src.DB, "99", 7, "daniil", "tag:dev-daniil-old", "old")

	// Upper-case hostname on purpose: the applied tag must be lower-cased
	// (headscale 0.29 rejects upper-case tags — B176).
	hs := newAutoTestLister(headscale.NodeView{
		ID:        "2",
		Hostname:  "WorkPC",
		UserName:  "daniil",
		UserID:    "7",
		CreatedAt: "",
	})

	Backfill(src, hs, hs.nodes, userID, "daniil", nil)

	var stale int
	if err := src.DB.QueryRow(`SELECT COUNT(*) FROM node_owner_map WHERE node_id = '99'`).Scan(&stale); err != nil {
		t.Fatalf("count stale row: %v", err)
	}
	if stale != 0 {
		t.Errorf("node_owner_map still has %d row(s) for the deleted node 99, want 0 (the GC pass must collect them)", stale)
	}

	var username, hostname string
	if err := src.DB.QueryRow(
		`SELECT username, hostname FROM node_owner_map WHERE node_id = '2'`).Scan(&username, &hostname); err != nil {
		t.Fatalf("node_owner_map has no row for the OIDC node 2: %v", err)
	}
	if username != "daniil" {
		t.Errorf("node_owner_map.username = %q, want %q", username, "daniil")
	}
	if hostname != "WorkPC" {
		t.Errorf("node_owner_map.hostname = %q, want %q (the live headscale hostname)", hostname, "WorkPC")
	}

	// Strategy E records tag:private for the node AND applies the per-device
	// dev-tag: headscale ends up with both (AddTag is additive).
	applied := hs.tagsFor(2)
	if !hasTag(applied, "tag:dev-daniil-workpc") {
		t.Errorf("dev tag not applied to node 2 (got %v, want tag:dev-daniil-workpc — lower-cased hostname)", applied)
	}
	if !hasTag(applied, "tag:private") {
		t.Errorf("tag:private not applied to node 2 (got %v) — Strategy E's scope tag", applied)
	}
	// The per-user path permits the tag one at a time (EnsureTagOwner); the
	// BATCH form (EnsureTagOwners) belongs to the reconciler, not here.
	if _, _, batches, ownerCalls := hs.counts(); batches != 0 || ownerCalls != 1 {
		t.Errorf("tag-owner calls = batches=%d perTag=%d, want batches=0 perTag=1", batches, ownerCalls)
	}
}
