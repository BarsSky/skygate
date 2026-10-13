// group_actions_b380.go — renaming and clearing the operator's own rule groups (B380).
//
// WHY A SEPARATE ACTION, and not "the label changes when you add the same rule again":
// the natural key of a rule (user, device, exit node, type, value, parent_domain) does
// not include the label — deliberately, because the label is presentation and must never
// change WHICH rule exists (B123/B183's duplicate semantics depend on that key). So an
// insert whose key already exists is a duplicate and changes nothing, which would make
// "I want to rename my «Игры» group" impossible. This handler is that missing verb: one
// action, scoped to the caller's own rows, that renames a label everywhere it appears
// (or clears it when the target is empty).
//
// NO ACL RE-APPLY, and that is a property, not an omission: the label never reaches
// GenerateACL, prefix_owner, the routed prefix set or the exit-node choice. A rename is
// therefore free of routing consequences — and the audit row says so, so nobody has to
// read the ACL code to believe it.
package exit_rules

import (
	"fmt"
	"net/http"
	"net/url"

	"skygate/internal/db"
)

// PostMyExitRuleGroup handles POST /my/exit-rules/group.
//
// Fields:
//
//	group_from — the label to rename (required; normalised)
//	group_to   — the new label ("" clears the group)
func (s *Service) PostMyExitRuleGroup(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	from := NormaliseGroupLabel(r.FormValue("group_from"))
	to := NormaliseGroupLabel(r.FormValue("group_to"))
	if from == "" {
		http.Redirect(w, r, groupFlash("", "no group was named to rename"), http.StatusFound)
		return
	}
	if from == to {
		// Idempotent no-op rather than an error: the operator pressed the button on a
		// form they did not change, and saying "nothing to do" is friendlier than a
		// red banner — and truthful, because the state they asked for already holds.
		http.Redirect(w, r, groupFlash(from, ""), http.StatusFound)
		return
	}

	n, err := db.SetDeviceRuleGroup(s.dbc(), c.UserID, from, to)
	if err != nil {
		http.Redirect(w, r, groupFlash(from, err.Error()), http.StatusFound)
		return
	}
	if n == 0 {
		// The label they typed does not exist on their rules. Said out loud: silently
		// redirecting to an unchanged page is how a user concludes the button is
		// broken (the same honesty rule the delete path follows).
		http.Redirect(w, r, groupFlash(from,
			fmt.Sprintf("no rule of yours carries the group %q", from)), http.StatusFound)
		return
	}

	detail := fmt.Sprintf("renamed group %q -> %q on %d rule(s)", from, to, n)
	if to == "" {
		detail = fmt.Sprintf("cleared group %q on %d rule(s)", from, n)
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_rule_group_rename", detail)

	target := "/my/exit-rules?view=group&group_renamed=" + url.QueryEscape(fmt.Sprintf("%d", n))
	if to == "" {
		target += "&group_cleared=1"
	} else {
		target += "&group_to=" + url.QueryEscape(to)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// groupFlash builds the error / no-op redirect back to the group view, keeping the label
// the operator typed so the form re-renders with it instead of empty.
func groupFlash(from, errMsg string) string {
	out := "/my/exit-rules?view=group"
	if from != "" {
		out += "&group_to=" + url.QueryEscape(from)
	}
	if errMsg != "" {
		out += "&group_err=" + url.QueryEscape(errMsg)
	}
	return out
}
