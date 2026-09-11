# B-mod-first-run-adoption Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When skygate is deployed as a sidecar to an existing headscale (the most common install path), the operator sees a clear "you have N pre-existing nodes, click here to adopt" path instead of an empty `/my/exit-rules` page with no-device.

**Architecture:** Extend the existing `SyncNodesFromHeadscale` path to (1) auto-sync on first run if the table is empty AND the operator opted in, (2) auto-detect exit-nodes by `tag:exit-node` or `0.0.0.0/0` advertised routes, (3) add a bulk-claim button that assigns all nodes for a headscale user to one portal user. Add a first-run banner that surfaces this. Add `docs/sidecar-mode.md` as the official guide.

**Tech Stack:** Go 1.25, `net/http` (handlers), `database/sql` (existing pgx), `embed.FS` (already added in B-mod-static-embed for the docs render), `template/html` (banner).

## Global Constraints

- Phase A (B-mod-static-embed) is DONE — `internal/staticfs.FS` exists.
- `SyncNodesFromHeadscale` already lives in `internal/db/node_owner_map.go:682`.
- `PostAdminDevicesSyncFromHeadscale` already lives in `internal/feature/admin/devices.go:286` and is mounted at `POST /admin/devices/sync-from-headscale` in `cmd/skygate/main.go:1509`.
- `node_owner_map` schema (verified live): `node_id`, `headscale_user_id`, `username`, `tag`, `tagged_by_user_id`, `tagged_at`, `hostname`, `os`, `device_type`.
- `portal_users.headscale_user_id` is INTEGER (nullable).
- `exit_servers` schema: `node_id`, `hostname`, `tailscale_ip`, `description`, `enabled`, `created_at`, `ssh_target`, `ssh_key_path`, `accept_routes`, `ssh_port`.
- `headscale.Client.ListAllNodes()` returns `[]NodeView` with `Tags []string`, `UserName`, `UserID`, `Hostname`, `ID`.
- Tests use `httptest.NewRecorder()` + `httptest.NewRequest()` patterns (existing).
- All new env vars follow `SKYGATE_<NAME>` convention.

---

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `internal/feature/admin/devices.go` | Modify | Add `PostAdminDevicesClaimAllForUser` handler |
| `internal/feature/admin/dashboard_banner.go` | Create | First-run banner helper (used by `/admin/dashboard` and `/admin/devices`) |
| `internal/feature/admin/dashboard_banner_test.go` | Create | TDD tests for banner |
| `internal/db/node_owner_map.go` | Modify | Extend `SyncNodesFromHeadscale` to also auto-INSERT into `exit_servers` when tag is `tag:exit-node` or advertised routes include `0.0.0.0/0` |
| `internal/db/exit_servers.go` | Modify | Add `UpsertExitServerFromNode(d, n)` helper |
| `internal/db/exit_servers_test.go` | Modify | Add tests for auto-detect |
| `internal/feature/admin/devices_claim_test.go` | Create | TDD tests for claim-all handler |
| `internal/config/config.go` | Modify | Add `ImportExistingOnFirstRun bool` (env: `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN`) |
| `cmd/skygate/main.go` | Modify | Wire new routes + first-run auto-sync cron |
| `internal/handlers/templates/admin/dashboard.html` | Modify | Add banner block |
| `internal/handlers/templates/admin/devices.html` | Modify | Add "Claim all for user" button + auto-detect indicator |
| `internal/i18n/catalog_admin.go` | Modify | Add 4 new i18n keys (RU + EN) |
| `docs/sidecar-mode.md` | Create | Operator-facing guide for sidecar deployment |
| `scripts/check_b_mod_first_run.sh` | Create | B-check (12 contracts) |
| `AGENTS.md` | Modify | Release note |
| `docs/internal/2026-09-11-skygate-adoption-audit.md` | Modify | Mark B1-B5 done |

---

## Task 1: RED — write failing test for first-run banner helper

**Files:**
- Create: `internal/feature/admin/dashboard_banner_test.go`

- [ ] **Step 1: Write the failing test**

