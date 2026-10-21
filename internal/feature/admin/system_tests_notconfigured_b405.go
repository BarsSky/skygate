// system_tests_notconfigured_b405.go — B405 (2026-10-21).
//
// «НЕ НАСТРОЕНО» — ЭТО НЕ «СЛОМАНО».
//
// The diagnostic catalogue answers one question per row: "is this subsystem
// healthy?". For an OPTIONAL subsystem there are three answers, not two:
//
//	PASS  — configured, and it works
//	FAIL  — configured, and it is broken
//	SKIP  — never configured: nothing was verified, and nothing is wrong
//
// Measured on the operator's own installation, the middle of that list was
// being used for the last one: `integrations.configured` answered
// "no integration keys configured (DERP / headplane / telegram all empty)" as
// a FAILURE, and `backup.recent` answered "backup dir not configured" the same
// way. Neither is a defect — an installation that uses neither Telegram nor
// Headplane nor S3 backups is a correct installation, and a red row for it
// teaches the operator to ignore red rows. That is the damage: the catalogue's
// verdicts stop meaning anything once "I did not turn this on" and "this is
// broken" look identical.
//
// So: every test whose subject is an OPTIONAL feature reports SKIP with (a) the
// feature's name, (b) exactly what is missing and (c) one line saying how to
// configure it — and FAIL is reserved for the dependencies without which the
// process cannot work at all (its database, its headscale client). The contract
// in scripts/check_b405_not_configured.sh keeps that split honest by scanning
// every `SystemTestFail, "… not configured"` in the package.
package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"skygate/internal/db"
)

// notConfiguredMarker is the phrase every optional-feature SKIP carries. It is
// a marker, not decoration: the page greys the row and the contract greps for
// it, so "not configured" reads the same in the catalogue, in the log and in a
// check.
const notConfiguredMarker = "not configured"

// systemTestNotConfigured is the single constructor for the third verdict.
//
// It exists so the answer cannot drift back into a FAIL one call site at a time:
// the reason names the FEATURE, what is missing, and what to do about it (a bare
// "not configured" leaves the operator exactly as stuck as a false red row).
func systemTestNotConfigured(feature, missing, fix string) (SystemTestStatus, string) {
	msg := fmt.Sprintf("%s is %s (%s) — nothing was verified and nothing is wrong", feature, notConfiguredMarker, missing)
	if fix != "" {
		msg += "; to enable it: " + fix
	}
	return SystemTestSkip, msg
}

// b405Tests is the B405 batch. Kept in its own slice so a block's additions can
// be counted without depending on their position in the registry.
var b405Tests = []SystemTestDef{
	{
		// The optional integrations the panel can drive. Before B405 an
		// unconfigured installation got a red row for "no integration keys
		// configured"; it now says that no integration was ever turned on.
		Name:        "integrations.oidc",
		Category:    "integrations",
		Description: "the OIDC provider is enabled and its settings are complete enough to sign in",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "service or DB not initialised"
			}
			row, err := db.GetOIDCSettings(s.dbc())
			if err != nil {
				if errors.Is(err, db.ErrOIDCSettingsNotFound) {
					// No row at all: the env-var path may still be in
					// use, and the boot sequence is the only place
					// that knows. Not a defect either way.
					return systemTestNotConfigured(
						"the OIDC provider",
						"no oidc_settings row and no SKYGATE_OIDC_ISSUER in the environment",
						"set SKYGATE_OIDC_ISSUER, or configure it on the OIDC card in /admin/settings")
				}
				return SystemTestFail, "read oidc_settings: " + err.Error()
			}
			if !row.Enabled || strings.TrimSpace(row.Issuer) == "" {
				return systemTestNotConfigured(
					"the OIDC provider",
					"the stored settings are disabled or carry no issuer",
					"enable it on the OIDC card in /admin/settings and set the issuer URL")
			}
			// Configured — now a missing piece IS a defect, because the
			// operator asked for OIDC and logins will fail without it.
			var problems []string
			if strings.TrimSpace(row.ClientID) == "" {
				problems = append(problems, "no client_id (headscale cannot complete the code exchange)")
			}
			if strings.TrimSpace(row.RedirectURIs) == "" {
				problems = append(problems, "no redirect_uris (the callback URL is not registered)")
			}
			if strings.TrimSpace(row.KeyDir) == "" {
				problems = append(problems, "no key_dir (the signing key has nowhere to live)")
			}
			if len(problems) > 0 {
				return SystemTestFail, "OIDC is ENABLED but incomplete: " + strings.Join(problems, "; ")
			}
			return SystemTestPass, fmt.Sprintf("OIDC enabled: issuer=%s client_id=%s redirect_uris=%d key_dir=%s",
				row.Issuer, row.ClientID, len(strings.Fields(row.RedirectURIs)), row.KeyDir)
		},
	},
}

func init() {
	TestRegistry = append(TestRegistry, b405Tests...)
}
