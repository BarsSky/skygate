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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// sshTargetRe is the ONLY shape we accept for exit_servers.ssh_target:
// an optional `user@`, then a hostname / IPv4 literal / bracketed IPv6
// literal, then an optional `:port`. Anchored, no whitespace, no leading
// dash, no shell metacharacters.
//
// B266 (2026-09-19) — see the security note in SetAdvertisedRoutes: the
// value used to be appended positionally to the ssh argv, so a target
// like `-oProxyCommand=…` became an ssh OPTION and executed inside a
// container that holds the docker socket.
var sshTargetRe = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?(?:[A-Za-z0-9._-]+|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?$`)

// IsSafeSSHTarget reports whether `target` is an acceptable
// exit_servers.ssh_target value. Exported so the /admin/exit-nodes form
// can reject a bad value at write time with a clear message instead of
// failing minutes later inside a background sync.
//
// Pure function (unit-tested).
func IsSafeSSHTarget(target string) bool {
	t := strings.TrimSpace(target)
	if t == "" || len(t) > 255 {
		return false
	}
	if strings.HasPrefix(t, "-") || strings.ContainsAny(t, " \t\n\r;|&$`'\"\\<>(){}*?!") {
		return false
	}
	if !sshTargetRe.MatchString(t) {
		return false
	}
	// A `:port` suffix must be a legal TCP port.
	if i := strings.LastIndex(t, ":"); i >= 0 && !strings.HasSuffix(t, "]") {
		if p, err := strconv.Atoi(t[i+1:]); err != nil || p < 1 || p > 65535 {
			return false
		}
	}
	// A bare IP literal is fine; a hostname must contain at least one
	// letter or dot (rejects things like "123").
	host := t
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	return strings.ContainsAny(host, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ.")
}

// ApproveAllRoutes enables all pending routes for a node via headscale
// CLI (docker exec). 2026-07-07: previously used /api/v1/routes but
// that's deprecated/http.StatusNotFound in headscale 0.29.1. Now we shell out to
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

	var apiErr error
	// 1. REST API first — it is the only path that works on EVERY install kind
	//    (docker, systemd, binary). headscale has exposed
	//    POST /api/v1/node/{id}/approve_routes since 0.23; the old
	//    /api/v1/routes endpoint is the one that was deprecated, which is why
	//    the CLI fallback was added in 2026-07-07.
	if err := c.do("POST", fmt.Sprintf("/api/v1/node/%d/approve_routes", nodeID),
		map[string]any{"routes": routes}, nil); err == nil {
		return len(routes), nil
	} else {
		apiErr = err
	}

	// 2. CLI fallback, install-kind aware: `docker exec` on a containerised
	//    headscale, the `headscale` binary directly on a NATIVE install.
	//    Pre-2026-09-19 this branch hardcoded docker, so every native install
	//    failed the "Tag as exit-node" / approve-routes flow with
	//    `exec: "docker": executable file not found in $PATH`
	//    (operator report, native Debian host).
	routeStr := strings.Join(routes, ",")
	args := []string{"nodes", "approve-routes", "-i", strconv.Itoa(nodeID), "-r", routeStr, "--force"}
	out, cliErr := c.runHeadscaleCLI(args...)
	if cliErr != nil {
		return 0, fmt.Errorf("approve-routes: API failed (%v) and CLI failed (%w: %s)", apiErr, cliErr, strings.TrimSpace(string(out)))
	}
	return len(routes), nil
}

