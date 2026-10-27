// B431 — «опять не видит новое обновление страница обновлений».
//
// Measured on the operator's live install 2026-10-27: the running build was v1.5.178, the
// shared checker had cached v1.5.178 at 14:46, the next release (v1.5.179) was published at
// 15:51, and the page still answered «последняя: v1.5.178» at 16:10 and 16:20 — while
// «Проверить сейчас» rendered the same cached page, because the handler called the cached
// render path. These tests pin the three halves of the fix:
//
//  1. the success cache is short enough that a release cannot hide for hours;
//  2. CheckFresh bypasses BOTH windows and seeds the cache for the following render;
//  3. Check/CheckFresh are safe to call concurrently on the process-wide instance.
package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// b431Server is a GitHub stand-in whose tag and status the test can change mid-run, so
// "a release was published after the cache was filled" is expressible without sleeping.
type b431Server struct {
	mu     sync.Mutex
	hits   int
	tag    string
	status int
	srv    *httptest.Server
}

func newB431Server(t *testing.T, tag string) *b431Server {
	t.Helper()
	s := &b431Server{tag: tag, status: http.StatusOK}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits++
		tag, status := s.tag, s.status
		s.mu.Unlock()
		w.Header().Set("X-RateLimit-Remaining", "59")
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(ghRelease{
			TagName:     tag,
			Name:        tag,
			HTMLURL:     "https://github.com/BarsSky/skygate/releases/tag/" + tag,
			PublishedAt: time.Now().Add(-time.Hour),
		})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *b431Server) set(tag string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tag = tag
	s.status = status
}

