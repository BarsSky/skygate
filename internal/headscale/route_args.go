// Pure helpers for building the `tailscale set ...` command line.
//
// Extracted from the body of Client.SetAdvertisedRoutes so the de-dup
// logic, the base-route prepending and the AcceptRoutes flag string can
// all be unit-tested without exec'ing ssh.
//
// Behaviour:
//  1. Always prepend 0.0.0.0/0 and ::/0. Both are inserted even if the
//     caller passed them; dedupe is based on a shared `seen` map.
//  2. Routes keep their order after the base pair, but the same string
//     may not appear twice.
//  3. AcceptRoutes is interpreted as the per-node preference stored in
//     exit_servers.accept_routes:
//     -1 → " --accept-routes=false"
//     0 → ""    (do not pass --accept-routes; legacy behaviour)
//     1 → " --accept-routes=true"
//     Anything outside that range is treated as 0.
package headscale

import "strings"

// ExitNodeBaseRoutes are the routes every exit node must advertise for
// the node to remain a useful exit-node. tailscale set --advertise-routes=
// replaces the node's route list atomically; missing these two means the
// node is a regular node, not an exit.
var ExitNodeBaseRoutes = []string{"0.0.0.0/0", "::/0"}

// BuildTailscaleSetRoutes returns the comma-joined route list that
// should go into --advertise-routes=. Always leads with 0.0.0.0/0 and
// ::/0; subsequent routes are deduped against both the base pair and
// any earlier caller-supplied route. Order of the caller-supplied
// routes is preserved.
func BuildTailscaleSetRoutes(routes []string) string {
	base := append([]string{}, ExitNodeBaseRoutes...)
	seen := map[string]bool{base[0]: true, base[1]: true}
	for _, r := range routes {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		base = append(base, r)
	}
	return strings.Join(base, ",")
}

// AcceptRoutesFlag returns the `--accept-routes=true|false` flag
// fragment to append to a `tailscale set` command, or "" when AcceptRoutes
// is unset (default behaviour — leave the node's existing flag alone).
//
//	-1 → " --accept-routes=false" (recommended for nodes that co-host
//	                                another VPN server, e.g. Amnezia-AWG;
//	                                without --accept-routes=false the node
//	                                pulls peer subnets into source-routing
//	                                table 52 and Telegram/Google from the
//	                                other VPN get black-holed)
//	 0 → ""                           (do not touch the node flag)
//	 1 → " --accept-routes=true"     (legacy full-accept behaviour)
//
// Any other int is treated as 0.
func AcceptRoutesFlag(acceptRoutes int) string {
	switch acceptRoutes {
	case -1:
		return " --accept-routes=false"
	case 1:
		return " --accept-routes=true"
	default:
		return ""
	}
}

// BuildTailscaleSetCommand is the full command string passed to ssh.
// It is provided for callers that want to log or audit the exact command
// without invoking SetAdvertisedRoutes. Args are not shell-quoted; the
// caller is responsible for not supplying user input that contains
// shell metacharacters.
func BuildTailscaleSetCommand(routes []string, acceptRoutes int) string {
	return "tailscale set --advertise-exit-node --advertise-routes=" +
		BuildTailscaleSetRoutes(routes) +
		AcceptRoutesFlag(acceptRoutes)
}
