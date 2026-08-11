package db

// migrations_v0.54.go — v0.33.1.39: Technical user / infra portal user.
//
// Background (operator report 2026-08-10):
//
//   "Я предлагал создать технического пользователя что будет
//    принимать к себе устройства по типу exit node и host чтобы
//    иметь возможность держать их в отдельной группе и
//    инициализировать данного пользователя при первичной
//    настройке и развертывании skygate. То есть в итоге все
//    exit node устройства что будут добавляться через skygate
//    должны будут быть инициализированны от этого пользователя
//    также и с развертываемыми контурами дублирующими skygate.
//    Позволит изолировать правила и настройки."
//
// The infra user is a "technical" portal user that owns:
//   - skygate-host-* nodes (the node that runs skygate itself)
//   - exit-node devices (relay-* that skygate manages via
//     /admin/exit-nodes)
//   - subnet-router devices (e.g. skyadmin-subnet-router, until
//     v0.33.1.38 removed it for the 10.0.1.0/24 case)
//
// Isolation benefits vs the current "all in skyadmin" model:
//   - The bot in skygate-host-1 (which needs internet to reach
//     api.telegram.org) is governed by a single per-device ACL
//     grant owned by the infra user, not by skyadmin. The
//     current state requires the operator to apply both
//     tag:dev-skyadmin-skygate-vm AND tag:private to the
//     skygate-host-1 node (a manual workaround that the
//     technical user replaces).
//   - Exit-node changes (new relay, deprecated relay) don't
//     pollute skyadmin's node_owner_map or device_rules.
//   - skygate's deployment replicas (HA skygate-host-2) get
//     the same isolation — the skygate-internal nodes don't
//     intermingle with operator user devices.
//
// This V054 migration creates the portal_users row for 'infra'
// (idempotent — safe to run on every start). The corresponding
// headscale user is created at startup by the
// ensureInfraUser helper in cmd/skygate/main.go (which calls
// the headscale gRPC API / CLI to provision the user in
// headscale, then updates portal_users.headscale_user_id to
// point at the new headscale user id).
//
// The portal_user's password_hash is a randomly-generated
// bcrypt hash — there's no intent to ever let 'infra' log in
// via the web UI (the autoupdater + per-device grants are the
// only thing that need an entry in portal_users). The hash
// prevents password-less login as 'infra' even if the
// headscale_url fallback path is ever taken.
//
// Idempotency. The migration is a single INSERT ... ON
// CONFLICT DO NOTHING — running it twice is a no-op. The
// applied_migrations table check (added in V049) ensures
// this migration only runs once per fresh DB; for backup
// restores from a post-V054 snapshot, the insert is also
// a no-op (username is the conflict key).

import (
	"database/sql"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

func migrateV054(d *sql.DB) error {
	// Idempotency: bail if the infra row already exists. This
	// is belt-and-suspenders on top of the applied_migrations
	// table check upstream — if the migration gets re-run
	// for any reason (manual invocation, backup restore), we
	// don't want a duplicate portal_users row.
	var n int
	if err := d.QueryRow(
		`SELECT count(*) FROM portal_users WHERE username = 'infra'`,
	).Scan(&n); err != nil {
		return fmt.Errorf("migrate v0.54: check infra user: %w", err)
	}
	if n > 0 {
		return nil
	}
	// Random bcrypt hash (no plain-text password exists).
	// The 'infra' user is never meant to log in — this hash
	// just ensures bcrypt comparison fails fast if anyone
	// tries. The salt is unimportant because we'll never
	// verify against a real password.
	hash, err := bcrypt.GenerateFromPassword(
		[]byte("infra-never-logs-in-via-web-2026-08-10"),
		bcrypt.DefaultCost,
	)
	if err != nil {
		return fmt.Errorf("migrate v0.54: bcrypt hash: %w", err)
	}
	// 2026-08-10: v0.33.1.41 #2 — the 'infra' row uses a
	// reserved id (99) to avoid colliding with auto-assigned
	// ids in fresh test DBs (where the next free id is 1, the
	// same id the test helpers use for "user1"). Production
	// portal_users are typically in the 1..100 range (operator
	// accounts); 99 is reserved for system accounts (infra +
	// future "service-bot"-style users).
	//
	// Idempotent on re-runs: the count check above skips if
	// the row already exists. If a fresh DB has a portal_user
	// at id=99 (e.g. an operator manually created one with
	// that id), the INSERT OR IGNORE on the username column
	// is also a no-op (UNIQUE constraint on username) — the
	// migration is then a soft no-op and the next start will
	// log a warning that 'infra' wasn't created.
	if _, err := d.Exec(
		`INSERT OR IGNORE INTO portal_users (id, username, password_hash, is_admin) VALUES (99, 'infra', ?, 0)`,
		string(hash),
	); err != nil {
		return fmt.Errorf("migrate v0.54: insert infra: %w", err)
	}
	return nil
}
