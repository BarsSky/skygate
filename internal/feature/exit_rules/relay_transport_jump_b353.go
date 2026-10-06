// relay_transport_jump_b353.go — B353 (2026-10-06).
//
// THE TAILNET PATH IS NOT AUTOMATICALLY A PATH — AND A PEER RELAY USUALLY HAS ONE.
//
// B310 taught the transport to prefer the tailnet address of a relay, because that
// is the path a provider-level block of the relay's public IP cannot take away. It
// still assumed that "skygate is on the tailnet" implies "skygate can reach this
// relay", and on the reference deployment that assumption failed outright:
//
//	exit-node sync(emilia): tailnet 100.64.0.3 is not answering: dial tcp
//	  100.64.0.3:22: i/o timeout; tailnet fd7a:115c:a1e0::3 is not answering: …
//	container: tailscale ping 100.64.0.3 → timed out (karolina/sharlotta → pong)
//	emilia (its own words): skygate-host online=true relay=waw cur='' lastHS=never
//	container: emilia relay="hel"; derp28b/c/d.tailscale.com → i/o timeout
//	emilia:    derp28b/c/d.tailscale.com → OPEN, hel 13.4ms
//
// The portal and the relay were both on the tailnet and there was still no path:
// emilia's DERP home was Helsinki, this site's network blocks those Hetzner
// addresses, and no direct connection had ever completed, so every packet the
// portal sent towards emilia went to a relay region it could not reach. Meanwhile
// `karolina` — a peer relay on the same tailnet, reachable from the portal — reached
// emilia over the tailnet without any trouble (`nc -z 100.64.0.3 22` → OPEN).
//
// A/B on that deployment, with everything else held constant: emilia homed at waw →
// `pong from emilia via DERP(waw) in 85ms`, TCP22 OPEN, ssh OK; homed at hel → ping
// timeout, TCP22 blocked, ssh timeout. So the fix cannot be "prefer the tailnet
// address" — it has to be "ask a peer that can actually get there".
//
// # WHAT THIS ADDS
//
// A LAST RUNG on the B310 ladder: for a relay whose tailnet address is unreachable,
// candidates that reach it THROUGH a peer relay (`ssh -J <peer>`). They are appended
// after every direct path, so a healthy relay never pays for a hop, and the hop is:
//
//   - another row in `exit_servers` (the table where relays are configured);
//   - provably reachable right now — the hop itself is TCP-probed before use, and
//     that probe is what the failure report names;
//   - never the target and never one of the target's own addresses;
//   - ordered proven-first (a relay skygate has already applied routes to), because
//     "this relay answers the portal" is the only evidence available from inside the
//     container.
//
// The hop needs no key of its own: `ssh -J` tunnels TCP, so the SAME identity
// authenticates both legs — which is why this works on the deployment that motivated
// it (every relay authorises the portal's management key).
//
// Deliberately NOT changed: which relay owns which prefix (B274/B275), the direct
// ladder's order and wording, and the local transport.
package exit_rules

import (
	"database/sql"
	"log"
	"sort"
	"strconv"
	"strings"

	"skygate/internal/headscale"
)

// RelayEndpointJump is the transport kind recorded when the routes travelled
// through a PEER relay instead of straight from the portal. It lands in
// `relay_apply_via:<relay>` and is rendered on /admin/exit-nodes, so an operator
// reading "tailnet-jump 100.64.0.3 via root@100.64.0.2" sees both ends of the path
// management actually used.
const RelayEndpointJump = "tailnet-jump"

// maxJumpHops bounds how many peer relays ONE unreachable target is retried
// through. Each hop costs a TCP probe plus an ssh attempt, and the staggered sync
// walks every relay on every tick — an unbounded list would turn one broken relay
// into N extra ssh calls per pass. Three survives the realistic case (a handful of
// relays, one of them healthy) and the truncation is logged, never silent.
const maxJumpHops = 3

// jumpHopRow is the `exit_servers` column set the hop decision needs, plus the one
// fact that lives in `global_settings` (Proven).
type jumpHopRow struct {
	Hostname    string
	TailscaleIP string
	SSHPort     string
	SSHTarget   string
	// Proven is true when skygate has already applied routes to this relay
	// (`relay_apply_state:<relay>`), i.e. this relay demonstrably answers the
	// portal.
	Proven bool
}

