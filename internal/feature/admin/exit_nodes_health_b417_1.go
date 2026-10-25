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
	now := time.Now().Format("2006-01-02 15:04")
	out := make([]exitnode.HealthLine, 0, len(rows))
	for _, row := range rows {
		line := exitnode.HealthLine{Host: row.Hostname, At: now}
		req, ok := s.healthRequestFor(row.SSHTarget, row.SSHKeyPath, row.SSHPort)
		if !ok {
			line.Problem = "ssh_target/порт релея не разобрать — проверьте поля строки"
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
// cannot parse rather than probing the wrong host.
func (s *Service) healthRequestFor(target, keyPath, port string) (exitnode.Request, bool) {
	t := strings.TrimSpace(target)
	user, host := "", t
	if i := strings.Index(t, "@"); i >= 0 {
		user, host = t[:i], t[i+1:]
	}
	if user == "" {
		user = "root"
	}
	p := 22
	if port != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(port)); err == nil && n > 0 {
			p = n
		}
	}
	key := strings.TrimSpace(keyPath)
	if key == "" {
		key = s.effectiveExitSSHKeyPath("")
	}
	if host == "" || key == "" {
		return exitnode.Request{}, false
	}
	return exitnode.Request{Host: host, User: user, Port: p, NodeName: host, KeyPath: key}, true
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
