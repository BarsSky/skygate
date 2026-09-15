// internal/headscale/users_cache_b247_test.go — B247 cache-invalidation tests.
//
// B247 (2026-09-15): CreateUser's fallback path (when the POST returned
// 200/201 with empty `id`) used to call ListUsers — which returned the
// STALE cache from before the POST, missing the just-created user. The
// fallback's `for ... if users[i].Name == name` loop never matched, and
// cmd/skygate/main.go's ensureInfraUser logged "create returned empty
// ID and 'infra' not in headscale user list (response shape may have
// changed)" — but the user was actually created successfully.
//
// The fix is two-pronged:
//   1. CreateUser invalidates the user cache on success (so the next
//      ListUsers sees the new user).
//   2. CreateUser's fallback path uses ListUsersFresh (which bypasses
//      the cache and forces a fresh GET /api/v1/user).
//
// These tests pin both behaviours via an httptest server that records
// the number of GET /api/v1/user calls so we can prove the cache hit
// was bypassed.
package headscale

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUserHS responds to GET/POST /api/v1/user. The user list is
// mutable (initial + after-Create state) so tests can simulate the
// "POST created a user, but the cache didn't pick it up" scenario.
// hits counts GET /api/v1/user calls so the test can assert the
// cache was bypassed / re-fetched.
func fakeUserHS(t *testing.T, initialUsers []HSUser) (*httptest.Server, *Client, *int32, *[]HSUser, func(name string)) {
	t.Helper()
	var hits int32
	state := append([]HSUser(nil), initialUsers...)
	var mu = make(chan struct{}, 1)
	mu <- struct{}{} // binary semaphore as a mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/user":
			if r.Method == http.MethodGet {
				atomic.AddInt32(&hits, 1)
				<-mu
				out := state
				mu <- struct{}{}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"users":`))
				writeUsersJSON(w, out)
				_, _ = w.Write([]byte(`}`))
				return
			}
			if r.Method == http.MethodPost {
				// Return 200 with an EMPTY id (the 0.29.x
				// inconsistency the fallback path triggers on).
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"","name":"newone","createdAt":""}`))
				return
			}
		}
		http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	addFn := func(name string) {
		<-mu
		state = append(state, HSUser{ID: "999", Name: name})
		mu <- struct{}{}
	}
	return srv, New(srv.URL, "fake-key"), &hits, &state, addFn
}

// writeUsersJSON writes a JSON array of HSUser without importing
// encoding/json at the test file level (keeps the helper terse).
func writeUsersJSON(w http.ResponseWriter, users []HSUser) {
	_, _ = w.Write([]byte(`[`))
	for i, u := range users {
		if i > 0 {
			_, _ = w.Write([]byte(`,`))
		}
		_, _ = w.Write([]byte(`{"id":"`))
		_, _ = w.Write([]byte(u.ID))
		_, _ = w.Write([]byte(`","name":"`))
		_, _ = w.Write([]byte(u.Name))
		_, _ = w.Write([]byte(`","createdAt":""}`))
	}
	_, _ = w.Write([]byte(`]`))
}

// TestCreateUser_CacheInvalidationOnSuccess pins behaviour (1):
// after a successful CreateUser (POST returned a non-empty ID), the
// next ListUsers MUST NOT serve the cache — the cache must have been
// invalidated. Pre-B247 the cache stayed warm until cacheTTL, and a
// subsequent ListUsers on the /admin/users page would show the
// stale list missing the newly-created user.
func TestCreateUser_CacheInvalidationOnSuccess(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/user":
			if r.Method == http.MethodGet {
				atomic.AddInt32(&hits, 1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"users":[{"id":"1","name":"alice","createdAt":""}]}`))
				return
			}
			if r.Method == http.MethodPost {
				atomic.AddInt32(&hits, 1) // count POST too
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"42","name":"bob","createdAt":""}`))
				return
			}
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "fake-key")
	c.cacheTTL = time.Hour // long enough that pre-B247 stale-cache would mask the change

	// 1. Prime the cache.
	if _, err := c.ListUsers(); err != nil {
		t.Fatalf("ListUsers prime: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("after prime: hits = %d, want 1", got)
	}

	// 2. CreateUser — POST returns id="42" (success path).
	u, err := c.CreateUser("bob")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u == nil || u.ID != "42" {
		t.Fatalf("CreateUser returned %v, want id=42", u)
	}

	// 3. ListUsers post-create — cache should be invalidated so this
	// is a fresh API call. Pre-B247: cacheTTL=1h + no invalidation
	// means this would serve the stale empty list and the new "bob"
	// user wouldn't be visible to the next /admin/users page render.
	if _, err := c.ListUsers(); err != nil {
		t.Fatalf("ListUsers post-create: %v", err)
	}
	// hits should be 3 (1 GET prime + 1 POST + 1 GET post-create).
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("after ListUsers post-create: hits = %d, want 3 (cache invalidated → fresh GET)", got)
	}
}

