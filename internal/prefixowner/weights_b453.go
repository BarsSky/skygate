// weights_b453.go — B453 (2026-10-30). «веса нагрузки на relay»: the last open item of B275.1.
//
// The assignment engine spread unclaimed prefixes over the healthy relays by LEAST LOAD, which is
// an even split. That is right for identical relays and wrong for a real tailnet: a small VPS and a
// big box should not each carry half of 200 prefixes. The engine now accepts a WEIGHT per relay and
// prefers the relay with the least load RELATIVE to its weight.
//
// TWO PROPERTIES ARE DELIBERATE, because both protect live traffic:
//
//  1. STICKY STILL WINS. A prefix that already has a healthy owner stays where it is; weights are
//     consulted only where the engine was going to choose anyway (a new prefix, or a prefix whose
//     owner is gone/overloaded). Changing a weight therefore never migrates traffic — it only
//     changes where the NEXT prefixes land. An operator raising a relay's weight to "take some
//     load" is asking for that, and a failover is not a redistribution.
//  2. AN EMPTY/INVALID WEIGHT IS ONE. A relay missing from the map, a zero, a negative or a
//     garbage value behaves exactly like today, so this block cannot change any existing install's
//     behaviour until somebody sets a weight — and it can never divide by zero or starve a relay
//     that the healthy set says must be usable.
package prefixowner

import (
	"database/sql"
	"sort"
	"strconv"
	"strings"
)

// DefaultWeight is what a relay gets when the operator did not name it.
const DefaultWeight = 1

// Weights maps a relay hostname (as `prefix_owner.exit_node_id` spells it) to a positive weight.
// The empty value and every invalid entry mean DefaultWeight.
type Weights map[string]int

// Of is the weight of one relay, clamped to at least 1.
func (w Weights) Of(relay string) int {
	if n, ok := w[relay]; ok && n > 0 {
		return n
	}
	return DefaultWeight
}

// ParseWeights reads the operator's setting: "relay=weight,relay=weight".
//
// It never fails. Anything it does not understand is reported back in `rejected` so the caller can
// log it, because a typo in a weight must not silently behave like an even split — that is the same
// class as a check that reports success while verifying nothing.
func ParseWeights(raw string) (w Weights, rejected []string) {
	w = Weights{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		relay, valStr, ok := strings.Cut(item, "=")
		relay = strings.TrimSpace(relay)
		valStr = strings.TrimSpace(valStr)
		if !ok || relay == "" {
			rejected = append(rejected, item)
			continue
		}
		n, err := strconv.Atoi(valStr)
		if err != nil || n < 0 {
			// Zero and negative are rejected rather than clamped: they look like an intent
			// ("take no prefixes") that the engine does not implement, and silently treating
			// them as 1 would hide that.
			rejected = append(rejected, item)
			continue
		}
		if n == 0 {
			rejected = append(rejected, item)
			continue
		}
		w[relay] = n
	}
	sort.Strings(rejected)
	return w, rejected
}

// pickLeastRelative returns the candidate with the smallest load/weight ratio, ties broken by name
// so the result is deterministic across passes.
//
// The comparison is cross-multiplied (load[c] * w(best) vs load[best] * w(c)) so it stays in
// integers: a float ratio would let two relays with identical ratios differ in the last bit and
// make the table oscillate between passes.
func pickLeastRelative(candidates []string, load map[string]int, w Weights) string {
	if len(candidates) == 0 {
		return ""
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		lc, lb := load[c], load[best]
		wc, wb := w.Of(c), w.Of(best)
		switch {
		case lc*wb < lb*wc:
			best = c
		case lc*wb == lb*wc && c < best:
			best = c
		}
	}
	return best
}

// WeightDistribution answers, for the CURRENT load and weights, how many of `incoming` new prefixes
// each relay should take. It exists so the page (and a test) can show the intended split without
// running the engine, and so the arithmetic is one pure function instead of something to infer.
func WeightDistribution(relays []string, load map[string]int, w Weights, incoming int) map[string]int {
	out := map[string]int{}
	if incoming <= 0 || len(relays) == 0 {
		return out
	}
	cur := map[string]int{}
	for _, r := range relays {
		cur[r] = load[r]
	}
	ordered := append([]string{}, relays...)
	sort.Strings(ordered)
	for i := 0; i < incoming; i++ {
		best := pickLeastRelative(ordered, cur, w)
		if best == "" {
			break
		}
		cur[best]++
		out[best]++
	}
	return out
}

// SettingWeights is the global_settings key an operator writes to weight the relays, in the form
// "relay=weight,relay=weight". It lives in the database rather than in .env so a panel change does
// not need a container recreate (the same reasoning as B272's update.git_url).
const SettingWeights = "prefix_owner.weights"

// LoadWeights reads and parses SettingWeights. A missing key (the default on every install) means
// "no weights" — an even split — and is not an error.
func LoadWeights(d *sql.DB) (Weights, []string) {
	if d == nil {
		return Weights{}, nil
	}
	var raw string
	if err := d.QueryRow(`SELECT value FROM global_settings WHERE key = $1`, SettingWeights).Scan(&raw); err != nil {
		return Weights{}, nil
	}
	return ParseWeights(raw)
}
