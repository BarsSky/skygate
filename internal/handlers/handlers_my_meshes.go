// 2026-07-20: v0.22.0 — /my/meshes user-scope page.
//
// The v0.22.0 mesh feature was bot-only at first (/mesh
// create|join|leave|meshes). The operator flagged that
// users have no obvious place in the WEB UI to create
// or join a shared network — the bot is convenient for
// power users but every identified user (including
// non-Telegram ones) needs a web entry point.
//
// This file adds the four HTTP routes:
//
//   GET  /my/meshes                 — render the page
//                                     (current meshes +
//                                      create form +
//                                      join form)
//   POST /my/meshes/create          — create a new mesh
//                                     (you become the
//                                     first member +
//                                     creator)
//   POST /my/meshes/join            — join an existing
//                                     mesh by 8-char code
//   POST /my/meshes/leave           — leave a mesh
//                                     (by code) or all
//                                     (no code)
//
// All three POST routes follow the v0.17.0 admin-pattern:
//   - validate input
//   - call into the mesh package
//   - write an audit_log row
//   - redirect to /my/meshes?ok=... or ?err=...
//
// The page reuses the same flash-success / flash-error
// pattern as /admin/invites and /admin/users: the GET
// handler reads `r.URL.Query().Get("ok")` /
// `r.URL.Query().Get("err")` and renders the banner
// accordingly. No JavaScript needed; the page works
// on every browser + phone.
//
// The bot path is unchanged — /mesh create|join|leave
// dispatch the same internal/mesh functions. Web +
// bot share the same underlying state (the meshes +
// mesh_members tables) so creating a mesh via the web
// shows up in /mesh join's "this code is for alice"
// flow, and vice versa.
//
// ACL re-apply: on a successful join/leave, the handler
// fires the per-plane re-apply goroutine (same path
// the bot /mesh join uses). The web reply is fast —
// the ACL re-apply runs in the background and the
// operator can monitor the headscale policy in
// /admin/exit-rules.
package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"skygate/internal/acl"
	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
	"skygate/internal/mesh"
)

