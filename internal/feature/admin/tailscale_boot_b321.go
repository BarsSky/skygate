package admin

// tailscale_boot_b321.go — B321: Tailscale must survive a container recreate, and the
// canonical hostname must be the one the tailnet actually gets.
//
// # THE LIVE REPORT (agent VM, 2026-09-25)
//
//   «при этом при каждом обновлении слетает запуск tailscale приходится прожимать
//    каждый раз старт и активным стал skygate-host-1 как skygate tailscale хотя
//    должен быть строго skygate-host»
//
// Two independent halves, both traced on the live host:
//
//  1. "the start is lost on every update". The reference compose pins
//     `SKYGATE_TS_AUTHKEY_FILE=/dev/null` and Docker freezes the environment at
//     container CREATION, so every update (`--force-recreate`) produces a container
//     whose entrypoint sees the /dev/null sentinel and skips tailscaled entirely.
//     The web UI is the only thing that then starts it — which is why the operator
//     has to press Start after every update. Nothing in skygate re-applied the
//     operator's earlier decision, because that decision lived only in a
//     `global_settings` row the ENTRYPOINT has never heard of.
//
//  2. "the active name became skygate-host-1 again". `tailscaleHostname()` returned
//     the frozen `SKYGATE_TS_HOSTNAME` from the same environment, so the Start click
//     ran `tailscale up --hostname=skygate-host-1`: the node was renamed straight
//     back to the legacy v0.33.1.9 placeholder that B251/B320 exist to remove.
//
// # WHAT THIS FILE DOES
//
//  * `tailscale.desired_state` (`on`/`off`) is the operator's intent, written by the
//    Start / Stop / Enable / Disable actions and read by the process ITSELF, so the
//    decision survives a recreate even when the container environment says
//    `/dev/null`. Unset keeps the pre-B321 env-driven behaviour exactly.
//  * `tailscale.hostname` is the desired name, and the legacy placeholder shape
//    `skygate-host-<n>...` is NEVER honoured: `NormalizeTailscaleHostname` maps it to
//    the reserved canonical name (with a log line naming where it came from).
//  * A boot pass (plus a 5-minute tick) brings tailscaled up and enforces the name on
//    the RUNNING daemon (`tailscale up --hostname` does not rename an existing node —
//    `tailscale set --hostname` does).
//  * Reclaiming the name also removes the stale infra tag that carried the OLD name
//    (live: node 87 kept BOTH `tag:dev-infra-skygate-host` and
//    `tag:dev-infra-skygate-host-1` after a successful rename).

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/feature/exit_rules"
)

const (
	// tailscaleDesiredStateDBKey records whether the OPERATOR wants Tailscale up.
	// Values: "" (unset — follow the environment), "on", "off".
	tailscaleDesiredStateDBKey = "tailscale.desired_state"
	// tailscaleHostnameDBKey is the desired tailnet name. Empty falls back to the
	// environment and then to the reserved canonical name.
	tailscaleHostnameDBKey = "tailscale.hostname"

	tailscaleDesiredOn  = "on"
	tailscaleDesiredOff = "off"

	// TailscaleAutostartInterval is how often the running process re-asserts the
	// desired state. Idempotent: a healthy, authenticated client is left alone.
	TailscaleAutostartInterval = 5 * time.Minute

	// tailscaleAutostartBootDelay lets the container entrypoint finish before the
	// boot pass touches the daemon.
	tailscaleAutostartBootDelay = 10 * time.Second
)

// tailscalePlaceholderRe matches the legacy v0.33.1.9 placeholder names — the shape
// headscale produces when several registrations compete for one name
// (`skygate-host-1`, `skygate-host-1-1`, …). B251 made `skygate-host` canonical and
// match it by strict equality, so a suffixed client silently fails every "is this
// me?" check: infra ownership, colocation sanity, the SSH-source ACL, the self-probes.
var tailscalePlaceholderRe = regexp.MustCompile(`^skygate-host(-\d+)+$`)

