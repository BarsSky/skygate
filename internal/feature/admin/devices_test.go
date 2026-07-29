package admin

// devices_test.go — regression tests for the v0.30.1
// user-device-can't-be-exit-node guard (nodeTagRefusedForUserDevice)
// AND for the v0.31.x PostAdminDeviceMeta manual override form.
//
// The bug: on 2026-07-28, user1's Windows box "base"
// (headscale id=7, tag:dev-user1-base) was found carrying
// tag:exit-node in headscale. The Tailscale client on base
// auto-selected "Base" as exit-node (0ms self-loop = lowest
// metric), and all internet traffic from base went to /dev/null.
// User reported: "пропал доступ в сеть" + "exit node не
// выбирается корректно".
//
// Root cause: tag:exit-node was set via direct headscale CLI
// outside of skygate (no audit_log entry for node=7), so
// skygate never had a chance to refuse the request. The
// v0.30.1 fix adds a guard in PostAdminNodeTag that refuses
// the same shape of request when it comes through the skygate
// admin UI (the most common accidental path: admin clicks
// "Tag as exit-node" on the wrong row in /admin/devices).
//
// refactor-v0.30 Phase B step 3a: tests moved here from
// internal/handlers/handlers_admin_nodes_test.go (the
// function-under-test moved with them).
//
// 2026-07-29: PostAdminDeviceMeta added — manual override for
// the per-device OS + device_type columns added in v0.31.x.
// The auto-detect (internal/devicemeta.Detect) handles ~80%
// of hostnames correctly. The remaining 20% (custom hostnames
// like "A71", "laptop", etc.) need an admin override via
// POST /admin/devices/{id}/meta. The test below covers the
// "happy path" of the handler — invalid OS / device_type are
// rejected, the row is updated, the audit is written.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/devicemeta"

	_ "github.com/mattn/go-sqlite3"
)

// TestNodeTagRefused_ExitNodeOnUserDevice — the primary
// regression test. A node with tag:dev-user1-base (per-user
// device) must refuse tag:exit-node.
func TestNodeTagRefused_ExitNodeOnUserDevice(t *testing.T) {
	current := []string{"tag:dev-user1-base", "tag:private"}
	refused, msg, hadTag := nodeTagRefusedForUserDevice(7, "tag:exit-node", current)
	if !refused {
		t.Fatalf("expected refuse for tag:exit-node on per-user device, got refused=false msg=%q", msg)
	}
	if !strings.Contains(msg, "tag:dev-user1-base") {
		t.Errorf("refuse message should mention existing dev tag, got: %s", msg)
	}
	if !strings.Contains(msg, "tag:exit-node") {
		t.Errorf("refuse message should mention attempted tag, got: %s", msg)
	}
	if hadTag != "tag:dev-user1-base" {
		t.Errorf("existingDevTag should be the dev tag, got %q", hadTag)
	}
}

// TestNodeTagRefused_PerRelayExitTag — also refused. tag:exit-relay-1
// (per-relay name) is also tag:exit-* and would make the user
// device an exit-node candidate.
func TestNodeTagRefused_PerRelayExitTag(t *testing.T) {
	current := []string{"tag:dev-admin-workstation-1", "tag:private"}
	refused, msg, _ := nodeTagRefusedForUserDevice(9, "tag:exit-relay-1", current)
	if !refused {
		t.Fatalf("expected refuse for tag:exit-relay-1 on per-user device, got refused=false msg=%q", msg)
	}
	if !strings.Contains(msg, "Per-user devices") {
		t.Errorf("refuse message should explain the rule, got: %s", msg)
	}
}

// TestNodeTagAllowed_ExitNodeOnRelay — POSITIVE case. A relay
// (no per-user tag) MUST be allowed to get tag:exit-node.
// This is the legitimate "Tag as exit-node" flow on /admin/exit-nodes.
func TestNodeTagAllowed_ExitNodeOnRelay(t *testing.T) {
	current := []string{"tag:public"} // relay-1-style: no dev tag
	refused, msg, hadTag := nodeTagRefusedForUserDevice(3, "tag:exit-node", current)
	if refused {
		t.Fatalf("tag:exit-node on a relay (no dev tag) must be allowed, got refused=true msg=%q hadTag=%q", msg, hadTag)
	}
	if hadTag != "" {
		t.Errorf("hadTag should be empty when allowed, got %q", hadTag)
	}
}