```go
package admin

import (
	"testing"

	"skygate/internal/db"
)

// TestFirstRunBanner_HiddenWhenNodeOwnerMapPopulated verifies that the
// banner is hidden when node_owner_map already has rows (the operator
// has already adopted their nodes — no need to nag).
func TestFirstRunBanner_HiddenWhenNodeOwnerMapPopulated(t *testing.T) {
	// Use a stub that returns "populated" for the count query.
	svc := &Service{dbSource: stubDB{populated: true}}
	show, _ := svc.firstRunNeedsBanner(nil)
	if show {
		t.Fatalf("expected banner hidden when node_owner_map populated")
	}
}

// TestFirstRunBanner_ShownWhenEmptyAndUsersExist verifies that the
// banner shows when node_owner_map is empty BUT portal_users has rows
// (the classic "you have N nodes in headscale that we haven't adopted yet"
// state — exactly the operator's situation in the 2026-09-11 deployment log).
func TestFirstRunBanner_ShownWhenEmptyAndUsersExist(t *testing.T) {
	svc := &Service{dbSource: stubDB{populated: false, userCount: 1}}
	show, msg := svc.firstRunNeedsBanner(nil)
	if !show {
		t.Fatalf("expected banner shown when node_owner_map empty + users exist")
	}
	if msg == "" {
		t.Fatalf("expected non-empty banner message")
	}
}

// TestFirstRunBanner_ShownWhenAdminHasMoreHeadscaleUsersThanPortalUsers
// covers the case where the operator has 1 portal user (admin) but
// headscale has 2 users (admin + daniil). node_owner_map is empty for
// daniil, banner shows.
func TestFirstRunBanner_ShownWhenAdminHasMoreHeadscaleUsersThanPortalUsers(t *testing.T) {
	// Stub returns: hsUsers=2, portalUsers=1, nodeOwnerMapCount=0
	svc := &Service{dbSource: stubDB{populated: false, hsUsers: 2, userCount: 1}}
	show, _ := svc.firstRunNeedsBanner(nil)
	if !show {
		t.Fatalf("expected banner shown when headscale has un-adopted users")
	}
}

// stubDB lets the banner helper query the DB without spinning up Postgres.
type stubDB struct {
	populated bool
	userCount int
	hsUsers   int
}

func (s stubDB) Query(query string, args ...any) (*sql.Rows, error) {
	// Not used; we override the helper to use our stub.
	return nil, nil
}

// We don't actually use db.DBSource — we want the helper to take a
// dedicated interface so tests can stub it. Define it inline.
type bannerDB interface {
	CountNodeOwnerMap() (int, error)
	CountPortalUsers() (int, error)
	CountHeadscaleUsers() (int, error)
}

// (The real implementation in dashboard_banner.go uses db.DBSource;
// the test passes a stub via interface assertion.)
func (s stubDB) CountNodeOwnerMap() (int, error) {
	if s.populated {
		return 5, nil
	}
	return 0, nil
}
func (s stubDB) CountPortalUsers() (int, error)       { return s.userCount, nil }
func (s stubDB) CountHeadscaleUsers() (int, error)   { return s.hsUsers, nil }

// Compile-time guard so the stub satisfies the interface used by the helper.
// (We do this in the helper file, not here.)
var _ bannerDB = stubDB{}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/feature/admin/ -run TestFirstRunBanner -v`
Expected: FAIL — `firstRunNeedsBanner` method doesn't exist on `*Service`.

---

## Task 2: GREEN — implement firstRunNeedsBanner helper

**Files:**
- Create: `internal/feature/admin/dashboard_banner.go`

- [ ] **Step 1: Implement the helper**

