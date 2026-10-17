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
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"os"
	"path/filepath"

	"skygate/internal/acl"
	"skygate/internal/db"
	"skygate/internal/derphealth"
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
			for _, n := range nodes {
				host := strings.ToLower(n.GivenName)
				routesByHost[host] = n.AvailableRoutes
				for _, t := range n.Tags {
					if strings.HasPrefix(t, "tag:dev-infra-") {
						tagByHost[host] = t
					}
				}
			}
			problems := []string{}
			for prefix, tag := range ownerByPrefix {
				host := strings.ToLower(hostByTag[strings.ToLower(tag)])
				if host == "" {
					problems = append(problems, fmt.Sprintf("%s is owned by %s, which is not a node in this tailnet", prefix, tag))
					continue
				}
				if !containsStr(routesByHost[host], prefix) {
					problems = append(problems, fmt.Sprintf("%s is owned by %s, but %s does not advertise it", prefix, host, host))
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
			if len(problems) > 0 || dupes > 0 {
				return SystemTestFail, fmt.Sprintf("%d prefix(es) are not served as recorded and %d prefix(es) are advertised by more than one relay. First: %s. Re-sync on /admin/exit-nodes.",
					len(problems), dupes, strings.Join(problems[:min(3, len(problems))], "; "))
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
			stale := []string{}
			checked := 0
			for _, r := range rules {
				lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				ips, lerr := net.DefaultResolver.LookupHost(lctx, r.domain)
				cancel()
				if lerr != nil {
					stale = append(stale, fmt.Sprintf("%s no longer resolves (%v)", r.domain, lerr))
					continue
				}
				for _, ip := range ips {
					parsed := net.ParseIP(ip)
					if parsed == nil || parsed.To4() == nil {
						continue // IPv6 /128 rows are out of scope for this check
					}
					checked++
					var n int
					if err := s.dbc().QueryRowContext(ctx,
						`SELECT COUNT(*) FROM device_rules WHERE device_id = $1 AND target_value = $2 AND enabled = 1`,
						r.deviceID, ip+"/32").Scan(&n); err != nil {
						continue
					}
					if n == 0 {
						stale = append(stale, fmt.Sprintf("%s → %s has no rule row", r.domain, ip))
					}
				}
			}
			if len(stale) > 0 {
				return SystemTestFail, fmt.Sprintf("%d of %d address(es) are stale or missing: %s. The domain auto-updater rewrites them on its interval — check /admin/system_tests → DNS auto-update, or press Re-sync on the rule.",
					len(stale), checked, strings.Join(stale[:min(3, len(stale))], "; "))
			}
			return SystemTestPass, fmt.Sprintf("%d address(es) across %d domain rule(s) match today's DNS", checked, len(rules))
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
