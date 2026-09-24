package admin

// tailscale_selfname_b320.go — B320: the reserved tailnet name must belong to the
// LIVE client, not to a dead registration.
//
// # THE LIVE CONFLICT (agent VM, 2026-09-24)
//
// Operator report: «Посмотри не переприменился ли skygate-host — он offline и это
// tailscale клиент при skygate при этом появился skygate-host-1 и если это тоже
// клиент tailscale при skygate то у нас явно конфликт …»
//
// `headscale nodes list` showed exactly that:
//
//	id=57  given_name="skygate-host"    name="skygate-host-1-1"  tag:dev-infra-skygate-host
//	       ip=100.64.0.9   OFFLINE since 2026-09-21 21:32  (machine key #1)
//	id=87  given_name="skygate-host-1"  name="skygate-host-1"    tag:dev-infra-skygate-host-1
//	       ip=100.64.0.10  ONLINE, this container        (machine key #2)
//
// while the LIVE client said so itself:
//
//	$ tailscale status --json | .Self.HostName  →  "skygate-host-1"
//	$ docker exec … env | grep SKYGATE_TS_HOSTNAME →  skygate-host-1
//	  (pinned in BOTH .env and docker-compose.yml)
//
// So the canonical name is held by a GHOST (a registration from an earlier epoch with a
// different machine key, offline for days) and the real client wears the `-1` suffix.
// The `-1` is not a re-application of anything: it is a **legacy placeholder from
// v0.33.1.9** that B251 fixed in the code (the default is `skygate-host`, and
// `isInfraNode`/`isReservedSelfHostname` match it by strict equality) but which nobody
// removed from the operator's `.env`/compose. That is why the two halves disagree:
// every "is this me?" check (infra ownership attribution, colocation sanity, the
// SSH-source ACL, the self-probes) keys off `skygate-host`, which today is the ghost.
//
// The suffix cascade the operator described is real and visible in the data: the ghost
// had to be given the internal name `skygate-host-1-1` when it registered, i.e. both
// `skygate-host` and `skygate-host-1` were taken at that moment.
//
// # WHAT THIS FILE DOES
//
// It detects the conflict, explains it on /admin/tailscale with both node ids, and
// offers the half that is safe to automate: DELETE the ghost. A node is only ever
// deleted when it is (a) offline, (b) wearing the canonical name, (c) ours by
// attribution (an infra tag or the infra user) and (d) not the live node. Nothing else
// is touched — renaming the live client is a `tailscale set --hostname` away, and the
// env pin that would undo it on the next restart is the operator's file.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"skygate/internal/headscale"
)

// ReservedSelfHostname is the canonical tailnet name of the skygate host itself
// (B251). `isInfraNode` and `isReservedSelfHostname` match it by strict equality, so a
// suffixed client misses every self-check.
const ReservedSelfHostname = "skygate-host"

// SelfNameState is what the page needs to explain the situation.
type SelfNameState struct {
	// Live is the name the running client registered with ("" when the daemon is down).
	Live string
	// Configured is what the configuration asks for (SKYGATE_TS_HOSTNAME / default).
	Configured string
	// Canonical is the reserved name (ReservedSelfHostname).
	Canonical string
	// EnvPinned is the raw SKYGATE_TS_HOSTNAME from the container env, so the page can
	// say that a restart would re-apply a suffixed name.
	EnvPinned string
	// Ghost* describe the OFFLINE node that currently holds the canonical name.
	GhostID   string
	GhostName string
	GhostTag  string
	GhostIP   string
	GhostSeen string
	// NeedsName is true when the live client is NOT wearing the canonical name.
	NeedsName bool
	// Conflict is true when NeedsName AND a ghost holds the canonical name.
	Conflict bool
}

// selfNameState collects the facts. A nil/empty node list (headscale unreachable)
// degrades to "live name only" — the page still says the name is not canonical.
func (s *Service) selfNameState(live string) SelfNameState {
	st := SelfNameState{
		Live:       strings.TrimSpace(live),
		Configured: strings.TrimSpace(s.tailscaleHostname()),
		Canonical:  ReservedSelfHostname,
		EnvPinned:  strings.TrimSpace(envTailscaleHostname()),
	}
	st.NeedsName = st.Live != "" && !strings.EqualFold(st.Live, st.Canonical)
	nodes, err := s.HSGlobalFn().ListAllNodes()
	if err != nil {
		log.Printf("tailscale self-name: list nodes: %v", err)
		return st
	}
	ghost, ok := pickSelfNameGhost(nodes, st.Live, st.Canonical)
	if !ok {
		return st
	}
	st.GhostID = ghost.ID
	st.GhostName = firstNonEmptyAdmin(ghost.GivenName, ghost.Hostname)
	st.GhostTag = firstTag(ghost.Tags)
	st.GhostIP = firstNonEmptyAdmin(ghost.IPAddresses...)
	st.GhostSeen = ghost.LastSeen
	st.Conflict = st.NeedsName
	return st
}

func envTailscaleHostname() string {
	return strings.TrimSpace(getenvTailscaleHostname())
}

