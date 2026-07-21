// 2026-07-15: Этап 14 v18 (v0.12.0) — per-user headscale control
// plane.
//
// The portal_users table grows two new columns in v0.35:
//
//   headscale_url        TEXT NOT NULL DEFAULT ''
//   headscale_api_key_enc TEXT NOT NULL DEFAULT ''
//
// A non-empty url means "this user is bound to <this> control
// plane instead of the global HEADSCALE_URL". The api key is
// stored encrypted (AES-GCM keyed by SKYGATE_SECRET_KEY) — see
// EncryptForColumn / DecryptForColumn in secrets.go.
//
// This file owns the read / write helpers. The App.HSForUser
// method (handlers/app_controlplane.go) is the routing layer
// that turns a portal_user id into a *headscale.Client.

package db

import (
	"database/sql"
	"errors"
	"strings"
)

// ErrNoUserControlPlane is returned by GetUserHeadscaleConfig when
// the row has no per-user override (url == ''). Callers should
// treat this as "use the global default" and not an error to
// surface to the operator — the admin UI uses it to know which
// rows to render in the "Use default headscale" column.
var ErrNoUserControlPlane = errors.New("db: portal_user has no per-user control plane override")

// UserControlPlane holds the decrypted (url, api_key) pair for a
// portal user. URL "" means "use the global default"; the
// api_key is meaningful only when URL is non-empty.
type UserControlPlane struct {
	URL    string
	APIKey string
}

// HasOverride returns true if the user has a per-user control
// plane configured (URL set). An override without an API key
// is not useful in practice (the headscale client requires
// both) but the helper still reports true so the admin UI can
// show the partial state with an error hint.
func (c UserControlPlane) HasOverride() bool { return c.URL != "" }

// GetUserHeadscaleConfig returns the per-user (url, api_key)
// override for the given portal_users row. Returns
// ErrNoUserControlPlane if the row has no override (URL "").
//
// The api_key is decrypted via DecryptForColumn(keyHex). If
// the stored ciphertext is corrupt (bad base64, auth fail,
// wrong key) the helper returns the underlying
// ErrSecretCiphertextCorrupt so the admin UI can prompt the
// operator to re-enter the key.
//
// NOTE: the keyHex is the SKYGATE_SECRET_KEY value. It's
// passed in (not read from env) so the function is testable
// without touching os.Getenv. App.HSForUser is the place that
// reads the env var.
func GetUserHeadscaleConfig(d *sql.DB, userID int64, keyHex string) (UserControlPlane, error) {
	var url, keyEnc string
	err := d.QueryRow(
		`SELECT headscale_url, headscale_api_key_enc FROM portal_users WHERE id = ?`,
		userID,
	).Scan(&url, &keyEnc)
	if err == sql.ErrNoRows {
		return UserControlPlane{}, ErrUserNotFound
	}
	if err != nil {
		return UserControlPlane{}, err
	}
	if url == "" {
		return UserControlPlane{}, ErrNoUserControlPlane
	}
	apiKey, err := DecryptForColumn(keyEnc, keyHex)
	if err != nil {
		return UserControlPlane{URL: url}, err
	}
	return UserControlPlane{URL: url, APIKey: apiKey}, nil
}

// SetUserHeadscaleConfig writes the per-user override. Pass
// empty url to clear the override (use the global default
// again). Empty apiKey is allowed only when url is also empty
// (a half-configured override is treated as a config error).
//
// The apiKey is encrypted via EncryptForColumn before the
// INSERT. The same keyHex used for decryption is the only
// thing that can decrypt it — rotating SKYGATE_SECRET_KEY
// without re-encrypting makes every existing per-user key
// unreadable (DecryptForColumn returns
// ErrSecretCiphertextCorrupt). The admin UI surfaces this as
// "the stored key was encrypted with a different key;
// please re-enter".
func SetUserHeadscaleConfig(d *sql.DB, userID int64, url, apiKey, keyHex string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		// Clear: delete the override entirely (the
		// column default '' is the "use global" sentinel,
		// so writing the empty string is equivalent).
		_, err := d.Exec(
			`UPDATE portal_users SET headscale_url = '', headscale_api_key_enc = '' WHERE id = ?`,
			userID,
		)
		return err
	}
	if apiKey == "" {
		return errors.New("api_key is required when url is set")
	}
	enc, err := EncryptForColumn(apiKey, keyHex)
	if err != nil {
		return err
	}
	_, err = d.Exec(
		`UPDATE portal_users SET headscale_url = ?, headscale_api_key_enc = ? WHERE id = ?`,
		url, enc, userID,
	)
	return err
}

