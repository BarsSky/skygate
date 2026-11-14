// relay_bulk_b471.go — B471 (2026-11-14): «перенести ГРУППУ правил на другой exit-узел»
// and the three gaps the B440 request channel left open.
//
// THE OPERATOR'S QUESTION, verbatim:
//
//	«если допустим захотим поменять в skygate группу правил на другой exit node то все ли
//	 пройдёт штатно и есть ли возможность делать переход сразу группой выбранных правил и
//	 может ли пользователь не администратор подать заявку на перенос»
//
// Three answers, three pieces of code:
//
//  1. GROUP MOVE IN THE PANEL. Before this file the admin page could add rules in a batch
//     (B376) and delete them one by one, but it could not CHANGE a rule's relay at all —
//     the only in-place rewrite lived in /my/exit-rules («применить подстановку») and in
//     the CLI (`skygate prefix-owner --rules-to`). Moving 60 rows therefore meant delete +
//     re-add, which is not the same operation: the natural key of a rule INCLUDES its
//     relay (B123/B183), so a re-add creates a SECOND row and the prefix ends up with two
//     claims, decided by the hostname tie-break instead of the operator. This file adds
//     `POST /admin/exit-rules/bulk-relay`: checkboxes in the rule tree, one relay, one
//     checkbox for "also take over the manual pins".
//
//  2. THE PINS ARE A SEPARATE DECISION. A prefix whose `prefix_owner.source = 'manual'`
//     is held where the operator put it, deliberately (B275): rewriting the RULES does
//     not move it. So a bulk move that only rewrote rules would look like it did nothing
//     for exactly the prefixes somebody had already pinned. The form therefore says how
//     many manual pins the selection touches and moves them only when asked — and it
//     reports the ones it left alone because they are pinned to some OTHER relay (where
//     moving them would be a different, unrequested decision).
//
//  3. A GROUP REQUEST FROM A USER. The B440 form moved ONE rule per ask. A user whose
//     whole Cloudflare set should follow them had to file one request per rule, and the
//     operator had to approve each — the same decision, N times. `PostMyRelayRequest` now
//     reads every `rule_id` the form sends, unions their resolved prefixes and files ONE
//     request (the stored row keeps a comma-joined device list, and the audit row counts
//     both). A single `rule_id` still works, because it is the same field.
//
// WHAT MAKES A MOVE "ШТАТНО" (and is therefore wired here rather than left to the next
// timer): after the rules and the pins are written, the SAME three steps the approval path
// runs — ReapplyACLIfDrifted (the per-CIDR `via` pins must follow the assignment table,
// B276) and the asynchronous route sync (the new relay has to ADVERTISE the prefix and
// headscale has to APPROVE it, B441). Without them the ACL would name a relay that is not
// serving the prefix yet, which is the silent black hole B441 fixed.
package exit_rules

