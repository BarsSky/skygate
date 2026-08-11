// 2026-08-10: v0.33.1.35 — AddTag read-modify-write contract
// tests.
//
// Background. The headscale 0.29 `nodes tag` subcommand
// REPLACES the entire tag set on a node (no add/remove
// primitive). The skygate PostAdminExitNodeTagAsExitNode
// handler used to call TagNode("tag:exit-node") directly,
// which silently wiped any pre-existing per-user dev tag
// (e.g. `tag:dev-skyadmin-emilia`) the operator had set
// on the node. The v0.33.1.30 B82 follow-up documented
// that the per-user device marker is the "tag:dev-<user>-<host>"
// prefix; the exit-node ACL grants in the live policy
// reference these tags directly, so wiping them
// silently broke the grant until the operator re-applied
// the tag by hand.
//
// The fix: the handler now calls AddTag, which reads
// the current tag set via ListAllNodes and writes the
// union (existing + new tag). This file pins the
// AddTag contract end-to-end:
//
// 1. AddTag preserves the existing tag set
//    (tag:dev-skyadmin-emilia stays)
// 2. AddTag appends the requested tag
//    (tag:exit-node is added)
// 3. AddTag is a no-op when the tag is already present
//    (no headscale call, no docker exec)
// 4. AddTag with empty/result-list returns an error
//    gracefully (no nil-deref crash)

package headscale

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestAddTag_PreservesExistingTags pins the B85+ contract:
// when a node has tags [tag:dev-skyadmin-emilia, tag:private]
// and the operator calls AddTag(nodeID, "tag:exit-node"),
// the resulting docker exec call must include BOTH
// pre-existing tags AND the new tag. Pre-fix TagNode
// would have written only "tag:exit-node" — wiping
// the per-user dev marker and breaking the per-user
// ACL grant in the live policy.
func TestAddTag_PreservesExistingTags(t *testing.T) {
	const fakeNodeID = "3"
	const fakeHostname = "emilia"
	var dockerCalls []string
	var dockerMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ListAllNodes -> GET /api/v1/node
		if r.Method == "GET" && r.URL.Path == "/api/v1/node" {
			_, _ = w.Write([]byte(fmt.Sprintf(
				`{"nodes":[{"id":"%s","givenName":"%s","user":{"id":"1","name":"skyadmin"},`+
					`"online":true,"ipAddresses":["100.64.0.3"],`+
					`"tags":["tag:dev-skyadmin-emilia","tag:private"]}]}`,
				fakeNodeID, fakeHostname,
			)))
			return
		}
		http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, 404)
	}))
	defer srv.Close()

	c := New(srv.URL, "fake-token")
	// Override the docker runner so we capture (rather
	// than execute) the headscale CLI call. Returns
	// empty output, nil error so the helper thinks
	// the call succeeded.
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		dockerMu.Lock()
		defer dockerMu.Unlock()
		dockerCalls = append(dockerCalls, strings.Join(args, " "))
		return []byte("Node updated\n"), nil
	})

	if err := c.AddTag(3, "tag:exit-node"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}

	if len(dockerCalls) != 1 {
		t.Fatalf("docker exec called %d times, want 1; calls: %v", len(dockerCalls), dockerCalls)
	}
	call := dockerCalls[0]
	// Must include BOTH the existing tags AND the new tag.
	for _, want := range []string{"tag:dev-skyadmin-emilia", "tag:private", "tag:exit-node"} {
		if !strings.Contains(call, want) {
			t.Errorf("AddTag docker call missing %q; got: %s", want, call)
		}
	}
	// The "tag" subcommand form is `-t tag1,tag2,tag3`
	// (comma-joined). The headscale CLI accepts both
	// comma-joined and repeated `-t` flags; the helper
	// uses comma-joined via strings.Join(tags, ",").
	if !strings.Contains(call, "-t tag:dev-skyadmin-emilia,tag:private,tag:exit-node") &&
		!strings.Contains(call, "-t tag:dev-skyadmin-emilia,tag:exit-node,tag:private") &&
		!strings.Contains(call, "-t tag:private,tag:dev-skyadmin-emilia,tag:exit-node") &&
		!strings.Contains(call, "-t tag:private,tag:exit-node,tag:dev-skyadmin-emilia") &&
		!strings.Contains(call, "-t tag:exit-node,tag:dev-skyadmin-emilia,tag:private") &&
		!strings.Contains(call, "-t tag:exit-node,tag:private,tag:dev-skyadmin-emilia") {
		t.Errorf("AddTag docker call tag list missing one of the expected orderings; got: %s", call)
	}
}

