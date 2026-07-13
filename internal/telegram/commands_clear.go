// Package telegram — /clearrules (Этап 13, 2026-07-13).
//
// Nuclear option: deletes ALL rules for a user. Two-phase confirmation
// (mint + confirm) mirrors /restart: the first call counts rules and
// shows a sample, the second call executes the wipe.
//
// Grammar:
//
//	/clearrules                  — mint for caller; reply shows count
//	                              + sample of rules that will go away,
//	                              asks for /clearrules confirm within 30s
//	/clearrules confirm          — consume pending request, wipe
//	/clearrules <username>       — admin only: mint for that user
//	/clearrules <username> confirm — admin only: confirm wipe for that user
//
// The two-phase pattern protects against a fat-fingered /clearrules
// on the wrong chat — the user has to consciously type a second
// message within 30s to commit. The 30s TTL matches /restart's
// restartTTL so the operator's mental model is "one pending token
// per chat, type fast".
//
// pendingClears is keyed by chat_id (NOT by token) because /clearrules
// has no token — the chat_id itself is the implicit confirmation
// handle. This means a user can only have ONE pending clear at a
// time; minting a new one (e.g. switching from self to another user
// as admin) overwrites the previous one. That matches the user's
// intent: "I just changed my mind" naturally maps to "mint again".
//
// Per-rule cascade is the same as /delrule: domain rules cascade to
// /32 siblings (autoupdater-derived) via
// db.DeleteRuleOrCascadeByParentDomain; non-domain rules use
// db.DeleteRuleForUser. The cascade count is reported in the reply
// so the operator knows "cleared N rules, of which K were /32 fan-out
// of M domains".
//
// The full pipeline (DB wipe → ACL regen → SetPolicy → log + audit)
// runs after the wipe. Read-only deploys (env.HS == nil) get the
// same guard /delrule has: DB delete still runs, ACL pipeline is
// skipped with a clear "ask admin to /admin/exit-rules/sync" hint.

package telegram

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"skygate/internal/acl"
	"skygate/internal/db"
)

// clearTTL is how long a freshly-minted /clearrules confirmation
// stays valid. Matches /restart's restartTTL (30s) so the
// operator's mental model is "one pending confirmation per chat,
// expires fast enough to be reusable but short enough to not leave
// a foot-gun around". 30s is enough for a human to type the second
// message; tight enough that a leaked chat can't be used later.
const clearTTL = 30 * time.Second

// clearRequest is the value stored in pendingClears. Username is
// the target (may be the caller OR another user for admins);
// expiry drives the TTL check. The struct is small enough that
// boxing it into a sync.Map value is fine.
type clearRequest struct {
	username string
	expiry   time.Time
}

// pendingClears holds unconsumed /clearrules confirmations. Key:
// chat_id (int64). Value: clearRequest.
//
// sync.Map is fine — the map is small (one entry per active
// confirmation per chat) and reads are common (we check on every
// /clearrules confirm). The /restart pattern uses the same shape
// keyed by token; we use chat_id here because /clearrules has no
// explicit token (the chat identity IS the token).
var pendingClears sync.Map

// clearRulesReply is the entry point for /clearrules. Two-phase:
// phase 1 mints a pending request, phase 2 confirms and wipes.
//
// Grammar (3 valid shapes + 2 admin-only):
//
//	/clearrules                        — mint for caller
//	/clearrules confirm                — confirm last mint for this chat
//	/clearrules <username>             — admin: mint for that user
//	/clearrules <username> confirm     — admin: confirm for that user
//
// dispatch decides mint vs confirm and (if admin) the target
// username. The dispatch is whitespace-tokenized so
// "/clearrules alice confirm" is parsed as (mint-target, confirm).
func clearRulesReply(env BotEnv, arg string) string {
	if !env.IsIdentified() {
		return "clearrules: chat not bound to a portal user. Ask an admin to /bind your chat_id."
	}
	parts := strings.Fields(strings.TrimSpace(arg))

	// Dispatch.
	//
	//   parts=0  → mint for caller
	//   parts=1  → "confirm" → confirm; else admin mint for that user
	//   parts=2  → "<user> confirm" → admin confirm for that user
	//   parts=3+ → usage hint
	var (
		doConfirm   bool
		targetName  string // "" = caller; non-empty = admin-specified
	)
	switch len(parts) {
	case 0:
		// mint for caller
	case 1:
		if strings.EqualFold(parts[0], "confirm") {
			doConfirm = true
		} else {
			if !env.IsAdmin {
				return "clearrules: extra args (admin-only: /clearrules <username>). Drop the username to clear your own rules."
			}
			targetName = parts[0]
		}
	case 2:
		if !env.IsAdmin {
			return "clearrules: extra args (admin-only: /clearrules <username> confirm)"
		}
		if !strings.EqualFold(parts[1], "confirm") {
			return "clearrules: usage: /clearrules [username] [confirm]"
		}
		doConfirm = true
		targetName = parts[0]
	default:
		return "clearrules: usage: /clearrules [username] [confirm]"
	}

	if doConfirm {
		return confirmClearRules(env, targetName)
	}
	return mintClearRules(env, targetName)
}

