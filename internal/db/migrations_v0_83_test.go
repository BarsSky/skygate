package db

// migrations_v0_83_test.go — B457: the DERP health table must hold one row per RELAY.
//
// The migration is the load-bearing half of the block: without the composite key the
// cron can only ever store one verdict per region, no matter what the prober does. The
// SQLite case is exercised for real (old schema → migrate → composite key + row kept),
// and the "run it twice" case proves the guard, because a rebuild that runs a second
// time on an already-migrated table would drop the live verdicts.

import (
	"database/sql"
	"fmt"
	"testing"
)

// oldDerpHealthSchema is the v0.62 shape (region_id PRIMARY KEY), copied here so the
// test keeps testing the MIGRATION and not whatever CREATE TABLE the migration chain
// happens to contain today.
const oldDerpHealthSchema = `
CREATE TABLE derp_health (
	region_id     INTEGER PRIMARY KEY,
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
	probes_failed INTEGER NOT NULL DEFAULT 0
)`

func openSQLiteForTest(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestB457_SQLiteMigrationMovesTheKeyToTheRelay — old schema, one existing verdict,
// then the migration: the key becomes (region_id, url) and the stored measurement
// survives untouched.
func TestB457_SQLiteMigrationMovesTheKeyToTheRelay(t *testing.T) {
	d := openSQLiteForTest(t)
	if _, err := d.Exec(oldDerpHealthSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO derp_health
		(region_id, is_own, host, url, name, region_code, region_name, locality, country,
		 latency_ms, last_check, healthy, last_error, probes_total, probes_failed)
		VALUES (900, 1, 'derp.example.com', 'https://derp.example.com:443', '', 'mow', 'Moscow', '', '',
		        20, 1700000000, 1, '', 7, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := migrateV083SQLite(d); err != nil {
		t.Fatalf("migrateV083SQLite: %v", err)
	}

	cols, err := derpHealthSQLitePKColumns(d)
	if err != nil {
		t.Fatalf("read pk: %v", err)
	}
	if len(cols) != 2 || cols[0] != "region_id" || cols[1] != "url" {
		t.Fatalf("primary key after the migration = %v, want [region_id url]", cols)
	}

	// The existing verdict survived with every column intact.
	var lat, probes, healthy int
	var host, url string
	if err := d.QueryRow(`SELECT host, url, latency_ms, probes_total, healthy FROM derp_health`).
		Scan(&host, &url, &lat, &probes, &healthy); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if host != "derp.example.com" || url != "https://derp.example.com:443" || lat != 20 || probes != 7 || healthy != 1 {
		t.Fatalf("the migrated row changed: host=%q url=%q lat=%d probes=%d healthy=%d", host, url, lat, probes, healthy)
	}

	// And the key now ALLOWS what the old one forbade: a second relay of the same
	// region on the same hostname, different port.
	if _, err := d.Exec(`INSERT INTO derp_health
		(region_id, is_own, host, url, name, region_code, region_name, locality, country,
		 latency_ms, last_check, healthy, last_error, probes_total, probes_failed)
		VALUES (900, 1, 'derp.example.com', 'https://derp.example.com:8443', '', 'mow', 'Moscow', '', '',
		        NULL, 1700000000, 0, 'connection refused', 1, 1)`); err != nil {
		t.Fatalf("a second relay of the same region must be storable: %v", err)
	}

	// Running the migration AGAIN must be a no-op: a rebuild on a migrated table would
	// silently recreate it from itself and could lose the extra row.
	if err := migrateV083SQLite(d); err != nil {
		t.Fatalf("migrateV083SQLite (second run): %v", err)
	}
	var rows int
	if err := d.QueryRow(`SELECT count(*) FROM derp_health`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Fatalf("%d rows after a second migration run, want 2", rows)
	}
}

// TestB457_SQLiteMigrationIsSafeWithoutTheTable — a hand-built schema (or a very old
// database) has no derp_health at all; the migration must not fail the boot for that.
func TestB457_SQLiteMigrationIsSafeWithoutTheTable(t *testing.T) {
	d := openSQLiteForTest(t)
	if err := migrateV083SQLite(d); err != nil {
		t.Fatalf("migrateV083SQLite without the table: %v", err)
	}
}

// TestB457_SQLiteMigrationResumesAfterTheDrop — the crash window that makes this
// migration a three-phase copy instead of a RENAME: the rows are safe in the staging
// table, `derp_health` is gone, and the next boot must FINISH the job rather than
// conclude "nothing to migrate" (which would leave the dashboard empty and the
// measurements stranded under a name no query reads).
func TestB457_SQLiteMigrationResumesAfterTheDrop(t *testing.T) {
	d := openSQLiteForTest(t)
	if _, err := d.Exec(fmt.Sprintf(derpHealthCreateSQL, derpHealthSideTable)); err != nil {
		t.Fatalf("create staging table: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO ` + derpHealthSideTable + `
		(region_id, is_own, host, url, name, region_code, region_name, locality, country,
		 latency_ms, last_check, healthy, last_error, probes_total, probes_failed)
		VALUES (900, 1, 'derp.example.com', 'https://derp.example.com:443', '', 'mow', 'Moscow', '', '',
		        20, 1700000000, 1, '', 7, 0)`); err != nil {
		t.Fatalf("seed staging: %v", err)
	}

	if err := migrateV083SQLite(d); err != nil {
		t.Fatalf("migrateV083SQLite (resume): %v", err)
	}
	var lat int
	var url string
	if err := d.QueryRow(`SELECT url, latency_ms FROM derp_health`).Scan(&url, &lat); err != nil {
		t.Fatalf("the measurement did not come back into derp_health: %v", err)
	}
	if url != "https://derp.example.com:443" || lat != 20 {
		t.Fatalf("restored row = %q/%d, want the staged one", url, lat)
	}
	if sqliteTableExists(d, derpHealthSideTable) {
		t.Fatal("the staging table survived a completed resume")
	}
}

// TestB457_SQLiteMigrationResumesAfterTheCreate — the second crash window: the
// per-relay table exists but is EMPTY and the staging table still holds the rows. A
// migration that judged "the key is already per-relay, done" would silently throw the
// measurements away.
func TestB457_SQLiteMigrationResumesAfterTheCreate(t *testing.T) {
	d := openSQLiteForTest(t)
	if _, err := d.Exec(fmt.Sprintf(derpHealthCreateSQL, "derp_health")); err != nil {
		t.Fatalf("create new table: %v", err)
	}
	if _, err := d.Exec(fmt.Sprintf(derpHealthCreateSQL, derpHealthSideTable)); err != nil {
		t.Fatalf("create staging table: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO ` + derpHealthSideTable + `
		(region_id, is_own, host, url, name, region_code, region_name, locality, country,
		 latency_ms, last_check, healthy, last_error, probes_total, probes_failed)
		VALUES (4, 0, 'derp4f.tailscale.com', 'https://derp4f.tailscale.com', '4f', 'fra', 'Frankfurt', '', '',
		        125, 1700000000, 1, '', 3, 0)`); err != nil {
		t.Fatalf("seed staging: %v", err)
	}

	if err := migrateV083SQLite(d); err != nil {
		t.Fatalf("migrateV083SQLite (resume after create): %v", err)
	}
	var rows int
	if err := d.QueryRow(`SELECT count(*) FROM derp_health`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("%d rows in derp_health, want 1 copied back from staging", rows)
	}
	if sqliteTableExists(d, derpHealthSideTable) {
		t.Fatal("the staging table survived a completed resume")
	}
}
