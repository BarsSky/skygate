package admin

// invites.go — /admin/invites page (admin overview of every
// invite_codes row).
//
// refactor-v0.30 Phase B step 3a: moved from
// internal/handlers/admin_invites.go.
//
// Handlers: GetAdminInvites, PostAdminInvitesRevoke.

import (
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/invite"
	"skygate/internal/i18n"
)

// inviteRow is the template-side row shape for the
// /admin/invites table. Wraps *invite.Invite with the
// resolved grantor and consumer usernames (the raw table
// stores user ids; the page shows names).
type inviteRow struct {
	*invite.Invite
	GrantorName  string
	ConsumerName string
}

// GetAdminInvites renders /admin/invites. Admin-only.
func (s *Service) GetAdminInvites(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	invites, err := invite.ListAll(s.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	lang := s.I18n.LangFromRequest(r)

	rows := make([]inviteRow, 0, len(invites))
	for _, inv := range invites {
		row := inviteRow{Invite: inv}
		if name, _ := db.GetUserNameByID(s.DB, inv.GrantorUserID); name != "" {
			row.GrantorName = name
		} else {
			row.GrantorName = "user#" + strconv.FormatInt(inv.GrantorUserID, 10)
		}
		if inv.ConsumedByUserID > 0 {
			if name, _ := db.GetUserNameByID(s.DB, inv.ConsumedByUserID); name != "" {
				row.ConsumerName = name
			} else {
				row.ConsumerName = "user#" + strconv.FormatInt(inv.ConsumedByUserID, 10)
			}
		}
		rows = append(rows, row)
	}

	s.Backend.RenderWithLayout(w, r, "admin/invites.html", c, map[string]any{
		"Page":         "admin/invites",
		"Title":        i18n.T(lang, "title.admin_invites"),
		"Invites":      rows,
		"TotalCount":   len(rows),
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
	})
}

// PostAdminInvitesRevoke marks the named invite code as
// revoked. Admin-only. Idempotent — revoking an already-revoked
// or already-consumed code is a no-op.
func (s *Service) PostAdminInvitesRevoke(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
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
	if err := invite.RevokeInvite(s.DB, code); err != nil {
		http.Redirect(w, r, "/admin/invites?err=revoke_failed", http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "invite_revoke", code)
	http.Redirect(w, r, "/admin/invites?ok=revoked", http.StatusFound)
}
