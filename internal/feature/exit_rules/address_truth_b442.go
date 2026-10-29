// address_truth_b442.go — B442 (2026-10-29) — «корректность инструмента что проверяет
// конечный адрес и собирает необходимую информацию о том какие подсети и адреса
// необходимо также добавить в список на маршрутизацию».
//
// Three answers the resolver gave that were not true, each with a small pure helper so the
// rule can be tested without DNS, a database or a headscale client:
//
//   - SplitAnswers: an AAAA answer was silently DISCARDED, and a domain that only answers
//     over IPv6 therefore looked like a domain that resolves to nothing — its rows were
//     deleted on the spot and no page, log line or notification said why.
//   - ConfirmDeletions: one DNS answer was treated as the whole truth, so a resolver that
//     returns a PARTIAL set (round-robin, a transient SERVFAIL for one name, a CDN moving
//     an address) deleted live /32 rows — and with them the ACL grant and the advertised
//     route, until the next tick re-added them. An address is now dropped only when a
//     SECOND lookup still does not answer it.
//   - The deny filter (see sync_domain.go / sync_routes.go / prefixowner.LoadClaims): a
//     `deny` rule claims a prefix like an accept rule does, so it could decide WHO SERVES
//     that prefix while grants[] cannot express "deny" at all (B265). A refused
//     destination pulled a prefix onto a relay for no reason.
package exit_rules

import "strings"

// SplitAnswers separates the A answers from the AAAA ones. The callers need both halves
// explicitly: the v4 half is what the ACL and the routes can carry today, and the v6 half
// has to be REPORTED instead of silently dropped.
func SplitAnswers(addrs []string) (v4, v6 []string) {
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.Contains(a, ":") {
			v6 = append(v6, a)
			continue
		}
		v4 = append(v4, a)
	}
	return v4, v6
}

// ConfirmDeletions decides which stored addresses may be removed.
//
// `stored` is every resolved row this domain owns, `first` the answer that triggered the
// reconciliation and `second` a second lookup of the same name. An address is dropped only
// when BOTH lookups answered and neither contains it: a single partial answer is not
// evidence that an address stopped existing, and deleting a /32 also deletes its ACL grant
// and its advertised route.
//
// secondAnswered is a separate argument on purpose: when the confirming lookup FAILS the
// map is empty, and an empty map must never be read as "the address is gone" — that is the
// exact confusion between "no data" and "a negative answer" that this block removes.
func ConfirmDeletions(stored []string, first, second map[string]bool, secondAnswered bool) (drop, kept []string) {
	for _, ip := range stored {
		ip = strings.TrimSpace(ip)
		if ip == "" || !secondAnswered || first[ip] || second[ip] {
			kept = append(kept, ip)
			continue
		}
		drop = append(drop, ip)
	}
	return drop, kept
}

// DenyClaimsPrefix reports whether a rule of this action is allowed to VOTE on who serves
// its prefix. Deny rules are not expressible in headscale grants[] (B265 logs how many are
// skipped), so letting one decide the owner of a prefix moves a route for a destination
// nobody may reach — and it can steal the ownership of a prefix an accept rule needs.
//
// An EMPTY action is an accept: `action` was added after the first rules existed (v0.31),
// and COALESCE(action,'accept') is what every query in this project already assumes.
func DenyClaimsPrefix(action string) bool {
	a := strings.TrimSpace(action)
	return a == "" || strings.EqualFold(a, "accept")
}
