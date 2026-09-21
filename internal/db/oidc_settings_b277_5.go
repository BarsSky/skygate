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
