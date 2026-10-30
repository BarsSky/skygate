package admin

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/exitnode"
	"skygate/internal/localtime"
)

// B417.1 (2026-10-24) — the panel must not call a relay healthy because headscale
// approved a route; see internal/exitnode/health_b417_1.go for the reasoning.
//
// The read-only probe runs over the SAME ssh path the route sync uses, for every row in
// exit_servers, at most once per exitNodeHealthInterval (and on demand). Results are
// persisted so the page can show the last known state after a restart, and a transition
// ok→broken is announced once through the notifier.

// exitNodeHealthKey is the global_settings key holding the last health snapshot.
const exitNodeHealthKey = "exitnode.health"

// exitNodeHealthInterval is how often the background pass may probe the relays. Five
// minutes between ticks × this = the real cadence; the probe costs one ssh round trip
// per relay, so it is deliberately much slower than the 2-minute approval pass.
const exitNodeHealthInterval = 15 * time.Minute

var healthRunMu sync.Mutex
var healthLastRun time.Time

// exitNodeHealthState is the persisted snapshot: one line per relay.
type exitNodeHealthState struct {
	At    time.Time             `json:"at"`
	Lines []exitnode.HealthLine `json:"lines"`
}

// loadExitNodeHealth returns the last snapshot (empty when none was taken).
func (s *Service) loadExitNodeHealth() exitNodeHealthState {
	var st exitNodeHealthState
	d := s.dbc()
	if d == nil {
		return st
	}
	raw, err := db.GetGlobalSetting(d, exitNodeHealthKey, "")
	if err != nil || strings.TrimSpace(raw) == "" {
		return st
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return exitNodeHealthState{}
	}
	return st
}

// RunExitNodeHealthCheck probes every registered relay and returns the rows. It is
// read-only on the target: ip_forward, iptables -S, tailscale status.
func (s *Service) RunExitNodeHealthCheck(ctx context.Context) []exitnode.HealthLine {
	rows, err := db.ListExitServers(s.dbc())
	if err != nil {
		return nil
	}
	now := localtime.Format(localtime.Now())
	out := make([]exitnode.HealthLine, 0, len(rows))
	for _, row := range rows {
		line := exitnode.HealthLine{Host: row.Hostname, At: now}
		// B454: the probe resolves the target the SAME way the route apply does. A row whose
		// ssh_target is empty (live: `aisha`) used to answer «поле ssh_target пустое» even though
		// skygate itself already knew where that relay is — `exit_servers.tailscale_ip` — and the
		// apply path has used that fallback since B292. Two resolvers for one question is how the
		// panel and the sync come to disagree.
		target := row.SSHTarget
		if strings.TrimSpace(target) == "" {
			if resolved, rerr := db.LookupExitServerSSHTarget(s.dbc(), row.Hostname); rerr == nil && strings.TrimSpace(resolved) != "" {
				target = resolved
			}
		}
		req, reason := s.healthRequestFor(target, row.SSHKeyPath, row.SSHPort)
		if req.Host == "" {
			// B449: the reason names the FIELD and the shape, because «не разобрать» alone
			// told the operator nothing about whether the row was empty or mistyped — and
			// neither says the relay cannot carry traffic.
			line.Problem = reason + " — это настройка строки релея (SSH для проверки), доступ через релей от неё не зависит"
			out = append(out, line)
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		res, rerr := exitnode.Run(hctx, req, exitnode.BuildHealthScript())
		cancel()
		var h exitnode.Health
		if rerr != nil {
			h.Err = rerr.Error()
		} else {
			h = exitnode.ParseHealth(res.RawOutput)
		}
		line.Problem = exitnode.HealthVerdict(h)
		line.OK = line.Problem == ""
		out = append(out, line)
	}
	return exitnode.HealthSortOrders(out)
}

// healthRequestFor builds the ssh request for one exit_servers row. It refuses a row it
// cannot parse rather than probing the wrong host, and it says WHICH field is wrong.
//
// B449: the parse lives in exitnode.ParseSSHTarget so the probe and the route apply read
// the same string the same way. The port can arrive twice — inside `ssh_target`
// (`root@100.64.0.2:18022`) and in the `ssh_port` column — and the explicit port in the
// string wins, because it is the more specific statement about THIS row.
func (s *Service) healthRequestFor(target, keyPath, port string) (exitnode.Request, string) {
	spec, reason, ok := exitnode.ParseSSHTarget(target)
	if !ok {
		return exitnode.Request{}, reason
	}
	p := spec.Port
	if p == 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(port)); err == nil && n > 0 {
			p = n
		}
	}
	if p == 0 {
		p = 22
	}
	user := spec.User
	if user == "" {
		user = "root"
	}
	key := strings.TrimSpace(keyPath)
	if key == "" {
		key = s.effectiveExitSSHKeyPath("")
	}
	if key == "" {
		return exitnode.Request{}, "не задан SSH-ключ (ssh_key_path строки и SKYGATE_EXIT_SSH_KEY пусты)"
	}
	return exitnode.Request{Host: spec.Host, User: user, Port: p, NodeName: spec.Host, KeyPath: key}, ""
}

