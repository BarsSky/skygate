// Package admin — exit_nodes.go owns the /admin/exit-nodes page
// (list, add, delete, sync, health-now, tag/untag) and the
// helpers used by the headscale-update-monitor banner that
// the template renders above the table.
//
// refactor-v0.30 Phase B step 3b.3 (2026-07-29): moved from
// internal/handlers/admin_exit_nodes.go. The handlers used
// to be methods on *App; they now live on *Service. Fields
// that were on *App (SSHKeyPath, ExitNodeMonitor) and the
// SyncAdvertisedRoutes callback are now Service fields,
// wired from cmd/skygate/main.go. The tag-test file
// (admin_exit_nodes_tag_test.go) was deleted because it
// depended on internal/handlers test helpers (authedReqFor,
// newTestApp) that don't exist in this package yet.
//
// File map after the refactor Phase D split (2026-10-01). The file was 1880
// lines and mixed the page, its write paths, the prefix-assignment truth block
// and several helper families:
//   - exit_nodes.go              — this doc, ExitNodeInfo, the shared type
//   - exit_nodes_page.go         — GET /admin/exit-nodes
//   - exit_nodes_helpers.go      — pure helpers (tags, sync status, version banner)
//   - exit_nodes_handlers.go     — the POST write paths
//   - exit_nodes_prefix_drift.go — prefix_owner vs the advertised/ACL policy
//   - exit_nodes_tag.go          — tag / untag a node as an exit node
//   - exit_nodes_servers.go      — exit_servers row inclusion + backfill
//
// The B-checks that pin this page now read the exit_nodes* SURFACE through
// scripts/lib/gosurface.sh instead of one path — see B339.

package admin

import (
	"time"
)

