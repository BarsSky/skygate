// exit_nodes_tag.go — tag / untag a node as an exit node.
//
// Split out of exit_nodes.go in refactor Phase D (2026-10-01): the
// accept-routes form parser plus the two B87 tag handlers, which share the
// "refuse a per-user device, then re-apply the ACL" shape.

package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/headscale"
)

// parseAcceptRoutesFormValue converts the form "state" string
// to the -1/0/1 int the column + headscale SetAdvertisedRoutes
// expect. Returns a friendly error for unknown values so the
// handler can render a "bad value" flash without 500ing.
//
//	"1"   →  1  (true)
//	"0"   →  0  (default / unset)
//	"-1"  → -1  (false)
//
// Whitespace trimmed. Anything else → error.
func parseAcceptRoutesFormValue(s string) (int, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "1":
		return 1, nil
	case "0":
		return 0, nil
	case "-1":
		return -1, nil
	default:
		return 0, fmt.Errorf("state must be 1, 0, or -1 (got %q)", s)
	}
}

// PostAdminExitNodeTagAsExitNode is the v0.18.1 "Tag as
// exit-node" button on /admin/exit-nodes. It replaces the
// operator's two manual `docker exec headscale headscale
// nodes ...` invocations with a single click:
//
//  1. Approves the exit-node bases (0.0.0.0/0, ::/0) on
//     the headscale side via the CLI. We approve ONLY the
//     two base routes, not the full availableRoutes set
//     (relay-3 has 200+ subnets that the operator does
//     NOT want auto-approved).
//  2. Tags the node with `tag:exit-node`. The ACL
//     already includes `* → tag:exit-node:*` so the new
//     node immediately starts accepting tailnet traffic.
//
// Both steps go through the same docker-exec headscale
// CLI that the operator used to run by hand. The handler
// refuses to act if:
//   - the node doesn't have 0.0.0.0/0 AND ::/0 advertised
//     (operator hasn't run `tailscale set --advertise-exit-node` yet)
//   - the node is already tagged with `tag:exit-node`
//     (idempotency: this handler is for the
//     "tag" half of the workflow, not the "untag")
//
// PostAdminExitNodeUntagAsExitNode (below) handles the
// reverse — removing tag:exit-node from a node.
func (s *Service) PostAdminExitNodeTagAsExitNode(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idStr := r.FormValue("node_id")
	if idStr == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("node_id required"), http.StatusSeeOther)
		return
	}
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("bad node id"), http.StatusSeeOther)
		return
	}

	// Find the node and verify it has the exit-node
	// bases advertised. We refuse to tag a node that
	// hasn't advertised 0.0.0.0/0+::/0 (the operator
	// must run `tailscale set --advertise-exit-node`
	// first — that's the "I want this to be an exit-node"
	// gate). This is also why the button is only rendered
	// in the template for nodes that have these routes
	// advertised; the server-side check is defense in
	// depth in case the operator crafts a POST by hand.
	allNodes, err := s.HSGlobalFn().ListAllNodes()
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("list nodes: "+err.Error()), http.StatusSeeOther)
		return
	}
	var target *headscale.NodeView
	for i := range allNodes {
		if allNodes[i].ID == idStr {
			target = &allNodes[i]
			break
		}
	}
	if target == nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("node not found"), http.StatusSeeOther)
		return
	}
	hasV4, hasV6 := false, false
	for _, rt := range target.AvailableRoutes {
		if rt == "0.0.0.0/0" {
			hasV4 = true
		}
		if rt == "::/0" {
			hasV6 = true
		}
	}
	if !hasV4 || !hasV6 {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(
			"node does not advertise 0.0.0.0/0 + ::/0 yet — run `tailscale set --advertise-exit-node` on the relay first"), http.StatusSeeOther)
		return
	}

	// Idempotency: if the node already has tag:exit-node,
	// skip the TagNode call. The button is hidden in this
	// case but we re-check here.
	for _, t := range target.Tags {
		if t == "tag:exit-node" {
			http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(
				fmt.Sprintf("%s is already tagged as exit-node", target.Hostname)), http.StatusSeeOther)
			return
		}
	}

	// Step 1: approve the exit-node bases. We approve
	// ONLY 0.0.0.0/0 and ::/0 (not the full availableRoutes)
	// to avoid accidentally approving relay-3's 200+
	// subnets.
	hs := s.HSGlobalFn()
	approved, err := hs.ApproveRoutesForNodeID(nodeID, []string{"0.0.0.0/0", "::/0"})
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("approve-routes: "+err.Error()), http.StatusSeeOther)
		return
	}

	// Step 2: tag with tag:exit-node. The ACL already
	// allows `* → tag:exit-node:*`, so the node starts
	// accepting traffic immediately on the next ACL
	// poll by the Tailscale client (usually <60s).
	//
	// 2026-08-10 v0.33.1.35 B87: switched from hs.TagNode to
	// hs.AddTag. Pre-fix, TagNode REPLACED the entire tag
	// set on the node (headscale's `nodes tag --force`
	// subcommand takes a full tag set, not a delta). The
	// exit-nodes on the live VM are also per-user devices
	// (tagged `tag:dev-skyadmin-emilia`, etc.) — the
	// v0.33.1.30 B82 follow-up documented that
	// `tag:dev-skyadmin-*` is the per-user device marker
	// that lets the per-user ACL grants resolve. The
	// pre-fix TagNode silently wiped those tags on every
	// "Tag as exit-node" click, breaking the per-user grant
	// until the operator re-applied the tag manually. The
	// fix: AddTag (read-modify-write at
	// internal/headscale/tags.go:117) reads the current
	// tag set first, appends `tag:exit-node`, and writes
	// the union — preserving the existing per-user dev-tag.
	// AddTag also propagates ListAllNodes errors now (the
	// pre-fix silently swallowed the read error and would
	// have written only `[want]`, silently wiping the
	// existing tags). The `UntagNode` call below
	// (PostAdminExitNodeUntagAsExitNode) already does the
	// same read-modify-write dance for removal, so the
	// read-modify pattern is consistent across both
	// directions. Pinned by 4 unit tests in
	// internal/headscale/tags_test.go (PreservesExistingTags,
	// NoOpWhenAlreadyPresent, PreservesOnError,
	// TagNode_ReplacesEntireSet).
	if err := hs.AddTag(nodeID, "tag:exit-node"); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("tag: "+err.Error()), http.StatusSeeOther)
		return
	}

	hs.InvalidateCache()
	s.Backend.Audit(c.UserID, c.Username, "exit_node_tag",
		fmt.Sprintf("node=%s id=%d approved_routes=%d tag=tag:exit-node",
			target.Hostname, nodeID, approved))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(
		fmt.Sprintf("%s is now tagged as exit-node (%d routes approved)",
			target.Hostname, approved)), http.StatusSeeOther)
}

