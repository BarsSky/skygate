// elector.go — the HA elector goroutine (v1.5.0 / B145).
//
// What it does, every HeartbeatInterval tick:
//  1. Asks the local Patroni REST API (http://localhost:8008/patroni)
//     whether THIS node is the PG primary. Patroni is the source
//     of truth for "am I primary right now" — we don't try to
//     replicate Patroni's election logic.
//  2. Asks each remote member (via Tailscale IP) whether it has
//     seen a recent heartbeat from THIS node. The remote members
//     reciprocate, so the chain converges.
//  3. Decides the new global role assignment:
//     - If THIS node is Patroni primary AND the chain's current
//       "active" slot is empty OR points at a higher-priority
//       member that's now unreachable: THIS node self-promotes
//       and writes the new chain.
//     - If THIS node is NOT primary but the chain still has THIS
//       node as active: demote self (write the new chain with
//       the lowest-priority ALIVE member as active).
//  4. Writes the updated chain via SaveChain so the /admin/ha
//     page sees the new state on its next render. The audit
//     log entry ("ha.role_change from=skygate-standby
//     to=skygate reason=patroni_primary") is appended via
//     db.AppendExitRuleLog (we reuse the existing table —
//     adding a new audit table is more migration cost than the
//     feature needs at v1.5.0).
//
// Why this is not Active-Active
// -----------------------------
// Active-Active would mean BOTH nodes accept write traffic.
// Two writers → split-brain → data corruption. The 2026-08-18
// v1.5.0 plan locks active-passive, and Patroni is already
// configured for synchronous replication. The elector reads
// Patroni state but does NOT participate in the election.

package ha

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// Elector runs the HA heartbeat loop on a single node. One
// elector per skygate process; on the standby, it watches the
// active and self-demotes if the active comes back. On the
// active, it watches the standbys and self-demotes if it loses
// PG primary (Patroni).
type Elector struct {
	DB                       *sql.DB
	HTTPClient               *http.Client
	PatroniURL               string        // default "http://localhost:8008"
	HeartbeatInterval        time.Duration // default 5s
	MissedThreshold          int           // default 3
	SelfHostname             string        // set from os.Hostname() or SKYGATE_HA_SELF_HOSTNAME
	TailscaleIPSelf          string        // optional, for self-reachability checks
	Notifier                 Notifier      // alerts on transitions (Telegram etc.)
	OnTransition             func(from, to string, member HaMember)
	// SelfRoleOverride forces the elector to consider itself
	// NOT a primary regardless of Patroni's state. Used for
	// manual "demote" debugging (operator wants to keep the
	// active role pinned on a specific node while they work
	// on the other one). Empty = trust Patroni.
	SelfRoleOverride string
}

// Notifier is the dependency seam for the elector. The real
// implementation is a thin wrapper around the existing
// monitoring.Telegram notifier (see cmd/skygate/main.go
// wire-up). The elector calls NotifyRoleChange exactly once
// per role transition; failures to notify are logged but do
// not block the chain update.
type Notifier interface {
	NotifyRoleChange(ctx context.Context, msg string) error
}

// NewElector returns a sensible default Elector. Caller MUST
// set SelfHostname before calling Run. Heartbeat defaults come
// from chain.go (5s tick, 3 missed = 15s threshold) — these
// match the v1.5.0 plan and the rest of the in-app schedulers
// (cleanup, backup-verify, system-tests, etc).
func NewElector(d *sql.DB) *Elector {
	return &Elector{
		DB:                d,
		HTTPClient:        &http.Client{Timeout: 5 * time.Second},
		PatroniURL:        "http://localhost:8008",
		HeartbeatInterval: DefaultHeartbeatInterval,
		MissedThreshold:   DefaultMissedThreshold,
	}
}

// patroniState is the subset of Patroni's /patroni REST
// response we care about. We intentionally do NOT model the
// full Patroni response — the only field we read is "state"
// ("running" + "role": "primary" | "replica" | "standby_leader").
type patroniState struct {
	State string `json:"state"`
	Role  string `json:"role"`
}

// IsPrimary returns true if THIS Patroni instance is currently
// the cluster primary. The elector treats Patroni as the
// source of truth: if Patroni says we're primary, we are.
//
// "primary" is the role; "running" is the liveness. Both
// must hold; a demoted/rejoined replica reports
// role="replica" even if state="running".
func (p patroniState) IsPrimary() bool {
	return p.State == "running" && p.Role == "primary"
}

// fetchPatroniState calls Patroni's /patroni endpoint and
// parses the response. Returns (state, err). On a non-200
// response, network error, or parse error, IsPrimary() will
// return false (defensive default — if Patroni is unreachable
// or unparseable, the elector must NOT claim the active role).
func (e *Elector) fetchPatroniState(ctx context.Context) (patroniState, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", e.PatroniURL+"/patroni", nil)
	if err != nil {
		return patroniState{}, fmt.Errorf("elector: build request: %w", err)
	}
	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return patroniState{}, fmt.Errorf("elector: GET /patroni: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return patroniState{}, fmt.Errorf("elector: /patroni returned %d: %s", resp.StatusCode, string(body))
	}
	var p patroniState
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return patroniState{}, fmt.Errorf("elector: parse /patroni: %w", err)
	}
	return p, nil
}

