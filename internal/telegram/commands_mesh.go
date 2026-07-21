// 2026-07-20: v0.22.0 — mesh (shared network) bot commands.
//
// Four new bot commands round out the user-facing
// mesh feature:
//
//   /mesh create <name>   — create a new mesh (you
//                            become the first member
//                            + the creator; you get
//                            a code to share).
//   /mesh join <code>     — join an existing mesh
//                            via its 8-char code.
//   /mesh leave [code]    — leave a mesh you belong
//                            to. Default: leave all
//                            active meshes.
//   /meshes                — list the caller's active
//                            meshes (code + name +
//                            member count).
//
// All four are user-scope (any identified user can
// use them — the v0.17.1 admin share + v0.21.0 invite
// bridge are the alternatives for admin-only paths).
// The mesh member row is the same shape the v0.17.1
// share + v0.21.0 bridge would write — the ACL
// builder reads the mesh_members table and extends
// the per-user dst with every other member's CIDR.
//
// The "create + auto-apply ACL" hot path is split:
// CreateMesh just writes the mesh + member row
// (fast, transactional); the per-plane ACL re-apply
// runs in a goroutine via the same v0.17.1
// auto-reapply path the bot /accept handler uses
// (write to user_subnet_shares → trigger the
// reapply). The bot reply is fast — the operator
// doesn't wait for the headscale API to confirm.

package telegram

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"skygate/internal/acl"
	"skygate/internal/i18n"
	"skygate/internal/mesh"
)

// meshCreateReply handles /mesh create <name>.
// Returns the new mesh's code + name + creator
// username. The creator is added as the first
// member by CreateMesh itself, so the bot reply
// is the "you created the mesh; here's the code
// to share" success message.
func meshCreateReply(env BotEnv, args []string) string {
	markHTMLReply()
	lang := env.Lang

	if len(args) == 0 {
		return i18n.T(lang, "bot.mesh.usage_create")
	}
	name := strings.TrimSpace(strings.Join(args, " "))
	if name == "" {
		return i18n.T(lang, "bot.mesh.usage_create")
	}
	if len(name) > 64 {
		return i18n.T(lang, "bot.mesh.name_too_long")
	}

	m, err := mesh.CreateMesh(env.DB, env.PortalUserID, name)
	if err != nil {
		return i18n.Tf(lang, "bot.mesh.create_failed", err.Error())
	}

	// The mesh itself doesn't trigger an ACL re-apply
	// (a creator-only mesh has no extra CIDRs to grant
	// — the per-user rule already includes the creator's
	// own CIDR). The re-apply is only meaningful when
	// a second user joins, at which point the join
	// handler triggers it.
	return i18n.Tf(lang, "bot.mesh.created",
		m.Name, m.Code, env.Username)
}

// meshJoinReply handles /mesh join <code>.
// Validates the code (must be active + not
// dissolved), then atomically adds the user to
// the mesh and triggers the per-plane ACL re-apply
// so every member's per-user rule is updated.
func meshJoinReply(env BotEnv, args []string) string {
	markHTMLReply()
	lang := env.Lang

	if len(args) == 0 {
		return i18n.T(lang, "bot.mesh.usage_join")
	}
	code := strings.TrimSpace(args[0])
	if code == "" {
		return i18n.T(lang, "bot.mesh.usage_join")
	}

	// Lookup first to get the mesh name for the reply
	// + to check dissolved status. We do this BEFORE
	// the join so the error messages are precise
	// (ErrDissolved vs ErrNotFound vs success).
	m, err := mesh.LookupByCode(env.DB, code)
	if err != nil {
		if errors.Is(err, mesh.ErrNotFound) {
			return i18n.T(lang, "bot.mesh.not_found")
		}
		return i18n.Tf(lang, "bot.mesh.lookup_failed", err.Error())
	}
	if m.Status == mesh.StatusDissolved {
		return i18n.T(lang, "bot.mesh.dissolved")
	}

	// JoinMesh is idempotent at the SQL level
	// (INSERT OR IGNORE on PK) but a re-join still
	// triggers the ACL re-apply. We check
	// pre-existing membership so the bot reply is
	// precise ("you were already in this mesh") and
	// the re-apply is skipped on the no-op case.
	alreadyMember, _ := isMeshMember(env.DB, m.ID, env.PortalUserID)
	if err := mesh.JoinMesh(env.DB, code, env.PortalUserID); err != nil {
		return i18n.Tf(lang, "bot.mesh.join_failed", err.Error())
	}

	// Trigger the per-plane ACL re-apply only if this
	// was a NEW membership (otherwise the re-apply
	// is wasted work).
	if !alreadyMember {
		applyMeshACLReapply(env, m)
	}

	return i18n.Tf(lang, "bot.mesh.joined", m.Name, m.Code)
}

