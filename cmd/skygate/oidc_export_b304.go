// B304 (v1.5.69) — `skygate oidc-export`.
//
// Why it exists: the panel can now configure everything, but headscale still needs
// the SAME client_secret in its own config, and the admin UI deliberately never
// echoes a stored secret back over HTTP (a stolen admin session must not leak it).
// A host-side command closes that gap without weakening the UI: run as the skygate
// user on the box, it prints what headscale needs.
//
// It is also the piece that makes the generated apply script safe: the script
// contains NO secret, it calls this command on the host.
//
// Usage:
//
//	skygate oidc-export                 # .env block for skygate (secret replaced by a note)
//	skygate oidc-export --secret        # same, with the real client_secret
//	skygate oidc-export --headscale     # headscale `oidc:` block, WITH the secret
//	skygate oidc-export --headscale --no-secret
//	                                    # headscale block with a placeholder (for docs/diffs)
//	skygate oidc-export --json          # machine-readable (never includes the secret)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/oidc"
)

// oidcExportValues is the resolved OIDC configuration plus where each field came
// from — the same precedence /admin/oidc uses (DB row wins, env is the fallback).
type oidcExportValues struct {
	Enabled      bool   `json:"enabled"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"-"` // never marshalled
	SecretSet    bool   `json:"secret_set"`
	RedirectURIs string `json:"redirect_uris"`
	KeyDir       string `json:"key_dir"`
	Source       map[string]string
}