// NormalizeTailscaleHostname canonicalizes a configured hostname. It returns the name
// to use and whether it had to be rewritten. Pure — the table is unit-tested.
func NormalizeTailscaleHostname(name string) (string, bool) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", false
	}
	if tailscalePlaceholderRe.MatchString(strings.ToLower(n)) {
		return tailscaleHostnameDefault(), true
	}
	return n, false
}

// tailscaleDesiredState returns the operator's recorded intent ("" when unset).
// Nil-safe: unit tests without a DB fall through to "".
func (s *Service) tailscaleDesiredState() string {
	if s == nil || s.DB == nil {
		return ""
	}
	v, err := db.GetGlobalSetting(s.dbc(), tailscaleDesiredStateDBKey, "")
	if err != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case tailscaleDesiredOn:
		return tailscaleDesiredOn
	case tailscaleDesiredOff:
		return tailscaleDesiredOff
	}
	return ""
}

// setTailscaleDesiredState records the operator's intent. An empty value clears it.
func (s *Service) setTailscaleDesiredState(state string) error {
	return db.SetGlobalSetting(s.dbc(), tailscaleDesiredStateDBKey, strings.ToLower(strings.TrimSpace(state)))
}

// tailscaleHostnameResolved resolves the name the tailnet should see, in the order
// DB > environment > reserved default, and rewrites the legacy placeholder shape.
// Returns (name, source, rewritten) with source one of "db" / "env" / "default".
func (s *Service) tailscaleHostnameResolved() (string, string, bool) {
	raw, source := "", "default"
	if s != nil && s.DB != nil {
		if v, err := db.GetGlobalSetting(s.dbc(), tailscaleHostnameDBKey, ""); err == nil && strings.TrimSpace(v) != "" {
			raw, source = strings.TrimSpace(v), "db"
		}
	}
	if raw == "" && s != nil && strings.TrimSpace(s.TailscaleHostname) != "" {
		raw, source = strings.TrimSpace(s.TailscaleHostname), "env"
	}
	if raw == "" {
		raw = tailscaleHostnameDefault()
	}
	name, rewritten := NormalizeTailscaleHostname(raw)
	return name, source, rewritten
}

// tailscaleAutostartDecision answers "should this process bring Tailscale up by
// itself, and why". The operator's recorded intent outranks the frozen container
// environment — that is the whole point of B321 — while an install that never
// touched the panel keeps the exact pre-B321 env-driven behaviour.
func (s *Service) tailscaleAutostartDecision() (bool, string) {
	if !tailscaleAvailableFn() {
		return false, "the tailscale binaries are not on PATH in this process"
	}
	switch s.tailscaleDesiredState() {
	case tailscaleDesiredOff:
		return false, "the panel records Tailscale as OFF (tailscale.desired_state=off)"
	case tailscaleDesiredOn:
		if set, _ := s.readTailscaleAuthKey(); !set {
			return false, "the panel records Tailscale as ON but there is no usable auth key at " +
				s.tailscaleAuthKeyPath()
		}
		return true, "the panel records Tailscale as ON (tailscale.desired_state=on)"
	}
	// Unset: pre-B321 behaviour, driven by the environment/DB auth-key path.
	if s.tailscaleAuthKeyDisabled() {
		return false, "no panel intent and " + s.tailscaleAuthKeyPath() +
			" disables Tailscale in this container (set it from /admin/tailscale to make this survive updates)"
	}
	if set, _ := s.readTailscaleAuthKey(); !set {
		return false, "no panel intent and no usable auth key at " + s.tailscaleAuthKeyPath()
	}
	return true, "the auth key at " + s.tailscaleAuthKeyPath() + " is configured in the environment"
}

// tsAutostartLogOnce logs a message only when it differs from the previous one, so a
// deliberately disabled install does not write a line every 5 minutes.
var tsAutostartLog struct {
	mu   sync.Mutex
	last string
}

func tsAutostartLogOnce(msg string) {
	tsAutostartLog.mu.Lock()
	changed := tsAutostartLog.last != msg
	tsAutostartLog.last = msg
	tsAutostartLog.mu.Unlock()
	if changed {
		log.Printf("tailscale-autostart: %s", msg)
	}
}

