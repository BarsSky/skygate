// Package db — IntegrationConfig (v0.11.0): runtime-editable config
// for the pluggable components the operator can swap out without
// re-running deploy.sh. Persisted in global_settings, falls back
// to the deploy-time env vars (`DERP_EXTERNAL_URLS`,
// `HEADPLANE_EXTERNAL_URL`, `HEADPLANE_ENABLED`) when no DB row
// exists, so an operator who has only ever used deploy.sh keeps
// working without any change.
//
// Four keys in global_settings:
//
//   derp.external_urls      TEXT  comma-separated list of HTTPS
//                                  URLs serving a Tailscale-compatible
//                                  derpmap.json. Empty = no external
//                                  DERP (only the public Tailscale
//                                  relay + the bundled derper, if
//                                  enabled).
//
//   derp.bundled_enabled    TEXT  "0" / "1". Controls whether
//                                  deploy.sh should bring up the
//                                  bundled derper container. Empty
//                                  = false.
//
//   headplane.mode          TEXT  "bundled" / "external" / "off".
//                                  "bundled" = local sidecar (the
//                                  v0.10.10 default);
//                                  "external" = point at an
//                                  existing instance (see
//                                  HEADPLANE_EXTERNAL_URL in
//                                  .env.example);
//                                  "off" = no Headplane (skygate
//                                  has no UI to link to). Empty =
//                                  "bundled" for backward compat.
//
//   headplane.external_url  TEXT  Public URL of the existing
//                                  Headplane. Only consulted when
//                                  headplane.mode == "external".
//                                  Empty = use the bundled sidecar.
//
// The Storage pattern matches the existing
// SaveTelegramStrictMode / LoadTelegramStrictMode pair (see
// telegram_login_tokens.go): the bot and the admin UI read
// global_settings on every request so a change takes effect on
// the next /admin/* page load (or, for headscale, on the next
// ./deploy/deploy.sh — v0.11.0 still asks the operator to run
// it after a save; the runtime re-renderer is the v0.11.1
// follow-up).
//
// 2026-07-15: Этап 14 v14 (v0.11.0). Lifts the v0.10.12 deploy-time
// env vars into the web UI so the operator can manage DERP /
// Headplane from /admin/integrations without touching the shell.

package db

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

// IntegrationConfig is the runtime-editable view of the pluggable
// components. Every field is the value the operator has chosen via
// /admin/integrations (or the deploy-time env-var fallback for
// operators who haven't migrated yet).
//
// The Config struct is the input to deploy.sh's re-render step
// (the v0.11.0 web UI just saves; v0.11.1 will wire a Go-side
// re-renderer that reads this same struct).
type IntegrationConfig struct {
	// DERP — external URLs (comma-separated, no spaces). Empty
	// list = no external DERP. The bundled derper is a
	// separate switch (BundledDERP) so an operator can run
	// the bundled one AND point at additional external ones.
	DERPExternalURLs []string
	// DERP — bundled container (derper). When true, deploy.sh
	// renders + starts the derper-compose.yml service. When
	// false, deploy.sh skips it.
	BundledDERP bool

	// Headplane — mode. "bundled" = local sidecar (default);
	// "external" = point at an existing instance; "off" =
	// no Headplane wired in.
	HeadplaneMode string
	// Headplane — public URL of the existing instance. Only
	// consulted when HeadplaneMode == "external".
	HeadplaneExternalURL string
}