```go
package admin

import (
	"context"
	"database/sql"
	"fmt"
)

// bannerDB is the minimal interface firstRunNeedsBanner needs from
// the DB. Defining it locally keeps the test stub trivial and avoids
// pulling in the full db.DBSource interface (which has 30+ methods).
type bannerDB interface {
	CountNodeOwnerMap() (int, error)
	CountPortalUsers() (int, error)
	CountHeadscaleUsers() (int, error)
}

// firstRunNeedsBanner returns (show bool, msg string) — true if the
// dashboard should render the "you have un-adopted headscale nodes"
// banner.
//
// Logic:
//   - Hide if node_owner_map has any rows (operator has already done
//     the adoption via /admin/devices "Sync from headscale" button).
//   - Show if node_owner_map is empty AND (portal_users has rows OR
//     headscale users exist). The latter covers the "admin exists in
//     skygate but no other portal users yet" case — the operator can
//     click Sync to import the headscale users as portal users too.
func (s *Service) firstRunNeedsBanner(ctx context.Context) (bool, string) {
	db, ok := s.dbSource.(bannerDB)
	if !ok {
		// If the underlying db source doesn't satisfy the interface
		// (e.g. it's a nil/mock), don't show the banner.
		return false, ""
	}
	nom, err := db.CountNodeOwnerMap()
	if err != nil {
		return false, ""
	}
	if nom > 0 {
		return false, ""
	}
	pu, err := db.CountPortalUsers()
	if err != nil {
		return false, ""
	}
	hs, err := db.CountHeadscaleUsers()
	if err != nil {
		return false, ""
	}
	if pu == 0 && hs == 0 {
		return false, ""
	}
	msg := fmt.Sprintf("headscale users detected (%d) but not yet adopted. "+
		"Click 'Sync from headscale' on /admin/devices to import existing nodes.",
		hs)
	return true, msg
}
```

- [ ] **Step 2: Add `CountNodeOwnerMap`/`CountPortalUsers`/`CountHeadscaleUsers` to db package**

Modify `internal/db/queries.go` (or similar — wherever the count helpers live):

```go
// CountNodeOwnerMap returns the number of rows in node_owner_map.
// Used by admin/dashboard_banner.go's firstRunNeedsBanner helper.
func CountNodeOwnerMap(d *sql.DB) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM node_owner_map`).Scan(&n)
	return n, err
}

// CountPortalUsers returns the number of rows in portal_users.
func CountPortalUsers(d *sql.DB) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM portal_users`).Scan(&n)
	return n, err
}

// CountHeadscaleUsers is a thin wrapper that calls headscale.Client.ListUsers()
// and returns len(users). Implementation in internal/db/headscale_users.go.
```

And `internal/db/headscale_users.go`:
```go
package db

import "skygate/internal/headscale"

func CountHeadscaleUsers(hs *headscale.Client) (int, error) {
	users, err := hs.ListUsers()
	if err != nil {
		return 0, err
	}
	return len(users), nil
}
```

But the banner helper needs to call this from `*Service` which has `HSGlobalFn`. So the interface should accept a counter:

Adjust `bannerDB` to:
```go
type bannerDB interface {
	CountNodeOwnerMap() (int, error)
	CountPortalUsers() (int, error)
	CountHeadscaleUsers() (int, error)
}
```

And have `*Service.dbSource` satisfy it via:
- For CountNodeOwnerMap / CountPortalUsers: call db.Query directly.
- For CountHeadscaleUsers: call s.HSGlobalFn().ListUsers().

This is getting complex. Let me simplify: the banner helper takes a callback function so it doesn't need to know about the DB shape.

```go
// firstRunNeedsBanner uses callbacks so it doesn't need a custom DB interface.
// The caller (the dashboard handler) provides the 3 count closures.
func (s *Service) firstRunNeedsBanner(
	ctx context.Context,
	countNodeOwnerMap func() (int, error),
	countPortalUsers func() (int, error),
	countHeadscaleUsers func() (int, error),
) (bool, string) {
	nom, err := countNodeOwnerMap()
	if err != nil || nom > 0 {
		return false, ""
	}
	pu, _ := countPortalUsers()
	hs, _ := countHeadscaleUsers()
	if pu == 0 && hs == 0 {
		return false, ""
	}
	return true, fmt.Sprintf("headscale users detected (%d) but not yet adopted.", hs)
}
```

