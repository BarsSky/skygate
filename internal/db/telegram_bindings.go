// Package db — telegram_bindings helpers.
//
// Этап 11 (2026-07-12): moves the raw SQL out of
// internal/telegram/commands.go into a typed helper so the bot can
// reason about "who is messaging" without re-parsing the SELECT.
//
// The single source of truth for a chat → user mapping is the
// telegram_bindings table (see migrations_v0.29.go). admin_chat_id is
// the bootstrap-admin chat configured in global_settings; we treat it
// as a binding to the bootstrap admin even if no row exists, so the
// single-admin setup keeps working without a manual /bind step.

package db

import (
	"database/sql"
	"errors"
)

// ErrTelegramBindingNotFound is returned by GetTelegramBinding when no
// row matches the chat_id. Callers (mostly the bot dispatcher) use
// errors.Is to short-circuit to a "chat not bound" error reply.
var ErrTelegramBindingNotFound = errors.New("telegram: chat not bound")

// TelegramBinding is the typed view of one row in telegram_bindings.
// is_admin is denormalized from portal_users.is_admin at bind time
// (so a permission check is one indexed lookup, not a join).
type TelegramBinding struct {
	ChatID        int64
	PortalUserID  int64
	IsAdmin       bool
	BoundAt       int64
	BoundByUserID int64
}

// GetTelegramBinding returns the binding for chatID, or
// ErrTelegramBindingNotFound when no row exists. The admin_chat_id
// fallback (treating the configured admin chat as bound to userID)
// is the caller's responsibility — the helper just reads the table.
func GetTelegramBinding(d *sql.DB, chatID int64) (*TelegramBinding, error) {
	var b TelegramBinding
	var isAdmin int
	err := d.QueryRow(qSelectTelegramBindingByChatID, chatID).Scan(
		&b.ChatID, &b.PortalUserID, &isAdmin, &b.BoundAt, &b.BoundByUserID,
	)
	if err == sql.ErrNoRows {
		return nil, ErrTelegramBindingNotFound
	}
	if err != nil {
		return nil, err
	}
	b.IsAdmin = isAdmin != 0
	return &b, nil
}

// GetTelegramBindingByUser returns the first binding whose
// portal_user_id matches userID. Telegram itself allows one user to
// have multiple chats (a phone + a laptop, say) but for our purposes
// the most-recent binding is the one we care about; ORDER BY bound_at
// DESC is implicit via the index iteration order. We pick the latest
// because that's the device the user is most likely typing from.
func GetTelegramBindingByUser(d *sql.DB, userID int64) (*TelegramBinding, error) {
	var b TelegramBinding
	var isAdmin int
	err := d.QueryRow(qSelectTelegramBindingByUser, userID).Scan(
		&b.ChatID, &b.PortalUserID, &isAdmin, &b.BoundAt, &b.BoundByUserID,
	)
	if err == sql.ErrNoRows {
		return nil, ErrTelegramBindingNotFound
	}
	if err != nil {
		return nil, err
	}
	b.IsAdmin = isAdmin != 0
	return &b, nil
}

// ListTelegramBindings returns all bindings, newest first. Used by
// the admin /admin/telegram page (TBD) to show which chat is bound
// to which user, and by tests.
func ListTelegramBindings(d *sql.DB) ([]TelegramBinding, error) {
	rows, err := d.Query(qSelectAllTelegramBindings)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TelegramBinding
	for rows.Next() {
		var b TelegramBinding
		var isAdmin int
		if err := rows.Scan(&b.ChatID, &b.PortalUserID, &isAdmin, &b.BoundAt, &b.BoundByUserID); err != nil {
			return nil, err
		}
		b.IsAdmin = isAdmin != 0
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpsertTelegramBinding creates or replaces the binding for chatID.
// isAdmin should be the current portal_users.is_admin value at the
// time of binding (we denormalize it for fast permission checks).
// boundByUserID is the admin who created the binding (0 for system).
//
// The query uses ON CONFLICT(chat_id) DO UPDATE so a re-bind
// (admin rebinding a chat to a different user) overwrites cleanly.
// We refresh bound_at to "now" so a re-bound chat sorts to the top
// of ListTelegramBindings.
func UpsertTelegramBinding(d *sql.DB, chatID, portalUserID, boundByUserID int64, isAdmin bool) error {
	v := 0
	if isAdmin {
		v = 1
	}
	_, err := d.Exec(qInsertTelegramBinding, chatID, portalUserID, v, boundByUserID)
	return err
}

// DeleteTelegramBinding removes a single binding by chatID. Idempotent
// (deleting a non-existent row is not an error in SQLite).
func DeleteTelegramBinding(d *sql.DB, chatID int64) error {
	_, err := d.Exec(qDeleteTelegramBindingByChat, chatID)
	return err
}

// DeleteTelegramBindingsByUser removes every binding pointing at
// userID. Called by the admin user-delete path
// (handlers_admin_users.go) so a deleted user doesn't leave orphan
// rows that the dispatcher would still treat as legitimate.
func DeleteTelegramBindingsByUser(d *sql.DB, userID int64) error {
	_, err := d.Exec(qDeleteTelegramBindingsByUser, userID)
	return err
}
