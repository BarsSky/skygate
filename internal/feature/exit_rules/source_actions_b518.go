// source_actions_b518.go — B518: what the operator can do to a SOURCE card as a
// whole.
//
// THE OPERATOR'S REPORT (2026-10-30, verbatim): «отдельно по подсетям что
// добавляются вместе с доменами — когда происходит такое добавление то потом не
// понятно к какому домену относилась та или иная подсеть, из за чего если
// потребуется координально менять правила то придется все сносить и задавать
// заново, что очень не удобно».
//
// B516 answered the first half (the card shows the domain and every row resolved
// from it, in both provenance spellings). This file is the second half: the verbs
// that make «change the routing of this domain» a single action instead of
// «delete everything and re-add it».
//
//	assign-group   «собрать группу из источника» — file the card's existing rows
//	               under the operator's own name (B380's label), which is the
//	               verb B380 was missing: the label could be written at CREATION,
//	               and a re-added rule is a duplicate that changes nothing.
//	source-resolve «переразрешить сейчас» — clear the per-domain resolve gate and
//	               run the SAME resolver pass the scheduler runs. Re-implementing
//	               one domain's slice of that pass would create a second, divergent
//	               implementation of CDN markers / IPv6 answers / service ranges.
//
// The other two verbs a card needs already exist and are reused rather than
// duplicated: DELETE posts the card's whole id set to /my/exit-rules/delete
// (which cascades), and «ask for another relay» posts the same ids to the
// existing /my/exit-rules/relay-request (B471's multi-rule ask).
//
// EVERY WRITE HERE IS OWNER-SCOPED and the id set comes from the MODEL, never from
// the form alone: the page's card and the action must mean the same rows (B517).
package exit_rules

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
)

// ownerRuleRows loads every rule of one user with the B276.1 fan-out marker
// resolved, projected to the row shape the group model reads.
//
// Shared by every card-level action (switch, file into a group, re-resolve) so
// they all see the same set — the failure mode this avoids is one action honouring
// the fan-out marker and another treating the copies as hand-made rules.
func (s *Service) ownerRuleRows(userID int64) ([]RuleRow, error) {
	rows, err := db.GetDeviceRulesForUser(s.dbc(), userID)
	if err != nil {
		return nil, err
	}
	if keys := s.allDeviceRuleKeys(userID); len(keys) > 0 {
		for i := range rows {
			rows[i].AllDevices = keys[allDeviceRuleKey(rows[i].ExitNodeID, rows[i].TargetType, rows[i].TargetValue)]
		}
	}
	return ruleRowsFromDeviceRules(rows), nil
}

