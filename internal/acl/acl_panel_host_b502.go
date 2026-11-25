package acl

// acl_panel_host_b502.go — WHO IS `skygate` ON THE TAILNET, for the `ssh` rule.
//
// THE BUG (measured on the reference deployment, 2026-11-19)
// ---------------------------------------------------------
// The generated `ssh` rule allowed `root` into `tag:exit-node` from
//
//	tag:private, <admin>@<baseDomain>, <tags matching the prefix tag:dev-infra-skygate>
//
// The prefix was the whole assumption: that the panel host's infra tag always
// carries the literal word `skygate`. The reference host is named `svyatoslava`,
// so its tag is `tag:dev-infra-svyatoslava` (the B111 infra-retag flow mints
// `tag:dev-infra-<hostname>`), the prefix filter dropped it, and the rule's `src`
// never named the identity that actually opens the connection. Tailscale SSH on
// every relay answered
//
//	tailscale: tailnet policy does not permit you to SSH to this node
//	Connection closed by 100.64.0.9 port 22
//
// for aisha, emilia, sharlotta and karolina. Only the relays that could be reached
// some OTHER way still worked — emilia through a public sshd, karolina through a
// non-intercepting port (18022) — which is exactly the pattern that hid the defect:
// the transport ladder kept succeeding on the two paths tailscaled does not police.
//
// The fix resolves the panel host's identity instead of guessing it from a name
// prefix. The ladder, most authoritative first:
//
//  1. the panel host's own hostname, matched against `node_owner_map` (the
//     ownership record the rest of this generator already trusts — B284/B287);
//     the hostname comes from SKYGATE_HOST_TS / SKYGATE_HOSTNAME (a container's
//     os.Hostname() is its container id, so the env override comes first) and
//     from os.Hostname() (correct on every native install);
//  2. the legacy `tag:dev-infra-skygate*` prefix, so an existing deployment whose
//     host really is named `skygate-host-1` keeps the exact policy it had;
//  3. nothing — and then the generator SAYS SO in the log, because the symptom is
//     a Tailscale SSH refusal half a minute later on a different page, and the
//     operator has no way to connect the two.
//
// What this deliberately does NOT do: remove the panel host from the infra
// catch-all. `getInfraExitNodeTags` skips `tag:dev-infra-skygate*` and therefore
// does NOT skip a hostname-derived tag, so `src: ["*"] → dst: [<panel tag>]` stays
// in the policy — that grant is how devices reach the panel over the tailnet on the
// reference deployment, and deleting it here would be a silent access change
// dressed as a cleanup. The asymmetry is recorded in docs/troubleshooting.md §19.

import (
	"database/sql"
	"log"
	"os"
	"strings"
	"sync"

	"skygate/internal/db"
)

// panelHostOSHostname is the last rung of the hostname ladder, as a VARIABLE so a test can
// make «this host is not in node_owner_map» TRUE instead of hoping for it (B522).
//
// Measured 2026-11-25 on the reference install: TestB502_UnresolvedHostSaysSoAndKeepsThe-
// PolicyValid set both env variables to names that cannot exist and demanded "nothing
// resolved". The ladder then fell through to os.Hostname() = `svyatoslava`, which IS in
// node_owner_map on that very host, so the resolver returned tag:dev-infra-svyatoslava and
// the test failed — the host answering a question about the product. Because the gate runs
// `go test ./...`, this single line of environment dragged eleven other checks down.
var panelHostOSHostname = os.Hostname

// panelHostCandidates returns the hostnames this panel can be known by on the
// tailnet, most authoritative first. Case-insensitive matching is the caller's job
// (node_owner_map holds whatever headscale reported).
func panelHostCandidates() []string {
	var raw []string
	for _, env := range []string{"SKYGATE_HOST_TS", "SKYGATE_HOSTNAME"} {
		raw = append(raw, strings.Split(os.Getenv(env), ",")...)
	}
	if h, err := panelHostOSHostname(); err == nil {
		raw = append(raw, h)
	}
	out := make([]string, 0, len(raw)*2)
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(v, ".")))
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, r := range raw {
		add(r)
		// `svyatoslava.tsnet.example.com` and `svyatoslava` are the same node; a
		// node_owner_map row may carry either form.
		if i := strings.Index(r, "."); i > 0 {
			add(r[:i])
		}
	}
	return out
}

// panelHostInfraTags resolves the tag(s) the panel host carries in the tailnet, i.e.
// the identity a relay sees when skygate opens an SSH connection. It returns the
// resolved tags and a human-readable source for the log line (never for the policy).
//
// Best effort by design: a read failure or an unmatched hostname returns nil rather
// than failing the whole policy generation — a policy without the panel tag is the
// pre-B502 behaviour, and refusing to apply any policy at all would be worse than
// the bug being fixed.
func panelHostInfraTags(d *sql.DB, tagsByUser map[string][]string) (tags []string, source string) {
	cands := panelHostCandidates()
	legacy := getSkygateHostInfraTags(tagsByUser)

	if d != nil && len(cands) > 0 {
		if owners, err := db.ListAllNodeOwners(d); err == nil {
			byHost := make(map[string][]string, len(owners))
			for _, o := range owners {
				host := strings.ToLower(strings.TrimSpace(o.Hostname))
				tag := strings.TrimSpace(o.Tag)
				user := strings.ToLower(strings.TrimSpace(o.Username))
				if host == "" {
					continue
				}
				if tag != "" {
					byHost[host] = append(byHost[host], tag)
				}
				// The per-device tag is DERIVED from (username, hostname) by
				// qSelectPerUserDeviceTags, and that derivation — not the ownership
				// row's `tag` column — is what the rest of this generator puts in
				// tagOwners. When the row carries a class tag (`tag:exit-node`) or no
				// tag at all, the derived name is the one the node actually has.
				if user != "" {
					byHost[host] = append(byHost[host], "tag:dev-"+user+"-"+host)
				}
			}
			for _, c := range cands {
				if hits, ok := byHost[c]; ok && len(hits) > 0 {
					return dedupeTags(append(hits, legacy...)), "node_owner_map hostname " + c
				}
			}
		}
	}
	if len(legacy) > 0 {
		return legacy, "legacy tag:dev-infra-skygate* prefix"
	}
	where := "no hostname candidates"
	if len(cands) > 0 {
		where = "node_owner_map has no row for " + strings.Join(cands, ", ")
	}
	return nil, "unresolved — " + where
}

// dedupeTags keeps the first occurrence of each tag and preserves order, so a tag
// that is both hostname-resolved and legacy-matched is emitted once.
func dedupeTags(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var (
	panelTagLogMu  sync.Mutex
	panelTagLogged string
)

// logPanelHostTags makes the resolution visible exactly when it CHANGES (or fails),
// because the failure mode is silence: a missing tag produces a perfectly valid
// policy and a refusal on a different page half a minute later.
func logPanelHostTags(tags []string, source string) {
	key := strings.Join(tags, ",")
	panelTagLogMu.Lock()
	changed := key != panelTagLogged
	if changed {
		panelTagLogged = key
	}
	panelTagLogMu.Unlock()
	if !changed {
		return
	}
	if len(tags) == 0 {
		log.Printf("acl: B502 — the ssh rule does NOT name this host (%s): Tailscale SSH from skygate to a relay whose port 22 is tailscaled's will be refused with 'tailnet policy does not permit you to SSH to this node'. Set SKYGATE_HOST_TS to this host's tailscale name, or tag the host node in node_owner_map", source)
		return
	}
	log.Printf("acl: ssh rule src includes the panel host tag(s) %s (%s)", strings.Join(tags, ", "), source)
}
