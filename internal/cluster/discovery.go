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
	// Addresses is the RAW TailscaleIPs list as the peer reported it.
	// B359: TailscaleIP is the first IPv4 (empty for a v6-only peer), so
	// the raw list is what lets the predicate tell "this candidate is
	// reachable only over IPv6" from "this candidate reported no address"
	// instead of silently dropping it.
	Addresses []string
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
	HostName     string   `json:"HostName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	Tags         []string `json:"Tags"`
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
		Self TailscalePeerRaw            `json:"Self"`
		Peer map[string]TailscalePeerRaw `json:"Peer"`
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
		Addresses:   p.TailscaleIPs,
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

// DiscoverNewNodes returns the Tailscale peers
// that are candidates for ADOPTION as skygate
// hosts, together with a named reason for every
// peer that was not adopted.
//
// B359 (2026-10-07). Pre-B359 the only filter
// was "tag filter + not already in cluster_node",
// so EVERY online peer became a pending
// `skygate-standby` row — including the
// exit-node relays. Measured live on the
// reference deployment: `emilia`, `karolina`
// and `sharlotta` are exit-node RELAYS (they
// carry tag:exit-node and rows in
// `exit_servers`), they were inserted as
// standby candidates by the ticker, had no
// skygate to join, and settled in state=failed
// forever. /admin/cluster showed three
// permanently-failed "standby" nodes that could
// never succeed, because the discovery
// predicate had no positive notion of "this is
// a skygate host".
//
// THE PREDICATE (the whole of it is
// ClassifyPeerForAdoption, a pure function):
//
//	A peer is adopted only when it is NOT a
//	relay AND it is positive evidence of a
//	skygate host. A relay is: the peer carries
//	`tag:exit-node`, or its hostname/IP is in
//	`exit_servers`. Positive evidence of a
//	skygate host is the per-node infra tag the
//	panel's own onboarding mints —
//	`tag:dev-infra-<lowercase hostname>` —
//	which is exactly the `headscale nodes tag`
//	step of the manual bootstrap runbook that
//	/admin/cluster/onboard replaced.
//
// Every peer that fails is returned in
// `Report.Rejected` with a DiscoverySkipReason,
// never silently dropped: "we cannot decide"
// must not look like "there is nothing there"
// (L-54). The callers surface them (the HTTP
// handler in the flash, the ticker in a
// rate-limited log line + audit row).
//
// Relay index: `relayIndex` may be nil (the
// unit tests pass one explicitly); when it is
// nil the function loads `exit_servers` from
// `d`. On any error from TailscaleStatus
// (binary missing, tailscaled down, JSON parse
// fail) or from the `exit_servers` read, the
// error is returned so the caller can log it
// and write a cluster.discovery.error audit row.
func DiscoverNewNodes(ctx context.Context, d *sql.DB, clusterID, tagFilter string, relayIndex *RelayIndex) (Report, error) {
	var report Report
	status, err := GetTailscaleStatus(ctx)
	if err != nil {
		return report, err
	}
	// List existing hostnames in cluster_node
	// (state doesn't matter — we skip duplicates
	// even if the existing row is failed or
	// draining). Empty cluster (no rows) is fine.
	existing, err := listClusterHostnames(d, clusterID)
	if err != nil {
		return report, fmt.Errorf("list cluster_node hostnames: %w", err)
	}
	idx := relayIndex
	if idx == nil {
		loaded, lerr := RelayIndexFromDB(d)
		if lerr != nil {
			return report, fmt.Errorf("load exit_servers (the relay index): %w", lerr)
		}
		idx = loaded
	}
	for _, p := range status.Peer {
		adopt, rejected, reason := ClassifyPeerForAdoption(p, status.Self, tagFilter, existing, idx)
		switch {
		case adopt:
			report.ToAdopt = append(report.ToAdopt, p)
		case rejected:
			report.Rejected = append(report.Rejected, DiscoveryRejection{
				Hostname: p.Hostname,
				IP:       p.TailscaleIP,
				Reason:   reason,
			})
		default:
			// A peer that is not a skygate-host candidate at all
			// (self, a laptop, an offline phone): counted for the
			// operator's "scanned N peers" line, never adopted.
			report.NotCandidate++
		}
	}
	return report, nil
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

// ============================================================================
// B359 (2026-10-07) — the skygate-host predicate and the named rejection reasons
// ============================================================================

// DiscoverySkipReason is the NAMED reason a discovered peer was not adopted as
// a skygate host. It is a code, not prose: the UI maps it to an i18n key and the
// log line prints it, so the same fact is spelled the same way in both places.
type DiscoverySkipReason string

const (
	// SkipRelayExitTag — the peer carries tailscale's literal `tag:exit-node`.
	// This is the B273 predicate for "this is an exit node" and the strongest
	// single fact available from `tailscale status --json`.
	SkipRelayExitTag DiscoverySkipReason = "relay-exit-tag"
	// SkipRelayExitServer — the peer has a row in `exit_servers`, skygate's own
	// record of the relays it manages (/admin/exit-nodes).
	SkipRelayExitServer DiscoverySkipReason = "relay-exit-server"
	// SkipNotSkygateHost — the peer carries none of the positive evidence that
	// it is a skygate host (the per-node infra tag the panel's onboarding mints
	// for that exact hostname).
	SkipNotSkygateHost DiscoverySkipReason = "not-skygate-host"
	// SkipAlreadyClusterMember — a cluster_node row for this hostname already
	// exists. Re-running discovery is a no-op (the idempotency property). This
	// also covers a host that is MID-ONBOARDING: /admin/cluster/onboard creates
	// the row before it mints the invite, so a host being provisioned through
	// the panel is already here.
	SkipAlreadyClusterMember DiscoverySkipReason = "already-cluster-member"
	// SkipIPv6Only — the peer has no IPv4 address, so it cannot be written to
	// `cluster_node.tailscale_ip`. This is a MISCONFIGURATION of a potential
	// skygate host, not "nothing to see": the whole point of naming it is that
	// the operator learns why a host they just provisioned never showed up.
	SkipIPv6Only DiscoverySkipReason = "ipv6-only"
	// SkipNoAddress — the peer reported no address at all. Same visibility
	// argument as SkipIPv6Only.
	SkipNoAddress DiscoverySkipReason = "no-address"
	// SkipNoHostname — the peer reported an empty HostName.
	SkipNoHostname DiscoverySkipReason = "no-hostname"
	// SkipSelf — the local node (we never adopt ourselves).
	SkipSelf DiscoverySkipReason = "self"
	// SkipOffline — the peer is not Online right now (B223's rule: we do not
	// create rows for a phone that is off).
	SkipOffline DiscoverySkipReason = "offline"
	// SkipTagFilter — the peer does not carry SKYGATE_DISCOVERY_TAG.
	SkipTagFilter DiscoverySkipReason = "tag-filter"
	// SkipNoInfraUser — the `infra` portal user is not linked to a headscale
	// user, so a preauth key minted for it would carry a tag headscale refuses
	// and ownership could never be recorded (see B266/B342).
	SkipNoInfraUser DiscoverySkipReason = "infra-user-not-linked"
)

// RelayIndex is the set of hostnames and IPs that belong to `exit_servers`.
// Lookups are case-insensitive on the hostname (headscale lower-cases node
// names, the operator's row may not) and exact on the IP.
type RelayIndex struct {
	Hostnames map[string]struct{}
	IPs       map[string]struct{}
}

// RelayIndexFromDB loads the relay index from `exit_servers`. It is the single
// reader of that table inside the discovery path: the same rows drive the
// /admin/exit-nodes page, so "the relay list" has exactly one source.
func RelayIndexFromDB(d *sql.DB) (*RelayIndex, error) {
	if d == nil {
		return &RelayIndex{}, nil
	}
	rows, err := d.Query(`SELECT hostname, COALESCE(tailscale_ip, '') FROM exit_servers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := &RelayIndex{Hostnames: map[string]struct{}{}, IPs: map[string]struct{}{}}
	for rows.Next() {
		var host, ip string
		if scanErr := rows.Scan(&host, &ip); scanErr != nil {
			return nil, scanErr
		}
		if h := strings.ToLower(strings.TrimSpace(host)); h != "" {
			idx.Hostnames[h] = struct{}{}
		}
		if ip = strings.TrimSpace(ip); ip != "" {
			idx.IPs[ip] = struct{}{}
		}
	}
	return idx, rows.Err()
}

