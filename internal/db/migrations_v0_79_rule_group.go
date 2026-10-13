// migrations_v0_79_rule_group.go — v0.79 (B380): the operator's OWN name for a group
// of rules.
//
// WHY (2026-10-13, operator request): «возможность добавить в exit rules для
// пользователей группировать те правила что они задают так им будет проще
// ориентироваться не только по домену но и по пользовательскому названию».
//
// A rule list read by domain is fine until the destinations stop being one thing: a user
// running twenty game rules, four work hosts and a pile of streaming ranges sees an
// alphabetical wall in which their own mental model ("Игры", "Работа", "Реклама") is
// nowhere. The label is deliberately NOT derived from anything: the CDN grouping
// (B237.22) and the by-target view (B376) group by what the destination IS, and this
// column records what the OPERATOR says it is for. Both are useful and they must not be
// conflated, so the label never influences the ACL, the prefix assignment or the
// auto-updater's decisions — it is presentation the user owns.
//
//	group_label — '' (the default) when the rule belongs to no group the user named.
//	              Free text, normalised on write by NormaliseGroupLabel in the feature
//	              package (trimmed, inner whitespace collapsed, capped at 40 runes).
//
// The column is additive with a default, so an existing installation keeps its
// behaviour: every pre-V079 row is ungrouped, which is exactly what it was.
//
// The index is (user_id, group_label) because every query that uses the label is
// "this user's rules, grouped/named" — the user is always the first key, so the index
// is usable for both the group list and a per-user lookup.
package db

import (
	"database/sql"
	"fmt"
)

const ruleGroupColumnSQL = `ALTER TABLE device_rules ADD COLUMN IF NOT EXISTS group_label TEXT NOT NULL DEFAULT ''`

const ruleGroupIndexSQL = `CREATE INDEX IF NOT EXISTS idx_device_rules_user_group ON device_rules(user_id, group_label)`

// migrateV079PG adds device_rules.group_label on PostgreSQL.
func migrateV079PG(d *sql.DB) error {
	if _, err := d.Exec(ruleGroupColumnSQL); err != nil {
		return fmt.Errorf("v0.79 device_rules.group_label: %w", err)
	}
	if _, err := d.Exec(ruleGroupIndexSQL); err != nil {
		return fmt.Errorf("v0.79 device_rules.group_label idx: %w", err)
	}
	return nil
}

// migrateV079SQLite adds device_rules.group_label on SQLite through the single DDL
// chokepoint (SQLite has no ADD COLUMN IF NOT EXISTS; execSQLiteDDL routes ADD COLUMN
// through addColumnIfMissingSQLite, which is also what makes a re-run a no-op).
func migrateV079SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, []string{ruleGroupColumnSQL, ruleGroupIndexSQL}); err != nil {
		return fmt.Errorf("v0.79 device_rules.group_label (sqlite): %w", err)
	}
	return nil
}