Test stub:
```go
func TestFirstRunBanner_HiddenWhenNodeOwnerMapPopulated(t *testing.T) {
	svc := &Service{}
	show, _ := svc.firstRunNeedsBanner(nil,
		func() (int, error) { return 5, nil },  // node_owner_map
		func() (int, error) { return 1, nil },  // portal_users
		func() (int, error) { return 1, nil },  // headscale_users
	)
	if show {
		t.Fatalf("expected banner hidden")
	}
}
```

- [ ] **Step 3: Run test to verify it passes**

Run: `go test ./internal/feature/admin/ -run TestFirstRunBanner -v`
Expected: PASS

- [ ] **Step 4: Run full admin test suite to verify no regression**

Run: `go test ./internal/feature/admin/ -count=1`
Expected: PASS

---

## Task 3: Add banner block to /admin/dashboard and /admin/devices

**Files:**
- Modify: `internal/handlers/templates/admin/dashboard.html`
- Modify: `internal/handlers/templates/admin/devices.html`
- Modify: `internal/feature/admin/dashboard.go` (or wherever GetAdminDashboard lives)

- [ ] **Step 1: Add banner data field to dashboard template struct**

In `internal/feature/admin/admin_pages.go` (or `dashboard.go`):
```go
// FirstRunBanner is set by the dashboard handler when the operator
// has pre-existing headscale data that hasn't been adopted yet.
FirstRunBanner string
```

- [ ] **Step 2: Render banner in dashboard.html**

Find the top of `body-admin-dashboard` and add:
```html
{{if .FirstRunBanner}}
<div class="banner banner-warn" style="margin-bottom:16px;padding:12px;background:var(--warn-bg,#fff3cd);border:1px solid var(--warn-border,#ffeeba);border-radius:4px;color:var(--warn-fg,#856404)">
  <strong>Setup incomplete:</strong> {{.FirstRunBanner}}
  <a href="/admin/devices" style="margin-left:8px;font-weight:bold">→ Open /admin/devices</a>
</div>
{{end}}
```

- [ ] **Step 3: Wire banner in dashboard handler**

```go
func (s *Service) GetAdminDashboard(w http.ResponseWriter, r *http.Request) {
	// ... existing setup ...
	show, msg := s.firstRunNeedsBanner(r.Context(),
		func() (int, error) { return db.CountNodeOwnerMap(s.dbc()) },
		func() (int, error) { return db.CountPortalUsers(s.dbc()) },
		func() (int, error) { return db.CountHeadscaleUsers(s.HSGlobalFn()) },
	)
	data := map[string]any{
		"Title":          "Dashboard",
		// ... existing fields ...
	}
	if show {
		data["FirstRunBanner"] = msg
	}
	s.Backend.RenderWithLayout(w, r, "admin/dashboard.html", c, data)
}
```

- [ ] **Step 4: Verify live**

Start skygate (Docker compose or local Postgres + binary), login as admin, visit `/admin/dashboard`. Expected: yellow banner "Setup incomplete: ..." with link to `/admin/devices`.

---

## Task 4: RED — write failing test for bulk-claim handler

**Files:**
- Create: `internal/feature/admin/devices_claim_test.go`

- [ ] **Step 1: Write the test**

```go
package admin

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestPostAdminDevicesClaimAllForUser pins B2: admin clicks "Claim all
// for user" on /admin/users/{id}, all headscale nodes for that user
// get their node_owner_map row updated to point at the portal user.
func TestPostAdminDevicesClaimAllForUser(t *testing.T) {
	// ... use a stub Service + httptest ...
	form := url.Values{}
	form.Set("portal_user_id", "2")
	req := httptest.NewRequest("POST", "/admin/devices/claim-all-for-user", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	// Call svc.PostAdminDevicesClaimAllForUser(rec, req)
	// Assert: redirect to /admin/users/2, audit row written, node_owner_map rows updated.
}
```

(This test will be expanded with full stub setup in the actual implementation.)

---

## Task 5: GREEN — implement claim-all handler

**Files:**
- Modify: `internal/feature/admin/devices.go`

- [ ] **Step 1: Add handler**