// ContainsHost reports whether `hostname` is a known relay (case-insensitive).
func (r *RelayIndex) ContainsHost(hostname string) bool {
	if r == nil {
		return false
	}
	_, found := r.Hostnames[strings.ToLower(strings.TrimSpace(hostname))]
	return found
}

// ContainsIP reports whether `ip` is a known relay address.
func (r *RelayIndex) ContainsIP(ip string) bool {
	if r == nil {
		return false
	}
	_, found := r.IPs[strings.TrimSpace(ip)]
	return found
}

// IsRelay is the composed question the discovery predicate asks about a peer.
func (r *RelayIndex) IsRelay(hostname, ip string) bool {
	return r.ContainsHost(hostname) || (strings.TrimSpace(ip) != "" && r.ContainsIP(ip))
}

// hasExitNodeTag reports whether the peer carries tailscale's literal
// `tag:exit-node`. Compared case-insensitively, matching headscale (the same
// fold B273's admin tag test uses).
func hasExitNodeTag(tags []string) bool {
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), "tag:exit-node") {
			return true
		}
	}
	return false
}

// IsSkygateHostTag reports whether `tag` is the per-node infra tag for EXACTLY
// this hostname: `tag:dev-infra-<lowercase hostname>`. The exact-host match is
// deliberate — it is the same shape /admin/cluster/onboard mints
// ("tag:dev-infra-" + hostname) and the same shape the manual runbook's step 3
// applied. A bare `tag:dev-infra-` prefix would accept a relay tagged
// `tag:dev-infra-emilia` for the host `karolina`, and would accept the relay's
// own infra tag if the `tag:exit-node` / exit_servers facts were ever missing.
func IsSkygateHostTag(tag, hostname string) bool {
	h := strings.ToLower(strings.TrimSpace(hostname))
	if h == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(tag), "tag:dev-infra-"+h)
}

