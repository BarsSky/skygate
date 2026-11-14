// provider_store_b473.go — B473: DNS credentials as ROWS, one per (provider, zone).
//
// The shipped store (credentials.go) keeps exactly ONE credential set: five fixed global_settings
// keys shaped like reg.ru's login + alternative password + zone. That is why the block cannot be
// adapted to another provider by configuration alone — a Cloudflare API token, a Route53
// access-key/secret pair or an RFC 2136 TSIG key have nowhere to live, and a second zone cannot be
// configured at all.
//
// This file is the storage half of the seam. A row is:
//
//	id         — stable identity (the panel selects a row, capabilities follow the provider)
//	provider   — "regru" today; the registry in internal/dns owns the list
//	zone       — the domain the registrar MANAGES (not a host name)
//	fields_enc — AES-256-GCM ciphertext of a JSON object with the provider's own fields
//	enabled    — a row can be kept but not offered as the active one
//
// The provider-specific part is deliberately OPAQUE here: `map[string]string` encrypted as a whole.
// Adding a provider therefore never needs a migration — only an adapter and a field schema in
// internal/dns — which is the entire point.
//
// THE LEGACY ROW IS IMPORTED ONCE. An installation upgrading from v1.5.x has `ha.dns.*` filled and
// this table empty; `ImportLegacyOnce` turns that into `(regru, <zone>, {login, password, cert_pem})`
// and marks `ha.dns.migrated`, so (a) the operator does not have to retype anything, and (b) an
// operator who deletes every row is not silently re-imported into a resurrected credential.
package dnsexternal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"skygate/internal/db"
)

// dnsMigrationFlagKey records that the legacy `ha.dns.*` row has already been imported. It is set in
// the same pass as the insert, so a crash between the two leaves the flag unset and the next read
// retries the import (which is idempotent: the table is still empty).
const dnsMigrationFlagKey = "ha.dns.migrated"

// dnsActiveRowKey is the global_settings key holding the id of the active credential row. Absent =
// "the first enabled row", which keeps a single-row install working without ever writing this key.
const dnsActiveRowKey = "dns.active_id"

// ProviderRow is one stored credential set, with its provider-specific fields decrypted.
type ProviderRow struct {
	ID       int64
	Provider string
	Zone     string
	Fields   map[string]string
	Enabled  bool
	// Active marks the row the provider builder and the panel use by default.
	Active bool
}

// Field returns a provider field, trimmed. An absent field is "" — callers validate.
func (r ProviderRow) Field(name string) string {
	return strings.TrimSpace(r.Fields[name])
}

// Secret returns a provider field without trimming (a token or key may carry meaningful edge bytes
// in theory; every shipped provider trims anyway, so this is for callers that want the raw value).
func (r ProviderRow) Secret(name string) string {
	return r.Fields[name]
}

// normalizedProvider maps the historical alias to the honest name, so `external` and `regru` can
// never become two rows for the same zone.
func normalizedProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" || p == ProviderExternal {
		return ProviderRegru
	}
	return p
}