// mintClearRules is phase 1. It looks up the target user (caller
// or admin-specified), counts their rules (with a top-10 sample),
// mints a pending request under env.ChatID, and asks for
// confirmation. The "no rules" branch is a no-op (no pending
// stored) so a follow-up /clearrules confirm doesn't accidentally
// wipe rules that the user never confirmed.
func mintClearRules(env BotEnv, targetName string) string {
	target := db.User{ID: env.PortalUserID, Username: env.Username, IsAdmin: env.IsAdmin}
	if targetName != "" {
		u, err := lookupUserByUsername(env.DB, targetName)
		if err != nil {
			return fmt.Sprintf("clearrules: %v (admin can target another user with: /clearrules <username>)", err)
		}
		target = *u
	}

	cnt, samples, err := countAndSampleUserRules(env.DB, target.ID)
	if err != nil {
		return fmt.Sprintf("clearrules: db error: %v", err)
	}
	if cnt == 0 {
		return fmt.Sprintf("clearrules: %s has no exit-rules. Nothing to clear.", target.Username)
	}

	// Mint pending request. Overwrites any previous pending clear
	// for this chat (matches /restart: "the most recent mint
	// wins" — explicit re-mint, no stale confirms).
	pendingClears.Store(env.ChatID, clearRequest{
		username: target.Username,
		expiry:   time.Now().Add(clearTTL),
	})

	// Audit the REQUEST (not the action; the action gets a
	// separate row when /clearrules confirm fires). Lets an
	// operator see "alice tried to clear her rules at T" even if
	// she didn't follow through.
	_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rules_clear_requested",
		fmt.Sprintf("via bot: %d rule(s) pending confirm from chat %d", cnt, env.ChatID))

	var sb strings.Builder
	fmt.Fprintf(&sb, "clearrules: this will delete ALL %d rule(s) for %s:\n", cnt, target.Username)
	for _, s := range samples {
		sb.WriteString("  • " + s + "\n")
	}
	if cnt > len(samples) {
		fmt.Fprintf(&sb, "  ... (%d more)\n", cnt-len(samples))
	}
	fmt.Fprintf(&sb, "\nSend /clearrules confirm within %s to proceed.\n", clearTTL)
	sb.WriteString("(ignored if the request is older than the TTL, or the chat mints a new target)")
	return trimForTelegram(sb.String())
}