func (s *b431Server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func (s *b431Server) checker() *Checker {
	return &Checker{
		HTTPClient:     &http.Client{Transport: rewriteTransport{target: s.srv.URL}},
		Owner:          "BarsSky",
		Repo:           "skygate",
		Channel:        "stable",
		CurrentVersion: "v1.5.178",
	}
}

// TestB431_SuccessCacheIsShortEnough: the window is the product decision, so it is pinned.
// 15 minutes is the ceiling — a release that hides longer than that is the report this
// block answers.
func TestB431_SuccessCacheIsShortEnough(t *testing.T) {
	if checkSuccessCache <= 0 {
		t.Fatalf("checkSuccessCache = %v; a non-positive window would disable the cache entirely", checkSuccessCache)
	}
	if checkSuccessCache > 15*time.Minute {
		t.Fatalf("checkSuccessCache = %v, want <= 15m — at 6h the page hid a published release for hours (measured 2026-10-27)", checkSuccessCache)
	}
}

// TestB431_FreshCheckBypassesTheSuccessCache reproduces the live sequence: cache filled,
// a new release published, a cached page still stale, then the operator presses
// «Проверить сейчас».
func TestB431_FreshCheckBypassesTheSuccessCache(t *testing.T) {
	srv := newB431Server(t, "v1.5.178")
	c := srv.checker()
	ctx := context.Background()

	first, _ := c.Check(ctx)
	if first == nil || first.Latest != "v1.5.178" {
		t.Fatalf("first Check: got %+v, want Latest=v1.5.178", first)
	}
	if srv.count() != 1 {
		t.Fatalf("first Check made %d request(s), want 1", srv.count())
	}

	// Cached: no second request, same answer.
	cached, _ := c.Check(ctx)
	if srv.count() != 1 {
		t.Fatalf("a warm cache made another request (%d); the cache is the quota guard", srv.count())
	}
	if cached.Latest != "v1.5.178" {
		t.Fatalf("cached Latest = %q, want v1.5.178", cached.Latest)
	}

	// The release lands. A cached Check is blind to it — this is the reported symptom,
	// not a test artefact.
	srv.set("v1.5.179", http.StatusOK)
	stale, _ := c.Check(ctx)
	if srv.count() != 1 {
		t.Fatalf("a cached Check hit GitHub (%d requests) — the test no longer models the bug", srv.count())
	}
	if stale.Latest != "v1.5.178" {
		t.Fatalf("cached Check Latest = %q, want the stale v1.5.178 (else the cache window is gone)", stale.Latest)
	}

	// «Проверить сейчас».
	fresh, _ := c.CheckFresh(ctx)
	if srv.count() != 2 {
		t.Fatalf("CheckFresh made no request (%d total) — the button is still a no-op", srv.count())
	}
	if fresh == nil || fresh.Latest != "v1.5.179" {
		t.Fatalf("CheckFresh: got %+v, want Latest=v1.5.179", fresh)
	}
	if fresh.Error != "" {
		t.Fatalf("CheckFresh reported Error = %q on a healthy server", fresh.Error)
	}

	// The page rendered right after the click must be fresh, not the value it replaced.
	after, _ := c.Check(ctx)
	if srv.count() != 2 {
		t.Fatalf("the render after CheckFresh made another request (%d) — CheckFresh must seed the cache", srv.count())
	}
	if after.Latest != "v1.5.179" {
		t.Fatalf("render after CheckFresh shows %q, want v1.5.179 — the fresh answer was not cached", after.Latest)
	}
}

// TestB431_FreshCheckOverridesTheFailureBackoff: an operator asking explicitly must reach
// GitHub even while the failure window is open, otherwise a single blip freezes the button
// for 15 minutes.
func TestB431_FreshCheckOverridesTheFailureBackoff(t *testing.T) {
	srv := newB431Server(t, "v1.5.179")
	srv.set("v1.5.179", http.StatusInternalServerError)
	c := srv.checker()
	ctx := context.Background()

	failed, _ := c.Check(ctx)
	if failed == nil || failed.Error == "" {
		t.Fatalf("Check against a 500 returned %+v, want a Result with Error set", failed)
	}
	if srv.count() != 1 {
		t.Fatalf("first Check made %d request(s), want 1", srv.count())
	}
	backoff, _ := c.Check(ctx)
	if srv.count() != 1 {
		t.Fatalf("the backoff window made another request (%d); a failing GitHub would be hammered", srv.count())
	}
	if backoff == nil || backoff.Error == "" {
		t.Fatalf("backoff Result = %+v, want Error set", backoff)
	}

	srv.set("v1.5.179", http.StatusOK)
	fresh, _ := c.CheckFresh(ctx)
	if srv.count() != 2 {
		t.Fatalf("CheckFresh stayed inside the failure backoff (%d requests) — the button cannot recover from a blip", srv.count())
	}
	if fresh == nil || fresh.Latest != "v1.5.179" || fresh.Error != "" {
		t.Fatalf("CheckFresh after recovery = %+v, want Latest=v1.5.179 and no Error", fresh)
	}

	// And the recovery is cached, so the next render is cheap.
	if again, _ := c.Check(ctx); srv.count() != 2 || again.Latest != "v1.5.179" {
		t.Fatalf("post-recovery Check: requests=%d Latest=%q, want 2 and v1.5.179", srv.count(), again.Latest)
	}
}

// TestB431_ConcurrentCheckAndFreshAreSafe: the checker is process-wide since B424, so a
// page render and a «Проверить сейчас» click really do overlap. Run with -race (CI does):
// this is the test that fails if the mutex the type doc promises is ever removed.
func TestB431_ConcurrentCheckAndFreshAreSafe(t *testing.T) {
	srv := newB431Server(t, "v1.5.179")
	c := srv.checker()

	var wg sync.WaitGroup
	var nils atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				var (
					res *Result
					ctx = context.Background()
				)
				if (i+j)%2 == 0 {
					res, _ = c.Check(ctx)
				} else {
					res, _ = c.CheckFresh(ctx)
				}
				if res == nil {
					nils.Add(1)
				}
			}
		}(i)
	}
	wg.Wait()
	if got := nils.Load(); got != 0 {
		t.Fatalf("%d of 80 concurrent calls returned a nil Result", got)
	}
	// The mutex serialises requests, so the count must be well under one per call.
	if got := srv.count(); got == 0 || got > 80 {
		t.Fatalf("%d requests for 80 calls — the cache/mutex is not doing its job", got)
	}
}
