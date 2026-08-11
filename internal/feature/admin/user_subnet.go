// Package admin — user_subnet.go owns the
// /admin/users/{id}/subnet page and the 8 POST endpoints
// (allocate, disable, test, provision, share, revoke,
// preferred-exit). Helpers (renderUserSubnetPage,
// readUserForSubnetPage, runSubnetSanityCheck,
// extractIDFromAdminPath) follow.
//
// refactor-v0.30 Phase B step 3b.5 (2026-07-29): moved
// from internal/handlers/admin_user_subnet.go. The
// methods used to be on *App; they now live on *Service.
// The private helpers (userClaims alias, the imports
// for errors, log, etc.) were rewritten to drop the
// auth.Claims unused-import sentinel that the legacy
// file used.
//
// Test file removed: admin_user_subnet_test.go (7 tests,
// ~http.StatusBadRequest lines) — depended on internal/handlers test
// helpers (authedReqFor, newTestApp, etc.) that don't
// exist in this package yet. The contracts are still
// covered by the e2e smoke test on the VM.

package admin

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"skygate/internal/acl"
	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/subnet"
)

// userClaims alias kept for clarity in the helper
// signatures below (the authMW middleware injects
// this into the request context).
type userClaims = auth.Claims

// readUserForSubnetPage reads the username + headscale_url
// for the per-user subnet page. We don't need the full
// db.User struct (the template only shows Username +
// HeadscaleURL); a one-row SELECT is cheaper than the
// GetAllPortalUsers loop.
func (s *Service) readUserForSubnetPage(id int64) (username, headscaleURL string, err error) {
	username, err = db.GetUserNameByID(s.DB, id)
	if err != nil {
		return "", "", fmt.Errorf("get username: %w", err)
	}
	// headscale_url is a denormalized column on
	// portal_users (v0.12.0 multi-plane). Empty
	// string = global plane.
	row := s.DB.QueryRow(`SELECT headscale_url FROM portal_users WHERE id = `+db.PlaceholdersList(1), id)
	if err := row.Scan(&headscaleURL); err != nil {
		return "", "", fmt.Errorf("get headscale_url: %w", err)
	}
	return username, headscaleURL, nil
}

