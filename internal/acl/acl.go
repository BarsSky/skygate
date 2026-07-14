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
package acl

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

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

// GenerateACL builds the per-user headscale 0.29 HuJSON policy.
// Pure function — only uses the database. Moved out of the
// (a *App) method to make it accessible from the telegram bot
// (which has no *App reference) without a cycle through
// internal/handlers.
//
// 2026-07-11: Этап 9 part 2 — SQL moved to db.GetACLEntries.
//
// 2026-07-13: signature widened to take *sql.DB instead of
// (*App). The body is byte-for-byte identical to the previous
// App method. baseDomain ("tsnet.skynas.ru") is still hard-coded
// because it is the only deployment; refactor to read it from
// config.Config is on the multi-deploy roadmap.
func GenerateACL(d *sql.DB) (string, error) {
	aclRows, err := db.GetACLEntries(d)
	if err != nil {
		return "", err
	}

	type ruleEntry struct {
		deviceIP string
		target   string
		action   string
	}
	var entries []ruleEntry
	for _, e := range aclRows {
		if e.TargetType == "subnet" || e.TargetType == "ip" {
			entries = append(entries, ruleEntry{deviceIP: e.DeviceIP, target: e.TargetValue, action: e.Action})
		}
	}

	const baseDomain = "tsnet.skynas.ru"
	usernames, err := db.GetPortalUsernames(d)
	if err != nil {
		return "", err
	}
	var identities []string
	for _, uname := range usernames {
		if uname != "" {
			identities = append(identities, uname+"@"+baseDomain)
		}
	}
	if identities == nil {
		identities = []string{}
	}

	var sb strings.Builder
	sb.WriteString("{\n  \"acls\": [\n")

	// Per-user rule: user can reach their OWN devices only.
	for i, idn := range identities {
		if i > 0 {
			sb.WriteString(",\n")
		}
		sb.WriteString("    { \"action\": \"accept\", \"src\": [\"" + idn + "\"], \"dst\": [\"" + idn + ":*\"] }")
	}

	// Informational/audit per-device exit-rules.
	for _, e := range entries {
		src := "\"*\""
		if e.deviceIP != "" {
			src = fmt.Sprintf("\"%s\"", e.deviceIP)
		}
		sb.WriteString(",\n    { \"action\": \"" + e.action + "\", \"src\": [" + src + "], \"dst\": [\"" + e.target + ":*\"] }")
	}

	sb.WriteString(",\n    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"tag:public:*\"] }")
	sb.WriteString(",\n    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"tag:exit-node:*\"] }")
	sb.WriteString(",\n    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"*:*\"] }")
	sb.WriteString("\n  ],\n")

	sb.WriteString("  \"tagOwners\": {\n")
	sb.WriteString("    \"tag:public\": [\"skyadmin@" + baseDomain + "\"]\n")
	// 2026-07-14: Этап 14 v7 — register tag:exit-node as
	// owned by skyadmin so the headscale parser accepts the
	// policy. The SSH rule (and the per-user ACL) references
	// this tag; without an entry in tagOwners the policy
	// load fails with "tag not found: tag:exit-node". We
	// never *apply* this tag through skygate (it stays as
	// a headplane admin task), but headscale still requires
	// the owner entry to be present in the policy file.
	sb.WriteString(",\n    \"tag:exit-node\": [\"skyadmin@" + baseDomain + "\"]\n")
	if len(identities) > 1 {
		sb.WriteString(",\n    \"tag:private\": [" + strings.Join(quoteAll(identities), ",") + "]\n")
	} else {
		sb.WriteString(",\n    \"tag:private\": [\"" + (identities[0]) + "\"]\n")
	}
	sb.WriteString("  },\n")

	sb.WriteString("  \"groups\": {\n")
	for i, idn := range identities {
		if i > 0 {
			sb.WriteString(",\n")
		}
		parts := strings.SplitN(idn, "@", 2)
		groupName := "group:" + parts[0]
		sb.WriteString("    \"" + groupName + "\": [\"" + idn + "\"]")
	}
	sb.WriteString("\n  },\n")

	sb.WriteString("  \"ssh\": [\n")
	sb.WriteString("    {\n")
	sb.WriteString("      \"action\": \"accept\",\n")
	sb.WriteString("      \"src\": [\"tag:private\", \"skyadmin@" + baseDomain + "\"],\n")
	sb.WriteString("      \"dst\": [\"tag:exit-node\"],\n")
	sb.WriteString("      \"users\": [\"root\"]\n")
	sb.WriteString("    },\n")
	// 2026-07-14: Этап 14 v7 — allow admin to SSH into tag:public
	// relay nodes (emilia, sharlotta, karolina) so they can be
	// reconfigured (e.g. enable --advertise-exit-node) without
	// needing direct public-IP SSH access. src is restricted to
	// the admin's identity only — no other user (tag:private
	// or otherwise) gets in. The existing tag:exit-node rule
	// above is preserved unchanged, so private devices that
	// happen to be tagged exit-node remain reachable.
	sb.WriteString("    {\n")
	sb.WriteString("      \"action\": \"accept\",\n")
	sb.WriteString("      \"src\": [\"skyadmin@" + baseDomain + "\"],\n")
	sb.WriteString("      \"dst\": [\"tag:public\"],\n")
	sb.WriteString("      \"users\": [\"root\"]\n")
	sb.WriteString("    }\n")
	sb.WriteString("  ]\n")

	sb.WriteString("}")
	return sb.String(), nil
}

