// oidc_settings_b277_5.go — DB helpers for the v0.75 oidc_settings
// table. Single-row storage of the OIDC config that the admin
// web UI (form on /admin/oidc) writes to.
//
// The boot sequence reads DB first and falls back to env vars
// only when no DB row exists (preserves the legacy env-only
// deployments). The admin can write a row at any time — the
// form's "Enable" button writes enabled=1 + the form fields;
// "Disable" writes enabled=0 but preserves the form fields
// (so the operator can re-enable without re-typing).

package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OIDCSettings mirrors one row of oidc_settings. Zero value means
// "no row exists yet — boot falls back to env vars".
type OIDCSettings struct {
	Enabled      bool
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURIs string
	KeyDir       string
	CreatedAt    int64
	UpdatedAt    int64
}

// ErrOIDCSettingsNotFound is returned by GetOIDCSettings when no
// row exists. Callers (the boot sequence) fall back to env vars
// in that case.
var ErrOIDCSettingsNotFound = errors.New("oidc_settings: row not found")

// GetOIDCSettings reads the single row of oidc_settings. Returns
// ErrOIDCSettingsNotFound if no row exists.
func GetOIDCSettings(d *sql.DB) (OIDCSettings, error) {
	var s OIDCSettings
	var en int
	err := d.QueryRow(
		`SELECT enabled, issuer, client_id, client_secret, redirect_uris, key_dir, created_at, updated_at
		   FROM oidc_settings WHERE id = 1`,
	).Scan(&en, &s.Issuer, &s.ClientID, &s.ClientSecret, &s.RedirectURIs, &s.KeyDir, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OIDCSettings{}, ErrOIDCSettingsNotFound
		}
		return OIDCSettings{}, err
	}
	s.Enabled = en == 1
	return s, nil
}

// SaveOIDCSettings upserts the single row of oidc_settings.
// Empty issuer is allowed (the admin may have started setup
// and not finished yet — we persist the row so the form
// re-renders with the partial state).
//
// The CreatedAt column is set on first insert (id=1); updated_at
// is bumped on every save.
func SaveOIDCSettings(d *sql.DB, s OIDCSettings) error {
	now := time.Now().Unix()
	en := 0
	if s.Enabled {
		en = 1
	}
	_, err := d.Exec(`
		INSERT INTO oidc_settings
			(id, enabled, issuer, client_id, client_secret, redirect_uris, key_dir, created_at, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, $7)
		ON CONFLICT (id) DO UPDATE SET
			enabled = excluded.enabled,
			issuer = excluded.issuer,
			client_id = excluded.client_id,
			client_secret = excluded.client_secret,
			redirect_uris = excluded.redirect_uris,
			key_dir = excluded.key_dir,
			updated_at = excluded.updated_at`,
		en, s.Issuer, s.ClientID, s.ClientSecret, s.RedirectURIs, s.KeyDir, now)
	return err
}

// DisableOIDCSettings sets enabled=0 on the single row. If no
// row exists the call is a no-op (returns nil) — the admin can
// call this without first having saved a row.
func DisableOIDCSettings(d *sql.DB) error {
	now := time.Now().Unix()
	_, err := d.Exec(`
		INSERT INTO oidc_settings (id, enabled, issuer, client_id, client_secret, redirect_uris, key_dir, created_at, updated_at)
		VALUES (1, 0, '', '', '', '', '', $1, $1)
		ON CONFLICT (id) DO UPDATE SET enabled = 0, updated_at = $1`,
		now)
	return err
}

// ---------- B290 (2026-09-22): the secret must not sit in the DB in clear -----
//
// The operator reported that OIDC could only be enabled by hand-editing the env
// file ("пока нет в env строчки нельзя никак настроить") and asked for a
// self-contained in-app switch. /admin/oidc has had a form since v0.75, but it
// stored the client_secret in PLAINTEXT and — worse — could not take effect
// without a restart, which is why the feature felt absent.
//
// A saved secret is now encrypted with SKYGATE_SECRET_KEY and marked with the
// `enc:v1:` prefix. The marker is what keeps this migration-free: rows written
// before B290 (plaintext, no marker) still read correctly, and a row saved with
// no key available stays plaintext rather than becoming unreadable.

// oidcSecretEncPrefix marks an encrypted client_secret.
const oidcSecretEncPrefix = "enc:v1:"

// GetOIDCSettingsDecrypted reads the row and decrypts the client_secret when it
// carries the encryption marker. Plaintext (legacy) values pass through, and an
// empty secretKeyHex leaves the value as stored — the caller (boot, admin page)
// can then still use an env-provided secret as the fallback.
func GetOIDCSettingsDecrypted(d *sql.DB, secretKeyHex string) (OIDCSettings, error) {
	s, err := GetOIDCSettings(d)
	if err != nil {
		return s, err
	}
	if s.ClientSecret == "" || secretKeyHex == "" {
		return s, nil
	}
	if !strings.HasPrefix(s.ClientSecret, oidcSecretEncPrefix) {
		return s, nil // legacy plaintext row
	}
	plain, derr := DecryptForColumn(strings.TrimPrefix(s.ClientSecret, oidcSecretEncPrefix), secretKeyHex)
	if derr != nil {
		// Never silently drop a configured secret: report it and let the caller
		// decide (the admin page shows the error, boot falls back to env).
		return s, fmt.Errorf("oidc_settings: cannot decrypt the stored client_secret (is SKYGATE_SECRET_KEY unchanged?): %w", derr)
	}
	s.ClientSecret = plain
	return s, nil
}

// SaveOIDCSettingsEncrypted upserts the row and encrypts the client_secret with
// secretKeyHex (empty key = store as-is, which keeps a keyless install working).
// An already-prefixed value is never double-encrypted.
func SaveOIDCSettingsEncrypted(d *sql.DB, s OIDCSettings, secretKeyHex string) error {
	if s.ClientSecret != "" && secretKeyHex != "" && !strings.HasPrefix(s.ClientSecret, oidcSecretEncPrefix) {
		enc, err := EncryptForColumn(s.ClientSecret, secretKeyHex)
		if err != nil {
			return fmt.Errorf("oidc_settings: encrypt client_secret: %w", err)
		}
		s.ClientSecret = oidcSecretEncPrefix + enc
	}
	return SaveOIDCSettings(d, s)
}

// OIDCSecretIsEncrypted reports whether a stored value carries the B290 marker.
// The admin page uses it to say "зашифрован" instead of guessing.
func OIDCSecretIsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, oidcSecretEncPrefix)
}