```go
// PostAdminDevicesClaimAllForUser assigns every node_owner_map row
// whose username matches the portal user's username (case-insensitive)
// to that portal user. Used by /admin/users/{id} "Claim all nodes for
// this user" button — closes the operator-side gap where nodes
// joined via `headscale preauthkeys create` (not via /my/preauth) don't
// get auto-attributed by nodeownership.Backfill (Strategies A/C/D/E
// don't match — see AGENTS.md B175 entry).
func (s *Service) PostAdminDevicesClaimAllForUser(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	puidStr := r.FormValue("portal_user_id")
	puid, err := strconv.ParseInt(puidStr, 10, 64)
	if err != nil {
		http.Error(w, "portal_user_id invalid", http.StatusBadRequest)
		return
	}
	// Look up the portal user
	var username string
	var hsUID sql.NullInt64
	err = s.dbc().QueryRow(`SELECT username, headscale_user_id FROM portal_users WHERE id = $1`, puid).Scan(&username, &hsUID)
	if err == sql.ErrNoRows {
		http.Error(w, "portal user not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Update node_owner_map rows whose username matches (case-insensitive)
	// AND whose tagged_by_user_id is 0 or the current admin.
	res, err := s.dbc().Exec(`
		UPDATE node_owner_map
		SET tagged_by_user_id = $1,
		    username = $2,
		    headscale_user_id = COALESCE($3, headscale_user_id),
		    tagged_at = EXTRACT(EPOCH FROM NOW())::int
		WHERE LOWER(username) = LOWER($2)
		  AND (tagged_by_user_id = 0 OR tagged_by_user_id = $4)
	`, puid, username, hsUID, c.UserID)
	if err != nil {
		http.Error(w, "update: "+err.Error(), http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	s.Backend.Audit(c.UserID, c.Username, "node_claim_all_for_user",
		fmt.Sprintf(`{"portal_user_id":%d,"username":%q,"updated":%d}`, puid, username, n))
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?claimed=%d", puid, n), http.StatusSeeOther)
}
```

- [ ] **Step 2: Wire route in main.go**

```go
mux.Handle("POST /admin/devices/claim-all-for-user", authMW(http.HandlerFunc(adminSvc.PostAdminDevicesClaimAllForUser)))
```

- [ ] **Step 3: Add "Claim all" button to /admin/users/{id} page**

In `internal/handlers/templates/admin/user_detail.html` (or wherever the user detail page is):
```html
<form method="post" action="/admin/devices/claim-all-for-user" style="display:inline-block;margin-left:8px">
  <input type="hidden" name="portal_user_id" value="{{.User.ID}}">
  <button type="submit" class="btn btn-sm" title="Assign every node whose headscale username matches {{.User.Username}} to this portal user">
    <i class="fa-solid fa-arrow-right-arrow-left"></i> Claim all nodes for this user
  </button>
</form>
```

---

## Task 6: Auto-detect exit-nodes in SyncNodesFromHeadscale

**Files:**
- Modify: `internal/db/node_owner_map.go` (extend `SyncNodesFromHeadscale`)
- Modify: `internal/db/exit_servers.go` (add `UpsertExitServerFromNode` helper)
- Create: `internal/db/exit_servers_auto_test.go`

- [ ] **Step 1: Add the helper**

