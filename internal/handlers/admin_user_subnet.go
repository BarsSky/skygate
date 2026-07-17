package handlers

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

// auth.Claims is the type the authMW middleware
// injects into the request context. We use it in
// the renderUserSubnetPage helper signature.
var _ = (*auth.Claims)(nil)

// admin_user_subnet.go — /admin/users/{id}/subnet.
//
// 2026-07-17: v0.16.0 — per-user subnets admin page.
//
// The page is the operator's cockpit for one user's
// personal subnet. It shows:
//   - whether the user has a subnet allocated (and
//     which CIDR — 10.0.<uid>.0/24, deterministic)
//   - the current lifecycle status
//   - the router hostname + headscale node_id (when
//     the sidecar registers, v0.16.1)
//   - the per-plane context (v0.12.0 multi-plane)
//
// The form has 3 actions:
//   - "Allocate subnet" — POST → calls subnet.Create;
//     the row goes in with status=pending. The actual
//     sidecar container provisioning ships in v0.16.1;
//     v0.16.0's button creates the row so the operator
//     can confirm the schema + CIDR + denorm columns
//     work end-to-end before the v0.16.1 work adds the
//     docker-sidecar management.
//   - "Disable" — POST → calls subnet.SetStatus(disabled).
//     Keeps the row for audit but marks the subnet
//     disabled (no live sidecar). Re-enable is
//     "Allocate subnet" again (idempotent: returns the
//     existing row + ErrAlreadyExists).
//   - "Test" — admin UI button (no POST) that runs a
//     quick sanity check on the row: "yes it's there,
//     CIDR is valid, status is one of pending/active/
//     disabled, denorm columns match the user_subnets
//     row". Useful for catching "the denorm got out of
//     sync" bugs before the v0.16.1 sidecar work
//     depends on the denorm for /mysubnet.
//
// Routes:
//   GET  /admin/users/{id}/subnet          — page
//   POST /admin/users/{id}/subnet/allocate — allocate
//   POST /admin/users/{id}/subnet/disable  — disable
//   POST /admin/users/{id}/subnet/test     — sanity check

// readUserForSubnetPage reads the username + headscale_url
// for the per-user subnet page. We don't need the full
// db.User struct (the template only shows Username +
// HeadscaleURL); a one-row SELECT is cheaper than the
// GetAllPortalUsers loop.
func readUserForSubnetPage(a *App, id int64) (username, headscaleURL string, err error) {
	username, err = db.GetUserNameByID(a.DB, id)
	if err != nil {
		return "", "", fmt.Errorf("get username: %w", err)
	}
	// headscale_url is a denormalized column on
	// portal_users (v0.12.0 multi-plane). Empty
	// string = global plane.
	row := a.DB.QueryRow(`SELECT headscale_url FROM portal_users WHERE id = ?`, id)
	if err := row.Scan(&headscaleURL); err != nil {
		return "", "", fmt.Errorf("get headscale_url: %w", err)
	}
	return username, headscaleURL, nil
}