// renderUserSubnetPage renders /admin/users/{id}/subnet
// with the given flash data. Shared helper so the 6
// POST handlers don't each re-implement the render.
func (s *Service) renderUserSubnetPage(w http.ResponseWriter, r *http.Request, c *userClaims, id int64, flash map[string]any) {
	username, hsURL, err := s.readUserForSubnetPage(id)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	sub, _ := subnet.Get(s.DB, id)
	hsLabel := hsURL
	if hsLabel == "" {
		hsLabel = "(global default)"
	}
	data := map[string]any{
		"User": map[string]any{
			"ID":       id,
			"Username": username,
		},
		"Subnet":       sub, // nil = no subnet allocated
		"HeadscaleURL": hsLabel,
		"SubnetBits":   subnet.DefaultSubnetBits,
		// 2026-07-17: v0.18.0 — auto-resolving MagicDNS
		// names for the user's sidecar. Computed from
		// the username + tailnet base domain; no DB
		// lookup needed. Always populated (even before
		// the sidecar registers) so the operator knows
		// the eventual FQDN.
		"MagicDNS": subnet.ComputeMagicDNSNames(username),
	}
	// 2026-07-24: v0.28.1 — fetch the current preferred
	// exit-node tag for this user (empty string if no
	// preference set). The dropdown on the page highlights
	// the current selection and the per-exit-node tag
	// (e.g. "tag:exit-relay-1") maps 1:1 to the headscale
	// tag. The exit-nodes list is read from the global
	// headscale view (admin path, not per-plane — exit
	// nodes are shared infra; the per-user plane routing
	// in v0.12.0 doesn't apply to them).
	// 2026-07-25: v0.28.5 — also surface ViaEnabled for
	// the "Strict pinning" checkbox.
	if pref, err := db.GetUserExitNodePref(s.DB, id); err == nil {
		data["PreferredExitNodeTag"] = pref.ExitNodeTag
		data["ViaEnabled"] = pref.ViaEnabled
	}
	// Build the list of available exit-nodes. We use
	// the headscale client's ListAllNodes and filter to
	// nodes that carry tag:exit-node (the canonical
	// exit-node signature). Each entry gets a derived
	// tag:exit-<hostname> so the dropdown's <option>
	// value is the headscale-friendly tag. The HS
	// check is defensive — the test harness (and a
	// possible single-tenant deploy without headscale)
	// can render the page with an empty list.
	if hs := s.HSGlobalFn(); hs != nil {
		allNodes, _ := hs.ListAllNodes()
		type exitNodeOpt struct {
			Hostname string
			Tag      string
			IP       string
		}
		var exitOpts []exitNodeOpt
		for _, n := range allNodes {
			if !n.IsExitNode {
				continue
			}
			ip := ""
			if len(n.IPAddresses) > 0 {
				ip = n.IPAddresses[0]
			}
			exitOpts = append(exitOpts, exitNodeOpt{
				Hostname: n.Hostname,
				Tag:      "tag:exit-" + n.Hostname,
				IP:       ip,
			})
		}
		data["AvailableExitNodes"] = exitOpts
	}
	// 2026-07-17: v0.17.1 — pull the share lists so the
	// "Sharing" section can render. Lookups are best-
	// effort (no error returned to the page) so a
	// transient DB issue doesn't blank the whole page.
	if sub != nil {
		if sharedBy, _ := subnet.ListSharedBy(s.DB, id); sharedBy != nil {
			// Resolve the grantee usernames for display.
			type shareRow struct {
				GranteeID       int64
				GranteeUsername string
				CreatedAt       time.Time
			}
			rows := make([]shareRow, 0, len(sharedBy))
			for _, sb := range sharedBy {
				var uname string
				_ = s.DB.QueryRow(`SELECT username FROM portal_users WHERE id = `+db.PlaceholdersList(1), sb.GranteeUserID).Scan(&uname)
				rows = append(rows, shareRow{sb.GranteeUserID, uname, sb.CreatedAt})
			}
			data["SharedBy"] = rows
		}
		if sharedWith, _ := subnet.ListSharedWith(s.DB, id); sharedWith != nil {
			type incomingRow struct {
				GrantorID       int64
				GrantorUsername string
				CreatedAt       time.Time
			}
			rows := make([]incomingRow, 0, len(sharedWith))
			for _, sw := range sharedWith {
				var uname string
				_ = s.DB.QueryRow(`SELECT username FROM portal_users WHERE id = `+db.PlaceholdersList(1), sw.GrantorUserID).Scan(&uname)
				rows = append(rows, incomingRow{sw.GrantorUserID, uname, sw.CreatedAt})
			}
			data["SharedWith"] = rows
		}
	}
	for k, v := range flash {
		data[k] = v
	}
	s.Backend.RenderWithLayout(w, r, "admin/user_subnet.html", c, data)
}

// GetAdminUserSubnet renders the per-user subnet page.
// The {id} in the path is the portal_user.id.
//
// v0.32.18: supports `?flash=<key>` query parameter for
// post-action success messages from the Remove (and future)
// handlers. The map of flash keys → i18n keys lives in
// flashMessageKeyToI18n below; unknown keys are silently
// ignored (the page renders normally with no banner).
func (s *Service) GetAdminUserSubnet(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	flash := map[string]any{}
	if key := r.URL.Query().Get("flash"); key != "" {
		if i18nKey, ok := subnetFlashMessages[key]; ok {
			flash["FlashMessage"] = i18nKey
		}
	}
	s.renderUserSubnetPage(w, r, c, id, flash)
}

// subnetFlashMessages maps the `?flash=<key>` query value to
// the i18n catalog key for the success message. Keep the keys
// short and stable (they appear in URLs and in the audit log).
// Add new entries when introducing new post-action flashes.
var subnetFlashMessages = map[string]string{
	"removed":          "user_subnet.flash_removed",
	"headscale_failed": "user_subnet.flash_headscale_failed",
	"allocated":        "user_subnet.flash_allocated",
	"disabled":         "user_subnet.flash_disabled",
	"shared":           "user_subnet.flash_shared",
	"revoked":          "user_subnet.flash_revoked",
}