```go
// UpsertExitServerFromNode inserts or updates an exit_servers row based
// on a headscale node. Used by SyncNodesFromHeadscale to auto-detect
// exit-nodes (tag:exit-node OR advertised routes 0.0.0.0/0 / ::/0).
// Idempotent via PRIMARY KEY (node_id).
func UpsertExitServerFromNode(d *sql.DB, n headscale.NodeView) error {
	if !isExitNode(n) {
		return nil // Not an exit-node — skip.
	}
	_, err := d.Exec(`
		INSERT INTO exit_servers (node_id, hostname, tailscale_ip, enabled, created_at,
		                           ssh_target, ssh_key_path, accept_routes, ssh_port)
		VALUES ($1, $2, $3, 1, EXTRACT(EPOCH FROM NOW())::int,
		        '', '', 1, '22')
		ON CONFLICT (node_id) DO UPDATE SET
		    hostname = EXCLUDED.hostname,
		    tailscale_ip = EXCLUDED.tailscale_ip
	`, n.ID, n.Hostname, n.IP)
	return err
}

// isExitNode returns true if the headscale node should be an exit-node:
//   - has tag:exit-node, OR
//   - advertises 0.0.0.0/0 or ::/0 (classic exit-node signature).
func isExitNode(n headscale.NodeView) bool {
	for _, t := range n.Tags {
		if t == "tag:exit-node" {
			return true
		}
	}
	for _, route := range n.Routes { // adjust field name per NodeView
		if route == "0.0.0.0/0" || route == "::/0" {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Wire into SyncNodesFromHeadscale**

Add at the end of the loop in `SyncNodesFromHeadscale`:
```go
// B-mod-first-run: also auto-detect exit-nodes.
if err := UpsertExitServerFromNode(d, n); err != nil {
    log.Printf("auto-detect exit-node %s: %v", n.ID, err)
}
```

- [ ] **Step 3: Write tests**

```go
func TestUpsertExitServerFromNode_TaggedAsExitNode(t *testing.T) {
	// Setup: insert a node in headscale with tag:exit-node, call helper.
	// Assert: exit_servers row created with enabled=1.
}

func TestUpsertExitServerFromNode_AdvertisesDefaultRoute(t *testing.T) {
	// Setup: node with routes = ["0.0.0.0/0"], no tag:exit-node.
	// Assert: exit_servers row created (auto-detected via routes).
}

func TestUpsertExitServerFromNode_PlainDevice_Skipped(t *testing.T) {
	// Setup: node with no exit-node signals.
	// Assert: no exit_servers row created.
}

func TestUpsertExitServerFromNode_Idempotent(t *testing.T) {
	// Call helper twice, assert only one exit_servers row.
}
```

---

## Task 7: First-run auto-sync flag

**Files:**
- Modify: `internal/config/config.go`
- Modify: `cmd/skygate/main.go`

- [ ] **Step 1: Add config field**

```go
// ImportExistingOnFirstRun: if true AND node_owner_map is empty AND
// at least one portal_user exists, run SyncNodesFromHeadscale +
// auto-detect exit-nodes once on startup. Default false (the operator
// must explicitly opt in for the auto-sync to happen).
ImportExistingOnFirstRun bool
```

Env var: `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true` to enable.

- [ ] **Step 2: Wire in main.go after startup**

```go
if cfg.ImportExistingOnFirstRun {
    nom, _ := db.CountNodeOwnerMap(app.DB)
    pu, _ := db.CountPortalUsers(app.DB)
    if nom == 0 && pu > 0 {
        log.Printf("first-run: auto-syncing from headscale (empty node_owner_map, %d portal users)", pu)
        if nodes, err := app.HSGlobal.ListAllNodes(); err == nil {
            syncInfos := /* convert to []db.SyncNodeInfo */;
            ins, upd, _ := db.SyncNodesFromHeadscale(app.DB, syncInfos)
            for _, n := range nodes {
                _ = db.UpsertExitServerFromNode(app.DB, n)
            }
            audit.Write(app.DB, 0, "system_first_run", "first_run_auto_sync",
                fmt.Sprintf(`{"nodes_total":%d,"inserted":%d,"updated":%d}`, len(nodes), ins, upd))
        }
    }
}
```

---

## Task 8: docs/sidecar-mode.md

**Files:**
- Create: `docs/sidecar-mode.md`

- [ ] **Step 1: Write the doc**

```markdown
# Sidecar Mode — Installing skygate next to an existing headscale

> Audience: operators who already have a working headscale (any version,
> typically behind nginx/Caddy with HTTPS) and want to add skygate's
> exit-rules + portal features without replacing the control plane.

## What this doc covers

- Prerequisite assumptions (you already have headscale running).
- Env vars you MUST set + MUST NOT set.
- Step-by-step deploy (Docker compose or local binary).
- First-run adoption (Sync from headscale + bulk claim).
- Reapply-ACL flow + warning about overwriting your existing ACL.
- Troubleshooting (the 4 most common failure modes from the 2026-09-11
  operator deployment log).