// EnsureTailscaleUp brings the client up if the recorded intent says it should be up,
// and enforces the resolved hostname on the RUNNING daemon. Idempotent: it reuses a
// daemon that already answers and calls `tailscale up` with the same key, which
// Tailscale treats as a no-op. Never fatal — every failure is logged and returned.
func (s *Service) EnsureTailscaleUp(reason string) (bool, string) {
	enabled, why := s.tailscaleAutostartDecision()
	if !enabled {
		tsAutostartLogOnce(reason + ": not starting: " + why)
		return false, why
	}
	name, source, rewritten := s.tailscaleHostnameResolved()
	if rewritten {
		log.Printf("tailscale-autostart: %s: the configured hostname (source=%s) is the legacy "+
			"v0.33.1.9 placeholder — using the reserved canonical name %q instead; "+
			"update SKYGATE_TS_HOSTNAME in .env/docker-compose.yml to silence this",
			reason, source, name)
	}
	out, err := s.startTailscaled()
	if err != nil {
		tsAutostartLogOnce(reason + ": start FAILED: " + err.Error() + " (out=" + truncate(out, 200) + ")")
		return false, err.Error()
	}
	// Enforce the name on the daemon. `tailscale up --hostname` sets the name of a
	// NEW registration only; an existing node is renamed with `tailscale set`.
	if live := tailscaleSelfHostname(); live != "" && !strings.EqualFold(live, name) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if rOut, rErr := renameSelfClient(ctx, name); rErr != nil {
			log.Printf("tailscale-autostart: %s: could not rename the client from %q to %q: %v (%s)",
				reason, live, name, rErr, truncate(rOut, 200))
		} else {
			log.Printf("tailscale-autostart: %s: renamed the running client from %q to %q "+
				"(the hostname the configuration asked for)", reason, live, name)
		}
	}
	// B321.1: the same tick also clears an infra tag that no longer describes this node.
	// The operator's live state after a successful reclaim is exactly that — the ghost is
	// gone, the node is `skygate-host`, and `tag:dev-infra-skygate-host-1` is still on it —
	// and the reclaim button refuses to run when there is nothing left to delete, so the
	// leftover could otherwise only be removed by hand. Idempotent and self-limited to the
	// online self node.
	if stale := s.cleanupStaleSelfTags(name, name); len(stale) > 0 {
		log.Printf("tailscale-autostart: %s: removed the stale infra tag(s) %v from the self node",
			reason, stale)
	}
	tsAutostartLogOnce(reason + ": up (hostname=" + name + ", source=" + source + ")")
	return true, ""
}

// RunTailscaleAutostart is the boot pass plus the periodic re-assert. Called from
// main.go once the admin Service is fully wired; it returns immediately.
func (s *Service) RunTailscaleAutostart(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(tailscaleAutostartBootDelay):
		}
		s.EnsureTailscaleUp("boot")
		t := time.NewTicker(TailscaleAutostartInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.EnsureTailscaleUp("tick")
			}
		}
	}()
}

