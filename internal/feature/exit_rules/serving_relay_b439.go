// serving_relay_b439.go — B439 (2026-10-29) — WHO CARRIES THIS RULE, RIGHT NOW.
//
// WHY THIS EXISTS. Since B275 the relay that carries a prefix is decided by the
// `prefix_owner` assignment table, and `device_rules.exit_node_id` became the
// operator's WISH, not the route. Four user-visible answers were still computed from
// the wish:
//
//   - the status badge on /my/exit-rules ("approved" / "pending" / "wrong"),
//   - the "N rules reference an exit-node the device does not use" banner,
//   - the admin page's Applicable / ApprovedInHeadscale pair,
//   - the system test that counts the same mismatch.
//
// Live consequence on the reference deployment (2026-10-29): a domain's 25 ranges
// were owned by `karolina` while the rule named `emilia`; the traffic flowed, and the
// row still said the rule was NOT approved and that Tailscale would ignore it. The
// operator's words: «несмотря на политику авто-выбора доступного exit-node, в таблице
// висят пометки о расхождении в предпочтении устройства».
//
// This file is the single place that answers "who carries it", so the pages, the
// admin view and the tests cannot disagree with each other again. It is deliberately
// pure: no DB, no HTTP — the caller passes the owner map it already has.
package exit_rules

import "strings"

// ServingSource says WHY that relay is the carrier.
type ServingSource string

const (
	// ServingByOwner — the assignment table names this relay: it advertises the
	// prefix and its approved routes are what the client gets. This is the normal
	// state of a working rule since B275.
	ServingByOwner ServingSource = "owner"
	// ServingByRule — nothing owns the prefix yet, so the rule's own exit_node_id is
	// still the intent (a fresh rule, or an assignment pass that has not run).
	ServingByRule ServingSource = "rule"
	// ServingByAuto — neither: the engine has not chosen yet ("auto" rules).
	ServingByAuto ServingSource = "auto"
)

// Serving is the answer: which relay carries the rule, and why.
type Serving struct {
	Host   string
	Source ServingSource
}

// NormalizeRelay makes two spellings of the same relay comparable: tags and
// hostnames differ by case and by the `tag:dev-infra-` prefix, and a trailing dot or
// spaces have bitten comparisons before.
func NormalizeRelay(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "tag:dev-infra-")
	s = strings.TrimSuffix(s, ".")
	return s
}

// ServingRelayFor picks the carrier. `owner` is prefix_owner's relay hostname for
// this prefix ("" when nothing owns it); `ruleRelay` is device_rules.exit_node_id.
//
// The ORDER is the contract: an owner always wins, because the data plane follows the
// table — reporting the rule's wish as the carrier is exactly the false alarm this
// block removes.
func ServingRelayFor(ruleRelay, owner string) Serving {
	if h := NormalizeRelay(owner); h != "" {
		return Serving{Host: h, Source: ServingByOwner}
	}
	if h := NormalizeRelay(ruleRelay); h != "" {
		return Serving{Host: h, Source: ServingByRule}
	}
	return Serving{Source: ServingByAuto}
}

// ServedByAnotherRelay reports whether the rule is carried by a relay other than the
// one it names — the fact worth SHOWING ("обслуживает X"), never worth a red row.
func ServedByAnotherRelay(ruleRelay, owner string) bool {
	s := ServingRelayFor(ruleRelay, owner)
	return s.Source == ServingByOwner && s.Host != NormalizeRelay(ruleRelay)
}

// IsRelayMismatch is the one comparison that still deserves a warning: the device has
// a preference, the prefix has NO owner (so the rule's own relay is the live intent),
// and that relay is not the preference. Everything else — an owner-served rule, an
// auto rule, a device without a preference — is not a mismatch, and calling it one is
// what made the banner permanent.
func IsRelayMismatch(ruleRelay, pref, owner string) bool {
	if strings.TrimSpace(pref) == "" {
		return false
	}
	if NormalizeRelay(owner) != "" {
		return false
	}
	r := NormalizeRelay(ruleRelay)
	if r == "" {
		return false
	}
	return r != NormalizeRelay(pref)
}

// OwnerOfResolved picks the single owner shared by every resolved prefix of a DOMAIN
// rule, or "" when they disagree (or none is owned): a domain rule is carried by one
// relay only when all of its ranges are, and guessing one of several owners would
// repeat the same "report a wish as a fact" mistake.
func OwnerOfResolved(resolved map[string]bool, ownerByPrefix map[string]string) string {
	if len(resolved) == 0 || len(ownerByPrefix) == 0 {
		return ""
	}
	first := ""
	for prefix := range resolved {
		o := NormalizeRelay(ownerByPrefix[prefix])
		if o == "" {
			return ""
		}
		if first == "" {
			first = o
			continue
		}
		if o != first {
			return ""
		}
	}
	return first
}
