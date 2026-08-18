// Package ha — High-Availability chain for skygate (v1.5.0 / B145).
//
// The HA model is **active-passive with priority chain**:
//   - One node holds the "active" role (responds to skygate.skynas.ru,
//     holds the DNS A-record, owns the Patroni primary).
//   - The rest of the chain are "standby" nodes, ordered by
//     priority (lower number = higher priority; P1 is the
//     preferred active, P2 the first failover target, etc).
//   - The "next active to promote" is the lowest-priority ALIVE
//     member in the chain. "Alive" = heartbeat seen within the
//     MissedThreshold * HeartbeatInterval window.
//
// State persistence
// -----------------
// The chain is stored as a single JSON array under the
// `global_settings.ha_chain` key. This keeps the schema
// migration-free (the `global_settings` table is already in
// every skygate deploy) and makes admin-driven edits trivial
// (the future /admin/ha page just rewrites the JSON).
//
// SecretBox
// ---------
// The reg.ru SSL cert + alternative password live in
// `internal/ha/regapi/credentials.go` and are stored in
// `global_settings` rows encrypted via `db.EncryptForColumn`
// (AES-256-GCM keyed by SKYGATE_SECRET_KEY).
package ha

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// HaRole is a node's role within the HA chain.
//
// "active"   — currently serving skygate.skynas.ru (owns DNS,
//              Patroni primary, holds the cert + secret).
// "standby"  — alive and ready to take over; lower priority = later
//              in the failover order.
// "unreachable" — heartbeat missed; should be removed from the
//              active rotation until it recovers.
type HaRole string

const (
	RoleActive      HaRole = "active"
	RoleStandby     HaRole = "standby"
	RoleUnreachable HaRole = "unreachable"
	// RoleUnknown is the zero value when the elector has not
	// yet observed the node's Patroni state.
	RoleUnknown HaRole = "unknown"
)

// HaMember is one node in the HA chain. The chain is ordered
// by Priority (ascending: 1 is the highest priority / preferred
// active). Hostname is the headscale MagicDNS name (e.g. "skygate",
// "skygate-standby"); PublicIP is what the external DNS A-record
// should resolve to when this node is active.
type HaMember struct {
	Hostname  string  `json:"hostname"`
	Priority  int     `json:"priority"`
	PublicIP  string  `json:"public_ip,omitempty"`
	TailscaleIP string `json:"tailscale_ip,omitempty"`
	LastSeen  int64   `json:"last_seen_unix,omitempty"` // 0 = never seen
	Role      HaRole  `json:"role,omitempty"`
}

// HaChain is the entire chain. Members MUST be sorted by
// Priority ascending (1, 2, 3, ...). The chain with no
// members is valid (e.g. on a fresh deploy before /admin/ha
// is configured).
type HaChain struct {
	Members []HaMember `json:"members"`
	// AutoFailoverEnabled is the operator toggle. When false,
	// the elector never auto-promotes; the operator must use
	// the "Force promote" / "Force demote" buttons on /admin/ha.
	AutoFailoverEnabled bool `json:"auto_failover_enabled"`
	// LastTransitionUnix is the timestamp of the most recent
	// role change, used for the "last failover was N seconds
	// ago" stat on the UI. 0 = never transitioned.
	LastTransitionUnix int64 `json:"last_transition_unix,omitempty"`
}

// DefaultHeartbeatInterval is how often each node checks the
// others (5s — same as the rest of the in-app schedulers
// like /admin/backup, /admin/system_tests, smoke-mesh cleanup).
// Per the v1.5.0 plan: 5s tick, 3 missed = 15s failover
// threshold.
const DefaultHeartbeatInterval = 5 * time.Second

// DefaultMissedThreshold is how many consecutive missed
// heartbeats before a member is considered unreachable. With
// the 5s default interval, 3 missed = 15s. The plan locks
// these as the defaults; per-deploy override is via env vars.
const DefaultMissedThreshold = 3

