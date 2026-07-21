// Headscale node operations + node types.
//
// HSNode is the headscale-side wire representation; NodeView is the
// flattended, UI-friendly projection (hostname, exit-node flag, IP
// slice, etc.) that handlers and templates actually consume. The
// toView() conversion is the seam between the two.
package headscale

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type HSNode struct {
	ID              string        `json:"id"`
	MachineKey      string        `json:"machineKey"`
	NodeKey         string        `json:"nodeKey"`
	DiscoKey        string        `json:"discoKey"`
	Name            string        `json:"name"`
	GivenName       string        `json:"givenName"`
	User            HSUser        `json:"user"`
	IPAddresses     []string      `json:"ipAddresses"`
	Online          bool          `json:"online"`
	LastSeen        string        `json:"lastSeen"`
	CreatedAt       string        `json:"createdAt"`
	// Expiry is the headscale-side node.expiry field as a
	// RFC3339Nano string. Empty when the node has no
	// expiry (the typical state for tagged nodes; see
	// expirewatch package doc for the background).
	// 2026-07-21: v0.23.3 — added so the expirewatch
	// goroutine can decide whether to renew a node
	// without making an extra /api/v1/node/{id} round
	// trip per node per tick. headscale's /api/v1/node
	// endpoint always populates this when the field is
	// non-null in the DB.
	Expiry          string        `json:"expiry"`
	Tags            []string      `json:"tags"`
	AvailableRoutes []string      `json:"availableRoutes"`
	// ApprovedRoutes is what headscale has actually approved for
	// this node (after `headscale nodes approve-routes` runs).
	// Distinct from AvailableRoutes (what the node asked for)
	// because the operator / auto-approver may have only
	// approved a subset, or none at all. Read by the sidecar
	// auto-approver (v0.16.7) to decide when to flip
	// user_subnets.status to active.
	ApprovedRoutes  []string      `json:"approvedRoutes"`
	PreAuthKey      *HSPreauthKey `json:"preAuthKey"`
}

type NodeView struct {
	ID              string
	Hostname        string
	GivenName       string
	IPAddresses     []string
	Online          bool
	LastSeen        string
	UserName        string
	UserID          string
	IsExitNode      bool
	Tags            []string
	AvailableRoutes []string
	// ApprovedRoutes mirrors HSNode.ApprovedRoutes — see the
	// comment on HSNode for the distinction. Used by
	// sidecar.auto_approver.
	ApprovedRoutes  []string
	// PreAuthKeyID is the headscale ID of the preauth key this node
	// registered with, or "" if the node predates our key tracking.
	PreAuthKeyID string
	// CreatedAt is the RFC3339 timestamp from headscale for when the
	// node first registered. Used by backfillNodeOwnership as a
	// fallback when a user's preauth key has no stored headscale_preauth_id
	// (e.g. because the headscale API response shape changed and the
	// key ID field stopped being captured). In that case we still
	// match by "node created after this preauth key" with a safety
	// margin to avoid stealing another user's recent node.
	CreatedAt string
	// Expiry is the RFC3339Nano string from headscale for the
	// node's expiry. Empty when the node has no expiry (the
	// typical state for tagged nodes; see expirewatch package
	// doc). 2026-07-21: v0.23.3 — added so the expirewatch
	// goroutine can decide whether to renew a node without
	// making an extra /api/v1/node/{id} round trip per node
	// per tick. The expirewatch path does time.Parse on this
	// with RFC3339Nano; an unparseable string is treated as
	// "no expiry" and the node is renewed defensively.
	Expiry string
}

// toView flattens an HSNode into a NodeView. The conversion copies
// fields and computes IsExitNode via hasExitNodeTag (see below), so
// handlers can pass NodeView around without re-running that check.
func (n HSNode) toView() NodeView {
	tags := append([]string{}, n.Tags...)
	host := n.GivenName
	if host == "" {
		host = n.Name
	}
	var pakID string
	if n.PreAuthKey != nil {
		pakID = n.PreAuthKey.ID
	}
	return NodeView{
		ID:              n.ID,
		Hostname:        host,
		GivenName:       n.GivenName,
		IPAddresses:     n.IPAddresses,
		Online:          n.Online,
		LastSeen:        n.LastSeen,
		UserName:        n.User.Name,
		UserID:          n.User.ID,
		IsExitNode:      hasExitNodeTag(tags, n.Name, n.AvailableRoutes),
		Tags:            tags,
		AvailableRoutes: n.AvailableRoutes,
		ApprovedRoutes:  n.ApprovedRoutes,
		PreAuthKeyID:    pakID,
		CreatedAt:       n.CreatedAt,
		// 2026-07-21: v0.23.3 — Expiry plumbed through
		// to NodeView so expirewatch doesn't have to do
		// an extra /api/v1/node/{id} call per node. The
		// headscale API always returns the field when
		// it's non-null in the DB; empty string = nil
		// expiry (tagged nodes, pre-v0.23.3 installs
		// that predate the regReq.Expiry path).
		Expiry:          n.Expiry,
	}
}

