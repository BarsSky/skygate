// admin_toggle_b520.go — B520: the admin page can switch another user's rule on and
// off, and can SEE the rules it switched off.
//
// WHY THIS IS NOT THE /my HANDLER WITH A DIFFERENT PATH: three things differ, and
// each of them is a way to get it wrong.
//
//  1. THE OWNER. The UPDATE is owner-scoped (`db.SetDeviceRulesEnabled` filters
//     `user_id`), and the admin does not know the owner of an arbitrary id — it is
//     resolved per id through `db.GetRuleAdminContext`, the same helper B379 uses for
//     the admin delete, so both admin mutations agree on who a row belongs to.
//  2. THE AUDIT ACTOR. The row belongs to the owner, the decision belongs to the
//     admin: `ruleStateChange` carries both, and the audit row names the ADMIN while
//     the detail names the owner (`admin skyadmin for michail disabled 3 rule(s)`).
//     Writing the owner into the actor column would make the trail claim the user
//     switched off their own rule.
//  3. THE VISIBILITY. The admin list is enabled-only by construction (B358), so a
//     switched-off rule would vanish — and a rule you cannot see is a rule you cannot
//     switch back on. The page therefore offers `?show_disabled=1` (the `All`
//     statement variants in the db layer), and this handler PRESERVES that flag in
//     the return URL so the operator stays in the view they were reading.
package exit_rules

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/db"
)

// PostAdminExitRuleToggle handles POST /admin/exit-rules/toggle (admin only).
//
// Fields:
//
//	id            one rule id (the per-row switch)
//	ids[]         several rule ids (the tree's checkboxes, if a caller sends them)
//	enabled       "1"/"0" — the state the operator asked for
//	show_disabled "1" — preserve the switched-off view on the way back
//	back          the page to return to (validated against the admin path)
func (s *Service) PostAdminExitRuleToggle(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, adminRulesFlash(r.FormValue("back"), map[string]string{"admin_err": "the form could not be read"}), http.StatusSeeOther)
		return
	}
	back := r.FormValue("back")
	// The switched-off view survives the action: the operator pressed a button IN
	// that view and must not be thrown back into the enabled-only one (where the row
	// they just switched off would be gone). Every redirect below goes through this
	// helper, so no path can forget the flag.
	showDisabled := strings.TrimSpace(r.FormValue("show_disabled")) == "1"
	flash := func(kv map[string]string) string {
		if showDisabled {
			kv["show_disabled"] = "1"
		}
		return adminRulesFlash(back, kv)
	}

	desired, ok := ruleToggleDesired(r.FormValue("enabled"))
	if !ok {
		http.Redirect(w, r, flash(map[string]string{"admin_err": "the request named no state"}), http.StatusSeeOther)
		return
	}

	rawIDs := append([]string{}, r.Form["ids"]...)
	if v := strings.TrimSpace(r.FormValue("id")); v != "" {
		rawIDs = append(rawIDs, v)
	}
	if len(rawIDs) == 0 {
		http.Redirect(w, r, flash(map[string]string{"admin_err": "no rule was named"}), http.StatusSeeOther)
		return
	}

	// Group the ids by OWNER: the UPDATE is owner-scoped, and one audit row per owner
	// is what makes the trail readable ("admin X switched off 3 rules of michail").
	type ownerBatch struct {
		id   int64
		name string
		ids  []int
	}
	byOwner := map[int64]*ownerBatch{}
	order := make([]int64, 0, len(rawIDs))
	for _, raw := range rawIDs {
		id, perr := strconv.Atoi(strings.TrimSpace(raw))
		if perr != nil || id <= 0 {
			continue
		}
		ctx, cerr := db.GetRuleAdminContext(s.dbc(), id)
		if cerr != nil {
			// A stale id (deleted in another tab) is skipped, not fatal: the other
			// rows the operator selected must still be applied.
			continue
		}
		b := byOwner[ctx.UserID]
		if b == nil {
			b = &ownerBatch{id: ctx.UserID, name: ctx.UserName}
			byOwner[ctx.UserID] = b
			order = append(order, ctx.UserID)
		}
		b.ids = append(b.ids, id)
	}
	if len(order) == 0 {
		http.Redirect(w, r, flash(map[string]string{"admin_err": "no such rule"}), http.StatusSeeOther)
		return
	}

	var changed int64
	for _, ownerID := range order {
		b := byOwner[ownerID]
		n, err := s.setRulesEnabled(ruleStateChange{
			OwnerID:   b.id,
			IDs:       b.ids,
			Enabled:   desired,
			Kind:      "admin page",
			Subject:   fmt.Sprintf("admin %s for %s", c.Username, b.name),
			AuditID:   c.UserID,
			AuditName: c.Username,
			Action:    "admin_rule_",
		})
		if err != nil {
			http.Redirect(w, r, flash(map[string]string{"admin_err": err.Error()}), http.StatusSeeOther)
			return
		}
		changed += n
	}
	if changed == 0 {
		http.Redirect(w, r, flash(map[string]string{"admin_err": "nothing changed"}), http.StatusSeeOther)
		return
	}

	state := "off"
	if desired {
		state = "on"
	}
	http.Redirect(w, r, flash(map[string]string{
		"admin_toggled": state,
		"admin_n":       strconv.FormatInt(changed, 10),
	}), http.StatusSeeOther)
}
