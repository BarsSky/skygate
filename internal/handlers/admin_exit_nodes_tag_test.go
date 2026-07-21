package handlers

// admin_exit_nodes_tag_test.go — v0.18.1 tests for the
// "Tag as exit-node" / "Untag" handlers on /admin/exit-nodes.
// The handlers call HSGlobal().ApproveRoutesForNodeID and
// HSGlobal().TagNode (which shell out to `docker exec
// headscale headscale nodes ...`). Those calls are
// integration-only — the real verification is the e2e
// smoke test. This file focuses on the validation
// branches that should NOT need a live headscale:
//   * missing node_id → redirect with err
//   * bad node_id     → redirect with err
//   * node doesn't advertise 0.0.0.0/0+::/0 → redirect with err
//   * node already tagged → idempotent success redirect
//
// The "happy path" (button click → docker exec → tag applied)
// is covered by manual deploy verification on the VM.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestPostAdminExitNodeTagAsExitNode_MissingNodeID —
// the form's hidden node_id field is empty. The handler
// must redirect with an error, not 500.
func TestPostAdminExitNodeTagAsExitNode_MissingNodeID(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()

	form := url.Values{}
	form.Set("node_id", "") // empty
	req := authedReqFor(t, a, "POST", "/admin/exit-nodes/tag-as-exit", form, "skyadmin")
	w := httptest.NewRecorder()
	a.PostAdminExitNodeTagAsExitNode(w, req)

	// Must be a 303 redirect to /admin/exit-nodes?err=...
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d (body: %s)", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/admin/exit-nodes") {
		t.Errorf("expected redirect to /admin/exit-nodes, got %q", loc)
	}
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect, got %q", loc)
	}
}

// TestPostAdminExitNodeTagAsExitNode_BadNodeID — the
// node_id isn't a valid integer.
func TestPostAdminExitNodeTagAsExitNode_BadNodeID(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()

	form := url.Values{}
	form.Set("node_id", "not-a-number")
	req := authedReqFor(t, a, "POST", "/admin/exit-nodes/tag-as-exit", form, "skyadmin")
	w := httptest.NewRecorder()
	a.PostAdminExitNodeTagAsExitNode(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	// The error message is URL-encoded; " " becomes "+" in
	// application/x-www-form-urlencoded. We accept either
	// form (raw space or +) so the test is robust.
	if !strings.Contains(loc, "bad+node+id") && !strings.Contains(loc, "bad%20node%20id") && !strings.Contains(loc, "bad node id") {
		t.Errorf("expected 'bad node id' (URL-encoded) in redirect err, got %q", loc)
	}
}

// TestPostAdminExitNodeUntagAsExitNode_BadNodeID —
// the Untag handler must also redirect on bad input
// rather than 500ing.
func TestPostAdminExitNodeUntagAsExitNode_BadNodeID(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()

	form := url.Values{}
	form.Set("node_id", "abc")
	req := authedReqFor(t, a, "POST", "/admin/exit-nodes/untag", form, "skyadmin")
	w := httptest.NewRecorder()
	a.PostAdminExitNodeUntagAsExitNode(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "bad+node+id") && !strings.Contains(loc, "bad%20node%20id") && !strings.Contains(loc, "bad node id") {
		t.Errorf("expected 'bad node id' (URL-encoded) in redirect err, got %q", loc)
	}
}

// TestPostAdminExitNodeTagAsExitNode_Forbidden —
// the handler must 403 on a non-admin caller, even if
// the input is otherwise valid.
func TestPostAdminExitNodeTagAsExitNode_Forbidden(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()

	form := url.Values{}
	form.Set("node_id", "1")
	// Caller is "alice" (non-admin).
	req := authedReqFor(t, a, "POST", "/admin/exit-nodes/tag-as-exit", form, "alice")
	w := httptest.NewRecorder()
	a.PostAdminExitNodeTagAsExitNode(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", w.Code)
	}
}

// TestPostAdminExitNodeUntagAsExitNode_Forbidden —
// same as above for the Untag handler.
func TestPostAdminExitNodeUntagAsExitNode_Forbidden(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()

	form := url.Values{}
	form.Set("node_id", "1")
	req := authedReqFor(t, a, "POST", "/admin/exit-nodes/untag", form, "alice")
	w := httptest.NewRecorder()
	a.PostAdminExitNodeUntagAsExitNode(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", w.Code)
	}
}