// ClearUserHeadscaleConfig removes any per-user override
// (writes "" to both columns). Idempotent — clearing a row
// that was already on the global default is a no-op.
func ClearUserHeadscaleConfig(d *sql.DB, userID int64) error {
	_, err := d.Exec(
		`UPDATE portal_users SET headscale_url = '', headscale_api_key_enc = '' WHERE id = ?`,
		userID,
	)
	return err
}

// AllUsersHeadscaleConfig returns the (url, keyEnc) pair for
// every portal user, regardless of whether they have an
// override. Used by ListControlPlanes to count users per plane
// and by the /admin/control-planes page to render the
// per-plane health section.
//
// The api_key is NOT decrypted here — the caller doesn't
// need it for display, and we want to keep the secret
// handling on the request path. DecryptForColumn is called
// on demand (e.g. when the admin clicks "Test" on a plane).
func AllUsersHeadscaleConfig(d *sql.DB) ([]PortalUserControlPlaneRow, error) {
	rows, err := d.Query(
		`SELECT id, username, headscale_url, headscale_api_key_enc FROM portal_users ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PortalUserControlPlaneRow
	for rows.Next() {
		var r PortalUserControlPlaneRow
		if err := rows.Scan(&r.UserID, &r.Username, &r.URL, &r.KeyEncrypted); err != nil {
			return nil, err
		}
		r.HasOverride = r.URL != ""
		out = append(out, r)
	}
	return out, rows.Err()
}

// PortalUserControlPlaneRow is one row of AllUsersHeadscaleConfig's
// output. HasOverride is a pre-computed bool (URL != "") so the
// admin UI doesn't have to repeat the check.
type PortalUserControlPlaneRow struct {
	UserID      int64
	Username    string
	URL         string
	KeyEncrypted string
	HasOverride bool
}

// ControlPlaneSummary aggregates the rows from
// AllUsersHeadscaleConfig into per-plane buckets. The "default"
// plane (URL == "" for every user) is reported as
// GlobalDefaultUsers so the admin sees the split between
// "everyone on the default" and "N users on plane X".
//
// One row per distinct URL, plus the global row. The order
// is "global first, then planes by URL" so the default is
// always at the top of /admin/control-planes.
type ControlPlaneSummary struct {
	URL                string
	Users              []string // usernames on this plane
	HealthLastCheck    string   // "ok" / "fail: <err>" / "" (never checked)
}

// SummariseControlPlanes groups the per-user rows into per-plane
// summaries. The first returned entry is always the global
// default (URL set to globalURL), even when no users use the
// default; this keeps the UI layout stable. The Users slice
// on the default entry lists every user with HasOverride=false.
func SummariseControlPlanes(rows []PortalUserControlPlaneRow, globalURL string) []ControlPlaneSummary {
	defaults := ControlPlaneSummary{URL: globalURL}
	byURL := map[string]*ControlPlaneSummary{
		"__default__": &defaults,
	}
	for _, r := range rows {
		key := "__default__"
		if r.HasOverride {
			key = r.URL
		}
		bucket, ok := byURL[key]
		if !ok {
			bucket = &ControlPlaneSummary{URL: r.URL}
			byURL[key] = bucket
		}
		bucket.Users = append(bucket.Users, r.Username)
	}
	out := make([]ControlPlaneSummary, 0, len(byURL))
	if d, ok := byURL["__default__"]; ok {
		out = append(out, *d)
	}
	// Then the rest, sorted by URL for stability.
	keys := make([]string, 0, len(byURL)-1)
	for k := range byURL {
		if k == "__default__" {
			continue
		}
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	for _, k := range keys {
		out = append(out, *byURL[k])
	}
	return out
}
