// admin_delete_b379.go — the admin-side per-rule delete (B379, closing TD-29).
//
// THE DEFECT THIS CLOSES (measured 2026-10-11, filed as TD-29 while B376 was being
// written). Both delete controls on /admin/exit-rules post to
// `/admin/exit-rules/delete`, and **no mux.Handle registered that path**: a grep over
// the whole tree found the two forms and zero server-side matches, so clicking delete
// on the admin page — for any rule, of any user — answered 404. `PostDeleteExitRule`
// exists, but it is registered only under `/my/exit-rules/delete` and it deletes by the
// row's own `user_id`, i.e. it can only ever delete the CALLER's rules. An admin
// therefore had no way at all to remove a rule from another user's device; the workaround
// was to impersonate the user's page or edit the database by hand.
//
// WHY IT IS NOT A ONE-LINE `DeleteRule(id)`
// ----------------------------------------
// The /my delete is not a single DELETE — it is a decision tree (form_my.go):
//
//   - `all_devices = 1`  → cascade the whole fan-out group (B277.4), because each device
//     carries its own copy of the logical rule and deleting the clicked row alone leaves
//     N-1 ghosts;
//   - `target_type = 'domain'` with a `parent_domain` → also delete the derived /32 rows
//     (B123/B184), because they are what the ACL actually pins and the domain auto-updater
//     re-resolves them on its next tick;
//   - otherwise → delete the one row.
//
// Every one of those helpers is scoped by `user_id`, and the scope is load-bearing: the
// cascade must not reach across users. So the admin path reads the rule's OWNER first
// (`db.GetRuleAdminContext`, the one helper that reads a rule without a user filter) and
// then runs exactly the owner-scoped deletes the owner's own page runs. Copying the tree
// is what makes the two paths agree by construction rather than by review.
//
// A rule that does not exist is reported, not silently ignored (the /my path skips it,
// which is right for a checkbox list and wrong for a single explicit admin click).
package exit_rules

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"skygate/internal/db"
)

// PostAdminExitRuleDelete handles POST /admin/exit-rules/delete (admin only).
//
// Fields: `id` (single, what the template posts) and/or `ids` (multi-value, so a future
// bulk control works without a second route).
func (s *Service) PostAdminExitRuleDelete(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Defense in depth, exactly like PostAdminExitRule: the route is already behind the
	// admin-scoped middleware, and this handler still refuses on its own.
	if !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rawIDs := make([]string, 0, 4)
	for _, v := range r.Form["ids"] {
		if v != "" {
			rawIDs = append(rawIDs, v)
		}
	}
	if v := r.FormValue("id"); v != "" {
		rawIDs = append(rawIDs, v)
	}
	if len(rawIDs) == 0 {
		http.Redirect(w, r, adminDeleteRedirect("missing rule id"), http.StatusFound)
		return
	}

	deleted := 0
	cascade := 0
	fanOut := 0
	missing := 0
	for _, raw := range rawIDs {
		id, aerr := strconv.Atoi(raw)
		if aerr != nil || id <= 0 {
			missing++
			continue
		}
		ctx, err := db.GetRuleAdminContext(s.dbc(), id)
		if err != nil {
			// NotFound and "the read failed" are reported the same way here on purpose:
			// the operator's next action is the same (reload and look), and the audit row
			// below records the attempt either way.
			missing++
			continue
		}

		// The owner-scoped deletes, in the same order and with the same predicates the
		// owner's own PostDeleteExitRule uses.
		switch {
		case ctx.AllDevices:
			if n, derr := db.DeleteAllDeviceFanOut(s.dbc(), ctx.UserID, ctx.ExitNode, ctx.TargetType, ctx.TargetValue); derr == nil {
				deleted += n
				if n > 1 {
					fanOut += n - 1
				}
			}
		case ctx.TargetType == "domain" && ctx.ParentDomain != "":
			if n, derr := db.DeleteRuleOrCascadeByParentDomain(s.dbc(), ctx.UserID, id, ctx.ParentDomain); derr == nil {
				deleted += int(n)
				if n > 1 {
					cascade += int(n) - 1
				}
			}
		default:
			// Still owner-scoped: GetRuleAdminContext told us who owns it, and
			// DeleteRuleForUser re-checks that owner at DELETE time, so a row that changed
			// hands between the two statements cannot be removed by accident.
			if derr := db.DeleteRuleForUser(s.dbc(), id, ctx.UserID); derr == nil {
				deleted++
			}
		}

		s.Backend.Audit(c.UserID, c.Username, "admin_delete_exit_rule_for_user",
			fmt.Sprintf("rule=%d owner=%s (uid=%d) device=%d exit=%s target=%s all_devices=%t parent_domain=%q",
				id, ctx.UserName, ctx.UserID, ctx.DeviceID, ctx.ExitNode,
				ctx.TargetType+" "+ctx.TargetValue, ctx.AllDevices, ctx.ParentDomain))
	}

	if deleted == 0 {
		if missing > 0 {
			http.Redirect(w, r, adminDeleteRedirect(
				fmt.Sprintf("no rule was deleted: %d id(s) not found (already deleted, or never existed)", missing)),
				http.StatusFound)
			return
		}
		http.Redirect(w, r, adminDeleteRedirect("nothing to delete"), http.StatusFound)
		return
	}

	// Apply the ACL, but only on real drift — the same semantics as both other delete
	// paths, so a delete whose effect the live policy already reflects does not spend a
	// SetPolicy round trip (and does not restart headscale for nothing, B298).
	detail := fmt.Sprintf("admin %s deleted %d rule(s)", c.Username, deleted)
	if cascade > 0 {
		detail += fmt.Sprintf(" (cascade: %d /32)", cascade)
	}
	if fanOut > 0 {
		detail += fmt.Sprintf(" (fan-out cascade: %d)", fanOut)
	}
	if res := s.applyACLIfDrifted("admin-rule-delete", detail); res.Err != nil {
		db.MarkACLFail(s.dbc(), res.Version, res.Err.Error())
	}

	target := fmt.Sprintf("/admin/exit-rules?deleted=%d", deleted)
	if cascade > 0 {
		target += "&deleted_cascade=" + url.QueryEscape(strconv.Itoa(cascade))
	}
	if fanOut > 0 {
		target += "&deleted_fanout=" + url.QueryEscape(strconv.Itoa(fanOut))
	}
	if missing > 0 {
		target += "&warn=" + url.QueryEscape(
			fmt.Sprintf("%d id(s) were not found and were left alone", missing))
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// adminDeleteRedirect builds the error redirect for this handler. It deliberately does
// NOT carry the add-form's `form_*` fields: a failed DELETE has no form state to
// preserve, and re-rendering the add form with a stale target user would be worse than
// re-rendering it empty.
func adminDeleteRedirect(msg string) string {
	return "/admin/exit-rules?err=" + url.QueryEscape(msg)
}
