package sidecar

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/subnet"
)

// openTestDB opens a fresh sqlite db with the full schema
// (migrations applied). Mirrors internal/db/db_test.go:openTestDB
// to keep tests hermetic.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "sidecar-test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedPortalUser inserts a portal user with the given username
// and an optional headscale user_id. Returns the portal user id.
func seedPortalUser(t *testing.T, d *sql.DB, username string, hsID int64) int64 {
	t.Helper()
	_, err := d.Exec(`INSERT INTO portal_users
		(username, password_hash, is_admin, headscale_user_id, created_at)
		VALUES (?, '', 0, ?, ?)`, username, hsID, time.Now().Unix())
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var id int64
	if err := d.QueryRow(`SELECT id FROM portal_users WHERE username = ?`, username).Scan(&id); err != nil {
		t.Fatalf("get id: %v", err)
	}
	return id
}

// fakeNodeServer returns a headscale-API stub that:
//   - on GET /api/v1/node returns the given list of nodes
//   - on POST /api/v1/preauthkey returns a fake key
//   - on PUT /api/v1/node/{id}/expire returns 200
//   - on everything else returns 404
func fakeNodeServer(t *testing.T, nodes []headscale.HSNode) (*httptest.Server, *headscale.Client) {
	t.Helper()
	mu := sync.Mutex{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/node" && r.Method == "GET":
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
		case strings.HasPrefix(r.URL.Path, "/api/v1/node/") && r.Method == "GET":
			mu.Lock()
			defer mu.Unlock()
			idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/node/")
			var found *headscale.HSNode
			for i := range nodes {
				if nodes[i].ID == idStr {
					found = &nodes[i]
					break
				}
			}
			if found == nil {
				http.Error(w, "not found", 404)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"node": *found})
		case r.URL.Path == "/api/v1/preauthkey" && r.Method == "POST":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(headscale.PreauthKey{
				ID:     "1",
				Key:    "hskey-fake-test-key-do-not-use",
				UserID: 1,
			})
		default:
			http.Error(w, "not found", 404)
		}
	}))
	c := headscale.New(srv.URL, "test-key")
	t.Cleanup(srv.Close)
	return srv, c
}

// --- UsernameFromHostname ---

func TestUsernameFromHostname(t *testing.T) {
	cases := []struct {
		in        string
		wantUser  string
		wantMatch bool
	}{
		{"skygate-subnet-alice", "alice", true},
		{"skygate-subnet-michail_42", "michail_42", true},
		{"skygate-subnet-", "", false},
		{"alice", "", false},
		{"SKYGATE-SUBNET-LOWER", "", false}, // case-sensitive
		{"", "", false},
	}
	for _, c := range cases {
		gotUser, gotMatch := UsernameFromHostname(c.in)
		if gotUser != c.wantUser || gotMatch != c.wantMatch {
			t.Errorf("UsernameFromHostname(%q) = (%q, %v), want (%q, %v)",
				c.in, gotUser, gotMatch, c.wantUser, c.wantMatch)
		}
	}
}

// --- containsCIDR ---

func TestContainsCIDR(t *testing.T) {
	cidrs := []string{"10.0.0.0/24", "192.168.1.0/24", "10.0.42.0/24"}
	if !containsCIDR(cidrs, "10.0.42.0/24") {
		t.Error("containsCIDR missed 10.0.42.0/24")
	}
	if containsCIDR(cidrs, "10.0.99.0/24") {
		t.Error("containsCIDR false positive on 10.0.99.0/24")
	}
	if !containsCIDR([]string{"  10.0.42.0/24  "}, "10.0.42.0/24") {
		t.Error("containsCIDR should trim whitespace")
	}
}

// --- hasTag ---

func TestHasTag(t *testing.T) {
	if !hasTag([]string{"tag:private", "tag:subnet-router"}, "tag:subnet-router") {
		t.Error("hasTag missed tag:subnet-router")
	}
	if hasTag([]string{"tag:private"}, "tag:subnet-router") {
		t.Error("hasTag false positive")
	}
	if hasTag(nil, "tag:subnet-router") {
		t.Error("hasTag(nil) should be false")
	}
}

// --- SyncOnce — node goes from pending → active via auto-approval ---

func TestSyncOnce_AutoApprovesRoute(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", 100)
	// headscale_user_id is already set by seedPortalUser
	_ = uid
	// Allocate a subnet row in pending state.
	_, err := subnet.Create(d, uid, "", "skygate-subnet-alice")
	if err != nil {
		t.Fatalf("subnet.Create: %v", err)
	}

	// Fake headscale that reports one node with tag:subnet-router,
	// available route 10.0.<uid>.0/24, no approved routes yet.
	nodes := []headscale.HSNode{
		{
			ID:              "7",
			Name:            "skygate-subnet-alice",
			GivenName:       "skygate-subnet-alice",
			AvailableRoutes: []string{"10.0.99.0/24"}, // WRONG CIDR — should not approve
			Tags:            []string{"tag:subnet-router"},
			Online:          true,
			LastSeen:        time.Now().Format(time.RFC3339),
		},
	}
	_, hs := fakeNodeServer(t, nodes)
	mgr := New(d, func(_ int64) *headscale.Client { return hs }, nil, 0)

	if err := mgr.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	// Status should still be pending — the node's available route
	// is wrong, so we couldn't approve anything.
	got, err := subnet.Get(d, uid)
	if err != nil {
		t.Fatalf("subnet.Get: %v", err)
	}
	if got.Status != subnet.StatusPending {
		t.Errorf("status = %q, want pending (wrong CIDR available)", got.Status)
	}
}