// confirmClearRules is the second phase of /clearrules. It looks up
// the pending request for env.ChatID, checks the TTL, fetches the
// (id, target_type, parent_domain) of every rule the user owns,
// deletes them (with cascade for domain rules), and runs the ACL
// pipeline. The request is consumed exactly once — successful,
// expired, or malformed — so a second /clearrules confirm without
// a fresh mint returns "no pending clear request".
//
// expectedUsername is "" when the user typed /clearrules confirm
// (use whatever's in the pending request) or a username when an
// admin typed /clearrules <user> confirm (verify the pending
// matches; reject if it doesn't, so a chat with a stale pending
// can't accidentally wipe the wrong user).
func confirmClearRules(env BotEnv, expectedUsername string) string {
	v, ok := pendingClears.Load(env.ChatID)
	if !ok {
		return "clearrules: no pending clear request for this chat. Send /clearrules to mint one."
	}
	req, ok := v.(clearRequest)
	if !ok {
		// Defensive: shouldn't happen — we only ever store
		// clearRequest — but treat a malformed entry as
		// "expired" rather than panic. A subsequent mint
		// will overwrite the bad entry.
		pendingClears.Delete(env.ChatID)
		return "clearrules: pending clear store is corrupted; mint a new one with /clearrules"
	}
	if time.Now().After(req.expiry) {
		pendingClears.Delete(env.ChatID)
		return fmt.Sprintf("clearrules: pending clear expired (>%s old); mint a new one with /clearrules", clearTTL)
	}
	// Admin safety check: if the admin typed "/clearrules alice
	// confirm" and the pending is for "bob", refuse. This stops
	// a typo or a race (two mints in a row) from wiping the
	// wrong user.
	if expectedUsername != "" && !strings.EqualFold(req.username, expectedUsername) {
		return fmt.Sprintf("clearrules: pending clear is for %q, not %q. Mint a new one with /clearrules %s",
			req.username, expectedUsername, expectedUsername)
	}

	// Resolve the target user from the stored username. If the
	// user row disappeared between mint and confirm (e.g. admin
	// deleted the user), we surface the error rather than wipe
	// the wrong account.
	target, err := lookupUserByUsername(env.DB, req.username)
	if err != nil {
		pendingClears.Delete(env.ChatID)
		return fmt.Sprintf("clearrules: target user %q no longer exists; mint a new one with /clearrules", req.username)
	}

	// Fetch every rule (id, target_type, parent_domain) for
	// the target user. We do this BEFORE the delete so the
	// cascade logic (domain → /32 siblings) can run per-row.
	// A simpler "DELETE FROM device_rules WHERE user_id = ?"
	// would skip the cascade and leave orphan /32 rows around
	// for domain rules.
	rows, err := env.DB.Query(
		`SELECT id, target_type, COALESCE(parent_domain, '')
		   FROM device_rules WHERE user_id = ?`, target.ID)
	if err != nil {
		return fmt.Sprintf("clearrules: db error: %v", err)
	}
	type ruleInfo struct {
		id           int
		targetType   string
		parentDomain string
	}
	var rules []ruleInfo
	for rows.Next() {
		var r ruleInfo
		if err := rows.Scan(&r.id, &r.targetType, &r.parentDomain); err != nil {
			rows.Close()
			return fmt.Sprintf("clearrules: scan error: %v", err)
		}
		rules = append(rules, r)
	}
	rows.Close()

	// Consume the pending request exactly once. We do this
	// BEFORE the delete + pipeline so a failure in the delete
	// step still prevents a second confirm from re-running the
	// same wipe. The trade-off: on a partial failure, the user
	// must re-mint to retry. That's fine — the rules are
	// already partially gone; re-minting makes the intent
	// explicit.
	pendingClears.Delete(env.ChatID)

	if len(rules) == 0 {
		// Edge case: rules were cleared between mint and
		// confirm (e.g. autoupdater removed them, or another
		// /delrule fired). No work to do.
		_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rules_cleared",
			"via bot: no rules to clear (already empty at confirm time)")
		return "clearrules: nothing to do — rules were already empty at confirm time."
	}

	// Delete each rule with cascade for domain rules. Cascade
	// count = rows_affected - 1 (the "extra" /32s beyond the
	// row we asked to delete).
	deleted := 0
	totalCascade := 0
	for _, r := range rules {
		if r.targetType == "domain" && r.parentDomain != "" {
			n, err := db.DeleteRuleOrCascadeByParentDomain(env.DB, target.ID, r.id, r.parentDomain)
			if err == nil {
				totalCascade += int(n) - 1
			}
		} else {
			_ = db.DeleteRuleForUser(env.DB, r.id, target.ID)
		}
		deleted++
	}

	// ACL pipeline. Read-only deploys (HS == nil) skip the
	// pipeline — the rules are already gone, admin can
	// /admin/exit-rules/sync to push the updated policy
	// manually. Same guard as /delrule.
	if env.HS == nil {
		auditDetail := fmt.Sprintf("via bot: cleared all %d rule(s) for %s (cascade: %d) — ACL sync skipped (read-only mode)",
			deleted, target.Username, totalCascade)
		_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rules_cleared", auditDetail)
		return fmt.Sprintf("clearrules: ✓ removed %d rule(s) for %s (cascade: %d). ACL sync skipped (read-only mode) — ask admin to /admin/exit-rules/sync.",
			deleted, target.Username, totalCascade)
	}

	detailForLog := fmt.Sprintf("user %s cleared all %d rule(s) (cascade: %d) for %s via bot",
		env.Username, deleted, totalCascade, target.Username)
	pipe := acl.ApplyACLPipeline(env.DB, env.HS, nil, env.Username, detailForLog)

	// Audit the actual action (separate from the request row
	// written in the mint phase).
	auditDetail := fmt.Sprintf("via bot: cleared all %d rule(s) for %s (cascade: %d)",
		deleted, target.Username, totalCascade)
	_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rules_cleared", auditDetail)

	// Reply. Success: list count + cascade + ACL version.
	// Failure: rules deleted but ACL not applied — ask admin
	// to sync.
	if pipe.Applied {
		return fmt.Sprintf("clearrules: ✓ cleared %d rule(s) for %s (cascade: %d)\n  ACL v%d applied to headscale",
			deleted, target.Username, totalCascade, pipe.Version)
	}
	return fmt.Sprintf("clearrules: ⚠ all %d rule(s) deleted from DB for %s (cascade: %d) but ACL v%d was NOT applied to headscale: %v\nAsk an admin to /admin/exit-rules/sync.",
		deleted, target.Username, totalCascade, pipe.Version, pipe.Err)
}

// countAndSampleUserRules returns (total count, top 10 sample
// strings) for the target user's rules. Sample is "id target_value"
// pairs ordered by id DESC. Used by the /clearrules mint phase to
// show the user what they're about to delete without having to
// /my_rules first.
//
// Returns an error from the COUNT query if it fails; the SELECT
// failure falls through to an empty sample (the count alone is
// enough to drive the reply).
func countAndSampleUserRules(d *sql.DB, userID int64) (int, []string, error) {
	var cnt int
	if err := d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = ?`, userID).Scan(&cnt); err != nil {
		return 0, nil, err
	}
	if cnt == 0 {
		return 0, nil, nil
	}
	rows, err := d.Query(
		`SELECT id, target_value FROM device_rules WHERE user_id = ? ORDER BY id DESC LIMIT 10`, userID)
	if err != nil {
		return cnt, nil, nil // count is fine; sample is best-effort
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return cnt, out, nil
		}
		out = append(out, fmt.Sprintf("#%d %s", id, v))
	}
	return cnt, out, nil
}
