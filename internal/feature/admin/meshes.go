package admin

// meshes.go — /admin/meshes page (admin overview of every meshes row).
//
// refactor-v0.30 Phase B step 3a: moved from
// internal/handlers/admin_meshes.go.
//
// Handler: GetAdminMeshes. Read-only on v0.22.0; creation/join/leave
// happen via the bot per the "bots for user-to-user interaction,
// admin UI for oversight" UX choice.

import (
	"net/http"
	"strconv"

	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/mesh"
)

// meshRow is one row of the /admin/meshes table: the mesh
// itself + the resolved creator name + the member count +
// the member list (used by the template to render the
// "Members: alice, bob, carol" line under each row).
type meshRow struct {
	Mesh        *mesh.Mesh
	CreatorName string
	MemberCount int
	MemberList  []mesh.Member
}

// GetAdminMeshes renders /admin/meshes. Admin-only. Read-only
// on the v0.22.0 release.
func (s *Service) GetAdminMeshes(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	meshes, err := mesh.ListAllMeshes(s.dbc())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]meshRow, 0, len(meshes))
	for _, m := range meshes {
		row := meshRow{Mesh: m}
		if name, _ := db.GetUserNameByID(s.dbc(), m.CreatorUserID); name != "" {
			row.CreatorName = name
		} else {
			row.CreatorName = "user#" + strconv.FormatInt(m.CreatorUserID, 10)
		}
		members, _ := mesh.ListMembers(s.dbc(), m.ID)
		row.MemberCount = len(members)
		row.MemberList = members
		rows = append(rows, row)
	}
	lang := s.I18n.LangFromRequest(r)
	s.Backend.RenderWithLayout(w, r, "admin/meshes.html", c, map[string]any{
		"Page":       "admin/meshes",
		"Title":      i18n.T(lang, "title.admin_meshes"),
		"Meshes":     rows,
		"TotalCount": len(rows),
		"Lang":       lang,
	})
}
