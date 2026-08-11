// user_subnet_remove.go — admin "Remove subnet-router" handler (v0.32.18).
//
// This is the inverse of PostAdminUserSubnetProvision + the
// sidecar's auto-approver. When the user (or operator) decides
// the subnet-router device is dead/retired/unwanted, this
// handler cleans up all the related state in one shot:
//   1. Deletes the headscale node (the tailscaled instance
//      that was running on the user's router host)
//   2. Clears user_subnets.router_node_id + router_hostname,
//      sets status back to 'pending' (so a new preauth can
//      be issued and a new device registered cleanly)
//   3. Clears portal_users denorm fields (subnet_status='pending',
//      subnet_cidr='', subnet_router_*='')
//   4. Writes an audit_log entry with the headscale node id
//      that was deleted
//
// The handler is idempotent: if the user_subnets row exists but
// router_node_id is empty (e.g. admin hit Remove twice), step 1
// is skipped and steps 2-4 still run. If the user_subnets row
// does not exist at all (user never allocated), the handler
// returns a http.StatusNotFound — there is nothing to remove.
//
// The ACL policy is NOT re-applied here. h-user-admin-subnet
// is always in the per-user grant (it maps to 10.0.<uid>.0/24
// regardless of whether a router is active), so removing the
// headscale node doesn't change the policy. Re-applying would
// just add a row to acl_snapshots with no diff.
//
// 2026-07-25: added the second half of the lifecycle —
// Provision creates, Remove destroys. v0.32.18 ships Remove
// as the missing piece. The Disable button remains as a
// softer "opt out without losing the row" option; Remove is
// the full cleanup.

package admin

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"skygate/internal/subnet"
)

// PostAdminUserSubnetRemove handles the admin "Remove subnet-router"
// button on /admin/users/{id}/subnet. The full lifecycle:
//
//	POST /admin/users/{id}/subnet/remove
//
// Admin-only. Idempotent. See file docstring for the full
// semantics.
func (s *Service) PostAdminUserSubnetRemove(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := extractIDFromAdminPath(r.URL.Path, "/subnet/remove")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}

	// 1. Read user_subnets. Missing row → http.StatusNotFound.
	sub, err := subnet.Get(s.DB, id)
	if err != nil {
		if errors.Is(err, subnet.ErrNotFound) {
			http.Error(w, "no subnet row for this user (run /subnet/allocate first)", http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("read user_subnets: %v", err), http.StatusInternalServerError)
		return
	}

	// 2. Delete the headscale node if there is one. The DB
	//    cleanup below is the source of truth for the
	//    skygate side; if headscale delete fails (node
	//    already gone, network glitch, etc.) we still
	//    proceed so the admin isn't stuck with a stale
	//    row that re-blocks the user.
	deletedHeadscaleID := int64(0)
	headscaleErr := ""
	if sub.RouterNodeID != "" {
		nid, perr := strconv.ParseInt(sub.RouterNodeID, 10, 64)
		if perr != nil || nid <= 0 {
			// router_node_id is non-empty but unparseable.
			// This shouldn't happen — the sidecar's
			// SetRouter stores the headscale numeric
			// ID. Log and proceed with DB cleanup.
			log.Printf("subnet_remove: user=%d router_node_id=%q is not a valid int (treating as 'no headscale node')", id, sub.RouterNodeID)
		} else if hs := s.HSForUserFn(0); hs != nil {
			if derr := hs.DeleteNode(nid); derr != nil {
				headscaleErr = derr.Error()
				log.Printf("subnet_remove: headscale delete node %d failed for user=%d: %v (continuing with DB cleanup)", nid, id, derr)
			} else {
				deletedHeadscaleID = nid
			}
		} else {
			log.Printf("subnet_remove: headscale client is nil for user=%d (skipped headscale delete)", id)
		}
	}

	// 3. Clear user_subnets fields. SET to 'pending' so
	//    the user can re-provision a new router later
	//    (admin clicks Provision, gets a new preauth,
	//    new device registers, sidecar re-approves).
	if _, err := s.DB.Exec(
		`UPDATE user_subnets
		   SET status = $1, router_node_id = '', router_hostname = '',
		       updated_at = strftime('%s','now')
		 WHERE user_id = $2`,
		subnet.StatusPending, id,
	); err != nil {
		http.Error(w, fmt.Sprintf("update user_subnets: %v", err), http.StatusInternalServerError)
		return
	}

	// 4. Clear portal_users denorm fields. The
	//    `subnet_cidr` and `subnet_router_*` columns
	//    are NOT NULL DEFAULT '' in v0.16.0, so we set
	//    them to empty string (not NULL) per the
	//    constraint contract.
	if _, err := s.DB.Exec(
		`UPDATE portal_users
		   SET subnet_status = $1, subnet_cidr = '',
		       subnet_router_node_id = '', subnet_router_hostname = ''
		 WHERE id = $2`,
		subnet.StatusPending, id,
	); err != nil {
		http.Error(w, fmt.Sprintf("update portal_users: %v", err), http.StatusInternalServerError)
		return
	}

	// 5. Audit log. The detail string includes the
	//    headscale node id that was deleted (or 0 if
	//    there wasn't one), so an operator can find
	//    this entry via grep on the row id.
	detail := fmt.Sprintf("user_id=%d deleted_headscale_node_id=%d", id, deletedHeadscaleID)
	if headscaleErr != "" {
		detail += fmt.Sprintf(" headscale_error=%q (DB cleaned, headscale may still have the node)", headscaleErr)
	}
	s.Backend.Audit(c.UserID, c.Username, "subnet_router_removed", detail)

	// 6. Redirect to the subnet page with a flash.
	//    The flash maps to user_subnet.flash_removed
	//    (success) or user_subnet.flash_headscale_failed
	//    (partial) in the i18n catalog.
	flashKey := "removed"
	if headscaleErr != "" {
		flashKey = "headscale_failed"
	}
	redirectURL := fmt.Sprintf("/admin/users/%d/subnet?flash=%s", id, flashKey)
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}