// hasExitNodeTag decides whether a node should be exposed as an exit
// node to portal users. Three signals, in order:
//
//  1. Explicit `tag:exit-node` (Tailscale convention).
//  2. Node name starts with `exit-` or `exitnode` (legacy convention).
//  3. Node advertises 0.0.0.0/0 or ::/0 — headscale 0.29 lets a node
//     function as an exit node by advertising the base routes even
//     without an explicit tag (this is how karolina/emilia/sharlotta
//     work in our deployment).
func hasExitNodeTag(tags []string, name string, availableRoutes []string) bool {
	// 1. Explicit tag:exit-node (Tailscale convention)
	for _, t := range tags {
		if strings.EqualFold(t, "tag:exit-node") {
			return true
		}
	}
	// 2. Name starts with exit- or exitnode
	n := strings.ToLower(name)
	if strings.HasPrefix(n, "exit-") || strings.HasPrefix(n, "exitnode") {
		return true
	}
	// 3. headscale 0.29: any node with availableRoutes containing 0.0.0.0/0
	//    is functionally an exit node (advertises itself as a router for
	//    the whole internet). This is how our karolina/emilia/sharlotta
	//    work as exit nodes without an explicit tag.
	for _, r := range availableRoutes {
		if r == "0.0.0.0/0" || r == "::/0" {
			return true
		}
	}
	return false
}

type hsNodeList struct {
	Nodes []HSNode `json:"nodes"`
}

// ListAllNodes returns every node in the tailnet (admin). Result is cached
// for cacheTTL to absorb the ~50ms cost of headscale's gRPC-to-HTTP gateway
// on every page render.
func (c *Client) ListAllNodes() ([]NodeView, error) {
	c.cacheMu.RLock()
	if c.cacheAll != nil && time.Since(c.cacheAllAt) < c.cacheTTL {
		out := c.cacheAll
		c.cacheMu.RUnlock()
		return out, nil
	}
	c.cacheMu.RUnlock()

	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	// double-check after acquiring write lock
	if c.cacheAll != nil && time.Since(c.cacheAllAt) < c.cacheTTL {
		return c.cacheAll, nil
	}

	var raw hsNodeList
	err := c.do("GET", "/api/v1/node", nil, &raw)
	if err != nil {
		return nil, err
	}
	out := make([]NodeView, 0, len(raw.Nodes))
	for _, n := range raw.Nodes {
		out = append(out, n.toView())
	}
	c.cacheAll = out
	c.cacheAllAt = time.Now()
	return out, nil
}

// ListExitNodes returns the subset of all nodes flagged as exit nodes
// by hasExitNodeTag (see above). Cached indirectly via ListAllNodes.
func (c *Client) ListExitNodes() ([]NodeView, error) {
	all, err := c.ListAllNodes()
	if err != nil {
		return nil, err
	}
	var exits []NodeView
	for _, n := range all {
		if n.IsExitNode {
			exits = append(exits, n)
		}
	}
	return exits, nil
}

// ListNodesByUser returns all nodes belonging to a headscale user.
// Handles {"nodes":[...]} wrapper.
func (c *Client) ListNodesByUser(userName string) ([]NodeView, error) {
	var list hsNodeList
	err := c.do("GET", "/api/v1/node?user="+userName, nil, &list)
	if err == nil && list.Nodes != nil {
		out := make([]NodeView, 0, len(list.Nodes))
		for _, n := range list.Nodes {
			out = append(out, n.toView())
		}
		return out, nil
	}
	var flat []HSNode
	if err2 := c.do("GET", "/api/v1/node?user="+userName, nil, &flat); err2 == nil {
		out := make([]NodeView, 0, len(flat))
		for _, n := range flat {
			out = append(out, n.toView())
		}
		return out, nil
	}
	return nil, err
}

// DeleteNode removes a node from headscale by its numeric ID. The cache
// is invalidated on success so the next ListAllNodes reflects the change.
func (c *Client) DeleteNode(nodeID int64) error {
	err := c.do("DELETE", "/api/v1/node/"+strconv.FormatInt(nodeID, 10), nil, nil)
	if err == nil {
		c.InvalidateCache()
	}
	return err
}