// DiscoveryRejection is one peer that discovery examined and did NOT adopt,
// with the named reason why. Every field is copied out of the live
// TailscalePeer — no live object is retained.
type DiscoveryRejection struct {
	Hostname string
	IP       string
	Reason   DiscoverySkipReason
}

// Report is the outcome of one discovery pass.
//
//	ToAdopt      — peers that passed the predicate; the caller INSERTs one
//	               cluster_node row each via EnsureDiscoveredNode.
//	Rejected     — peers that were examined and named as NOT skygate hosts
//	               (or not yet adoptable), with the reason.
//	NotCandidate — peers the pass never even considered (self, offline, tag
//	               filtered). Counted so "scanned N peers" stays honest.
type Report struct {
	ToAdopt      []TailscalePeer
	Rejected     []DiscoveryRejection
	NotCandidate int
}

// ClassifyPeerForAdoption is THE discovery predicate, as a pure function: no
// DB, no clock, no network. It answers three questions: adopt this peer? if
// not, is it a rejection the operator must be told about, and with what named
// reason?
//
// The relay check comes FIRST, so exit-node RELAYS are never adopted as
// skygate hosts even when they also carry an infra tag (they do: B111 gives
// every relay `tag:dev-infra-<its own hostname>`, and the relay happens to be
// the node `isInfraNode` was written for).
//
// The (name, ip) pair in the returned reason is the DECISION KEY the callers
// throttle on, so a repeat of the same verdict is one event, not one per tick.
func ClassifyPeerForAdoption(p, self TailscalePeer, tagFilter string, existing map[string]struct{}, relayIndex *RelayIndex) (adopt, rejected bool, reason DiscoverySkipReason) {
	if p.Hostname != "" && p.Hostname == self.Hostname {
		return false, false, SkipSelf
	}
	if !p.Online {
		return false, false, SkipOffline
	}
	if !matchesTagFilter(p, tagFilter) {
		return false, false, SkipTagFilter
	}
	// A relay is never a skygate host. Checked before every other fact.
	if hasExitNodeTag(p.Tags) {
		return false, true, SkipRelayExitTag
	}
	if relayIndex.IsRelay(p.Hostname, p.TailscaleIP) {
		return false, true, SkipRelayExitServer
	}
	if p.Hostname == "" {
		return false, true, SkipNoHostname
	}
	// Positive evidence: the per-node infra tag this exact host would carry if
	// it had been provisioned through the panel.
	infraTagged := false
	for _, t := range p.Tags {
		if IsSkygateHostTag(t, p.Hostname) {
			infraTagged = true
			break
		}
	}
	if !infraTagged {
		return false, true, SkipNotSkygateHost
	}
	if _, found := existing[p.Hostname]; found {
		return false, true, SkipAlreadyClusterMember
	}
	// cluster_node.tailscale_ip is INET (v4-only): a v6-only peer cannot be
	// recorded. That is a misconfiguration of a REAL candidate, so it is
	// reported rather than dropped (pre-B359 firstIPv4 returned "" and the
	// peer vanished).
	if p.TailscaleIP == "" {
		if len(p.Addresses) > 0 {
			return false, true, SkipIPv6Only
		}
		return false, true, SkipNoAddress
	}
	return true, false, ""
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
	// B354 (2026-10-06): the row this INSERT depends on must exist FIRST. Every
	// cluster_* table FKs to `cluster`, and the bootstrap row was created only by the
	// admin handlers' first-use paths (AddNode / IssueInvite) — so a deployment where
	// nobody had opened /admin/cluster had an EMPTY `cluster` table and this INSERT
	// failed on every tick:
	//
	//	🔎 discovery-ticker: ensure "karolina" failed: insert discovered node: ERROR:
	//	   insert or update on table "cluster_node" violates foreign key constraint
	//	   "cluster_node_cluster_id_fkey" (SQLSTATE 23503)
	//
	// measured live: three peers per tick (karolina, sharlotta, emilia), three log
	// lines and three audit rows every five minutes, `cluster_node` at 0 rows, and
	// /admin/cluster permanently empty. Discovery IS a first use of the cluster tree,
	// so it bootstraps the same row B200 would have — same id, name defaulted to the
	// id, ON CONFLICT DO NOTHING — and an operator's own cluster row is never touched.
	if _, err := LookupCluster(d, clusterID); err == ErrClusterNotFound {
		if err := EnsureCluster(d, clusterID, clusterID); err != nil {
			return fmt.Errorf("ensure cluster row: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("look up cluster row: %w", err)
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
	// B291 (2026-09-22): the roles array was written with the PostgreSQL-only
	// literal `ARRAY['skygate-standby']::text[]`, so on SQLite (the native `aro`
	// host) every discovery INSERT failed with
	//
	//	insert discovered node: SQL logic error: near "['skygate-standby']": syntax error
	//
	// every five minutes and cluster_node never received a row — which is why
	// /admin/cluster and /admin/ha were empty. The literal is now produced by
	// db.TextArrayLiteral and cast per dialect (PG coerces it, SQLite stores it
	// in the TEXT-affinity column the migration declares), and the timestamp is
	// bound through db.DialectKind.TimeValue so it reads back on both.
	dialect := db.ActiveDialect()
	_, err := d.Exec(`
		INSERT INTO cluster_node (
			id, cluster_id, hostname, tailscale_ip, roles, state,
			skygate_version, joined_at
		) VALUES ($1, $2, $3, $4, `+dialect.CastTextArray("$5")+`, 'pending', $6, $7)
		ON CONFLICT (id) DO NOTHING
	`, discID, clusterID, hostname, tailscaleIP,
		db.TextArrayLiteral([]string{"skygate-standby"}),
		"(discovered via Tailscale)", dialect.TimeValue(now))
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

// InfraUserLinked reports whether the `infra` portal user (the account that owns
// every skygate host and every relay, B111) is linked to a headscale user.
//
// B359: this is a PRECONDITION of the panel-only bootstrap, not decoration. The
// onboard action mints the new host's preauth key for that user
// (s.InfraHeadscaleUserID → CreatePreauthKeyWithTags); with no linked headscale
// user the key would carry a tag headscale refuses, and the ownership row could
// never be written — so the panel must say so BEFORE the operator provisions a
// VM, instead of letting them discover it on the new host. A missing `infra`
// row returns (false, nil): "not provisioned yet" is a state to report, not an
// error to raise.
func InfraUserLinked(d *sql.DB) (bool, error) {
	if d == nil {
		return false, nil
	}
	var hsID sql.NullInt64
	err := d.QueryRow(
		`SELECT headscale_user_id FROM portal_users WHERE username = 'infra'`,
	).Scan(&hsID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return hsID.Valid && hsID.Int64 > 0, nil
}
