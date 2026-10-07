// migrations_v0_78_prefix_reservation.go — v0.78 (B360): a failover is a
// RESERVATION, not an amnesia.
//
// WHY (measured on the reference deployment, 2026-10-06/07)
// ---------------------------------------------------------
// During the karolina outage the operator set the global override from the panel
// (`global_settings.prefix_owner_force_relay = emilia`, audit
// `prefix_owner_force`, 2026-10-06 19:49:23Z). `prefix_owner` then held 139 rows,
// all `emilia`, all `source='global'` — while `LoadClaims` saw 194 ip/subnet
// claims naming karolina. karolina came back, all three relays reported `online`
// with approved routes, and NOTHING returned: the override is a latch with no
// expiry and no surface, and the assignment engine had no memory that the move had
// been a failover rather than a decision.
//
// Clearing the override by hand made the very next pass return all 139 prefixes
// (source='explicit', changed=139) — so the engine CAN return; it simply had no
// obligation to re-ask the question and no record of who the prefix belonged to.
//
// V078 gives `prefix_owner` that memory. Three additive columns:
//
//	failover_from  — the relay this prefix was TAKEN FROM because it was not in
//	                 the healthy set ('' = no active reservation). A prefix whose
//	                 previous owner was healthy and which moved anyway is a
//	                 GENUINE decision change and clears this column, because a
//	                 reservation must never outlive the reason that justified it
//	                 (docs/LESSONS.md L-60).
//	failover_at    — unix seconds of the event that last touched the reservation:
//	                 when an ACTIVE reservation was recorded (failover_from != '')
//	                 or when the reservation was RETURNED (failover_from == '').
//	                 The second reading is what makes the anti-flap quarantine
//	                 checkable without a fourth column: a new failover within the
//	                 quarantine window of this stamp is a QUICK FLAP.
//	failover_flaps — consecutive quick flaps for this prefix; the return window is
//	                 hysteresis * 2^flaps, capped. It survives a restart (a
//	                 process-local counter would let a restart reset the penalty),
//	                 and a failover that happens at least one quarantine window
//	                 after the last return resets it to 0.
//
// All three are additive with defaults, so an existing installation keeps working
// and every pre-V078 row simply means "no reservation" — no backfill can invent a
// reservation, and guessing one would hand a prefix to a relay for a reason the
// operator cannot see.
//
// Both chains add the same columns; the SQLite side goes through execSQLiteDDL
// (SQLite has no `ADD COLUMN IF NOT EXISTS`).
package db

import (
	"database/sql"
	"fmt"
)

// prefixReservationColumnSQLs adds the B360 reservation columns (idempotent on
// both dialects; SQLite goes through execSQLiteDDL → addColumnIfMissingSQLite).
//
// INTEGER rather than BIGINT for the two stamps keeps the declaration identical to
// prefix_owner.updated_at, which the parity test already compares as one type
// class on both backends.
var prefixReservationColumnSQLs = []string{
	`ALTER TABLE prefix_owner ADD COLUMN IF NOT EXISTS failover_from TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE prefix_owner ADD COLUMN IF NOT EXISTS failover_at INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE prefix_owner ADD COLUMN IF NOT EXISTS failover_flaps INTEGER NOT NULL DEFAULT 0`,
}

// migrateV078PG adds the reservation columns on PostgreSQL.
func migrateV078PG(d *sql.DB) error {
	for _, q := range prefixReservationColumnSQLs {
		if _, err := d.Exec(q); err != nil {
			return fmt.Errorf("v0.78 prefix_owner reservation (%s): %w", q, err)
		}
	}
	return nil
}

// migrateV078SQLite adds the same columns through the single DDL chokepoint.
func migrateV078SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, prefixReservationColumnSQLs); err != nil {
		return fmt.Errorf("v0.78 prefix_owner reservation (sqlite): %w", err)
	}
	return nil
}
