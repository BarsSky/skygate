package db

// migrations_v0_70_b238.go — v0.70 (B238) — portal_users
// AFTER UPDATE audit trigger.
//
// Operator 2026-09-11: someone rotated the
// `skyadmin` portal_users password_hash OUTSIDE skygate
// (cost-10 bcrypt via an external CLI, not via the
// /password_change form which uses skygate's
// auth.HashPassword at cost-12). The skygate UI never
// logged a `password_change` audit row for the rotation,
// but the password no longer matched the
// SKYGATE_ADMIN_PASS env var, so login started returning
// 401 for the operator.
//
// B238 closes the visibility gap at the DB level: an
// AFTER UPDATE trigger on portal_users writes one
// `password_change_db` audit row whenever
// `OLD.password_hash IS DISTINCT FROM NEW.password_hash`
// (regardless of WHO did the UPDATE — skygate, psql, a
// one-off recovery script, an ORM, etc.). The detail
// captures both hash prefixes (7 chars of each — the
// bcrypt version + cost + first salt bytes) so the
// operator can see "the hash was $2a$12 → $2b$10" or
// "the same prefix repeated" (i.e. someone reset the
// password to the same value via UPSERT).
//
// Coexistence with the UI `password_change` audit row:
// POST /password_change writes 'password_change' (via
// feature/auth/service.go:376). B238's trigger ALSO
// fires and writes 'password_change_db'. Two rows is
// intentional — the UI row attributes the change to the
// actor (user_id), the DB row attributes it to the
// transaction (pg_current_xact_id). When they diverge
// (a row appears with action='password_change_db' but
// NO matching 'password_change' / 'user_password_reset'
// UI row in the same timeframe), the operator knows
// the password was changed outside the UI.
//
// 2026-09-11: v0.70 (B238).

import (
	"database/sql"
)

// migrateV070PG creates the portal_users AFTER UPDATE
// audit trigger. Idempotent: re-running the migration
// is a no-op (CREATE OR REPLACE FUNCTION + DROP TRIGGER
// IF EXISTS + CREATE TRIGGER).
//
// The trigger body uses plpgsql exception swallowing
// for the substr() calls so a NULL hash on either side
// (which shouldn't happen per the schema's NOT NULL but
// could in pre-v0.25 / corrupted rows) yields
// detail='old_prefix=<null> new_prefix=<null>' instead
// of aborting the transaction.
func migrateV070PG(d *sql.DB) error {
	// 1. Create-or-replace the trigger function. Runs
	//    on each AFTER UPDATE of portal_users. Skips
	//    rows where password_hash is unchanged (e.g.
	//    updating only is_admin / username / theme)
	//    so the audit_log stays focused on actual
	//    credential changes.
	if _, err := d.Exec(`
		CREATE OR REPLACE FUNCTION portal_users_audit_trigger()
		RETURNS TRIGGER AS $$
		BEGIN
			-- Only log when password_hash actually changed.
			-- OLD.password_hash / NEW.password_hash are both
			-- NOT NULL per schema (v0.25 created the table
			-- with NOT NULL DEFAULT '').
			IF OLD.password_hash IS DISTINCT FROM NEW.password_hash THEN
				INSERT INTO audit_log (
					user_id, username, action, detail,
					target_type, target_id
				) VALUES (
					OLD.id,
					OLD.username,
					'password_change_db',
					'old_prefix=' || COALESCE(substr(OLD.password_hash, 1, 7), '<null>')
					|| ' new_prefix=' || COALESCE(substr(NEW.password_hash, 1, 7), '<null>')
					|| ' txid=' || pg_current_xact_id()::text,
					'portal_user',
					OLD.username
				);
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
	`); err != nil {
		return err
	}
	// 2. Drop + recreate the trigger. DROP IF EXISTS
	//    makes the migration idempotent (re-running
	//    re-attaches the trigger with the latest
	//    function body).
	if _, err := d.Exec(`
		DROP TRIGGER IF EXISTS portal_users_audit ON portal_users;
	`); err != nil {
		return err
	}
	if _, err := d.Exec(`
		CREATE TRIGGER portal_users_audit
			AFTER UPDATE ON portal_users
			FOR EACH ROW
			EXECUTE FUNCTION portal_users_audit_trigger();
	`); err != nil {
		return err
	}
	return nil
}