// rulesActionFlash appends the outcome of a card action to the return URL, the
// same way the toggle does. Kept separate so the two actions cannot drift in how
// they report.
func rulesActionFlash(back string, kv map[string]string) string {
	u, err := url.Parse(safeRulesBack(back))
	if err != nil {
		u = &url.URL{Path: rulesToggleBackPath}
	}
	q := u.Query()
	for _, k := range []string{"group_assigned", "assigned_to", "source_resolved", "source_added", "source_removed", "source_err"} {
		q.Del(k)
	}
	for k, v := range kv {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// cardActionIDs resolves the rows a card-level action must carry: the card's
// whole set when `scope=group`, otherwise the posted ids intersected with the
// caller's rows.
func (s *Service) cardActionIDs(r *http.Request, rows []RuleRow, allowedExtra ...string) ([]int64, string, error) {
	if strings.EqualFold(strings.TrimSpace(r.FormValue("scope")), "group") {
		lens, ok := ParseRuleLens(r.FormValue("lens"))
		if !ok {
			lens = RuleLensDefault(rows)
		}
		ids, found := RuleIDsOfGroup(rows, lens, r.FormValue("group"))
		if !found {
			return nil, "", fmt.Errorf("the group no longer exists")
		}
		return ids, "group", nil
	}
	raw := append([]string{}, r.Form["ids"]...)
	for _, name := range allowedExtra {
		raw = append(raw, r.Form[name]...)
	}
	ids := make([]int64, 0, len(raw))
	for _, v := range raw {
		if id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return intersectRuleIDs(rows, ids), "selection", nil
}

// PostMyExitRuleAssignGroup handles POST /my/exit-rules/assign-group.
//
// This is the verb B380 was missing: «these existing rules are one group now».
// The label is presentation the operator owns — it never reaches the ACL, the
// prefix assignment or the exit-node choice — so there is deliberately NO
// re-apply here, and the audit row says so implicitly by naming only the label.
//
// Fields:
//
//	scope    "group" (a whole card) or "ids" (an explicit selection)
//	lens     the grouping the card was rendered under
//	group    the card's key
//	ids[]    the selection, for scope="ids"
//	group_to the label to set ("" removes the rules from their group)
//	back     the page to return to
func (s *Service) PostMyExitRuleAssignGroup(w http.ResponseWriter, r *http.Request) {
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

	rows, err := s.ownerRuleRows(c.UserID)
	if err != nil {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": err.Error()}), http.StatusSeeOther)
		return
	}
	ids, scope, aerr := s.cardActionIDs(r, rows)
	if aerr != nil {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": aerr.Error()}), http.StatusSeeOther)
		return
	}
	if len(ids) == 0 {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": "no rule of yours matches that request"}), http.StatusSeeOther)
		return
	}

	label := NormaliseGroupLabel(r.FormValue("group_to"))
	intIDs := make([]int, 0, len(ids))
	for _, id := range ids {
		intIDs = append(intIDs, int(id))
	}
	n, err := db.SetDeviceRulesGroupLabel(s.dbc(), c.UserID, intIDs, label)
	if err != nil {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": err.Error()}), http.StatusSeeOther)
		return
	}

	verb := "filed"
	if label == "" {
		verb = "cleared the group of"
	}
	detail := fmt.Sprintf("user %s %s %d rule(s) (%s)", c.Username, verb, n, scope)
	if label != "" {
		detail += fmt.Sprintf(", group %q", label)
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_rule_group_assign", detail)

	http.Redirect(w, r, rulesActionFlash(back, map[string]string{
		"group_assigned": strconv.FormatInt(n, 10),
		"assigned_to":    label,
	}), http.StatusSeeOther)
}

// PostMyExitRuleSourceResolve handles POST /my/exit-rules/source-resolve — the
// «переразрешить сейчас» action of a source card.
//
// It clears the domain's resolve gate and runs the real resolver pass, because a
// second implementation of «resolve one domain» beside the tested one would drift
// on exactly the parts that took blocks to get right (CDN markers, IPv6-only
// answers, service ranges). The pass is the same work the five-minute tick does,
// so the button cannot write anything the scheduler would not have written — it
// only decides WHEN.
//
// Refusals are explicit: a card that is not a source, or a domain whose rule is
// switched off (the resolver only follows ENABLED domain rules), gets its reason
// instead of a silent no-op.
func (s *Service) PostMyExitRuleSourceResolve(w http.ResponseWriter, r *http.Request) {
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

	rows, lerr := s.ownerRuleRows(c.UserID)
	if lerr != nil {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": lerr.Error()}), http.StatusSeeOther)
		return
	}
	lens, ok := ParseRuleLens(r.FormValue("lens"))
	if !ok {
		lens = RuleLensDefault(rows)
	}
	g, found := FindRuleGroup(BuildRuleGroups(rows, lens), r.FormValue("group"))
	if !found {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": "the group no longer exists"}), http.StatusSeeOther)
		return
	}
	if g.Kind != GroupKindDomain && g.Kind != GroupKindCDN {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": "only a domain card can be re-resolved"}), http.StatusSeeOther)
		return
	}
	domain := strings.TrimSpace(g.Title)
	if domain == "" {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": "the card names no domain"}), http.StatusSeeOther)
		return
	}

	// The resolver follows ENABLED domain rules only (sync_domain.go selects
	// `enabled = 1 AND target_type = 'domain'`), so a switched-off domain rule
	// means the pass would do nothing — say that instead of reporting "0 added".
	active := false
	for _, rr := range g.Rules {
		if strings.EqualFold(rr.TargetType, "domain") && rr.Enabled && strings.EqualFold(strings.TrimSpace(rr.TargetValue), domain) {
			active = true
			break
		}
	}
	if !active {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{
			"source_err": "the domain rule is switched off — switch it on first, or the resolver will skip it",
		}), http.StatusSeeOther)
		return
	}

	if err := s.clearDomainResolveStamp(domain); err != nil {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": err.Error()}), http.StatusSeeOther)
		return
	}
	added, removed, rerr := s.runResolvePass()
	if rerr != nil {
		http.Redirect(w, r, rulesActionFlash(back, map[string]string{"source_err": rerr.Error()}), http.StatusSeeOther)
		return
	}

	detail := fmt.Sprintf("user %s re-resolved %s on request (added=%d removed=%d)", c.Username, domain, added, removed)
	s.Backend.Audit(c.UserID, c.Username, "exit_rule_domain_resolved", detail)

	http.Redirect(w, r, rulesActionFlash(back, map[string]string{
		"source_resolved": domain,
		"source_added":    strconv.Itoa(added),
		"source_removed":  strconv.Itoa(removed),
	}), http.StatusSeeOther)
}
