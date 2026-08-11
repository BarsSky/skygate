// Headscale route operations: approve-routes (headscale side) and
// SetAdvertisedRoutes (tailscale side, via SSH).
//
// ApproveAllRoutes* runs on the headscale host (so `docker exec
// headscale` is fine). SetAdvertisedRoutes runs on the exit-node
// host, so it shells out over SSH using an explicit `-i <key>` +
// `BatchMode=yes` (never prompt for a password) — the previous
// hard-coded `-F /home/admin/.ssh/config` only worked in the legacy
// /home/admin operator layout; the dockerised skygate container
// has no /home/admin/ at all, so the SSH step silently failed and
// the headscale approve-routes step made the UI look successful
// even though tailscaled on the relay was never re-configured.
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
// accidentally approve relay-3's 200+ subnets).
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

// splitSSHTarget parses an `user@host:port` SSH target into the
// (host, port) pair that `ssh` expects on the command line.
// Returns the original target as `host` when there's no port
// suffix. Strips the `user@` prefix so the host can be passed
// directly to `ssh` together with a `-p port` flag (the
// `user@host:port` shorthand is ssh_config-only — `ssh` on
// the command line interprets `:` as part of the hostname
// and tries to resolve it via DNS, which is why a target like
// `root@193.233.130.178:18022` used to fail with "Could not
// resolve hostname 193.233.130.178:18022").
//
// Examples:
//
//	splitSSHTarget("root@193.233.130.178:18022")
//	  -> "root@193.233.130.178", "18022"
//	splitSSHTarget("root@karolina")
//	  -> "root@karolina", ""
//	splitSSHTarget("karolina")
//	  -> "karolina", ""
//	splitSSHTarget("root@[2001:db8::1]:2222")
//	  -> "root@[2001:db8::1]", "2222"
func splitSSHTarget(target string) (host, port string) {
	host = target
	if i := strings.LastIndex(target, ":"); i >= 0 {
		candidate := target[i+1:]
		if _, err := strconv.Atoi(candidate); err == nil {
			port = candidate
			host = target[:i]
		}
	}
	return host, port
}

// ApproveRoutesForNodeID approves a specific route list on
// a headscale node identified by numeric ID. 2026-07-17:
// v0.18.1 — public API for the "Tag as exit-node" button
// on /admin/exit-nodes. The button approves only the
// exit-node bases (0.0.0.0/0, ::/0) instead of the full
// availableRoutes set (relay-3 has 200+ subnets that the
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
//	                           VPN server, e.g. Amnezia-AWG on relay-3;
//	                           without this, Tailscale pulls Google/Telegram
//	                           subnets from peers into source-routing table
//	                           52 and traffic from the other VPN black-holes)
//	 0 -> do not touch AcceptRoutes (legacy behaviour, default for nodes
//	      that do not opt in via exit_servers.accept_routes)
//	 1 -> --accept-routes=true  (full legacy behaviour, OK for pure
//	                             exit-nodes that share no other VPN)
//
// sshTarget is the SSH target in `user@host[:port]` form (typically the
// value of exit_servers.ssh_target). When empty, the function falls back
// to `nodeHostname` — the historical behaviour, which only works for
// hosts whose Tailscale name resolves directly in DNS / /etc/hosts.
//
// sshKeyPath is the absolute path to the private key INSIDE the
// skygate container (typically the value of exit_servers.ssh_key_path,
// or the Config.SSHKeyPath / SKYGATE_EXIT_SSH_KEY default). When
// empty, the function refuses to run and returns a clear error
// rather than falling back to the legacy /home/admin/.ssh/config
// path (which doesn't exist in the container) — silently falling
// back to a non-existent path is what bit us pre-v0.33.1.
//
// 2026-08-04 v0.33.1: signature changed. The previous single-arg
// `SetAdvertisedRoutes(node, routes, acceptRoutes)` hard-coded
// `/home/admin/.ssh/config` and always SSH'd to `nodeHostname`.
// The hard-coded config file doesn't exist in the dockerised
// skygate; the per-exit-node `ssh_target` (which encodes the
// non-default `Port 18022` karolina uses) was being ignored.
// Callers MUST pass both sshTarget and sshKeyPath now.
func (c *Client) SetAdvertisedRoutes(nodeHostname string, routes []string, acceptRoutes int, sshTarget, sshKeyPath string) (string, error) {
	if len(routes) == 0 {
		return "", fmt.Errorf("empty routes list")
	}
	// Build the SSH target. The exit_servers.ssh_target column was
	// added in v0.24 precisely so each relay can live on a non-default
	// port + have its own user (root / a dedicated deploy user); using
	// nodeHostname as the SSH target is a fallback for nodes that
	// haven't been customised through the /admin/exit-nodes form.
	target := strings.TrimSpace(sshTarget)
	if target == "" {
		target = nodeHostname
	}
	// Refuse to run with an empty key path. The legacy fallback
	// (`/home/admin/.ssh/config`) silently failed in the dockerised
	// skygate, and a missing-key error is more honest than a
	// "config file not found" coming out of `ssh` two minutes later.
	keyPath := strings.TrimSpace(sshKeyPath)
	if keyPath == "" {
		return "", fmt.Errorf("SetAdvertisedRoutes(%s): no ssh_key_path provided; set exit_servers.ssh_key_path or SKYGATE_EXIT_SSH_KEY", nodeHostname)
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
	// BatchMode=yes: never prompt for a password / passphrase. The
	// skygate process runs headless in a container — an interactive
	// prompt would hang the entire sync goroutine until docker
	// times it out. StrictHostKeyChecking=accept-new: pin the
	// host key on first connect (so a re-deploy to a different
	// relay IP doesn't get silently MITM'd), but don't fail
	// when the host key is new. ConnectTimeout=10: bound the
	// per-call latency so a dead relay doesn't block the whole
	// sync (the operator sees "ssh=err=..." instead of a hung
	// request).
	//
	// sshTarget supports the `user@host:port` shorthand (the
	// natural shape when reading exit_servers.ssh_target from
	// the operator-side /admin/exit-nodes form). `ssh` itself
	// does NOT understand the `host:port` part on the command
	// line (that's an ssh_config-only syntax), so we split the
	// target into (user@host, port) and use `-p port` explicitly.
	// When the target has no `:port` suffix, port stays "" and
	// ssh uses 22.
	sshArgs := []string{
		"-i", keyPath,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
	}
	host, port := splitSSHTarget(target)
	if port != "" {
		sshArgs = append(sshArgs, "-p", port)
	}
	sshArgs = append(sshArgs, host, cmd)
	sshCmd := exec.Command("ssh", sshArgs...)
	out, err := sshCmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("ssh %s (key %s): %s", target, keyPath, strings.TrimSpace(string(out)))
}
