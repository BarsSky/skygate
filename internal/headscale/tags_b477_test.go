// tags_b477_test.go — B477 (2026-11-15).
//
// A TAG WRITE IS A READ-MODIFY-WRITE OF THE WHOLE SET, SO IT MAY NOT READ A CACHE.
//
// Measured live on the migration host: node `SkyBars` re-authenticated through
// OIDC, headscale dropped ALL its tags (that part is headscale's behaviour), and
// then skygate's own two writers raced:
//
//	18:15:08 infra-backfill: AddTag(node 55, tag:private)
//	18:15:10 tag-reconcile:  applied tag:dev-skyadmin-skybars … "headscale had []"
//	final tags on the node:  [tag:dev-skyadmin-skybars]     ← tag:private gone
//
// Both writers read headscale's tag list through the 5-second ListAllNodes cache,
// so the second one wrote the set it had read BEFORE the first write — and
// headscale's tag API REPLACES the whole set. For 2.5 minutes the node matched no
// per-device ACL rule and its access looked like "the migration broke the tailnet".
//
// The fix is not "add another tag call": it is (a) read FRESH inside the mutation,
// (b) hold a client-wide lock across the read and the write, (c) drop the read
// cache after a successful write so page renderers see reality. Each test below
// fails on the pre-B477 tree.
package headscale

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// tagFake is a stateful headscale: GET /api/v1/node returns the CURRENT tags and
// POST /api/v1/node/{id}/tags REPLACES them, exactly like the real API.
type tagFake struct {
	mu     sync.Mutex
	tags   map[string][]string
	writes [][]string // every tag set received, in order
	delay  time.Duration
}