// ExitNodeInfo is the row shape for /admin/exit-nodes. Most
// fields are populated from the DB (db.ListExitServers) and
// enriched from headscale (ListAllNodes) + the health-monitor
// snapshot (db.ListExitNodeHealth). The template renders every
// field — see internal/handlers/templates/admin/exit_nodes.html.
type ExitNodeInfo struct {
	NodeID      string `json:"node_id"`
	Hostname    string `json:"hostname"`
	TailscaleIP string `json:"tailscale_ip"`
	SSHTarget   string `json:"ssh_target"`
	// 2026-08-09 v0.33.1.29 B81: the SSH target that the next
	// SyncAdvertisedRoutes call will actually use, after applying
	// the operator-override → root@<tailscale_ip> → "" fallback
	// chain (via db.LookupExitServerSSHTarget). The template
	// renders this in the SSH column so the operator can see what
	// the SSH step will hit BEFORE the next sync (the previous
	// "show only the stored ssh_target, see the actual one in the
	// audit log after a failed sync" UX was the v0.33.1 trap).
	// SSHTargetAuto is true when the resolved value came from the
	// tailscale_ip column (not from ssh_target) — the template
	// uses it to render a subtle "auto" badge so the operator
	// knows the row is using the B81 fallback and the stored
	// ssh_target column is empty.
	SSHTargetAuto     bool   `json:"ssh_target_auto"`
	ResolvedSSHTarget string `json:"resolved_ssh_target"`
	SSHKeyPath        string `json:"ssh_key_path"`
	// 2026-08-10 v0.33.1.33 B85: the per-row non-default SSH
	// port. The B81 auto-fallback in LookupExitServerSSHTarget
	// appends ":<port>" to "root@<tailscale_ip>" when this is
	// set. Empty = port 22 (the v0.33.1.29 / v0.33.1.32 default).
	// The template renders this in the add form (the only
	// place the operator edits it) — the table render shows
	// the full ResolvedSSHTarget (which already includes the
	// port via the B85 chain).
	SSHPort      string   `json:"ssh_port"`
	Enabled      bool     `json:"enabled"`
	Routes       []string `json:"routes"`
	RouteCount   int      `json:"route_count"`
	SyncStatus   string   `json:"sync_status"`
	Description  string   `json:"description"`
	AcceptRoutes int      `json:"accept_routes"` // -1=false, 0=unset, 1=true
	// B312: WHERE this relay sits (from exit_servers.location_*, V077). LocationKnown
	// is false when nothing is known yet — the page then says "неизвестно" instead of
	// rendering an empty cell that looks like a bug, and the location-priority
	// fallback simply does not apply to this relay.
	Location       string `json:"location"`
	LocationSource string `json:"location_source"`
	LocationKnown  bool   `json:"location_known"`
	// 2026-07-15: v0.13.0 — health monitor fields. Populated
	// from exit_node_health (the snapshot table updated by
	// the background monitor) and matched on NodeID. Empty
	// strings / false mean "no snapshot yet" — the page
	// renders a "—" placeholder.
	Online             bool      `json:"online"`
	LastSeen           string    `json:"last_seen"`
	LastSeenAgo        string    `json:"last_seen_ago"`
	State              string    `json:"state"`
	Healthy            bool      `json:"healthy"`
	LastCheckAt        time.Time `json:"last_check_at"`
	HasExitTag         bool      `json:"has_exit_tag"`
	AdvertisedRoutesOK bool      `json:"advertised_routes_ok"`
	// 2026-07-17: v0.18.1 — raw headscale-side state. The
	// "Tag as exit-node" / "Untag" buttons need to know
	// whether the node already has tag:exit-node and
	// whether it advertises 0.0.0.0/0 + ::/0 (the
	// exit-node bases). Without these the template
	// can't decide which button to render.
	Tags                []string `json:"tags"`
	AdvertisesV4Default bool     `json:"advertises_v4_default"`
	AdvertisesV6Default bool     `json:"advertises_v6_default"`
	// B273 (v1.5.18) — the APPROVED half of the same question,
	// read from the live headscale node (NodeView.ApprovedRoutes)
	// rather than from the health snapshot. Advertised-but-
	// unapproved is the single most common "the relay is up but
	// nothing routes" state, and pre-B273 no page showed it:
	// /admin/exit-nodes counted AvailableRoutes (advertised) and
	// the monitor called it healthy. ApprovedV4Default is the one
	// that decides whether clients actually receive the
	// 0.0.0.0/0 exit route.
	ApprovedV4Default bool `json:"approved_v4_default"`
	ApprovedV6Default bool `json:"approved_v6_default"`
	// ApprovedRoutesOK is the single-boolean form used by the
	// template's "маршруты не одобрены" warning tag.
	ApprovedRoutesOK bool `json:"approved_routes_ok"`
	// B292 (2026-09-23) — the SSH half of the same "will the next sync be able
	// to do anything" question.
	//
	// EffectiveSSHKeyPath is the path the next sync will pass to `ssh -i`: the
	// row's ssh_key_path, else the global default (which is now resolved per
	// install kind — /ssh-sync/id_ed25519 inside the container, a host path under
	// the data dir on a native install). SSHKeyState is one of
	// ok/unset/missing/unreadable/directory/relative/inaccessible (see
	// headscale.SSHKeyState) and SSHKeyNote carries the same reason plus the fix
	// for the tooltip. Pre-B292 none of this was on the page: the first sign of
	// trouble was a raw `ssh` warning inside a GREEN flash after pressing
	// Re-sync, while the sync silently advertised nothing.
	EffectiveSSHKeyPath string `json:"effective_ssh_key_path"`
	SSHKeyState         string `json:"ssh_key_state"`
	SSHKeyNote          string `json:"ssh_key_note"`
	// B293 (2026-09-23) — this relay IS this host.
	//
	// LocalRelay is set when the live local tailscaled's own addresses include
	// one of the relay's headscale addresses (the operator's `aro`: the local
	// daemon is `exit-node-vps`, 100.64.0.1). Such a relay is configured with a
	// LOCAL `tailscale set` (see internal/headscale/local_apply_b293.go) — no
	// SSH, no key — so the SSH-column warning is suppressed and the row says so.
	// LocalTransport names the rung the next sync will use (direct / sudo /
	// helper), or why none is available.
	LocalRelay     bool   `json:"local_relay"`
	LocalTransport string `json:"local_transport"`
}