// PostAdminExitNodeUntagAsExitNode is the v0.18.1
// "Untag" button on /admin/exit-nodes. Removes
// `tag:exit-node` from a node. Useful when the
// operator wants to demote a relay back to a
// regular node (e.g. the relay is going down for
// maintenance and they don't want tailnet clients
// to pick it as an exit-node).
//
// The handler does NOT touch the approved routes —
// those stay as-is. To remove the routes too, the
// operator has to run `docker exec headscale headscale
// nodes approve-routes -i <id> -r "" --force` (or
// similar); we don't expose that from the UI because
// route removal is rarely wanted.
func (s *Service) PostAdminExitNodeUntagAsExitNode(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idStr := r.FormValue("node_id")
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("bad node id"), http.StatusSeeOther)
		return
	}

	hs := s.HSGlobalFn()
	// UntagNode preserves the other tags (replaces the
	// full tag list, leaving the others in place). If
	// the node was tagged only with tag:exit-node, it
	// falls back to tag:private so headscale keeps at
	// least one tag (the headscale CLI rejects empty
	// tag sets).
	if err := hs.UntagNode(nodeID, "tag:exit-node"); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("untag: "+err.Error()), http.StatusSeeOther)
		return
	}
	hs.InvalidateCache()
	s.Backend.Audit(c.UserID, c.Username, "exit_node_untag",
		fmt.Sprintf("node_id=%d tag=tag:exit-node", nodeID))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape("Removed tag:exit-node from node."), http.StatusSeeOther)
}