import (
	"database/sql"
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

// BulkRelayMaxRules caps one bulk move. The limit is a guard against a select-all on a
// huge tree turning into a thousand SSH-triggering writes, not a product assumption: the
// live tailnet's largest device carries ~60 rules.
const BulkRelayMaxRules = 200

// BulkRelayCard is the form's data, resolved on the server so the template stays dumb.
type BulkRelayCard struct {
	// Relays are the pin-able relays (exit nodes with a per-node tag, B284) — the same
	// list the request form offers, so the two surfaces cannot disagree about where a
	// prefix may be moved.
	Relays []string
	// FlashOK / FlashErr carry the redirect's sentence (the B237.19 pattern: the result
	// travels in the query string, so a refresh cannot re-submit the form).
	FlashOK  string
	FlashErr string
}

// bulkRuleRow is the minimum a move needs from `device_rules`. UserID/DeviceID are
// needed because the prefix resolver is scoped to (user, device, relay).
type bulkRuleRow struct {
	ID         int64
	UserID     int64
	Device     string
	DeviceID   int64
	ExitNode   string
	TargetType string
	Target     string
}

// pinRef is one `prefix_owner` row as far as this block cares: WHO holds the prefix and
// whether that was the operator's own decision.
type pinRef struct {
	Relay  string
	Manual bool
}

// BulkRelayPlan is the pure answer to "what would this selection do?".
//
// It exists as a separate, DB-free type so the decision can be tested without a headscale
// client, a database or a browser: the handler only performs it.
type BulkRelayPlan struct {
	// Moves are the rules whose relay actually changes.
	Moves []bulkRuleRow
	// Unchanged are the selected rules already on the target relay — counted, not
	// written, so a re-submit of the same form is a no-op instead of an audit-noise.
	Unchanged []bulkRuleRow
	// PinsToMove are manual pins that hold these prefixes on the relay the RULES named:
	// they follow the selection when the operator asked for it.
	PinsToMove []string
	// PinsToKeep are manual pins that hold these prefixes on SOME OTHER relay. Moving
	// them would be a different decision, so they are reported instead of taken.
	PinsToKeep []pinKeep
	// PrefixesPerRule is the resolved address set each moved rule claims, kept so the
	// caller can report what actually travelled (a domain rule can expand to a CDN set).
	PrefixesPerRule map[int64][]string
}

// pinKeep records a pin the plan refuses to take, WITH the relay that holds it: "3 pinned
// elsewhere" without saying where is not actionable.
type pinKeep struct {
	Prefix string
	Relay  string
}

// planBulkRelayMove is the whole decision, pure: rules + their resolved prefixes + the
// current pin table + the target relay + whether pins may be taken.
func planBulkRelayMove(rules []bulkRuleRow, prefixesPerRule map[int64][]string, pins map[string]pinRef, toRelay string, takePins bool) BulkRelayPlan {
	plan := BulkRelayPlan{PrefixesPerRule: map[int64][]string{}}
	target := NormalizeRelay(toRelay)
	seenMove, seenKeep := map[string]bool{}, map[string]bool{}
	for _, rule := range rules {
		oldRelay := NormalizeRelay(rule.ExitNode)
		prefixes := cleanPrefixes(prefixesPerRule[rule.ID])
		if oldRelay == target {
			plan.Unchanged = append(plan.Unchanged, rule)
			continue
		}
		plan.Moves = append(plan.Moves, rule)
		plan.PrefixesPerRule[rule.ID] = prefixes
		for _, p := range prefixes {
			pin, ok := pins[p]
			if !ok || !pin.Manual {
				continue
			}
			// A pin follows the selection only when it is the SAME decision: the rules
			// named this relay and the operator asked to take pins over. Anything else is
			// somebody's separate choice (another relay, or pins not requested at all).
			if takePins && NormalizeRelay(pin.Relay) == oldRelay {
				if !seenMove[p] {
					seenMove[p] = true
					plan.PinsToMove = append(plan.PinsToMove, p)
				}
				continue
			}
			if seenKeep[p] {
				continue
			}
			seenKeep[p] = true
			plan.PinsToKeep = append(plan.PinsToKeep, pinKeep{Prefix: p, Relay: NormalizeRelay(pin.Relay)})
		}
	}
	sort.Strings(plan.PinsToMove)
	sort.Slice(plan.PinsToKeep, func(i, j int) bool { return plan.PinsToKeep[i].Prefix < plan.PinsToKeep[j].Prefix })
	return plan
}

// bulkMoveResult is what the handler reports and audits.
type bulkMoveResult struct {
	Moved      int
	PinsMoved  int
	Unchanged  int
	Problems   []string
	KeptPins   []pinKeep
	MovedRules []bulkRuleRow
	Target     string
}

// applyBulkRelayPlan performs a plan: rules first, then the pins that follow them. A rule
// that cannot be written (a duplicate natural key on the target relay — routine after a
// CDN expansion) is reported and does NOT abort the rest, the same rule B449 established
// for the user-facing twin of this operation.
func applyBulkRelayPlan(d *sql.DB, plan BulkRelayPlan, toRelay string) bulkMoveResult {
	res := bulkMoveResult{Target: NormalizeRelay(toRelay), KeptPins: plan.PinsToKeep, Unchanged: len(plan.Unchanged)}
	for _, rule := range plan.Moves {
		if err := db.UpdateDeviceRuleExitNode(d, int(rule.ID), res.Target); err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("rule %d (%s → %s): %v", rule.ID, rule.Device, rule.Target, err))
			continue
		}
		res.Moved++
		res.MovedRules = append(res.MovedRules, rule)
	}
	for _, p := range plan.PinsToMove {
		if err := prefixowner.SetManual(d, p, res.Target); err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("pin %s → %s: %v", p, res.Target, err))
			continue
		}
		res.PinsMoved++
	}
	return res
}