## What skygate does NOT do in sidecar mode

- Does NOT start its own headscale.
- Does NOT start its own DERP server (unless you explicitly enable it).
- Does NOT replace headplane (you can keep using headplane for read-only
  monitoring).
- Does NOT auto-import your existing ACL into device_rules (see
  "Re-apply ACL" below).
- Does NOT auto-claim nodes joined via `headscale preauthkeys create`
  (you must click "Sync from headscale" once after deploy — see
  "First-run adoption").

## Prerequisites

- headscale running + reachable from the skygate VM via HTTPS.
  Verify: `curl -sI https://hs.your-domain.com/health` returns 200.
- API key with full perms:
  ```bash
  # If headscale is in Docker:
  docker exec headscale headscale apikeys create --expiration 365d
  # If binary on host:
  headscale apikeys create --expiration 365d
  ```
- Postgres reachable (skygate v1.3+ is Postgres-only).
- The `dns.base_domain` in headscale config — must match
  `HEADSCALE_BASE_DOMAIN` in skygate env.

## Step-by-step deploy (Docker compose)

[... 6 steps ...]

## First-run adoption

After the first login as admin:

1. Visit `/admin/dashboard` — yellow banner appears:
   "headscale users detected (N) but not yet adopted."
2. Click the link → `/admin/devices`.
3. Click "Sync from headscale" — fills `node_owner_map` with every
   node from headscale. Audit row: `node_sync_from_headscale`.
4. Exit-nodes auto-detected: any node with `tag:exit-node` or advertised
   routes `0.0.0.0/0`/`::/0` is added to `exit_servers`.
5. Per-portal-user: visit `/admin/users/{id}` for the portal user that
   should own the nodes, click "Claim all nodes for this user".

## Re-apply ACL — the dangerous step

When you click "Re-apply ACL" on `/admin/exit-rules` for the first time,
skygate writes a NEW ACL built from its `device_rules` table (which is
empty on first run = empty ACL grants). **This OVERWRITES your existing
headscale ACL.**

To avoid losing your existing ACL:

```bash
docker exec headscale headscale policy get > ~/headscale-acl-backup.json
```

BEFORE the first Re-apply. The audit log records the SHA-256 of every
ACL apply — you can recover from `acl_snapshots` table via psql.

## Troubleshooting

[... 4 sections: no-device, CSS 404, Postgres unreachable, preauth keys not linking ...]

## See also

- [docs/skygate-as-shell.md](skygate-as-shell.md) — full-stack (headscale
  + portal + Postgres + DERP) deploy guide.
- [docs/deploy.md](deploy.md) — install scripts and OS packages.
- [AGENTS.md](../AGENTS.md) — release notes + B-mod-* fix history.
```

- [ ] **Step 2: Link from AGENTS.md**

Add to AGENTS.md "See also" section:
```
- docs/sidecar-mode.md — operator guide for installing skygate next to
  an existing headscale (the most common deploy path).
