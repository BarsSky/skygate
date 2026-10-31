// relay_weights_b456.go — B456 (2026-10-31). The editor B453 promised: «веса нагрузки на
// relay» existed as a setting nothing in the panel could write.
//
// B453 taught the assignment engine to prefer the least load RELATIVE to a relay's weight, read
// from global_settings key prefix_owner.weights ("relay=weight,relay=weight"). The only way to set
// it was SQL. This block adds the surface, and it is deliberately a FORM OF NUMBERS rather than one
// text field: the stored setting stays a string, but the operator never has to remember the syntax,
// and a typo becomes a named refusal instead of a silently ignored entry.
//
// THREE PROPERTIES ARE THE POINT:
//
//  1. A REFUSAL SAVES NOTHING. One bad cell (0, -1, "abc", 100) rejects the WHOLE submit and names
//     the relay and the value. Saving the good half and dropping the bad one would leave the
//     operator believing a weight is in force when it is not — the same class as a check that
//     reports success while verifying nothing.
//  2. THE FORM SAYS WHAT IT DOES NOT DO. Weighting a relay does NOT move prefixes that already have
//     a healthy owner (B453's sticky rule): it only decides where the NEXT prefixes land. The page
//     prints the current share and the share of the next few, so "I raised emilia and nothing
//     happened" is answered before it is asked.
//  3. THE ENGINE IS THE ONE THAT READS IT BACK. After the write the handler re-reads the setting
//     through prefixowner.LoadWeights — the parser the reconcile pass uses — and refuses to claim
//     success when the two disagree.
package admin

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/prefixowner"
)

// WeightsMax is the largest weight the form accepts. A weight of 100 is not a decision, it is a
// typo (the field is "twice as much as the other relay", not "a hundred times").
const WeightsMax = 99

// WeightsPreviewNew is how many hypothetical incoming prefixes the page uses for the "where the
// next ones land" column. Small on purpose: it must read as an illustration, not a promise.
const WeightsPreviewNew = 10

// RelayWeightRow is one relay in the weights editor.
type RelayWeightRow struct {
	Relay string
	// Weight is the effective weight (prefixowner.DefaultWeight when the operator never set
	// one), so the input always shows the number the engine will use.
	Weight int
	// Stored is true only when the operator actually wrote this weight down; the page badges the
	// rest as "by default" instead of pretending they were chosen.
	Stored bool
	// Load is how many prefixes this relay owns RIGHT NOW — the half of the story the weight
	// cannot change.
	Load int
	// Share is how many of WeightsPreviewNew new prefixes this relay would take. It is computed
	// by the engine's own distribution function, so the number on screen cannot drift from the
	// number the engine would produce.
	Share int
}

// RelayWeightsView is everything the weights card renders.
type RelayWeightsView struct {
	Rows []RelayWeightRow
	// Target is WeightsPreviewNew, passed explicitly so the heading and the arithmetic cannot
	// disagree.
	Target int
	// Configured is true when at least one weight is stored (i.e. the tailnet is NOT on an even
	// split).
	Configured bool
	// Raw is the stored setting as written, shown verbatim so the operator can see the exact
	// string the engine parses.
	Raw string
	// Rejected is what prefixowner.ParseWeights could not understand — a value written by SQL or
	// by an older build. It is displayed, because a rejected entry means a relay is running at
	// weight 1 while somebody believes otherwise.
	Rejected []string
}

// BuildRelayWeights composes the view from plain data so it can be tested without HTTP, a database
// or a running engine.
func BuildRelayWeights(relays []string, load map[string]int, w prefixowner.Weights, raw string, target int) RelayWeightsView {
	if target <= 0 {
		target = WeightsPreviewNew
	}
	view := RelayWeightsView{Target: target, Raw: raw, Configured: len(w) > 0}
	seen := map[string]bool{}
	ordered := make([]string, 0, len(relays))
	for _, r := range relays {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		ordered = append(ordered, r)
	}
	sort.Strings(ordered)
	share := prefixowner.WeightDistribution(ordered, load, w, target)
	for _, r := range ordered {
		_, stored := w[r]
		view.Rows = append(view.Rows, RelayWeightRow{
			Relay:  r,
			Weight: w.Of(r),
			Stored: stored,
			Load:   load[r],
			Share:  share[r],
		})
	}
	return view
}

// serializedWeights renders the setting "relay=weight,..." deterministically, omitting relays that
// sit at the default. Omitting them keeps a fresh install byte-identical to no setting at all, and
// keeps the string readable: only the decisions the operator made are in it.
func serializedWeights(w prefixowner.Weights) string {
	names := make([]string, 0, len(w))
	for name, weight := range w {
		if weight <= 0 || weight == prefixowner.DefaultWeight {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+strconv.Itoa(w[name]))
	}
	return strings.Join(parts, ",")
}

// parseWeightInputs reads the submitted cells for the KNOWN relays (never for names the form
// invented) and returns the weights to store plus one problem sentence per rejected cell.
//
// The parser is deliberately strict and the reason is named: 0 and negatives look like an intent
// the engine does not implement ("take nothing"), and text looks like a typo. B453 already refuses
// them at parse time; refusing them HERE, before the write, is what makes the refusal visible
// while the operator is still looking at the form.
func parseWeightInputs(relays []string, get func(string) string) (prefixowner.Weights, []string) {
	out := prefixowner.Weights{}
	var problems []string
	for _, relay := range relays {
		raw := strings.TrimSpace(get("w_" + relay))
		if raw == "" {
			continue // untouched cell = default weight
		}
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s=%q — вес должен быть целым числом", relay, raw))
			continue
		case n < prefixowner.DefaultWeight:
			problems = append(problems, fmt.Sprintf("%s=%d — вес не может быть меньше %d (движок не умеет «взять ноль префиксов»)", relay, n, prefixowner.DefaultWeight))
			continue
		case n > WeightsMax:
			problems = append(problems, fmt.Sprintf("%s=%d — вес больше %d это опечатка, а не доля", relay, n, WeightsMax))
			continue
		}
		if n != prefixowner.DefaultWeight {
			out[relay] = n
		}
	}
	return out, problems
}

