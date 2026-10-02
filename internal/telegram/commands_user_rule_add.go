package telegram

import (
	"fmt"
	"net"
	"skygate/internal/acl"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"strconv"
	"strings"
)

func addRuleReply(env BotEnv, args []string) string {
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.add_rule.not_bound")
	}
	if len(args) == 0 {
		return i18n.T(lang, "bot.add_rule.usage")
	}

	// Pull off a possible trailing "deny" / "accept".
	action := "accept"
	last := args[len(args)-1]
	switch strings.ToLower(last) {
	case "deny", "block", "reject":
		action = "deny"
		args = args[:len(args)-1]
	case "accept", "allow":
		action = "accept"
		args = args[:len(args)-1]
	}
	if len(args) == 0 {
		return i18n.T(lang, "bot.add_rule.missing_action_target")
	}

	// Admin target: /add_rule <username> <target> [...]
	// Two args + admin = username is first; otherwise the
	// first arg is the target. We don't use resolveTargetUser
	// here because that helper wants a single string; we
	// already have args[0] / args[1] split.
	target := db.User{ID: env.PortalUserID, Username: env.Username, IsAdmin: env.IsAdmin}
	if len(args) >= 2 && env.IsAdmin {
		u, err := lookupUserByUsername(env.DB, args[0])
		if err != nil {
			return i18n.Tf(lang, "bot.add_rule.target_err", err)
		}
		target = *u
		args = args[1:]
	} else if len(args) >= 2 && !env.IsAdmin {
		return i18n.T(lang, "bot.add_rule.extra_args")
	}
	if len(args) == 0 {
		return i18n.T(lang, "bot.add_rule.missing_target")
	}

	value, _, err := classifyTarget(args[0])
	if err != nil {
		return i18n.Tf(lang, "bot.add_rule.target_invalid", err)
	}

	// Read defaults.
	deviceNodeID, err := db.GetDefaultDevice(env.DB, target.ID)
	if err != nil || deviceNodeID == "" {
		return i18n.T(lang, "bot.add_rule.no_default_device")
	}
	exitNodeNodeID, err := db.GetDefaultExitNode(env.DB, target.ID)
	if err != nil || exitNodeNodeID == "" {
		return i18n.T(lang, "bot.add_rule.no_default_exit")
	}

	// Validate defaults are still current. The default columns
	// are TEXT pointers into node_owner_map / exit_servers;
	// those rows can disappear (device removed from tailnet,
	// exit-server disabled) and the default becomes stale.
	// We re-check on every insert so a rule never lands with
	// a dead device or disabled exit-node.
	var deviceIP string
	if env.userHS() != nil {
		if nodes, err := env.userHS().ListAllNodes(); err == nil {
			for _, n := range nodes {
				if n.ID == deviceNodeID {
					if len(n.IPAddresses) > 0 {
						deviceIP = n.IPAddresses[0]
					}
					break
				}
			}
		}
	}
	deviceOwned, err := db.CountNodeOwnerByNodeUser(env.DB, deviceNodeID, target.Username)
	if err != nil || deviceOwned == 0 {
		return i18n.Tf(lang, "bot.add_rule.stale_device", deviceNodeID, target.Username)
	}
	// device_id: device_rules.device_id is INT, default column
	// is TEXT (node_id). headscale node_ids are always numeric,
	// so Atoi is safe; we surface a clear error otherwise.
	devID, err := strconv.Atoi(deviceNodeID)
	if err != nil {
		return i18n.Tf(lang, "bot.add_rule.bad_device_id", deviceNodeID, err)
	}

	// Resolve the exit-node hostname. device_rules.exit_node_id
	// stores the hostname (matches what the web form inserts);
	// the default column stores the node_id. The lookup is a
	// single indexed read against exit_servers. We also check
	// enabled=1 because the user might have picked an
	// exit-server that the admin later disabled — the default
	// is then stale and we should refuse to insert a rule
	// pointing at a disabled server.
	var exitNodeHostname string
	var exitNodeEnabled int
	err = env.DB.QueryRow(
		`SELECT COALESCE(hostname, ''), COALESCE(enabled, 0)
		   FROM exit_servers WHERE node_id = $1`,
		exitNodeNodeID,
	).Scan(&exitNodeHostname, &exitNodeEnabled)
	if err != nil || exitNodeHostname == "" {
		return i18n.Tf(lang, "bot.add_rule.stale_exit_node", exitNodeNodeID)
	}
	if exitNodeEnabled == 0 {
		return i18n.Tf(lang, "bot.add_rule.disabled_exit_node", exitNodeNodeID, exitNodeHostname)
	}

	// Per-user / per-device / total rule-limit checks. Same
	// counts the web form uses (CountEnabledNonSubnetRules*).
	maxPerUser := env.MaxFor(target.Username)
	if maxPerUser > 0 {
		cnt, _ := db.CountEnabledNonSubnetRulesForUser(env.DB, target.ID)
		if cnt >= maxPerUser {
			return i18n.Tf(lang, "bot.add_rule.user_limit", cnt, maxPerUser, target.Username)
		}
	}
	if env.MaxRulesPerDevice > 0 {
		cnt, _ := db.CountEnabledNonSubnetRulesForUserDevice(env.DB, target.ID, devID)
		if cnt >= env.MaxRulesPerDevice {
			return i18n.Tf(lang, "bot.add_rule.device_limit", cnt, env.MaxRulesPerDevice, devID)
		}
	}
	if env.MaxTotalRules > 0 {
		cnt, _ := db.CountEnabledRules(env.DB)
		if cnt >= env.MaxTotalRules {
			return i18n.Tf(lang, "bot.add_rule.system_limit", cnt, env.MaxTotalRules)
		}
	}

	// Classify + DNS resolve. Mirrors the web form: domains get
	// resolved to A records and inserted as /32 subnets
	// (Tailscale advertises routes as CIDR, not bare IPs).
	// If DNS fails, the bot still inserts the original target
	// as target_type=domain so the autoupdater can retry later.
	dnsWarning := ""
	ipsToInsert := []string{value}
	typeToInsert := "ip"
	if strings.Contains(value, "/") {
		typeToInsert = "subnet"
	}
	// Reclassify "domain" targets by looking at the raw arg.
	rawTarget := args[0]
	if !strings.Contains(rawTarget, "/") && !isIPLiteral(rawTarget) {
		// Domain.
		typeToInsert = "subnet"
		if addrs, err := net.LookupHost(rawTarget); err == nil {
			ipsToInsert = nil
			seen := map[string]bool{}
			for _, a := range addrs {
				if strings.Contains(a, ":") {
					continue
				}
				if seen[a] {
					continue
				}
				seen[a] = true
				ipsToInsert = append(ipsToInsert, a+"/32")
			}
			if len(ipsToInsert) == 0 {
				// Domain resolved only to IPv6 — fall back
				// to storing the bare domain so the
				// autoupdater retries A records later.
				typeToInsert = "domain"
				ipsToInsert = []string{rawTarget}
			}
		} else {
			dnsWarning = i18n.Tf(lang, "bot.add_rule.dns_warning_prefix", rawTarget, err)
			typeToInsert = "domain"
			ipsToInsert = []string{rawTarget}
		}
	} else if typeToInsert == "ip" && !strings.Contains(value, "/") {
		// Bare IP → add /32 so Tailscale accepts it as a
		// CIDR route.
		ipsToInsert = []string{value + "/32"}
		typeToInsert = "subnet"
	}

	// Save the parent domain (target_type=domain) so the
	// autoupdater can track it and add knownSubdomains.
	parentDomain := ""
	if typeToInsert == "domain" {
		parentDomain = rawTarget
	}

	// Insert the rules. The web form does dedup via
	// FindDeviceRuleID + AppendDeviceRule; the bot skips the
	// dedup check for v1 (admin can clean up duplicates later
	// via /admin/exit-rules/cleanup). One insert per IP.
	var insertedIDs []int64
	for _, ip := range ipsToInsert {
		// v0.28.0: pass target.Username as userName. deviceHostname is
		// empty here — the bot only has the deviceIP at this point —
		// and is backfilled by the next /my/devices load (which now
		// writes hostname into device_rules via a follow-up UPDATE).
		// The ACL builder handles empty hostname by falling back to
		// src=device_ip for that one rule, so the rule is live
		// immediately and switches to the tag-based src after the
		// backfill lands.
		rowID, err := db.AppendDeviceRule(env.DB, target.ID, devID, exitNodeHostname, typeToInsert, ip, action, deviceIP, parentDomain, target.Username, "")
		if err != nil {
			return i18n.Tf(lang, "bot.add_rule.db_error", err)
		}
		insertedIDs = append(insertedIDs, rowID)
	}

	// Apply ACL pipeline. The pipeline ALWAYS saves the
	// snapshot (even on SetPolicy failure) so the operator
	// can roll back. We pass nil for the Alerter — the bot
	// audit_log row is the bot's audit trail; an extra
	// Telegram ping per /add_rule would be noise.
	//
	// 2026-07-13: Этап 13 follow-up — read-only guard.
	// /delrule and /clearrules already skip the pipeline
	// when env.HS == nil (read-only deploy). This brings
	// /add_rule in line with them: insert the rules + audit,
	// but skip the headscale.SetPolicy call (which would
	// nil-deref) and tell the user to ask an admin to sync.
	// This also matches addDeviceReply's "telegram not wired
	// for writes" guard pattern.
	detailForLog := fmt.Sprintf("user %s added rule(s) (type=%s target=%s exit=%s) for %s via bot",
		target.Username, typeToInsert, rawTarget, exitNodeHostname, target.Username)
	if env.userHS() == nil {
		auditDetail := fmt.Sprintf("via bot: %s %s → %s (exit=%s, action=%s, ids=%v) — ACL sync skipped (read-only mode)",
			typeToInsert, rawTarget, exitNodeHostname, exitNodeHostname, action, insertedIDs)
		if dnsWarning != "" {
			auditDetail += "; " + dnsWarning
		}
		_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rule_added", auditDetail)
		reply := i18n.Tf(lang, "bot.add_rule.read_only_ok", len(insertedIDs), target.Username, insertedIDs)
		if dnsWarning != "" {
			reply += "\n  ⚠ " + dnsWarning
		}
		return reply
	}
	pipe := acl.ApplyACLPipelineForPlane(env.DB, env.userHS(), env.userTargetPlaneURL(target.ID), nil, target.Username, detailForLog, false)

	// Audit log (under the target user, so per-user audit
	// views stay correct). The action is rule_added; the
	// detail captures what was added and which exit-node.
	auditDetail := fmt.Sprintf("via bot: %s %s → %s (exit=%s, action=%s, ids=%v)",
		typeToInsert, rawTarget, exitNodeHostname, exitNodeHostname, action, insertedIDs)
	if dnsWarning != "" {
		auditDetail += "; " + dnsWarning
	}
	_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rule_added", auditDetail)

	// Reply. Success case: list the inserted ids + the
	// ACL version that was applied. SetPolicy failure case:
	// the snapshot is still saved — call it out so the user
	// knows to ask an admin to retry the sync. ALSO send a
	// Telegram alert to the operator (the "🛡️ ACL" alert
	// fires on snapshot save; the "❌ ACL apply failed" alert
	// fires here, in the failure-only branch) so the
	// operator wakes up even if the user doesn't notice
	// the warning in the bot reply.
	if pipe.Applied {
		reply := i18n.Tf(lang, "bot.add_rule.applied_ok", len(insertedIDs), target.Username, typeToInsert, rawTarget, action, exitNodeHostname, insertedIDs, pipe.Version)
		if dnsWarning != "" {
			reply += "\n  ⚠ " + dnsWarning
		}
		return reply
	}
	// SetPolicy failed — ping the operator via the same
	// Notifier that /ack uses. Async so the bot reply
	// isn't blocked on the Telegram API call.
	if env.Notifier != nil {
		go env.Notifier.SendAlert(fmt.Sprintf("❌ ACL apply failed (rule by %s)\n  target: %s %s\n  err: %v",
			target.Username, typeToInsert, rawTarget, pipe.Err))
	}
	return i18n.Tf(lang, "bot.add_rule.applied_failed", target.Username, typeToInsert, rawTarget, insertedIDs, pipe.Version, pipe.Err)
}