// LoadIntegrations reads the integration config from global_settings
// and falls back to the deploy-time env vars for any unset key.
// The env-var fallback is the bridge from the v0.10.12 deploy-time
// model to the v0.11.0 runtime model: an operator who has only
// ever used deploy.sh sees the same effective config without
// having to touch the web UI first.
//
// The env-var reads are best-effort: a missing env var or a
// malformed value falls through to the DB default (empty / false
// / "bundled"). This means the first admin visit to
// /admin/integrations on a fresh DB shows the same state as
// deploy.sh would have produced — no surprises.
func LoadIntegrations(d *sql.DB, env map[string]string) (*IntegrationConfig, error) {
	if env == nil {
		env = map[string]string{}
	}
	cfg := &IntegrationConfig{
		HeadplaneMode: "bundled", // default
	}
	// DERP — external URLs.
	if v := loadGlobalSetting(d, "derp.external_urls"); v != "" {
		cfg.DERPExternalURLs = splitCSV(v)
	} else if v := env["DERP_EXTERNAL_URLS"]; v != "" {
		cfg.DERPExternalURLs = splitCSV(v)
	}
	// DERP — bundled.
	if v := loadGlobalSetting(d, "derp.bundled_enabled"); v != "" {
		cfg.BundledDERP = v == "1"
	} else if v := env["DERP_ENABLED"]; v != "" {
		cfg.BundledDERP = strings.EqualFold(v, "true")
	}
	// Headplane — mode. "bundled" is the default; we only
	// override if the DB or env explicitly says otherwise.
	if v := loadGlobalSetting(d, "headplane.mode"); v != "" {
		cfg.HeadplaneMode = v
	} else if v := env["HEADPLANE_ENABLED"]; v != "" {
		// Map the v0.10.12 boolean to the v0.11.0 mode:
		//   HEADPLANE_ENABLED=false → "off"
		//   HEADPLANE_ENABLED=true  → (no override; default
		//                              "bundled" wins unless
		//                              HEADPLANE_EXTERNAL_URL
		//                              is set, in which case
		//                              "external" wins below)
		if strings.EqualFold(v, "false") {
			cfg.HeadplaneMode = "off"
		}
	}
	// Headplane — external URL. Presence flips mode to "external"
	// (matches deploy.sh's behaviour where setting
	// HEADPLANE_EXTERNAL_URL is enough to point at the existing
	// instance without flipping HEADPLANE_ENABLED).
	if v := loadGlobalSetting(d, "headplane.external_url"); v != "" {
		cfg.HeadplaneExternalURL = v
		cfg.HeadplaneMode = "external"
	} else if v := env["HEADPLANE_EXTERNAL_URL"]; v != "" {
		cfg.HeadplaneExternalURL = v
		cfg.HeadplaneMode = "external"
	}
	return cfg, nil
}

// LoadIntegrationsFromOS is the production wrapper: it reads the
// env-var map from os.Getenv. Used by /admin/integrations and any
// other call site that wants the integrated view; tests pass a
// custom env map for determinism.
func LoadIntegrationsFromOS(d *sql.DB) (*IntegrationConfig, error) {
	return LoadIntegrations(d, map[string]string{
		"DERP_ENABLED":           os.Getenv("DERP_ENABLED"),
		"DERP_EXTERNAL_URLS":     os.Getenv("DERP_EXTERNAL_URLS"),
		"HEADPLANE_ENABLED":      os.Getenv("HEADPLANE_ENABLED"),
		"HEADPLANE_EXTERNAL_URL": os.Getenv("HEADPLANE_EXTERNAL_URL"),
	})
}

// SaveIntegrations writes the integration config to global_settings.
// Each field is upserted; missing/empty values are written as the
// empty string so the row exists (so the read path can distinguish
// "operator cleared the value" from "operator never set it"). The
// env-var fallback in LoadIntegrations still works for operators
// who haven't visited /admin/integrations yet.
//
// The caller is responsible for validation (URL shape, allowed
// modes, etc.) — SaveIntegrations writes whatever it's given. The
// HTTP handler does the validation in the form-decode step.
func SaveIntegrations(d *sql.DB, cfg *IntegrationConfig) error {
	stmts := []struct {
		key, value string
	}{
		{"derp.external_urls", strings.Join(cfg.DERPExternalURLs, ",")},
		{"derp.bundled_enabled", boolToZeroOne(cfg.BundledDERP)},
		{"headplane.mode", cfg.HeadplaneMode},
		{"headplane.external_url", cfg.HeadplaneExternalURL},
	}
	for _, s := range stmts {
		if _, err := d.Exec(
			`INSERT INTO global_settings(key, value, updated_at)
			 VALUES (?, ?, strftime('%s','now'))
			 ON CONFLICT(key) DO UPDATE SET
			   value = excluded.value,
			   updated_at = excluded.updated_at`,
			s.key, s.value,
		); err != nil {
			return fmt.Errorf("save %s: %w", s.key, err)
		}
	}
	return nil
}

// loadGlobalSetting is a tiny helper for the read path. Returns
// "" on any error so the caller can fall through to the env var
// (no need to distinguish "row missing" from "row has empty
// value" — both are "use the default").
func loadGlobalSetting(d *sql.DB, key string) string {
	var v string
	err := d.QueryRow(`SELECT value FROM global_settings WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return ""
	}
	return v
}

// splitCSV trims whitespace around each element. Tailscale's own
// CSV parsing tolerates whitespace, but the operator types these
// into a form field and a trailing space is an easy typo — be
// lenient on read.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// boolToZeroOne is the inverse of the LoadTelegramStrictMode
// convention: "1" for true, "0" for false. Centralised here so
// future bool fields don't have to invent their own encoding.
func boolToZeroOne(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
