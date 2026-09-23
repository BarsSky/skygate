// internal/feature/exit_rules/domain_interval_b308.go — B308 (v1.5.73).
//
// WHY THIS EXISTS (live evidence, agent VM, 2026-09-23)
//
// /admin/exit-nodes kept showing the red «политика headscale УСТАРЕЛА» banner on
// both hosts, and the policy was rewritten over and over, even though the operator
// had changed nothing. The journal said why:
//
//	17:41:02 acl-drift: auto-updater tick changed 38 rule(s) (added=19 removed=19) — deferring (throttle 30m0s)
//	17:46:02 acl-drift: auto-updater tick changed 34 rule(s) (added=17 removed=17) — deferring
//	18:11:16 acl-drift: auto-updater tick changed 50 rule(s) (added=18 removed=32) — deferring
//	18:16:00 acl-drift: auto-updater tick changed 50 rule(s) (added=32 removed=18) — deferring
//	18:51:04 acl-drift: ACL re-applied (snapshot v1655, generated=50420 bytes)
//
// 39 drift/defer lines in two hours, with ±20 rules per five-minute tick. The
// rules were not the operator's: `DomainAutoUpdater` re-resolved every domain rule
// on every tick, and a domain whose A records rotate (ghcr.io, quay.io,
// minimax.io, …) returns a slightly different IP set each time — so the derived
// /32 rows were deleted and re-inserted forever, the generated ACL never stopped
// changing, the drift banner was red almost permanently, and on a `policy.mode:
// file` host every (throttled) re-apply restarted headscale.
//
// The resolution itself is not wrong — an IP that moved must follow — but it does
// not need to happen every five minutes. This file adds the missing policy: a
// per-domain minimum re-resolve interval, stored in global_settings so the
// operator can change it from the panel without a restart, defaulting to six
// hours (DNS answers that rotate inside six hours are a CDN's business, and the
// CDN path has its own stable range set anyway).
//
// A domain that has never been resolved is always due, and a failed lookup does
// not mark the domain as resolved, so an unreachable resolver is retried on the
// next tick instead of being skipped for hours.
package exit_rules

import (
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
)

// SettingDomainResolveIntervalSec is the global_settings key holding the minimum
// number of seconds between two resolutions of the same domain.
const SettingDomainResolveIntervalSec = "dns_domain_resolve_interval_sec"

// SettingDomainResolveAtPrefix is the global_settings key PREFIX holding the last
// successful resolution time (unix seconds) for one domain. The domain is
// lower-cased and appended.
const SettingDomainResolveAtPrefix = "domain_resolve_at:"

const (
	// DefaultDomainResolveInterval is used when the operator never set one.
	DefaultDomainResolveInterval = 6 * time.Hour
	// MinDomainResolveInterval is the floor for the setting: below this the gate
	// cannot do its job (the autoupdater itself ticks at five minutes).
	MinDomainResolveInterval = 5 * time.Minute
	// MaxDomainResolveInterval is the ceiling: a rule whose IP moved must still
	// follow within a week.
	MaxDomainResolveInterval = 7 * 24 * time.Hour
)

// ParseDomainResolveInterval turns the stored value into a duration, clamped to
// [Min, Max]. Empty or unparseable input yields the default, and a stored "0"
// (or negative) DISABLES the gate — i.e. resolve every tick, the pre-B308
// behaviour, which is a legitimate choice for an operator chasing a moving
// target. Exported so the admin page shows the same effective value the updater
// uses.
func ParseDomainResolveInterval(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultDomainResolveInterval
	}
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return DefaultDomainResolveInterval
	}
	if secs <= 0 {
		return 0 // explicitly unlimited: every tick
	}
	return ClampDomainResolveInterval(time.Duration(secs) * time.Second)
}

// parseDomainResolveInterval is the internal alias kept for readability at the
// call sites inside this package.
func parseDomainResolveInterval(raw string) time.Duration { return ParseDomainResolveInterval(raw) }

// domainResolveDue reports whether a domain whose last successful resolution was
// `last` (unix seconds, 0 = never) should be resolved again at `now`.
//
// interval <= 0 means "no gate" (resolve every tick). A domain that was never
// resolved is always due, whatever the interval.
func domainResolveDue(last, now int64, interval time.Duration) bool {
	if interval <= 0 {
		return true
	}
	if last <= 0 {
		return true
	}
	if now < last {
		// Clock went backwards (NTP step, container restart with a skewed
		// clock): treat the domain as due rather than freezing it for hours.
		return true
	}
	return now-last >= int64(interval.Seconds())
}

// DomainResolveInterval reads the configured interval (default six hours). A nil
// database (unit tests, a boot that failed before the DB opened) yields the
// default.
func (s *Service) DomainResolveInterval() time.Duration {
	if s == nil || s.dbc() == nil {
		return DefaultDomainResolveInterval
	}
	raw, err := db.GetGlobalSetting(s.dbc(), SettingDomainResolveIntervalSec, "")
	if err != nil {
		return DefaultDomainResolveInterval
	}
	return parseDomainResolveInterval(raw)
}

// ClampDomainResolveInterval applies the policy's floor/ceiling to a duration the
// operator typed. Exported so the admin handler stores exactly the value the
// updater will use (and can show it back in the flash).
func ClampDomainResolveInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return 0 // explicitly unlimited: resolve every tick
	}
	if d < MinDomainResolveInterval {
		return MinDomainResolveInterval
	}
	if d > MaxDomainResolveInterval {
		return MaxDomainResolveInterval
	}
	return d
}

// domainResolveDueFor is the DB-backed half: it reads the domain's last
// successful resolution and applies domainResolveDue.
func (s *Service) domainResolveDueFor(domain string, now int64, interval time.Duration) bool {
	if s == nil || s.dbc() == nil {
		return true
	}
	key := SettingDomainResolveAtPrefix + strings.ToLower(strings.TrimSpace(domain))
	raw, err := db.GetGlobalSetting(s.dbc(), key, "")
	if err != nil {
		return true // cannot tell → resolve (never leave a rule unrefreshed)
	}
	last, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return domainResolveDue(last, now, interval)
}

// markDomainResolved records a SUCCESSFUL resolution so the next ticks skip the
// domain until the interval elapses. Callers must not call it after a failed
// lookup.
func (s *Service) markDomainResolved(domain string, now int64) {
	if s == nil || s.dbc() == nil {
		return
	}
	key := SettingDomainResolveAtPrefix + strings.ToLower(strings.TrimSpace(domain))
	if err := db.SetGlobalSetting(s.dbc(), key, strconv.FormatInt(now, 10)); err != nil {
		// Not fatal: the worst case is one extra resolve next tick.
		return
	}
}

// DomainResolveIntervalLabel renders the effective policy for the panel and for
// the skipped-domains log line: "6h0m0s" or "every tick (gate disabled)".
func DomainResolveIntervalLabel(d time.Duration) string {
	if d <= 0 {
		return "every tick (gate disabled)"
	}
	return d.String()
}