// cleanupStaleSelfTags removes infra tags on the LIVE self node that do not describe it:
// a tag built from the PREVIOUS hostname (immediately after a reclaim) or — B321.1 — any
// `tag:…-infra-<something-else>` left on a node that is already wearing the canonical name.
// headscale tags are additive, so a rename leaves the old `tag:dev-infra-<old-name>`
// behind (live: node 87 carried both the canonical and the `-1` tag after a successful
// reclaim) and a stale tag is exactly the class of ghost identity B188/B265 had to clean up
// by hand. The second rule matters because the operator's state after a successful reclaim
// is precisely "ghost gone, old tag still there": the previous name is no longer derivable
// from the live name, so the tag set itself has to say what is stale.
//
// Read-only on every other node: only an ONLINE node wearing the canonical (or the previous)
// name is touched, and only infra-family tags that do not match the canonical name.
func (s *Service) cleanupStaleSelfTags(previousName, canonical string) []string {
	prev := strings.TrimSpace(previousName)
	canon := strings.TrimSpace(canonical)
	if canon == "" {
		return nil
	}
	// This helper runs automatically right after a reclaim and on the autostart tick, so it
	// must be safe when the service has no headscale client wired at all (unit tests, a very
	// early boot).
	if s.HSGlobalFn == nil {
		return nil
	}
	hs := s.HSGlobalFn()
	if hs == nil {
		return nil
	}
	nodes, err := hs.ListAllNodes()
	if err != nil {
		log.Printf("tailscale-reclaim: cannot list nodes to clean the stale tag of %q: %v", prev, err)
		return nil
	}
	var removed []string
	for _, n := range nodes {
		name := firstNonEmptyAdmin(n.GivenName, n.Hostname)
		if !strings.EqualFold(name, canon) && (prev == "" || !strings.EqualFold(name, prev)) {
			continue
		}
		if !n.Online {
			continue
		}
		id := parseNodeID(n.ID)
		for _, t := range staleInfraTags(n.Tags, canon) {
			if err := hs.UntagNode(id, t); err != nil {
				log.Printf("tailscale-reclaim: untag %q on node %s: %v", t, n.ID, err)
				continue
			}
			removed = append(removed, t)
			log.Printf("tailscale-reclaim: removed the stale tag %q (it does not describe %q) from node %s",
				t, canon, n.ID)
		}
	}
	return removed
}

// staleInfraTags is the pure selection behind cleanupStaleSelfTags: the infra-family tags a
// node wearing `canonical` must not keep. The tag encodes the hostname, so any
// `tag:dev-infra-<name>` / `tag:infra-<name>` whose name is not the canonical one is a
// leftover — whether it came from the rename that just happened or from an earlier epoch.
// Tags outside the infra family (tag:exit-node, tag:private, tag:subnet-router, a user's
// dev-tag) are never touched.
//
// The hostname is derived with the ONE shared implementation, exit_rules.TagToHostname:
// B279.1's contract E1 fails the gate on a second `TrimPrefix(…, "tag:…")` derivation, and
// it caught the first version of this helper.
func staleInfraTags(tags []string, canonical string) []string {
	canon := strings.ToLower(strings.TrimSpace(canonical))
	var out []string
	for _, raw := range tags {
		t := strings.TrimSpace(raw)
		lt := strings.ToLower(t)
		if !strings.HasPrefix(lt, "tag:dev-infra-") && !strings.HasPrefix(lt, "tag:infra-") {
			continue
		}
		if canon != "" && strings.ToLower(exit_rules.TagToHostname(t)) == canon {
			continue
		}
		out = append(out, t)
	}
	return out
}

// handleTailscaleSaveHostname persists the desired tailnet name. An empty value hands
// the decision back to the environment/default; the legacy placeholder shape is
// rewritten to the canonical name and reported, never stored as asked.
func (s *Service) handleTailscaleSaveHostname(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	raw := strings.TrimSpace(r.FormValue("hostname"))
	name, rewritten := NormalizeTailscaleHostname(raw)
	if raw != "" && name == "" {
		tsRedirect(w, r, "", "Пустое имя — введите hostname")
		return
	}
	if err := db.SetGlobalSetting(s.dbc(), tailscaleHostnameDBKey, name); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_save_hostname", "err=db_set "+err.Error())
		tsRedirect(w, r, "", "Не удалось сохранить имя в БД: "+err.Error())
		return
	}
	msg := "Имя сохранено: " + name + ". Применится при следующей проверке (до 5 минут) и после любого обновления."
	if rewritten {
		msg = "«" + raw + "» — это legacy-плейсхолдер v0.33.1.9; сохранено каноническое имя " + name +
			". Оно применится при следующей проверке (до 5 минут)."
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_save_hostname",
		"requested="+raw+" stored="+name+" rewritten="+boolStr(rewritten))
	s.invalidateTailscaleState()
	s.EnsureTailscaleUp("save_hostname")
	tsRedirect(w, r, msg, "")
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// tailscaleAvailableFn indirection so the B321 decision table can be unit-tested
// without the tailscale binaries on PATH. Production always calls tailscaleAvailable.
var tailscaleAvailableFn = tailscaleAvailable
