// Package acl — shared headscale ACL pipeline.
//
// 2026-07-13: Этап 11 part 2b. The "rules changed, sync to headscale"
// sequence was previously inlined in three places (web form, web
// API, web delete) plus the bot would need a fourth copy. Extracting
// it into this package lets the bot (which can't import handlers
// without a cycle) reuse the same logic AND lets future web paths
// share the helper without re-implementing the order-sensitive
// dance between GenerateACL → SaveACLSnapshot → SetPolicy →
// Mark + Log.
//
// The pipeline is intentionally narrow: it does the four DB+HS
// steps and nothing more. Caller-specific side effects
// (Notifier.SendAlert, SyncAdvertisedRoutes) stay at the call site
// because the bot skips them while the web form does both.
//
// File map after the refactor Phase D split (2026-10-01). The file was 2209
// lines and mixed four unrelated jobs; the package doc above still describes
// only the pipeline, which is now one of its files:
//   - acl.go                — this doc, the env helpers, the Alerter interface
//   - acl_ownership.go      — node/device ownership → the tag a rule uses
//   - acl_generate.go       — the policy builders (the no-via plane)
//   - acl_tags.go           — tagOwners JSON for the declared dev tags
//   - acl_apply.go          — snapshot + the headscale apply pipeline
//   - acl_generate_via.go   — GenerateACLWithViaForPlane (the via= variant)
//   - acl_set.go            — SetACLForAllPlanes (an operator-supplied policy)

package acl

import (
	"os"
)

// 2026-08-03: v0.32.29 — moved the headscale tailnet
// domain and admin identity out of source-level constants
// and into env-driven config. The defaults are placeholders
// so the public github repo carries no operator-specific DNS
// or usernames; live deployments set SKYGATE_BASE_DOMAIN
// and SKYGATE_ADMIN_IDENTITY in their .env to the real
// values.
func envBaseDomain() string {
	if d := os.Getenv("SKYGATE_BASE_DOMAIN"); d != "" {
		return d
	}
	return "tsnet.example.com"
}

func envAdminIdentity() string {
	if a := os.Getenv("SKYGATE_ADMIN_IDENTITY"); a != "" {
		return a
	}
	return "admin"
}

// Alerter is the minimal interface SaveACLSnapshot needs from a
// notifier. The full telegram.Notifier (which has SendTelegram +
// SendAlert) satisfies this implicitly — Go interfaces are
// structural. Defined locally to avoid an import cycle with
// internal/telegram (which would be the natural home but already
// depends on internal/handlers via App.Notifier).
//
// The SendAlert signature mirrors telegram.Notifier.SendAlert
// (returns int64 = alert id, 0 when not configured). SaveACLSnapshot
// discards the return value — it only needs the side effect of
// dispatching the alert.
//
// Pass nil to suppress the alert (e.g. bot path, where audit_log
// is enough and the operator doesn't need a Telegram ping for
// every /add_rule).
type Alerter interface {
	SendAlert(text string) int64
}

// NoopAlerter discards every SendAlert. Useful as a default in
// code paths that don't have a real notifier wired in.
type NoopAlerter struct{}

// SendAlert is the no-op implementation of Alerter.
func (NoopAlerter) SendAlert(string) int64 { return 0 }