// TestCreateUser_ListUsersFreshOnFallback pins behaviour (2): when
// the POST returns 200 with EMPTY id (the headscale 0.29.x
// inconsistency), CreateUser's fallback must bypass the stale cache
// via ListUsersFresh — otherwise it returns "user not found" even
// though the user was just created.
//
// Pre-B247: ListUsers returned the stale cache (without the new
// user), and the fallback loop's `for ... if users[i].Name == name`
// never matched → ensureInfraUser logged "response shape may have
// changed" warning.
//
// Post-B247: ListUsersFresh invalidates cache → fresh API call →
// new user visible → fallback loop matches → user returned with
// correct id.
func TestCreateUser_ListUsersFreshOnFallback(t *testing.T) {
	// Track GET /api/v1/user calls separately from POST calls so
	// we can prove the fallback issued a FRESH GET (cache bypass).
	var getHits, postHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/user":
			if r.Method == http.MethodGet {
				atomic.AddInt32(&getHits, 1)
				// Each GET returns the user list with
				// "infra" — but only on the SECOND+ call.
				// The first GET returns empty (simulating the
				// cached state from BEFORE the POST created
				// "infra"). This proves the fallback MUST hit
				// the API again to find the user.
				//
				// Actually: simpler — every GET returns the
				// user "infra" present. Pre-B247 the cache
				// would mask this; post-B247 we invalidate
				// before each fallback GET, so the GET fires
				// every time and the user is found.
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"users":[{"id":"99","name":"infra","createdAt":""}]}`))
				return
			}
			if r.Method == http.MethodPost {
				atomic.AddInt32(&postHits, 1)
				// Empty id — forces fallback path.
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"","name":"infra","createdAt":""}`))
				return
			}
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "fake-key")
	c.cacheTTL = time.Hour

	// Prime the cache with a ListUsers call that does NOT see
	// "infra" (simulating the operator who called hs.ListUsers()
	// in cmd/skygate/main.go:3536 BEFORE ensureInfraUser called
	// CreateUser).
	//
	// In our mock, every GET returns the user — but the cache
	// doesn't know about the user we'll POST below. The point of
	// this test is to prove the FALLBACK issues a fresh GET even
	// if the cache was warm BEFORE the POST. We simulate the warm
	// cache by manually seeding c.cacheUsers with an empty list:
	c.cacheMu.Lock()
	c.cacheUsers = []HSUser{} // empty (the user "infra" is NOT here yet)
	c.cacheUsersAt = time.Now()
	c.cacheMu.Unlock()

	// CreateUser — POST returns empty id → fallback path.
	u, err := c.CreateUser("infra")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u == nil {
		t.Fatal("CreateUser returned nil user")
	}
	if u.ID != "99" {
		t.Errorf("CreateUser returned id=%q, want 99 (fallback should find user in fresh GET)", u.ID)
	}
	if u.Name != "infra" {
		t.Errorf("CreateUser returned name=%q, want infra", u.Name)
	}

	// Critical: the fallback MUST have issued at least one GET
	// (ListUsersFresh invalidates cache then calls ListUsers which
	// fires GET because cache is now nil). Pre-B247 the cache was
	// NOT invalidated, so ListUsers would return the stale empty
	// list and the fallback loop would not match.
	if got := atomic.LoadInt32(&getHits); got < 1 {
		t.Errorf("fallback GET hits = %d, want >=1 (cache bypass)", got)
	}
	if got := atomic.LoadInt32(&postHits); got != 1 {
		t.Errorf("POST hits = %d, want exactly 1", got)
	}
}

// TestListUsersFresh_BypassesStaleCache pins the ListUsersFresh
// helper itself. Without invalidation, the second ListUsers call
// returns cached data; with ListUsersFresh, the cache is cleared
// first so the second call MUST hit the API.
func TestListUsersFresh_BypassesStaleCache(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"users":[{"id":"1","name":"alice","createdAt":""}]}`))
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "fake-key")
	c.cacheTTL = time.Hour // long enough that pre-B247 stale-cache would mask the change

	// First ListUsers → cache miss → hits = 1.
	if _, err := c.ListUsers(); err != nil {
		t.Fatalf("ListUsers #1: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("after ListUsers #1: hits = %d, want 1", got)
	}

	// Second ListUsers → cache hit (no new API call).
	if _, err := c.ListUsers(); err != nil {
		t.Fatalf("ListUsers #2: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("after ListUsers #2: hits = %d, want still 1 (cache hit)", got)
	}

	// ListUsersFresh → cache invalidated → fresh API call → hits = 2.
	if _, err := c.ListUsersFresh(); err != nil {
		t.Fatalf("ListUsersFresh: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("after ListUsersFresh: hits = %d, want 2 (cache bypass)", got)
	}

	// ListUsers after fresh → cache hit again (hits stays at 2).
	if _, err := c.ListUsers(); err != nil {
		t.Fatalf("ListUsers #3: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("after ListUsers #3: hits = %d, want still 2 (new cache hit)", got)
	}
}

// TestInvalidateUsersCache_Simple is a sanity test that the helper
// does what it claims — clears cacheUsers + cacheUsersAt.
func TestInvalidateUsersCache_Simple(t *testing.T) {
	_, c, _, _, _ := fakeUserHS(t, nil)
	c.cacheMu.Lock()
	c.cacheUsers = []HSUser{{ID: "1", Name: "x"}}
	c.cacheUsersAt = time.Now()
	c.cacheMu.Unlock()
	c.InvalidateUsersCache()
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()
	if c.cacheUsers != nil {
		t.Errorf("cacheUsers not cleared: %v", c.cacheUsers)
	}
	if !c.cacheUsersAt.IsZero() {
		t.Errorf("cacheUsersAt not zero: %v", c.cacheUsersAt)
	}
}
