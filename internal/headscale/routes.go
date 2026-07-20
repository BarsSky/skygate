// Headscale route operations: approve-routes (headscale side) and
// SetAdvertisedRoutes (tailscale side, via SSH).
//
// ApproveAllRoutes* runs on the headscale host (so `docker exec
// headscale` is fine). SetAdvertisedRoutes runs on the exit-node
// host, so it shells out over SSH using /home/skyadmin/.ssh/config.
//
// The base-route prepending, dedup logic, and AcceptRoutes flag
// fragment live in route_args.go (pure helpers, no I/O, unit-tested
// in route_args_test.go) so the SSH invocation below stays narrowly
// focused on placing the command.
package headscale

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ApproveAllRoutes enables all pending routes for a node via headscale
// CLI (docker exec). 2026-07-07: previously used /api/v1/routes but
// that's deprecated/404 in headscale 0.29.1. Now we shell out to
// `docker exec headscale headscale nodes approve-routes -i <id> -r <routes>`.
func (c *Client) ApproveAllRoutes(nodeHostname string) (int, error) {
	return c.ApproveAllRoutesWithList(nodeHostname, nil)
}

// ApproveAllRoutesWithList is ApproveAllRoutes with an explicit route
// list. When routes is nil, the function fetches the node's current
// availableRoutes via the API and uses those. Returns the number of
// routes approved.
func (c *Client) ApproveAllRoutesWithList(nodeHostname string, routes []string) (int, error) {
	nodes, err := c.ListAllNodes()
	if err != nil {
		return 0, err
	}
	var nodeID int
	for _, n := range nodes {
		if strings.EqualFold(n.Hostname, nodeHostname) || strings.EqualFold(n.GivenName, nodeHostname) {
			nodeID, _ = strconv.Atoi(n.ID)
			break
		}
	}
	if nodeID == 0 {
		return 0, fmt.Errorf("node %q not found", nodeHostname)
	}

	return c.approveRoutesForNodeID(nodeID, routes)
}

// approveRoutesForNodeID is the inner worker: takes a node ID
// and a routes list, fetches AvailableRoutes if routes is nil,
// and shells out to the headscale CLI. 2026-07-17: v0.18.1 —
// factored out so the v0.18.1 "Tag as exit-node" button
// can approve just 0.0.0.0/0+::/0 without ApproveAllRoutes'
// "approve every pending route" behaviour (which would
// accidentally approve karolina's 200+ subnets).
func (c *Client) approveRoutesForNodeID(nodeID int, routes []string) (int, error) {
	if nodeID == 0 {
		return 0, fmt.Errorf("invalid node id 0")
	}

	if len(routes) == 0 {
		var nodeInfo struct {
			Node struct {
				AvailableRoutes []string `json:"availableRoutes"`
			} `json:"node"`
		}
		if err := c.do("GET", fmt.Sprintf("/api/v1/node/%d", nodeID), nil, &nodeInfo); err == nil {
			routes = nodeInfo.Node.AvailableRoutes
		}
	}

	if len(routes) == 0 {
		return 0, nil
	}

	routeStr := strings.Join(routes, ",")
	cmd := exec.Command("docker", "exec", "headscale",
		"/ko-app/headscale", "nodes", "approve-routes",
		"-i", strconv.Itoa(nodeID),
		"-r", routeStr,
		"--force")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("approve-routes: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return len(routes), nil
}

// ApproveRoutesForNodeID approves a specific route list on
// a headscale node identified by numeric ID. 2026-07-17:
// v0.18.1 — public API for the "Tag as exit-node" button
// on /admin/exit-nodes. The button approves only the
// exit-node bases (0.0.0.0/0, ::/0) instead of the full
// availableRoutes set (karolina has 200+ subnets that the
// operator does NOT want auto-approved).
//
// Routes that are not in the node's AvailableRoutes will
// fail the headscale CLI with a clear error — callers
// should first verify the routes are advertised (read the
// node via API and check AvailableRoutes).
func (c *Client) ApproveRoutesForNodeID(nodeID int64, routes []string) (int, error) {
	return c.approveRoutesForNodeID(int(nodeID), routes)
}

// SetAdvertisedRoutes updates advertised routes on an exit node via SSH.
//
// acceptRoutes controls whether --accept-routes is also re-applied on the
// node:
//
//	-1 -> --accept-routes=false (recommended for nodes that co-host another
//	                           VPN server, e.g. Amnezia-AWG on karolina;
//	                           without this, Tailscale pulls Google/Telegram
//	                           subnets from peers into source-routing table
//	                           52 and traffic from the other VPN black-holes)
//	 0 -> do not touch AcceptRoutes (legacy behaviour, default for nodes
//	      that do not opt in via exit_servers.accept_routes)
//	 1 -> --accept-routes=true  (full legacy behaviour, OK for pure
//	                             exit-nodes that share no other VPN)
func (c *Client) SetAdvertisedRoutes(nodeHostname string, routes []string, acceptRoutes int) (string, error) {
	if len(routes) == 0 {
		return "", fmt.Errorf("empty routes list")
	}
	// Always keep 0.0.0.0/0 and ::/0 advertised so the node stays a usable
	// exit node. `tailscale set --advertise-routes=` replaces the list, so
	// any call without these bases would silently strip the exit-node
	// capability. Dedupe to avoid duplicate-route errors on tailscaled.
	// Base routes + dedup + AcceptRoutes flag fragment are pure helpers
	// (see route_args.go) so the SSH invocation below stays narrowly
	// focused on actually placing the command. Any future change to the
	// tailscale flag set belongs in the helper, not here.
	cmd := BuildTailscaleSetCommand(routes, acceptRoutes)
	sshCmd := exec.Command("ssh", "-F", "/home/skyadmin/.ssh/config",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		nodeHostname, cmd)
	out, err := sshCmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("run manually on %s: %s", nodeHostname, cmd)
}