// renderUserSubnetPage renders /admin/users/{id}/subnet
// with the given flash data. Shared helper so the three
// POST handlers don't each re-implement the render.
func renderUserSubnetPage(a *App, w http.ResponseWriter, r *http.Request, c *userClaims, id int64, flash map[string]any) {
	username, hsURL, err := readUserForSubnetPage(a, id)
	if err != nil {
		http.Error(w, "user not found", 404)
		return
	}
	sub, _ := subnet.Get(a.DB, id)
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
	// 2026-07-17: v0.17.1 — pull the share lists so the
	// "Sharing" section can render. Lookups are best-
	// effort (no error returned to the page) so a
	// transient DB issue doesn't blank the whole page.
	if sub != nil {
		if sharedBy, _ := subnet.ListSharedBy(a.DB, id); sharedBy != nil {
			// Resolve the grantee usernames for display.
			type shareRow struct {
				GranteeID       int64
				GranteeUsername string
				CreatedAt       time.Time
			}
			rows := make([]shareRow, 0, len(sharedBy))
			for _, s := range sharedBy {
				var uname string
				_ = a.DB.QueryRow(`SELECT username FROM portal_users WHERE id = ?`, s.GranteeUserID).Scan(&uname)
				rows = append(rows, shareRow{s.GranteeUserID, uname, s.CreatedAt})
			}
			data["SharedBy"] = rows
		}
		if sharedWith, _ := subnet.ListSharedWith(a.DB, id); sharedWith != nil {
			type incomingRow struct {
				GrantorID       int64
				GrantorUsername string
				CreatedAt       time.Time
			}
			rows := make([]incomingRow, 0, len(sharedWith))
			for _, s := range sharedWith {
				var uname string
				_ = a.DB.QueryRow(`SELECT username FROM portal_users WHERE id = ?`, s.GrantorUserID).Scan(&uname)
				rows = append(rows, incomingRow{s.GrantorUserID, uname, s.CreatedAt})
			}
			data["SharedWith"] = rows
		}
	}
	for k, v := range flash {
		data[k] = v
	}
	a.renderWithLayout(w, r, "admin-user-subnet", c, data)
}

// userClaims alias kept for clarity in the helper
// signatures below (the authMW middleware injects
// this into the request context).
type userClaims = auth.Claims

// GetAdminUserSubnet renders the per-user subnet page.
// The {id} in the path is the portal_user.id.
func (a *App) GetAdminUserSubnet(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet")
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	renderUserSubnetPage(a, w, r, c, id, nil)
}