func newTagFake(initial map[string][]string) (*tagFake, *httptest.Server) {
	f := &tagFake{tags: map[string][]string{}}
	for k, v := range initial {
		f.tags[k] = append([]string{}, v...)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.delay > 0 && r.Method == http.MethodGet {
			time.Sleep(f.delay)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/node":
			type node struct {
				ID   string   `json:"id"`
				Name string   `json:"name"`
				Tags []string `json:"tags"`
			}
			out := struct {
				Nodes []node `json:"nodes"`
			}{}
			ids := make([]string, 0, len(f.tags))
			for id := range f.tags {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				out.Nodes = append(out.Nodes, node{ID: id, Name: id, Tags: f.tags[id]})
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tags"):
			var body struct {
				Tags []string `json:"tags"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/node/"), "/tags")
			f.tags[id] = append([]string{}, body.Tags...)
			f.writes = append(f.writes, append([]string{}, body.Tags...))
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return f, srv
}

func (f *tagFake) tagsOf(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.tags[id]...)
}

func (f *tagFake) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func hasTagIn(list []string, want string) bool {
	for _, t := range list {
		if t == want {
			return true
		}
	}
	return false
}

// TestB477_SecondWriterKeepsTheFirstWritersTag is the live failure, reproduced:
// the backfill adds tag:private, the reconciler adds the dev tag, and the node
// must end up with BOTH. Pre-B477 the reconciler read the 5-second-old list and
// wrote `[dev]`, erasing tag:private.
func TestB477_SecondWriterKeepsTheFirstWritersTag(t *testing.T) {
	f, srv := newTagFake(map[string][]string{"55": {}})
	defer srv.Close()
	c := New(srv.URL, "k")

	if err := c.AddTag(55, TagPrivateTag); err != nil {
		t.Fatalf("first AddTag: %v", err)
	}
	if err := c.AddTag(55, "tag:dev-skyadmin-skybars"); err != nil {
		t.Fatalf("second AddTag: %v", err)
	}
	got := f.tagsOf("55")
	for _, want := range []string{TagPrivateTag, "tag:dev-skyadmin-skybars"} {
		if !hasTagIn(got, want) {
			t.Fatalf("final tags = %v, want both %q and the dev tag — the second writer read a stale "+
				"snapshot and headscale REPLACED the set (B477)\nwrites: %v", got, want, f.writes)
		}
	}
}

// TestB477_UnknownExistingTagSurvivesTheWrite: the tag the database never knew
// about (tag:private, applied by an operator or an older flow) must survive a
// dev-tag write. This is the exact live state: the DB row named only the dev tag.
func TestB477_UnknownExistingTagSurvivesTheWrite(t *testing.T) {
	f, srv := newTagFake(map[string][]string{"55": {TagPrivateTag}})
	defer srv.Close()
	c := New(srv.URL, "k")

	if err := c.AddTag(55, "tag:dev-skyadmin-skybars"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	if got := f.tagsOf("55"); !hasTagIn(got, TagPrivateTag) {
		t.Fatalf("final tags = %v — a tag headscale already carried was dropped (B477)", got)
	}
}

// TestB477_ConcurrentWritersDoNotLoseEachOther: both writers run at the same
// moment (the reconciler and the infra backfill are separate goroutines), so the
// mutation must be serialised, not just fresh. Repeated because the pre-fix
// failure is a race.
func TestB477_ConcurrentWritersDoNotLoseEachOther(t *testing.T) {
	for round := 0; round < 5; round++ {
		f, srv := newTagFake(map[string][]string{"55": {}})
		f.delay = 5 * time.Millisecond // widen the window a cache reader would lose in
		c := New(srv.URL, "k")

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = c.AddTag(55, TagPrivateTag) }()
		go func() { defer wg.Done(); _ = c.AddTag(55, "tag:dev-skyadmin-skybars") }()
		wg.Wait()

		got := f.tagsOf("55")
		if !hasTagIn(got, TagPrivateTag) || !hasTagIn(got, "tag:dev-skyadmin-skybars") {
			t.Fatalf("round %d: concurrent writes left %v — one writer erased the other (B477)\nwrites: %v",
				round, got, f.writes)
		}
		srv.Close()
	}
}

// TestB477_UntagKeepsSiblings: removing one tag rewrites the rest, and must not
// resurrect a tag a concurrent writer had just removed (or drop a sibling).
func TestB477_UntagKeepsSiblings(t *testing.T) {
	f, srv := newTagFake(map[string][]string{"55": {"tag:dev-skyadmin-skybars", TagPrivateTag}})
	defer srv.Close()
	c := New(srv.URL, "k")

	if err := c.UntagNode(55, TagPrivateTag); err != nil {
		t.Fatalf("UntagNode: %v", err)
	}
	got := f.tagsOf("55")
	if hasTagIn(got, TagPrivateTag) {
		t.Fatalf("final tags = %v, want tag:private removed", got)
	}
	if !hasTagIn(got, "tag:dev-skyadmin-skybars") {
		t.Fatalf("final tags = %v, the sibling tag was dropped (B477)", got)
	}
}

// TestB477_WriteIsVisibleToTheNextPageRender: page renderers go through the
// cached ListAllNodes, so a successful write has to invalidate it — otherwise the
// operator sees the pre-write state for up to a cache TTL and re-applies it.
func TestB477_WriteIsVisibleToTheNextPageRender(t *testing.T) {
	f, srv := newTagFake(map[string][]string{"55": {}})
	defer srv.Close()
	c := New(srv.URL, "k")

	// Prime the read cache (a page render) before the write.
	if _, err := c.ListAllNodes(); err != nil {
		t.Fatalf("ListAllNodes: %v", err)
	}
	if err := c.AddTag(55, "tag:dev-skyadmin-skybars"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	nodes, err := c.ListAllNodes() // served from the cache unless the write cleared it
	if err != nil {
		t.Fatalf("ListAllNodes after write: %v", err)
	}
	for _, n := range nodes {
		if n.ID != "55" {
			continue
		}
		if !hasTagIn(n.Tags, "tag:dev-skyadmin-skybars") {
			t.Fatalf("cached read after a write shows %v — the write did not invalidate the cache (B477)", n.Tags)
		}
		return
	}
	t.Fatalf("node 55 missing from ListAllNodes; fake state %v", f.tagsOf("55"))
}

// TestB477_AddTagOnAMissingNodeDoesNotWriteNothingSilently: the read must find the
// node; if headscale does not list it, AddTag must still add the tag rather than
// skip the write (a node can be registered but filtered out of a list response).
// This pins the "empty current list" behaviour that B321/B477 depend on.
func TestB477_AddTagWritesWhenTheNodeIsAbsentFromTheList(t *testing.T) {
	f, srv := newTagFake(map[string][]string{}) // no nodes listed at all
	defer srv.Close()
	c := New(srv.URL, "k")

	if err := c.AddTag(55, "tag:dev-skyadmin-skybars"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	if got := f.tagsOf("55"); len(got) != 1 || got[0] != "tag:dev-skyadmin-skybars" {
		t.Fatalf("final tags = %v, want exactly the requested tag (writes: %v)", got, f.writes)
	}
	if f.writeCount() != 1 {
		t.Fatalf("writes = %d, want exactly 1", f.writeCount())
	}
	_ = fmt.Sprint()
}
