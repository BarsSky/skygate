// toggle_b517.go — B517: switching a rule (or a whole group) ON and OFF.
//
// THE OPERATOR'S REPORT (2026-10-30, verbatim): «не хватает возможности выключения
// и включения правила, сейчас можно только удалить или добавить».
//
// That sentence describes a panel that offers exactly two verbs, one of which is
// destructive. `device_rules.enabled` has existed since v0.20 and every consumer
// already honours it (the ACL entry query, the advertised-route sync, the DNS
// auto-updater, the preferred-exit reconciler, the prefix claims, every quota
// counter), so the missing half was never storage — it was a WRITER and the
// decision about what "switch off" means at the edges. This file is those two
// things and nothing else.
//
// WHAT "OFF" MEANS, decided here rather than left to the reader:
//
//   - the rule leaves the generated ACL (grants AND the per-CIDR `via` pins) and
//     the advertised-route set, so the destination stops being routed by it;
//   - it stops counting toward the per-device and per-user caps (the counters
//     filter `enabled = 1`), which the usage badge on the page reflects;
//   - a disabled DOMAIN rule stops being re-resolved, and its resolved rows are
//     switched off WITH it — otherwise the domain row would say "off" while its
//     /32s kept working, which is exactly the class of lie this project keeps
//     paying for (B343/B347/B439);
//   - an «all my devices» rule is switched off in ALL its fan-out copies, because
//     the propagation pass would otherwise re-materialise an enabled copy on the
//     next device the user registers;
//   - the ACL is re-applied only if the live policy actually drifts
//     (`applyACLIfDrifted`, the same tail every other rule mutation uses), and the
//     advertised routes are re-synced after that, because the relay must stop
//     announcing prefixes no rule covers any more.
//
// WHAT IT DELIBERATELY DOES NOT DO: notify Telegram on success. Disabling a rule
// is reversible, often done in bursts while the operator is looking at the page,
// and the audit trail (`exit_rule_logs` + `audit_log`) already records it. An ACL
// apply FAILURE still alerts, because that one leaves the live policy stale.
package exit_rules

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// rulesToggleBackPath is the only page a toggle may return to. Everything else
// is replaced by the page root: a redirect target is an input, and an input that
// can name another origin is how an open redirect ships.
const rulesToggleBackPath = "/my/exit-rules"

// safeRulesBack validates the `back` field the forms carry.
//
// The rule is exact rather than a prefix: the parsed PATH must BE the rules page
// and the URL must name no scheme and no host. A prefix check looked sufficient
// and is not — `/my/exit-rules/../../admin` starts with the right text and the
// browser normalises it to `/admin`, so a "return address" would have become a
// redirect to another page of the panel. Backslashes and control characters are
// refused before parsing, because some clients normalise them into separators and
// a newline in a Location header is header injection.
func safeRulesBack(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\\\n\r\t\x00") {
		return rulesToggleBackPath
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path != rulesToggleBackPath {
		return rulesToggleBackPath
	}
	return raw
}