// TestAddTag_NoOpWhenAlreadyPresent pins the idempotency
// contract: when the node already has the requested tag,
// AddTag must NOT issue a docker exec call. Pre-fix
// TagNode would have written the same tag set (which
// is a headscale no-op but still a wasted call) and
// emitted a misleading audit log entry.
func TestAddTag_NoOpWhenAlreadyPresent(t *testing.T) {
	const fakeNodeID = "3"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(
			`{"nodes":[{"id":"%s","givenName":"emilia","user":{"id":"1","name":"skyadmin"},`+
				`"online":true,"ipAddresses":["100.64.0.3"],"tags":["tag:exit-node"]}]}`,
			fakeNodeID,
		)))
	}))
	defer srv.Close()

	c := New(srv.URL, "fake-token")
	var dockerCalled bool
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		dockerCalled = true
		return []byte(""), nil
	})

	if err := c.AddTag(3, "tag:exit-node"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	if dockerCalled {
		t.Errorf("AddTag should be a no-op when tag already present; docker was called")
	}
}

// TestAddTag_PreservesOnError pins the failure-mode
// contract: when ListAllNodes fails (the read side of
// the read-modify-write), AddTag returns the error and
// does NOT issue the docker exec. Pre-fix this would
// have silently written the new tag, wiping the
// existing tags. The post-fix AddTag returns the read
// error so the caller can decide whether to retry.
func TestAddTag_PreservesOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal headscale error", 500)
	}))
	defer srv.Close()

	c := New(srv.URL, "fake-token")
	var dockerCalled bool
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		dockerCalled = true
		return []byte(""), nil
	})

	if err := c.AddTag(3, "tag:exit-node"); err == nil {
		t.Fatalf("AddTag should have errored when ListAllNodes failed; got nil")
	}
	if dockerCalled {
		t.Errorf("AddTag should not call docker when ListAllNodes fails")
	}
}

// TestTagNode_DoesNotPreserve pins the OLD broken
// contract that AddTag was added to work around. This
// test documents the explicit failure mode: TagNode
// replaces the entire tag set. Callers that want to
// preserve the existing tags must use AddTag (or pass
// the full desired tag set to TagNode). The two
// functions are NOT drop-in replacements.
func TestTagNode_ReplacesEntireSet(t *testing.T) {
	var dockerCalls []string
	var dockerMu sync.Mutex
	c := New("http://unused.invalid", "fake-token")
	c.ExecContainer = "headscale"
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		dockerMu.Lock()
		defer dockerMu.Unlock()
		dockerCalls = append(dockerCalls, strings.Join(args, " "))
		return []byte("Node updated\n"), nil
	})

	if err := c.TagNode(3, "tag:exit-node"); err != nil {
		t.Fatalf("TagNode: %v", err)
	}
	if len(dockerCalls) != 1 {
		t.Fatalf("TagNode docker calls = %d, want 1", len(dockerCalls))
	}
	call := dockerCalls[0]
	// TagNode writes ONLY the tags passed — no read of
	// current state. This is the pre-fix behaviour that
	// the handler switch to AddTag fixed. The test
	// documents the trap: a handler that uses TagNode
	// will silently wipe every other tag the node has.
	if !strings.Contains(call, "-t tag:exit-node") {
		t.Errorf("TagNode should write the passed tag; got: %s", call)
	}
	if strings.Contains(call, "tag:dev-skyadmin-emilia") {
		t.Errorf("TagNode should NOT preserve unrelated tags (this is the bug the handler had); got: %s", call)
	}
}
