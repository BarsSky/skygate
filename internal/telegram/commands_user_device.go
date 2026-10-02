package telegram

import (
	"log"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"time"
)

func addDeviceReply(env BotEnv, arg string) string {
	lang := env.Lang
	if !env.IsIdentified() {
		log.Printf("bot.add_device: chat not bound (ChatID=%d)", env.ChatID)
		return i18n.T(lang, "bot.add_device.not_bound")
	}
	target, isAdminArg, err := resolveTargetUser(env, arg)
	if err != nil {
		log.Printf("bot.add_device: resolveTargetUser arg=%q err=%v", arg, err)
		return i18n.Tf(lang, "bot.add_device.target_err", err)
	}
	if isAdminArg && !env.IsAdmin {
		log.Printf("bot.add_device: non-admin tried to act on %q", target.Username)
		return i18n.T(lang, "bot.add_device.admin_only")
	}
	// 2026-07-13: Этап 11 part 1 — guard read-only deploys. SetHS is
	// called from main.go so HS is non-nil in production; the check
	// exists so a future operator who restarts skygate without
	// SetHS sees a clear error rather than a nil-deref panic.
	// 2026-07-16: v0.12.1 — uses env.userHS() so the preauth key
	// is issued on the bound user's per-plane control plane
	// (or the global one if they have no override).
	if env.userHS() == nil {
		log.Printf("bot.add_device: userHS() is nil (read-only deploy?)")
		return i18n.T(lang, "bot.add_device.read_only")
	}
	hsUserID, _, err := db.GetUserHSByID(env.DB, target.ID)
	if err != nil {
		log.Printf("bot.add_device: GetUserHSByID userID=%d err=%v", target.ID, err)
		return i18n.Tf(lang, "bot.add_device.no_hs_user", target.Username)
	}
	if !hsUserID.Valid {
		log.Printf("bot.add_device: no headscale_user_id for userID=%d username=%q", target.ID, target.Username)
		return i18n.Tf(lang, "bot.add_device.no_hs_user", target.Username)
	}
	log.Printf("bot.add_device: target=%q hsUserID=%d, calling CreatePreauthKey on plane", target.Username, hsUserID.Int64)
	key, err := env.userHS().CreatePreauthKey(hsUserID.Int64, "1h", false)
	if err != nil {
		log.Printf("bot.add_device: CreatePreauthKey userID=%d err=%v", hsUserID.Int64, err)
		return i18n.Tf(lang, "bot.add_device.hs_failed", err)
	}
	log.Printf("bot.add_device: got key from HS, prefix=%q, calling InsertPreauthKey", key.Key[:min(20, len(key.Key))])
	expiresAt := time.Now().Add(time.Hour).Unix()
	if _, err := db.InsertPreauthKey(env.DB, target.ID, key.Key, expiresAt, key.ID); err != nil {
		log.Printf("bot.add_device: InsertPreauthKey userID=%d err=%v", target.ID, err)
		return i18n.Tf(lang, "bot.add_device.persist_failed", err)
	}
	if err := db.AppendAuditLog(env.DB, target.ID, target.Username, "preauth_issued", "1h single-use (via bot)"); err != nil {
		log.Printf("bot.add_device: AppendAuditLog userID=%d err=%v", target.ID, err)
		return i18n.Tf(lang, "bot.add_device.audit_failed", err)
	}
	log.Printf("bot.add_device: success userID=%d, setting pendingReplyForCurrentMessage", target.ID)
	// Set the pending reply with platform picker. The polling
	// loop reads pendingReplyForCurrentMessage after this
	// returns and attaches the inline keyboard to the
	// sendMessage payload. The picker includes a 📋 Copy
	// button (Telegram copy_text field) so the user can copy
	// the preauth key to the clipboard without long-pressing
	// the code block. After the user picks a platform, the
	// callback handler in notify.go renders the per-platform
	// install instructions.
	pendingReplyForCurrentMessage = buildPlatformPicker(lang, key.Key)
	// 2026-07-16: v0.15.2 — butler-voice gate envelope with
	// parse_mode=HTML (the picker sets that on the
	// inline_keyboard). The reply is wrapped in
	// "🪶 ═══ Skygate ═══ … ═══ — Ваш Дворецкий ═══"
	// with time-of-day greeting, title in <b>, subheader
	// in <blockquote>, the key in <pre>, and a next-steps
	// hint in <i>. cmdReply{skipWrap: true} in
	// dispatchCommand tells HandleCommand to skip the v1
	// Compose() wrapper so we don't get two gate envelopes
	// stacked.
	return butlerEnvelope(
		lang, target.Username,
		i18n.T(lang, "bot.add_device.title"),
		i18n.T(lang, "bot.add_device.subheader"),
		"<pre>"+escapeHTML(key.Key)+"</pre>",
		i18n.T(lang, "bot.add_device.footer"),
		WithIcon("🔑"),
		// 2026-07-16: v0.15.5 — preauth keys are
		// security-sensitive. Mark the reply warning so
		// the operator sees 🔑! in the chat list and
		// knows the body has a credential to act on.
		WithUrgency(UrgencyWarning),
	)
}

// addRuleReply adds a new exit-rule for the caller (or, for admins,
// for a named user).
//
// The argument grammar is intentionally simple:
//
//	/add_rule <target>                → action=accept (uses defaults)
//	/add_rule <target> deny           → action=deny (uses defaults)
//	/add_rule <username> <target>     → admin-only: add for that user
//
// "Defaults" = the user's /setdefaultdevice + /setexitnode
// preferences (Этап 11 part 2a). The bot refuses to add a rule
// if either default is unset, so the user is forced to pick
// their device + exit-node explicitly before they start writing
// rules — matches the web form's device_id + exit_node
// selectors, just in a "set once, reuse" shape.
//
// 2026-07-13: Этап 11 part 2b — real write. Mirrors
// handlers/exit_rules_form_my.go:PostMyExitRule:
//
//  1. Read defaults (device_node_id, exit_node_id) for the
//     target user.
//  2. Validate defaults are still current (device in
//     node_owner_map, exit-node still enabled in exit_servers).
//  3. Per-user / per-device / total rule-limit check.
//  4. DNS resolve for domains → split into /32 subnets
//     (Tailscale ACLs work at L3/L4, not L7 — domains are
//     resolved to IPs and pinned as /32 subnets).
//  5. Insert rule(s) into device_rules.
//  6. acl.ApplyACLPipeline → GenerateACL → SetPolicy →
//     MarkACLApplied/Fail + AppendExitRuleLog.
//  7. audit_log row under the *target* user (so per-user
//     audit views stay correct).
//
// The bot skips the per-rule SyncAdvertisedRoutes call (admin
// can trigger via /admin/exit-rules/sync) and the Telegram
// Notifier alert (audit_log is the bot's audit trail).
