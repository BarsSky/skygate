// Package cluster — discovery.go implements the B223
// (Phase 4.3) Tailscale auto-discovery flow.
//
// Background
// ----------
// Pre-B223, onboarding a new skygate node was a
// 4-step manual process:
//   1. Operator runs `skygate cluster invite` on
//      the orchestrator to generate an sgn1
//      token.
//   2. Operator copies the token to the new node
//      (scp, screen-share, etc).
//   3. New node runs `skygate cluster join <token>`.
//   4. Operator goes to /admin/cluster and clicks
//      "Approve" on the new pending row (B217).
//
// B223 collapses steps 1-3 into a single
// orchestrator-side poll: every 5 minutes (or on
// a manual click of "Run Tailscale discovery" on
// /admin/cluster), the orchestrator runs
// `tailscale status --json` locally, parses the
// Peer list, and INSERTs a cluster_node row
// (state=pending) for every Tailscale peer that
// (a) is NOT already in cluster_node AND
// (b) optionally has a specific Tailscale tag
// (configurable via SKYGATE_DISCOVERY_TAG, default
// "" = no filter). The new node does NOT have to
// do anything — the orchestrator just notices it
// on the Tailscale network. Step 4 (the B217
// Approve click) is unchanged; the admin still
// gates state=ready on a manual decision.
//
// Why the tag filter matters
// --------------------------
// Tailscale peers include laptops, phones,
// printers, etc. — not just skygate candidates.
// Without a tag filter, every new device on the
// tailnet would spawn a cluster_node row, which
// the admin would have to manually dismiss.
// The SKYGATE_DISCOVERY_TAG env var lets the
// operator scope discovery to a specific subset
// (e.g. "tag:skygate-candidate" — every node
// that joined the tailnet with that Tailscale
// ACL tag).
//
// Why we use `tailscale status --json` (not the
// Tailscale HTTP API)
// --------------------------------------------
// The HTTP API requires an API key
// (TS_API_KEY env var or OAuth secret) — the
// operator has to provision it. `tailscale status
// --json` is available out-of-the-box (any node
// with tailscaled running can see its peers via
// the local control socket). The trade-off: we
// only see peers that the local tailscaled can
// see. If the orchestrator's tailscaled is
// stale, discovery is stale. A future B-block
// could add the API-key path for the
// authoritative view, but the local status is
// good enough for v1.
//
// Why state=pending (not a new "discovered" state)
// -------------------------------------------------
// The B217 Approve flow handles state=pending. A
// new "discovered" state would require modifying
// the state machine + the B204 elector + the
// /admin/cluster UI + the cluster_audit filter.
// Reusing pending keeps the change small and
// the B221 audit row (`cluster.discovery.new_node`)
// is the operator's signal that the row was
// auto-created (not manually added).

package cluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"skygate/internal/db"
)

// tailscaleStatusFn is the package-level mock
// hook for the `tailscale status --json`
// shell-out. Production code never sets it —
// TailscaleStatus() runs the real binary when
// it's nil. The unit tests in
// discovery_b223_test.go assign + restore this
// around the cases that need a fixed status
// output (the discovery happy path + the
// "tailscaled not running" error path).
var tailscaleStatusFn func(ctx context.Context) ([]byte, error)

// TailscalePeer is the subset of
// `tailscale status --json` we care about. The
// real JSON has many more fields (Created,
// LastSeen, OS, etc.) — we only model what
// cluster_node cares about.
type TailscalePeer struct {
	// Hostname is the Tailscale "node name"
	// (e.g. "skygate-standby.tail.ts.net",
	// trimmed to the short form "skygate-standby"
	// by TailscaleHostnameShort).
	Hostname string
	// TailscaleIP is the first IPv4 from
	// TailscaleIPs. v6-only peers are skipped
	// (cluster_node.tailscale_ip is INET4).
	TailscaleIP string
	// Online is true if the peer's BackendState
	// is "Running" at the time of the status
	// query. Offline peers are filtered out by
	// DiscoverNewNodes (we don't want to spam
	// cluster_node with rows for a phone that's
	// off).
	Online bool
	// Tags is the list of Tailscale ACL tags
	// applied to the peer (e.g. ["tag:skygate-
	// candidate"]). Empty for untagged peers.
	Tags []string
}

// TailscaleStatus is the parsed `tailscale status
// --json` output. We only model the fields the
// discovery path reads; the full schema has
// dozens of fields we don't care about.
type TailscaleStatus struct {
	// Self is the local node. Excluded from
	// discovery (we never want to "discover"
	// ourselves).
	Self TailscalePeer
	// Peer is the list of all other nodes in
	// the tailnet, keyed by the Tailscale DNS
	// name (e.g. "skygate-standby.tail.ts.net").
	Peer map[string]TailscalePeer
}