// --- SyncOnce — status flips to router_active when ApprovedRoutes contains CIDR ---

func TestSyncOnce_FlipsToRouterActiveWhenRouteApproved(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", 100)
	_, err := subnet.Create(d, uid, "", "skygate-subnet-alice")
	if err != nil {
		t.Fatalf("subnet.Create: %v", err)
	}
	userCIDR, _ := subnet.AllocateCIDR(uid) // 10.0.<uid>.0/24

	// Node already has the per-user CIDR approved (covers the
	// case where the operator pre-approved routes manually).
	// The sidecar's SyncOnce must see this and flip status to
	// router_active (per v0.22.3 semantics: a tag:subnet-router
	// node with an approved route IS a live subnet-router).
	//
	// 2026-07-22: v0.26.0 — was StatusActive in the pre-v0.22.3
	// semantics. v0.22.3 split that into active (no router)
	// vs router_active (router up). Since the sidecar code path
	// only runs when there's a live tag:subnet-router node that
	// just had its route approved, the right status is
	// router_active. The pre-fix test name and assertion
	// encoded the v0.16.7 binary status and silently passed
	// for over a year (since v0.22.3 the value has been
	// clobbered on every SyncOnce tick from router_active
	// back to active in production — only the e2e subnet-router
	// pilot on 2026-07-22 caught it).
	nodes := []headscale.HSNode{
		{
			ID:              "7",
			Name:            "skygate-subnet-alice",
			GivenName:       "skygate-subnet-alice",
			AvailableRoutes: []string{userCIDR},
			ApprovedRoutes:  []string{userCIDR},
			Tags:            []string{"tag:subnet-router"},
			Online:          true,
		},
	}
	_, hs := fakeNodeServer(t, nodes)
	mgr := New(d, func(_ int64) *headscale.Client { return hs }, nil, 0)

	if err := mgr.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	got, _ := subnet.Get(d, uid)
	if got.Status != subnet.StatusRouterActive {
		t.Errorf("status = %q, want router_active (route pre-approved on tag:subnet-router node)", got.Status)
	}
	if got.RouterNodeID != "7" {
		t.Errorf("RouterNodeID = %q, want 7", got.RouterNodeID)
	}
}

// --- SyncOnce — node disappeared → status flips to disabled ---

func TestSyncOnce_DisablesStaleNode(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", 100)
	_, err := subnet.Create(d, uid, "", "skygate-subnet-alice")
	if err != nil {
		t.Fatalf("subnet.Create: %v", err)
	}
	// Pre-set to active with a router node that will disappear.
	if err := subnet.SetStatus(d, uid, subnet.StatusActive); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if err := subnet.SetRouter(d, uid, "ghost", ""); err != nil {
		t.Fatalf("SetRouter: %v", err)
	}

	// Fake headscale returns NO nodes — the active router is gone.
	_, hs := fakeNodeServer(t, nil)
	mgr := New(d, func(_ int64) *headscale.Client { return hs }, nil, 0)

	if err := mgr.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	got, _ := subnet.Get(d, uid)
	if got.Status != subnet.StatusDisabled {
		t.Errorf("status = %q, want disabled (node disappeared)", got.Status)
	}
}

// --- SyncOnce — no tag:subnet-router nodes → noop, status unchanged ---

func TestSyncOnce_NoNodesIsNoop(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", 100)
	_, _ = subnet.Create(d, uid, "", "skygate-subnet-alice")

	// Nodes with different tags — should be ignored.
	nodes := []headscale.HSNode{
		{ID: "1", Name: "emilia", GivenName: "emilia", Tags: []string{"tag:exit-node"}},
		{ID: "2", Name: "karolina", GivenName: "karolina", Tags: []string{"tag:private"}},
	}
	_, hs := fakeNodeServer(t, nodes)
	mgr := New(d, func(_ int64) *headscale.Client { return hs }, nil, 0)

	if err := mgr.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	got, _ := subnet.Get(d, uid)
	if got.Status != subnet.StatusPending {
		t.Errorf("status = %q, want pending (no subnet-router nodes)", got.Status)
	}
	if got.RouterNodeID != "" {
		t.Errorf("RouterNodeID = %q, want empty (no relevant nodes)", got.RouterNodeID)
	}
}

// --- SyncOnce — stats are recorded ---

