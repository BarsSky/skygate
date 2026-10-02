// Boot-time provisioning helpers for the skygate binary.
//
// First-run work that must happen before the server is useful: the primary
// portal admin (B264), the first-run OIDC/headscale auto-sync, the
// node_owner_map backfill, the `infra`/per-user headscale users and the
// Telegram bootstrap from the environment. main() calls them in a fixed order,
// and each is idempotent. Moved verbatim out of cmd/skygate/main.go during
// refactor Phase D (2026-10-02).

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/headscale"
	"strconv"
	"strings"
)

// truncateForDB clamps a string to max bytes, appending
// "..." when truncated. Used by runBackupVerifyFail to
// keep global_settings values bounded (a 4096-char
// detail in a TEXT column is fine, but the page render
// truncates anyway and storing 1MB of replay stderr
// just bloats the DB).
func truncateForDB(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// bootstrapAdmin creates the admin user in Skygate DB on first start.
//
// 2026-09-19: v0.72 (B264) — the bootstrap admin is also the IMMUTABLE
// PRIMARY admin (portal_users.is_primary=1), the single row the
// /admin/users handlers refuse to demote, delete or rename.
//
// Both branches now maintain that marker:
//
//   - first start (no row): the INSERT sets is_admin=1 AND is_primary=1;
//   - already-exists: the row is re-asserted to is_admin=1 + is_primary=1
//     so the marker self-heals after an out-of-band demotion, or after
//     the V072 backfill could not match SKYGATE_ADMIN_USER (the
//     migration's documented fallback then marks the lowest-id admin —
//     this branch moves the marker to the configured name).
//
// The partial UNIQUE index portal_users_one_primary_uniq allows only one
// is_primary=1 row, so the already-exists branch clears any other
// primary FIRST. Both UPDATEs are idempotent, and the caller
// (main) only invokes this function when SKYGATE_ADMIN_PASS is set.
func bootstrapAdmin(d *sql.DB, username, password string) error {
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM portal_users WHERE username=$1", username).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		// Clear a stale primary on another row before claiming the
		// marker for the configured bootstrap admin.
		if _, err := d.Exec(`UPDATE portal_users SET is_primary = 0 WHERE is_primary = 1 AND username <> $1`, username); err != nil {
			return err
		}
		if _, err := d.Exec(`UPDATE portal_users SET is_admin = 1, is_primary = 1 WHERE username = $1`, username); err != nil {
			return err
		}
		log.Printf("   bootstrap: user %q already exists, re-asserted is_admin=1 is_primary=1", username)
		return nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = d.Exec(`INSERT INTO portal_users(username, password_hash, is_admin, is_primary) VALUES($1,$2,$3,$4)`,
		username, hash, 1, 1)
	if err != nil {
		return err
	}
	log.Printf("✅ bootstrap admin created: %q (primary, immutable)", username)
	return nil
}

// runFirstRunAutoSync — B-mod-first-run-adoption T7.
//
// When SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true AND node_owner_map
// is empty AND at least one portal_user exists, runs
// SyncNodesFromHeadscale + auto-detect exit-servers (T6) once on
// startup. Closes the operator's "deploy skygate as a sidecar to
// existing headscale" gap — the operator goes from "empty
// /admin/devices page" to "my existing headscale nodes are
// already imported" with a single env var.
//
// Returns an error (logged + skipped, doesn't block startup) so
// unit tests can exercise the happy path without forking a
// subprocess.
func runFirstRunAutoSync(ctx context.Context, d *sql.DB, hs *headscale.Client) error {
	// Guard 1: skip if node_owner_map already has rows. The
	// operator has already done the sync manually; the auto-sync
	// would be a no-op anyway and could surprise them.
	var nomCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_owner_map`).Scan(&nomCount); err != nil {
		return fmt.Errorf("count node_owner_map: %w", err)
	}
	if nomCount > 0 {
		// Silent no-op — node_owner_map is populated, the
		// operator has already done the sync. The pre-existing
		// "Sync from headscale" button is the canonical
		// refresh path from here on.
		return nil
	}
	// Guard 2: skip if no portal_users exist. A fresh deploy
	// with SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true but no
	// admin yet should NOT auto-sync (the headscale users
	// would have no portal-side mapping).
	var portalUserCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM portal_users`).Scan(&portalUserCount); err != nil {
		return fmt.Errorf("count portal_users: %w", err)
	}
	if portalUserCount == 0 {
		return nil
	}

	// Happy path: sync all headscale nodes into node_owner_map
	// + auto-detect exit-servers.
	nodes, err := hs.ListAllNodes()
	if err != nil {
		return fmt.Errorf("headscale ListAllNodes: %w", err)
	}
	log.Printf("first-run: auto-syncing %d nodes from headscale (empty node_owner_map, %d portal users)", len(nodes), portalUserCount)

	// Build []db.SyncNodeInfo from the headscale view. The
	// helper at headscale.Client.ListAllNodes already
	// populated NodeView.IsExitNode (from hasExitNodeTag),
	// so T6's auto-detect kicks in for free during sync.
	// (Before T6 existed, the operator had to manually
	// create each exit-server after this auto-sync.)
	syncInfos := make([]db.SyncNodeInfo, 0, len(nodes))
	for _, n := range nodes {
		// B279 (v1.5.46): the node's own tag, never "the first tag
		// that starts with tag:". On a relay whose only tag is the
		// class tag `tag:exit-node` the old loop seeded
		// node_owner_map with a role, not an identity — the starting
		// point of the live `aro` phantom-relay incident.
		tag := db.PickPerNodeTag(n.Tags)
		hsUID := int64(0)
		if n.UserID != "" {
			if v, perr := strconv.ParseInt(n.UserID, 10, 64); perr == nil {
				hsUID = v
			}
		}
		host := n.Hostname
		if host == "" {
			host = n.ID
		}
		syncInfos = append(syncInfos, db.SyncNodeInfo{
			ID:         n.ID,
			Hostname:   host,
			Tag:        tag,
			Username:   n.UserName,
			HSUserID:   hsUID,
			TaggedBy:   0, // 0 = system sync
			IsExitNode: n.IsExitNode,
		})
	}

	ins, upd, err := db.SyncNodesFromHeadscale(d, syncInfos)
	if err != nil {
		return fmt.Errorf("SyncNodesFromHeadscale: %w", err)
	}

	// Audit row (system actor = 0, since no logged-in user
	// triggered this — it ran on startup).
	detail := fmt.Sprintf(`{"nodes_total":%d,"inserted":%d,"updated":%d}`, len(nodes), ins, upd)
	if err := db.AppendAuditLogWithTarget(d, 0, "system", "first_run_auto_sync", detail,
		"system", "first_run_auto_sync"); err != nil {
		log.Printf("first-run: audit row failed: %v (sync succeeded; audit row missing)", err)
	}

	log.Printf("first-run: auto-sync complete (inserted=%d updated=%d); exit-servers auto-detected for IsExitNode nodes", ins, upd)
	return nil
}

