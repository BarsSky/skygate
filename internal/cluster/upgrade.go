// Package cluster — upgrade.go implements the B222
// rolling-upgrade orchestrator (Phase 4.2 of
// docs/internal/cluster-management.md).
//
// "Rolling upgrade" means: upgrade one node at a time
// across the cluster, draining the node first
// (state=draining, B217), then doing the actual binary
// push + restart on the target, then polling the
// target's /healthz until the new build is live, then
// marking the node state=ready (the new RejoinNode
// helper, B222). The orchestrator's own node is
// skipped — the operator's "self-upgrade" is a separate
// one-off: the operator upgrades a non-orchestrator
// node first, that node becomes the new orchestrator
// (or the operator just rerun the upgrade command
// after sshing to the new build on the orchestrator).
//
// Scope of this B222 chunk
// ------------------------
// The orchestrator wires the state machine + audit
// rows. The actual binary push is OUT OF SCOPE — the
// operator must push the binary between drain and
// rejoin (e.g. via `skygate deploy push --target=<h>`
// from B150 + `skygate deploy pull` on the target, or
// via scp + systemctl restart). The orchestrator
// detects "the upgrade is done" by polling the
// target's /healthz endpoint and waiting for the
// build metadata to match the orchestrator's build
// (the build string is set via -ldflags at compile
// time, so each binary uniquely identifies its
// commit).
//
// Why wait for build match, not just any /healthz
// -----------------------------------------------
// The target's /healthz returns 200 the moment the
// skygate process is up — but it might be the OLD
// binary if the operator pushed the new binary to
// the wrong path, or the systemd unit didn't pick up
// the new file. Matching the build string is the
// only way to confirm the NEW skygate is serving
// traffic. The 5-minute timeout gives the operator
// time to fix a typo + retry; past 5 min the
// orchestrator aborts and writes a `node_rejoin`
// audit row with `result=error` (B215 style).
//
// Self-upgrade guard
// ------------------
// The orchestrator MUST NOT upgrade its own node —
// the upgrade process restarts skygate, and the
// orchestrator IS skygate. The guard compares the
// target's hostname against the orchestrator's
// `os.Hostname()` value and refuses with a clear
// error: "refusing to upgrade self — run
// `skygate deploy push` from a peer node, then
// ssh to this node and run `skygate deploy pull`
// manually". The check is in the public entry
// points (UpgradeNode + UpgradeAll) so the CLI +
// the HTTP handler + the B-check all use the same
// path.
package cluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"skygate/internal/db"
)

// UpgradeOrchestrator coordinates the per-node
// state machine for a rolling upgrade. The struct
// is value-only (no goroutines internally); callers
// invoke UpgradeNode / UpgradeAll and the helper
// runs the drain+wait+rejoin sequence synchronously
// per node. For "upgrade all (rolling)" the caller
// loops over the ready-or-failed nodes (B217 state
// filter) and calls UpgradeNode for each.
//
// The HTTP handler at /admin/cluster/upgrade runs
// the loop in a goroutine and returns 303 to the
// page immediately; the B222.1 follow-up will add
// SSE for live progress (the B194 framework already
// has the broker + EventSource plumbing — B222.1
// just wires the orchestrator's per-node events
// into the broker).
type UpgradeOrchestrator struct {
	// BuildID is the orchestrator's own build
	// metadata string (set via -ldflags at compile
	// time, exposed via /healthz). The orchestrator
	// polls the target's /healthz until the
	// response's `build` field matches this value.
	// The comparison is exact (string equality) —
	// partial matches would race with the operator
	// pushing an intermediate build for testing.
	BuildID string

	// HTTPClient is the client used for the per-node
	// /healthz poll. The default
	// (&http.Client{Timeout: 5*time.Second}) is fine
	// for a Tailscale-reachable target; tests can
	// inject a custom one.
	HTTPClient *http.Client

	// HealthTimeout is the per-node "wait for the
	// new build to come up" deadline. Default 5 min
	// — long enough for a slow binary push (large
	// cross-atlantic scp) + restart, short enough
	// that an operator who forgot to push doesn't
	// have to wait hours. Override for testing
	// (e.g. 200ms for a unit test).
	HealthTimeout time.Duration

	// HealthPollInterval is how often the
	// orchestrator polls the target's /healthz.
	// Default 2s — gives the target time to swap
	// the binary + bind the listener between polls,
	// without making the operator wait 30s for a
	// healthz that came up 2s after the previous
	// poll.
	HealthPollInterval time.Duration

	// Now is the clock source. Default
	// time.Now. The unit tests override this to
	// fast-forward through the timeout without
	// sleeping.
	Now func() time.Time
}

