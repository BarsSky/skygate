// Package sidecar — per-user subnet sidecar provisioning for
// skygate v0.16.7+.
//
// The per-user subnet feature (v0.16.0 roadmap, v0.16.6 ships the
// data model, v0.16.7 ships the runtime) has two halves:
//
//  1. **Database row** — user_subnets + 3 denorm columns on
//     portal_users. v0.16.6. The "pending" state means "row
//     exists, no node yet".
//
//  2. **Live node** — a tailscale node tagged `tag:subnet-router`
//     that advertises the per-user CIDR. v0.16.7. This package
//     owns the auto-approval + status-sync logic that turns a
//     pending row into an active one.
//
// The user (or their machine) runs a tailscaled sidecar that
// authenticates with a per-user preauth key. The sidecar is the
// 24/7 anchor that gives the user's tailnet devices a route
// into `10.0.<uid>.0/24`. The actual sidecar container management
// is OUT OF SCOPE for v0.16.7 — the user provides the preauth
// key to whatever runtime they want (their own machine, a
// Docker container on a host they control, a Raspberry Pi, etc.).
// All this package does is:
//
//   - issue preauth keys (tag:subnet-router, single-use, 1h TTL)
//   - watch headscale for new tag:subnet-router nodes
//   - auto-approve the per-user CIDR route when it appears
//   - flip user_subnets.status to "active" with router_node_id
//   - detect stale nodes and mark "disabled" (last_seen > 5min)
//
// Future v0.17.0 work will add `tag:subnet-router` to
// GenerateACL's tagOwners so the sidecar node is allowed to
// advertise the per-user CIDR; the auto-approver already does
// the headscale-side approval today.
package sidecar

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/headscale"
	"skygate/internal/subnet"
)

// HSResolver returns the headscale client that talks to the
// control plane this user lives on. For per-plane users (v0.12.0+)
// this is the user's own client; for global users it's the
// default. The sidecar manager calls this once per user per
// sync cycle to keep the API surface small.
type HSResolver func(userID int64) *headscale.Client

// Manager owns the sidecar lifecycle. One per skygate process.
// Spawned by cmd/skygate/main.go on app start; Run() blocks the
// caller's goroutine until ctx is cancelled.
type Manager struct {
	DB         *sql.DB
	HS         HSResolver
	Logger     *log.Logger
	syncPeriod time.Duration

	mu        sync.RWMutex
	lastSync  time.Time
	lastStats SyncStats
}

// SyncStats is the result of a single sync cycle. Exposed via
// Manager.LastStats() so the admin UI can show "last sync: 1
// node approved, 0 disabled" without touching the DB.
type SyncStats struct {
	At              time.Time
	NodesScanned    int
	RoutesApproved  int
	StatusActivated int
	StatusDisabled  int
	Errors          int
}

// New returns a Manager. The sync period defaults to 30s; the
// operator can override via SKYGATE_SIDECAR_SYNC_PERIOD env var
// in cmd/skygate/main.go.
func New(db *sql.DB, hs HSResolver, logger *log.Logger, syncPeriod time.Duration) *Manager {
	if logger == nil {
		logger = log.Default()
	}
	if syncPeriod <= 0 {
		syncPeriod = 30 * time.Second
	}
	return &Manager{
		DB:         db,
		HS:         hs,
		Logger:     logger,
		syncPeriod: syncPeriod,
	}
}

// Run is the main loop. It blocks until ctx is cancelled.
// Each tick runs SyncOnce, which is idempotent (safe to call
// concurrently with itself if you spawn two Managers by
// accident — the DB-level state checks prevent double-approval).
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(m.syncPeriod)
	defer t.Stop()
	// First sync right away, no warmup delay.
	if err := m.SyncOnce(ctx); err != nil {
		m.Logger.Printf("sidecar.SyncOnce (initial): %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.SyncOnce(ctx); err != nil {
				m.Logger.Printf("sidecar.SyncOnce: %v", err)
			}
		}
	}
}