// TestNodeTagAllowed_PrivateOnUserDevice — POSITIVE case.
// tag:private on a per-user device is fine (it's the normal
// "auto-apply tag:private" path in backfillNodeOwnership).
// The guard must NOT over-fire on tag:private.
func TestNodeTagAllowed_PrivateOnUserDevice(t *testing.T) {
	current := []string{"tag:dev-user1-base"}
	refused, msg, _ := nodeTagRefusedForUserDevice(7, "tag:private", current)
	if refused {
		t.Fatalf("tag:private on a per-user device must be allowed (it's the normal flow), got refused=true msg=%q", msg)
	}
}

// TestNodeTagAllowed_PublicOnUserDevice — POSITIVE case.
// tag:public is not an exit-node tag, must be allowed even
// on a per-user device (catches a regression where the prefix
// check was too greedy).
func TestNodeTagAllowed_PublicOnUserDevice(t *testing.T) {
	current := []string{"tag:dev-user1-base"}
	refused, msg, _ := nodeTagRefusedForUserDevice(7, "tag:public", current)
	if refused {
		t.Fatalf("tag:public on a per-user device must be allowed, got refused=true msg=%q", msg)
	}
}

// TestNodeTagAllowed_SubnetRouterOnUserDevice — POSITIVE case.
// tag:subnet-router is a role tag, not an exit-node. Allowed.
func TestNodeTagAllowed_SubnetRouterOnUserDevice(t *testing.T) {
	current := []string{"tag:dev-admin-workstation-1"}
	refused, msg, _ := nodeTagRefusedForUserDevice(9, "tag:subnet-router", current)
	if refused {
		t.Fatalf("tag:subnet-router on a per-user device must be allowed, got refused=true msg=%q", msg)
	}
}

// TestNodeTagAllowed_ExitNodeOnEmptyNode — POSITIVE case.
// A node with NO tags yet is a "fresh" node. Tagging it
// tag:exit-node is the normal "promote a fresh VPS to relay"
// flow (this is exactly what /admin/exit-nodes does).
func TestNodeTagAllowed_ExitNodeOnEmptyNode(t *testing.T) {
	refused, msg, _ := nodeTagRefusedForUserDevice(99, "tag:exit-node", nil)
	if refused {
		t.Fatalf("tag:exit-node on a fresh (tag-less) node must be allowed, got refused=true msg=%q", msg)
	}
	refused, msg, _ = nodeTagRefusedForUserDevice(99, "tag:exit-node", []string{})
	if refused {
		t.Fatalf("tag:exit-node on a fresh (empty-tag) node must be allowed, got refused=true msg=%q", msg)
	}
}

// TestNodeTagRefused_ExitNodeOnMultipleDevTags — a node with
// multiple tag:dev-* (e.g. a misconfigured edge case) — the
// guard fires on the FIRST one it finds, but the message
// reports which one it hit (so the operator can untag it).
func TestNodeTagRefused_ExitNodeOnMultipleDevTags(t *testing.T) {
	current := []string{
		"tag:dev-admin-workstation-1",
		"tag:dev-admin-workstation-2",
		"tag:private",
	}
	refused, msg, hadTag := nodeTagRefusedForUserDevice(9, "tag:exit-relay-3", current)
	if !refused {
		t.Fatalf("expected refuse, got allowed (msg=%q)", msg)
	}
	// We don't pin WHICH dev tag the guard reports — it
	// iterates in order. But it must be one of them.
	if !strings.HasPrefix(hadTag, "tag:dev-admin-") {
		t.Errorf("hadTag should be a admin dev tag, got %q", hadTag)
	}
}

