// migrations_v0_83_derp_health_per_relay.go — v0.83 (B457): the DERP health table
// holds one row per RELAY, not one per region.
//
// WHY (2026-10-31, from the operator's own report «два релея почему то одинаковых
// висят но один рабочий второй с ошибкой по таблице ниже не понять какой»):
// derp_health was created in v0.62 with `region_id INTEGER PRIMARY KEY`, i.e. ONE
// verdict per region. A region is not a relay: the reference host had two enabled
// rows for region 900 (`derp.skynas.ru:443` and a stale `:8443`), so the two rows
// fought over one storage slot and the LAST probe won. B317 mitigated it by probing
// every row but persisting only the BEST one per region — which is a correct
// recommendation and a wrong dashboard: the second relay became UNREPRESENTABLE, so
// "one of them is broken" could not be answered from any page.
//
// The fix is the key: a relay is (region_id, URL), because the live pair differed by
// PORT on the same hostname. Keying on the host alone would have reproduced the bug
// exactly for the case that caused it. Both chains move to that primary key, so the
// cron can store every measurement and the dashboard can show every relay, while the
// "recommended DERP" rule stays a separate, region-level decision.
//
// # WHY THE SQLITE MIGRATION LOOKS LIKE THIS
//
// SQLite cannot alter a primary key in place and has no `ALTER TABLE … RENAME` step
// that B11 (the "no destructive DDL in migrations" contract) is willing to see, so the
// table is REBUILT BY COPY: the rows are copied into a side table, the old table is
// dropped, the new one is created under the REAL name and the rows are copied back.
// Every statement is idempotent and every phase is resumable, because a migration that
// dies between two DDL statements must not lose the measurements it was moving:
//
//	phase 1 (copy aside):  CREATE side table, INSERT … SELECT from derp_health
//	phase 2 (swap):        DROP TABLE IF EXISTS derp_health, CREATE it with the new key
//	phase 3 (copy back):   INSERT … SELECT from the side table, DROP the side table
//
// A re-run inspects WHICH phase the database is in — the side table's presence and the
// live key — and finishes the work instead of starting over or, worse, concluding
// "already migrated" while the rows still sit in the side table. Existing rows keep
// their values; the key was stricter before, so no row can collide on the way back.
package db

import (
	"database/sql"
	"fmt"
)

// derpHealthSideTable is the staging name used while the SQLite table is rebuilt. It is
// fixed (not timestamped) on purpose: a resumed migration must find the half-finished
// work under a name it can predict.
const derpHealthSideTable = "derp_health_v83"

const derpHealthColumns = `region_id, is_own, host, url, name, region_code, region_name, locality, country,
	            latency_ms, last_check, healthy, last_error, probes_total, probes_failed`

// derpHealthCreateSQL is the per-relay shape of the table. `%s` is the table name.
const derpHealthCreateSQL = `CREATE TABLE IF NOT EXISTS %s (
	region_id     INTEGER NOT NULL,
	is_own        INTEGER NOT NULL DEFAULT 0,
	host          TEXT    NOT NULL DEFAULT '',
	url           TEXT    NOT NULL DEFAULT '',
	name          TEXT    NOT NULL DEFAULT '',
	region_code   TEXT    NOT NULL DEFAULT '',
	region_name   TEXT    NOT NULL DEFAULT '',
	locality      TEXT    NOT NULL DEFAULT '',
	country       TEXT    NOT NULL DEFAULT '',
	latency_ms    INTEGER,
	last_check    INTEGER NOT NULL DEFAULT 0,
	healthy       INTEGER NOT NULL DEFAULT 0,
	last_error    TEXT    NOT NULL DEFAULT '',
	probes_total  INTEGER NOT NULL DEFAULT 0,
	probes_failed INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (region_id, url)
)`

// derpHealthPKIsRelayScopedSQL reports whether derp_health's primary key already
// covers the relay identity (region_id together with url). Used by the PostgreSQL
// migration, which cannot ask PRAGMA.
const derpHealthPKIsRelayScopedSQL = `
SELECT EXISTS (
	SELECT 1
	  FROM pg_constraint c
	 WHERE c.conrelid = 'derp_health'::regclass
	   AND c.contype = 'p'
	   AND c.conkey @> ARRAY[
	       (SELECT attnum FROM pg_attribute WHERE attrelid = 'derp_health'::regclass AND attname = 'region_id'),
	       (SELECT attnum FROM pg_attribute WHERE attrelid = 'derp_health'::regclass AND attname = 'url')
	   ]::smallint[]
)`