// jumpHop is one usable `ssh -J` hop.
type jumpHop struct {
	// Hostname is the relay's own name, for the log and the failure text.
	Hostname string
	// Target is the `[user@]host[:port]` handed to `ssh -J`.
	Target string
	// Why names the evidence behind the choice.
	Why string
	// Proven mirrors jumpHopRow.Proven and drives the ordering.
	Proven bool
}

// hopAddressFor renders the address the portal should use to reach this hop.
//
// The tailnet address wins (it is the path that survives a block of the relay's
// public address), then the operator's `ssh_target` — which may itself be a public
// address, and that is fine: the hop only has to be reachable FROM THE PORTAL and
// able to reach the target, not to be on any particular network itself. A row with
// neither is not a candidate: jumping to a bare hostname the portal cannot resolve
// only adds a third way to fail.
//
// Pure (unit-tested).
func hopAddressFor(row jumpHopRow) (target, why string) {
	user := ""
	if u, _, _ := splitSSHEndpoint(row.SSHTarget); u != "" {
		user = u
	}
	if ip := parseOneTailscaleIP(row.TailscaleIP); ip != "" {
		port := strings.TrimSpace(row.SSHPort)
		if _, _, p := splitSSHEndpoint(row.SSHTarget); p != "" {
			port = p
		}
		ep := RelayEndpoint{User: firstNonEmpty(user, "root"), Host: ip, Port: port}
		return ep.Target(), "its tailnet address"
	}
	if t := strings.TrimSpace(row.SSHTarget); t != "" {
		return t, "the operator's ssh_target"
	}
	return "", ""
}

// selectJumpHops picks the relays worth trying as a `ProxyJump` hop for `target`.
//
// `rows` are the other `exit_servers` rows; `targetAddrs` are every address the
// target itself is known by, so a relay is never asked to hop to itself (the hop is
// exactly the relay the portal CAN reach, and the target is the one it cannot).
//
// Order: proven relays first (a relay skygate has applied routes to has answered the
// portal before), then by hostname, so the attempt list is stable across runs — an
// operator comparing two ticks must not see the candidates shuffled.
//
// Pure (unit-tested).
func selectJumpHops(rows []jumpHopRow, target string, targetAddrs []string) []jumpHop {
	skip := map[string]bool{}
	if t := strings.ToLower(strings.TrimSpace(target)); t != "" {
		skip[t] = true
	}
	for _, a := range targetAddrs {
		skip[strings.ToLower(strings.TrimSpace(a))] = true
	}

	hops := []jumpHop{}
	for _, row := range rows {
		name := strings.TrimSpace(row.Hostname)
		if name == "" || skip[strings.ToLower(name)] {
			continue
		}
		addr, how := hopAddressFor(row)
		if addr == "" {
			continue
		}
		// The address itself may also be one of the target's (a duplicate row under
		// another name); never hop to the thing that cannot be reached.
		if _, hopHost, _ := splitSSHEndpoint(addr); hopHost != "" && skip[strings.ToLower(hopHost)] {
			continue
		}
		why := how + " — this relay is reachable from the portal, but has never been used as a hop"
		if row.Proven {
			why = how + " — skygate has already applied routes to this relay (it answers the portal)"
		}
		hops = append(hops, jumpHop{Hostname: name, Target: addr, Why: why, Proven: row.Proven})
	}
	sort.SliceStable(hops, func(i, j int) bool {
		if hops[i].Proven != hops[j].Proven {
			return hops[i].Proven
		}
		return hops[i].Hostname < hops[j].Hostname
	})
	return hops
}