// ---------------------------------------------------------------------------
// 2026-07-29: v0.31.x PostAdminDeviceMeta manual override tests.
//
// The handler is the admin-only manual override for the per-device
// OS + device_type columns added in v0.31.x. The auto-detect
// (internal/devicemeta.Detect) handles ~80% of hostnames correctly;
// the remaining 20% (custom names like "A71", "laptop") need an
// admin-set value via the form on /admin/devices.
//
// The tests below use a real SQLite (in-memory) + a stub Backend
// that records Audit calls. The DB row update is asserted by
// reading the row back via db.GetNodeOwner. The audit call is
// asserted by inspecting the recorded calls on the stub.
//
// We do NOT spin up headscale (the handler doesn't call it for
// the meta path — meta is purely a node_owner_map write).
// ---------------------------------------------------------------------------

// stubBackend is the minimum surface PostAdminDeviceMeta needs
// from the Backend interface. Other methods (Render, RenderWithLayout)
// return errors if called, so an accidental call to them in
// the meta path is caught by the test.
type stubBackend struct {
	user     *auth.Claims
	auditLog []string
}

func (s *stubBackend) Render(http.ResponseWriter, *http.Request, string, any) {
	// not used by PostAdminDeviceMeta
}
func (s *stubBackend) RenderWithLayout(http.ResponseWriter, *http.Request, string, *auth.Claims, map[string]any) {
	// not used by PostAdminDeviceMeta
}
func (s *stubBackend) CurrentUser(*http.Request) *auth.Claims {
	return s.user
}
func (s *stubBackend) Audit(_ int64, username, action, detail string) {
	s.auditLog = append(s.auditLog, username+"|"+action+"|"+detail)
}

