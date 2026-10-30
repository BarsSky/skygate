// system_tests_derp_rows_b447.go — B447 (2026-10-30). «два релея почему то одинаковых висят но
// один рабочий второй с ошибкой по таблице ниже не понять какой из них с ошибкой».
//
// Two defects answered here.
//
//  1. `derp.reachable` judged `infos[0]` — the FIRST own relay. On the reference install
//     `derp_relays` holds TWO rows for one machine (`:443` relay + `:8443` LAN entry point), so the
//     test looked at one of them and said nothing about the other. It now probes EVERY enabled row
//     with the row's own host:port and prints one line per URL.
//
//  2. `derp.relay_rows` counted "same host on another port" as a broken relay. That pair is the
//     documented LAN+public layout (B260.2 / B401 wrote it down themselves), and the test's own
//     message said «neither affects a working relay» while returning FAIL — so the operator was
//     left with a red test for a correct configuration. Only rows that are NOT relays (a derpmap
//     document, an unrelated path) or a TRUE duplicate (same host AND same port) fail now, and the
//     verdict names each row by its host:port.
package admin

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"skygate/internal/derpcfg"
	"skygate/internal/derphealth"
)

// b447LoopbackHints resolves each target's DIAL host inside THIS container and returns, per
// address, the trap-2 sentence when every resolved address is a loopback address. B396's lesson,
// per row: the live failure is about the container's own /etc/hosts, so the answer is the
// `derp.probe_host` knob, and no firewall rule will change it.
func b447LoopbackHints(ctx context.Context, targets []derphealth.RelayProbe) map[string]string {
	hints := map[string]string{}
	for _, t := range targets {
		if t.Skipped || t.Addr == "" {
			continue
		}
		host, _, err := net.SplitHostPort(t.Addr)
		if err != nil || host == "" {
			continue
		}
		addrs, lerr := net.DefaultResolver.LookupHost(ctx, host)
		if lerr != nil || len(addrs) == 0 || !b396AllLoopback(addrs) {
			continue
		}
		hints[t.Addr] = fmt.Sprintf("%s resolves to %s INSIDE this container, so the probe dials its own loopback (AGENTS trap #2): set the probe address on /admin/derp/relays (derp.probe_host, B296) to the relay's real address, or add an extra_hosts entry and run docker compose up -d --force-recreate skygate",
			host, strings.Join(addrs, ", "))
	}
	return hints
}

func init() {
	TestRegistry = append(TestRegistry, b447Tests...)
}

var b447Tests = []SystemTestDef{
	{
		Name:        "derp.reachable",
		Category:    "derp",
		Description: "every configured own DERP relay answers a TLS connection on ITS OWN host:port",
		Run:         b447Reachable,
	},
	{
		Name:        "derp.relay_rows",
		Category:    "derp",
		Description: "every stored relay row is a real DERP endpoint, named by host:port (the LAN port of the same host is a second entry point, not a broken relay)",
		Run:         b447RelayRows,
	},
}

// b447OwnRelayURLs lists the ENABLED own relay rows' URLs, in a stable order.
func b447OwnRelayURLs(ctx context.Context) ([]string, error) {
	s := getTestService()
	if s == nil || s.dbc() == nil {
		return nil, fmt.Errorf("service or DB not initialised")
	}
	rows, err := s.dbc().QueryContext(ctx, `SELECT COALESCE(url,'') FROM derp_relays
	                                         WHERE enabled = 1
	                                         ORDER BY is_bundled DESC, sort_order ASC, region_id ASC, url ASC`)
	if err != nil {
		return nil, fmt.Errorf("read derp_relays: %w", err)
	}
	defer rows.Close()
	var urls []string
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil && strings.TrimSpace(u) != "" {
			urls = append(urls, u)
		}
	}
	return urls, rows.Err()
}

// b447Reachable is the replacement for the old `derp.reachable`: one verdict PER ROW, each on its
// own port, each with its own timeout.
func b447Reachable(ctx context.Context) (SystemTestStatus, string) {
	s := getTestService()
	if s == nil || s.dbc() == nil {
		return SystemTestSkip, "DB not initialised"
	}
	urls, err := b447OwnRelayURLs(ctx)
	if err != nil {
		return SystemTestSkip, err.Error()
	}
	if len(urls) == 0 {
		return SystemTestSkip, "no own DERP relay is configured"
	}
	probeHost := derpcfg.DialHost(s.dbc())
	targets := derphealth.ProbeTargets(urls, probeHost)

	// B396's loopback lesson, per row: name the trap-2 case BEFORE dialling, because the failure
	// is about the container's /etc/hosts and no firewall rule will change it.
	lines := make([]string, 0, len(targets))
	failed, dialled, skipped := 0, 0, 0
	loopbackHosts := b447LoopbackHints(ctx, targets)
	for _, t := range targets {
		if t.Skipped {
			skipped++
			lines = append(lines, t.String())
			continue
		}
		dialled++
		if hint, ok := loopbackHosts[t.Addr]; ok {
			failed++
			lines = append(lines, t.URL+" — FAIL: "+hint)
			continue
		}
		p := derphealth.ProbeRelay(ctx, t, 3*time.Second)
		if p.OK {
			lines = append(lines, p.String())
			continue
		}
		failed++
		lines = append(lines, p.String())
	}

	// The probe host is part of the answer: a row can fail ONLY because the container dials a
	// loopback address, and then the fix is the knob, not the relay.
	cfg := derpcfg.Resolve(s.dbc())
	head := fmt.Sprintf("%d relay row(s): %d probed, %d skipped", len(targets), dialled, skipped)
	if cfg.Host != "" {
		head += fmt.Sprintf("; probes dial %s (source: %s) and speak the row's hostname as TLS SNI", cfg.Host, cfg.Source)
	}
	body := head + "\n" + strings.Join(lines, "\n")
	if failed > 0 {
		return SystemTestFail, body + "\n" + b447LoopbackHint(targets)
	}
	return SystemTestPass, body
}

