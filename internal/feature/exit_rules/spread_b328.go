// spread_b328.go — B328: turn an ALREADY SAVED single-device rule into an
// "all my devices" rule, in one action.
//
// WHY. `/my/exit-rules` has offered «все мои устройства» since B275.3 (fan-out at save
// time) and B276.1 made the intent survive a device registered later
// (`device_rules.all_devices` + the periodic pass). Both only help a rule that is saved
// THAT way. An operator who already had 33 rules on one device and then registered a
// second one had no path at all: every rule had to be re-typed with the option
// selected. That is exactly the operator report —
//
//	«правила что были заданы не распространяются на другие устройства — вроде ты
//	 говорил что так не сделать или здесь работает другой подход?»
//
// The old answer was "a rule is per device; re-add it with «все мои устройства»". The
// reason it never propagated AUTOMATICALLY is deliberate and stays: fan-out copies are
// indistinguishable from hand-made rules, so guessing which single-device rules were
// "meant" for every device would silently start copying rules nobody asked to copy.
// What was missing is the explicit action — the operator names the rule, and skygate
// records the intent and materialises it.
//
// HOW. The handler does not re-implement the fan-out. It marks the intent
// (db.MarkDeviceRulesAllDevices) and then runs the SAME pass the five-minute
// maintenance tick runs (`propagateAllDeviceRules`), so the button cannot produce rows
// the tick would not have produced: the copy keeps the source rule's action and
// parent_domain, the natural key makes it idempotent, and the marker is re-applied to
// every row it creates.
//
// WHAT IT DOES NOT DO. It never invents a rule — the natural key must already exist
// for the caller — and it never deletes or rewrites an existing row.
package exit_rules

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"skygate/internal/db"
)

// PostMyExitRuleSpread handles POST /my/exit-rules/spread.
//
// Form fields — the rule's natural key, minus the device (the same key the ALL-DEVICES
// section and the fan-out group on):
//
//	exit_node, target_type, target_value
func (s *Service) PostMyExitRuleSpread(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	exitNode := strings.TrimSpace(r.FormValue("exit_node"))
	targetType := strings.TrimSpace(r.FormValue("target_type"))
	targetValue := strings.TrimSpace(r.FormValue("target_value"))
	if targetType == "" || targetValue == "" {
		redirectSpreadErr(w, r, "missing target_type/target_value — nothing was changed")
		return
	}

	// The rule must already belong to the caller. This is what keeps the action from
	// inventing rules, and it uses the same natural key as everything else.
	existing, err := db.GetDeviceRulesForUser(s.dbc(), c.UserID)
	if err != nil {
		redirectSpreadErr(w, r, "could not read your rules: "+err.Error()+" — nothing was changed")
		return
	}
	found, alreadyAll := false, false
	for _, row := range existing {
		if row.ExitNodeID != exitNode || row.TargetType != targetType || row.TargetValue != targetValue {
			continue
		}
		found = true
		if row.AllDevices {
			alreadyAll = true
		}
	}
	if !found {
		redirectSpreadErr(w, r, fmt.Sprintf("no rule %s %s via %s belongs to you — nothing was changed", targetType, targetValue, exitNode))
		return
	}
	if alreadyAll {
		http.Redirect(w, r, "/my/exit-rules?spread=already", http.StatusFound)
		return
	}

	// Record the intent BEFORE copying: if the pass below fails halfway, the periodic
	// tick finishes the job. The other order would leave copies that no marker
	// explains — exactly the "ghost rule" class B277.4 had to sweep up.
	if _, merr := db.MarkDeviceRulesAllDevices(s.dbc(), c.UserID, exitNode, targetType, targetValue); merr != nil {
		redirectSpreadErr(w, r, "could not record the all-devices intent: "+merr.Error()+" — nothing was changed")
		return
	}

	created, perr := s.propagateAllDeviceRules()
	if perr != nil {
		// The intent is recorded, so this is recoverable: say so instead of implying
		// the action did nothing.
		log.Printf("exit-rules: spread %s %s via %s marked, but the immediate fan-out failed: %v (the maintenance pass will retry)",
			targetType, targetValue, exitNode, perr)
		http.Redirect(w, r, "/my/exit-rules?spread=marked&spread_err="+url.QueryEscape(perr.Error()), http.StatusFound)
		return
	}

	devices := s.userDeviceIDs(c.Username)
	log.Printf("exit-rules: spread %s %s via %s to all %d device(s) of %s (created=%d row(s))",
		targetType, targetValue, exitNode, len(devices), c.Username, created)
	if s.Backend != nil {
		s.Backend.Audit(c.UserID, c.Username, "exit_rules_spread_all_devices",
			fmt.Sprintf("%s %s via %s → %d device(s), created=%d", targetType, targetValue, exitNode, len(devices), created))
	}

	http.Redirect(w, r, fmt.Sprintf("/my/exit-rules?spread=ok&spread_devices=%d&spread_created=%d", len(devices), created), http.StatusFound)
}

// redirectSpreadErr sends the operator back to the page with a named reason. Every
// refusal in this handler goes through here, so a failed spread can never look like a
// successful one (the B237.19 flash pattern the rest of the feature uses).
func redirectSpreadErr(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/my/exit-rules?err="+url.QueryEscape("spread: "+reason), http.StatusFound)
}
