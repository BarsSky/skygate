// migrations_v0_84_dns_providers.go — v0.84 (B473): DNS provider credentials become ROWS, not a
// single flat set of global_settings keys.
//
// WHY (the seam the operator asked for: «как адаптировать под разных провайдеров?»)
//
// The shipped credential store kept ONE provider's shape — reg.ru's login + alternative password +
// zone — under five fixed keys (`ha.dns.provider`, `ha.dns.login`, `ha.dns.zone`,
// `ha.dns.password_enc`, `ha.dns.cert_pem_enc`). That is enough for exactly one provider and exactly
// one zone: a Cloudflare API token, a Route53 access key/secret pair or an RFC 2136 TSIG key have
// nowhere to live, and a second zone cannot be configured at all.
//
// This table is the storage half of the seam. One row = one (provider, zone) pair, with the
// provider-specific fields as an encrypted JSON object, so adding a provider is an adapter plus a
// field schema — not a migration. The legacy keys are NOT dropped: they are imported ONCE into this
// table (see internal/ha/dnsexternal/provider_store_b473.go) and stay behind as history, so an
// installation that upgrades keeps working with no operator action.
//
// Both chains, in the shape the rest of this package uses: PostgreSQL gets BIGSERIAL + BIGINT epoch
// columns, SQLite gets INTEGER PRIMARY KEY AUTOINCREMENT and goes through execSQLiteDDL.
package db

import (
	"database/sql"
	"fmt"
)

const dnsProvidersPG = `
CREATE TABLE IF NOT EXISTS dns_provider_creds (
    id         BIGSERIAL PRIMARY KEY,
    provider   TEXT   NOT NULL,
    zone       TEXT   NOT NULL,
    fields_enc TEXT   NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at BIGINT NOT NULL DEFAULT 0,
    updated_at BIGINT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS dns_provider_creds_provider_zone_uniq
    ON dns_provider_creds (provider, zone);`

const dnsProvidersSQLiteStmt1 = `CREATE TABLE IF NOT EXISTS dns_provider_creds (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    provider   TEXT    NOT NULL,
    zone       TEXT    NOT NULL,
    fields_enc TEXT    NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0
);`

const dnsProvidersSQLiteStmt2 = `CREATE UNIQUE INDEX IF NOT EXISTS dns_provider_creds_provider_zone_uniq
    ON dns_provider_creds (provider, zone);`

// migrateV084PG creates dns_provider_creds on PostgreSQL.
//
// The UNIQUE index is the integrity rule the store relies on: one credential set per (provider, zone),
// so two rows can never disagree about the same zone — the operator edits the row instead of adding a
// second one that would silently win depending on ORDER BY.
func migrateV084PG(d *sql.DB) error {
	if _, err := d.Exec(dnsProvidersPG); err != nil {
		return fmt.Errorf("v0.84 dns_provider_creds: %w", err)
	}
	return nil
}

// migrateV084SQLite creates dns_provider_creds on SQLite through the single DDL chokepoint.
func migrateV084SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, []string{dnsProvidersSQLiteStmt1, dnsProvidersSQLiteStmt2}); err != nil {
		return fmt.Errorf("v0.84 dns_provider_creds (sqlite): %w", err)
	}
	return nil
}