// runHealthCheckIfDue is the ticker entry: it never blocks a tick for long and never
// probes more often than the interval.
func (s *Service) runHealthCheckIfDue(ctx context.Context) {
	healthRunMu.Lock()
	if time.Since(healthLastRun) < exitNodeHealthInterval {
		healthRunMu.Unlock()
		return
	}
	healthLastRun = time.Now()
	healthRunMu.Unlock()

	prev := s.loadExitNodeHealth()
	lines := s.RunExitNodeHealthCheck(ctx)
	if len(lines) == 0 {
		return
	}
	b, _ := json.Marshal(exitNodeHealthState{At: time.Now(), Lines: lines})
	if err := db.SetGlobalSetting(s.dbc(), exitNodeHealthKey, string(b)); err != nil {
		log.Printf("[b417.1] health snapshot not saved: %v", err)
	}
	s.announceHealthTransitions(prev.Lines, lines)
}

// announceHealthTransitions notifies ONCE per relay that goes from ok to broken (and
// once when it recovers), so a flapping relay cannot flood the chat.
func (s *Service) announceHealthTransitions(prev, cur []exitnode.HealthLine) {
	if s.Notifier == nil {
		return
	}
	wasOK := map[string]bool{}
	for _, l := range prev {
		wasOK[l.Host] = l.OK
	}
	for _, l := range cur {
		nowKnown, seen := wasOK[l.Host]
		if seen && nowKnown == l.OK {
			continue
		}
		if !l.OK {
			go s.Notifier.SendAlert("⚠️ exit-node " + l.Host + ": маршрут одобрен, но выход не работает — " + l.Problem +
				"\nskygate ничего не меняет на VPS сам: проверьте правила (iptables/ip_forward) или перезапустите провижининг.")
			continue
		}
		go s.Notifier.SendAlert("✅ exit-node " + l.Host + ": forwarding/NAT снова в порядке.")
	}
}

// PostAdminExitNodeHealthCheck re-runs the probe from the page («Проверить выход»).
func (s *Service) PostAdminExitNodeHealthCheck(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lines := s.RunExitNodeHealthCheck(context.Background())
	b, _ := json.Marshal(exitNodeHealthState{At: time.Now(), Lines: lines})
	_ = db.SetGlobalSetting(s.dbc(), exitNodeHealthKey, string(b))
	broken := 0
	for _, l := range lines {
		if !l.OK {
			broken++
		}
	}
	msg := "проверено релеев: " + strconv.Itoa(len(lines)) + ", проблем: " + strconv.Itoa(broken)
	if broken > 0 {
		s.redirectProvisionFlash(w, r, false, "", "", "", "", msg)
		return
	}
	s.redirectProvisionFlash(w, r, true, "", "", "", "", msg)
}