// runHeadscaleCLI runs a headscale CLI verb on whichever install kind this
// deployment is: `docker exec <container> <binary> …` when docker and a
// container name are available, the local `headscale` binary otherwise
// (systemd/binary installs, where docker is not installed at all).
func (c *Client) runHeadscaleCLI(args ...string) ([]byte, error) {
	container := c.ExecContainer
	if container == "" {
		container = "headscale"
	}
	bin := c.headscaleCLIPath()
	// A native install: no docker in PATH at all — try the local binary first
	// so we do not even attempt a docker lookup that cannot succeed.
	if _, err := exec.LookPath("docker"); err == nil && c.dockerRunner != nil {
		full := append([]string{"exec", container, bin}, args...)
		return c.dockerRunner(full...)
	}
	if _, err := exec.LookPath("docker"); err == nil {
		full := append([]string{"exec", container, bin}, args...)
		out, err := exec.Command("docker", full...).CombinedOutput()
		if err == nil {
			return out, nil
		}
		// docker exists but the container/binary layout differs — fall through
		// to the local binary and keep the docker error for the caller.
		local, localErr := exec.Command("headscale", args...).CombinedOutput()
		if localErr == nil {
			return local, nil
		}
		return out, fmt.Errorf("docker exec failed (%v); local headscale failed (%v)", err, localErr)
	}
	out, err := exec.Command("headscale", args...).CombinedOutput()
	if err != nil {
		// B272.6: name the ACTUAL install kind in the hint. The old wording
		// offered docker and "install the headscale CLI" side by side, so a
		// native (systemd) host — where docker is absent by design and the CLI
		// IS installed — got a sentence that fit none of its facts. Live on
		// aro: `docker not in PATH and "headscale" failed: exit status 1 …`
		// while the real cause was the socket permission
		// (`/var/run/headscale/headscale.sock: permission denied`), and the
		// REST path had already succeeded anyway.
		return out, fmt.Errorf("local headscale CLI %q failed: %w — on a native install the CLI needs access to the headscale socket (add the skygate user to the headscale group) and its own config (e.g. -c /etc/headscale/config.yaml); for a containerised headscale set SKYGATE_HEADSCALE_CONTAINER, and for a different CLI path set SKYGATE_HEADSCALE_CLI", "headscale", err)
	}
	return out, nil
}

// headscaleCLIPath is the in-container path of the headscale binary (the
// official image ships /ko-app/headscale; SKYGATE_HEADSCALE_CLI overrides it
// for images with the binary on $PATH).
func (c *Client) headscaleCLIPath() string {
	if p := os.Getenv("SKYGATE_HEADSCALE_CLI"); p != "" {
		return p
	}
	return "/ko-app/headscale"
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
	// B266 (2026-09-19) — SECURITY: `ssh_target` comes from the
	// operator-writable exit_servers table and used to be appended
	// POSITIONALLY to the ssh argv, so a value beginning with `-`
	// (e.g. `-oProxyCommand=<cmd>`) was parsed by ssh as an OPTION and
	// executed inside the skygate container — which mounts
	// /var/run/docker.sock and carries NET_ADMIN+SYS_ADMIN. That is a
	// container-root escalation reachable by any admin session.
	//
	// Two independent guards now:
	//   1. a strict shape check on the target (user@host[:port] only);
	//   2. `--` before the host plus explicit hardening options, so
	//      even a future validation regression cannot turn the value
	//      into an option.
	if !IsSafeSSHTarget(target) {
		return "", fmt.Errorf("SetAdvertisedRoutes(%s): refusing unsafe ssh_target %q (expected [user@]host[:port])", nodeHostname, target)
	}
	keyPath := strings.TrimSpace(sshKeyPath)
	if keyPath == "" {
		return "", fmt.Errorf("SetAdvertisedRoutes(%s): no ssh_key_path provided; set exit_servers.ssh_key_path or SKYGATE_EXIT_SSH_KEY", nodeHostname)
	}
	// B266: a relative key path would be resolved against the
	// container's CWD and is almost always an operator typo; require
	// absolute so the failure mode is a clear message instead of
	// "Permission denied (publickey)".
	if !filepath.IsAbs(keyPath) {
		return "", fmt.Errorf("SetAdvertisedRoutes(%s): ssh_key_path must be absolute (got %q)", nodeHostname, keyPath)
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
		// B266 hardening: never let the operator's ssh_config inject a
		// command, and never fall back to a different identity.
		"-o", "ProxyCommand=none",
		"-o", "IdentitiesOnly=yes",
	}
	host, port := splitSSHTarget(target)
	if port != "" {
		sshArgs = append(sshArgs, "-p", port)
	}
	// B266: `--` terminates option parsing, so the host is always the
	// first non-option argument even if validation above regresses.
	sshArgs = append(sshArgs, "--", host, cmd)
	sshCmd := exec.Command("ssh", sshArgs...)
	out, err := sshCmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("ssh %s (key %s): %s", target, keyPath, strings.TrimSpace(string(out)))
}
