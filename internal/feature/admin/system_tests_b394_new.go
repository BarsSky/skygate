package admin

// system_tests_b394_new.go — the eleven diagnostic tests B394 adds.
//
// WHY THESE ELEVEN
// ----------------
// The catalogue could say "the database answers" and "headscale is reachable", but
// not the things that actually break an installation, each of them measured live
// in this project's own history:
//
//   - an exit node advertising 0.0.0.0/0 that nobody APPROVED (B273) — the relay
//     looks online and carries nothing;
//   - node_owner_map claiming a tag headscale does not have (B272) — every
//     per-device rule silently matches nothing;
//   - a live ACL that differs from the generated one (B276) — the pins point at a
//     relay that no longer serves the prefix;
//   - prefix ownership that disagrees with what the relays advertise (B274/B275) —
//     the same prefix from two relays, or none;
//   - a read-only database (a replica, `default_transaction_read_only`) that only
//     fails when somebody writes;
//   - a half-migrated schema;
//   - a full disk (the operator's own host hit 90 % on 2026-10-15);
//   - a DERP relay none of the clients can reach (B265/B289);
//   - a process that is still booting while the page says it is up (B269);
//   - a domain rule whose stored /32s no longer match DNS;
//   - a backup that stopped running.
//
// Every test is READ-ONLY and bounded by the runner's per-test deadline (5 s).
// The write probe runs inside a transaction that is rolled back, so it proves the
// database accepts writes without leaving anything behind.

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"os"
	"path/filepath"

	"skygate/internal/acl"
	"skygate/internal/db"
	"skygate/internal/derphealth"
	"skygate/internal/feature/exit_rules"
	"skygate/internal/headscale"
	"skygate/internal/prefixowner"
	"skygate/internal/startup"
)

func init() {
	TestRegistry = append(TestRegistry, b394Tests...)
}