func quoteAll(ss []string) []string {
	res := make([]string, len(ss))
	for i, s := range ss {
		res[i] = strconv.Quote(s)
	}
	return res
}

// SaveACLSnapshot inserts one row into acl_snapshots and returns
// the new version. The alerter is optional — pass nil to skip the
// "🛡️ ACL #N" Telegram alert (the bot path, which records the
// change in audit_log instead).
//
// Moved out of (*App).saveACLSnapshot so the telegram bot can
// reuse it.
func SaveACLSnapshot(d *sql.DB, config, username string, alerter Alerter) int {
	ver, _ := db.NextACLVersion(d)
	_ = db.SaveACLSnapshot(d, ver, config, username)
	if alerter != nil {
		// Async to avoid blocking the caller on a Telegram API
		// round-trip. Mirrors the previous (a *App) behaviour.
		go alerter.SendAlert(fmt.Sprintf("🛡️ ACL #%d by %s\nLength: %d bytes", ver, username, len(config)))
	}
	return ver
}

// ApplyResult is the typed return of ApplyACLPipeline so callers
// can branch on "applied to headscale" without juggling three
// separate return values. Err is non-nil when GenerateACL or
// SetPolicy failed; Version is the snapshot version (always set
// on the success path, may be 0 on GenerateACL failure); Applied
// is true iff SetPolicy succeeded.
type ApplyResult struct {
	Version int
	Applied bool
	Err     error
}

// ApplyACLPipeline runs the standard "rules changed, sync to
// headscale" pipeline:
//
//   1. GenerateACL          — build the policy JSON from device_rules
//   2. SaveACLSnapshot      — persist the snapshot (always, so the
//                             operator can roll back even on failure)
//   3. HS.SetPolicy         — push to headscale
//   4. MarkACLApplied/Fail  + AppendExitRuleLog
//
// detailForLog is written to exit_rule_logs on both the success
// and failure path so an operator scanning the audit trail sees
// the human-readable context that triggered the sync.
//
// The Alerter receives a Telegram alert on the SaveACLSnapshot
// step (mirroring the existing web behaviour). Pass nil to skip.
// Notifier alerts for success/failure and SyncAdvertisedRoutes
// are intentionally NOT in this helper: those are caller-specific
// (the web form does both, the bot does neither for v1) and the
// caller chains them after this function returns.
func ApplyACLPipeline(d *sql.DB, hs *headscale.Client, alerter Alerter, username, detailForLog string) ApplyResult {
	acl, err := GenerateACL(d)
	if err != nil {
		return ApplyResult{Version: 0, Applied: false, Err: fmt.Errorf("generate ACL: %w", err)}
	}
	ver := SaveACLSnapshot(d, acl, username, alerter)
	if setErr := hs.SetPolicy(acl); setErr != nil {
		db.MarkACLFail(d, ver, setErr.Error())
		db.AppendExitRuleLog(d, ver, db.ExitRuleActionApplyFail, detailForLog+": "+setErr.Error())
		return ApplyResult{Version: ver, Applied: false, Err: setErr}
	}
	db.MarkACLApplied(d, ver)
	db.AppendExitRuleLog(d, ver, db.ExitRuleActionApply, detailForLog)
	return ApplyResult{Version: ver, Applied: true, Err: nil}
}
