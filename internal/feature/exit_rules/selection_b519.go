// selection_b519.go — B519: act on a SELECTION of rows.
//
// THE REGRESSION THIS CLOSES: the pre-B516 page had checkboxes and a multi-delete
// bar; the group page (the default since B517) had neither, so «select these twelve
// subnets and do one thing with them» became impossible — the only tools were the
// per-row switch and the whole-card verbs. A rework that takes a capability away
// without saying so is exactly the kind of regression the block history keeps
// recording, so it is restored here on the new page, with the verbs the new page
// actually has.
//
// HOW IT IS BUILT, and why: ONE form (id="rules-selection") plus checkboxes bound
// to it by the HTML `form` attribute (a form may not wrap table rows, and
// duplicating the checkbox set per action is how the two copies drift). Every verb
// is a SUBMIT BUTTON with its own `name="op"`, and «удалить» overrides the action
// with `formaction` — so the browser posts one form to the delete endpoint, which
// already owns the cascade (B123/B277.4). No JavaScript is needed for the action;
// the small inline script only maintains the counter and the select-all box.
//
// The three ops here reuse the shared writers instead of re-implementing them:
//
//	group   → db.SetDeviceRulesGroupLabel (the B518 writer)
//	disable → setRulesEnabled(false)      (the B517 tail: audit + ACL + routes)
//	enable  → setRulesEnabled(true)
//
// Delete is deliberately NOT routed through this handler: the cascade lives in
// PostDeleteExitRule, and a second implementation of «delete this domain with its
// subnets» is how one of the two loses the cascade.
package exit_rules

import (
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/db"
)

// selectionFlashKeys are the query keys a selection outcome may set. Centralised
// so rulesActionFlash can clear them before writing a new outcome (an old banner
// surviving a new action is the «did it work?» confusion this page keeps fixing).
var selectionFlashKeys = []string{"selection_op", "selection_n"}

// PostMyExitRuleSelection handles POST /my/exit-rules/selection.
//
// Fields:
//
//	op       "group" | "disable" | "enable"
//	ids[]    the checked rule ids
//	group_to the label for op=group ("" removes the rules from their group)
//	back     the page to return to (validated)
func (s *Service) PostMyExitRuleSelection(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, rulesActionFlash(r.FormValue("back"), map[string]string{"source_err": "the form could not be read"}), http.StatusSeeOther)
		return
	}
	back := r.FormValue("back")
	op := strings.ToLower(strings.TrimSpace(r.FormValue("op")))

	rows, err := s.ownerRuleRows(c.UserID)
	if err != nil {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": err.Error()}), http.StatusSeeOther)
		return
	}
	// scope is not posted by the bar: a selection is always an explicit id list,
	// and every id is intersected with the caller's own rows.
	ids, _, aerr := s.cardActionIDs(r, rows)
	if aerr != nil {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": aerr.Error()}), http.StatusSeeOther)
		return
	}
	if len(ids) == 0 {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{
			"source_err": "nothing is selected — tick the rules first",
		}), http.StatusSeeOther)
		return
	}

	intIDs := make([]int, 0, len(ids))
	for _, id := range ids {
		intIDs = append(intIDs, int(id))
	}

	switch op {
	case "group":
		label := NormaliseGroupLabel(r.FormValue("group_to"))
		n, gerr := db.SetDeviceRulesGroupLabel(s.dbc(), c.UserID, intIDs, label)
		if gerr != nil {
			http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": gerr.Error()}), http.StatusSeeOther)
			return
		}
		verb := "filed"
		if label == "" {
			verb = "cleared the group of"
		}
		detail := "user " + c.Username + " " + verb + " " + strconv.FormatInt(n, 10) + " rule(s) (selection)"
		if label != "" {
			detail += ", group \"" + label + "\""
		}
		s.Backend.Audit(c.UserID, c.Username, "exit_rule_group_assign", detail)
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{
			"group_assigned": strconv.FormatInt(n, 10),
			"assigned_to":    label,
		}), http.StatusSeeOther)

	case "disable", "enable":
		enabled := op == "enable"
		changed, serr := s.setRulesEnabled(ruleStateChange{
			OwnerID:   c.UserID,
			IDs:       intIDs,
			Enabled:   enabled,
			Kind:      "selection",
			Subject:   "user " + c.Username,
			AuditID:   c.UserID,
			AuditName: c.Username,
			Action:    "exit_rule_",
		})
		if serr != nil {
			http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": serr.Error()}), http.StatusSeeOther)
			return
		}
		state := "off"
		if enabled {
			state = "on"
		}
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{
			"selection_op": state,
			"selection_n":  strconv.FormatInt(changed, 10),
		}), http.StatusSeeOther)

	default:
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": "unknown action"}), http.StatusSeeOther)
	}
}
