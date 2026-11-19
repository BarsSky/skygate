// relay_exec_b501.go — B501 (2026-11-19).
//
// «НУЖЕН МЕХАНИЗМ ПЕРЕПОДКЛЮЧЕНИЯ В ВЕБИНТЕРФЕЙСЕ» — the operator's request, and the
// live case behind it: relay `karolina` reported `Nearest DERP: New York City` while the
// operator's own region 900 (`mow`) answered fine, because its tailscaled was holding a
// stale netmap. The fix is a daemon restart — a ten-second operation that until now
// required an SSH session, i.e. exactly the kind of thing an admin page should be able to
// do, audit and verify.
//
// WHAT THIS FILE IS: the smallest primitive that makes it possible — run ONE fixed
// command on a relay over the SAME ssh invocation the route apply already uses
// (`buildSetAdvertisedRoutesArgv`: BatchMode, accept-new host key, bounded connect
// timeout, optional jump host), then read back the state it produced.
//
// It is deliberately NOT a command box: the command is a constant in this file, the
// handler passes a hostname and nothing else, and the only way to change what runs is to
// change the source. `ExecOnRelay` is generic enough for a future action, but no user
// input ever reaches it.
package headscale

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// RelayReconnectCommand restarts the daemon and then REPORTS the state it came back in.
//
// The ladder inside it is ordered by "least invasive that can still refresh the netmap":
//
//	systemctl  — the Debian/Ubuntu service name; a restart keeps the node's stored
//	             preferences (advertised routes, exit-node flag), so the relay comes back
//	             as the same relay;
//	rc-service — OpenRC hosts (Alpine), same reasoning;
//	down/up    — a last resort for a host whose service manager skygate cannot reach.
//	             `tailscale up` re-applies the stored prefs; if the node key has expired
//	             this fails loudly instead of silently leaving the relay offline, and the
//	             caller reports the error.
//
// The trailing `tailscale status --json` is what turns "I ran a command" into "the relay
// is back, and on region X" — the same distinction B451 made for route applies.
const RelayReconnectCommand = `(systemctl restart tailscaled 2>/dev/null || rc-service tailscaled restart 2>/dev/null || (tailscale down && tailscale up)) ; sleep 3 ; tailscale status --json`

// ExecOnRelay runs ONE fixed command on a relay and returns its combined output.
//
// The ssh invocation is built by `buildSetAdvertisedRoutesArgv`, i.e. the exact shape the
// route apply uses, so a transport that works for routes works here too (including the
// B353 jump rung). `keyPath` goes through SSHKeyProblem, which reports a missing or
// relative key as a clear message instead of "Permission denied (publickey)".
func (c *Client) ExecOnRelay(target, keyPath, jump, command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("ExecOnRelay: empty command")
	}
	if problem := SSHKeyProblem(keyPath); problem != "" {
		return "", fmt.Errorf("ExecOnRelay(%s): %s — %s", target, problem, SSHKeyFixHint(keyPath))
	}
	host, port := splitSSHTarget(target)
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("ExecOnRelay: empty ssh target")
	}
	argv := buildSetAdvertisedRoutesArgv(keyPath, host, port, command, jump)
	out, err := exec.Command("ssh", argv...).CombinedOutput()
	text := string(out)
	if err != nil {
		return text, fmt.Errorf("relay %s: the command did not run: %s (%s)",
			target, firstLine(strings.TrimSpace(text)), firstLine(err.Error()))
	}
	return text, nil
}

// RelaySelfState is the slice of `tailscale status --json` a reconnect reports back.
type RelaySelfState struct {
	HostName string
	Relay    string
	Online   bool
	// BackendState is "Running" on a healthy daemon; any other value explains a relay
	// that restarted but did not come back.
	BackendState string
}

// ParseRelaySelfState extracts the relay's OWN view from a `tailscale status --json`
// dump, tolerating the other lines the reconnect command may print before it (a
// resumed `tailscale up` can emit a login URL). Pure, so the tolerance is testable.
func ParseRelaySelfState(raw string) (RelaySelfState, error) {
	start := strings.Index(raw, "{")
	if start < 0 {
		return RelaySelfState{}, fmt.Errorf("the relay printed no JSON status (output: %s)", firstLine(strings.TrimSpace(raw)))
	}
	var doc struct {
		Self struct {
			HostName     string `json:"HostName"`
			Relay        string `json:"Relay"`
			Online       bool   `json:"Online"`
			BackendState string `json:"BackendState"`
		} `json:"Self"`
	}
	if err := json.Unmarshal([]byte(raw[start:]), &doc); err != nil {
		return RelaySelfState{}, fmt.Errorf("the relay's status JSON did not parse: %v", err)
	}
	if doc.Self.HostName == "" && doc.Self.Relay == "" && doc.Self.BackendState == "" {
		return RelaySelfState{}, fmt.Errorf("the relay's status JSON carried no Self block")
	}
	return RelaySelfState{
		HostName:     doc.Self.HostName,
		Relay:        doc.Self.Relay,
		Online:       doc.Self.Online,
		BackendState: doc.Self.BackendState,
	}, nil
}