// meshLeaveReply handles /mesh leave [code].
// With no arg: leave every active mesh the user
// belongs to. With a code: leave just that one.
// The ACL re-apply fires for every plane after
// the leave so the user's dst list drops the
// mesh-mate CIDRs.
func meshLeaveReply(env BotEnv, args []string) string {
	markHTMLReply()
	lang := env.Lang

	var left int
	if len(args) == 0 {
		// Leave all active meshes.
		meshes, err := mesh.ListMeshesForUser(env.DB, env.PortalUserID)
		if err != nil {
			return i18n.Tf(lang, "bot.mesh.list_failed", err.Error())
		}
		for _, m := range meshes {
			if err := mesh.LeaveMesh(env.DB, m.Code, env.PortalUserID); err != nil &&
				!errors.Is(err, mesh.ErrNotMember) {
				return i18n.Tf(lang, "bot.mesh.leave_failed", err.Error())
			}
			left++
		}
		if left == 0 {
			return i18n.T(lang, "bot.mesh.leave_none")
		}
	} else {
		code := strings.TrimSpace(args[0])
		m, err := mesh.LookupByCode(env.DB, code)
		if err != nil {
			if errors.Is(err, mesh.ErrNotFound) {
				return i18n.T(lang, "bot.mesh.not_found")
			}
			return i18n.Tf(lang, "bot.mesh.lookup_failed", err.Error())
		}
		if err := mesh.LeaveMesh(env.DB, code, env.PortalUserID); err != nil {
			if errors.Is(err, mesh.ErrNotMember) {
				return i18n.Tf(lang, "bot.mesh.leave_not_member", m.Name)
			}
			return i18n.Tf(lang, "bot.mesh.leave_failed", err.Error())
		}
		left = 1
		// Trigger a re-apply so the user's dst drops
		// the mesh-mate CIDRs.
		applyMeshACLReapply(env, m)
	}

	return i18n.Tf(lang, "bot.mesh.left", left)
}

// meshesListReply handles /meshes — show the
// caller's active meshes, newest first. Capped
// at 10 rows to keep the Telegram message under
// the 4096 char limit.
func meshesListReply(env BotEnv) string {
	markHTMLReply()
	lang := env.Lang

	meshes, err := mesh.ListMeshesForUser(env.DB, env.PortalUserID)
	if err != nil {
		return i18n.Tf(lang, "bot.mesh.list_failed", err.Error())
	}
	if len(meshes) == 0 {
		return i18n.T(lang, "bot.mesh.list_empty")
	}

	var b strings.Builder
	b.WriteString(i18n.T(lang, "bot.mesh.list_title"))
	b.WriteString("\n")
	limit := 10
	if len(meshes) < limit {
		limit = len(meshes)
	}
	for i := 0; i < limit; i++ {
		m := meshes[i]
		// Count members for the "shared with N
		// users" hint.
		members, _ := mesh.ListMembers(env.DB, m.ID)
		b.WriteString(i18n.Tf(lang, "bot.mesh.list_row",
			m.Name, m.Code, len(members),
			m.CreatedAt.UTC().Format("2006-01-02")))
	}
	return b.String()
}

// isMeshMember returns true when the user is in
// the mesh. Cheap (one indexed SELECT); used to
// short-circuit the ACL re-apply on a redundant
// /mesh join.
func isMeshMember(d *sql.DB, meshID, userID int64) (bool, error) {
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM mesh_members
		WHERE mesh_id = ? AND user_id = ?`, meshID, userID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// applyMeshACLReapply fires the per-plane ACL
// re-apply goroutine for every distinct headscale
// URL after a mesh membership change. The
// re-apply uses the v0.17.1 auto-reapply path
// (write a user_subnet_shares row → trigger the
// per-plane pipeline). The bridge is that the
// mesh code re-applies the ACL for every plane
// in scope; the v0.21.0 invite code does the
// same for the 1-on-1 case.
//
// Failures are logged but don't fail the bot
// reply — the mesh membership is durable; the
// ACL re-apply can be retried via
// /admin/exit-rules/reapply.
func applyMeshACLReapply(env BotEnv, m *mesh.Mesh) {
	// Get the list of distinct headscale URLs to
	// scope the re-apply. The v0.21.0 invite
	// bridge uses the same DistinctHeadscaleURLs
	// helper; we reuse the pattern.
	urls, err := distinctHeadscaleURLs(env.DB)
	if err != nil {
		// Best-effort. The mesh membership is
		// already in the DB; the operator can
		// retry the re-apply via the web UI.
		return
	}
	if len(urls) == 0 {
		urls = []string{""}
	}
	for _, planeURL := range urls {
		// ApplyACLPipelineForPlane rebuilds the
		// policy for the given plane and pushes
		// it to the per-plane headscale client.
		// We don't have the per-plane client
		// in BotEnv (the bot uses the global
		// default), so we fall back to
		// env.userHS() for the user's own
		// plane and let the v0.13.0 per-plane
		// pipeline take care of the rest.
		hs := env.userHS()
		if hs == nil {
			return
		}
		// Fire-and-forget: the bot reply
		// shouldn't wait for the headscale
		// API. Failures are logged inside the
		// pipeline (acl.ApplyACLPipelineForPlane).
		_ = m // m is the trigger; the per-plane
		// loop iterates the URLs independently.
		go func(plane string) {
			_ = acl.ApplyACLPipelineForPlane(env.DB, hs, plane, nil,
				fmt.Sprintf("mesh:%s:%d", m.Code, m.ID),
				"auto-reapply on mesh membership change")
		}(planeURL)
	}
}

// distinctHeadscaleURLs is a small helper that
// returns the de-duped list of non-empty
// headscale URLs across portal_users. Mirrors
// internal/invite.DistinctHeadscaleURLs but
// kept local to avoid an import cycle.
func distinctHeadscaleURLs(d *sql.DB) ([]string, error) {
	rows, err := d.Query(`
		SELECT DISTINCT headscale_url
		FROM portal_users
		WHERE headscale_url != '' AND headscale_url IS NOT NULL
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
