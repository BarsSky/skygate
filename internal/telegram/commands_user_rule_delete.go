package telegram

import (
	"fmt"
	"skygate/internal/acl"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"strconv"
	"strings"
)

func deleteRuleReply(env BotEnv, arg string) string {
	lang := env.Lang
	if !env.IsIdentified() {
		return i18n.T(lang, "bot.delrule.not_bound")
	}
	args := strings.Fields(strings.TrimSpace(arg))
	if len(args) == 0 {
		return i18n.T(lang, "bot.delrule.usage")
	}

	// Admin target: /delrule <username> <id> ... — first arg is the
	// target user (admin only), rest are rule ids. We detect
	// "username vs id" by trying strconv.Atoi on the first arg:
	// an all-digit arg is a rule id, anything else is treated as
	// a username. This avoids the admin getting tripped up when
	// their own username happens to be a positive integer.
	target := db.User{ID: env.PortalUserID, Username: env.Username, IsAdmin: env.IsAdmin}
	_, firstErr := strconv.Atoi(args[0])
	firstIsNum := firstErr == nil
	if env.IsAdmin && !firstIsNum {
		// First arg is non-numeric — treat as a username.
		u, err := lookupUserByUsername(env.DB, args[0])
		if err != nil {
			return i18n.Tf(lang, "bot.delrule.target_err", err)
		}
		target = *u
		args = args[1:]
	} else if !env.IsAdmin && !firstIsNum {
		// Non-admin: a non-numeric first arg in a multi-arg
		// command looks like an attempt to use the admin
		// <username> <id> form. Reject explicitly.
		if len(args) > 1 {
			return i18n.T(lang, "bot.delrule.extra_args")
		}
		// Single non-numeric arg: it's just a bad id — fall
		// through to the per-id validation below.
	}
	if len(args) == 0 {
		return i18n.T(lang, "bot.delrule.missing_ids")
	}

	// Parse all ids. Per-id errors are collected into `skipped`
	// so the reply can list "what we couldn't do" alongside the
	// successful deletes.
	type idJob struct {
		id           int
		targetType   string
		parentDomain string
	}
	var jobs []idJob
	var skipped []string
	for _, a := range args {
		id, err := strconv.Atoi(a)
		if err != nil || id <= 0 {
			skipped = append(skipped, fmt.Sprintf("%q (not a positive integer)", a))
			continue
		}
		// GetRuleTargetTypeAndParent filters by (id, user_id) —
		// a missing id OR a cross-user id both return ErrNotFound.
		// We surface them as "not found / not yours" so we don't
		// leak rule existence across users.
		tType, parentDomain, err := db.GetRuleTargetTypeAndParent(env.DB, id, target.ID)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%d (not found / not yours)", id))
			continue
		}
		jobs = append(jobs, idJob{id: id, targetType: tType, parentDomain: parentDomain})
	}
	if len(jobs) == 0 {
		return i18n.Tf(lang, "bot.delrule.no_valid_ids", strings.Join(skipped, ", "))
	}

	// Delete each rule and COUNT WHAT WAS ACTUALLY DELETED.
	//
	// B390 (TD-33 H6): this loop used to append every parsed id to `deletedIDs`
	// and increment the count regardless of the DELETE's result, so a failed
	// query, a rule that was already gone and a rule owned by somebody else were
	// all reported as deleted — to the user AND in the audit row. The helper
	// counts rows removed, keeps the cascade non-negative, and names the ids that
	// removed nothing.
	jobs2 := make([]ruleDeleteJob, 0, len(jobs))
	for _, j := range jobs {
		jobs2 = append(jobs2, ruleDeleteJob(j))
	}
	outcome := deleteRuleJobs(env.DB, target.ID, jobs2)
	deletedIDs := outcome.deleted
	totalCascade := outcome.cascade
	notDeleted := failedSuffix(outcome.failed)
	if len(skipped) > 0 {
		if notDeleted != "" {
			notDeleted += ", "
		}
		notDeleted += strings.Join(skipped, ", ")
	}
	if len(deletedIDs) == 0 {
		// Nothing was removed: say so and stop before the ACL pipeline runs on a
		// database that did not change.
		_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rule_deleted",
			fmt.Sprintf("via bot: deleted 0 rule(s) for %s; nothing matched: %s", target.Username, notDeleted))
		return i18n.Tf(lang, "bot.delrule.nothing_deleted", target.Username, notDeleted)
	}

	// ACL pipeline. Read-only deploys (userHS() == nil) skip
	// the pipeline — the rules are already gone, admin can
	// /admin/exit-rules/sync to push the updated policy
	// manually. 2026-07-16: v0.12.1 — uses env.userHS() so
	// the policy is pushed on the user's per-plane control
	// plane (or the global one if they have no override).
	if env.userHS() == nil {
		auditDetail := fmt.Sprintf("via bot: deleted %d rule(s) for %s (cascade: %d, ids=%v) — ACL sync skipped (read-only mode)",
			len(deletedIDs), target.Username, totalCascade, deletedIDs)
		if notDeleted != "" {
			auditDetail += fmt.Sprintf("; not deleted: %s", notDeleted)
		}
		_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rule_deleted", auditDetail)
		reply := i18n.Tf(lang, "bot.delrule.read_only_ok", len(deletedIDs), target.Username, totalCascade)
		if notDeleted != "" {
			reply += i18n.Tf(lang, "bot.delrule.not_deleted_suffix", notDeleted)
		}
		return reply
	}

	detailForLog := fmt.Sprintf("user %s deleted %d rule(s) (cascade: %d) for %s via bot",
		target.Username, len(deletedIDs), totalCascade, target.Username)
	pipe := acl.ApplyACLPipelineForPlane(env.DB, env.userHS(), env.userTargetPlaneURL(target.ID), nil, target.Username, detailForLog, false)

	// Audit log under target user. The action is rule_deleted; the
	// detail captures what was deleted + cascade count + skipped ids
	// (so an operator scanning audit_log sees the full picture).
	auditDetail := fmt.Sprintf("via bot: deleted %d rule(s) for %s (cascade: %d, ids=%v)",
		len(deletedIDs), target.Username, totalCascade, deletedIDs)
	if notDeleted != "" {
		auditDetail += fmt.Sprintf("; not deleted: %s", notDeleted)
	}
	_ = db.AppendAuditLog(env.DB, target.ID, target.Username, "rule_deleted", auditDetail)

	// Reply. Success: list deleted ids + ACL version. Failure:
	// rules deleted but ACL not applied — ask admin to sync
	// AND ping the operator via Notifier (same pattern as
	// addRuleReply) so the operator wakes up even if the
	// user doesn't notice the warning in the bot reply.
	if pipe.Applied {
		reply := i18n.Tf(lang, "bot.delrule.applied_ok", len(deletedIDs), target.Username, totalCascade, deletedIDs, pipe.Version)
		if notDeleted != "" {
			reply += i18n.Tf(lang, "bot.delrule.not_deleted_suffix", notDeleted)
		}
		return reply
	}
	if env.Notifier != nil {
		go env.Notifier.SendAlert(i18n.Tf(i18n.LangRU, "alert.acl.apply_failed_delete_ids", target.Username, deletedIDs, pipe.Err))
	}
	return i18n.Tf(lang, "bot.delrule.applied_failed", target.Username, deletedIDs, pipe.Version, pipe.Err)
}

// bindReply binds a Telegram chat_id to a portal user. Admin-only.
// The command shape is:
//
//	/bind <chat_id> <username>
//
// e.g. /bind 123456789 user1. The user gives us their chat_id
// (a positive number for a DM, negative for a group) and the
// admin pastes it in. The chat is then "theirs" — they can use
// /my_* commands and write rules for themselves.
//
// We require the admin to type the chat_id (rather than the chat
// announcing itself) so a user can't bind someone else's chat
// to their own account by guessing an admin chat.
