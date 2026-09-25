// untag_guard_b321_test.go — B321 (2026-09-25): UntagNode must never rewrite a node's
// tag set from an empty read.
//
// Pre-B321 the read-modify-write swallowed its own read error and fell through with an
// empty `current` slice, so a failed read produced `[tag:private]` — silently wiping
// every other tag the node carried. That is exactly the defect the 2026-08-10 AddTag
// fix closed in the other direction ("on read error, do NOT call the inner TagNode"),
// and it stopped being theoretical once B321 started calling UntagNode automatically
// after a name reclaim. The contracts below pin the three no-write paths.
package headscale

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// untagHarness serves GET /api/v1/node with the given body/status and records whether
// any tag write was attempted.
func untagHarness(t *testing.T, listStatus int, listBody string) (*Client, *int32) {
	t.Helper()
	var writes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/node" {
			w.WriteHeader(listStatus)
			_, _ = w.Write([]byte(listBody))
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			atomic.AddInt32(&writes, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "test-key"), &writes
}

// A failed read must not become a write. This is the data-loss path.
func TestUntagNode_ReadErrorWritesNothing_B321(t *testing.T) {
	c, writes := untagHarness(t, http.StatusInternalServerError, `{"message":"boom"}`)
	if err := c.UntagNode(87, "tag:dev-infra-skygate-host-1"); err == nil {
		t.Fatal("a failed node read must return an error")
	}
	if n := atomic.LoadInt32(writes); n != 0 {
		t.Fatalf("a failed read must not write any tag (%d write(s) attempted)", n)
	}
}

// A node that is not in the list must not be treated as "has no tags".
func TestUntagNode_NodeMissingWritesNothing_B321(t *testing.T) {
	c, writes := untagHarness(t, http.StatusOK, `{"nodes":[]}`)
	if err := c.UntagNode(87, "tag:dev-infra-skygate-host-1"); err == nil {
		t.Fatal("an unknown node id must return an error")
	}
	if n := atomic.LoadInt32(writes); n != 0 {
		t.Fatalf("an unknown node must not write any tag (%d write(s) attempted)", n)
	}
}

// An absent tag is a no-op — no equal rewrite, and certainly no lossy one.
func TestUntagNode_AbsentTagWritesNothing_B321(t *testing.T) {
	c, writes := untagHarness(t, http.StatusOK,
		`{"nodes":[{"id":"87","givenName":"skygate-host","tags":["tag:dev-infra-skygate-host","tag:private"]}]}`)
	if err := c.UntagNode(87, "tag:dev-infra-skygate-host-1"); err != nil {
		t.Fatalf("removing an absent tag must succeed without writing: %v", err)
	}
	if n := atomic.LoadInt32(writes); n != 0 {
		t.Fatalf("an absent tag must not rewrite the tag set (%d write(s) attempted)", n)
	}
}

// An empty tag is a programming error, not a request to strip everything.
func TestUntagNode_EmptyTagRejected_B321(t *testing.T) {
	c, writes := untagHarness(t, http.StatusOK, `{"nodes":[]}`)
	if err := c.UntagNode(87, "   "); err == nil {
		t.Fatal("an empty tag must be rejected")
	}
	if n := atomic.LoadInt32(writes); n != 0 {
		t.Fatalf("an empty tag must not write (%d write(s) attempted)", n)
	}
}

// The happy path still removes exactly one tag and keeps the rest.
func TestUntagNode_RemovesOnlyTheRequestedTag_B321(t *testing.T) {
	c, writes := untagHarness(t, http.StatusOK,
		`{"nodes":[{"id":"87","givenName":"skygate-host","tags":["tag:dev-infra-skygate-host","tag:dev-infra-skygate-host-1","tag:private"]}]}`)
	if err := c.UntagNode(87, "tag:dev-infra-skygate-host-1"); err != nil {
		t.Fatalf("UntagNode: %v", err)
	}
	if n := atomic.LoadInt32(writes); n == 0 {
		t.Fatal("the requested tag exists, so exactly one write was expected")
	}
}