func backfillNodeOwners(d *sql.DB, hs *headscale.Client, adminName string) error {
	nodes, err := hs.ListAllNodes()
	if err != nil {
		return err
	}
	var adminID sql.NullInt64
	var adminHSID sql.NullInt64
	if err := d.QueryRow(`SELECT id, headscale_user_id FROM portal_users WHERE username=$1 AND is_admin=1`, adminName).
		Scan(&adminID, &adminHSID); err != nil {
		return err
	}
	if !adminID.Valid || !adminHSID.Valid {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, n := range nodes {
		isPublic := false
		for _, t := range n.Tags {
			if t == "tag:public" {
				isPublic = true
				break
			}
		}
		if !isPublic {
			continue
		}
		if n.UserName != "tagged-devices" {
			continue
		}
		// 2026-07-12: Этап 10 part 4 — moved to
		// db.InsertIgnoreNodeOwner.
		if err := db.InsertIgnoreNodeOwner(tx, n.ID, adminHSID.Int64, adminName, "tag:public", adminID.Int64); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ensureHeadscaleUser(d *sql.DB, hs *headscale.Client, username string) error {
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM portal_users WHERE username=$1 AND headscale_user_id IS NOT NULL", username).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	existing, _ := hs.ListUsers()
	for _, u := range existing {
		if u.Name == username {
			_, err := d.Exec("UPDATE portal_users SET headscale_user_id=$1 WHERE username=$2", u.ID, username)
			return err
		}
	}
	created, err := hs.CreateUser(username)
	if err != nil {
		return err
	}
	_, err = d.Exec("UPDATE portal_users SET headscale_user_id=$1 WHERE username=$2", created.ID, username)
	return err
}

// ensureInfraUser — v0.33.1.41 — Issue 4 technical user.
//
// Provisions the 'infra' headscale user and links it to the
// portal_users row that V054 created at id=99. Idempotent
// (safe to run on every start).
//
// Why the 'infra' user is special:
//   - The portal_users row is created by V054 (id=99, never
//     logs in, password hash is a random bcrypt of a
//     never-used string).
//   - The corresponding headscale user is created here at
//     first start, OR re-linked if it already exists (e.g.
//     created manually by the operator).
//   - skygate-host-* nodes (which auto-discover via the
//     B77 autoupdater) get assigned to 'infra' (not
//     'skyadmin') so the bot in skygate-host-1 (which needs
//     internet to reach api.telegram.org) is governed by
//     a single per-device ACL grant owned by the infra
//     user, isolated from operator-portal-user policy.
//
// Failure mode: if headscale is unreachable at startup, the
// 'infra' portal_users row exists (V054 already ran) but
// headscale_user_id is NULL. The ACL generator skips the
// per-infra grant (ACL builder filters out users with
// headscale_user_id IS NULL). Result: skygate-host-* nodes
// fall back to the catch-all `* → tag:private` and `* →
// tag:exit-node` grants, which is the v0.33.1.40 behaviour
// and is functional. The next restart (when headscale is
// reachable) wires the link and the per-infra grants
// activate.
func ensureInfraUser(d *sql.DB, hs *headscale.Client) error {
	// 1. If the row is already linked, nothing to do.
	var n int
	if err := d.QueryRow(
		`SELECT count(*) FROM portal_users WHERE username = 'infra' AND headscale_user_id IS NOT NULL`,
	).Scan(&n); err != nil {
		return fmt.Errorf("infra: check link: %w", err)
	}
	if n > 0 {
		return nil
	}
	// 2. Look up headscale for an existing 'infra' user.
	//    (Operator may have pre-created it via the headscale
	//    CLI; in that case we just link, we don't try to
	//    re-create.)
	existing, err := hs.ListUsers()
	if err != nil {
		return fmt.Errorf("infra: list headscale users: %w", err)
	}
	for _, u := range existing {
		if u.Name == "infra" {
			_, err := d.Exec(
				`UPDATE portal_users SET headscale_user_id = $1 WHERE username = 'infra'`,
				u.ID,
			)
			if err != nil {
				return fmt.Errorf("infra: link existing headscale user: %w", err)
			}
			log.Printf("✅ infra user linked to existing headscale user id=%s", u.ID)
			return nil
		}
	}
	// 3. Create the headscale user and link.
	created, err := hs.CreateUser("infra")
	if err != nil {
		return fmt.Errorf("infra: create headscale user: %w", err)
	}
	// headscale.CreateUser's primary return path sometimes
	// returns an empty ID (the headscale POST response shape
	// changed between versions — 0.29.x doesn't always
	// populate the `id` field on success). The fallback
	// inside CreateUser should find the user by name in
	// ListUsers, but if THAT also fails (e.g. transient
	// headscale hiccup between the POST and the LIST), the
	// returned `created.ID` is "". Try one more ListUsers
	// pass here to make the function total: any function
	// path that says "created the user" must end with a
	// non-empty link.
	if created.ID == "" {
		// B247: use ListUsersFresh so the just-created user is visible
		// even if the cache from the earlier `existing, err := hs.ListUsers()`
		// call at line 3536 is still warm. Pre-B247 the stale cache made
		// this loop's `for ... u.Name == "infra"` never match, and the
		// function returned the cryptic "response shape may have changed"
		// error — but the user was actually created successfully, just
		// not visible in the cached list.
		users, lerr := hs.ListUsersFresh()
		if lerr != nil {
			return fmt.Errorf("infra: create returned empty ID and re-list failed: %w", lerr)
		}
		found := false
		for _, u := range users {
			if u.Name == "infra" {
				created = &u
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("infra: create returned empty ID and 'infra' not in headscale user list (response shape may have changed)")
		}
	}
	_, err = d.Exec(
		`UPDATE portal_users SET headscale_user_id = $1 WHERE username = 'infra'`,
		created.ID,
	)
	if err != nil {
		return fmt.Errorf("infra: link new headscale user: %w", err)
	}
	log.Printf("✅ infra headscale user created and linked (id=%s)", created.ID)
	return nil
}

// bootstrapTelegramFromEnv copies the Telegram bot token and chat id
// from .env into the global_settings table the first time the app
// starts. After that, /admin/telegram is the canonical source — the
// admin page can rotate / disable the bot without touching .env.
func bootstrapTelegramFromEnv(d *sql.DB) error {
	_, _, ok, err := db.LoadTelegramToken(d)
	if err != nil {
		return err
	}
	if ok {
		return nil // already configured via UI
	}
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	chat := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))
	if token == "" && chat == "" {
		return nil
	}
	return db.SaveTelegramToken(d, token, chat)
}