// relayWeightsKnownRelays is the set of relays the editor may talk about: every exit_servers row,
// every relay the assignment table currently names, and every relay that already has a stored
// weight. The union matters — a relay removed from exit_servers but still owning prefixes (or still
// weighted) must remain visible, because silently dropping its weight on the next save would be a
// change the operator never made.
func (s *Service) relayWeightsKnownRelays(rows []PrefixOwnerRow, w prefixowner.Weights) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	if servers, err := db.ListExitServers(s.dbc()); err == nil {
		for _, e := range servers {
			add(e.Hostname)
		}
	} else {
		log.Printf("[exit-nodes] weights editor: cannot list exit_servers (%v) — falling back to the assignment table", err)
	}
	for _, r := range rows {
		add(r.ExitNode)
	}
	for name := range w {
		add(name)
	}
	sort.Strings(out)
	return out
}

// loadRelayWeightsView builds the weights card from the database. The load column comes from the
// assignment table itself (COUNT(*) GROUP BY owner), not from the rows the page happened to render: a
// drifted-filtered or row-capped page must not under-report how much a relay carries.
func (s *Service) loadRelayWeightsView(rows []PrefixOwnerRow) RelayWeightsView {
	w, rejected := prefixowner.LoadWeights(s.dbc())
	raw, err := db.GetGlobalSetting(s.dbc(), prefixowner.SettingWeights, "")
	if err != nil {
		log.Printf("[exit-nodes] weights editor: cannot read %s: %v", prefixowner.SettingWeights, err)
	}
	load := map[string]int{}
	if countRows, qerr := s.dbc().Query(`SELECT exit_node_id, COUNT(*) FROM prefix_owner GROUP BY exit_node_id`); qerr == nil {
		defer countRows.Close()
		for countRows.Next() {
			var relay string
			var n int
			if countRows.Scan(&relay, &n) == nil {
				load[strings.TrimSpace(relay)] = n
			}
		}
	} else {
		// Fall back to the rendered rows and SAY so — an under-reported load is worse than none.
		log.Printf("[exit-nodes] weights editor: prefix_owner count failed (%v) — using the rendered rows", qerr)
		for _, r := range rows {
			load[strings.TrimSpace(r.ExitNode)]++
		}
	}
	view := BuildRelayWeights(s.relayWeightsKnownRelays(rows, w), load, w, raw, WeightsPreviewNew)
	view.Rejected = rejected
	return view
}

// PostAdminExitNodeRelayWeights saves the weights (B456). Admin-only.
//
// `reset=1` clears the setting (an even split again) — one explicit control instead of asking the
// operator to type 1 into every cell.
func (s *Service) PostAdminExitNodeRelayWeights(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows, _ := s.loadPrefixOwnerRows()
	current, _ := prefixowner.LoadWeights(s.dbc())
	relays := s.relayWeightsKnownRelays(rows, current)

	var next prefixowner.Weights
	if strings.TrimSpace(r.FormValue("reset")) == "1" {
		next = prefixowner.Weights{}
	} else {
		var problems []string
		next, problems = parseWeightInputs(relays, r.FormValue)
		if len(problems) > 0 {
			// Nothing is written: a half-applied form would leave a weight the operator thinks
			// is in force but is not.
			msg := "веса не сохранены — " + strings.Join(problems, "; ")
			http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(msg), http.StatusSeeOther)
			return
		}
	}

	serialized := serializedWeights(next)
	if err := db.SetGlobalSetting(s.dbc(), prefixowner.SettingWeights, serialized); err != nil {
		log.Printf("[exit-nodes] cannot save %s: %v", prefixowner.SettingWeights, err)
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	// Read it back through the ENGINE's parser: the write is only real if the reconcile pass will
	// see the same weights.
	back, rejected := prefixowner.LoadWeights(s.dbc())
	if len(rejected) > 0 || !sameWeights(back, next) {
		log.Printf("[exit-nodes] %s read-back mismatch: stored %q, engine sees %v (rejected %v)", prefixowner.SettingWeights, serialized, back, rejected)
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(
			"веса записаны, но движок прочитал их иначе ("+serialized+") — проверьте настройку "+prefixowner.SettingWeights),
			http.StatusSeeOther)
		return
	}

	action := "prefix_owner_weights"
	result := "веса заданы: " + serialized
	if serialized == "" {
		action = "prefix_owner_weights_reset"
		result = "веса сброшены — движок снова делит нагрузку ровно"
	}
	s.Backend.Audit(c.UserID, c.Username, action, "weights="+serialized)
	// The sentence the operator needs AFTER saving, not before: a weight change is not a
	// redistribution. Naming it here is what stops "I raised emilia, why did nothing move".
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(result+
		". Существующие префиксы не переезжают: вес решает, куда движок положит следующие."), http.StatusSeeOther)
}

// sameWeights compares two weight maps entry by entry (a map comparison needs that in Go, and the
// intent is clearer than reflect.DeepEqual for a map of ints).
func sameWeights(a, b prefixowner.Weights) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
