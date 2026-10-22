// system_tests_domain_verdict_b411.go — B411 (2026-10-22): the pieces the
// exit_rules.domain_prefix test needs to answer "will this heal by itself?".
//
// Kept in their own file so the test body stays readable and so the DB read
// (the B308 per-domain resolve stamp) has exactly one implementation: the same
// key the auto-updater writes, lower-cased the same way (markDomainResolved).
package admin

import (
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/feature/exit_rules"
)

// b411LastResolve returns the unix time of the last SUCCESSFUL resolve of domain
// according to the B308 stamp, or 0 when the domain has never been resolved (or
// the value is unreadable — which exit_rules.DomainRowsVerdictFor treats as
// "never", the conservative answer: it reports a defect rather than hiding one).
func b411LastResolve(s *Service, domain string) int64 {
	if s == nil || s.dbc() == nil {
		return 0
	}
	key := exit_rules.SettingDomainResolveAtPrefix + strings.ToLower(strings.TrimSpace(domain))
	raw, err := db.GetGlobalSetting(s.dbc(), key, "")
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return v
}

// b411FixHint names the action for a domain that will not heal by itself. The
// distinction matters: "enable auto-update" and "wait for the interval" are
// different instructions and the panel could not tell them apart (B396).
func b411FixHint(autoOn bool, interval time.Duration) string {
	if !autoOn {
		return "DNS auto-update is OFF: nothing will rewrite these rows until you enable it on this page (or press Re-sync on the rule)"
	}
	if interval <= 0 {
		return "DNS auto-update is ON and resolves every tick, so this should have healed already — check the auto-updater log for a failed lookup for these domains"
	}
	return "DNS auto-update is ON (every " + exit_rules.DomainResolveIntervalLabel(interval) +
		") but the interval has already elapsed for these domains, so the pass that should have rewritten them did not — check the auto-updater log"
}
