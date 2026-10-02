// exit_nodes_helpers.go — the small pure helpers the page and the handlers share.
//
// Split out of exit_nodes.go in refactor Phase D (2026-10-01): string/list
// plumbing, the exit-node tag predicate, the sync-status calculator and the
// headscale-version banner readers. None of them touch the database, which is
// what makes them the natural unit to read first when the page misbehaves.

package admin

import (
	"fmt"
	"strings"
	"time"
)

// splitCommaList splits the comma-joined tailscale_ip column into its entries.
// Pure; used by the B293 co-location check (a relay's address list can carry IPv4
// and IPv6, and the local daemon may own either).
func splitCommaList(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// effectiveExitSSHKeyPath returns the SSH private key the next advertised-routes
// sync will use for a relay whose exit_servers.ssh_key_path is `rowKeyPath`
// (B292).
//
// The row's own value wins; otherwise the global default from Config
// (SKYGATE_EXIT_SSH_KEY, or the install-kind default — see
// config.resolveExitSSHKeyPath). Pure, so the page can be tested without a
// filesystem.
func (s *Service) effectiveExitSSHKeyPath(rowKeyPath string) string {
	if p := strings.TrimSpace(rowKeyPath); p != "" {
		return p
	}
	if s != nil && s.Cfg != nil {
		return strings.TrimSpace(s.Cfg.SSHKeyPath)
	}
	return ""
}

// hasExitNodeTagFor reports whether the headscale tag list carries
// tag:exit-node. B273 (v1.5.18) — the comparison is
// case-insensitive so it agrees with headscale's own
// hasExitNodeTag (which uses strings.EqualFold) and with the health
// monitor; the pre-B273 exact `== "tag:exit-node"` test meant a node
// tagged `Tag:Exit-Node` was "tagged" for routing and "untagged" for
// the health banner.
func hasExitNodeTagFor(tags []string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, "tag:exit-node") {
			return true
		}
	}
	return false
}

// computeSyncStatus is the pure helper that decides
// whether an exit node's advertised-routes count from
// headscale matches the count of device_rules in skygate
// that target that node.
//
// 2026-07-30: v0.32.3 — extracted from the inline loop
// in AdminExitNodes so the contract is unit-testable
// (see exit_nodes_test.go). The function is small and
// has no side effects; the integration between
// computeSyncStatus + the headscale-fetching code path
// is covered by the live verify-post checks.
//
// Returns one of:
//
//	""                            — no rules target this node, no status
//	"synced"                      — skygate rules count == headscale routes
//	"mismatch: have N, want M"    — drift detected
//
// "have N" is the headscale-side count (len(AvailableRoutes))
// and "want M" is the skygate-side count (device_rules
// with exit_node_id == hostname). When "want M" is 0 the
// status stays empty (the node is not in use from skygate's
// view; headscale may still have routes from the operator's
// manual setup, and that's fine).
//
// The "mismatch" wording is preserved verbatim — the
// /admin/exit-nodes page renders this string in the
// "СТАТУС" column and operators have come to expect it.
func computeSyncStatus(hostname string, routeCount int, expectedRoutes map[string]int) string {
	expected := expectedRoutes[hostname]
	if expected > 0 && routeCount != expected {
		return fmt.Sprintf("mismatch: have %d, want %d", routeCount, expected)
	}
	if expected > 0 {
		return "synced"
	}
	return ""
}

// headscaleUpdateForBanner is a small helper that
// returns the headscale-update-monitor's
// UpdateAvailable flag (or false if the monitor is
// not wired). Keeping the helper separate from
// the data map means the template can use it as a
// single condition without nil-checks inline.
//
// v0.20.0. 2026-07-20.
func headscaleUpdateForBanner(s *Service) bool {
	if s.HeadscaleUpdateMonitor == nil {
		return false
	}
	_, upd, _, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return upd
}

// headscaleBreakingForBanner returns the
// BreakingAvailable flag (same nil-safe pattern).
func headscaleBreakingForBanner(s *Service) bool {
	if s.HeadscaleUpdateMonitor == nil {
		return false
	}
	_, _, brk, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return brk
}

// headscaleLatestTag returns the latest seen release
// tag (or "" if the monitor is not wired / hasn't
// polled yet).
func headscaleLatestTag(s *Service) string {
	if s.HeadscaleUpdateMonitor == nil {
		return ""
	}
	latest, _, _, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return latest.TagName
}

// headscalePinnedTag returns the operator's pinned
// version (or "").
func headscalePinnedTag(s *Service) string {
	if s.HeadscaleUpdateMonitor == nil {
		return ""
	}
	_, _, _, _, _, pinned := s.HeadscaleUpdateMonitor.Snapshot()
	return pinned
}

// headscaleHTMLURL returns the GitHub release URL
// for the latest seen release (or "").
func headscaleHTMLURL(s *Service) string {
	if s.HeadscaleUpdateMonitor == nil {
		return ""
	}
	latest, _, _, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return latest.HTMLURL
}

// humanizeDuration formats a time.Duration as a short
// human-readable string ("3s", "2m 14s", "1h 5m", "2d 3h").
// Used by /admin/exit-nodes to render the "last seen X ago"
// column without pulling moment.js / dayjs. Negative inputs
// are treated as "0s" (the monitor's clock skew can produce
// these on a clock-adjusting laptop).
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd %dh", days, hours)
}