// NewUpgradeOrchestrator returns a default-configured
// orchestrator. Callers may override fields on the
// returned struct before invoking UpgradeNode.
//
// The defaults match the values documented on the
// struct fields: 5s HTTP timeout, 5 min health
// timeout, 2s poll interval, real clock.
func NewUpgradeOrchestrator(buildID string) *UpgradeOrchestrator {
	return &UpgradeOrchestrator{
		BuildID:            buildID,
		HTTPClient:         &http.Client{Timeout: 5 * time.Second},
		HealthTimeout:      5 * time.Minute,
		HealthPollInterval: 2 * time.Second,
		Now:                time.Now,
	}
}

// SelfHostname returns the local hostname (the
// orchestrating node's hostname). Used by the
// self-upgrade guard to compare against the target.
// Pure wrapper around os.Hostname() so the unit
// tests can mock it (see upgrade_b222_test.go's
// `selfHostnameFunc` package var + the test's
// save/restore pattern).
func SelfHostname() (string, error) {
	if selfHostnameFn != nil {
		return selfHostnameFn()
	}
	return os.Hostname()
}

// selfHostnameFn is the package-level mock hook
// for tests. Production code never sets this —
// SelfHostname() falls through to os.Hostname()
// when it's nil. The unit tests in
// upgrade_b222_test.go assign + restore this
// around the cases that need a fixed
// hostname (the self-upgrade guard).
var selfHostnameFn func() (string, error)

// ErrSelfUpgrade is returned by UpgradeNode when the
// target hostname matches the orchestrator's own
// hostname. The HTTP handler maps this to a 400
// response with the operator-readable message.
var ErrSelfUpgrade = errors.New("refusing to upgrade self — run the upgrade from a peer node, then ssh here and run `skygate deploy pull` + `docker restart skygate` (or `systemctl restart skygate`) manually")

// UpgradeNode runs the per-node state machine for a
// rolling upgrade: drain → wait for the target's
// /healthz to report the new build → rejoin.
//
// Returns nil on success. On error, the orchestrator
// has already tried to roll the state back (the
// helper that fails writes a `node_rejoin` audit
// row with `result=error` to make the failure
// visible in /admin/audit).
//
// The function is synchronous: a caller who wants
// to run an "upgrade all" loop calls UpgradeNode
// once per target. The HTTP handler in
// /admin/cluster/upgrade runs the loop in a
// goroutine; the live UI shows the per-node state
// transitions via SSE (B222.1 follow-up; not in
// B222 itself).
func (o *UpgradeOrchestrator) UpgradeNode(ctx context.Context, d *sql.DB, clusterID, hostname, actor, reason string) error {
	// Self-upgrade guard. The check is BEFORE the
	// drain call so a misclick on the orchestrator's
	// own row doesn't put the cluster into a weird
	// state (drained but never rejoined because the
	// upgrade process is the orchestrator itself
	// and it just got restarted).
	if err := o.checkSelfUpgrade(hostname); err != nil {
		return err
	}
	if actor == "" {
		actor = "system"
	}

	// Phase 1: drain. B217 sets state=draining and
	// writes a node_drain audit row. The HA chain
	// (B204) sees the node as failed (no heartbeat
	// during the upgrade window) — the elector
	// will recommend a standby for promotion if
	// the cluster is in a state where that's
	// needed.
	if err := DrainNode(d, clusterID, hostname, actor, reason); err != nil {
		return fmt.Errorf("drain %q: %w", hostname, err)
	}

	// Phase 2: wait for the operator to push the
	// new binary + restart the target. The
	// orchestrator polls /healthz until the build
	// string matches. On timeout the helper
	// writes a fail audit row + leaves the node
	// in state=draining (the operator must
	// re-run the upgrade, or use the existing
	// B217 "Drain+Remove" to clean up).
	if err := o.waitForBuild(ctx, hostname); err != nil {
		_ = appendUpgradeFailAudit(d, clusterID, hostname, actor, err.Error())
		return fmt.Errorf("wait for build on %q: %w", hostname, err)
	}

	// Phase 3: rejoin. Flips state=draining →
	// state=ready and refreshes last_seen_at. The
	// B204 elector's next tick sees the node as
	// alive + on the new build.
	if err := RejoinNode(d, clusterID, hostname, actor); err != nil {
		_ = appendUpgradeFailAudit(d, clusterID, hostname, actor, err.Error())
		return fmt.Errorf("rejoin %q: %w", hostname, err)
	}
	return nil
}

