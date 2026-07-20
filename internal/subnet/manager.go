package subnet

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// manager.go — CRUD layer for user_subnets.
//
// The allocator (allocator.go) is a pure function: given
// a user_id, return a CIDR. The manager wraps it with
// DB lookups to provide the CRUD operations the rest of
// skygate needs:
//
//   - Create: allocate + insert a row in pending state
//   - Get:    look up by user_id (one subnet per user)
//   - List:   for the admin subnet map
//   - SetStatus: status transitions (pending → active →
//     disabled) as the sidecar comes up / goes down
//   - SetRouter: stub for v0.16.1 (sidecar fills node_id
//     and container_id once it registers with headscale)
//
// All operations keep `portal_users.subnet_cidr` /
// `subnet_status` / `subnet_router_node_id` in sync —
// the denormalized columns are read by /mysubnet and
// /admin/users/{id} without a JOIN, so they MUST match
// the user_subnets row.
//
// 2026-07-17: v0.16.0 — schema + CRUD. The actual
// sidecar container management (start tailscaled,
// issue preauth, register node, approve routes) is
// the v0.16.1 follow-up. The manager's Create method
// just allocates the row; SetStatus / SetRouter are
// what v0.16.1 will call as the sidecar transitions
// through its lifecycle.

// Status is the lifecycle state of a user_subnets row.
//
// pending       — no devices yet (or no nodes snapshot for the user)
//                 the row is allocated, but the user has not added
//                 any Tailscale devices, so there's nothing to
//                 route into the personal subnet. This is the
//                 natural state for a freshly-created user.
//
// active        — user has ≥1 device (any tag) in headscale, the
//                 10.0.<uid>.0/24 CIDR is "active" as a logical
//                 namespace. Devices can reach it via the per-user
//                 ACL rule, but the user has NOT set up a
//                 subnet-router (no machine advertising 10.0.<uid>.0/24
//                 into the tailnet). This is the v0.22.3 default
//                 for every user with at least one device.
//
// router_active — bonus status: active + a tag:subnet-router node
//                 is registered in headscale AND has the user's
//                 CIDR in its approved routes. Means the user has
//                 actually set up a subnet-router on their home
//                 network and the 10.0.<uid>.0/24 is a real,
//                 routable subnet (not just a label).
//
// disabled      — opt-out; row kept for audit but no live
//                 subnet. Manual override via the admin "Disable"
//                 button. SyncStatus preserves this state across
//                 re-runs (admin's intent wins over derived state).
//
// 2026-07-21: v0.22.3 — added router_active status + reworked
// pending semantics. Pre-v0.22.3, pending meant "no subnet-router
// registered" which left every production user in pending even
// when they had plenty of devices in the tailnet. v0.22.3 flips
// it: pending = no devices, active = devices exist, router_active
// = bonus on top.
//
// The string values are stored in user_subnets.status and
// portal_users.subnet_status (the latter is the "none" case for
// users who never opted in).
const (
	StatusPending      = "pending"
	StatusActive       = "active"
	StatusRouterActive = "router_active"
	StatusDisabled     = "disabled"
)

// Subnet is the in-memory representation of a
// user_subnets row. Maps 1:1 to the table columns.
//
// 2026-07-17: v0.16.0. Status, RouterNodeID, and
// RouterContainerID are empty in v0.16.0 (no sidecar
// yet); SetStatus fills Status, and the v0.16.1
// sidecar work fills RouterNodeID + RouterContainerID.
type Subnet struct {
	ID                int64
	UserID            int64
	CIDR              string
	SubnetBits        int
	ControlPlaneURL   string
	Status            string
	RouterNodeID      string
	RouterContainerID string
	RouterHostname    string
	CreatedAt         int64
	UpdatedAt         int64
}

// ErrAlreadyExists is returned by Create when the user
// already has a subnet row. Callers can detect this
// and call Get instead (the typical flow is "if exists,
// show the existing subnet; else offer to create one").
var ErrAlreadyExists = fmt.Errorf("subnet: user already has a subnet")