// pickSelfNameGhost finds the OFFLINE node that squats the canonical name.
//
// Pure so the guards can be tested without a headscale server. A node qualifies only
// when ALL of these hold:
//
//	name     exactly the canonical name (GivenName, or Hostname when GivenName is empty)
//	offline  never delete a node that is connected — a live node with our name is a
//	         different problem (and not one a delete may solve)
//	ours     an infra-family tag or the infra user; this is what keeps the action from
//	         touching somebody else's device
func pickSelfNameGhost(nodes []headscale.NodeView, live, canonical string) (headscale.NodeView, bool) {
	for _, n := range nodes {
		name := firstNonEmptyAdmin(n.GivenName, n.Hostname)
		if !strings.EqualFold(name, canonical) {
			continue
		}
		// Never the live client itself (it can legitimately hold the canonical name).
		if live != "" && strings.EqualFold(name, live) {
			continue
		}
		if n.Online {
			continue
		}
		if !isInfraFamilyNode(n) {
			continue
		}
		return n, true
	}
	return headscale.NodeView{}, false
}

// isInfraFamilyNode reports whether a node belongs to skygate's own infrastructure:
// an infra tag (`tag:dev-infra-…`, `tag:infra-…`) or the `infra` headscale user.
func isInfraFamilyNode(n headscale.NodeView) bool {
	if strings.EqualFold(strings.TrimSpace(n.UserName), "infra") {
		return true
	}
	for _, t := range n.Tags {
		lt := strings.ToLower(t)
		// The real tags are `tag:dev-infra-<host>` (per-device infra) and the legacy
		// `tag:infra-<host>`. `-infra-` covers the first — the substring after `:dev`
		// is `-infra-`, NOT `:infra-`, and the first version of this helper checked
		// `:infra-` and therefore matched NOTHING (the unit test caught it).
		if strings.Contains(lt, "-infra-") || strings.HasPrefix(lt, "tag:infra") {
			return true
		}
	}
	return false
}

func firstTag(tags []string) string {
	for _, t := range tags {
		if s := strings.TrimSpace(t); s != "" {
			return s
		}
	}
	return ""
}

func firstNonEmptyAdmin(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// getenvTailscaleHostname is a var so tests can pin the env layer.
var getenvTailscaleHostname = func() string { return strings.TrimSpace(os.Getenv("SKYGATE_TS_HOSTNAME")) }

// tailscaleSelfHostname is the name the RUNNING daemon registered with
// (`.Self.HostName`). "" when the daemon does not answer — the page then reports only
// the configured name.
func tailscaleSelfHostname() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return ""
	}
	var st struct {
		Self struct {
			HostName string `json:"HostName"`
		} `json:"Self"`
	}
	if json.Unmarshal(out, &st) != nil {
		return ""
	}
	return strings.TrimSpace(st.Self.HostName)
}

// PostAdminTailscaleReclaimName deletes the offline ghost that holds the reserved
// name, so the live client can take it.
//
// What it deliberately does NOT do: rename the live client. `tailscale set
// --hostname=skygate-host` is a one-line command the page prints (together with the
// `.env`/compose edit that stops `tailscale up --hostname=$SKYGATE_TS_HOSTNAME` from
// re-applying the suffix on the next restart), and doing it behind the operator's back
// while the env still pins the old name would produce a name that silently flips back.
func (s *Service) PostAdminTailscaleReclaimName(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	running, _, _, _, _ := tailscaleStatus()
	state := s.selfNameState(tailscaleSelfHostname())
	if state.GhostID == "" {
		http.Redirect(w, r, "/admin/tailscale?err="+urlFlash("no stale node holds "+ReservedSelfHostname), http.StatusFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.HSGlobalFn().DeleteNode(parseNodeID(state.GhostID)); err != nil {
		log.Printf("tailscale self-name: delete ghost %s (%s): %v", state.GhostID, state.GhostName, err)
		s.Backend.Audit(c.UserID, c.Username, "tailscale.reclaim_name",
			fmt.Sprintf("action=delete_ghost id=%s name=%s err=%v", state.GhostID, state.GhostName, err))
		http.Redirect(w, r, "/admin/tailscale?err="+urlFlash("delete "+state.GhostName+": "+err.Error()), http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale.reclaim_name",
		fmt.Sprintf("action=delete_ghost id=%s name=%s tag=%s ip=%s last_seen=%s (offline duplicate of the reserved name)",
			state.GhostID, state.GhostName, state.GhostTag, state.GhostIP, state.GhostSeen))
	log.Printf("tailscale self-name: deleted the offline duplicate %q (id=%s, %s, last seen %s) that held the reserved name %s; the live client is %q",
		state.GhostName, state.GhostID, state.GhostTag, state.GhostSeen, ReservedSelfHostname, state.Live)
	s.invalidateTailscaleState()
	// The rename itself needs the daemon; say so instead of pretending.
	if !running {
		http.Redirect(w, r, "/admin/tailscale?err="+urlFlash(
			"the stale node is gone, but Tailscale is not running — press Start, then renameself"), http.StatusFound)
		return
	}
	if out, err := renameSelfClient(ctx, ReservedSelfHostname); err != nil {
		http.Redirect(w, r, "/admin/tailscale?err="+urlFlash("rename: "+out+": "+err.Error()), http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale.reclaim_name",
		fmt.Sprintf("action=rename_self to=%s (from %s)", ReservedSelfHostname, state.Live))
	http.Redirect(w, r, "/admin/tailscale?ok="+urlFlash("name_reclaimed"), http.StatusFound)
}

// renameSelfClient asks the running daemon to change its hostname.
func renameSelfClient(ctx context.Context, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "tailscale", "set", "--hostname="+name)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// The remaining helpers are tiny shims over things this package already does
// elsewhere; keeping them here makes the block self-contained.

func parseNodeID(id string) int64 {
	var n int64
	for _, r := range id {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int64(r-'0')
	}
	return n
}

func urlFlash(msg string) string {
	return strings.NewReplacer(" ", "_", "\n", "_", "&", "_", "?", "_").Replace(msg)
}
