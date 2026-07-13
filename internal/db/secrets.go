package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// Telegram secret storage lives in global_settings so the admin UI can
// read/write it without going through the file system. Two keys are used:
//
//   telegram.bot_token    text, the API token BotFather issued
//   telegram.chat_id      text, the chat_id to send to (could be a user
//                                integer or a -100... supergroup id)
//
// The token is stored verbatim — we never log it, never include it in
// error paths, and the GetTelegramUIState() helper returns only a
// fingerprint (last 4 chars of <prefix>:<secret>) so the UI can show
// "configured" without exposing the secret.
//
// These keys are deliberately NOT seeded by migrations — they must only
// exist after an administrator has consciously configured them. Tests
// seed them via helpers.

const (
	tgBotTokenKey = "telegram.bot_token"
	tgChatIDKey   = "telegram.chat_id"
)

// SaveTelegramToken writes both bot_token and chat_id atomically.
// Either both succeed or both are rejected, so the system is never in a
// half-configured state. Pass empty chat_id with non-empty token to keep
// the previous chat_id (so a chat rotation alone works).
func SaveTelegramToken(d *sql.DB, token, chatID string) error {
	token = strings.TrimSpace(token)
	chatID = strings.TrimSpace(chatID)
	if token == "" && chatID == "" {
		return fmt.Errorf("token and chat_id both empty; nothing to save")
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if token != "" {
		if _, err := tx.Exec(
			`INSERT INTO global_settings (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = strftime('%s','now')`,
			tgBotTokenKey, token,
		); err != nil {
			return err
		}
	}
	if chatID != "" {
		if _, err := tx.Exec(
			`INSERT INTO global_settings (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = strftime('%s','now')`,
			tgChatIDKey, chatID,
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// LoadTelegramToken returns (bot_token, chat_id, ok). ok is true when
// EITHER the token OR the chat_id is set (token-only is enough to
// receive messages via getUpdates; chat_id is only needed for outgoing
// notifications via sendMessage).
//
// 2026-07-13: Этап 12 follow-up — ok used to require both, which
// meant the bot wouldn't even start polling until the admin had
// pasted a chat_id (a chicken-and-egg: chat_id only becomes known
// AFTER the bot is polling and the admin messages it). The new
// semantics: token-only = polling-enabled (can receive commands);
// chat_id additionally = can-send (notifications work).
//
// For callers that need to know "can I send?" use
// LoadTelegramSendTarget which returns ok only when chat_id is
// also set.
func LoadTelegramToken(d *sql.DB) (token, chatID string, ok bool, err error) {
	if err = d.QueryRow(`SELECT value FROM global_settings WHERE key = ?`, tgBotTokenKey).Scan(&token); err == sql.ErrNoRows {
		token, err = "", nil
	} else if err != nil {
		return "", "", false, err
	}
	if err = d.QueryRow(`SELECT value FROM global_settings WHERE key = ?`, tgChatIDKey).Scan(&chatID); err == sql.ErrNoRows {
		chatID, err = "", nil
	} else if err != nil {
		return "", "", false, err
	}
	// 2026-07-13: changed from `token != "" && chatID != ""` to
	// `token != "" || chatID != ""` — see function comment.
	return token, chatID, token != "" || chatID != "", nil
}

// LoadTelegramSendTarget returns (token, chat_id, ok). ok is true
// only when BOTH are set, so callers that need to sendMessage can
// short-circuit with a clear "no chat_id configured" path.
func LoadTelegramSendTarget(d *sql.DB) (token, chatID string, ok bool, err error) {
	if err = d.QueryRow(`SELECT value FROM global_settings WHERE key = ?`, tgBotTokenKey).Scan(&token); err == sql.ErrNoRows {
		token, err = "", nil
	} else if err != nil {
		return "", "", false, err
	}
	if err = d.QueryRow(`SELECT value FROM global_settings WHERE key = ?`, tgChatIDKey).Scan(&chatID); err == sql.ErrNoRows {
		chatID, err = "", nil
	} else if err != nil {
		return "", "", false, err
	}
	return token, chatID, token != "" && chatID != "", nil
}

// DeleteTelegramToken removes both keys. Idempotent.
func DeleteTelegramToken(d *sql.DB) error {
	if _, err := d.Exec(`DELETE FROM global_settings WHERE key IN (?, ?)`, tgBotTokenKey, tgChatIDKey); err != nil {
		return err
	}
	return nil
}

// TelegramFingerprint returns a short, non-secret identifier of a bot
// token that is safe to log or render in the UI. Tokens look like
//
//	1234567890:AAGt34wtH...long...
//
// We surface only "<botid>:<last4>" so admins can confirm which bot
// they configured without leaking the secret. If the token does not
// match the expected shape we return "?" — the UI then knows the
// stored value is malformed and can prompt the operator to re-enter.
func TelegramFingerprint(token string) string {
	if token == "" {
		return ""
	}
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "?"
	}
	secret := parts[1]
	if len(secret) < 4 {
		return parts[0] + ":?short"
	}
	return parts[0] + ":" + secret[len(secret)-4:]
}

// RandomConfirmationToken is a short hex token used to require the
// admin to type a confirmation before destructive actions (rotate /
// disable). 6 hex chars (~16M combinations) is plenty against an
// authenticated-form CSRF where the attacker already has a session.
func RandomConfirmationToken(n int) (string, error) {
	if n < 1 {
		n = 1
	}
	if n > 16 {
		n = 16
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