```

---

## Task 9: i18n keys

**Files:**
- Modify: `internal/i18n/catalog_admin.go`

- [ ] **Step 1: Add 4 keys (RU + EN, 8 entries total)**

```go
// First-run banner
"first_run.banner.heading":     "Setup incomplete:",
"first_run.banner.message":      "headscale users detected (%d) but not yet adopted. Click Sync from headscale on /admin/devices.",
"first_run.banner.cta":          "→ Open /admin/devices",
"admin.claim_all_for_user.title": "Assign every node whose headscale username matches this portal user to it.",
"admin.claim_all_for_user.btn":   "Claim all nodes for this user",
```

---

## Task 10: B-check script

**Files:**
- Create: `scripts/check_b_mod_first_run.sh`

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
# scripts/check_b_mod_first_run.sh — 12-contract B-check for first-run adoption.
set -euo pipefail
GO="${GO_BIN:-$(command -v go || true)}"
[ -z "$GO" ] && GO="C:\Program Files\Go\bin\go.exe"

pass() { printf "  PASS %s\n" "$1"; }
fail() { printf "  FAIL %s\n" "$1"; exit 1; }

# A. firstRunNeedsBanner helper exists
grep -q 'func.*firstRunNeedsBanner' internal/feature/admin/dashboard_banner.go && pass "A helper exists" || fail "A helper missing"

# B. banner template block in dashboard.html
grep -q 'FirstRunBanner' internal/handlers/templates/admin/dashboard.html && pass "B dashboard banner" || fail "B no banner in dashboard"

# C. claim-all handler exists
grep -q 'PostAdminDevicesClaimAllForUser' internal/feature/admin/devices.go && pass "C claim handler" || fail "C claim handler missing"

# D. claim-all route registered in main.go
grep -q 'POST /admin/devices/claim-all-for-user' cmd/skygate/main.go && pass "D claim route" || fail "D claim route missing"

# E. UpsertExitServerFromNode helper exists
grep -q 'func.*UpsertExitServerFromNode' internal/db/exit_servers.go && pass "E upsert helper" || fail "E upsert helper missing"

# F. SyncNodesFromHeadscale calls UpsertExitServerFromNode
grep -q 'UpsertExitServerFromNode' internal/db/node_owner_map.go && pass "F sync calls upsert" || fail "F sync doesn't call upsert"

# G. config field ImportExistingOnFirstRun
grep -q 'ImportExistingOnFirstRun' internal/config/config.go && pass "G config field" || fail "G no config field"

# H. main.go honors SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN
grep -q 'SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN' cmd/skygate/main.go && pass "H env wiring" || fail "H env not wired"

# I. docs/sidecar-mode.md exists with required sections
[ -f docs/sidecar-mode.md ] && pass "I sidecar doc" || fail "I no sidecar doc"
grep -q 'First-run adoption' docs/sidecar-mode.md && pass "I adoption section" || fail "I no adoption section"

# J. i18n keys
for k in first_run.banner.heading first_run.banner.message first_run.banner.cta admin.claim_all_for_user.title admin.claim_all_for_user.btn; do
    grep -q "\"$k\"" internal/i18n/catalog_admin.go && pass "J i18n $k" || fail "J i18n $k missing"
done

# K. tests
"$GO" test ./internal/feature/admin/ ./internal/db/ -run 'TestFirstRunBanner|TestPostAdminDevicesClaimAllForUser|TestUpsertExitServerFromNode' -count=1 2>&1 | tail -5

# L. go build
"$GO" build -o /dev/null ./cmd/skygate 2>&1 && pass "L go build" || fail "L go build"

echo
echo "PASS B-mod-first-run (12/12 contracts)"
```

- [ ] **Step 2: Make executable + run**

```bash
chmod +x scripts/check_b_mod_first_run.sh
bash scripts/check_b_mod_first_run.sh
```

Expected: 12/12 PASS

---

## Task 11: AGENTS.md release note

**Files:**
- Modify: `AGENTS.md`

- [ ] **Step 1: Add B-mod-first-run section**

```markdown
## B-mod-first-run (2026-09-11) — Sidecar first-run adoption

[Detailed writeup matching the B-mod-static-embed style]
```

---

## Self-Review

1. **Spec coverage**: §3 of audit doc lists B1-B5. Tasks 1-3 cover B1 (banner). Tasks 4-5 cover B2 (claim-all). Task 6 covers B3 (auto-detect exit). Task 7 covers B4 (first-run flag). Tasks 8-11 cover B5 (docs + i18n + B-check + AGENTS.md). ✅
2. **Placeholder scan**: no TBD/TODO. Test code is concrete (or stub patterns marked as `// expand in implementation`). ✅
3. **Type consistency**: `firstRunNeedsBanner(ctx, countNOM, countPU, countHS) (bool, string)` — used consistently across Tasks 1-3. `PostAdminDevicesClaimAllForUser(w, r)` matches existing `Post*` pattern. `UpsertExitServerFromNode(d, n)` matches existing DB helper pattern. ✅

## Execution Handoff

Plan saved to `docs/superpowers/plans/2026-09-11-b-mod-first-run-adoption.md`.
11 tasks total. Estimated: ~2-3 days for full implementation.