// ExtendNodeExpiry sets node.Expiry to the given future time. This
// works around a Tailscale 1.98.x client behaviour where RegisterRequest
// includes an Expiry that is only a few seconds in the future, and
// headscale 0.29.x blindly applies that Expiry to the node — see
// hscontrol/state.go's HandleNodeFromAuthPath: `if !regReq.Expiry.IsZero()
// { node.Expiry = &regReq.Expiry }`. Within ~4 seconds of registration
// the client receives a netmap with `Expired: true, MachineAuthorized:
// false` and gets force-logged-out, which manifested on 2026-07-21
// as the Android device (skybars / node 10) being unable to stay
// connected after a fresh preauth. The expirewatch goroutine
// (internal/expirewatch) calls this method on every node whose
// expiry is within the renewal threshold (default 7d), so even if
// headscale wrote a 4-second expiry at registration the watcher
// pushes it back out to ~30d within the next sync tick (5m by
// default).
//
// CLI path (only path that works in headscale 0.29.2):
//
//	docker exec headscale headscale nodes expire -i <id>
//	    --expiry <RFC3339>
//
// Why not the REST API? In headscale 0.29.2 the
// `POST /api/v1/node/{id}/expire` endpoint has a bug:
// it accepts the call with HTTP 200 and the body shape
// `{"expiry":"2026-08-20T11:29:54Z"}` but silently falls
// back to `time.Now()` for the actual expiry value. Verified
// live on 2026-07-21: a POST with a 30-day-future expiry
// got back an expiry value 5 seconds in the future (the
// same value the call would have set if you passed an
// empty body). The gRPC handler source
// (hscontrol/grpcv1.go ExpireNode) clearly reads
// `request.GetExpiry().AsTime()` when the field is set,
// so the bug is in the REST-to-gRPC JSON binding (the
// wrong field name or no binding at all). Tried 7
// different body shapes (`expiry` / `Expiry` / camelCase /
// `expiry_unix` / `valid_until` / `node_expiry` / `expire_at`)
// — all produce the same 5-second clamped result.
//
// The CLI uses the same code path (it calls gRPC internally
// too, but it sets the `expiry` proto field correctly),
// so the CLI works. When headscale fixes the REST bug
// (likely in 0.29.3 or 0.30.x — the field is named
// `expiry` in the proto), ExtendNodeExpiry can switch
// back to the API path. For now: CLI only.
//
// `dockerRunner` is the function used to shell out the
// `docker` command. Production wiring uses
// `exec.Command("docker", args...)`; tests can inject
// a stub that records the call and exits 0 without
// touching the system docker. nil dockerRunner =
// use the default (exec.Command).
//
// Cache: invalidates on success so the next ListAllNodes reflects
// the new expiry within one TTL window.
func (c *Client) ExtendNodeExpiry(nodeID int64, expiry time.Time) error {
	idStr := strconv.FormatInt(nodeID, 10)
	if c.ExecContainer == "" {
		return fmt.Errorf("no ExecContainer configured (needed for CLI fallback; REST API path is broken in headscale 0.29.2)")
	}
	args := []string{"exec", c.ExecContainer, "headscale", "nodes", "expire",
		"-i", idStr, "--expiry", expiry.UTC().Format(time.RFC3339)}
	var out []byte
	var err error
	if c.dockerRunner != nil {
		out, err = c.dockerRunner(args...)
	} else {
		out, err = exec.Command("docker", args...).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("cli: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	c.InvalidateCache()
	return nil
}

// NodeList returns every node in the tailnet as a generic []map[string]any
// projection. Used by exit-rules code paths that only need a few fields
// (id, hostname, ip, user) and don't want to depend on NodeView.
func (c *Client) NodeList() ([]map[string]any, error) {
	var list struct {
		Nodes []struct {
			ID        string   `json:"id"`
			GivenName string   `json:"givenName"`
			IPAddress string   `json:"ipAddress4"`
			UserID    string   `json:"user"`
			Tags      []string `json:"forcedTags"`
		} `json:"nodes"`
	}
	err := c.do("GET", "/api/v1/node?show_all=true", nil, &list)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, n := range list.Nodes {
		out = append(out, map[string]any{
			"id": n.ID, "hostname": n.GivenName,
			"ip": n.IPAddress, "user": n.UserID,
		})
	}
	return out, nil
}

// NodeInfo returns hostname + IPv4 for a node matching hostname prefix.
// Used by the route-setup script orchestrator to resolve exit-node IPs
// without forcing the script's host to know the headscale node IDs.
func (c *Client) NodeInfo(hostname string) (string, string, error) {
	var list struct {
		Nodes []struct {
			ID         string   `json:"id"`
			GivenName  string   `json:"givenName"`
			IPAddress4 string   `json:"ipAddress4"`
			Tags       []string `json:"forcedTags"`
		} `json:"nodes"`
	}
	if err := c.do("GET", "/api/v1/node?show_all=true", nil, &list); err != nil {
		return "", "", err
	}
	for _, n := range list.Nodes {
		if strings.EqualFold(n.GivenName, hostname) || strings.HasPrefix(n.GivenName, hostname) {
			return n.GivenName, n.IPAddress4, nil
		}
	}
	return "", "", fmt.Errorf("node %q not found", hostname)
}