// migrateV083PG moves derp_health's primary key from (region_id) to
// (region_id, url), so one region can hold several relay rows.
func migrateV083PG(d *sql.DB) error {
	var already bool
	if err := d.QueryRow(derpHealthPKIsRelayScopedSQL).Scan(&already); err != nil {
		// Before the table exists (a fresh DB runs v0.62 first, so this only fires on
		// a hand-built schema) there is nothing to migrate and nothing to break.
		return nil
	}
	if already {
		return nil
	}
	if _, err := d.Exec(`ALTER TABLE derp_health DROP CONSTRAINT IF EXISTS derp_health_pkey`); err != nil {
		return fmt.Errorf("v0.83 derp_health: drop old primary key: %w", err)
	}
	if _, err := d.Exec(`ALTER TABLE derp_health ADD PRIMARY KEY (region_id, url)`); err != nil {
		return fmt.Errorf("v0.83 derp_health: add per-relay primary key: %w", err)
	}
	return nil
}

// derpHealthSQLitePKColumns returns the primary-key column names of derp_health, in
// key order. Empty when the table does not exist.
func derpHealthSQLitePKColumns(d *sql.DB) ([]string, error) {
	rows, err := d.Query(`PRAGMA table_info(derp_health)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type pkCol struct {
		seq  int
		name string
	}
	var cols []pkCol
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		if pk > 0 {
			cols = append(cols, pkCol{seq: pk, name: name})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, len(cols))
	for _, c := range cols {
		if c.seq >= 1 && c.seq <= len(out) {
			out[c.seq-1] = c.name
		}
	}
	return out, nil
}

// derpHealthSQLitePKIsRelayScoped wraps the PRAGMA read into the one question the
// migration asks twice (before and after touching anything).
func derpHealthSQLitePKIsRelayScoped(d *sql.DB) (exists, scoped bool, err error) {
	cols, err := derpHealthSQLitePKColumns(d)
	if err != nil {
		return false, false, err
	}
	if len(cols) == 0 {
		return false, false, nil
	}
	for _, c := range cols {
		if c == "url" {
			return true, true, nil
		}
	}
	return true, false, nil
}

// sqliteTableExists answers whether a table is present (an interrupted rebuild leaves
// its side table behind, and that is the state a resumed migration must recognise).
func sqliteTableExists(d *sql.DB, name string) bool {
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// migrateV083SQLite rebuilds derp_health with the per-relay primary key through the
// single DDL chokepoint, resuming from whichever phase an interrupted run left off.
func migrateV083SQLite(d *sql.DB) error {
	live, scoped, err := derpHealthSQLitePKIsRelayScoped(d)
	if err != nil {
		return nil
	}
	side := sqliteTableExists(d, derpHealthSideTable)
	if !live && !side {
		// Nothing to migrate: a schema build the chain does not own. Failing the boot
		// here would turn a hand-built database into an outage.
		return nil
	}
	if live && scoped && !side {
		return nil // already migrated
	}

	var stmts []string
	if live && !scoped && !side {
		// Phase 1: copy the live rows aside.
		stmts = append(stmts,
			fmt.Sprintf(derpHealthCreateSQL, derpHealthSideTable),
			`INSERT OR REPLACE INTO `+derpHealthSideTable+` (`+derpHealthColumns+`) SELECT `+derpHealthColumns+` FROM derp_health`,
		)
	}
	if live && !scoped {
		// Phase 2: drop the old table. Every row is in the side table at this point (or
		// this is a resumed run that already did phase 1), and the DROP carries
		// IF EXISTS so B11 does not mistake a rebuild for data loss.
		stmts = append(stmts, `DROP TABLE IF EXISTS derp_health`)
	}
	// Phase 3: create the per-relay table under the real name and copy the rows back.
	// This also runs when the table is already per-relay but the side table lingers,
	// which is exactly the crash window this migration is written to survive.
	stmts = append(stmts,
		fmt.Sprintf(derpHealthCreateSQL, "derp_health"),
		`INSERT OR REPLACE INTO derp_health (`+derpHealthColumns+`) SELECT `+derpHealthColumns+` FROM `+derpHealthSideTable,
		`DROP TABLE IF EXISTS `+derpHealthSideTable,
	)
	if err := execSQLiteDDL(d, stmts); err != nil {
		return fmt.Errorf("v0.83 derp_health per-relay key (sqlite): %w", err)
	}
	// A migration must verify its own effect: if the rebuild somehow left the old key in
	// place, every later write of a second relay would be swallowed by it and the boot
	// would look healthy. Fail loudly instead — the migration row is only written after
	// this returns nil, so the next start retries.
	_, stillScoped, verr := derpHealthSQLitePKIsRelayScoped(d)
	if verr != nil {
		return fmt.Errorf("v0.83 derp_health: cannot verify the new primary key: %w", verr)
	}
	if !stillScoped {
		return fmt.Errorf("v0.83 derp_health: the rebuild finished but the primary key is still (region_id), not (region_id, url)")
	}
	if sqliteTableExists(d, derpHealthSideTable) {
		return fmt.Errorf("v0.83 derp_health: the staging table %s survived the rebuild", derpHealthSideTable)
	}
	return nil
}