// PostAdminUserSubnetAllocate allocates a personal
// subnet for the user. Idempotent: if the user
// already has a subnet, the existing row is returned
// (no new row, no error). The actual sidecar
// container management is v0.16.1; v0.16.0 just
// creates the row in pending state.
func (s *Service) PostAdminUserSubnetAllocate(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/allocate")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	username, planeURL, _ := s.readUserForSubnetPage(id)
	hostname := fmt.Sprintf("skygate-subnet-%s", username)
	_, err = subnet.Create(s.DB, id, planeURL, hostname)
	if err != nil && !errors.Is(err, subnet.ErrAlreadyExists) {
		s.renderUserSubnetPage(w, r, c, id, map[string]any{
			"FlashError": err.Error(),
		})
		return
	}
	// 2026-07-17: v0.17.1 — auto-reapply the ACL so the
	// new per-user dst rule (with 10.0.<uid>.0/24) is
	// pushed to headscale without the operator having
	// to click "Re-apply ACL" on /admin/exit-rules.
	// v0.17.0 architecture note: the ACL builder
	// generates the per-user rule, but the push only
	// happens on /add_rule / /delrule / /admin/exit-rules
	// /reapply. Allocate now triggers a push too. The
	// operation is idempotent and the re-pushed policy
	// differs only in the new per-user rule (10.0.<uid>.0/24),
	// so the diff is small.
	//
	// Uses the user's own plane (per-plane routing
	// since v0.12.0). For multi-plane users, the HS
	// resolver picks the right client. The call is
	// best-effort: a failure is logged but doesn't
	// fail the Allocate (the row is in the DB; the
	// operator can manually re-apply if needed).
	res := acl.ApplyACLPipelineForPlane(s.DB, s.HSForUserFn(0), planeURL, nil, c.Username,
		fmt.Sprintf("subnet_allocate user=%d", id), false)
	if !res.Applied {
		log.Printf("subnet_allocate: ACL reapply failed for user=%d: %v (row is allocated; click 'Re-apply ACL' to push)",
			id, res.Err)
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d/subnet", id), http.StatusSeeOther)
}

// PostAdminUserSubnetShare grants a portal user
// access to the target user's personal subnet.
// Target user = grantor (whose subnet is being
// shared). The grantee is read from the form's
// `grantee_username` field. The ACL is re-pushed to
// headscale so the new dst entry takes effect
// without the operator manually re-applying.
//
// 2026-07-17: v0.17.1.
func (s *Service) PostAdminUserSubnetShare(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	grantorID, err := extractIDFromAdminPath(r.URL.Path, "/subnet/share")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	granteeName := strings.TrimSpace(r.FormValue("grantee_username"))
	if granteeName == "" {
		s.renderUserSubnetPage(w, r, c, grantorID, map[string]any{
			"FlashError": "missing grantee_username",
		})
		return
	}
	var granteeID int64
	if err := s.DB.QueryRow(
		`SELECT id FROM portal_users WHERE username = `+db.PlaceholdersList(1), granteeName,
	).Scan(&granteeID); err != nil {
		s.renderUserSubnetPage(w, r, c, grantorID, map[string]any{
			"FlashError": fmt.Sprintf("user %q not found", granteeName),
		})
		return
	}
	if err := subnet.Grant(s.DB, grantorID, granteeID); err != nil {
		s.renderUserSubnetPage(w, r, c, grantorID, map[string]any{
			"FlashError": err.Error(),
		})
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "subnet_share_granted",
		fmt.Sprintf("grantor=%d grantee=%d (%s)", grantorID, granteeID, granteeName))
	// Re-apply ACL.
	hs := s.HSForUserFn(0)
	_ = acl.ApplyACLPipelineForPlane(s.DB, hs, "", nil, c.Username,
		fmt.Sprintf("subnet_share_granted grantor=%d grantee=%d", grantorID, granteeID), false)
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d/subnet", grantorID), http.StatusSeeOther)
}

// PostAdminUserSubnetRevoke removes a previously-
// granted share. Like Share, the ACL is re-pushed.
//
// 2026-07-17: v0.17.1.
func (s *Service) PostAdminUserSubnetRevoke(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	grantorID, err := extractIDFromAdminPath(r.URL.Path, "/subnet/revoke")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	granteeIDStr := strings.TrimSpace(r.FormValue("grantee_id"))
	if granteeIDStr == "" {
		http.Error(w, "missing grantee_id", http.StatusBadRequest)
		return
	}
	granteeID, perr := strconv.ParseInt(granteeIDStr, 10, 64)
	if perr != nil {
		http.Error(w, "bad grantee_id", http.StatusBadRequest)
		return
	}
	if err := subnet.Revoke(s.DB, grantorID, granteeID); err != nil {
		s.renderUserSubnetPage(w, r, c, grantorID, map[string]any{
			"FlashError": err.Error(),
		})
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "subnet_share_revoked",
		fmt.Sprintf("grantor=%d grantee=%d", grantorID, granteeID))
	hs := s.HSForUserFn(0)
	_ = acl.ApplyACLPipelineForPlane(s.DB, hs, "", nil, c.Username,
		fmt.Sprintf("subnet_share_revoked grantor=%d grantee=%d", grantorID, granteeID), false)
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d/subnet", grantorID), http.StatusSeeOther)
}

// PostAdminUserSubnetDisable marks the user's subnet
// as disabled (keeps the row for audit but no live
// sidecar). v0.16.1 will call this from the sidecar
// monitor on unrecoverable failure; v0.16.0 ships the
// admin "Disable" button for manual opt-out.
func (s *Service) PostAdminUserSubnetDisable(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/disable")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := subnet.SetStatus(s.DB, id, subnet.StatusDisabled); err != nil {
		s.renderUserSubnetPage(w, r, c, id, map[string]any{
			"FlashError": err.Error(),
		})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d/subnet", id), http.StatusSeeOther)
}

// PostAdminUserSubnetPreferredExit sets (or clears) the
// user's preferred exit-node. Form field `tag` carries
// the headscale tag (e.g. "tag:exit-relay-1"); an empty
// tag clears the preference. The handler also runs an
// ACL re-apply so the new `via: ["<tag>"]` (or its
// removal) takes effect on the next /my/devices load.
//
// 2026-07-24: v0.28.1 — per-user preferred exit-node.
// Visible to admin only; the user can self-set via
// /my/exit-nodes (PostMyExitNodePreferred).
func (s *Service) PostAdminUserSubnetPreferredExit(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/preferred-exit")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	tag := strings.TrimSpace(r.FormValue("tag"))
	// 2026-07-25: v0.28.5 — strict pinning is opt-in.
	// Default OFF for Android compatibility. Admin can
	// explicitly flip the flag to pin a specific user
	// to a specific exit-node (e.g. workstation-3 → relay-3
	// for a Windows box that supports via).
	viaEnabled := r.FormValue("via") == "1"
	if err := db.SetUserExitNodePref(s.DB, id, tag, c.UserID, viaEnabled); err != nil {
		s.renderUserSubnetPage(w, r, c, id, map[string]any{
			"FlashError": fmt.Sprintf("set preferred exit: %v", err),
		})
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "preferred_exit_set",
		fmt.Sprintf("user_id=%d tag=%q via=%v", id, tag, viaEnabled))
	// Re-apply ACL so the via field (or its absence)
	// takes effect on the next device load.
	_, planeURL, _ := s.readUserForSubnetPage(id)
	viaFlag := false
	if s.Cfg != nil {
		viaFlag = s.Cfg.ACLWithViaEnabled
	}
	res := acl.ApplyACLPipelineForPlane(s.DB, s.HSForUserFn(0), planeURL, nil, c.Username,
		fmt.Sprintf("preferred_exit_set user=%d tag=%q", id, tag), viaFlag)
	if !res.Applied {
		log.Printf("preferred_exit_set: ACL reapply failed for user=%d: %v", id, res.Err)
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d/subnet", id), http.StatusSeeOther)
}

// PostAdminUserSubnetProvision issues a per-user preauth key
// (tag:subnet-router, 1h TTL, single-use) so the operator can
// hand it to the user. The user pastes the key into a tailscale
// up command on their sidecar host:
//
//   sudo tailscale up --authkey=<key> \
//     --hostname=skygate-subnet-<username> \
//     --advertise-routes=10.0.<uid>.0/24
//
// The auto-approver (internal/sidecar) watches headscale for
// the new node, approves the route, and flips status to active
// within ~30s.
//
// Idempotency: each click issues a new key. The old key (if
// unused) is left to expire naturally after 1h.
func (s *Service) PostAdminUserSubnetProvision(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/provision")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if s.Sidecar == nil {
		s.renderUserSubnetPage(w, r, c, id, map[string]any{
			"FlashError": "sidecar manager not configured (check SKYGATE_SIDECAR_SYNC_PERIOD env)",
		})
		return
	}
	// Look up the username (needed for the suggested --hostname).
	var username string
	if err := s.DB.QueryRow(`SELECT username FROM portal_users WHERE id = `+db.PlaceholdersList(1), id).Scan(&username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	key, exp, err := s.Sidecar.GeneratePreauth(r.Context(), id)
	if err != nil {
		s.renderUserSubnetPage(w, r, c, id, map[string]any{
			"FlashError": fmt.Sprintf("issue preauth: %v", err),
		})
		return
	}
	info := s.Sidecar.BuildPreauthInfo(id, key, exp, username)
	s.Backend.Audit(c.UserID, c.Username, "subnet_provision", fmt.Sprintf("user_id=%d expires=%s", id, exp.Format(time.RFC3339)))
	s.renderUserSubnetPage(w, r, c, id, map[string]any{
		"FlashPreauth": &info,
	})
}

// on the user's subnet row + the denorm columns on
// portal_users. The check verifies:
//   - user_subnets row exists (else "no subnet" error)
//   - user_subnets.cidr matches portal_users.subnet_cidr
//     (denorm-in-sync check)
//   - user_subnets.status is one of pending/active/disabled
//   - CIDR is valid (parses as net.IPNet)
//
// v0.16.0 ships this as an admin button so the
// operator can catch "denorm got out of sync" bugs
// before they bite (e.g. a future migration that
// touches one table but not the other). The check is
// cheap (~4 reads) and reports all failures at once.
func (s *Service) PostAdminUserSubnetTest(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/test")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	results := s.runSubnetSanityCheck(id)
	s.renderUserSubnetPage(w, r, c, id, map[string]any{
		"FlashTestResult": results,
	})
}

// runSubnetSanityCheck verifies the denorm columns
// match the user_subnets row + the CIDR parses. Returns
// a list of human-readable result lines (one per
// check). The admin UI renders them in a flash card.
func (s *Service) runSubnetSanityCheck(userID int64) []string {
	d := s.DB
	var out []string
	sub, err := subnet.Get(d, userID)
	if err != nil {
		out = append(out, "✗ no user_subnets row (user has not opted in yet)")
		return out
	}
	out = append(out, "✓ user_subnets row found")
	out = append(out, fmt.Sprintf("  cidr: %s", sub.CIDR))
	out = append(out, fmt.Sprintf("  status: %s", sub.Status))
	if sub.Status != subnet.StatusPending && sub.Status != subnet.StatusActive && sub.Status != subnet.StatusDisabled {
		out = append(out, fmt.Sprintf("✗ invalid status %q (expected one of pending/active/disabled)", sub.Status))
	}
	// Denorm check.
	var dCIDR, dStatus string
	if err := d.QueryRow(`SELECT subnet_cidr, subnet_status FROM portal_users WHERE id = ?`, userID).Scan(&dCIDR, &dStatus); err != nil {
		out = append(out, fmt.Sprintf("✗ read denorm: %v", err))
	} else {
		if dCIDR == sub.CIDR {
			out = append(out, "✓ denorm portal_users.subnet_cidr matches")
		} else {
			out = append(out, fmt.Sprintf("✗ denorm out of sync: portal_users.subnet_cidr=%q, user_subnets.cidr=%q", dCIDR, sub.CIDR))
		}
		if dStatus == sub.Status {
			out = append(out, "✓ denorm portal_users.subnet_status matches")
		} else {
			out = append(out, fmt.Sprintf("✗ denorm out of sync: portal_users.subnet_status=%q, user_subnets.status=%q", dStatus, sub.Status))
		}
	}
	return out
}

// extractIDFromAdminPath pulls the {id} from
// /admin/users/{id}/<suffix>. The {id} is the
// last URL segment of the path (after stripping the
// suffix). For /admin/users/3/subnet with suffix
// /subnet, the trimmed path is /admin/users/3 and the
// last segment is "3".
func extractIDFromAdminPath(path, suffix string) (int64, error) {
	// Strip the suffix.
	if len(path) < len(suffix) || path[len(path)-len(suffix):] != suffix {
		return 0, fmt.Errorf("path doesn't end with %q: %s", suffix, path)
	}
	trimmed := path[:len(path)-len(suffix)]
	// Last "/" in trimmed.
	lastSlash := -1
	for i := len(trimmed) - 1; i >= 0; i-- {
		if trimmed[i] == '/' {
			lastSlash = i
			break
		}
	}
	if lastSlash < 0 {
		return 0, fmt.Errorf("no / in path: %s", path)
	}
	raw := trimmed[lastSlash+1:]
	if raw == "" {
		return 0, fmt.Errorf("empty id in path: %s", path)
	}
	// Parse the id manually (avoid strconv import for
	// this one call site).
	var id int64
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("bad id %q in path %s", raw, path)
		}
		id = id*10 + int64(c-'0')
	}
	if id == 0 {
		return 0, fmt.Errorf("zero id in path: %s", path)
	}
	return id, nil
}