// Validate checks the chain for the invariants the rest of
// the package relies on:
//   - all hostnames non-empty and unique
//   - all priorities unique and >= 1
//   - at most one member has Role=RoleActive
//
// Returns nil on success. The error is suitable for direct
// surfacing to the /admin/ha UI form (no internal jargon).
func (c *HaChain) Validate() error {
	if c == nil {
		return errors.New("ha: nil chain")
	}
	seenHost := make(map[string]struct{}, len(c.Members))
	seenPrio := make(map[int]struct{}, len(c.Members))
	activeCount := 0
	for i, m := range c.Members {
		if m.Hostname == "" {
			return fmt.Errorf("ha: members[%d]: hostname is empty", i)
		}
		if _, dup := seenHost[m.Hostname]; dup {
			return fmt.Errorf("ha: members[%d]: duplicate hostname %q", i, m.Hostname)
		}
		seenHost[m.Hostname] = struct{}{}
		if m.Priority < 1 {
			return fmt.Errorf("ha: members[%d] (%s): priority must be >= 1, got %d", i, m.Hostname, m.Priority)
		}
		if _, dup := seenPrio[m.Priority]; dup {
			return fmt.Errorf("ha: members[%d] (%s): duplicate priority %d", i, m.Hostname, m.Priority)
		}
		seenPrio[m.Priority] = struct{}{}
		if m.Role == RoleActive {
			activeCount++
		}
	}
	if activeCount > 1 {
		return fmt.Errorf("ha: at most one member may have role=active, found %d", activeCount)
	}
	return nil
}

// SortedByPriority returns a copy of the chain's members
// sorted by Priority ascending (P1 first, P2 second, ...).
// The receiver is not modified.
func (c *HaChain) SortedByPriority() []HaMember {
	if c == nil {
		return nil
	}
	out := make([]HaMember, len(c.Members))
	copy(out, c.Members)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		// Tiebreaker: hostname ascending, so the order is
		// deterministic when the operator forgets to assign
		// distinct priorities (caught by Validate, but
		// defence-in-depth in case the validation is skipped
		// during a partial edit).
		return out[i].Hostname < out[j].Hostname
	})
	return out
}

// FindByHostname returns the index of the member with the
// given hostname, or -1 if absent. Case-sensitive.
func (c *HaChain) FindByHostname(host string) int {
	if c == nil {
		return -1
	}
	for i, m := range c.Members {
		if m.Hostname == host {
			return i
		}
	}
	return -1
}

// Marshal returns the JSON bytes used for the
// `global_settings.ha_chain` row. Errors only on
// impossible states (NaN/Inf in fields, which HaMember
// doesn't allow).
func (c *HaChain) Marshal() ([]byte, error) {
	return json.Marshal(c)
}

