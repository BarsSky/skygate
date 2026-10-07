// internal/headscale/ssh_tunnel_b356.go — B356.1 (2026-10-07).
//
// A TCP TUNNEL THROUGH A RELAY, NOT A COMMAND ON THE RELAY.
//
// B353 taught the exit-node sync to reach a relay THROUGH a peer relay, and it spelled
// the hop out as an explicit `ssh -W '[%h]:%p' <hop>` ProxyCommand (see
// jumpProxyCommand in routes.go) instead of the implicit `-J`, because the implicit
// form inherits neither the identity nor the host-key policy (measured live on
// OpenSSH 10.3p1: `Permission denied (publickey,password)`).
//
// B356.1 needs the SAME shape for a different reason: on a deployment where the
// Telegram Bot API is blocked on the portal's own egress path (the ISP/host blocks
// 149.154.x), a peer relay on the same tailnet can reach it. The Bot API request must
// stay end-to-end TLS between this process and api.telegram.org — so the relay only
// moves bytes and the bot token can never appear in the relay's process list, argv or
// logs. That is exactly `ssh -W <dest>:<port> -- <relay>` with the child's
// stdin/stdout used as the socket.
//
// This file owns the ONE place that decides that argv, and it reuses every safety
// property the project already paid for:
//
//   - IsSafeSSHTarget (B266) gates the relay target — the value comes from the
//     operator-writable `exit_servers` table, so it must never become an ssh OPTION
//     (a leading dash) or carry shell metacharacters;
//   - jumpProxyCommandAllowed (B353) gates the key path, for the same reason: the key
//     path is operator-writable and must not be able to change the meaning of the
//     command;
//   - `-o ProxyCommand=none` + `-o IdentitiesOnly=yes` + `--` before the relay keep the
//     B266 properties: the operator's ssh_config can neither redirect the connection
//     nor offer a different identity, and option parsing ends before the host.
//
// WHY `-W` COMES BEFORE `--`. `-W host:port` is an OPTION that asks ssh to forward
// stdin/stdout to a remote destination; everything after the destination argument is
// the REMOTE COMMAND. `ssh -- relay -W host:port` would therefore try to run a program
// called `-W` ON the relay — the bot token would still be safe, but nothing would
// tunnel. The argv order is pinned by a unit test (TestB356_1_*) and by contract A3 of
// scripts/check_b356_1_telegram_relay_fallback.sh.
package headscale

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// SplitSSHTarget exposes the `[user@]host[:port]` splitter used to build every ssh
// argv in this package. Callers that keep relay connection facts (exit_servers'
// ssh_target / ssh_port) must parse them the same way the argv does, or the page and
// the process disagree — the class B292 was about.
//
// Pure (unit-tested).
func SplitSSHTarget(target string) (host, port string) {
	return splitSSHTarget(strings.TrimSpace(target))
}

// sshTunnelDestRe is the only shape accepted for the TUNNEL DESTINATION (the far end
// of `-W`): a hostname or an address literal, anchored, no leading dash, no
// whitespace, no shell metacharacters, no `:` (the port is a separate argument of this
// function and is rendered by NormalizeTunnelDest).
var sshTunnelDestRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// SSHTunnelKeyProblem names why a private-key path cannot be handed to `ssh`, or ""
// when it is usable. SHAPE ONLY — no filesystem probe, so this stays pure and cheap;
// callers pair it with SSHKeyProblem (which does the stat) when they want the whole
// verdict.
//
// The rule is B353's jumpProxyCommandAllowed: the key path reaches an argv element
// that comes from the operator-writable `exit_servers` table, so whitespace or a shell
// metacharacter must be refused by name instead of being passed along.
//
// Pure (unit-tested).
func SSHTunnelKeyProblem(keyPath string) string {
	p := strings.TrimSpace(keyPath)
	if p == "" {
		return "no ssh key configured"
	}
	if !jumpProxyCommandAllowed(p) {
		return fmt.Sprintf("ssh key path %q contains whitespace or a shell metacharacter", p)
	}
	return ""
}

// NormalizeTunnelDest validates the far end of the tunnel and renders the
// `host:port` form ssh's `-W` option expects. An IPv6 literal is bracketed (the same
// B353.1 lesson: `-W fd7a::3:443` is ambiguous, `-W [fd7a::3]:443` is not).
//
// Pure (unit-tested).
func NormalizeTunnelDest(destHost string, destPort int) (string, error) {
	if destPort < 1 || destPort > 65535 {
		return "", fmt.Errorf("ssh tunnel: destination port %d is not a legal TCP port", destPort)
	}
	h := strings.TrimSpace(destHost)
	if h == "" || len(h) > 253 {
		return "", fmt.Errorf("ssh tunnel: refusing destination host %q", destHost)
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		if ip.To4() != nil {
			return ip.String() + ":" + strconv.Itoa(destPort), nil
		}
		return "[" + ip.String() + "]:" + strconv.Itoa(destPort), nil
	}
	if !sshTunnelDestRe.MatchString(h) {
		return "", fmt.Errorf("ssh tunnel: refusing unsafe destination host %q (expected a hostname or an address literal)", destHost)
	}
	return h + ":" + strconv.Itoa(destPort), nil
}

// SSHTunnelArgv composes the argv for one TCP tunnel:
//
//	ssh -i <key> -o BatchMode=yes … -W <destHost>:<destPort> -- <relayTarget>
//
// The caller runs it with exec.Command("ssh", argv...) and uses the child's
// stdin/stdout as the connection, so the TLS session belongs to THIS process and the
// relay never sees the plaintext payload (B356.1 requirement 2).
//
// Every input is validated before it can reach an argv: `relayTarget` with
// IsSafeSSHTarget, `keyPath` with SSHTunnelKeyProblem, and the destination with
// NormalizeTunnelDest. A refusal is a NAMED error — never a silently different
// connection.
//
// Pure (unit-tested).
func SSHTunnelArgv(keyPath, relayTarget, destHost string, destPort int) ([]string, error) {
	key := strings.TrimSpace(keyPath)
	if problem := SSHTunnelKeyProblem(key); problem != "" {
		return nil, fmt.Errorf("ssh tunnel: %s", problem)
	}
	hop := strings.TrimSpace(relayTarget)
	if !IsSafeSSHTarget(hop) {
		return nil, fmt.Errorf("ssh tunnel: refusing unsafe relay target %q (expected [user@]host[:port])", relayTarget)
	}
	dest, err := NormalizeTunnelDest(destHost, destPort)
	if err != nil {
		return nil, err
	}
	relayHost, relayPort := splitSSHTarget(hop)
	args := []string{
		"-i", key,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		// Keep the tunnel honest about a relay that goes away: without these, a
		// silently dropped path blocks a read until the caller's own deadline.
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		"-o", "ProxyCommand=none",
		"-o", "IdentitiesOnly=yes",
	}
	if relayPort != "" {
		args = append(args, "-p", relayPort)
	}
	// -W is an OPTION (see the file doc): it must precede `--`, which terminates
	// option parsing before the relay host.
	args = append(args, "-W", dest, "--", relayHost)
	return args, nil
}