// ListProviderRows returns every row, newest-updated last, with the active one marked.
//
// It first performs the one-time legacy import, so the first read after an upgrade already sees the
// reg.ru credentials the install has been using.
func (s *Store) ListProviderRows() ([]ProviderRow, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("dnsexternal: no database")
	}
	if _, err := s.ImportLegacyOnce(); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT id, provider, zone, fields_enc, enabled FROM dns_provider_creds
	                          ORDER BY provider, zone, id`)
	if err != nil {
		return nil, fmt.Errorf("dnsexternal: list provider rows: %w", err)
	}
	defer rows.Close()
	out := []ProviderRow{}
	for rows.Next() {
		var (
			id        int64
			provider  string
			zone      string
			fieldsEnc string
			enabled   int
		)
		if err := rows.Scan(&id, &provider, &zone, &fieldsEnc, &enabled); err != nil {
			return nil, err
		}
		fields, derr := s.decryptFields(fieldsEnc)
		if derr != nil {
			// A row whose ciphertext cannot be decrypted is REPORTED, not skipped: silently dropping
			// it would make the panel claim the provider is not configured while a row exists.
			return nil, fmt.Errorf("dnsexternal: row %d (%s/%s): %w", id, provider, zone, derr)
		}
		out = append(out, ProviderRow{
			ID: id, Provider: provider, Zone: zone, Fields: fields, Enabled: enabled != 0,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if active, aerr := s.activeRowID(); aerr == nil && active > 0 {
		for i := range out {
			out[i].Active = out[i].ID == active
		}
		return out, nil
	}
	// No explicit choice: the first enabled row is the active one, and nothing is written.
	for i := range out {
		if out[i].Enabled {
			out[i].Active = true
			break
		}
	}
	return out, nil
}

// ActiveProviderRow returns the row the provider builder should use.
func (s *Store) ActiveProviderRow() (ProviderRow, error) {
	rows, err := s.ListProviderRows()
	if err != nil {
		return ProviderRow{}, err
	}
	for _, r := range rows {
		if r.Active && r.Enabled {
			return r, nil
		}
	}
	return ProviderRow{}, errNoActiveRow
}

// errNoActiveRow is the "nothing configured yet" state — a fresh install, not an error the operator
// should see as a malfunction.
var errNoActiveRow = fmt.Errorf("dnsexternal: no DNS provider credentials configured")

// IsNoActiveRow reports the empty state, so callers can render "not configured" instead of an error.
func IsNoActiveRow(err error) bool {
	return err != nil && strings.Contains(err.Error(), errNoActiveRow.Error())
}

// GetProviderRow returns one row by id.
func (s *Store) GetProviderRow(id int64) (ProviderRow, error) {
	var (
		provider, zone, fieldsEnc string
		enabled                   int
	)
	err := s.DB.QueryRow(`SELECT provider, zone, fields_enc, enabled FROM dns_provider_creds WHERE id = $1`, id).
		Scan(&provider, &zone, &fieldsEnc, &enabled)
	if err == sql.ErrNoRows {
		return ProviderRow{}, fmt.Errorf("dnsexternal: no credential row with id %d", id)
	}
	if err != nil {
		return ProviderRow{}, err
	}
	fields, derr := s.decryptFields(fieldsEnc)
	if derr != nil {
		return ProviderRow{}, derr
	}
	return ProviderRow{ID: id, Provider: provider, Zone: zone, Fields: fields, Enabled: enabled != 0}, nil
}

// SaveProviderRow inserts or updates the row for (provider, zone).
//
// The UNIQUE index on (provider, zone) is what makes "one credential set per zone" true: an existing
// row is UPDATED, never duplicated, so the operator cannot end up with two rows that disagree and an
// ORDER BY that decides which one the switch uses.
func (s *Store) SaveProviderRow(provider, zone string, fields map[string]string) (int64, error) {
	if s.DB == nil {
		return 0, fmt.Errorf("dnsexternal: no database")
	}
	provider = normalizedProvider(provider)
	zone = strings.ToLower(strings.TrimSpace(zone))
	if provider == "" || zone == "" {
		return 0, fmt.Errorf("dnsexternal: provider and zone are required")
	}
	fieldsEnc, err := s.encryptFields(fields)
	if err != nil {
		return 0, err
	}
	now := dbNowUnix()
	var id int64
	err = s.DB.QueryRow(`SELECT id FROM dns_provider_creds WHERE provider = $1 AND zone = $2`, provider, zone).Scan(&id)
	switch {
	case err == nil:
		if _, uerr := s.DB.Exec(`UPDATE dns_provider_creds
		                            SET fields_enc = $1, enabled = 1, updated_at = $2
		                          WHERE id = $3`, fieldsEnc, now, id); uerr != nil {
			return 0, fmt.Errorf("dnsexternal: update row %d: %w", id, uerr)
		}
		return id, nil
	case err != sql.ErrNoRows:
		return 0, err
	}
	if _, err := s.DB.Exec(`INSERT INTO dns_provider_creds (provider, zone, fields_enc, enabled, created_at, updated_at)
	                        VALUES ($1, $2, $3, 1, $4, $4)`, provider, zone, fieldsEnc, now); err != nil {
		return 0, fmt.Errorf("dnsexternal: insert row: %w", err)
	}
	// LastInsertId does not exist on pgx for a plain Exec without RETURNING; read it back by the
	// unique key, which is cheap and dialect-neutral.
	if err := s.DB.QueryRow(`SELECT id FROM dns_provider_creds WHERE provider = $1 AND zone = $2`, provider, zone).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteProviderRow removes one row and clears the active pointer when it pointed at it.
func (s *Store) DeleteProviderRow(id int64) error {
	if _, err := s.DB.Exec(`DELETE FROM dns_provider_creds WHERE id = $1`, id); err != nil {
		return fmt.Errorf("dnsexternal: delete row %d: %w", id, err)
	}
	if active, _ := s.activeRowID(); active == id {
		if err := db.SetGlobalSetting(s.DB, dnsActiveRowKey, ""); err != nil {
			return fmt.Errorf("dnsexternal: clear active row: %w", err)
		}
	}
	return nil
}

// ProviderRowByKey returns the row for (provider, zone) and whether it exists. The panel's save path
// uses it to MERGE: a masked password field left empty must KEEP the stored secret instead of wiping
// it, which is the difference between "edit the zone" and "lock yourself out of the registrar".
func (s *Store) ProviderRowByKey(provider, zone string) (ProviderRow, bool, error) {
	provider = normalizedProvider(provider)
	zone = strings.ToLower(strings.TrimSpace(zone))
	var (
		id        int64
		fieldsEnc string
		enabled   int
	)
	err := s.DB.QueryRow(`SELECT id, fields_enc, enabled FROM dns_provider_creds
	                       WHERE provider = $1 AND zone = $2`, provider, zone).
		Scan(&id, &fieldsEnc, &enabled)
	if err == sql.ErrNoRows {
		return ProviderRow{}, false, nil
	}
	if err != nil {
		return ProviderRow{}, false, err
	}
	fields, derr := s.decryptFields(fieldsEnc)
	if derr != nil {
		return ProviderRow{}, false, derr
	}
	return ProviderRow{ID: id, Provider: provider, Zone: zone, Fields: fields, Enabled: enabled != 0}, true, nil
}

// SetActiveRow records the operator's choice.
func (s *Store) SetActiveRow(id int64) error {
	if id == 0 {
		return db.SetGlobalSetting(s.DB, dnsActiveRowKey, "")
	}
	if _, err := s.GetProviderRow(id); err != nil {
		return err
	}
	return db.SetGlobalSetting(s.DB, dnsActiveRowKey, fmt.Sprintf("%d", id))
}

// activeRowID reads the stored pointer (0 when unset or unparsable).
func (s *Store) activeRowID() (int64, error) {
	v, err := db.GetGlobalSetting(s.DB, dnsActiveRowKey, "")
	if err != nil {
		return 0, err
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	var id int64
	if _, err := fmt.Sscanf(v, "%d", &id); err != nil {
		return 0, nil
	}
	return id, nil
}

// ImportLegacyOnce turns the pre-B473 `ha.dns.*` row into a provider row, exactly once.
//
// Returns (true, nil) when it imported something, (false, nil) when there was nothing to do — which
// is the normal case on every read after the first, because the flag is written in the same call.
func (s *Store) ImportLegacyOnce() (bool, error) {
	if s.DB == nil {
		return false, fmt.Errorf("dnsexternal: no database")
	}
	if flag, _ := db.GetGlobalSetting(s.DB, dnsMigrationFlagKey, ""); strings.TrimSpace(flag) == "1" {
		return false, nil
	}
	// Anything already in the table means the operator configured a provider through the new path;
	// do not resurrect the legacy row over it.
	var n int
	if err := s.DB.QueryRow(`SELECT count(*) FROM dns_provider_creds`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, db.SetGlobalSetting(s.DB, dnsMigrationFlagKey, "1")
	}
	legacy, err := s.loadLegacy()
	if err != nil {
		return false, err
	}
	zone := strings.ToLower(strings.TrimSpace(legacy.Zone))
	if zone == "" || strings.TrimSpace(legacy.Login) == "" {
		// Nothing usable to import (fresh install). Set the flag anyway, so the check is not repeated
		// on every page load; a later save writes the row directly.
		return false, db.SetGlobalSetting(s.DB, dnsMigrationFlagKey, "1")
	}
	fields := map[string]string{
		"login":    legacy.Login,
		"password": legacy.Password,
	}
	if legacy.CertPEM != "" {
		fields["cert_pem"] = legacy.CertPEM
	}
	id, serr := s.SaveProviderRow(ProviderRegru, zone, fields)
	if serr != nil {
		return false, serr
	}
	if err := s.SetActiveRow(id); err != nil {
		return false, err
	}
	if err := db.SetGlobalSetting(s.DB, dnsMigrationFlagKey, "1"); err != nil {
		return false, err
	}
	return true, nil
}

// encryptFields stores the provider-specific fields as one AES-GCM ciphertext of their JSON.
func (s *Store) encryptFields(fields map[string]string) (string, error) {
	if s.SecretKey == "" {
		return "", db.ErrSecretKeyUnset
	}
	clean := map[string]string{}
	for k, v := range fields {
		clean[strings.ToLower(strings.TrimSpace(k))] = v
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return "", fmt.Errorf("dnsexternal: marshal fields: %w", err)
	}
	enc, err := db.EncryptForColumn(string(raw), s.SecretKey)
	if err != nil {
		return "", fmt.Errorf("dnsexternal: encrypt fields: %w", err)
	}
	return enc, nil
}

// decryptFields is the inverse. An EMPTY ciphertext is an empty field set (a row written before any
// secret was filled), not a decryption failure.
func (s *Store) decryptFields(enc string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(enc) == "" {
		return out, nil
	}
	raw, err := db.DecryptForColumn(enc, s.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("dnsexternal: decrypt fields: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("dnsexternal: parse fields: %w", err)
	}
	return out, nil
}

// dbNowUnix is a tiny indirection so the store has no clock of its own (and tests can be exact about
// ordering without freezing time globally).
func dbNowUnix() int64 { return time.Now().Unix() }