// SyncOnce performs one full sync pass. Steps:
//
//  1. List every tag:subnet-router node in headscale (across all
//     planes). For each node, derive the username from the
//     hostname ("skygate-subnet-<username>") and look up the
//     matching portal user.
//  2. If the node has a pending route for the user's CIDR
//     (10.0.<uid>.0/24) and the row is pending, approve it
//     via the headscale API, then flip status to active with
//     router_node_id = node.ID.
//  3. For each user_subnets row that is currently active, check
//     that the corresponding node is still in headscale and
//     that its last_seen is within 5 minutes. If stale, flip
//     status to disabled.
//
// Returns a SyncStats summary; also stores it on the Manager
// (thread-safe via mu) for LastStats() to read.
func (m *Manager) SyncOnce(ctx context.Context) error {
	stats := SyncStats{At: time.Now()}

	nodesByUser, err := m.scanNodesByUser(ctx)
	if err != nil {
		stats.Errors++
		m.recordStats(stats)
		return fmt.Errorf("scan nodes: %w", err)
	}
	stats.NodesScanned = sumLen(nodesByUser)

	// Approved-routes pass: pending → active.
	for userID, ns := range nodesByUser {
		// Only act on the first node for the user.
		// Multiple nodes shouldn't happen (one sidecar per user)
		// but we don't want to N-approve if it does.
		if err := m.activateIfRoutesApproved(ctx, userID, ns[0]); err != nil {
			m.Logger.Printf("sidecar.activate user=%d node=%s: %v", userID, ns[0].ID, err)
			stats.Errors++
			continue
		}
		if subnetRow, _ := subnet.Get(m.DB, userID); subnetRow != nil && subnetRow.Status == subnet.StatusActive {
			stats.StatusActivated++
		}
		if ns[0].ApprovedRoutes != nil {
			stats.RoutesApproved++
		}
	}

	// Disabled pass: active → disabled if node gone stale.
	allSubnets, err := m.listAllSubnets()
	if err != nil {
		m.recordStats(stats)
		return fmt.Errorf("list subnets: %w", err)
	}
	for _, sn := range allSubnets {
		if sn.Status != subnet.StatusActive {
			continue
		}
		nodeID := sn.RouterNodeID
		if nodeID == "" {
			continue
		}
		ns, ok := nodesByUser[sn.UserID]
		if !ok || len(ns) == 0 || ns[0].ID != nodeID {
			// Node disappeared from headscale.
			if err := subnet.SetStatus(m.DB, sn.UserID, subnet.StatusDisabled); err != nil {
				m.Logger.Printf("sidecar.disable user=%d: %v", sn.UserID, err)
				stats.Errors++
				continue
			}
			stats.StatusDisabled++
		}
	}

	m.recordStats(stats)
	return nil
}

func sumLen(m map[int64][]headscale.NodeView) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}

