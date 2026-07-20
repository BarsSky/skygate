// 2026-07-20: v0.21.0 — invite bot commands.
//
// Three new bot commands round out the
// user-to-user subnet bridge:
//
//   /invite <username>            — grantor-side:
//                                    generate a code
//                                    valid for 7d.
//   /accept <code>                — grantee-side:
//                                    consume a code,
//                                    auto-bridge.
//   /invites                       — list the
//                                    caller's
//                                    outstanding
//                                    / consumed
//                                    invites.
//
// All three are user-scope (any identified
// user can use them — the v0.17.1
// admin-mediated share is the alternative for
// admin-only paths). The grantor doesn't have
// to be an admin; the grantee doesn't have to
// be an admin. The bridge row is written the
// same way the admin share would write it.
//
// The "consume + auto-reapply ACL" hot path is
// delegated to internal/invite.ApplyBridge,
// which wraps the user_subnet_shares INSERT
// and the per-plane ACL re-apply goroutine.
// The bot reply is fast (the ACL re-apply
// runs in the background).

package telegram

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/invite"
)

// inviteReply handles /invite <username>.
// Returns the generated code + the grantee
// username + the expiry time. Admins get a
// tabular format; users get the same (the
// command is identical for both — there's no
// admin-only variant).
func inviteReply(env BotEnv, args []string) string {
	markHTMLReply()
	lang := env.Lang

	if len(args) == 0 {
		return i18n.T(lang, "bot.invite.usage_invite")
	}
	grantee := strings.TrimSpace(args[0])
	if !validUsername(grantee) {
		return i18n.T(lang, "bot.invite.bad_username")
	}
	if grantee == env.Username {
		return i18n.T(lang, "bot.invite.self_invite")
	}

	// Create the invite. The grantee_username
	// is stored as-typed; resolution to a user
	// id happens at consume time (so A can
	// invite "bob" before bob signs up).
	inv, err := invite.CreateInvite(env.DB, env.PortalUserID, grantee, 0, "")
	if err != nil {
		return i18n.Tf(lang, "bot.invite.create_failed", err.Error())
	}

	// The grantor may also have existing
	// outstanding invites to the same user —
	// we surface the count so the grantor
	// doesn't spam-create duplicates.
	existing, _ := invite.ListByGrantor(env.DB, env.PortalUserID)
	count := 0
	for _, e := range existing {
		if e.GranteeUsername == grantee && e.Status == invite.StatusActive {
			count++
		}
	}

	expiryStr := inv.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
	body := i18n.Tf(lang, "bot.invite.generated",
		grantee, inv.Code, expiryStr, count)
	return body
}

// acceptReply handles /accept <code>.
// Validates the code, then atomically
// consumes it and applies the bridge (writes
// the user_subnet_shares row + triggers the
// per-plane ACL re-apply).
func acceptReply(env BotEnv, args []string) string {
	markHTMLReply()
	lang := env.Lang

	if len(args) == 0 {
		return i18n.T(lang, "bot.invite.usage_accept")
	}
	code := strings.TrimSpace(args[0])
	if code == "" {
		return i18n.T(lang, "bot.invite.bad_code")
	}

	// Sweep expired rows first so a row
	// created long ago but never swept shows
	// up as ErrExpired (not as a stale-active
	// "consumed by someone else" surprise).
	_, _ = invite.SweepExpired(env.DB)

	// Pre-validate so we can show precise
	// error messages BEFORE the atomic
	// consume.
	inv, err := invite.ValidateCode(env.DB, code, env.Username, env.PortalUserID)
	if err != nil {
		switch {
		case errors.Is(err, invite.ErrNotFound):
			return i18n.T(lang, "bot.invite.not_found")
		case errors.Is(err, invite.ErrSelfInvite):
			return i18n.T(lang, "bot.invite.self_invite")
		case errors.Is(err, invite.ErrNotForYou):
			return i18n.Tf(lang, "bot.invite.not_for_you", inv.GranteeUsername)
		case errors.Is(err, invite.ErrExpired):
			return i18n.T(lang, "bot.invite.expired")
		case errors.Is(err, invite.ErrAlreadyConsumed):
			return i18n.T(lang, "bot.invite.already_consumed")
		default:
			return i18n.Tf(lang, "bot.invite.validate_failed", err.Error())
		}
	}

	// Resolve grantor → grantor's username
	// (for the reply text).
	grantorName, _ := db.GetUserNameByID(env.DB, inv.GrantorUserID)

	// Atomic consume. On race with another
	// /accept call, the second one gets
	// ErrAlreadyConsumed (the WHERE
	// status='active' clause fails).
	consumed, err := invite.ConsumeCode(env.DB, code, env.PortalUserID)
	if err != nil {
		if errors.Is(err, invite.ErrAlreadyConsumed) {
			return i18n.T(lang, "bot.invite.already_consumed")
		}
		return i18n.Tf(lang, "bot.invite.consume_failed", err.Error())
	}

	// Apply the bridge. Resolves the
	// distinct plane URLs so the ACL
	// re-apply scope is correct (multi-plane
	// deploys re-apply each plane once).
	planeURLs, _ := invite.DistinctHeadscaleURLs(env.DB)
	if err := invite.ApplyBridge(
		env.DB,
		consumed.GrantorUserID,
		env.PortalUserID,
		consumed.Code,
		env.Username,
		planeURLs,
		nil, // ACL re-apply is run by the per-share row trigger; v0.17.1 + v0.21.0
		nil, // audit handled by the trigger too
		nil, // notifier optional; the user will see /my_subnet update
	); err != nil {
		return i18n.Tf(lang, "bot.invite.bridge_failed", err.Error())
	}

	return i18n.Tf(lang, "bot.invite.accepted",
		grantorName, env.Username)
}