// deleteRuleReply removes one or more of the caller's own rules by id.
// Cross-user is rejected: a regular user can only delete rules
// where user_id = env.PortalUserID. Admin users can delete another
// user's rule via the optional <username> prefix.
//
// The function is named deleteRuleReply (the historical name from
// when the only command was /delete_rule); it powers BOTH /delrule
// (the new short form, primary) AND /delete_rule (deprecated alias,
// kept for back-compat with the original /help text). HandleCommand
// routes both commands to this function.
//
// Grammar:
//
//	/delrule <id>                  — delete one rule
//	/delrule <id1> <id2> <id3>     — delete multiple (whitespace-separated)
//	/delrule <username> <id> ...   — admin only: delete for that user
//	/delete_rule <id>              — same (deprecated alias)
//
// 2026-07-13: Этап 12 — real write. Mirrors
// handlers/exit_rules_form_my.go:PostDeleteExitRule:
//
//  1. For each id: GetRuleTargetTypeAndParent verifies ownership
//     (the helper's WHERE filters by user_id, so a non-owned id
//     returns ErrNotFound — we surface both "missing" and
//     "not yours" as "not found / not yours" to avoid leaking
//     rule existence across users).
//  2. If target_type=domain + parent_domain: DeleteRuleOrCascadeByParentDomain
//     deletes the rule + any sibling /32 entries with the same
//     parent_domain (autoupdater-derived entries).
//  3. Else: DeleteRuleForUser deletes the single row.
//  4. acl.ApplyACLPipeline → GenerateACL → SetPolicy → Mark+Log.
//  5. audit_log row under the *target* user (so per-user audit
//     views stay correct).
//
// We collect per-id errors so the user gets a full report of "what
// was skipped" rather than failing on the first bad id. Multi-id
// deletes are best-effort: if SOME ids are valid we still process
// them and only fail completely when NO id is valid.
//
// Read-only deploys (env.HS == nil) get a guard: the DB delete
// still runs but the ACL pipeline is skipped with a clear hint
// ("ACL sync skipped — ask admin to /admin/exit-rules/sync"). This
// is a small improvement over addRuleReply (which would crash on
// nil HS); the same guard should be backported to addRuleReply in
// a follow-up.
//
// The bot skips the per-rule SyncAdvertisedRoutes call and the
// Telegram Notifier alert that the web form does — admin can
// trigger sync via /admin/exit-rules/sync, and audit_log is the
// bot's audit trail.
