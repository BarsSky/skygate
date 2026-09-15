// File: internal/feature/admin/infra_owner_sanity.go
//
// B-bug-fix (2026-09-15): startup sanity check for the "infra"
// headscale user. Live case: agent VM 192.168.13.69.
//
// Background (see AGENTS.md "Issue 4 infra user"):
//   - skygate provisions a dedicated headscale user "infra" at
//     startup (ensureInfraUser in cmd/skygate/main.go).
//   - Per the design, all infrastructure nodes — skygate-host-*
//     VMs + their attached exit-nodes (emilia/karolina/sharlotta)
//     — should be owned by "infra" so the ACL grants
//     `infra → autogroup:internet, tag:exit-*` apply.
//   - Pre-fix: there was NO check. The skygate-host-1-1 tailscale
//     node ended up on the synthetic "tagged-devices" user (id=11)
//     while the "infra" user (id=85) existed with zero nodes. The
//     grant-based access path was silently dead.
//
// SanityCheckInfraUserOwners is called from cmd/skygate/main.go
// after ensureInfraUser. It returns a list of nodes that the
// AGENTS.md says should belong to "infra" but don't, so the caller
// can log a clear remediation message.
//
// The function NEVER mutates state — moving a node to "infra" is a
// destructive operation (delete + re-register with a new preauth
// key for "infra"; the existing tailscaled on the VM must
// re-authenticate). The operator must do it deliberately via the
// gRPC API or via /admin/devices (delete the row, then register
// again with --user infra).
//
// Detection rules — a node SHOULD belong to "infra" if:
//   1. Its hostname starts with "skygate-host-" (the skygate VMs
//      themselves), OR
//   2. It has the `tag:exit-node` or `tag:dev-infra-*` tag (the
//      per-device dev-tag applied by the B175 Strategy E backfill
//      and the `tag:exit-node` from /admin/exit-nodes).
//
// Live case 2026-09-15: skygate-host-1-1 (id=43, this VM) has
// `tag:dev-skyadmin-skygate-host-1` (wrong — should be
// `tag:dev-infra-*`) AND is on `tagged-devices` user (id=11, wrong
// — should be `infra`). Both flags trip. Other infrastructure nodes
// (emilia/karolina/sharlotta) already have the right
// `tag:dev-infra-*` but are also on `tagged-devices` user — only
// the user-ownership flag trips for them.
//
// 2026-09-15: v1.5.3 — B-bug-fix (infra user provisioned but no nodes).

package admin

import (
	"log"
	"strconv"
	"strings"

	"skygate/internal/headscale"
)

// InfraOwnerMismatch describes one node that should belong to the
// "infra" headscale user per AGENTS.md but doesn't. Returned to the
// caller so the warning log + (future) admin remediation page can
// surface each affected node separately.
type InfraOwnerMismatch struct {
	NodeID       int64
	GivenName    string
	Hostname     string
	TailscaleIPs []string
	UserName     string // current user (e.g. "tagged-devices")
	Tags         []string
	// Mismatches is the list of rules this node violated. Each rule
	// is a stable identifier (e.g. "should_be_infra_user",
	// "should_have_tag_dev_infra"). The caller can map these to
	// user-facing remediation text.
	Mismatches []string
}

// SanityCheckInfraUserOwners queries headscale and returns the
// nodes that should belong to "infra" but don't. Read-only — no
// state is mutated. Safe to call on every skygate boot.
//
// Returns nil + nil if:
//   - headscale.ListAllNodes fails (operator can re-run later)
//   - The "infra" user doesn't exist yet (would be unusual —
//     ensureInfraUser runs before this; if it failed, log already
//     shows the cause)
//   - There are no mismatches
//
// The function logs ONE summary line at WARN level for the whole
// call, plus one DEBUG line per mismatch. The caller can also call
// this for an explicit report (e.g. /admin/healthcheck in future).
func SanityCheckInfraUserOwners(hs *headscale.Client) ([]InfraOwnerMismatch, error) {
	if hs == nil {
		return nil, nil
	}
	users, err := hs.ListUsers()
	if err != nil {
		return nil, err
	}
	var infraID string
	for _, u := range users {
		if u.Name == "infra" {
			infraID = u.ID
			break
		}
	}
	if infraID == "" {
		// No infra user yet — ensureInfraUser either hasn't run
		// or failed silently. Don't warn here; the earlier log
		// line is the real signal.
		return nil, nil
	}

	allNodes, err := hs.ListAllNodes()
	if err != nil {
		return nil, err
	}

	var mismatches []InfraOwnerMismatch
	for _, n := range allNodes {
		// We use Hostname (set by headscale.Client.toView) and
		// fall back to GivenName when Hostname is empty.
		hn := n.Hostname
		if hn == "" {
			hn = n.GivenName
		}
		ruleViolations := shouldBelongToInfra(hn, n.UserName, n.Tags)
		if len(ruleViolations) == 0 {
			continue
		}
		mismatches = append(mismatches, InfraOwnerMismatch{
			NodeID:       parseInt64(n.ID),
			GivenName:    n.GivenName,
			Hostname:     hn,
			TailscaleIPs: n.IPAddresses,
			UserName:     n.UserName,
			Tags:         n.Tags,
			Mismatches:   ruleViolations,
		})
	}
	if len(mismatches) > 0 {
		log.Printf("infra-sanity: %d infrastructure node(s) not owned by headscale user 'infra' — remediate via /admin/devices (delete + re-register with --user infra)", len(mismatches))
		for _, m := range mismatches {
			log.Printf("infra-sanity:   id=%d hostname=%q user=%q tags=%v mismatches=%v",
				m.NodeID, m.Hostname, m.UserName, m.Tags, m.Mismatches)
		}
	}
	return mismatches, nil
}

// shouldBelongToInfra is the rule engine. Pure function — easy to
// unit test. Returns the list of violation identifiers (empty =
// node is correctly on infra, or not a candidate at all).
//
// Rules:
//   R1: hostname starts with "skygate-host-"  → must be on infra
//       AND must have a tag starting with "tag:dev-infra-"
//   R2: has tag:exit-node  → must be on infra
//   R3: has tag:dev-infra-*  → must be on infra
//
// Each violation is a stable string the caller can map to a
// remediation message.
func shouldBelongToInfra(hostname, currentUser string, tags []string) []string {
	var violations []string

	// Candidate detection: is this node even supposed to be on infra?
	isCandidate := false
	if strings.HasPrefix(hostname, "skygate-host-") {
		isCandidate = true
	}
	for _, t := range tags {
		if t == "tag:exit-node" || strings.HasPrefix(t, "tag:dev-infra-") {
			isCandidate = true
			break
		}
	}
	if !isCandidate {
		return nil
	}

	if currentUser != "infra" {
		violations = append(violations, "should_be_infra_user")
	}

	// For skygate-host-*: also check that the dev-tag starts with
	// `tag:dev-infra-` (the B175 Strategy E naming convention).
	if strings.HasPrefix(hostname, "skygate-host-") {
		hasInfraTag := false
		for _, t := range tags {
			if strings.HasPrefix(t, "tag:dev-infra-") {
				hasInfraTag = true
				break
			}
		}
		if !hasInfraTag {
			violations = append(violations, "should_have_tag_dev_infra")
		}
	}
	return violations
}

// parseInt64 parses s as int64, returning 0 on error. Used for the
// NodeID field because headscale.NodeView.ID is a string (the
// headscale API returns IDs as decimal strings — see hsNode.ID).
func parseInt64(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}