func runOIDCExportSubcommand(args []string) error {
	wantSecret := false
	wantHeadscale := false
	noSecret := false
	wantJSON := false
	for _, a := range args {
		switch a {
		case "--secret":
			wantSecret = true
		case "--headscale", "--headscale-config":
			wantHeadscale = true
		case "--no-secret":
			noSecret = true
		case "--json":
			wantJSON = true
		case "help", "--help", "-h":
			fmt.Print(oidcExportHelp())
			return nil
		default:
			return fmt.Errorf("unknown flag %q (try: skygate oidc-export --help)", a)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}
	vals := oidcExportValues{
		Issuer:       strings.TrimRight(strings.TrimSpace(cfg.OIDCIssuerURL), "/"),
		ClientID:     strings.TrimSpace(cfg.OIDCClientID),
		ClientSecret: strings.TrimSpace(cfg.OIDCClientSecret),
		RedirectURIs: strings.TrimSpace(cfg.OIDCRedirectURIs),
		KeyDir:       strings.TrimSpace(cfg.OIDCKeyDir),
		Source: map[string]string{
			"issuer": "env", "client_id": "env", "client_secret": "env",
			"redirect_uris": "env", "key_dir": "env",
		},
	}
	if vals.Issuer == "" {
		vals.Source["issuer"] = "unset"
	}
	if vals.ClientSecret == "" {
		vals.Source["client_secret"] = "unset"
	}

	// The DB row wins where it carries a value — this is the whole point of the
	// command: whatever the panel shows is what headscale must be configured with.
	if d, oerr := db.OpenDSN(cfg.DBDSN); oerr == nil {
		if row, rerr := db.GetOIDCSettingsDecrypted(d, cfg.SecretKeyHex); rerr == nil {
			applyOIDCExportRow(&vals, row)
		} else if rerr != db.ErrOIDCSettingsNotFound {
			fmt.Fprintf(os.Stderr, "oidc-export: warning: reading oidc_settings failed: %v (using env values)\n", rerr)
		}
		_ = d.Close()
	} else {
		fmt.Fprintf(os.Stderr, "oidc-export: warning: database unavailable: %v (using env values)\n", oerr)
	}

	vals.SecretSet = vals.ClientSecret != ""
	vals.Enabled = vals.Issuer != "" && vals.SecretSet
	if strings.EqualFold(strings.TrimSpace(cfg.OIDCEnabledEnv), "false") ||
		strings.EqualFold(strings.TrimSpace(cfg.OIDCEnabledEnv), "0") ||
		strings.EqualFold(strings.TrimSpace(cfg.OIDCEnabledEnv), "no") {
		vals.Enabled = false
		vals.Source["enabled"] = "env(off)"
	}

	switch {
	case wantJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(vals)
	case wantHeadscale:
		fmt.Print(renderHeadscaleOIDCBlock(vals, !noSecret && wantSecret))
	default:
		fmt.Print(renderOIDCEnvBlock(vals, wantSecret))
	}
	return nil
}

// applyOIDCExportRow overlays the stored oidc_settings row (the values the panel
// saved) onto the env-derived defaults.
func applyOIDCExportRow(v *oidcExportValues, row db.OIDCSettings) {
	if s := strings.TrimSpace(row.Issuer); s != "" {
		v.Issuer = strings.TrimRight(s, "/")
		v.Source["issuer"] = "ui"
	}
	if s := strings.TrimSpace(row.ClientID); s != "" {
		v.ClientID = s
		v.Source["client_id"] = "ui"
	}
	if s := strings.TrimSpace(row.ClientSecret); s != "" {
		v.ClientSecret = s
		v.Source["client_secret"] = "ui"
	}
	if s := strings.TrimSpace(row.RedirectURIs); s != "" {
		v.RedirectURIs = s
		v.Source["redirect_uris"] = "ui"
	}
	if s := strings.TrimSpace(row.KeyDir); s != "" {
		v.KeyDir = s
		v.Source["key_dir"] = "ui"
	}
	v.Source["enabled"] = fmt.Sprintf("ui(%v)", row.Enabled)
}

// renderOIDCEnvBlock prints the .env lines for skygate. Without --secret the
// secret is rendered as a command substitution, so a shared terminal, a ticket or
// a CI log never carries it.
func renderOIDCEnvBlock(v oidcExportValues, withSecret bool) string {
	secret := "SKYGATE_OIDC_CLIENT_SECRET=$(skygate oidc-export --secret)   # or paste the value headscale also has"
	if !v.SecretSet {
		secret = "SKYGATE_OIDC_CLIENT_SECRET=<not set — fill it on /admin/oidc first>"
	} else if withSecret {
		secret = "SKYGATE_OIDC_CLIENT_SECRET=" + v.ClientSecret
	}
	var b strings.Builder
	b.WriteString("# skygate .env — OIDC (B304). Values saved on /admin/oidc WIN over these;\n")
	b.WriteString("# this block is for hosts managed through an env file.\n")
	b.WriteString("SKYGATE_OIDC_ISSUER=" + v.Issuer + "\n")
	b.WriteString("SKYGATE_OIDC_CLIENT_ID=" + v.ClientID + "\n")
	b.WriteString(secret + "\n")
	b.WriteString("SKYGATE_OIDC_REDIRECT_URIS=" + v.RedirectURIs + "\n")
	b.WriteString("SKYGATE_OIDC_KEY_DIR=" + v.KeyDir + "\n")
	b.WriteString("# SKYGATE_OIDC_ENABLED=false   # emergency off-switch: beats every other setting\n")
	b.WriteString("# sources: " + oidcSourceSummary(v.Source) + "\n")
	return b.String()
}

// renderHeadscaleOIDCBlock prints the `oidc:` block headscale needs, with the
// secret when it was explicitly requested (--headscale --secret) — the form
// deploy/skygate-apply-oidc.sh consumes.
//
// B313: the rendering itself moved to internal/oidc.RenderHeadscaleBlock so the
// privileged apply request the panel now writes carries a byte-identical block. This
// wrapper exists only to map the exporter's own value struct onto that shared one.
func renderHeadscaleOIDCBlock(v oidcExportValues, withSecret bool) string {
	return oidc.RenderHeadscaleBlock(oidc.HeadscaleBlockValues{
		Issuer:       v.Issuer,
		ClientID:     v.ClientID,
		ClientSecret: v.ClientSecret,
		RedirectURIs: v.RedirectURIs,
		SecretSet:    v.SecretSet,
	}, withSecret)
}

// oidcSourceSummary renders "field=source, …" deterministically, so a scripted
// diff of two exports shows which half moved (the panel or the env).
func oidcSourceSummary(src map[string]string) string {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+src[k])
	}
	return strings.Join(parts, " ")
}

func oidcExportHelp() string {
	return `skygate oidc-export — print the OIDC configuration headscale must be given (B304)

Usage:
  skygate oidc-export [flags]

Flags:
  --secret        include the stored client_secret in the .env block
  --headscale     print the headscale oidc: block instead of the .env block
  --no-secret     with --headscale: keep the secret as a placeholder
  --json          machine-readable output (never includes the secret)

Values come from the same precedence the admin panel uses: the oidc_settings row
saved on /admin/oidc wins, the SKYGATE_OIDC_* env vars are the fallback.
`
}
