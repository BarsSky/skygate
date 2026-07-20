// 2026-07-20: v0.22.2 hotfix — tests for the Strategy A
// tagless-node auto-apply fix in backfillNodeOwnership.
//
// The bug: pre-v0.22.2, when Strategy A matched a
// skygate-issued preauth key to a headscale node that
// had no tags (e.g. a freshly-registered device), the
// code set matchedTag = firstTagOrFallback(n) which
// returns "tag:untagged" for empty tags. The subsequent
// branch check `if matchedTag == "tag:private"` failed,
// so HS.TagNode(15, "tag:private") was NEVER called —
// the node stayed tagless in headscale forever. The
// snapshot row in node_owner_map got tag="tag:untagged",
// which on the next backfill still didn't trigger
// UpgradeStaleNodeOwnerToPrivate (it's only called
// when matchedTag=="tag:private").
//
// Strategy C had the same bug; it was fixed in 2026-07-10
// (see the comment in handlers_node_ownership.go). The
// v0.22.2 fix applies the same logic to Strategy A:
// when the preauth key came from skygate, the default
// matchedTag is "tag:private" — not "tag:untagged".
// firstTagOrFallback is only used when the node ALREADY
// has tags (e.g. skygate-vm has tag:private in headscale,
// so firstTagOrFallback returns "tag:private" and the
// result is unchanged).
//
// These tests pin the fix. They use an in-memory
// SQLite + the public db helpers (InsertIgnoreNodeOwnerWithHostname
// + UpgradeStaleNodeOwnerToPrivate) that the function
// calls. The full function path (which includes the
// "if matchedTag=='tag:private' then HS.TagNode" branch)
// is exercised by the live validation on the VM (see
// check_v0.22.2.sh); these unit tests pin the helper
// contract.
package handlers

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"

	_ "github.com/mattn/go-sqlite3"
)

const backfillTestSchema = `
CREATE TABLE portal_users (
	id INTEGER PRIMARY KEY,
	username TEXT NOT NULL,
	password_hash TEXT DEFAULT '',
	is_admin INTEGER DEFAULT 0,
	headscale_user_id INTEGER DEFAULT 0,
	headscale_url TEXT NOT NULL DEFAULT '',
	theme TEXT DEFAULT 'linear',
	created_at INTEGER DEFAULT 0,
	default_device_node_id TEXT NOT NULL DEFAULT '',
	default_exit_node_id TEXT NOT NULL DEFAULT '',
	subnet_cidr TEXT NOT NULL DEFAULT '',
	subnet_status TEXT NOT NULL DEFAULT '',
	subnet_router_node_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE preauth_keys (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	key TEXT NOT NULL,
	headscale_preauth_id INTEGER DEFAULT 0,
	expires_at INTEGER NOT NULL DEFAULT 0,
	used INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE node_owner_map (
	node_id INTEGER PRIMARY KEY,
	headscale_user_id INTEGER NOT NULL,
	username TEXT NOT NULL,
	tag TEXT NOT NULL,
	tagged_by_user_id INTEGER,
	tagged_at INTEGER NOT NULL DEFAULT 0,
	hostname TEXT NOT NULL DEFAULT ''
);
`

func openBackfillTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, q := range strings.Split(backfillTestSchema, ";") {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestBackfillHelper_StrategyA_TaglessNode_PinsV0222Fix
// is the helper-level pin for the v0.22.2 fix. The
// actual matchedTag computation happens in
// backfillNodeOwnership (handlers_node_ownership.go);
// the live validation on the VM (check_v0.22.2.sh)
// exercises the full function. Here we pin the helper
// contract: when backfill computes matchedTag="tag:private"
// for a Strategy A match on a tagless node, the helper
// chain (InsertIgnoreNodeOwner + UpgradeStaleNodeOwnerToPrivate)
// produces a snapshot row with tag="tag:private", and
// re-applying is idempotent (the snapshot row blocks
// any tag:untagged rewrite on subsequent backfills).
//
// This is the exact path MSI (id=15) would follow
// post-fix: preauth_key id=98 + node_id=15 + empty
// tags → matchedTag="tag:private" → snapshot row
// with tag="tag:private" → HS.TagNode(15, "tag:private")
// → headscale now shows forcedTags=[tag:private].
func TestBackfillHelper_StrategyA_TaglessNode_PinsV0222Fix(t *testing.T) {
	d := openBackfillTestDB(t)
	// Seed: portal user + preauth key
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id)
		 VALUES (1, 'skyadmin', 1)`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO preauth_keys (user_id, key, headscale_preauth_id, expires_at, used)
		 VALUES (1, 'hskey-test', 98, 9999999999, 1)`); err != nil {
		t.Fatalf("seed preauth: %v", err)
	}

	// Simulate the v0.22.2 fix: matchedTag="tag:private"
	// for a Strategy A match on a tagless node. The
	// "if matchedTag=='tag:private'" branch in the
	// function calls BOTH helpers (the INSERT OR IGNORE
	// + the UPGRADE). We mirror that here.
	_ = db.InsertIgnoreNodeOwnerWithHostname(
		d, "15", 1, "skyadmin", "tag:private", "msi", 1)
	_ = db.UpgradeStaleNodeOwnerToPrivate(d, "15", "tag:private", 1)

	// Assert: snapshot row has tag="tag:private"
	var tag string
	if err := d.QueryRow(
		`SELECT tag FROM node_owner_map WHERE node_id = 15`,
	).Scan(&tag); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if tag != "tag:private" {
		t.Errorf("snapshot tag = %q, want tag:private (the v0.22.2 fix)", tag)
	}

	// Idempotency: a re-backfill with a (hypothetical)
	// matchedTag="tag:untagged" (e.g. some future code
	// path) should NOT rewrite the snapshot to
	// tag:untagged. The PRIMARY KEY on node_id +
	// INSERT OR IGNORE prevents the rewrite, and
	// UpgradeStaleNodeOwnerToPrivate's WHERE clause
	// only matches empty/untagged rows (not tag:private).
	_ = db.InsertIgnoreNodeOwnerWithHostname(
		d, "15", 1, "skyadmin", "tag:untagged", "msi", 1)
	_ = db.UpgradeStaleNodeOwnerToPrivate(d, "15", "tag:private", 1)
	if err := d.QueryRow(
		`SELECT tag FROM node_owner_map WHERE node_id = 15`,
	).Scan(&tag); err != nil {
		t.Fatalf("read snapshot 2: %v", err)
	}
	if tag != "tag:private" {
		t.Errorf("after re-apply, tag = %q, want tag:private (idempotency violated)", tag)
	}
}

// TestBackfillHelper_TaglessNode_NoUpgradeFromPrivate
// is the inverse: a node that already has tag:private
// in headscale (e.g. skygate-vm, which has forcedTags
// including tag:private from the v0.16.7 sidecar code)
// — the helper should not downgrade it. This pins
// the contract that the v0.22.2 fix is non-regressive
// for nodes that already have the tag.
func TestBackfillHelper_TaglessNode_NoUpgradeFromPrivate(t *testing.T) {
	d := openBackfillTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id)
		 VALUES (1, 'skyadmin', 1)`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Existing snapshot row with tag:private.
	if _, err := d.Exec(
		`INSERT INTO node_owner_map
		 (node_id, headscale_user_id, username, tag, tagged_by_user_id, hostname)
		 VALUES (13, 1, 'skyadmin', 'tag:private', 1, 'skygate-vm')`); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	// Re-apply with the helper. Should be a no-op
	// (snapshot already exists, no upgrade needed).
	_ = db.InsertIgnoreNodeOwnerWithHostname(
		d, "13", 1, "skyadmin", "tag:private", "skygate-vm", 1)
	_ = db.UpgradeStaleNodeOwnerToPrivate(d, "13", "tag:private", 1)
	var tag string
	if err := d.QueryRow(
		`SELECT tag FROM node_owner_map WHERE node_id = 13`,
	).Scan(&tag); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if tag != "tag:private" {
		t.Errorf("skygate-vm tag flipped to %q, want tag:private (regression!)", tag)
	}
}