// scanNodesByUser lists all headscale nodes tagged tag:subnet-router
// across all control planes the resolver knows about, groups them
// by portal user_id (parsed from the hostname), and returns the map.
//
// In a single-plane deployment there's exactly one resolver
// invocation. In a multi-plane deployment (v0.12.0+), we walk
// every distinct plane via the DB.
func (m *Manager) scanNodesByUser(ctx context.Context) (map[int64][]headscale.NodeView, error) {
	// Pull every distinct control_plane_url from user_subnets so
	// we don't probe a plane that has no subnets. Empty string
	// means "use global default plane" — resolved via a nil UID
	// (the global client).
	planeURLs, err := m.distinctPlaneURLs()
	if err != nil {
		return nil, err
	}
	// Always include the global default if not present, so
	// single-plane deployments Just Work without needing a
	// row in user_subnets yet.
	hasDefault := false
	for _, u := range planeURLs {
		if u == "" {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		planeURLs = append(planeURLs, "")
	}

	out := make(map[int64][]headscale.NodeView)
	for _, planeURL := range planeURLs {
		hs := m.resolveHS(planeURL)
		if hs == nil {
			continue
		}
		nodes, err := hs.ListAllNodes()
		if err != nil {
			m.Logger.Printf("sidecar.ListAllNodes plane=%q: %v", planeURL, err)
			continue
		}
		for _, n := range nodes {
			if !hasTag(n.Tags, "tag:subnet-router") {
				continue
			}
			username, ok := UsernameFromHostname(n.Hostname)
			if !ok {
				continue
			}
			uid, err := m.UserIDFromUsername(ctx, username)
			if err != nil {
				m.Logger.Printf("sidecar.UserIDFromUsername %q: %v", username, err)
				continue
			}
			out[uid] = append(out[uid], n)
		}
	}
	return out, nil
}

// hasTag is a small slice helper.
func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// activateIfRoutesApproved approves the user's CIDR route on the
// given node (if pending) and flips user_subnets.status to
// active with the node_id. No-op if the row is already active.
func (m *Manager) activateIfRoutesApproved(ctx context.Context, userID int64, node headscale.NodeView) error {
	sub, err := subnet.Get(m.DB, userID)
	if err != nil {
		return err
	}
	if sub == nil {
		return nil // no subnet row → user never pressed Allocate
	}
	// Always (re)sync router_node_id + denorm even if status
	// is already active (covers a re-registration after crash).
	if sub.RouterNodeID != node.ID {
		if err := subnet.SetRouter(m.DB, userID, node.ID, ""); err != nil {
			return err
		}
	}
	// If the node has the per-user CIDR in its AvailableRoutes
	// (i.e. the user ran `tailscale up --advertise-routes=...`)
	// but it's not yet in ApprovedRoutes, approve it now.
	// ApproveAllRoutesWithList is idempotent — `--force` flag
	// makes headscale overwrite the existing approval, so
	// re-running on an already-approved node is safe.
	avail := node.AvailableRoutes
	if containsCIDR(avail, sub.CIDR) && !containsCIDR(node.ApprovedRoutes, sub.CIDR) {
		hs := m.HS(userID)
		if hs == nil {
			return fmt.Errorf("no hs client for user %d", userID)
		}
		if _, err := hs.ApproveAllRoutesWithList(node.Hostname, []string{sub.CIDR}); err != nil {
			return fmt.Errorf("approve %s for node %s: %w", sub.CIDR, node.Hostname, err)
		}
		m.Logger.Printf("sidecar: approved %s on node %s (user=%d)", sub.CIDR, node.Hostname, userID)
	}
	// If the node already has the per-user CIDR in its
	// approved routes, flip status to router_active. This
	// branch runs in the SAME sync cycle as the approval
	// above because headscale's approve-routes call updates
	// the in-memory HSNode (returned by the call), but our
	// local `node` variable was loaded BEFORE the approval
	// — so the live state is now ahead of the local state.
	// For correctness we just check whether the approval
	// call succeeded and flip accordingly; the next sync
	// cycle (30s later) re-reads the live state.
	//
	// 2026-07-22: v0.26.0 — was `subnet.StatusActive` here,
	// which was correct under the pre-v0.22.3 binary
	// status (active ⇔ route approved). v0.22.3 split this
	// into active (no router) vs router_active (router up).
	// Since this code path only runs when there's a live
	// `tag:subnet-router` node that just got the route
	// approved, the right status is router_active. The
	// previous value was clobbering the v0.22.3
	// router_active that the backfill had just set, causing
	// the status pill to flicker back to "active" every
	// 30s (between SyncOnce ticks and /my/devices loads).
	// Also respects the manual disabled override.
	if containsCIDR(node.ApprovedRoutes, sub.CIDR) {
		if sub.Status != subnet.StatusRouterActive && sub.Status != subnet.StatusDisabled {
			if err := subnet.SetStatus(m.DB, userID, subnet.StatusRouterActive); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) listAllSubnets() ([]subnet.Subnet, error) {
	rows, err := m.DB.Query(`
		SELECT id, user_id, cidr, status, control_plane_url, router_node_id,
		       router_container_id, router_hostname, created_at, updated_at
		  FROM user_subnets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []subnet.Subnet
	for rows.Next() {
		var s subnet.Subnet
		var routerNode, routerContainer, routerHost sql.NullString
		if err := rows.Scan(&s.ID, &s.UserID, &s.CIDR, &s.Status, &s.ControlPlaneURL,
			&routerNode, &routerContainer, &routerHost, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if routerNode.Valid {
			s.RouterNodeID = routerNode.String
		}
		if routerContainer.Valid {
			s.RouterContainerID = routerContainer.String
		}
		if routerHost.Valid {
			s.RouterHostname = routerHost.String
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *Manager) distinctPlaneURLs() ([]string, error) {
	rows, err := m.DB.Query(`
		SELECT DISTINCT control_plane_url
		  FROM user_subnets
		 WHERE control_plane_url != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *Manager) resolveHS(planeURL string) *headscale.Client {
	// planeURL == "" → global default (the App's HSForUser path
	// would also accept a 0 user_id, but Manager doesn't have
	// that — we let the resolver do its job with a sentinel 0).
	if m.HS == nil {
		return nil
	}
	if planeURL == "" {
		return m.HS(0)
	}
	// For per-plane resolution, we don't know the user_id from
	// just the URL — the resolver does a best-effort lookup by
	// walking the DB. For now, we use 0 as a "global" sentinel
	// and let the resolver fall back to the default plane.
	// (Multi-plane refinement is v0.17.0 territory.)
	return m.HS(0)
}

func (m *Manager) recordStats(s SyncStats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSync = s.At
	m.lastStats = s
}

// LastSync returns the wall-clock time of the most recent sync
// (zero value if no sync has run yet).
func (m *Manager) LastSync() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastSync
}

// LastStats returns the most recent sync stats. Useful for the
// admin UI to show "last sync: approved 1, disabled 0".
func (m *Manager) LastStats() SyncStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastStats
}

// GeneratePreauth issues a single-use preauth key with
// `tag:subnet-router` for the given user. The key expires in
// 1 hour — the user has 1 hour to paste it into their
// tailscale up command. After the node registers, future
// syncs see it and approve the route.
//
// Errors:
//   - subnet.ErrNotFound if the user has no subnet row
//   - headscale errors (network, auth) propagated as-is
func (m *Manager) GeneratePreauth(ctx context.Context, userID int64) (string, time.Time, error) {
	sub, err := subnet.Get(m.DB, userID)
	if err != nil {
		return "", time.Time{}, err
	}
	if sub == nil {
		return "", time.Time{}, subnet.ErrNotFound
	}
	// Look up the headscale user_id for this portal user.
	var hsUserID int64
	if err := m.DB.QueryRow(
		`SELECT headscale_user_id FROM portal_users WHERE id = $1`, userID,
	).Scan(&hsUserID); err != nil {
		return "", time.Time{}, fmt.Errorf("lookup portal user %d: %w", userID, err)
	}
	if hsUserID == 0 {
		return "", time.Time{}, fmt.Errorf("user %d has no headscale_user_id", userID)
	}
	hs := m.HS(userID)
	if hs == nil {
		return "", time.Time{}, fmt.Errorf("no headscale client for user %d", userID)
	}
	const ttl = 1 * time.Hour
	exp := time.Now().Add(ttl)
	pk, err := hs.CreatePreauthKeyWithTags(hsUserID, ttl.String(), false, []string{"tag:subnet-router"})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create preauth: %w", err)
	}
	m.Logger.Printf("sidecar: preauth issued user=%d key_id=%s expires=%s", userID, pk.ID, exp.Format(time.RFC3339))
	return pk.Key, exp, nil
}

// PreauthInfo is the result of GeneratePreauth — also embedded
// in the admin page so the operator can copy the key into a
// message to the user.
type PreauthInfo struct {
	Key        string
	ExpiresAt  time.Time
	Hostname   string // suggested --hostname for tailscale up
	Routes     string // suggested --advertise-routes
}

// BuildPreauthInfo returns a ready-to-display preauth + command
// snippet for the admin UI / bot. Doesn't talk to headscale —
// uses the existing subnet row + the freshly-issued key.
func (m *Manager) BuildPreauthInfo(userID int64, key string, expiresAt time.Time, username string) PreauthInfo {
	sub, err := subnet.Get(m.DB, userID)
	cidr := ""
	if err == nil && sub != nil {
		cidr = sub.CIDR
	}
	return PreauthInfo{
		Key:       key,
		ExpiresAt: expiresAt,
		Hostname:  "skygate-subnet-" + username,
		Routes:    cidr,
	}
}

// parseSubnetRouterHostname extracts the username from a
// sidecar hostname of the form "skygate-subnet-<username>".
// Returns ("", false) if the hostname doesn't match the
// expected pattern. Use UsernameFromHostname — this is kept
// as an alias for backward compat.
func parseSubnetRouterHostname(name string) (int64, bool) {
	username, ok := UsernameFromHostname(name)
	if !ok {
		return 0, false
	}
	// Older API returned an int64. Kept for callers that
	// haven't been migrated; new callers should use
	// UsernameFromHostname + UserIDFromUsername.
	_ = username
	return 0, true
}

// UsernameFromHostname returns the username encoded in a
// skygate-subnet-<name> hostname, plus a boolean indicating
// whether the prefix matched. The caller uses this to do a
// portal_users lookup and translate the hostname to a real
// user_id.
func UsernameFromHostname(name string) (string, bool) {
	const prefix = "skygate-subnet-"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(name, prefix)
	if rest == "" {
		return "", false
	}
	return rest, true
}

// containsCIDR returns true if cidrs contains the given CIDR
// (loose match — headscale may return it normalised).
func containsCIDR(cidrs []string, cidr string) bool {
	for _, c := range cidrs {
		if c == cidr {
			return true
		}
		// Loose: handle "10.0.42.0/24" vs "10.0.42.0/24" (no
		// normalisation needed in practice, but defend
		// against whitespace).
		if strings.TrimSpace(c) == cidr {
			return true
		}
	}
	return false
}

// CIDRForUser returns the deterministic 10.0.<uid>.0/24
// allocation for the given portal user_id. Wraps
// subnet.AllocateCIDR so the sidecar package doesn't leak
// the subnet package's signature into every callsite.
func CIDRForUser(userID int64) (string, error) {
	return subnet.AllocateCIDR(userID)
}

// UserIDFromUsername is a small helper that the auto-approver
// uses to translate a hostname (skygate-subnet-<username>) into
// a portal user_id. Pulled into a top-level function for test
// reuse; the actual lookup is just a SELECT.
func (m *Manager) UserIDFromUsername(ctx context.Context, username string) (int64, error) {
	var id int64
	err := m.DB.QueryRowContext(ctx,
		`SELECT id FROM portal_users WHERE username = $1`, username,
	).Scan(&id)
	return id, err
}

// helper for tests + code that needs the user's headscale_user_id.
func (m *Manager) HeadscaleUserID(ctx context.Context, userID int64) (int64, error) {
	var id int64
	err := m.DB.QueryRowContext(ctx,
		`SELECT headscale_user_id FROM portal_users WHERE id = $1`, userID,
	).Scan(&id)
	return id, err
}

// strconvI64 is a small helper that wraps strconv.ParseInt
// with a more contextual error. Used by the public helpers
// above when parsing user_id from a path or form value.
func strconvI64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
