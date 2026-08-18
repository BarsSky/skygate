// storage.go — load/save the HA chain from `global_settings.ha_chain`.
//
// We piggyback on the existing `global_settings` (key/value) table
// so the migration is free — the table is already in every
// skygate deploy since v0.32.20. The chain is serialised as a
// single JSON blob; admin-driven edits (Phase 5 /admin/ha form)
// rewrite the same row.

package ha

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"skygate/internal/db"
)

// GlobalSettingsKey is the `global_settings.key` row that holds
// the HA chain JSON. Exposed as a constant so /admin/ha, the
// deploy subcommand, and the certsync tests all use the same
// string (no typos).
const GlobalSettingsKey = "ha_chain"

// LoadChain returns the persisted HA chain, or the zero value
// (empty chain, no auto-failover) if the row is absent. An
// empty value is NOT an error — first-run state.
//
// Returns the parsed chain, the raw bytes (for hashing /
// change-detection), and any error. The raw bytes are useful
// for the /admin/ha "no changes since last load" badge: if
// the operator re-saves the same form, the raw bytes match
// and we can short-circuit the audit log.
//
// Errors only on:
//   - DB failure
//   - JSON parse failure
//   - chain.Validate() failure (e.g. operator hand-edited a
//     bad row via psql)
func LoadChain(d *sql.DB) (*HaChain, []byte, error) {
	raw, err := db.GetGlobalSetting(d, GlobalSettingsKey, "")
	if err != nil {
		return nil, nil, fmt.Errorf("ha: load %q: %w", GlobalSettingsKey, err)
	}
	if raw == "" {
		return &HaChain{}, nil, nil
	}
	c, err := UnmarshalChain([]byte(raw))
	if err != nil {
		return nil, []byte(raw), fmt.Errorf("ha: parse %q: %w", GlobalSettingsKey, err)
	}
	return c, []byte(raw), nil
}

// SaveChain persists the chain. The chain is Validate()d
// before write; a bad chain returns an error without
// touching the DB. The transaction wraps the SELECT-for-
// change-detection + UPDATE so two concurrent /admin/ha
// editors can't clobber each other's changes.
//
//   - Reads the current row inside the transaction
//   - Compares against the proposed chain (by JSON bytes)
//   - Writes the new value if it changed
//   - Returns (changed=true, prev=old) on a real change;
//     (changed=false, prev=old) when the new value matches
//     the existing row (idempotent re-save)
//
// The prev bytes are returned for the audit log so the
// operator can see "you changed X → Y" without us having
// to deep-compare two HaChain structs.
func SaveChain(d *sql.DB, c *HaChain) (changed bool, prev []byte, err error) {
	if err := c.Validate(); err != nil {
		return false, nil, err
	}
	newBytes, err := json.Marshal(c)
	if err != nil {
		return false, nil, fmt.Errorf("ha: marshal: %w", err)
	}
	tx, err := d.Begin()
	if err != nil {
		return false, nil, fmt.Errorf("ha: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	oldRaw, err := db.GetGlobalSettingTx(tx, GlobalSettingsKey, "")
	if err != nil {
		return false, nil, fmt.Errorf("ha: read current: %w", err)
	}
	if oldRaw == string(newBytes) {
		// Idempotent: nothing to do, but we still commit
		// the empty tx so the BEGIN/ROLLBACK pair is
		// balanced.
		if err := tx.Commit(); err != nil {
			return false, nil, fmt.Errorf("ha: commit no-op: %w", err)
		}
		committed = true
		return false, []byte(oldRaw), nil
	}
	if err := db.SetGlobalSettingTx(tx, GlobalSettingsKey, string(newBytes)); err != nil {
		return false, nil, fmt.Errorf("ha: write new: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, nil, fmt.Errorf("ha: commit: %w", err)
	}
	committed = true
	return true, []byte(oldRaw), nil
}