// b394Tests is the B394 batch. Kept in its own slice so the block's tests can be
// identified (and counted) without depending on their position in the registry.
var b394Tests = []SystemTestDef{
	{
		Name:        "headscale.exit_routes_approved",
		Category:    "headscale",
		Description: "every exit node that advertises 0.0.0.0/0 or ::/0 has that route APPROVED",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			hs, err := b394Headscale()
			if err != nil {
				return SystemTestSkip, err.Error()
			}
			nodes, err := hs.ListAllNodes()
			if err != nil {
				return SystemTestFail, "list nodes: " + err.Error()
			}
			exitNodes, bad := 0, []string{}
			for _, n := range nodes {
				if !n.IsExitNode {
					continue
				}
				exitNodes++
				for _, want := range []string{"0.0.0.0/0", "::/0"} {
					if !containsStr(n.AvailableRoutes, want) {
						continue
					}
					if !containsStr(n.ApprovedRoutes, want) {
						bad = append(bad, fmt.Sprintf("%s advertises %s but it is NOT approved", n.GivenName, want))
					}
				}
			}
			if exitNodes == 0 {
				return SystemTestSkip, "no exit node in this tailnet — nothing to approve"
			}
			if len(bad) > 0 {
				return SystemTestFail, fmt.Sprintf("%d of %d exit node(s) are unusable for full-tunnel egress: %s. Approve the routes on /admin/exit-nodes (or `skygate` re-syncs them).",
					len(bad), exitNodes, strings.Join(bad[:min(3, len(bad))], "; "))
			}
			return SystemTestPass, fmt.Sprintf("%d exit node(s), every advertised default route is approved", exitNodes)
		},
	},
	{
		Name:        "headscale.tags_drift",
		Category:    "headscale",
		Description: "every tag node_owner_map records is actually present on the node in headscale",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "service or DB not initialised"
			}
			hs, err := b394Headscale()
			if err != nil {
				return SystemTestSkip, err.Error()
			}
			nodes, err := hs.ListAllNodes()
			if err != nil {
				return SystemTestFail, "list nodes: " + err.Error()
			}
			owners, err := db.ListAllNodeOwners(s.dbc())
			if err != nil {
				return SystemTestFail, "read node_owner_map: " + err.Error()
			}
			tagsByNode := map[string][]string{}
			for _, n := range nodes {
				tagsByNode[n.ID] = n.Tags
			}
			drift, unattributed := []string{}, 0
			for _, o := range owners {
				if strings.TrimSpace(o.Tag) == "" {
					unattributed++
					continue
				}
				if !containsStr(tagsByNode[o.NodeID], o.Tag) {
					drift = append(drift, fmt.Sprintf("%s expects %s", o.Hostname, o.Tag))
				}
			}
			if len(drift) > 0 {
				return SystemTestFail, fmt.Sprintf("%d device(s) are recorded with a tag headscale does not have, so their per-device rules match nothing: %s. The reconciler re-applies missing tags every tick — if this persists, the tag is missing from tagOwners (see /admin/headscale/acl).",
					len(drift), strings.Join(drift[:min(5, len(drift))], ", "))
			}
			return SystemTestPass, fmt.Sprintf("%d owned node(s) carry the tag the portal recorded (%d unattributed)", len(owners)-unattributed, unattributed)
		},
	},
	{
		Name:        "headscale.policy_current",
		Category:    "headscale",
		Description: "the live headscale policy equals the policy skygate generates from the current data",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "service or DB not initialised"
			}
			hs, err := b394Headscale()
			if err != nil {
				return SystemTestSkip, err.Error()
			}
			live, err := hs.GetACL()
			if err != nil {
				return SystemTestFail, "read the live policy: " + err.Error()
			}
			generated, err := acl.GenerateACLForPlaneWithMode(s.dbc(), "", acl.ACLViaEnabled())
			if err != nil {
				return SystemTestFail, "generate the policy: " + err.Error()
			}
			same, err := headscale.PolicyEquivalent(live, generated)
			if err != nil {
				return SystemTestFail, "compare the policies: " + err.Error()
			}
			if !same {
				return SystemTestFail, fmt.Sprintf("the live policy (%d bytes) differs from the generated one (%d bytes): rules, tags or prefix ownership changed without an apply. Press Re-sync on /admin/headscale/acl.", len(live), len(generated))
			}
			return SystemTestPass, fmt.Sprintf("the live policy equals the generated one (%d bytes)", len(live))
		},
	},
	{
		Name:        "prefix_owner.consistency",
		Category:    "prefix_owner",
		Description: "every prefix is served by exactly ONE relay, and the owner actually advertises it",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "service or DB not initialised"
			}
			hs, err := b394Headscale()
			if err != nil {
				return SystemTestSkip, err.Error()
			}
			nodes, err := hs.ListAllNodes()
			if err != nil {
				return SystemTestFail, "list nodes: " + err.Error()
			}
			ownerByPrefix := prefixowner.TagByPrefix(s.dbc()) // prefix -> relay tag
			if len(ownerByPrefix) == 0 {
				return SystemTestSkip, "no prefix ownership recorded yet"
			}
			hostByTag := map[string]string{}
			for host, tag := range prefixowner.TagsByHost(s.dbc()) {
				hostByTag[strings.ToLower(tag)] = host
			}
			routesByHost, tagByHost := map[string][]string{}, map[string]string{}
			approvedByHost := map[string][]string{}
			for _, n := range nodes {
				host := strings.ToLower(n.GivenName)
				routesByHost[host] = n.AvailableRoutes
				approvedByHost[host] = n.ApprovedRoutes
				for _, t := range n.Tags {
					if strings.HasPrefix(t, "tag:dev-infra-") {
						tagByHost[host] = t
					}
				}
			}
			problems := []string{}
			// B396: "advertised" and "approved" are different facts (B273's lesson, in
			// this test's own vocabulary). A relay that advertises a prefix the operator
			// never approved serves nothing, and a message that only says "does not
			// advertise" sends them to the wrong button.
			unapproved := []string{}
			for prefix, tag := range ownerByPrefix {
				host := strings.ToLower(hostByTag[strings.ToLower(tag)])
				if host == "" {
					problems = append(problems, fmt.Sprintf("%s is owned by %s, which is not a node in this tailnet", prefix, tag))
					continue
				}
				if !containsStr(routesByHost[host], prefix) {
					problems = append(problems, fmt.Sprintf("%s is owned by %s, but %s does not advertise it", prefix, host, host))
					continue
				}
				// Only meaningful when headscale reports the approved set at all.
				if len(approvedByHost[host]) > 0 && !containsStr(approvedByHost[host], prefix) {
					unapproved = append(unapproved, fmt.Sprintf("%s is advertised by %s but not APPROVED there", prefix, host))
				}
			}
			// A prefix advertised by two relays makes headscale's primary flip.
			advertisers := map[string]int{}
			for host, routes := range routesByHost {
				if tagByHost[host] == "" {
					continue
				}
				for _, r := range routes {
					if r == "0.0.0.0/0" || r == "::/0" {
						continue
					}
					advertisers[r]++
				}
			}
			dupes := 0
			for _, n := range advertisers {
				if n > 1 {
					dupes++
				}
			}
			if len(problems) > 0 || dupes > 0 || len(unapproved) > 0 {
				parts := []string{}
				if len(problems) > 0 {
					parts = append(parts, fmt.Sprintf("%d prefix(es) are NOT SERVED as recorded: %s",
						len(problems), strings.Join(problems[:min(3, len(problems))], "; ")))
				}
				if len(unapproved) > 0 {
					parts = append(parts, fmt.Sprintf("%d prefix(es) are advertised but NOT APPROVED (clients ignore unapproved routes — approve them on /admin/exit-nodes): %s",
						len(unapproved), strings.Join(unapproved[:min(3, len(unapproved))], "; ")))
				}
				if dupes > 0 {
					parts = append(parts, fmt.Sprintf("%d prefix(es) are advertised by MORE THAN ONE relay (headscale's primary will flip between them)", dupes))
				}
				return SystemTestFail, strings.Join(parts, " | ")
			}
			return SystemTestPass, fmt.Sprintf("%d prefixes, each served by exactly one advertising relay", len(ownerByPrefix))
		},
	},
	{
		Name:        "db.writable_primary",
		Category:    "db",
		Description: "the database accepts writes (a rolled-back transaction), not just reads",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "DB not initialised"
			}
			conn := s.dbc()
			tx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				return SystemTestFail, "BEGIN failed: " + err.Error()
			}
			defer func() { _ = tx.Rollback() }() // never leave the probe behind
			if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE b394_write_probe (x INTEGER)`); err != nil {
				return SystemTestFail, "CREATE TEMP TABLE failed — the connection cannot write: " + err.Error()
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO b394_write_probe (x) VALUES (1)`); err != nil {
				return SystemTestFail, "INSERT failed — this database is read-only (a replica, or default_transaction_read_only=on): " + err.Error()
			}
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM b394_write_probe`).Scan(&n); err != nil || n != 1 {
				return SystemTestFail, fmt.Sprintf("the write was not visible inside the transaction (n=%d, err=%v)", n, err)
			}
			if err := tx.Rollback(); err != nil {
				return SystemTestFail, "ROLLBACK failed: " + err.Error() + " (the probe table is still open on this connection)"
			}
			return SystemTestPass, "the database accepted a write inside a transaction that was rolled back"
		},
	},
	{
		Name:        "db.schema_tables",
		Category:    "db",
		Description: "every table the panel needs exists in the active database (the schema is fully migrated)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "DB not initialised"
			}
			query := `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1`
			if db.ActiveDialect() == db.DialectSQLite {
				query = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
			}
			missing := []string{}
			for _, table := range b394RequiredTables {
				var n int
				if err := s.dbc().QueryRowContext(ctx, query, table).Scan(&n); err != nil {
					return SystemTestFail, fmt.Sprintf("cannot check table %q: %v", table, err)
				}
				if n == 0 {
					missing = append(missing, table)
				}
			}
			if len(missing) > 0 {
				return SystemTestFail, fmt.Sprintf("%d table(s) are missing: %s. The binary is newer than the schema — run the migrations (they run at boot; `/admin/database` shows the chain).",
					len(missing), strings.Join(missing, ", "))
			}
			return SystemTestPass, fmt.Sprintf("all %d required tables exist", len(b394RequiredTables))
		},
	},
	{
		Name:        "disk.free_space",
		Category:    "disk",
		Description: "the filesystem holding the data directory is not filling up (fail under 5 %, warn under 15 %)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			// The data directory is where the DB, the update state and the caches
			// live — the filesystem that decides whether the next backup or release can
			// be written. Derived from SKYGATE_DB when it names a file, else /data, else.
			path := b394DataDir()
			free, total, err := b394DiskUsage(path)
			if err != nil {
				return SystemTestSkip, fmt.Sprintf("cannot stat %s on this platform: %v", path, err)
			}
			if total == 0 {
				return SystemTestSkip, path + " reports no total size"
			}
			pct := float64(free) / float64(total) * 100
			msg := fmt.Sprintf("%s: %.1f %% free (%.1f GB of %.1f GB)", path, pct,
				float64(free)/1e9, float64(total)/1e9)
			if pct < 5 {
				return SystemTestFail, msg + " — below 5 %, a backup or an update will fail. Prune the Docker build cache (docker system prune), old backups and /var/log."
			}
			if pct < 15 {
				return SystemTestPass, msg + " — warning band (under 15 %): plan a cleanup before the next release."
			}
			return SystemTestPass, msg
		},
	},
	{
		Name:        "derp.reachable",
		Category:    "derp",
		Description: "the operator's own DERP relay answers a TLS connection from this container",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "DB not initialised"
			}
			infos, err := derphealth.FetchOwnDERPs(ctx, s.dbc())
			if err != nil {
				return SystemTestFail, "read the configured DERPs: " + err.Error()
			}
			if len(infos) == 0 {
				return SystemTestSkip, "no own DERP relay is configured"
			}
			info := infos[0]
			host, port, perr := b394SplitURL(info.URL)
			if perr != nil {
				return SystemTestFail, fmt.Sprintf("the configured DERP URL %q cannot be parsed: %v", info.URL, perr)
			}
			dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			// B396: name the loopback case BEFORE dialling. Measured live: the
			// operator's relay name resolved to 127.0.0.1 INSIDE the container, so
			// the panel dialled its own loopback and the message said "check the
			// firewall/DNS" — true, but not actionable. A host that resolves its own
			// public name to 127.0.0.1 leaks into containers through /etc/hosts or a
			// compose extra_hosts entry (AGENTS trap #2); the container can never
			// reach the relay that way and no firewall rule will change it.
			rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
			addrs, rerr := net.DefaultResolver.LookupHost(rctx, host)
			rcancel()
			if rerr == nil && len(addrs) > 0 && b396AllLoopback(addrs) {
				// B401: name BOTH fixes, in the order that costs least. The operator's
				// live report was exactly this state, and the previous sentence stopped
				// at "fix extra_hosts", which needs a compose edit and a container
				// recreate — while the panel already has a knob that does not.
				return SystemTestFail, fmt.Sprintf("%q resolves to %s INSIDE this container: the panel is dialling its own loopback and can never reach the relay. The host resolves its own public name to 127.0.0.1 and that leaks into the container (AGENTS trap #2). TWO fixes, both without touching skygate: (1) the cheapest — set the probe address on /admin/derp/relays (derp.probe_host, B296) to the relay's real public or LAN address; the probes keep speaking the hostname as TLS SNI, so the certificate still matches; (2) the root cause — add an extra_hosts entry for %s with the host's real IP in docker-compose.yml and run `docker compose up -d --force-recreate skygate` (a plain `restart` keeps the frozen /etc/hosts, trap #3).",
					host, strings.Join(addrs, ", "), host)
			}
			start := time.Now()
			dialer := &net.Dialer{}
			conn, derr := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, port), &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
			if derr != nil {
				// fall back to a plain TCP connect so the message distinguishes
				// "nothing listens" from "TLS is broken"
				if tc, terr := dialer.DialContext(dctx, "tcp", net.JoinHostPort(host, port)); terr == nil {
					_ = tc.Close()
					return SystemTestFail, fmt.Sprintf("%s accepts TCP but the TLS handshake failed (%v) — clients will not use this relay", net.JoinHostPort(host, port), derr)
				}
				return SystemTestFail, fmt.Sprintf("%s is unreachable from this container (%v) — check the firewall/DNS or remove the relay", net.JoinHostPort(host, port), derr)
			}
			_ = conn.Close()
			return SystemTestPass, fmt.Sprintf("%s answered a TLS handshake in %s", net.JoinHostPort(host, port), time.Since(start).Round(time.Millisecond))
		},
	},
	{
		Name:        "derp.relay_rows",
		Category:    "derp",
		Description: "every stored relay row is a real DERP node — no derpmap catalogue rows and no duplicate host entries presented as separate relays",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "service or DB not initialised"
			}
			// B401 (2026-10-20) — WHAT THE OPERATOR'S LIST ACTUALLY HELD.
			//
			// His `derp_relays` table had three rows and only ONE is a relay:
			//
			//	https://derp.skynas.ru:443                          the relay (healthy, 5 ms)
			//	https://derp.skynas.ru:8443                         the LAN-direct HTTP port
			//	https://controlplane.tailscale.com/derpmap/default  a DERP MAP DOCUMENT
			//
			// The last one is not a relay at all — AutoMigrateDerpRelays copied it from
			// the legacy `derp.external_urls` key (B265) — and nothing on the page said
			// so, which is why he asked «в релеи висят ещё два не рабочих derp — что
			// это и к чему». The page now labels them (B401); this test is the guard
			// that keeps such rows from coming back, and it names the fix (delete or
			// disable the row — neither has any effect on a working relay).
			rows, err := s.dbc().QueryContext(ctx, `SELECT hostname, COALESCE(url,''), COALESCE(enabled,0)
			                                       FROM derp_relays ORDER BY region_id, sort_order`)
			if err != nil {
				return SystemTestSkip, "read derp_relays: " + err.Error()
			}
			defer rows.Close()
			type relayRow struct {
				host, url string
				enabled   bool
			}
			var all []relayRow
			for rows.Next() {
				var r relayRow
				var en int
				if err := rows.Scan(&r.host, &r.url, &en); err != nil {
					continue
				}
				r.enabled = en == 1
				all = append(all, r)
			}
			var notRelays, dupPorts []string
			for i, r := range all {
				kind, why := derphealth.ClassifyRelayURL(r.url)
				if kind != derphealth.RelayKindNode {
					notRelays = append(notRelays, fmt.Sprintf("%s (%s: %s)", r.host, r.url, why))
					continue
				}
				if !r.enabled {
					continue
				}
				others := make([]string, 0, len(all))
				for j, o := range all {
					if j != i && o.enabled {
						others = append(others, o.url)
					}
				}
				if alt := derphealth.ClassifyAltPort(r.url, others); alt.IsAltPort {
					dupPorts = append(dupPorts, fmt.Sprintf("%s (%s — the same host already has a row on port %d)", r.host, r.url, alt.OtherPort))
				}
			}
			switch {
			case len(notRelays) == 0 && len(dupPorts) == 0:
				return SystemTestPass, fmt.Sprintf("all %d relay row(s) are real DERP nodes and no host is listed twice on different ports", len(all))
			case len(notRelays) > 0 && len(dupPorts) > 0:
				return SystemTestFail, fmt.Sprintf("%d row(s) are NOT relay nodes: %s; and %d row(s) duplicate a host on another port: %s. Delete or disable them on /admin/derp/relays — neither affects a working relay (the derpmap catalogue is merged by headscale itself, and the extra port is a LAN-only entry point).",
					len(notRelays), strings.Join(notRelays, "; "), len(dupPorts), strings.Join(dupPorts, "; "))
			case len(notRelays) > 0:
				return SystemTestFail, fmt.Sprintf("%d row(s) are NOT relay nodes: %s. Delete or disable them on /admin/derp/relays — the public derpmap catalogue is merged by headscale itself, so such a row can never become a relay.",
					len(notRelays), strings.Join(notRelays, "; "))
			default:
				return SystemTestFail, fmt.Sprintf("%d row(s) duplicate a host that already has a conventional-port row: %s. This is not a second relay — it is a LAN/alternative entry point; disable it if you do not use it.",
					len(dupPorts), strings.Join(dupPorts, "; "))
			}
		},
	},
	{
		Name:        "build.ready",
		Category:    "build",
		Description: "this process serves the real routes (not the boot-time provisional handler) and reports a build label",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			build := ""
			if s != nil {
				build = s.BuildVersion
			}
			stage := startup.StageName()
			phase := startup.Phase()
			if build == "" {
				return SystemTestFail, "the process reports no build label — /healthz cannot be used to verify a deploy"
			}
			if stage != "http" {
				return SystemTestFail, fmt.Sprintf("the process is still in startup stage %q (phase %q): the panel is answering from the provisional handler, so actions may hit a half-initialised app", stage, phase)
			}
			return SystemTestPass, fmt.Sprintf("build %s, stage %s (last phase %s)", build, stage, phase)
		},
	},
	{
		Name:        "exit_rules.domain_prefix",
		Category:    "exit_rules",
		Description: "a domain rule's stored /32 rows match what the domain resolves to today",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "service or DB not initialised"
			}
			rows, err := s.dbc().QueryContext(ctx, `SELECT device_id, target_value FROM device_rules
				WHERE enabled = 1 AND target_type = 'domain' AND target_value NOT LIKE '%.%/%'
				GROUP BY device_id, target_value ORDER BY device_id LIMIT 3`)
			if err != nil {
				return SystemTestFail, "read domain rules: " + err.Error()
			}
			type domRule struct {
				deviceID int64
				domain   string
			}
			var rules []domRule
			for rows.Next() {
				var r domRule
				if err := rows.Scan(&r.deviceID, &r.domain); err != nil {
					continue
				}
				rules = append(rules, r)
			}
			rows.Close()
			if len(rules) == 0 {
				return SystemTestSkip, "no domain rule to re-resolve"
			}
			// B411 (2026-10-22) — THE VERDICT IS ABOUT THE MECHANISM, NOT ABOUT
			// CATCHING DNS MID-ROTATION.
			//
			// This test used to call any answer the stored rows did not contain
			// "stale". Measured live on a healthy panel, minutes apart: 6 of 13
			// addresses "stale", then after the next pass ran — 5 published ranges
			// untouched, 8 obsolete /32 rows removed, the new answer written as a
			// gap. The mechanism was right both times; the assertion was the
			// problem, because a rotating provider (Akamai 62.115.x, AWS registry
			// edges) answers with new addresses every few minutes while B308's
			// re-resolve interval is six hours. Between two passes the difference is
			// GUARANTEED, so the test could never be green for such a domain.
			//
			// What is decided per rule now (exit_rules.DomainRowsVerdictFor):
			//   covered           — nothing to do;
			//   pending-next-pass — the answers moved AFTER the last resolve, so the
			//                       interval covers them: PASS, but said out loud;
			//   stale             — a pass was due and did not cover the answers, or
			//                       the rule was never resolved at all, or nothing is
			//                       scheduled to run: FAIL. This is the pre-B409 defect
			//                       class, and the only failing one.
			autoOn := db.GetGlobalSettingBool(s.dbc(), globalSettingsKeyDNSAutoUpdate,
				s.Cfg != nil && s.Cfg.DNSAutoUpdateEnabled)
			rawInterval, _ := db.GetGlobalSetting(s.dbc(), exit_rules.SettingDomainResolveIntervalSec, "")
			interval := exit_rules.ParseDomainResolveInterval(rawInterval)
			now := time.Now().Unix()

			staleDomains := []string{}
			pendingDomains := []string{}
			checked := 0
			readErr := ""
			for _, r := range rules {
				lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				ips, lerr := net.DefaultResolver.LookupHost(lctx, r.domain)
				cancel()
				if lerr != nil {
					// A resolver failure is NOT a product verdict, and it is not a
					// rotation either: name it separately and never mark the domain.
					staleDomains = append(staleDomains, fmt.Sprintf("%s does not resolve right now (%v)", r.domain, lerr))
					continue
				}
				// B406: the ACL asks "does any enabled row of this device CONTAIN the
				// address", not "is there a row that is exactly ip/32". The CDN path
				// writes the provider's RANGE (104.16.0.0/12 for Cloudflare), so the
				// exact-/32 question answered "stale" for every healthy CDN-expanded
				// domain — 8 of 12 on the operator's installation, while the resolver
				// was writing rows that same day.
				prefixes, perr := b406DevicePrefixes(ctx, s.dbc(), r.deviceID)
				if perr != nil {
					readErr = perr.Error()
					continue
				}
				uncovered := 0
				examples := []string{}
				for _, ip := range ips {
					parsed := net.ParseIP(ip)
					if parsed == nil || parsed.To4() == nil {
						continue // IPv6 /128 rows are out of scope for this check
					}
					addr, aerr := netip.ParseAddr(ip)
					if aerr != nil {
						continue
					}
					checked++
					if !b406Covers(prefixes, addr) {
						uncovered++
						if len(examples) < 2 {
							examples = append(examples, ip)
						}
					}
				}
				last := b411LastResolve(s, r.domain)
				verdict := exit_rules.DomainRowsVerdictFor(uncovered, last, now, interval)
				if !autoOn && verdict == exit_rules.DomainRowsPendingNextPass {
					// Nothing is scheduled, so "the next pass will fix it" is false:
					// rotation becomes permanent rot.
					verdict = exit_rules.DomainRowsStale
				}
				switch verdict {
				case exit_rules.DomainRowsPendingNextPass:
					pendingDomains = append(pendingDomains, fmt.Sprintf("%s: %d address(es) newer than the last resolve [%s] — %s",
						r.domain, uncovered, strings.Join(examples, ", "),
						exit_rules.DomainRowsVerdictNote(verdict, uncovered, last, now, interval)))
				case exit_rules.DomainRowsStale:
					reason := exit_rules.DomainRowsVerdictNote(verdict, uncovered, last, now, interval)
					if !autoOn {
						reason = "DNS auto-update is OFF, so nothing will rewrite these rows"
					}
					staleDomains = append(staleDomains, fmt.Sprintf("%s: %d address(es) in no enabled row [%s] — %s",
						r.domain, uncovered, strings.Join(examples, ", "), reason))
				}
			}
			if readErr != "" {
				// A read failure is a fact about the CHECK, never about the rows
				// (AGENTS trap #15): say so instead of reporting every address stale.
				return SystemTestFail, "could not read the device's derived rows: " + readErr
			}
			if len(staleDomains) > 0 {
				return SystemTestFail, fmt.Sprintf("%d domain rule(s) will NOT heal by themselves: %s. %s.",
					len(staleDomains), strings.Join(staleDomains[:min(3, len(staleDomains))], "; "),
					b411FixHint(autoOn, interval))
			}
			if len(pendingDomains) > 0 {
				return SystemTestPass, fmt.Sprintf("%d address(es) checked, none of them stuck: %s. The mechanism is working — these rows are simply one rotation behind.",
					checked, strings.Join(pendingDomains[:min(2, len(pendingDomains))], "; "))
			}
			return SystemTestPass, fmt.Sprintf("%d address(es) across %d domain rule(s) are inside a stored row and no answer has moved since the last resolve", checked, len(rules))
		},
	},
	{
		Name:        "exit_rules.exit_node_pin",
		Category:    "exit_rules",
		Description: "no device is silently pinned to one exit node (a via pin is a FILTER on the exit-node choice, not a preference)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "service or DB not initialised"
			}
			// B398 (2026-10-18) — THE REPORT THIS EXISTS TO PREVENT.
			//
			// On the live installation `device_exit_node_prefs` held
			// `cyborg → tag:dev-infra-karolina` with set_by_user_id=0, and the ACL wrote
			//     { "src": ["tag:dev-skyadmin-cyborg"], "dst": ["autogroup:internet"],
			//       "via": ["tag:dev-infra-karolina"] }
			// `via` there is a filter: choosing emilia in the client made the grant
			// unmatched and EVERY page stopped loading ("с karolina — всё грузится").
			// Nothing on any page said the device was restricted; the operator had to
			// find it by symptom. A human pin is legitimate, so this test PASSES, but it
			// NAMES the pin and its consequence.
			rows, err := s.dbc().QueryContext(ctx, `SELECT COALESCE(device_hostname,''), COALESCE(exit_node_tag,''),
			       COALESCE(set_by_user_id,0), COALESCE(via_enabled,0)
			  FROM device_exit_node_prefs ORDER BY device_hostname`)
			if err != nil {
				return SystemTestSkip, "read the device exit-node preferences: " + err.Error()
			}
			defer rows.Close()
			pinned, derived := []string{}, []string{}
			for rows.Next() {
				var host, tag string
				var setBy, via int
				if err := rows.Scan(&host, &tag, &setBy, &via); err != nil {
					continue
				}
				if host == "" || tag == "" || via == 0 {
					continue
				}
				if setBy > 0 {
					// B279.1: never derive a hostname from a tag by hand — the shared
					// helper knows which tags are CLASSES (a class tag has no hostname
					// and renders as ""), which is the bug B279 paid for.
					relay := exit_rules.TagToHostname(tag)
					if relay == "" {
						relay = tag
					}
					pinned = append(pinned, fmt.Sprintf("%s → %s", host, relay))
				} else {
					derived = append(derived, host)
				}
			}
			if err := rows.Err(); err != nil {
				return SystemTestSkip, "read the device exit-node preferences: " + err.Error()
			}
			if len(pinned) == 0 {
				if len(derived) > 0 {
					return SystemTestPass, fmt.Sprintf("no device is PINNED to one exit node; %d derived preference(s) are not enforced since B398 (%s), so any exit node chosen in the client works",
						len(derived), strings.Join(derived, ", "))
				}
				return SystemTestPass, "no device is pinned to a single exit node — every device may choose any exit node in its own client"
			}
			msg := fmt.Sprintf("%d device(s) are PINNED to one exit node: %s. `via` is a FILTER, so choosing another exit node in the Tailscale client blocks ALL internet traffic of that device — press ✕ in the «Exit node» column on /admin/devices (the owner does the same on /my/devices), or set it to the node you actually use, to allow free choice.",
				len(pinned), strings.Join(pinned, "; "))
			if len(derived) > 0 {
				msg += fmt.Sprintf(" (%d derived preference(s) are no longer enforced: %s)", len(derived), strings.Join(derived, ", "))
			}
			return SystemTestPass, msg
		},
	},
	{
		Name:        "ownership.attribution",
		Category:    "ownership",
		Description: "every device has a panel owner in node_owner_map (headscale's own user column says tagged-devices for a tagged node and is not an answer)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "service or DB not initialised"
			}
			// B400 (2026-10-20) — THE QUESTION IS MEASURED WHERE THE RUNTIME ASKS IT.
			//
			// Measured on the operator's installation: all 14 devices carry a tag, so
			// headscale books every one of them under the built-in user
			// `tagged-devices`; the dashboard wizard compared that string with the
			// panel's user list and therefore reported «14 node(s) have no panel owner
			// yet» for nine months of a perfectly attributed tailnet. node_owner_map
			// held a correct owner for all 14 the whole time.
			//
			// This test asks the same question the ACL, /my/*, the preference
			// reconciler and «all my devices» ask — node_owner_map — so the panel can
			// no longer report one thing here and behave as if it were another.
			owners, err := db.ListAllNodeOwners(s.dbc())
			if err != nil {
				return SystemTestSkip, "read node_owner_map: " + err.Error()
			}
			byID := make(map[string]db.NodeOwner, len(owners))
			for _, o := range owners {
				if id := strings.TrimSpace(o.NodeID); id != "" {
					byID[id] = o
				}
			}
			portalByName := map[string]bool{}
			if rows, perr := s.dbc().QueryContext(ctx, `SELECT lower(username) FROM portal_users`); perr == nil {
				defer rows.Close()
				for rows.Next() {
					var n sql.NullString
					if err := rows.Scan(&n); err == nil && n.Valid {
						portalByName[strings.TrimSpace(n.String)] = true
					}
				}
			}
			nodes, herr := s.HSGlobalFn().ListAllNodes()
			if herr != nil {
				return SystemTestSkip, "headscale did not answer: " + herr.Error()
			}
			unattributed := []string{}
			unknownOwner := []string{}
			for _, n := range nodes {
				host := strings.TrimSpace(n.GivenName)
				if host == "" {
					host = strings.TrimSpace(n.ID)
				}
				o, ok := byID[strings.TrimSpace(n.ID)]
				name := strings.ToLower(strings.TrimSpace(o.Username))
				switch {
				case !ok || name == "":
					unattributed = append(unattributed, host)
				case !portalByName[name]:
					unknownOwner = append(unknownOwner,
						fmt.Sprintf("%s → %q", host, o.Username))
				}
			}
			switch {
			case len(unattributed) == 0 && len(unknownOwner) == 0:
				return SystemTestPass, fmt.Sprintf("all %d device(s) have a panel owner in node_owner_map (headscale's User column reads tagged-devices for tagged nodes — expected, and not used as the answer)", len(nodes))
			case len(unattributed) > 0 && len(unknownOwner) > 0:
				return SystemTestFail, fmt.Sprintf("%d device(s) have NO panel owner: %s; and %d name a panel user that does not exist: %s. Adopt them on /admin/devices (or /admin/users → «Adopt as skygate user»); unattributed devices are invisible to /my/* pages, «all my devices» rules and the preference reconciler.",
					len(unattributed), strings.Join(unattributed, ", "), len(unknownOwner), strings.Join(unknownOwner, ", "))
			case len(unattributed) > 0:
				return SystemTestFail, fmt.Sprintf("%d device(s) have NO panel owner: %s. Adopt them on /admin/devices (or /admin/users → «Adopt as skygate user»); unattributed devices are invisible to /my/* pages, «all my devices» rules and the preference reconciler.",
					len(unattributed), strings.Join(unattributed, ", "))
			default:
				return SystemTestFail, fmt.Sprintf("%d device(s) name a panel user that does not exist: %s. The runtime cannot honour that attribution — fix the username or re-adopt the device on /admin/devices.",
					len(unknownOwner), strings.Join(unknownOwner, ", "))
			}
		},
	},
	{
		Name:        "backup.verify",
		Category:    "backup",
		Description: "the last backup ran, succeeded, and is recent (under 48 h)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "service or DB not initialised"
			}
			get := func(key string) string {
				v, _ := db.GetGlobalSetting(s.dbc(), key, "")
				return strings.TrimSpace(v)
			}
			status := get("backup.last_status")
			lastRun := get("backup.last_run")
			archive := get("backup.last_archive")
			if status == "" && lastRun == "" {
				return SystemTestSkip, "no backup has ever run on this installation"
			}
			if status != "" && status != "ok" {
				return SystemTestFail, fmt.Sprintf("the last backup finished with status %q (%s). Check /admin/backup and the destination.", status, get("backup.last_error"))
			}
			var ts int64
			if _, err := fmt.Sscanf(lastRun, "%d", &ts); err != nil || ts == 0 {
				return SystemTestFail, fmt.Sprintf("the last-run timestamp is unreadable (%q)", lastRun)
			}
			age := time.Since(time.Unix(ts, 0))
			if age > 48*time.Hour {
				return SystemTestFail, fmt.Sprintf("the last successful backup is %s old (under 48 h expected): %s", age.Round(time.Hour), archive)
			}
			if archive == "" {
				return SystemTestFail, "the last backup succeeded but recorded no archive name — verify it exists before you need it"
			}
			return SystemTestPass, fmt.Sprintf("last backup %s ago: %s", age.Round(time.Minute), archive)
		},
	},
}

// b396AllLoopback reports whether EVERY resolved address is a loopback address.
// Split out so the classification can be unit-tested without a network
// (system_tests_b396_test.go): a host that resolves its own public name to
// 127.0.0.1 is the failure this exists to name.
func b396AllLoopback(addrs []string) bool {
	if len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		ip := net.ParseIP(strings.TrimSpace(a))
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// b394Headscale resolves the headscale client the same way the existing tests do,
// with a named reason when there is none (a SKIP must say why, rule 1).
func b394Headscale() (*headscale.Client, error) {
	s := getTestService()
	if s == nil {
		return nil, fmt.Errorf("service not initialised")
	}
	if s.HSGlobalFn == nil {
		return nil, fmt.Errorf("headscale client not wired (nil HSGlobalFn)")
	}
	hs := s.HSGlobalFn()
	if hs == nil {
		return nil, fmt.Errorf("headscale client not configured (set HEADSCALE_URL + HEADSCALE_API_KEY)")
	}
	return hs, nil
}

// b394RequiredTables is the schema the panel cannot work without. It lists the
// tables whose ABSENCE means a migration did not run — deliberately not every
// table (an old install still works without the newest optional feature), so the
// check fails on a broken schema rather than on an incomplete rollout.
var b394RequiredTables = []string{
	"portal_users", "devices", "device_rules", "node_owner_map", "global_settings",
	"prefix_owner", "exit_servers", "device_exit_node_prefs", "user_exit_node_prefs",
	"acl_snapshots", "audit_log", "notifications", "system_tests_runs",
	"monitor_events", "derp_relays", "preauth_keys",
}

// b394SplitURL turns a DERP URL into host and port (443 by default).
func b394SplitURL(raw string) (string, string, error) {
	if raw == "" {
		return "", "", fmt.Errorf("empty URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", "", fmt.Errorf("no host in %q", raw)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return host, port, nil
}

func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// b394DataDir is the filesystem the panel writes to: the SQLite file's own
// directory when SKYGATE_DB names one, else the container's /data mount, else the
// process working directory. Written as a chain of existence checks rather than a
// config field so the test works on every install kind without new plumbing.
func b394DataDir() string {
	if dsn := strings.TrimSpace(os.Getenv("SKYGATE_DB")); strings.HasPrefix(dsn, "sqlite:") {
		if dir := filepath.Dir(strings.TrimPrefix(dsn, "sqlite:")); dir != "" && dir != "." {
			return dir
		}
	}
	for _, candidate := range []string{"/data", "/var/lib/skygate"} {
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "/"
}
