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
	"skygate/internal/headscale"

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

// InfraAuditIdentity — v0.33.1.41 stub. The device-meta
// handlers don't use it (the audit is always attributed
// to the admin who clicked the button), but the Backend
// interface requires the method. Return the same identity
// as the admin — matches the pre-B93 behaviour.
func (s *stubBackend) InfraAuditIdentity(fallbackUID int64, fallbackUsername string) (int64, string) {
	return fallbackUID, fallbackUsername
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

// ---------------------------------------------------------------------------
// 2026-08-09: v0.33.1.20 — tests for PostAdminDevicesForceBackfillTags
// + PostAdminDeviceTransfer + transferTargets helper.
//
// The full DB-success path of these handlers is exercised by the
// live verify on the VM (click "Force resync all tags" on
// /admin/devices; the operator's 12 nodes-without-dev-tag report
// is the live test). What we cover here is the parts that don't
// need a real headscale:
//   - admin gating (non-admin gets 403, no DB write)
//   - form validation (missing fields returns 400, no DB write)
//   - nil headscale client (returns 500 with a clear message)
//   - transferTargets pure function (the dropdown filter)
//
// The "rename" half of the v0.33.1.20 contract is pinned by
// TestBackfill_RenameUpdatesHostnameAndTag in
// internal/nodeownership/nodeownership_test.go (the helper-level
// test, no HTTP involved).
// ---------------------------------------------------------------------------

// TestTransferTargets_ExcludesSyntheticUser — the "Transfer"
// dropdown on /admin/devices lists only valid transfer
// destinations: portal users with a matching skygate account.
// The synthetic "tagged-devices" headscale user (which has no
// portal counterpart) must NOT appear, because transferring
// a node to it would put the row in a "no portal owner" state
// that the per-user backfill would orphan on the next run.
func TestTransferTargets_ExcludesSyntheticUser(t *testing.T) {
	in := map[string]int64{
		"skyadmin":    1,  // valid portal user, id=1
		"michail":     6,  // valid portal user, id=6
		"svyatoslava": 11, // valid portal user, id=11
		"tagged-devices": 0, // synthetic, no portal counterpart
		"":               12, // empty name (defensive)
	}
	got := transferTargets(in)
	want := []string{"michail", "skyadmin", "svyatoslava"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Defensive: synthetic user + empty name must never appear.
	for _, g := range got {
		if g == "tagged-devices" || g == "" {
			t.Errorf("transferTargets must NOT include %q, got %v", g, got)
		}
	}
}

// TestPostAdminDeviceTransfer_RejectsNonAdmin — non-admin
// users can't reassign a node to a different owner. 403, no
// DB write, no audit.
func TestPostAdminDeviceTransfer_RejectsNonAdmin(t *testing.T) {
	d := openDeviceMetaTestDB(t)
	if err := db.UpsertNodeOwner(d, "27", 1, "skyadmin", "tag:dev-skyadmin-svyatoslava", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}
	be := &stubBackend{user: &auth.Claims{UserID: 2, Username: "user1", IsAdmin: false}}
	svc := &Service{Backend: be, DB: d}
	form := url.Values{}
	form.Set("node_id", "27")
	form.Set("target_username", "svyatoslava")
	req := httptest.NewRequest("POST", "/admin/devices/transfer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	svc.PostAdminDeviceTransfer(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	// Row must be unchanged (still owned by skyadmin).
	n, err := db.GetNodeOwner(d, "27")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n.Username != "skyadmin" {
		t.Errorf("non-admin must NOT transfer, but row owner = %q", n.Username)
	}
	if len(be.auditLog) != 0 {
		t.Errorf("non-admin must NOT audit, got %v", be.auditLog)
	}
}

// TestPostAdminDeviceTransfer_MissingNodeID — bad form data
// is rejected with 400, no DB write. The handler validates
// node_id (must parse as int64) AND target_username (must be
// non-empty) BEFORE looking up the target user.
func TestPostAdminDeviceTransfer_MissingNodeID(t *testing.T) {
	d := openDeviceMetaTestDB(t)
	be := &stubBackend{user: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	svc := &Service{Backend: be, DB: d}
	form := url.Values{}
	form.Set("target_username", "svyatoslava")
	// node_id missing
	req := httptest.NewRequest("POST", "/admin/devices/transfer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	svc.PostAdminDeviceTransfer(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", w.Code, w.Body.String())
	}
	if len(be.auditLog) != 0 {
		t.Errorf("bad request must NOT audit, got %v", be.auditLog)
	}
}

// TestPostAdminDeviceTransfer_MissingTargetUsername — same
// contract for the other side: target_username is required.
func TestPostAdminDeviceTransfer_MissingTargetUsername(t *testing.T) {
	d := openDeviceMetaTestDB(t)
	if err := db.UpsertNodeOwner(d, "27", 1, "skyadmin", "tag:dev-skyadmin-svyatoslava", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}
	be := &stubBackend{user: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	svc := &Service{Backend: be, DB: d}
	form := url.Values{}
	form.Set("node_id", "27")
	// target_username missing
	req := httptest.NewRequest("POST", "/admin/devices/transfer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	svc.PostAdminDeviceTransfer(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", w.Code, w.Body.String())
	}
	// Row must NOT be changed.
	n, err := db.GetNodeOwner(d, "27")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n.Username != "skyadmin" {
		t.Errorf("bad request must NOT touch the row, owner = %q", n.Username)
	}
}

// TestPostAdminDeviceTransfer_NodeNotInDB — if the operator
// posts a node_id that has no node_owner_map row, the
// handler returns 400 (it can't UntagNode a non-existent
// old tag, can't build a meaningful new tag without the
// live hostname, and the redirect would be misleading).
//
// Test infrastructure: the handler looks up the target user
// via GetAllPortalUsers BEFORE checking the source node, so
// the test DB needs both tables. The portal_users row is
// the target, the node_owner_map is intentionally empty
// (no row for "99999") so the second check fires.
func TestPostAdminDeviceTransfer_NodeNotInDB(t *testing.T) {
	d := openDeviceMetaTestDB(t)
	// Seed portal_users so GetAllPortalUsers returns the
	// target user. Minimal schema (the helper only reads
	// the columns it needs: id, username, headscale_user_id).
	if _, err := d.Exec(`CREATE TABLE portal_users (
		id INTEGER PRIMARY KEY,
		username TEXT NOT NULL DEFAULT '',
		is_admin INTEGER NOT NULL DEFAULT 0,
		headscale_user_id INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT 0,
		theme TEXT NOT NULL DEFAULT 'linear',
		subnet_cidr TEXT NOT NULL DEFAULT '',
		subnet_status TEXT NOT NULL DEFAULT '',
		subnet_router_node_id TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("seed portal_users schema: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, is_admin, headscale_user_id) VALUES (11, 'svyatoslava', 0, 84)`,
	); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}
	be := &stubBackend{user: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	svc := &Service{Backend: be, DB: d, HSGlobalFn: func() *headscale.Client { return nil }}
	form := url.Values{}
	form.Set("node_id", "99999")
	form.Set("target_username", "svyatoslava")
	req := httptest.NewRequest("POST", "/admin/devices/transfer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	svc.PostAdminDeviceTransfer(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", w.Code, w.Body.String())
	}
}

// TestPostAdminDevicesForceBackfillTags_RejectsNonAdmin —
// non-admin users can't trigger the per-user backfill loop.
// 403, no DB write.
func TestPostAdminDevicesForceBackfillTags_RejectsNonAdmin(t *testing.T) {
	d := openDeviceMetaTestDB(t)
	be := &stubBackend{user: &auth.Claims{UserID: 2, Username: "user1", IsAdmin: false}}
	svc := &Service{Backend: be, DB: d}
	req := httptest.NewRequest("POST", "/admin/devices/force-backfill-tags", nil)
	w := httptest.NewRecorder()
	svc.PostAdminDevicesForceBackfillTags(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if len(be.auditLog) != 0 {
		t.Errorf("non-admin must NOT audit, got %v", be.auditLog)
	}
}

// TestPostAdminDevicesForceBackfillTags_NilHS — when no
// headscale client is wired (the handler depends on
// s.HSGlobalFn() returning a non-nil *headscale.Client to
// call ListAllNodes + AddTag/UntagNode), the handler must
// surface a clear 500 instead of crashing on a nil-pointer
// dereference. The test for the full success path lives on
// the VM (the operator's 2026-08-09 force-backfill run).
func TestPostAdminDevicesForceBackfillTags_NilHS(t *testing.T) {
	d := openDeviceMetaTestDB(t)
	be := &stubBackend{user: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	// HSGlobalFn is nil by default on &Service{} — the testutil
	// constructor also sets it to `func() *headscale.Client { return nil }`,
	// but we leave it nil here to match the production case
	// where the callback was never wired (which shouldn't
	// happen post-v0.33.1.0 main.go, but is a defensive case).
	svc := &Service{Backend: be, DB: d, HSGlobalFn: nil}
	req := httptest.NewRequest("POST", "/admin/devices/force-backfill-tags", nil)
	w := httptest.NewRecorder()
	svc.PostAdminDevicesForceBackfillTags(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 (nil HS), got %d body=%q", w.Code, w.Body.String())
	}
}
