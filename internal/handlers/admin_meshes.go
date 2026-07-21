// 2026-07-20: v0.22.0 — /admin/meshes page.
//
// Admin overview of every meshes row. Shows
// the mesh name, code, creator, status, member
// count, and the member list (expandable per
// row). Admin-only.
//
// The page is read-only on the v0.22.0 release:
// creation / join / leave happen via the bot
// (per the "bots for user-to-user interaction,
// admin UI for oversight" UX choice that
// mirrors the v0.21.0 invite admin page).
// Dissolve is also a no-op on the admin UI
// (the creator dissolves via the bot; the
// admin can see the dissolved status but
// can't trigger it from here).
//
// Pagination is intentionally absent — the
// expected volume is low (hundreds over the
// life of a deployment), and a hard cap of
// 200 rows on the SQL side keeps the page
// render under a second.

package handlers

import (
	"net/http"
	"strconv"

	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/mesh"
)

// meshRow is one row of the /admin/meshes table:
// the mesh itself + the resolved creator name +
// the member count + the member list (used by
// the template to render the "Members: alice,
// bob, carol" line under each row).
type meshRow struct {
	Mesh          *mesh.Mesh
	CreatorName   string
	MemberCount   int
	MemberList    []mesh.Member
}

// GetAdminMeshes renders /admin/meshes.
// Admin-only. Read-only on the v0.22.0 release.
func (a *App) GetAdminMeshes(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	meshes, err := mesh.ListAllMeshes(a.DB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]meshRow, 0, len(meshes))
	for _, m := range meshes {
		row := meshRow{Mesh: m}
		if name, _ := db.GetUserNameByID(a.DB, m.CreatorUserID); name != "" {
			row.CreatorName = name
		} else {
			row.CreatorName = "user#" + strconv.FormatInt(m.CreatorUserID, 10)
		}
		members, _ := mesh.ListMembers(a.DB, m.ID)
		row.MemberCount = len(members)
		row.MemberList = members
		rows = append(rows, row)
	}
	lang := a.I18n.LangFromRequest(r)
	a.renderWithLayout(w, r, "admin/meshes.html", c, map[string]any{
		"Page":       "admin/meshes",
		"Title":      i18n.T(lang, "title.admin_meshes"),
		"Meshes":     rows,
		"TotalCount": len(rows),
		"Lang":       lang,
	})
}