// TailscalePeerRaw is the per-peer JSON shape
// (Tailscale uses the DNS name as the map key).
// We model the fields we need.
type TailscalePeerRaw struct {
	HostName      string   `json:"HostName"`
	TailscaleIPs  []string `json:"TailscaleIPs"`
	Online        bool     `json:"Online"`
	Tags          []string `json:"Tags"`
}

// TailscaleStatus runs `tailscale status --json`
// and parses the output. Returns the parsed
// status, or an error if the binary is missing,
// tailscaled is not running, or the JSON parse
// fails. Production callers should treat any
// error as "0 peers discovered this tick" (the
// background poller logs + writes a
// cluster.discovery.error audit row + keeps
// going).
func GetTailscaleStatus(ctx context.Context) (*TailscaleStatus, error) {
	raw, err := TailscaleStatusRaw(ctx)
	if err != nil {
		return nil, err
	}
	return parseTailscaleStatus(raw)
}

// TailscaleStatusRaw is the lower-level "just
// shell out + return bytes" helper. Split out so
// the unit tests can mock the bytes without
// monkey-patching the parser.
func TailscaleStatusRaw(ctx context.Context) ([]byte, error) {
	if tailscaleStatusFn != nil {
		return tailscaleStatusFn(ctx)
	}
	// 5s timeout — the binary usually returns
	// in < 200ms; the timeout covers a slow
	// control-socket lookup.
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "tailscale", "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		// stderr would have the actual reason
		// (e.g. "tailscaled not running");
		// cmd.Output hides it. We surface a
		// useful sentinel that the caller can
		// match on.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("tailscale status --json: exit %d: %s", exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("tailscale status --json: %w", err)
	}
	return out, nil
}