// rulesBackWithFlash appends (or replaces) the toggle outcome on the return URL.
// An empty state keeps the URL as it is, so an error path can pass only `errMsg`.
func rulesBackWithFlash(back, state string, n int64, errMsg string) string {
	u, err := url.Parse(safeRulesBack(back))
	if err != nil {
		u = &url.URL{Path: rulesToggleBackPath}
	}
	q := u.Query()
	q.Del("toggled")
	q.Del("n")
	q.Del("toggle_err")
	if state != "" {
		q.Set("toggled", state)
		q.Set("n", strconv.FormatInt(n, 10))
	}
	if errMsg != "" {
		q.Set("toggle_err", errMsg)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ruleToggleDesired parses the requested state. `ok=false` means "the form did
// not say", and the caller then derives the state from the rows themselves.
func ruleToggleDesired(raw string) (enabled, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "on", "true", "enable", "enabled":
		return true, true
	case "0", "off", "false", "disable", "disabled":
		return false, true
	}
	return false, false
}

// toggleTargetIDs answers «which rows must change when THIS row is switched?».
//
// The three answers are the three shapes a rule can have, and getting any of them
// wrong produces a half-switched rule:
//
//	all_devices → every copy of the same logical rule (the propagation pass only
//	              ever widens a group, so a single disabled copy would be
//	              resurrected on the next device);
//	domain      → the row itself PLUS everything resolved from it, in BOTH
//	              provenance spellings («example.com» for a /32, and
//	              «cdn:<provider>:example.com» for a range set);
//	anything    → just that row.
//
// Returns the ids and a short kind ("fanout" / "domain" / "rule") that the audit
// detail names, so the trail says what the operator actually switched.
func toggleTargetIDs(rows []RuleRow, id int64) ([]int64, string) {
	var target RuleRow
	found := false
	for _, r := range rows {
		if r.ID == id {
			target = r
			found = true
			break
		}
	}
	if !found {
		return nil, ""
	}

	if target.AllDevices {
		ids := make([]int64, 0, 4)
		for _, r := range rows {
			if !r.AllDevices {
				continue
			}
			if r.ExitNode != target.ExitNode || r.TargetType != target.TargetType ||
				r.TargetValue != target.TargetValue || r.ParentDomain != target.ParentDomain {
				continue
			}
			ids = append(ids, r.ID)
		}
		return ids, "fanout"
	}

	if strings.EqualFold(strings.TrimSpace(target.TargetType), "domain") {
		domain := strings.ToLower(strings.TrimSpace(target.TargetValue))
		ids := []int64{target.ID}
		for _, r := range rows {
			if r.ID == target.ID {
				continue
			}
			if ruleParentDomainMatches(r.ParentDomain, domain) {
				ids = append(ids, r.ID)
			}
		}
		return ids, "domain"
	}

	return []int64{target.ID}, "rule"
}

// ruleParentDomainMatches reports whether a parent_domain value names `domain`
// (already lower-cased), in either provenance spelling.
func ruleParentDomainMatches(parentDomain, domain string) bool {
	parent := strings.TrimSpace(parentDomain)
	if parent == "" || domain == "" {
		return false
	}
	if _, plain, ok := ParseCDNGroupMarker(parent); ok {
		return strings.ToLower(plain) == domain
	}
	return strings.ToLower(parent) == domain
}

// intersectRuleIDs keeps only the ids that belong to the caller's loaded rows.
//
// The SQL is already owner-scoped; this is the second lock on the same door, and
// it is the one that makes a stale id (a row deleted in another tab) a no-op
// instead of an UPDATE against a number the operator cannot see.
func intersectRuleIDs(rows []RuleRow, ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	known := make(map[int64]bool, len(rows))
	for _, r := range rows {
		known[r.ID] = true
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if known[id] {
			out = append(out, id)
		}
	}
	return out
}

// majorityDesired is the state a caller that did not name one should land on:
// switch the selection ON unless every selected row is already on. Used by
// hand-rolled requests; the page's own buttons always name the state, because a
// button that says «Выключить» must switch off even if the state moved under it.
func majorityDesired(rows []RuleRow, ids []int64) bool {
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	anyOff := false
	any := false
	for _, r := range rows {
		if !want[r.ID] {
			continue
		}
		any = true
		if !r.Enabled {
			anyOff = true
			break
		}
	}
	if !any {
		return true
	}
	return anyOff
}

// toggleGroupLabel is the short name the audit trail uses for a group: its title
// when it has one, otherwise its kind (the auto-relay card has no title).
func toggleGroupLabel(g RuleGroup) string {
	if t := strings.TrimSpace(g.Title); t != "" {
		return t
	}
	return g.Kind
}

// toggleCount parses the flash's rule count. A non-numeric value is ZERO rather
// than a template error: the count is presentation, and a page that fails to
// render because a query parameter is odd is a worse outcome than a banner
// saying "0" (the handler wrote that number itself, so this only guards a
// hand-edited URL).
func toggleCount(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// PostMyExitRuleToggle handles POST /my/exit-rules/toggle.
//
// Fields:
//
//	scope   "rule" | "group" | "ids"
//	id      the rule id, for scope=rule
//	lens    the grouping the page was on, for scope=group
//	group   the group key the page rendered, for scope=group
//	ids[]   explicit rule ids, for scope=ids (the multi-select bar, B519)
//	enabled "1"/"0" — the state the operator asked for
//	back    the page to return to (validated by safeRulesBack)
//
// The group's rows are recomputed from the SAME model the page rendered
// (BuildRuleGroups + FindRuleGroup), so "switch off this card" can never touch a
// different set of rules than the card showed.
func (s *Service) PostMyExitRuleToggle(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		// A malformed body is not a page-sized problem, and the parser's own text is
		// an internal detail: answer through the same banner every other refusal uses
		// (the R6 rule the error-leak guard enforces: http.Error would replace the
		// page with text/plain AND leak the internals).
		http.Redirect(w, r, rulesBackWithFlash(r.FormValue("back"), "", 0, "the form could not be read"), http.StatusSeeOther)
		return
	}
	back := r.FormValue("back")

	rows, err := db.GetDeviceRulesForUser(s.dbc(), c.UserID)
	if err != nil {
		http.Redirect(w, r, rulesBackWithFlash(back, "", 0, err.Error()), http.StatusSeeOther)
		return
	}
	// B276.1: the fan-out marker lives on the row, but the shared SELECT does not
	// carry it — resolve it the same way the page does, so toggling a row here and
	// toggling the card there act on the same set.
	if keys := s.allDeviceRuleKeys(c.UserID); len(keys) > 0 {
		for i := range rows {
			rows[i].AllDevices = keys[allDeviceRuleKey(rows[i].ExitNodeID, rows[i].TargetType, rows[i].TargetValue)]
		}
	}
	ruleRows := ruleRowsFromDeviceRules(rows)

	var targetIDs []int64
	var kind string
	switch strings.ToLower(strings.TrimSpace(r.FormValue("scope"))) {
	case "group":
		lens, ok := ParseRuleLens(r.FormValue("lens"))
		if !ok {
			lens = RuleLensDefault(ruleRows)
		}
		groups := BuildRuleGroups(ruleRows, lens)
		g, found := FindRuleGroup(groups, r.FormValue("group"))
		if !found {
			http.Redirect(w, r, rulesBackWithFlash(back, "", 0, "the group no longer exists"), http.StatusSeeOther)
			return
		}
		for _, rr := range g.Rules {
			targetIDs = append(targetIDs, rr.ID)
		}
		kind = "group " + toggleGroupLabel(g)
	case "rule":
		id, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("id")), 10, 64)
		targetIDs, kind = toggleTargetIDs(ruleRows, id)
	case "ids":
		for _, raw := range r.Form["ids"] {
			if id, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); perr == nil {
				targetIDs = append(targetIDs, id)
			}
		}
		kind = "selection"
	default:
		http.Redirect(w, r, rulesBackWithFlash(back, "", 0, "unknown scope"), http.StatusSeeOther)
		return
	}

	targetIDs = intersectRuleIDs(ruleRows, targetIDs)
	if len(targetIDs) == 0 {
		http.Redirect(w, r, rulesBackWithFlash(back, "", 0, "no rule of yours matches that request"), http.StatusSeeOther)
		return
	}

	desired, ok := ruleToggleDesired(r.FormValue("enabled"))
	if !ok {
		desired = majorityDesired(ruleRows, targetIDs)
	}

	intIDs := make([]int, 0, len(targetIDs))
	for _, id := range targetIDs {
		intIDs = append(intIDs, int(id))
	}
	changed, err := db.SetDeviceRulesEnabled(s.dbc(), c.UserID, intIDs, desired)
	if err != nil {
		http.Redirect(w, r, rulesBackWithFlash(back, "", 0, err.Error()), http.StatusSeeOther)
		return
	}

	verb := "disabled"
	state := "off"
	if desired {
		verb = "enabled"
		state = "on"
	}
	detail := fmt.Sprintf("user %s %s %d rule(s) (%s)", c.Username, verb, changed, kind)
	s.Backend.Audit(c.UserID, c.Username, "exit_rule_"+verb, detail)

	// The ACL tail every other rule mutation uses: regenerate, compare with the
	// LIVE policy, write only on drift. A disabled rule can make the policy
	// shrink, so the comparison is what decides — not the fact that a row changed.
	res := s.applyACLIfDrifted("user-rule-toggle", detail)
	logAction := db.ExitRuleActionDisable
	if desired {
		logAction = db.ExitRuleActionEnable
	}
	switch {
	case res.Err != nil:
		db.MarkACLFail(s.dbc(), res.Version, res.Err.Error())
		db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionApplyFail,
			fmt.Sprintf("user %s: %s %d rule(s): %v", c.Username, verb, changed, res.Err))
		if s.Notifier != nil {
			go s.Notifier.SendAlert(i18n.Tf(i18n.LangRU, "alert.acl.apply_failed_toggle", c.Username, changed, res.Err))
		}
	case res.Applied:
		db.MarkACLApplied(s.dbc(), res.Version)
		db.AppendExitRuleLog(s.dbc(), res.Version, logAction, detail)
		// The data plane follows the control plane: a prefix whose last enabled
		// rule just went away must stop being advertised, and a re-enabled one must
		// be advertised again. Same call the create/delete paths make.
		if s.SyncRoutes != nil {
			if sync := s.SyncRoutes(); sync != nil {
				for node, status := range sync {
					db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionSync,
						fmt.Sprintf("sync %s: %s", node, status))
				}
			}
		}
	default:
		// No drift: the live policy already matches what the operator asked for
		// (e.g. switching the last rule of a destination that another user's rule
		// still covers). Said in the journal, never as an error to the operator.
		db.AppendExitRuleLog(s.dbc(), db.ExitRuleLogNoVersion, logAction,
			fmt.Sprintf("%s (no ACL drift)", detail))
		log.Printf("acl-drift: toggle by %s (%d rules, %s) did not require a re-apply", c.Username, changed, verb)
	}

	http.Redirect(w, r, rulesBackWithFlash(back, state, changed, ""), http.StatusSeeOther)
}