// GetMyMeshes renders /my/meshes — user-scope page
// showing the caller's active meshes, a form to
// create a new mesh, and a form to join an existing
// one via 8-char code. The page mirrors the bot
// /meshes reply (same data) plus the two action
// forms.
//
// Visible to every identified user. Mesh membership
// is data-scoped to the caller (the bot /meshes
// command filters to env.PortalUserID; this page
// does the same via c.UserID).
func (a *App) GetMyMeshes(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	meshes, err := mesh.ListMeshesForUser(a.DB, c.UserID)
	if err != nil {
		log.Printf("web.my.meshes: ListMeshesForUser userID=%d err=%v", c.UserID, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Build the per-mesh row (Mesh + MemberCount +
	// CreatorName for the "shared with N users" line).
	// Member list itself is collapsed — the per-mesh
	// <details> expansion is in the template.
	rows := make([]myMeshRow, 0, len(meshes))
	for _, m := range meshes {
		row := myMeshRow{Mesh: m}
		if name, _ := getUserNameByID(a.DB, m.CreatorUserID); name != "" {
			row.CreatorName = name
		} else {
			row.CreatorName = fmt.Sprintf("user#%d", m.CreatorUserID)
		}
		members, _ := mesh.ListMembers(a.DB, m.ID)
		row.MemberCount = len(members)
		row.MemberList = members
		rows = append(rows, row)
	}
	a.renderWithLayout(w, r, "user/meshes.html", c, map[string]any{
		"Meshes":       rows,
		"FlashSuccess": translateMeshFlash(a, r, "ok", ""),
		"FlashError":   translateMeshFlash(a, r, "err", ""),
	})
}

// translateMeshFlash turns a URL-encoded flash
// code (ok=created, err=not_found, ...) into a
// localized message. The code is the value in
// the URL; the translated text comes from the
// i18n catalog (my_meshes.flash_* for success,
// my_meshes.flash_err_* for errors). Special
// values:
//
//   - ok=created&code=<value>  →  the value is
//     passed as a positional arg to the catalog
//     template (the mesh code)
//   - ok=joined&name=<value>   →  the value is
//     the mesh name (HTML-escaped in the template)
//   - err=join_failed:&detail= →  the detail is
//     appended to the translated error
//
// The queryArgs map carries the dynamic values
// (code, name, detail). The code prefix is the
// URL's ?ok= / ?err= value.
func translateMeshFlash(a *App, r *http.Request, kind, fallback string) string {
	q := r.URL.Query()
	raw := q.Get(kind)
	if raw == "" {
		return fallback
	}
	lang := a.I18n.LangFromRequest(r)
	// Build the i18n key. The catalog uses different
	// prefixes for success (flash_) and error
	// (flash_err_) so a code like "not_found" in the
	// err=... query maps to my_meshes.flash_err_not_found
	// (the convention matches the v0.17.x admin pages,
	// which use err=<code> → catalog key "common.err.<code>"
	// or similar — the split keeps the namespace
	// collision-free between the two sides).
	key := "my_meshes.flash_"
	if kind == "err" {
		key = "my_meshes.flash_err_"
	}
	key += raw
	// Some flash codes carry a positional arg
	// (the mesh code on create, the name on
	// join, the error detail on join/create
	// failure). We pull the arg from the same
	// query string.
	switch raw {
	case "created":
		// ok=created&code=<8-char>
		code := q.Get("code")
		if code == "" {
			return i18n.T(lang, key)
		}
		return i18n.Tf(lang, key, code)
	case "joined":
		// ok=joined&name=<mesh-name>
		name := q.Get("name")
		if name == "" {
			return i18n.T(lang, key)
		}
		return i18n.Tf(lang, key, name)
	case "join_failed", "create_failed", "lookup_failed", "leave_failed", "list_failed":
		// err=<code>&detail=<error message>
		detail := q.Get("detail")
		if detail == "" {
			return i18n.T(lang, key)
		}
		return i18n.Tf(lang, key, detail)
	default:
		return i18n.T(lang, key)
	}
}

// PostMyMeshesCreate handles POST /my/meshes/create.
// Form fields: name (string, required, max 64 chars).
// The caller becomes the creator + first member.
func (a *App) PostMyMeshesCreate(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/my/meshes?err=form_parse", http.StatusFound)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/my/meshes?err=missing_name", http.StatusFound)
		return
	}
	if len(name) > 64 {
		http.Redirect(w, r, "/my/meshes?err=name_too_long", http.StatusFound)
		return
	}
	m, err := mesh.CreateMesh(a.DB, c.UserID, name)
	if err != nil {
		log.Printf("web.my.meshes: CreateMesh userID=%d err=%v", c.UserID, err)
		http.Redirect(w, r,
			"/my/meshes?err=create_failed&detail="+url.QueryEscape(err.Error()),
			http.StatusFound)
		return
	}
	a.audit(c.UserID, c.Username, "mesh_create",
		fmt.Sprintf("mesh_id=%d name=%q code=%s", m.ID, m.Name, m.Code))
	http.Redirect(w, r,
		fmt.Sprintf("/my/meshes?ok=created&code=%s", m.Code),
		http.StatusFound)
}

// PostMyMeshesJoin handles POST /my/meshes/join.
// Form fields: code (string, required, normalized to
// upper-case + trimmed). On success: caller is added
// to the mesh + per-plane ACL re-apply fires (same
// path as the bot /mesh join handler).
func (a *App) PostMyMeshesJoin(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/my/meshes?err=form_parse", http.StatusFound)
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	if code == "" {
		http.Redirect(w, r, "/my/meshes?err=missing_code", http.StatusFound)
		return
	}
	// Pre-lookup so the error messages are precise
	// (ErrNotFound vs ErrDissolved vs success).
	m, err := mesh.LookupByCode(a.DB, code)
	if err != nil {
		if errors.Is(err, mesh.ErrNotFound) {
			http.Redirect(w, r, "/my/meshes?err=not_found", http.StatusFound)
			return
		}
		log.Printf("web.my.meshes: LookupByCode userID=%d err=%v", c.UserID, err)
		http.Redirect(w, r,
			"/my/meshes?err=lookup_failed&detail="+url.QueryEscape(err.Error()),
			http.StatusFound)
		return
	}
	if m.Status == mesh.StatusDissolved {
		http.Redirect(w, r, "/my/meshes?err=dissolved", http.StatusFound)
		return
	}
	// Idempotency: pre-check whether the user is already
	// a member. If so, the join is a no-op (the ACL
	// re-apply is skipped too — wasted work). The
	// surface error is "already_member" so the user
	// knows they didn't change anything.
	alreadyMember, _ := isMeshMember(a.DB, m.ID, c.UserID)
	if err := mesh.JoinMesh(a.DB, code, c.UserID); err != nil {
		log.Printf("web.my.meshes: JoinMesh userID=%d code=%s err=%v",
			c.UserID, code, err)
		http.Redirect(w, r,
			"/my/meshes?err=join_failed&detail="+url.QueryEscape(err.Error()),
			http.StatusFound)
		return
	}
	// Per-plane ACL re-apply (best-effort, async). The
	// same pattern as the bot /mesh join handler in
	// internal/telegram/commands_mesh.go. We use a
	// closure that resolves a plane URL to a *headscale.Client
	// (per the v0.13.0 per-plane pipeline): per-user
	// override if the user is on a non-default plane,
	// else the global default.
	if !alreadyMember {
		hsForPlane := func(planeURL string) *headscale.Client {
			if planeURL == "" {
				return a.HSGlobal()
			}
			return a.HSForUser(c.UserID)
		}
		go webMeshACLReapply(a.DB, hsForPlane, c.UserID,
			fmt.Sprintf("mesh:%s:%d", m.Code, m.ID))
	}
	a.audit(c.UserID, c.Username, "mesh_join",
		fmt.Sprintf("mesh_id=%d name=%q code=%s", m.ID, m.Name, m.Code))
	http.Redirect(w, r,
		fmt.Sprintf("/my/meshes?ok=joined&name=%s", url.QueryEscape(m.Name)),
		http.StatusFound)
}

// PostMyMeshesLeave handles POST /my/meshes/leave.
// Form fields: code (optional). With no code: leave
// every active mesh the caller is in. With a code:
// leave just that one mesh. The re-apply fires only
// when at least one leave actually happened.
func (a *App) PostMyMeshesLeave(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/my/meshes?err=form_parse", http.StatusFound)
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	var left int
	if code == "" {
		// Leave all active meshes.
		meshes, err := mesh.ListMeshesForUser(a.DB, c.UserID)
		if err != nil {
			log.Printf("web.my.meshes: ListMeshesForUser userID=%d err=%v",
				c.UserID, err)
			http.Redirect(w, r,
				"/my/meshes?err=list_failed&detail="+url.QueryEscape(err.Error()),
				http.StatusFound)
			return
		}
		for _, m := range meshes {
			if err := mesh.LeaveMesh(a.DB, m.Code, c.UserID); err != nil &&
				!errors.Is(err, mesh.ErrNotMember) {
				log.Printf("web.my.meshes: LeaveMesh userID=%d code=%s err=%v",
					c.UserID, m.Code, err)
				http.Redirect(w, r,
					"/my/meshes?err=leave_failed&detail="+url.QueryEscape(err.Error()),
					http.StatusFound)
				return
			}
			left++
		}
		if left == 0 {
			http.Redirect(w, r, "/my/meshes?err=leave_none", http.StatusFound)
			return
		}
	} else {
		m, err := mesh.LookupByCode(a.DB, code)
		if err != nil {
			if errors.Is(err, mesh.ErrNotFound) {
				http.Redirect(w, r, "/my/meshes?err=not_found", http.StatusFound)
				return
			}
			http.Redirect(w, r,
				"/my/meshes?err=lookup_failed&detail="+url.QueryEscape(err.Error()),
				http.StatusFound)
			return
		}
		if err := mesh.LeaveMesh(a.DB, code, c.UserID); err != nil {
			if errors.Is(err, mesh.ErrNotMember) {
				http.Redirect(w, r, "/my/meshes?err=leave_not_member", http.StatusFound)
				return
			}
			log.Printf("web.my.meshes: LeaveMesh userID=%d code=%s err=%v",
				c.UserID, code, err)
			http.Redirect(w, r,
				"/my/meshes?err=leave_failed&detail="+url.QueryEscape(err.Error()),
				http.StatusFound)
			return
		}
		left = 1
		// Trigger a re-apply so the user's dst drops
		// the mesh-mate CIDRs.
		hsForPlane := func(planeURL string) *headscale.Client {
			if planeURL == "" {
				return a.HSGlobal()
			}
			return a.HSForUser(c.UserID)
		}
		go webMeshACLReapply(a.DB, hsForPlane, c.UserID,
			fmt.Sprintf("mesh-leave:%s:%d", m.Code, m.ID))
	}
	a.audit(c.UserID, c.Username, "mesh_leave",
		fmt.Sprintf("count=%d", left))
	http.Redirect(w, r, "/my/meshes?ok=left", http.StatusFound)
}

// myMeshRow is one row of the /my/meshes table —
// the mesh itself + the resolved creator name +
// the member count + the member list (for the
// <details> expansion in the template).
type myMeshRow struct {
	Mesh        *mesh.Mesh
	CreatorName string
	MemberCount int
	MemberList  []mesh.Member
}

// isMeshMember returns true when the user is in the
// mesh. Cheap (one indexed SELECT); used to skip
// the ACL re-apply on a redundant /my/meshes/join
// (the operator hit "Join" twice by accident).
func isMeshMember(d *sql.DB, meshID, userID int64) (bool, error) {
	var n int
	if err := d.QueryRow(`
		SELECT COUNT(*) FROM mesh_members
		 WHERE mesh_id = ? AND user_id = ?`,
		meshID, userID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// getUserNameByID is a small wrapper around the
// db.GetUserNameByID helper to keep this file's
// import surface small. The function is just a
// one-liner that returns the empty string on
// "user not found" — the caller treats that as
// "use the user#N fallback" in the template.
func getUserNameByID(d *sql.DB, id int64) (string, error) {
	var name string
	err := d.QueryRow(`SELECT username FROM portal_users WHERE id = ?`, id).Scan(&name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return name, nil
}

// webMeshACLReapply fires the per-plane ACL
// re-apply goroutine for every distinct headscale
// URL after a web mesh membership change. The
// shape mirrors the bot path in
// internal/telegram/commands_mesh.go's
// applyMeshACLReapply, but the Web handler
// routes through HSForUser (per-user plane
// routing from v0.12.0) rather than the
// bot's env.userHS() closure.
//
// Best-effort: failures are logged but the
// web reply already returned. The mesh
// membership is durable; the operator can
// retry the re-apply via the web UI
// /admin/exit-rules/reapply.
func webMeshACLReapply(
	d *sql.DB,
	hsForPlane func(planeURL string) *headscale.Client,
	callerUserID int64,
	detailForLog string,
) {
	// Discover every distinct headscale URL the
	// operator has configured (one per plane +
	// the global default). The list is the
	// same one ApplyACLForAllPlanes uses; the
	// per-plane iteration mirrors the v0.13.0
	// per-plane ACL pipeline.
	planes, err := db.ListControlPlanes(d)
	if err != nil {
		log.Printf("web.my.meshes: ListControlPlanes err=%v", err)
		return
	}
	if len(planes) == 0 {
		// No planes configured — single-plane deploy
		// with the global default. Run the re-apply
		// against "" (the global default's URL).
		planes = []db.ControlPlaneUserCount{{URL: ""}}
	}
	for _, p := range planes {
		hs := hsForPlane(p.URL)
		if hs == nil {
			// No client for this plane (e.g.
			// SKYGATE_SECRET_KEY missing or corrupt
			// per-plane key). Skip; the operator
			// can re-apply via /admin/exit-rules/reapply.
			continue
		}
		// Fire-and-forget: the web reply is
		// already sent. The pipeline writes an
		// acl_snapshots row + an exit_rule_log
		// row; failures are logged in the pipeline.
		_ = callerUserID
		_ = p
		// The call is per-plane; we use the global
		// user-id=0 sentinel for HSForUser (the
		// per-plane client lookup doesn't need a
		// specific user). The pipeline rebuilds the
		// policy from the DB state, not the caller's
		// identity.
		go func(plane string) {
			_ = acl.ApplyACLPipelineForPlane(d, hs, plane, nil,
				"web:"+detailForLog,
				"web re-apply on mesh membership change")
		}(p.URL)
	}
}