// ErrNotFound is returned by Get / SetStatus / SetRouter
// when the user_id has no subnet row. The admin UI
// uses this to show "No subnet allocated" on
// /admin/users/{id}/subnet.
var ErrNotFound = fmt.Errorf("subnet: no subnet for user")

// Create allocates a CIDR for userID and inserts a
// pending-state user_subnets row. If a row already
// exists (UNIQUE(user_id) violation), returns the
// existing row + ErrAlreadyExists — the caller can then
// return that to the admin UI ("you already have a
// subnet allocated; here it is").
//
// controlPlaneURL is the per-plane context (v0.12.0
// multi-plane); '' = global plane. The same string is
// also written to user_subnets.control_plane_url so
// v0.13.0's per-plane ACL generation can scope subnet
// rules to the right plane.
//
// routerHostname is the operator-friendly name (e.g.
// "skygate-subnet-alice"); empty in v0.16.0, filled by
// the v0.16.1 sidecar provisioning step.
//
// 2026-07-17: v0.16.0.
//
// Implementation notes:
//   - The whole Create runs in a tx so the user_subnets
//     row + the portal_users denorm columns are written
//     atomically. A failed INSERT (UNIQUE violation)
//     triggers a Rollback before we call Get (the
//     Rollback releases SQLite's per-connection write
//     lock so a separate Get on the same *sql.DB can
//     proceed without deadlocking).
//   - The pre-check (SELECT 1 FROM portal_users WHERE
//     id=?) catches "user not in portal_users" before
//     the INSERT, so the admin UI gets a clear
//     "user_id=N not found in portal_users" error
//     instead of a raw "FOREIGN KEY constraint failed".
func Create(d *sql.DB, userID int64, controlPlaneURL, routerHostname string) (*Subnet, error) {
	// Allocate the CIDR first so we can return a
	// typed error for out-of-range user_id without
	// touching the DB.
	cidr, err := AllocateCIDR(userID)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	bits := DefaultSubnetBits
	tx, err := d.Begin()
	if err != nil {
		return nil, fmt.Errorf("subnet: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	// Pre-check: the user_subnets.user_id column has a
	// FOREIGN KEY reference to portal_users(id). One
	// extra query for a clearer error message is
	// worth it.
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM portal_users WHERE id = ?`, userID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("subnet: user_id=%d not found in portal_users", userID)
		}
		return nil, fmt.Errorf("subnet: pre-check portal_users: %w", err)
	}
	res, err := tx.Exec(`
		INSERT INTO user_subnets
			(user_id, cidr, subnet_bits, control_plane_url,
			 status, router_hostname, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, userID, cidr, bits, controlPlaneURL, StatusPending, routerHostname, now, now)
	if err != nil {
		// UNIQUE violation on user_id. Rollback
		// FIRST (so SQLite's per-connection write
		// lock releases) then Get the existing row.
		_ = tx.Rollback()
		var existing *Subnet
		var getErr error
		if existing, getErr = Get(d, userID); getErr == nil && existing != nil {
			return existing, ErrAlreadyExists
		}
		return nil, fmt.Errorf("subnet: insert: %w", err)
	}
	newID, _ := res.LastInsertId()
	// Update the denormalized columns on portal_users.
	res, err = tx.Exec(`
		UPDATE portal_users
		   SET subnet_cidr = ?,
		       subnet_status = ?,
		       subnet_router_node_id = ''
		 WHERE id = ?
	`, cidr, StatusPending, userID)
	if err != nil {
		return nil, fmt.Errorf("subnet: update portal_users: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Race: user was deleted between our pre-check
		// and this UPDATE. Should never happen in
		// practice (admin UI is single-tenant), but
		// surface the error so we don't silently leak
		// an orphan subnet row.
		return nil, fmt.Errorf("subnet: portal_users row for user_id=%d disappeared mid-tx", userID)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("subnet: commit: %w", err)
	}
	return &Subnet{
		ID:              newID,
		UserID:          userID,
		CIDR:            cidr,
		SubnetBits:      bits,
		ControlPlaneURL: controlPlaneURL,
		Status:          StatusPending,
		RouterHostname:  routerHostname,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

// Get returns the subnet row for userID, or
// ErrNotFound if no subnet is allocated.
func Get(d *sql.DB, userID int64) (*Subnet, error) {
	row := d.QueryRow(`
		SELECT id, user_id, cidr, subnet_bits, control_plane_url,
		       status, router_node_id, router_container_id,
		       router_hostname, created_at, updated_at
		  FROM user_subnets
		 WHERE user_id = ?
	`, userID)
	var s Subnet
	if err := row.Scan(&s.ID, &s.UserID, &s.CIDR, &s.SubnetBits, &s.ControlPlaneURL,
		&s.Status, &s.RouterNodeID, &s.RouterContainerID, &s.RouterHostname,
		&s.CreatedAt, &s.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("subnet: get: %w", err)
	}
	return &s, nil
}

// List returns all subnet rows, sorted by user_id. Used
// by the admin subnet map (/admin/control-planes
// extension) and by /mysubnet for "shared with me" /
// "I shared with" lookups (v0.17.1).
func List(d *sql.DB) ([]*Subnet, error) {
	rows, err := d.Query(`
		SELECT id, user_id, cidr, subnet_bits, control_plane_url,
		       status, router_node_id, router_container_id,
		       router_hostname, created_at, updated_at
		  FROM user_subnets
		 ORDER BY user_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("subnet: list: %w", err)
	}
	defer rows.Close()
	var out []*Subnet
	for rows.Next() {
		var s Subnet
		if err := rows.Scan(&s.ID, &s.UserID, &s.CIDR, &s.SubnetBits, &s.ControlPlaneURL,
			&s.Status, &s.RouterNodeID, &s.RouterContainerID, &s.RouterHostname,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("subnet: list scan: %w", err)
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// ListByStatus returns the subnet rows in the given
// status (e.g. "active" for the "active subnets" admin
// view, "pending" for the "needs attention" admin view).
// Empty status returns all rows (same as List).
func ListByStatus(d *sql.DB, status string) ([]*Subnet, error) {
	if status == "" {
		return List(d)
	}
	rows, err := d.Query(`
		SELECT id, user_id, cidr, subnet_bits, control_plane_url,
		       status, router_node_id, router_container_id,
		       router_hostname, created_at, updated_at
		  FROM user_subnets
		 WHERE status = ?
		 ORDER BY user_id ASC
	`, status)
	if err != nil {
		return nil, fmt.Errorf("subnet: list by status: %w", err)
	}
	defer rows.Close()
	var out []*Subnet
	for rows.Next() {
		var s Subnet
		if err := rows.Scan(&s.ID, &s.UserID, &s.CIDR, &s.SubnetBits, &s.ControlPlaneURL,
			&s.Status, &s.RouterNodeID, &s.RouterContainerID, &s.RouterHostname,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("subnet: list by status scan: %w", err)
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// SetStatus updates the lifecycle status of the
// user_subnets row and the denormalized portal_users
// columns. Called by:
//   - v0.16.1 sidecar provisioning: pending → active
//     when the sidecar registers + routes are approved
//   - v0.16.1 sidecar monitor: active → disabled on
//     unrecoverable failure
//   - /admin/users/{id}/subnet "Disable" button: any →
//     disabled (opt-out)
//
// 2026-07-17: v0.16.0. The v0.16.0 release has no
// sidecar code so the only caller is the admin
// "Disable" button (a manual opt-out).
func SetStatus(d *sql.DB, userID int64, status string) error {
	switch status {
	case StatusPending, StatusActive, StatusRouterActive, StatusDisabled:
		// ok
	default:
		return fmt.Errorf("subnet: invalid status %q", status)
	}
	now := time.Now().Unix()
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("subnet: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.Exec(`
		UPDATE user_subnets
		   SET status = ?, updated_at = ?
		 WHERE user_id = ?
	`, status, now, userID)
	if err != nil {
		return fmt.Errorf("subnet: update status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`
		UPDATE portal_users
		   SET subnet_status = ?
		 WHERE id = ?
	`, status, userID); err != nil {
		return fmt.Errorf("subnet: update portal_users: %w", err)
	}
	return tx.Commit()
}

// SetRouter updates the headscale node_id + container_id
// of the user_subnets row. Called by v0.16.1 after the
// sidecar registers with headscale and the operator (or
// the sidecar's auto-tag step) tags it tag:subnet-router.
//
// 2026-07-17: v0.16.0. Stub for the v0.16.1 work; the
// column exists so v0.16.1 can call it without a schema
// change.
func SetRouter(d *sql.DB, userID int64, routerNodeID, routerContainerID string) error {
	now := time.Now().Unix()
	res, err := d.Exec(`
		UPDATE user_subnets
		   SET router_node_id = ?,
		       router_container_id = ?,
		       updated_at = ?
		 WHERE user_id = ?
	`, routerNodeID, routerContainerID, now, userID)
	if err != nil {
		return fmt.Errorf("subnet: set router: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err := d.Exec(`
		UPDATE portal_users
		   SET subnet_router_node_id = ?
		 WHERE id = ?
	`, routerNodeID, userID); err != nil {
		return fmt.Errorf("subnet: update portal_users router: %w", err)
	}
	return nil
}

// SyncStatus computes the desired status for a user's
// subnet based on the current snapshot of their devices
// in node_owner_map, and updates user_subnets.status +
// the portal_users denorm column if the computed value
// differs from the current.
//
// 2026-07-21: v0.22.3 — the new status logic.
//
// The status decision matrix:
//
//	disabled            → disabled       (manual override wins)
//	0 devices           → pending        (row allocated, nothing to route)
//	≥1 device, no router → active        (logical namespace only)
//	≥1 device, + router → router_active  (real subnet-router up too)
//
// hasRouter is supplied by the caller based on a fresh
// headscale read. The handler code that calls SyncStatus
// (backfillNodeOwnership in handlers_node_ownership.go)
// already has the headscale node list, so passing the
// router presence is essentially free.
//
// SyncStatus is idempotent. Calling it twice in a row
// (with the same hasRouter value) is a no-op on the DB
// because SetStatus guards the no-op path internally
// (and the manual disabled case is a constant match).
//
// Errors:
//   - ErrNotFound if the user has no user_subnets row.
//     Callers (handlers) can detect this and skip the
//     sync — a user without a row isn't broken, they
//     just haven't clicked "Allocate subnet" yet.
//
// Audit / observability: returns the resulting status so
// the caller can log a one-liner like "subnet_sync
// user=42 status=active devices=3 router=false".
func SyncStatus(d *sql.DB, userID int64, hasRouter bool) (string, error) {
	sub, err := Get(d, userID)
	if err != nil {
		return "", err
	}
	if sub == nil {
		return "", ErrNotFound
	}
	// Manual disabled wins over derived state. The admin
	// clicked "Disable" for a reason; auto-sync must not
	// resurrect the row.
	if sub.Status == StatusDisabled {
		return StatusDisabled, nil
	}
	// Count the user's devices in node_owner_map. We
	// can't trust headscale's live state alone (it's
	// the source of truth for the tailnet, but
	// node_owner_map is the source of truth for
	// "what does skygate consider this user to own"
	// — which is what the status pill represents).
	// The backfill that just ran already populated
	// node_owner_map from headscale, so counting it
	// here is the right granularity.
	var username string
	if err := d.QueryRow(`SELECT username FROM portal_users WHERE id = ?`, userID).Scan(&username); err != nil {
		return "", fmt.Errorf("subnet: read username for user_id=%d: %w", userID, err)
	}
	var deviceCount int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM node_owner_map WHERE username = ?`, username,
	).Scan(&deviceCount); err != nil {
		return "", fmt.Errorf("subnet: count devices for user_id=%d: %w", userID, err)
	}
	var newStatus string
	switch {
	case deviceCount == 0:
		newStatus = StatusPending
	case hasRouter:
		newStatus = StatusRouterActive
	default:
		newStatus = StatusActive
	}
	if newStatus != sub.Status {
		if err := SetStatus(d, userID, newStatus); err != nil {
			return "", err
		}
	}
	return newStatus, nil
}