// b447LoopbackHint adds the trap-2 explanation when a failing row's hostname resolves to a
// loopback address inside this container — the live case that produced the operator's report.
func b447LoopbackHint(targets []derphealth.RelayProbe) string {
	for _, t := range targets {
		if t.Skipped || t.OK {
			continue
		}
		if strings.Contains(t.Err, "nothing accepts a TCP connection") && strings.Contains(t.Addr, ":") {
			host := strings.Split(t.Addr, ":")[0]
			return fmt.Sprintf("If %s resolves to 127.0.0.1 INSIDE this container, the panel is dialling its own loopback (AGENTS trap #2): set the probe address on /admin/derp/relays (derp.probe_host, B296) to the relay's real address, or add an extra_hosts entry and run `docker compose up -d --force-recreate skygate`.", host)
		}
	}
	return "Each row is probed on its OWN port: a row that fails while its neighbour passes is a per-address problem (firewall, listener, or a port nobody serves), not a dead relay."
}

// b447RelayRows re-judges the stored rows: a non-relay row fails, a same-host-different-port pair
// is INFO (two entry points), a true duplicate (same host AND port) fails.
func b447RelayRows(ctx context.Context) (SystemTestStatus, string) {
	s := getTestService()
	if s == nil || s.dbc() == nil {
		return SystemTestSkip, "service or DB not initialised"
	}
	rows, err := s.dbc().QueryContext(ctx, `SELECT hostname, COALESCE(url,''), COALESCE(enabled,0)
	                                         FROM derp_relays ORDER BY region_id, sort_order, id`)
	if err != nil {
		return SystemTestSkip, "read derp_relays: " + err.Error()
	}
	defer rows.Close()
	type row struct {
		host, url string
		enabled   bool
	}
	var all []row
	for rows.Next() {
		var r row
		var en int
		if rows.Scan(&r.host, &r.url, &en) != nil {
			continue
		}
		r.enabled = en == 1
		all = append(all, r)
	}
	if len(all) == 0 {
		return SystemTestSkip, "no relay rows stored"
	}

	var notRelays, dupes, altPorts []string
	seen := map[string]bool{} // host:port
	for _, r := range all {
		kind, note := derphealth.ClassifyRelayURL(r.url)
		endpoint := fmt.Sprintf("%s:%d", derphealth.RelayHost(r.url), derphealth.RelayPort(r.url))
		if kind != derphealth.RelayKindNode {
			notRelays = append(notRelays, fmt.Sprintf("%s (%s)", endpoint, note))
			continue
		}
		if seen[endpoint] {
			dupes = append(dupes, endpoint)
			continue
		}
		seen[endpoint] = true
	}
	// Same host, another port: NOT a duplicate relay — two entry points for one machine. Reported,
	// never failed: the per-row probe above is what says whether each one answers.
	hostPorts := map[string][]string{}
	for endpoint := range seen {
		host := endpoint[:strings.LastIndex(endpoint, ":")]
		hostPorts[host] = append(hostPorts[host], endpoint)
	}
	for host, eps := range hostPorts {
		if len(eps) > 1 {
			altPorts = append(altPorts, fmt.Sprintf("%s → %s", host, strings.Join(eps, " + ")))
		}
	}

	summary := fmt.Sprintf("%d row(s), each identified by host:port", len(all))
	if len(altPorts) > 0 {
		summary += "; the same host on several ports is two entry points, and derp.reachable says whether each one answers: " + strings.Join(altPorts, ", ")
	}
	if len(notRelays) > 0 {
		return SystemTestFail, fmt.Sprintf("%d row(s) are NOT DERP endpoints: %s. Delete or disable them on /admin/derp/relays — a derpmap document is merged by headscale itself and an unrelated path is never served. %s.",
			len(notRelays), strings.Join(notRelays, "; "), summary)
	}
	if len(dupes) > 0 {
		return SystemTestFail, fmt.Sprintf("%d duplicate row(s) name the SAME host and port: %s. Keep one — two rows with one address are one relay listed twice, and nothing on the page can tell them apart. %s.",
			len(dupes), strings.Join(dupes, ", "), summary)
	}
	return SystemTestPass, summary
}