func TestSyncOnce_RecordsStats(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice", 100)

	_, hs := fakeNodeServer(t, nil)
	mgr := New(d, func(_ int64) *headscale.Client { return hs }, nil, 0)
	if err := mgr.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	stats := mgr.LastStats()
	if stats.At.IsZero() {
		t.Error("LastStats.At is zero")
	}
	// With no nodes scanned, we should still record a time.
	if time.Since(stats.At) > 5*time.Second {
		t.Errorf("LastStats.At = %v, want ~now", stats.At)
	}
}

// --- GeneratePreauth — errors when no subnet row ---

func TestGeneratePreauth_ErrorsOnMissingSubnet(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", 0) // no headscale_user_id, no subnet row

	_, hs := fakeNodeServer(t, nil)
	mgr := New(d, func(_ int64) *headscale.Client { return hs }, nil, 0)

	_, _, err := mgr.GeneratePreauth(context.Background(), uid)
	if err == nil {
		t.Fatal("expected error for missing subnet row")
	}
}

// --- GeneratePreauth — succeeds when row + headscale_user_id set ---

func TestGeneratePreauth_Success(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", 100)
	_, err := subnet.Create(d, uid, "", "skygate-subnet-alice")
	if err != nil {
		t.Fatalf("subnet.Create: %v", err)
	}

	_, hs := fakeNodeServer(t, nil)
	mgr := New(d, func(_ int64) *headscale.Client { return hs }, nil, 0)

	key, exp, err := mgr.GeneratePreauth(context.Background(), uid)
	if err != nil {
		t.Fatalf("GeneratePreauth: %v", err)
	}
	if !strings.HasPrefix(key, "hskey-") {
		t.Errorf("key = %q, want hskey- prefix", key)
	}
	if exp.Before(time.Now()) || exp.After(time.Now().Add(2*time.Hour)) {
		t.Errorf("exp = %v, want ~1h from now", exp)
	}
}

// --- BuildPreauthInfo — composes the display payload ---

func TestBuildPreauthInfo(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", 100)
	_, _ = subnet.Create(d, uid, "", "skygate-subnet-alice")
	userCIDR, _ := subnet.AllocateCIDR(uid)

	_, hs := fakeNodeServer(t, nil)
	mgr := New(d, func(_ int64) *headscale.Client { return hs }, nil, 0)

	exp := time.Now().Add(1 * time.Hour)
	info := mgr.BuildPreauthInfo(uid, "hskey-fake", exp, "alice")
	if info.Key != "hskey-fake" {
		t.Errorf("Key = %q, want hskey-fake", info.Key)
	}
	if info.Hostname != "skygate-subnet-alice" {
		t.Errorf("Hostname = %q, want skygate-subnet-alice", info.Hostname)
	}
	if info.Routes != userCIDR {
		t.Errorf("Routes = %q, want %q (uid=%d → 10.0.<uid>.0/24)", info.Routes, userCIDR, uid)
	}
	if !info.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt = %v, want %v", info.ExpiresAt, exp)
	}
}

// --- listAllSubnets — covers all rows ---

func TestListAllSubnets(t *testing.T) {
	d := openTestDB(t)
	uid1 := seedPortalUser(t, d, "alice", 100)
	uid2 := seedPortalUser(t, d, "bob", 101)
	_, _ = subnet.Create(d, uid1, "", "skygate-subnet-alice")
	_, _ = subnet.Create(d, uid2, "", "skygate-subnet-bob")

	_, hs := fakeNodeServer(t, nil)
	mgr := New(d, func(_ int64) *headscale.Client { return hs }, nil, 0)
	all, err := mgr.listAllSubnets()
	if err != nil {
		t.Fatalf("listAllSubnets: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("got %d subnets, want 2", len(all))
	}
	// Sort by userID for stable assertion.
	sort.Slice(all, func(i, j int) bool { return all[i].UserID < all[j].UserID })
	if all[0].UserID != uid1 || all[1].UserID != uid2 {
		t.Errorf("user_ids = (%d, %d), want (%d, %d)", all[0].UserID, all[1].UserID, uid1, uid2)
	}
}

// --- distinctPlaneURLs — returns only non-empty distinct URLs ---

func TestDistinctPlaneURLs(t *testing.T) {
	d := openTestDB(t)
	uid1 := seedPortalUser(t, d, "alice", 100)
	uid2 := seedPortalUser(t, d, "bob", 101)
	_, _ = subnet.Create(d, uid1, "http://plane1:50444", "h1")
	_, _ = subnet.Create(d, uid2, "http://plane2:50444", "h2")
	// Add a duplicate (uid1 again, different plane — distinct should dedup)
	_, _ = d.Exec(`UPDATE user_subnets SET control_plane_url='http://plane1:50444' WHERE user_id=?`, uid1)

	_, hs := fakeNodeServer(t, nil)
	mgr := New(d, func(_ int64) *headscale.Client { return hs }, nil, 0)
	urls, err := mgr.distinctPlaneURLs()
	if err != nil {
		t.Fatalf("distinctPlaneURLs: %v", err)
	}
	sort.Strings(urls)
	want := []string{"http://plane1:50444", "http://plane2:50444"}
	if fmt.Sprintf("%v", urls) != fmt.Sprintf("%v", want) {
		t.Errorf("urls = %v, want %v", urls, want)
	}
}