// parseTailscaleStatus is a pure parser — no
// IO, no shell. Split out for the unit tests
// (they feed in canned bytes).
func parseTailscaleStatus(raw []byte) (*TailscaleStatus, error) {
	var s struct {
		Self  TailscalePeerRaw            `json:"Self"`
		Peer  map[string]TailscalePeerRaw `json:"Peer"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parse tailscale status: %w", err)
	}
	out := &TailscaleStatus{
		Self: peerFromRaw(s.Self),
		Peer: make(map[string]TailscalePeer, len(s.Peer)),
	}
	for k, p := range s.Peer {
		out.Peer[k] = peerFromRaw(p)
	}
	return out, nil
}

// peerFromRaw normalizes a TailscalePeerRaw into
// the discovery-friendly TailscalePeer shape
// (short hostname, first IPv4 only, etc).
func peerFromRaw(p TailscalePeerRaw) TailscalePeer {
	return TailscalePeer{
		Hostname:    TailscaleHostnameShort(p.HostName),
		TailscaleIP: firstIPv4(p.TailscaleIPs),
		Online:      p.Online,
		Tags:        p.Tags,
	}
}

// TailscaleHostnameShort trims the tailnet
// suffix from a Tailscale hostname. e.g.
// "skygate-standby.tail.ts.net" →
// "skygate-standby". Used as the cluster_node
// hostname (the B201 join flow's
// cluster_node.hostname is the short form).
//
// Tailscale's default MagicDNS suffix is
// "<tailnet>.ts.net" (2 segments). So a typical
// hostname is "<short>.<tailnet>.ts.net"
// (3+ segments). The function trims when the
// last 2 segments are exactly "ts" and "net".
// Falls back to returning the full name when
// the suffix doesn't match (the operator might
// have a custom MagicDNS suffix, or a single-
// label name from a non-MagicDNS tailnet).
func TailscaleHostnameShort(full string) string {
	parts := strings.Split(full, ".")
	if len(parts) < 3 {
		return full
	}
	// Check the last 2 segments for the
	// default Tailscale MagicDNS suffix
	// ("<tailnet>.ts.net").
	if strings.EqualFold(parts[len(parts)-1], "net") &&
		strings.EqualFold(parts[len(parts)-2], "ts") {
		return parts[0]
	}
	return full
}

// firstIPv4 returns the first IPv4 address in
// the list, or empty if the peer is v6-only.
// cluster_node.tailscale_ip is INET (PG treats
// INET as v4-only by default; v6 needs
// INET6 + a cidr conversion). v1 only handles
// v4.
func firstIPv4(ips []string) string {
	for _, ip := range ips {
		if strings.Contains(ip, ":") {
			continue // v6
		}
		return ip
	}
	return ""
}

// matchesTagFilter returns true if the peer has
// the requested tag. An empty tagFilter means
// "no filter" — every peer matches.
func matchesTagFilter(peer TailscalePeer, tagFilter string) bool {
	if tagFilter == "" {
		return true
	}
	for _, t := range peer.Tags {
		if strings.EqualFold(strings.TrimSpace(t), strings.TrimSpace(tagFilter)) {
			return true
		}
	}
	return false
}

// DiscoverNewNodes returns the list of Tailscale
// peers that are NOT already in cluster_node.
// The caller is expected to INSERT one row per
// returned peer via EnsureDiscoveredNode.
//
// Filters applied:
//   - excludes the local node (Self)
//   - excludes offline peers (we don't want
//     a cluster_node row for a phone that's off)
//   - excludes peers that fail the tag filter
//   - excludes peers that already exist in
//     cluster_node (idempotent re-runs are
//     no-ops)
//
// On any error from TailscaleStatus (binary
// missing, tailscaled down, JSON parse fail),
// returns the error so the caller can log it
// and write a cluster.discovery.error audit
// row. The caller decides whether to retry
// (background ticker does) or surface the
// error to the admin (HTTP handler does).
func DiscoverNewNodes(ctx context.Context, d *sql.DB, clusterID, tagFilter string) ([]TailscalePeer, error) {
	status, err := GetTailscaleStatus(ctx)
	if err != nil {
		return nil, err
	}
	// List existing hostnames in cluster_node
	// (state doesn't matter — we skip duplicates
	// even if the existing row is failed or
	// draining). Empty cluster (no rows) is fine.
	existing, err := listClusterHostnames(d, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list cluster_node hostnames: %w", err)
	}
	var out []TailscalePeer
	for _, p := range status.Peer {
		// Skip self.
		if p.Hostname == status.Self.Hostname {
			continue
		}
		// Skip offline (B223 only discovers peers
		// we can actually reach).
		if !p.Online {
			continue
		}
		// Skip tag-filtered-out peers.
		if !matchesTagFilter(p, tagFilter) {
			continue
		}
		// Skip duplicates.
		if _, found := existing[p.Hostname]; found {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// listClusterHostnames returns the set of
// hostnames already in cluster_node for the
// given cluster. Empty / nil on no rows. The
// helper is exported as a package-level function
// rather than a method so the unit tests can
// exercise it with a real DB.
func listClusterHostnames(d *sql.DB, clusterID string) (map[string]struct{}, error) {
	rows, err := d.Query(`
		SELECT hostname FROM cluster_node WHERE cluster_id = $1
	`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var h string
		if scanErr := rows.Scan(&h); scanErr != nil {
			return nil, scanErr
		}
		if h != "" {
			out[h] = struct{}{}
		}
	}
	return out, rows.Err()
}

// EnsureDiscoveredNode inserts a cluster_node
// row in state=pending for a peer the discovery
// poller found. The skygate_version column is
// "(discovered via Tailscale)" — the operator
// will see this in the B216 /admin/cluster
// page's version column. The audit row is
// cluster_audit (the B195 lifecycle table) with
// action=NodeDiscovered — the B215 /admin/ha
// filter surfaces this so the operator can see
// "this node was auto-created by the B223
// poller" in the per-node event history.
//
// Idempotent: if a row with the same hostname
// already exists, returns nil without
// modifying. The caller (DiscoverNewNodes) is
// expected to have already de-duplicated, but
// the ON CONFLICT clause here is a belt-and-
// suspenders against a race between two
// discovery ticks.
//
// The function is package-private (lowercase e)
// because only the B223 background poller +
// the HTTP handler should call it. External
// callers should go through DiscoverNewNodes +
// EnsureDiscoveredNode pairs so the
// cluster.discovery.run audit row gets
// written at the run level (not per-peer).
func EnsureDiscoveredNode(d *sql.DB, clusterID, hostname, tailscaleIP, actor string) error {
	if clusterID == "" || hostname == "" {
		return errors.New("cluster: empty cluster_id or hostname")
	}
	if actor == "" {
		actor = "system"
	}
	now := time.Now().UTC()
	// Synthetic node id — the B201 join flow
	// uses "node-<invite-prefix>" (12 chars).
	// The discovery flow uses "node-disc-<hostname>"
	// (truncated to 32 chars total) so the row
	// is recognizably auto-generated. The "disc"
	// prefix is the operator's signal that this
	// row was NOT created via the invite flow.
	discID := "node-disc-" + hostname
	if len(discID) > 32 {
		discID = discID[:32]
	}
	_, err := d.Exec(`
		INSERT INTO cluster_node (
			id, cluster_id, hostname, tailscale_ip, roles, state,
			skygate_version, joined_at
		) VALUES ($1, $2, $3, $4, ARRAY['skygate-standby']::text[], 'pending', $5, $6)
		ON CONFLICT (id) DO NOTHING
	`, discID, clusterID, hostname, tailscaleIP,
		"(discovered via Tailscale)", now)
	if err != nil {
		return fmt.Errorf("insert discovered node: %w", err)
	}
	// Audit row (cluster_audit / B215).
	// Action = "node_discovered". target_node_id
	// = the synthetic discID. Detail is JSONB
	// with the join-relevant fields.
	detail := fmt.Sprintf(`{"node_id":%q,"hostname":%q,"tailscale_ip":%q,"discovered_at":%q}`,
		discID, hostname, tailscaleIP, now.Format(time.RFC3339))
	_, _ = db.InsertClusterAudit(d, clusterID, db.NodeDiscovered, discID, actor, detail)
	return nil
}