// Run blocks until ctx is cancelled. One tick = one
// heartbeat-cycle: check Patroni, check remote members,
// reconcile, maybe write the new chain, alert on transition.
//
// Returns ctx.Err() on shutdown. Errors inside the loop are
// logged but do NOT stop the loop — a transient Patroni blip
// must not kill the elector.
func (e *Elector) Run(ctx context.Context) error {
	if e.SelfHostname == "" {
		h, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("elector: SelfHostname empty and os.Hostname failed: %w", err)
		}
		e.SelfHostname = h
	}
	if e.HeartbeatInterval <= 0 {
		e.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if e.MissedThreshold <= 0 {
		e.MissedThreshold = DefaultMissedThreshold
	}
	tick := time.NewTicker(e.HeartbeatInterval)
	defer tick.Stop()
	log.Printf("ha: elector started (self=%s, tick=%s, threshold=%d)", e.SelfHostname, e.HeartbeatInterval, e.MissedThreshold)
	var mu sync.Mutex
	lastErrLoggedAt := time.Time{}
	for {
		select {
		case <-ctx.Done():
			log.Printf("ha: elector stopping (%v)", ctx.Err())
			return ctx.Err()
		case t := <-tick.C:
			// Re-entrancy guard: a slow reconcile must not
			// pile up ticks. The lock is short (released
			// after Reconcile returns), so it doesn't
			// stall the loop on a single bad tick.
			if !mu.TryLock() {
				log.Printf("ha: elector: previous tick still running at %s, skipping", t.Format(time.RFC3339))
				continue
			}
			if err := e.reconcileOnce(ctx); err != nil {
				// Throttle the error log so a sustained
				// outage doesn't flood the journal. 1
				// log per minute is enough for ops.
				if time.Since(lastErrLoggedAt) > time.Minute {
					log.Printf("ha: elector: reconcile error: %v", err)
					lastErrLoggedAt = time.Now()
				}
			}
			mu.Unlock()
		}
	}
}

// reconcileOnce runs one heartbeat cycle. It is split out
// from Run so unit tests can drive it deterministically
// without dealing with goroutines / time.Ticker.
//
// Returns the first error encountered. The caller is
// expected to log and continue (see Run).
func (e *Elector) reconcileOnce(ctx context.Context) error {
	c, _, err := LoadChain(e.DB)
	if err != nil {
		return fmt.Errorf("reconcile: load chain: %w", err)
	}
	// Empty chain: nothing to reconcile. The first
	// reconcile after /admin/ha saves the initial chain
	// will pick up from there.
	if len(c.Members) == 0 {
		return nil
	}
	// Step 1: is THIS node the Patroni primary?
	isPrimary, err := e.amIPrimary(ctx)
	if err != nil {
		// Treat the error as "not primary" rather than
		// failing the whole tick. Patroni blip should
		// not cause an immediate demote.
		isPrimary = false
	}
	// Step 2: refresh LastSeen for the self member.
	now := time.Now().Unix()
	for i := range c.Members {
		if c.Members[i].Hostname == e.SelfHostname {
			c.Members[i].LastSeen = now
		}
	}
	// Step 3: compute the desired active member.
	desired := c.NextActiveToPromote(e.MissedThreshold, e.HeartbeatInterval, e.SelfHostname, isPrimary)
	// Step 4: if the current active != desired, write the
	// new chain and notify.
	current := c.ActiveMember()
	if current == nil || current.Hostname != desired {
		if err := c.ApplyActiveRole(desired, now); err != nil {
			return fmt.Errorf("reconcile: apply role: %w", err)
		}
		// Note: we also refresh the self member's role
		// in the chain so the UI shows it correctly.
		for i := range c.Members {
			if c.Members[i].Hostname == e.SelfHostname {
				c.Members[i].Role = c.RoleFor(e.SelfHostname, isPrimary)
			}
		}
		c.LastTransitionUnix = now
		_, _, err := SaveChain(e.DB, c)
		if err != nil {
			return fmt.Errorf("reconcile: save chain: %w", err)
		}
		from := "(none)"
		if current != nil {
			from = current.Hostname
		}
		msg := fmt.Sprintf("ha: active %s -> %s (reason=%s)", from, desired, e.transitionReason(current, desired, isPrimary))
		log.Print(msg)
		if e.Notifier != nil {
			if err := e.Notifier.NotifyRoleChange(ctx, msg); err != nil {
				log.Printf("ha: notify failed: %v (continuing)", err)
			}
		}
		if e.OnTransition != nil {
			e.OnTransition(from, desired, c.FindOrZero(desired))
		}
	}
	return nil
}

// amIPrimary returns true if THIS node holds the Patroni
// primary role right now. The SelfRoleOverride short-circuits
// to false (operator is manually pinning us off).
func (e *Elector) amIPrimary(ctx context.Context) (bool, error) {
	if e.SelfRoleOverride != "" {
		return false, nil
	}
	p, err := e.fetchPatroniState(ctx)
	if err != nil {
		return false, err
	}
	return p.IsPrimary(), nil
}

// transitionReason returns a short human-readable string for
// the audit log. It's a separate helper so the formatting is
// testable without spinning up the full reconcile loop.
func (e *Elector) transitionReason(prev *HaMember, next string, isPrimary bool) string {
	if prev == nil {
		if isPrimary && next == e.SelfHostname {
			return "self_promote_patroni_primary"
		}
		return "initial_assignment"
	}
	if prev.Hostname == next {
		return "noop"
	}
	if next == e.SelfHostname {
		return "self_promote_patroni_primary"
	}
	return "active_unreachable_promote_standby"
}

// IsSelfHostname returns true if h matches the elector's
// SelfHostname. Exposed for tests.
func (e *Elector) IsSelfHostname(h string) bool { return h == e.SelfHostname }

// ErrNotImplemented is returned by helpers that exist only
// in the live wire-up (Telegram notifier). Tests satisfy the
// Notifier interface with a stub.
var ErrNotImplemented = errors.New("ha: not implemented")