// bulkRulesByIDs reads the selected rules. `$n` placeholders through the shared helper:
// the ids arrive as a list, and `?` would be a PostgreSQL syntax error (B471 fixed a
// handful of those in the B440 code).
func (s *Service) bulkRulesByIDs(ids []int64) ([]bulkRuleRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.dbc().Query(`SELECT id, COALESCE(user_id,0), COALESCE(device_hostname,''),
	                                   COALESCE(device_id,0), COALESCE(exit_node_id,''),
	                                   COALESCE(target_type,''), COALESCE(target_value,'')
	                              FROM device_rules
	                             WHERE id IN (`+db.PlaceholdersList(len(ids))+`)
	                             ORDER BY device_hostname, target_value, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bulkRuleRow{}
	for rows.Next() {
		var r bulkRuleRow
		if err := rows.Scan(&r.ID, &r.UserID, &r.Device, &r.DeviceID, &r.ExitNode, &r.TargetType, &r.Target); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// pinsByPrefix snapshots the assignment table as far as the plan needs it.
func (s *Service) pinsByPrefix() map[string]pinRef {
	out := map[string]pinRef{}
	rows, err := s.dbc().Query(`SELECT prefix, exit_node_id, source FROM prefix_owner`)
	if err != nil {
		log.Printf("[bulk-relay] read prefix_owner: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var prefix, relay, source string
		if rows.Scan(&prefix, &relay, &source) != nil {
			continue
		}
		out[prefix] = pinRef{Relay: relay, Manual: strings.EqualFold(strings.TrimSpace(source), "manual")}
	}
	return out
}

// prefixesForRules resolves the addresses each rule claims, through the SAME resolver the
// request channel uses: one definition of "what does this rule move", so the two paths
// cannot disagree about the size of a decision.
func (s *Service) prefixesForRules(rules []bulkRuleRow) map[int64][]string {
	out := make(map[int64][]string, len(rules))
	for _, rule := range rules {
		out[rule.ID] = s.relayRequestPrefixes(rule.UserID, rule.DeviceID, rule.ExitNode, rule.TargetType, rule.Target)
	}
	return out
}

// adminBulkRelayCard fills the form (relays + flash), following the request card's shape.
func (s *Service) adminBulkRelayCard(r *http.Request) BulkRelayCard {
	return BulkRelayCard{
		Relays:   s.relayChoices(),
		FlashOK:  r.URL.Query().Get("bulk_relay_ok"),
		FlashErr: r.URL.Query().Get("bulk_relay_err"),
	}
}

// manualPinsLabelFrom names the manual pins among these prefixes, for the request card: the
// operator deciding a request must see that the decision OVERWRITES a pin they set by hand
// (prefixowner.SetManual is the manual pin, so an approval replaces it) — and, symmetrically,
// which prefixes are pinned somewhere else entirely.
//
// It takes the pin table as an ARGUMENT rather than reading it: the request card decorates a
// whole queue, and one query per row would be a page-proportional cost for a string. (The
// one-argument wrapper this file started with was dead code — staticcheck U1000 said so in the
// first gate run of the block, which is exactly what the B95 contract is for.)
func manualPinsLabelFrom(pins map[string]pinRef, prefixes []string) string {
	if len(prefixes) == 0 || len(pins) == 0 {
		return ""
	}
	parts := []string{}
	for _, p := range cleanPrefixes(prefixes) {
		if pin, ok := pins[p]; ok && pin.Manual {
			parts = append(parts, fmt.Sprintf("%s → %s", p, NormalizeRelay(pin.Relay)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	const show = 3
	if len(parts) > show {
		rest := len(parts) - show
		parts = append(parts[:show], fmt.Sprintf("+%d", rest))
	}
	return strings.Join(parts, ", ")
}

// PostAdminBulkRelayMove handles POST /admin/exit-rules/bulk-relay — the operator's group
// move: selected rules, one target relay, optionally taking the manual pins with them.
func (s *Service) PostAdminBulkRelayMove(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	back := "/admin/exit-rules"
	fail := func(msg string) {
		http.Redirect(w, r, back+"?bulk_relay_err="+url.QueryEscape(msg), http.StatusSeeOther)
	}

	if err := r.ParseForm(); err != nil {
		fail(s.I18n.T(lang, "exit_rules_admin.bulk_relay_bad_form"))
		return
	}
	target := NormalizeRelay(r.FormValue("to_relay"))
	takePins := strings.TrimSpace(r.FormValue("move_pins")) == "1"
	ids := parseRuleIDs(r.Form["rule_ids"])
	if len(ids) == 0 {
		fail(s.I18n.T(lang, "exit_rules_admin.bulk_relay_none"))
		return
	}
	if len(ids) > BulkRelayMaxRules {
		fail(s.I18n.Tf(lang, "exit_rules_admin.bulk_relay_too_many", BulkRelayMaxRules, len(ids)))
		return
	}
	if target == "" {
		fail(s.I18n.T(lang, "exit_rules_admin.bulk_relay_bad_relay"))
		return
	}
	// The target must be a relay we can actually PIN: an exit node carrying its own tag
	// (B284). The same list the request form offers, so the two surfaces cannot disagree.
	allowed := false
	for _, relay := range s.relayChoices() {
		if strings.EqualFold(relay, target) {
			allowed = true
			break
		}
	}
	if !allowed {
		fail(s.I18n.Tf(lang, "exit_rules_admin.bulk_relay_unknown_relay", target))
		return
	}

	rules, err := s.bulkRulesByIDs(ids)
	if err != nil {
		fail(s.I18n.Tf(lang, "exit_rules_admin.bulk_relay_db_error", err.Error()))
		return
	}
	if len(rules) == 0 {
		fail(s.I18n.T(lang, "exit_rules_admin.bulk_relay_none"))
		return
	}
	plan := planBulkRelayMove(rules, s.prefixesForRules(rules), s.pinsByPrefix(), target, takePins)
	res := applyBulkRelayPlan(s.dbc(), plan, target)

	s.Backend.Audit(c.UserID, c.Username, "admin_bulk_relay_move",
		fmt.Sprintf("to=%s rules_selected=%d moved=%d unchanged=%d pins_moved=%d pins_kept=%d problems=%d take_pins=%v",
			target, len(ids), res.Moved, res.Unchanged, res.PinsMoved, len(res.KeptPins), len(res.Problems), takePins))

	// The two steps that make the move real (see the file header): the ACL must follow the
	// assignment table, and the new relay must advertise + be approved. Both are shared
	// paths, never a second pipeline.
	if res.Moved > 0 {
		s.ReapplyACLIfDrifted(c.Username,
			fmt.Sprintf("bulk relay move: %d rule(s) → %s", res.Moved, target))
		if s.SyncRoutes != nil {
			go func() {
				touched := s.SyncRoutes()
				log.Printf("[bulk-relay] route sync after moving %d rule(s) to %s: %d relay(s) touched",
					res.Moved, target, len(touched))
			}()
		}
	}

	msg := s.I18n.Tf(lang, "exit_rules_admin.bulk_relay_ok", res.Moved, target)
	switch {
	case res.Unchanged > 0 && res.Moved == 0:
		msg = s.I18n.Tf(lang, "exit_rules_admin.bulk_relay_all_unchanged", res.Unchanged, target)
	case res.Unchanged > 0:
		msg += " " + s.I18n.Tf(lang, "exit_rules_admin.bulk_relay_unchanged_tail", res.Unchanged)
	}
	if res.PinsMoved > 0 {
		msg += " " + s.I18n.Tf(lang, "exit_rules_admin.bulk_relay_pins_moved", res.PinsMoved)
	}
	if len(res.KeptPins) > 0 {
		msg += " " + s.I18n.Tf(lang, "exit_rules_admin.bulk_relay_pins_kept", len(res.KeptPins), pinKeepLabel(res.KeptPins))
	}
	if len(res.Problems) > 0 {
		msg += " " + s.I18n.Tf(lang, "exit_rules_admin.bulk_relay_problems", len(res.Problems), firstProblems(res.Problems))
		fail(msg)
		return
	}
	http.Redirect(w, r, back+"?bulk_relay_ok="+url.QueryEscape(msg), http.StatusSeeOther)
}

// parseRuleIDs reads the checkbox values, de-duplicated and bounded: a browser sends one
// value per checked box, and a hand-made request must not be able to flood the query.
func parseRuleIDs(raw []string) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(raw))
	for _, v := range raw {
		id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= BulkRelayMaxRules {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// pinKeepLabel renders the kept pins for a one-line flash: "104.16.0.0/12 → emilia, +2".
func pinKeepLabel(kept []pinKeep) string {
	parts := make([]string, 0, len(kept))
	const show = 2
	for i, k := range kept {
		if i >= show {
			parts = append(parts, fmt.Sprintf("+%d", len(kept)-show))
			break
		}
		parts = append(parts, fmt.Sprintf("%s → %s", k.Prefix, k.Relay))
	}
	return strings.Join(parts, ", ")
}
