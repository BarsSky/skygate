package exit_rules

// api_validate_b507.go — the API must refuse a rule it cannot honour.
//
// MEASURED (2026-11-20, live API run by the assistant): the only checks were
// `device_id != 0` and a non-empty target_value, so `subnet 1.2.3.4/99`,
// `domain mega..nz` and a typo in a relay name all went into the database and
// became INERT rules — rows that look like rules on the page, match nothing in the
// ACL and produce no error anywhere. An assistant (or a script) validating its own
// work by "the API said added: 7" then believes the tailnet is configured.
//
// These validators are pure so they can be tested without a database or a tailnet,
// and they produce the operator's own words in `reason` — including, for a relay,
// the list of relays that DO exist.

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// ruleValidation is the verdict for ONE requested rule.
type ruleValidation struct {
	OK     bool
	Reason string
}

// validateRuleTarget checks the (target_type, target_value) pair.
func validateRuleTarget(targetType, targetValue string) ruleValidation {
	tt := strings.ToLower(strings.TrimSpace(targetType))
	tv := strings.TrimSpace(targetValue)
	switch tt {
	case "ip", "subnet":
		if tv == "" {
			return ruleValidation{Reason: "target_value is empty"}
		}
		if _, _, err := net.ParseCIDR(tv); err != nil {
			// A bare address is accepted ONLY as an exact host route; anything else
			// (a typo, a prefix out of range, a hostname in the wrong field) is
			// refused here rather than stored as an inert row.
			if ip := net.ParseIP(tv); ip == nil {
				return ruleValidation{Reason: fmt.Sprintf("target_value %q is not an IP address or CIDR", tv)}
			}
			if tt == "subnet" && !strings.Contains(tv, "/") {
				return ruleValidation{Reason: fmt.Sprintf("a subnet needs a prefix length, got %q (use %s/32 for a single host)", tv, tv)}
			}
		} else {
			_, n, _ := net.ParseCIDR(tv)
			ones, bits := n.Mask.Size()
			if bits != 0 && ones == 0 && !strings.HasSuffix(tv, "/0") {
				return ruleValidation{Reason: fmt.Sprintf("target_value %q has an unusable prefix length", tv)}
			}
		}
		return ruleValidation{OK: true}
	case "domain":
		if tv == "" {
			return ruleValidation{Reason: "target_value is empty"}
		}
		if err := validateDomainName(tv); err != nil {
			return ruleValidation{Reason: err.Error()}
		}
		return ruleValidation{OK: true}
	case "":
		return ruleValidation{Reason: "target_type is empty (expected ip, subnet or domain)"}
	default:
		return ruleValidation{Reason: fmt.Sprintf("target_type %q is not one of ip, subnet, domain", targetType)}
	}
}

// validateDomainName refuses what DNS cannot answer: empty labels, a leading or
// trailing dot, a double dot, characters outside the hostname set, and a label
// longer than 63 octets. `mega..nz` is the live example from the API run.
func validateDomainName(v string) error {
	if len(v) > 253 {
		return fmt.Errorf("domain %q is longer than 253 characters", v)
	}
	if strings.HasPrefix(v, ".") || strings.HasSuffix(v, ".") {
		return fmt.Errorf("domain %q must not start or end with a dot", v)
	}
	if strings.Contains(v, "..") {
		return fmt.Errorf("domain %q has an empty label (double dot)", v)
	}
	if !strings.Contains(v, ".") {
		return fmt.Errorf("domain %q has no dot — a bare label cannot be resolved", v)
	}
	for _, label := range strings.Split(v, ".") {
		if label == "" {
			return fmt.Errorf("domain %q has an empty label", v)
		}
		if len(label) > 63 {
			return fmt.Errorf("domain %q has a label longer than 63 characters", v)
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				continue
			}
			return fmt.Errorf("domain %q contains %q, which is not allowed in a hostname", v, string(r))
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("domain %q has a label starting or ending with a hyphen", v)
		}
	}
	return nil
}

// validateRuleAction accepts the two actions the ACL can express. `deny` is stored
// but cannot be emitted into `grants[]` (B265 §5), so it is accepted here and
// reported honestly in the response rather than silently dropped.
func validateRuleAction(action string) ruleValidation {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "accept", "deny":
		return ruleValidation{OK: true}
	}
	return ruleValidation{Reason: fmt.Sprintf("action %q is not one of accept, deny", action)}
}

// validateExitNode refuses a relay name that does not exist, naming the ones that
// do — a typo used to become an inert rule whose traffic silently took the first
// healthy relay.
func validateExitNode(requested string, available []string) ruleValidation {
	want := strings.TrimSpace(requested)
	if want == "" {
		// "" means "the user's preferred / first healthy relay" — a real choice.
		return ruleValidation{OK: true}
	}
	for _, a := range available {
		if strings.EqualFold(strings.TrimSpace(a), want) {
			return ruleValidation{OK: true}
		}
	}
	sorted := append([]string(nil), available...)
	sort.Strings(sorted)
	if len(sorted) == 0 {
		return ruleValidation{Reason: fmt.Sprintf("exit_node %q is unknown — no exit node is currently registered", want)}
	}
	return ruleValidation{Reason: fmt.Sprintf("exit_node %q is unknown — available: %s (leave it empty to use the preferred relay)",
		want, strings.Join(sorted, ", "))}
}

// ruleStatusFor maps a writer outcome onto the API's `status` word. The B480 case
// is an ERROR, not a success: the caller named a relay that will not carry the
// traffic, and the response must say so with the relay that owns the target.
func ruleStatusFor(res ruleInsertResult) (status, reason string) {
	switch res.Outcome {
	case ruleInsertCreated:
		return "created", ""
	case ruleInsertExisting:
		return "existing", ""
	case ruleInsertOtherRelay:
		return "error", fmt.Sprintf("target is already pinned to relay %q for this device; nothing was written — moving it is a request (B440), not a duplicate",
			res.OtherRelay)
	case ruleInsertFailed:
		return "error", "db error"
	}
	return "error", "unknown outcome"
}