// readJumpHopRows loads every relay's connection facts from `exit_servers`, plus the
// proven flag from the B309 apply record.
//
// A read failure is reported and degrades to "no hops": the ladder then behaves
// exactly as it did before B353, which is the honest outcome for a database problem —
// the relay stays unreachable and the existing failure report says why.
func readJumpHopRows(d *sql.DB, target string) []jumpHopRow {
	if d == nil {
		return nil
	}
	rows, err := d.Query(`SELECT COALESCE(hostname, ''), COALESCE(tailscale_ip, ''), COALESCE(ssh_port, ''), COALESCE(ssh_target, '') FROM exit_servers`)
	if err != nil {
		log.Printf("exit-node sync(%s): B353 cannot read the relay table for jump hops: %v", target, err)
		return nil
	}
	defer rows.Close()
	proven := provenRelays(d)
	var out []jumpHopRow
	for rows.Next() {
		var r jumpHopRow
		if err := rows.Scan(&r.Hostname, &r.TailscaleIP, &r.SSHPort, &r.SSHTarget); err != nil {
			log.Printf("exit-node sync(%s): B353 cannot read a relay row: %v", target, err)
			continue
		}
		r.Proven = proven[strings.ToLower(strings.TrimSpace(r.Hostname))]
		out = append(out, r)
	}
	return out
}

// provenRelays returns the lower-cased hostnames with a successful
// `relay_apply_state:<relay>` record — the same evidence B309/B352 use to decide
// which relays may carry prefixes.
func provenRelays(d *sql.DB) map[string]bool {
	out := map[string]bool{}
	if d == nil {
		return out
	}
	rows, err := d.Query(`SELECT key, COALESCE(value, '') FROM global_settings WHERE key LIKE $1`, SettingRelayApplyStatePrefix+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key, val string
		if rows.Scan(&key, &val) != nil {
			continue
		}
		if !ParseRelayApplyState(val).OK {
			continue
		}
		out[strings.ToLower(strings.TrimPrefix(key, SettingRelayApplyStatePrefix))] = true
	}
	return out
}

// appendJumpCandidates adds the B353 rung to a relay's candidate list: for each of
// the target's tailnet addresses, one candidate per peer relay worth hopping through.
//
// Only TAILNET targets are wrapped. That is the case this block exists for (the
// portal's own route to the relay's tailnet address is gone while the target is
// perfectly healthy on the tailnet), and it keeps the attempt count bounded: the
// operator's public target is reachable from the portal or it is not, so a hop would
// only add a second way to time out.
func appendJumpCandidates(d *sql.DB, hs *headscale.Client, cfg relaySSHConfig, node string, cands []RelayEndpoint) ([]RelayEndpoint, []string) {
	addrs := tailnetAddressesOf(hs, cfg, node)
	if len(addrs) == 0 {
		return cands, nil
	}
	rows := readJumpHopRows(d, node)
	if len(rows) == 0 {
		return cands, nil
	}
	hops := selectJumpHops(rows, node, append(append([]string{}, addrs...), node))
	if len(hops) == 0 {
		return cands, nil
	}
	var notes []string
	if len(hops) > maxJumpHops {
		omitted := make([]string, 0, len(hops)-maxJumpHops)
		for _, h := range hops[maxJumpHops:] {
			omitted = append(omitted, h.Hostname)
		}
		notes = append(notes, "more relays could carry this hop ("+strings.Join(omitted, ", ")+
			") — only the first "+strconv.Itoa(maxJumpHops)+" are tried")
		hops = hops[:maxJumpHops]
	}

	for _, ip := range addrs {
		// Reuse the spelling the direct tailnet candidate for this address already
		// uses (user + port), so the only difference between the two attempts is the
		// hop itself.
		base, ok := directCandidateForHost(cands, ip)
		if !ok {
			base = RelayEndpoint{User: "root", Host: ip, Port: cfg.SSHPort}
		}
		for _, h := range hops {
			cands = append(cands, RelayEndpoint{
				Kind: RelayEndpointJump,
				User: base.User,
				Host: ip,
				Port: base.Port,
				Jump: h.Target,
				Why:  "through " + h.Hostname + " (" + h.Why + ")",
			})
		}
	}
	if !SkygateTailnetState().Ready {
		notes = append(notes, "the portal itself is not on the tailnet, so only a hop reachable by its own address can carry this connection")
	}
	return cands, notes
}

// directCandidateForHost finds the direct (non-jump) candidate that targets `host`,
// so a jump candidate can copy its user/port verbatim.
func directCandidateForHost(cands []RelayEndpoint, host string) (RelayEndpoint, bool) {
	for _, c := range cands {
		if c.Jump == "" && strings.EqualFold(c.Host, host) {
			return c, true
		}
	}
	return RelayEndpoint{}, false
}