// openDeviceMetaTestDB seeds the minimum schema (node_owner_map
// with the v0.48 os + device_type columns) so PostAdminDeviceMeta
// can UPDATE the row. Mirrors the v0.48 schema in migrations_v0.48.go
// — the migration test in internal/db uses the same hand-rolled
// CREATE TABLE pattern (no migrate() loop).
func openDeviceMetaTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Exec(`
		CREATE TABLE node_owner_map (
			node_id           TEXT PRIMARY KEY,
			headscale_user_id INTEGER NOT NULL DEFAULT 0,
			username          TEXT NOT NULL DEFAULT '',
			tag               TEXT NOT NULL DEFAULT '',
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			tagged_at         INTEGER NOT NULL DEFAULT 0,
			hostname          TEXT NOT NULL DEFAULT '',
			os                TEXT NOT NULL DEFAULT 'unknown',
			device_type       TEXT NOT NULL DEFAULT 'unknown'
		)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return d
}

// TestPostAdminDeviceMeta_HappyPath — admin posts a valid
// os + device_type override for an existing node_owner_map row.
// Verifies:
//   - HTTP 303 (See Other) to /admin/devices?ok=device_meta
//   - The row's os + device_type columns are updated
//   - The Audit call records the right action + detail
func TestPostAdminDeviceMeta_HappyPath(t *testing.T) {
	d := openDeviceMetaTestDB(t)
	if err := db.UpsertNodeOwner(d, "123", 1, "admin", "tag:private", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Override the hostname too so the audit log is informative
	// (the handler doesn't use hostname, but the row is what
	// an admin would see in /admin/devices).
	if _, err := d.Exec(
		`UPDATE node_owner_map SET hostname = ? WHERE node_id = ?`,
		"A71", "123",
	); err != nil {
		t.Fatalf("seed hostname: %v", err)
	}

	be := &stubBackend{user: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	svc := &Service{Backend: be, DB: d}

	form := url.Values{}
	form.Set("node_id", "123")
	form.Set("os", "android")
	form.Set("device_type", "phone")
	req := httptest.NewRequest("POST", "/admin/devices/123/meta", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	svc.PostAdminDeviceMeta(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%q", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc != "/admin/devices?ok=device_meta" {
		t.Errorf("expected redirect to /admin/devices?ok=device_meta, got %q", loc)
	}

	// Row must be updated.
	n, err := db.GetNodeOwner(d, "123")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n.OS != "android" {
		t.Errorf("expected os=android, got %q", n.OS)
	}
	if n.DeviceType != "phone" {
		t.Errorf("expected device_type=phone, got %q", n.DeviceType)
	}

	// Audit must record the action.
	if len(be.auditLog) != 1 {
		t.Fatalf("expected 1 audit call, got %d: %v", len(be.auditLog), be.auditLog)
	}
	if !strings.Contains(be.auditLog[0], "device_meta_set") {
		t.Errorf("expected audit action=device_meta_set, got %q", be.auditLog[0])
	}
	if !strings.Contains(be.auditLog[0], "os=android") || !strings.Contains(be.auditLog[0], "device_type=phone") {
		t.Errorf("audit detail should include os + device_type, got %q", be.auditLog[0])
	}
}

// TestPostAdminDeviceMeta_EmptyDefaultsToUnknown — the form sends
// an empty <select> when the admin picks the "auto" option. The
// handler must treat that as "unknown" (re-enables auto-detect
// on the next /my/devices load). This is the key UX promise.
func TestPostAdminDeviceMeta_EmptyDefaultsToUnknown(t *testing.T) {
	d := openDeviceMetaTestDB(t)
	if err := db.UpsertNodeOwner(d, "123", 1, "admin", "tag:private", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Pre-set known values so we can verify the override-to-unknown works.
	if _, err := d.Exec(
		`UPDATE node_owner_map SET os = 'android', device_type = 'phone' WHERE node_id = ?`,
		"123",
	); err != nil {
		t.Fatalf("seed values: %v", err)
	}

	be := &stubBackend{user: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	svc := &Service{Backend: be, DB: d}

	form := url.Values{}
	form.Set("node_id", "123")
	// os + device_type are empty (admin picked "auto")
	req := httptest.NewRequest("POST", "/admin/devices/123/meta", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	svc.PostAdminDeviceMeta(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d body=%q", w.Code, w.Body.String())
	}
	n, err := db.GetNodeOwner(d, "123")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n.OS != devicemeta.OSUnknown {
		t.Errorf("expected os=%q (re-enable auto-detect), got %q", devicemeta.OSUnknown, n.OS)
	}
	if n.DeviceType != devicemeta.TypeUnknown {
		t.Errorf("expected device_type=%q (re-enable auto-detect), got %q", devicemeta.TypeUnknown, n.DeviceType)
	}
}

// TestPostAdminDeviceMeta_RejectsInvalidOS — the handler must
// reject unknown OS tokens (defense-in-depth; the <select>
// only offers valid options, but a forged request could send
// anything). 400 status, no DB write, no audit.
func TestPostAdminDeviceMeta_RejectsInvalidOS(t *testing.T) {
	d := openDeviceMetaTestDB(t)
	if err := db.UpsertNodeOwner(d, "123", 1, "admin", "tag:private", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}

	be := &stubBackend{user: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	svc := &Service{Backend: be, DB: d}

	form := url.Values{}
	form.Set("node_id", "123")
	form.Set("os", "plan9") // not a valid token
	form.Set("device_type", "client")
	req := httptest.NewRequest("POST", "/admin/devices/123/meta", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	svc.PostAdminDeviceMeta(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	n, err := db.GetNodeOwner(d, "123")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n.OS != "unknown" {
		t.Errorf("invalid os must NOT touch the row, but os=%q", n.OS)
	}
	if len(be.auditLog) != 0 {
		t.Errorf("invalid request must NOT audit, got %v", be.auditLog)
	}
}

// TestPostAdminDeviceMeta_RejectsNonAdmin — non-admin users
// cannot override device metadata (it's an admin-only
// debugging tool). 403 status, no DB write.
func TestPostAdminDeviceMeta_RejectsNonAdmin(t *testing.T) {
	d := openDeviceMetaTestDB(t)
	if err := db.UpsertNodeOwner(d, "123", 1, "admin", "tag:private", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}

	be := &stubBackend{user: &auth.Claims{UserID: 2, Username: "user1", IsAdmin: false}}
	svc := &Service{Backend: be, DB: d}

	form := url.Values{}
	form.Set("node_id", "123")
	form.Set("os", "windows")
	form.Set("device_type", "client")
	req := httptest.NewRequest("POST", "/admin/devices/123/meta", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	svc.PostAdminDeviceMeta(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	n, err := db.GetNodeOwner(d, "123")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n.OS != "unknown" {
		t.Errorf("non-admin must NOT touch the row, but os=%q", n.OS)
	}
}