// UnmarshalChain is the inverse of Marshal. Returns
// errors for invalid JSON or a chain that fails Validate.
// An empty chain (zero members) is valid and decodes to
// the zero value of HaChain.
func UnmarshalChain(data []byte) (*HaChain, error) {
	if len(data) == 0 {
		return &HaChain{}, nil
	}
	var c HaChain
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("ha: parse chain: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// IsAlive returns true if the member's LastSeen is recent
// enough to be considered reachable. "Recent" is
// (HeartbeatInterval * MissedThreshold) — i.e. the
// member is "alive" if it was seen within that window.
//
// A member with LastSeen=0 (never seen) is NOT alive.
// This guards against a fresh deploy where the chain was
// saved but the elector has not run any ticks yet.
func (m HaMember) IsAlive(heartbeatInterval time.Duration, missedThreshold int) bool {
	if m.LastSeen == 0 {
		return false
	}
	windowSec := int64(heartbeatInterval.Seconds() * float64(missedThreshold))
	if windowSec < 1 {
		windowSec = int64(heartbeatInterval.Seconds())
	}
	return time.Now().Unix()-m.LastSeen <= windowSec
}

// ActiveMember returns a pointer to the member with
// Role=RoleActive, or nil if no member is currently active
// (e.g. on a fresh deploy with no chain yet, or after the
// last active was just demoted and the new active is being
// computed). The pointer is into the receiver's slice; do
// not retain past mutations.
func (c *HaChain) ActiveMember() *HaMember {
	for i := range c.Members {
		if c.Members[i].Role == RoleActive {
			return &c.Members[i]
		}
	}
	return nil
}

// FindOrZero returns a pointer to the member with the given
// hostname, or a pointer to a zero-value HaMember if absent.
// The "zero" trick is convenient for the OnTransition
// callback signature (always a valid HaMember, never nil).
func (c *HaChain) FindOrZero(host string) HaMember {
	for _, m := range c.Members {
		if m.Hostname == host {
			return m
		}
	}
	return HaMember{Hostname: host}
}

// NextActiveToPromote decides which member should hold the
// active role right now, given the current Patroni state on
// THIS node. The algorithm:
//
//  1. If selfIsPrimary is true and self is in the chain and
//     alive: self wins (Patroni said we're the PG primary,
//     and we're reachable).
//  2. Otherwise: the lowest-priority ALIVE member wins. The
//     current active (if any) is preferred over others at
//     the same priority to avoid a flap when both nodes are
//     alive but the primary just hopped (we wait for the
//     active to truly time out before demoting).
//
// Returns the chosen hostname, or "" if the chain is empty
// or no member is alive (the operator needs to fix
// something — we don't auto-allocate roles on a fully-dead
// chain).
func (c *HaChain) NextActiveToPromote(missedThreshold int, heartbeatInterval time.Duration, selfHostname string, selfIsPrimary bool) string {
	if len(c.Members) == 0 {
		return ""
	}
	// Case 1: self is primary + alive → self wins.
	if selfIsPrimary && selfHostname != "" {
		idx := c.FindByHostname(selfHostname)
		if idx >= 0 && c.Members[idx].IsAlive(heartbeatInterval, missedThreshold) {
			return selfHostname
		}
	}
	// Case 2: lowest-priority alive member.
	sorted := c.SortedByPriority()
	for _, m := range sorted {
		if m.IsAlive(heartbeatInterval, missedThreshold) {
			return m.Hostname
		}
	}
	// No member is alive. Stay where we are (don't
	// promote to "" because the chain would lose its
	// active entirely; operators will see the "no
	// members alive" state on /admin/ha and intervene).
	current := c.ActiveMember()
	if current != nil {
		return current.Hostname
	}
	return ""
}

// ApplyActiveRole updates the chain so that the given
// hostname (or "" to clear the active) has RoleActive and
// every other member has RoleStandby. The transition unix
// is set to `nowSec` and LastSeen for the new active is
// refreshed to `nowSec` (so a re-pick doesn't immediately
// look "unreachable" to the next tick).
//
// Idempotent: if the active is already the requested
// hostname, no field is changed and nil is returned.
func (c *HaChain) ApplyActiveRole(hostname string, nowSec int64) error {
	if c == nil {
		return errors.New("ha: nil chain")
	}
	current := c.ActiveMember()
	if (current == nil && hostname == "") || (current != nil && current.Hostname == hostname) {
		return nil
	}
	for i := range c.Members {
		if c.Members[i].Hostname == hostname {
			c.Members[i].Role = RoleActive
			c.Members[i].LastSeen = nowSec
		} else {
			c.Members[i].Role = RoleStandby
		}
	}
	return nil
}

// RoleFor returns the role that the elector should record
// for the given hostname given the current Patroni state on
// THIS node. selfIsPrimary refers to the elector's node, not
// to `host`. If `host` is self and self is primary → active;
// if `host` is in the chain but not active → standby.
// Returns RoleUnknown if `host` is not in the chain.
func (c *HaChain) RoleFor(host string, selfIsPrimary bool) HaRole {
	idx := c.FindByHostname(host)
	if idx < 0 {
		return RoleUnknown
	}
	if host == "" {
		return RoleUnknown
	}
	// If the chain already has this host as active, keep it.
	if c.Members[idx].Role == RoleActive {
		return RoleActive
	}
	// Otherwise, this host is standby (the elector will
	// promote self to active separately if Patroni says
	// we're primary).
	return RoleStandby
}
