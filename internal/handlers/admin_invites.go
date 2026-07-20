// 2026-07-20: v0.21.0 — /admin/invites page.
//
// Admin overview of every invite_codes row.
// Shows the code, the grantor, the grantee, the
// status, the expiry time, and (for consumed
// rows) who consumed it. Admin-only.
//
// The page supports two actions:
//   - POST /admin/invites/revoke — mark an
//     active invite as revoked (the grantor
//     changed their mind, or the code leaked).
//   - GET /admin/invites — render the table.
//
// Pagination is intentionally absent — the
// expected volume is low (hundreds over the
// life of a deployment), and a hard cap of
// 200 rows on the SQL side keeps the page
// render under a second.
//
// The bot /invites command renders the same
// data scoped to the caller; the admin page
// is the unfiltered "show me everything"
// view.

package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/invite"
	"skygate/internal/i18n"
)

// GetAdminInvites renders /admin/invites.
// Admin-only.
func (a *App) GetAdminInvites(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	invites, err := invite.ListAll(a.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	lang := a.I18n.LangFromRequest(r)

	// Resolve grantor + consumer usernames
	// in a single batched query (200 rows
	// max, so the lookup is cheap).
	rows := make([]inviteRow, 0, len(invites))
	for _, inv := range invites {
		row := inviteRow{Invite: inv}
		if name, _ := db.GetUserNameByID(a.DB, inv.GrantorUserID); name != "" {
			row.GrantorName = name
		} else {
			row.GrantorName = "user#" + strconv.FormatInt(inv.GrantorUserID, 10)
		}
		if inv.ConsumedByUserID > 0 {
			if name, _ := db.GetUserNameByID(a.DB, inv.ConsumedByUserID); name != "" {
				row.ConsumerName = name
			} else {
				row.ConsumerName = "user#" + strconv.FormatInt(inv.ConsumedByUserID, 10)
			}
		}
		rows = append(rows, row)
	}

	a.renderWithLayout(w, r, "admin/invites.html", c, map[string]any{
		"Page":         "admin/invites",
		"Title":        i18n.T(lang, "title.admin_invites"),
		"Invites":      rows,
		"TotalCount":   len(rows),
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
	})
}

// PostAdminInvitesRevoke marks the named
// invite code as revoked. Admin-only.
// Idempotent — revoking an already-revoked or
// already-consumed code is a no-op.
func (a *App) PostAdminInvitesRevoke(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/invites?err=form_parse", http.StatusFound)
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	if code == "" {
		http.Redirect(w, r, "/admin/invites?err=missing_code", http.StatusFound)
		return
	}
	if err := invite.RevokeInvite(a.DB, code); err != nil {
		http.Redirect(w, r, "/admin/invites?err=revoke_failed", http.StatusFound)
		return
	}
	a.audit(c.UserID, c.Username, "invite_revoke", code)
	http.Redirect(w, r, "/admin/invites?ok=revoked", http.StatusFound)
}

// inviteRow is the template-side row shape
// for the /admin/invites table. Wraps
// *invite.Invite with the resolved grantor
// and consumer usernames (the raw table
// stores user ids; the page shows names).
type inviteRow struct {
	*invite.Invite
	GrantorName  string
	ConsumerName string
}