// invitesListReply handles /invites — show the
// caller's outstanding + consumed invites,
// newest first. Capped at the 10 most recent
// to keep the Telegram message under the 4096
// char limit.
func invitesListReply(env BotEnv) string {
	markHTMLReply()
	lang := env.Lang

	// Two queries: invites I generated +
	// invites for me (grantee_username ==
	// env.Username).
	outstanding, _ := invite.ListByGrantor(env.DB, env.PortalUserID)
	incoming, _ := invite.ListByGrantee(env.DB, env.Username)

	if len(outstanding) == 0 && len(incoming) == 0 {
		return i18n.T(lang, "bot.invite.list_empty")
	}

	var b strings.Builder
	b.WriteString(i18n.T(lang, "bot.invite.list_title"))
	b.WriteString("\n")

	if len(outstanding) > 0 {
		b.WriteString("\n")
		b.WriteString(i18n.T(lang, "bot.invite.list_outstanding_header"))
		b.WriteString("\n")
		limit := 10
		if len(outstanding) < limit {
			limit = len(outstanding)
		}
		for i := 0; i < limit; i++ {
			inv := outstanding[i]
			b.WriteString(i18n.Tf(lang, "bot.invite.list_row_outstanding",
				inv.GranteeUsername, inv.Code, statusLabel(lang, inv.Status),
				inv.ExpiresAt.UTC().Format("2006-01-02")))
		}
	}
	if len(incoming) > 0 {
		b.WriteString("\n")
		b.WriteString(i18n.T(lang, "bot.invite.list_incoming_header"))
		b.WriteString("\n")
		limit := 10
		if len(incoming) < limit {
			limit = len(incoming)
		}
		for i := 0; i < limit; i++ {
			inv := incoming[i]
			grantorName := fmt.Sprintf("user#%d", inv.GrantorUserID)
			if name, _ := db.GetUserNameByID(env.DB, inv.GrantorUserID); name != "" {
				grantorName = name
			}
			b.WriteString(i18n.Tf(lang, "bot.invite.list_row_incoming",
				grantorName, inv.Code, statusLabel(lang, inv.Status),
				inv.CreatedAt.UTC().Format("2006-01-02")))
		}
	}
	return b.String()
}

// validUsername returns true if the candidate
// is a valid skygate username (the same regex
// the admin user-create form enforces). Pulled
// from handlers_admin_users.go's regex.
func validUsername(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, ch := range s {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

// statusLabel returns a localised short label
// for an invite status. Used by /invites.
func statusLabel(lang, status string) string {
	key := "bot.invite.status_" + status
	if i18n.T(lang, key) == key {
		// Fallback: raw status name
		return status
	}
	return i18n.T(lang, key)
}

// silence unused imports warning for
// the patterns that aren't used here
// (sql.ErrNoRows, etc.)
var _ = sql.ErrNoRows