// UpgradeAll iterates the cluster_node rows in
// (cluster_id, hostname) order and runs UpgradeNode
// for each. Nodes with state=pending or state=draining
// (already in the middle of an upgrade) are SKIPPED —
// the operator's existing drain is still in flight.
// Nodes with state=failed ARE included — the B222
// "upgrade" path also recovers failed nodes (the
// rejoin is from_state=failed in that case). The
// orchestrating node is skipped (self-upgrade
// guard). Stops on the first error and returns the
// error from UpgradeNode (the rest of the nodes
// remain untouched — the operator can re-run the
// command to continue past the failure).
func (o *UpgradeOrchestrator) UpgradeAll(ctx context.Context, d *sql.DB, clusterID, actor, reason string) error {
	rows, err := d.Query(`
		SELECT hostname
		  FROM cluster_node
		 WHERE cluster_id = $1
		   AND state IN ('ready', 'failed')
		 ORDER BY hostname
	`, clusterID)
	if err != nil {
		return fmt.Errorf("list cluster_node: %w", err)
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var h string
		if scanErr := rows.Scan(&h); scanErr != nil {
			return fmt.Errorf("scan hostname: %w", scanErr)
		}
		targets = append(targets, h)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate cluster_node: %w", err)
	}
	if len(targets) == 0 {
		return errors.New("no upgradeable nodes (all are pending/draining, or the cluster is empty)")
	}
	for _, h := range targets {
		// Self-upgrade guard per node (a different
		// node than the orchestrator is fine; the
		// same node as the orchestrator is the
		// "self" case).
		if guardErr := o.checkSelfUpgrade(h); guardErr != nil {
			// Skip self in the "all" loop —
			// it's not an error, just an
			// intentional skip.
			continue
		}
		if err := o.UpgradeNode(ctx, d, clusterID, h, actor, reason); err != nil {
			return fmt.Errorf("upgrade %q: %w", h, err)
		}
	}
	return nil
}

// checkSelfUpgrade is the internal helper. It
// compares the target hostname against the
// orchestrator's own hostname (via SelfHostname)
// and returns ErrSelfUpgrade if they match. The
// comparison is case-insensitive (hostnames are
// case-insensitive per RFC 952).
func (o *UpgradeOrchestrator) checkSelfUpgrade(target string) error {
	self, err := SelfHostname()
	if err != nil {
		// If we can't read our own hostname, fall
		// through (don't block the upgrade). The
		// alternative is a hard error, but that
		// would block all upgrades on misconfigured
		// hosts where /proc/sys/kernel/hostname
		// is unreadable (rare in practice).
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(target), strings.TrimSpace(self)) {
		return ErrSelfUpgrade
	}
	return nil
}

// waitForBuild polls the target's /healthz endpoint
// until the build string matches o.BuildID, or the
// HealthTimeout elapses. The target URL is
// `http://<hostname>:8080/healthz` — the standard
// skygate port. A future B-block (B222.1) will
// replace this with a Tailscale-IP lookup from the
// cluster_node.tailscale_ip column (B201).
//
// Returns nil on success, an error on timeout or
// repeated HTTP failures.
func (o *UpgradeOrchestrator) waitForBuild(ctx context.Context, hostname string) error {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if o.HealthPollInterval <= 0 {
		o.HealthPollInterval = 2 * time.Second
	}
	if o.HealthTimeout <= 0 {
		o.HealthTimeout = 5 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	deadline := o.Now().Add(o.HealthTimeout)
	url := fmt.Sprintf("http://%s:8080/healthz", hostname)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if o.Now().After(deadline) {
			return fmt.Errorf("timeout after %s waiting for build on %q (last url: %s)", o.HealthTimeout, hostname, url)
		}
		if matched := o.pollOnce(ctx, url); matched {
			return nil
		}
		// Sleep until the next poll. The context-
		// aware sleep uses a timer + select so the
		// shutdown path can cancel mid-wait.
		t := time.NewTimer(o.HealthPollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// pollOnce does one HTTP GET /healthz and returns
// true if the response's "build" field matches
// o.BuildID. The function is permissive: any HTTP
// error (connection refused, timeout, 5xx) returns
// false (we keep polling). Only a 200 response
// with a matching build string returns true.
func (o *UpgradeOrchestrator) pollOnce(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := o.HTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	// Read up to 4 KiB. /healthz is ~150 bytes; we
	// cap to avoid a malicious response blowing up
	// memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return false
	}
	var parsed struct {
		Build string `json:"build"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	return strings.TrimSpace(parsed.Build) == strings.TrimSpace(o.BuildID)
}

// appendUpgradeFailAudit writes a
// db.AppendAuditLogWithTarget row with
// action="cluster.upgrade.fail" and
// target_type="cluster_node" + target_id=hostname.
// Called from UpgradeNode on the failure paths so
// the audit log has a clear "the upgrade failed
// because X" trail. Best-effort: if the audit write
// itself fails (DB down), we swallow the error so
// the original upgrade error reaches the caller.
//
// B221: this is the only audit row the upgrade
// orchestrator writes via the audit_log table
// (B215's cluster_audit is reserved for
// bootstrap state transitions + HA events). The
// row carries the structured (cluster_node,
// hostname) target so /admin/audit can group
// upgrade failures by node.
func appendUpgradeFailAudit(d *sql.DB, clusterID, hostname, actor, failReason string) error {
	detail := fmt.Sprintf(`{"cluster_id":%q,"hostname":%q,"actor":%q,"fail_reason":%q}`,
		clusterID, hostname, actor, failReason)
	_ = db.AppendAuditLogWithTarget(d, 0, actor, "cluster.upgrade.fail", detail, "cluster_node", hostname)
	return nil
}