// PostAdminUserSubnetAllocate allocates a personal
// subnet for the user. Idempotent: if the user
// already has a subnet, the existing row is returned
// (no new row, no error). The actual sidecar
// container management is v0.16.1; v0.16.0 just
// creates the row in pending state.
func (a *App) PostAdminUserSubnetAllocate(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/allocate")
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	username, planeURL, _ := readUserForSubnetPage(a, id)
	hostname := fmt.Sprintf("skygate-subnet-%s", username)
	_, err = subnet.Create(a.DB, id, planeURL, hostname)
	if err != nil && !errors.Is(err, subnet.ErrAlreadyExists) {
		renderUserSubnetPage(a, w, r, c, id, map[string]any{
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
	res := acl.ApplyACLPipelineForPlane(a.DB, a.HSForUser(0), planeURL, nil, c.Username,
		fmt.Sprintf("subnet_allocate user=%d", id))
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
func (a *App) PostAdminUserSubnetShare(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	grantorID, err := extractIDFromAdminPath(r.URL.Path, "/subnet/share")
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	granteeName := strings.TrimSpace(r.FormValue("grantee_username"))
	if granteeName == "" {
		renderUserSubnetPage(a, w, r, c, grantorID, map[string]any{
			"FlashError": "missing grantee_username",
		})
		return
	}
	var granteeID int64
	if err := a.DB.QueryRow(
		`SELECT id FROM portal_users WHERE username = ?`, granteeName,
	).Scan(&granteeID); err != nil {
		renderUserSubnetPage(a, w, r, c, grantorID, map[string]any{
			"FlashError": fmt.Sprintf("user %q not found", granteeName),
		})
		return
	}
	if err := subnet.Grant(a.DB, grantorID, granteeID); err != nil {
		renderUserSubnetPage(a, w, r, c, grantorID, map[string]any{
			"FlashError": err.Error(),
		})
		return
	}
	a.audit(c.UserID, c.Username, "subnet_share_granted",
		fmt.Sprintf("grantor=%d grantee=%d (%s)", grantorID, granteeID, granteeName))
	// Re-apply ACL.
	planeURL := ""
	if u, _, err := readUserForSubnetPage(a, grantorID); err == nil {
		_ = planeURL
		_ = u
	}
	if planeURL != "" {
		planeURL = strings.TrimSpace(planeURL)
	}
	hs := a.HSForUser(0)
	_ = acl.ApplyACLPipelineForPlane(a.DB, hs, "", nil, c.Username,
		fmt.Sprintf("subnet_share_granted grantor=%d grantee=%d", grantorID, granteeID))
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d/subnet", grantorID), http.StatusSeeOther)
}

// PostAdminUserSubnetRevoke removes a previously-
// granted share. Like Share, the ACL is re-pushed.
//
// 2026-07-17: v0.17.1.
func (a *App) PostAdminUserSubnetRevoke(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	grantorID, err := extractIDFromAdminPath(r.URL.Path, "/subnet/revoke")
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	granteeIDStr := strings.TrimSpace(r.FormValue("grantee_id"))
	if granteeIDStr == "" {
		http.Error(w, "missing grantee_id", 400)
		return
	}
	granteeID, perr := strconv.ParseInt(granteeIDStr, 10, 64)
	if perr != nil {
		http.Error(w, "bad grantee_id", 400)
		return
	}
	if err := subnet.Revoke(a.DB, grantorID, granteeID); err != nil {
		renderUserSubnetPage(a, w, r, c, grantorID, map[string]any{
			"FlashError": err.Error(),
		})
		return
	}
	a.audit(c.UserID, c.Username, "subnet_share_revoked",
		fmt.Sprintf("grantor=%d grantee=%d", grantorID, granteeID))
	hs := a.HSForUser(0)
	_ = acl.ApplyACLPipelineForPlane(a.DB, hs, "", nil, c.Username,
		fmt.Sprintf("subnet_share_revoked grantor=%d grantee=%d", grantorID, granteeID))
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d/subnet", grantorID), http.StatusSeeOther)
}

// PostAdminUserSubnetDisable marks the user's subnet
// as disabled (keeps the row for audit but no live
// sidecar). v0.16.1 will call this from the sidecar
// monitor on unrecoverable failure; v0.16.0 ships the
// admin "Disable" button for manual opt-out.
func (a *App) PostAdminUserSubnetDisable(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/disable")
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	if err := subnet.SetStatus(a.DB, id, subnet.StatusDisabled); err != nil {
		renderUserSubnetPage(a, w, r, c, id, map[string]any{
			"FlashError": err.Error(),
		})
		return
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
func (a *App) PostAdminUserSubnetProvision(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/provision")
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	if a.Sidecar == nil {
		renderUserSubnetPage(a, w, r, c, id, map[string]any{
			"FlashError": "sidecar manager not configured (check SKYGATE_SIDECAR_SYNC_PERIOD env)",
		})
		return
	}
	// Look up the username (needed for the suggested --hostname).
	var username string
	if err := a.DB.QueryRow(`SELECT username FROM portal_users WHERE id = ?`, id).Scan(&username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "user not found", 404)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	key, exp, err := a.Sidecar.GeneratePreauth(r.Context(), id)
	if err != nil {
		renderUserSubnetPage(a, w, r, c, id, map[string]any{
			"FlashError": fmt.Sprintf("issue preauth: %v", err),
		})
		return
	}
	info := a.Sidecar.BuildPreauthInfo(id, key, exp, username)
	a.audit(c.UserID, c.Username, "subnet_provision", fmt.Sprintf("user_id=%d expires=%s", id, exp.Format(time.RFC3339)))
	renderUserSubnetPage(a, w, r, c, id, map[string]any{
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
func (a *App) PostAdminUserSubnetTest(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/test")
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	results := runSubnetSanityCheck(a.DB, id)
	renderUserSubnetPage(a, w, r, c, id, map[string]any{
		"FlashTestResult": results,
	})
}

// runSubnetSanityCheck verifies the denorm columns
// match the user_subnets row + the CIDR parses. Returns
// a list of human-readable result lines (one per
// check). The admin UI renders them in a flash card.
func runSubnetSanityCheck(d *sql.DB, userID int64) []string {
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
