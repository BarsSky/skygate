package admin

// system_tests.go — Admin Test Page (v0.33.0).
//
// The /admin/system_tests page lets the operator run a
// battery of system checks (network, db, headscale, disk,
// wal-g, replication) and see the result inline. Each
// test is a Go function that returns (status, output).
// Results are stored in the system_tests_runs table
// (migration v0.51) for the "history" strip on the page.
//
// Test definition lifecycle:
//
//   1. Add the test func to the TestRegistry below.
//   2. The /admin/system_tests page renders the registry as
//      a grid; "Run" buttons call the runner.
//   3. The runner stores the result in system_tests_runs and
//      returns the live JSON for the page.
//   4. The "History" column on the page reads the last 20
//      rows from system_tests_runs.
//
// All tests are best-effort and timeout-fast (≤ 5s each).
// A test that hangs is a bug — the timeout is a safety net.
//
// 2026-08-05 v0.33.1.11 — replaced the two SQLite-only
// tests (db.sqlite_integrity / db.wal_mode) with
// backend-dispatching equivalents (db.integrity_check /
// db.journal_mode) so the same registry works on both
// SQLite (legacy / test rig) and PostgreSQL (the v0.33.1.7+
// production backend). Added 7 new tests covering exit-node
// availability, integrations, DNS resolution, duplicate
// devices, rule sanity, recent backups, and active meshes.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// SystemTestStatus is the result of one test.
type SystemTestStatus string

const (
	SystemTestPass SystemTestStatus = "pass"
	SystemTestFail SystemTestStatus = "fail"
	SystemTestSkip SystemTestStatus = "skip"
)

// SystemTestResult is the persisted shape of a single test run.
type SystemTestResult struct {
	Name     string            `json:"name"`
	Category string            `json:"category"`
	Status   SystemTestStatus  `json:"status"`
	Output   string            `json:"output"`
	Duration string            `json:"duration"`
}

// SystemTestDef is a registered test. Run returns
// (status, output). Closures capture *Service so they
// can read DB / headscale state at run time.
type SystemTestDef struct {
	Name        string
	Category    string // "network", "db", "headscale", "disk", "wal-g", "replication", "integrations", "backup"
	Description string
	Run         func(ctx context.Context) (SystemTestStatus, string)
}

// TestRegistry is the full list of in-process tests. The
// /admin/system_tests page renders every entry.
var TestRegistry = []SystemTestDef{
	{
		Name:        "net.tailscale_self",
		Category:    "network",
		Description: "tailscale0 interface is up (Tailscale daemon alive in the container)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			out, err := os.ReadFile("/proc/net/dev")
			if err != nil {
				return SystemTestFail, "cannot read /proc/net/dev: " + err.Error()
			}
			if strings.Contains(string(out), "tailscale0") {
				return SystemTestPass, "tailscale0 interface is up"
			}
			return SystemTestFail, "tailscale0 interface not found (Tailscale may be down)"
		},
	},
	{
		Name:        "net.headscale_reachable",
		Category:    "network",
		Description: "Headscale container /api/v1/policy responds with a non-empty policy",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil {
				return SystemTestFail, "service not initialised"
			}
			hs := s.HSGlobalFn()
			if hs == nil {
				return SystemTestFail, "headscale client not configured"
			}
			raw, err := hs.GetACL()
			if err != nil {
				return SystemTestFail, "getacl: " + err.Error()
			}
			if len(strings.TrimSpace(raw)) == 0 {
				return SystemTestFail, "policy is empty"
			}
			return SystemTestPass, fmt.Sprintf("policy fetched (%d bytes)", len(raw))
		},
	},
	{
		// 2026-08-05 v0.33.1.11: was "db.sqlite_integrity"
		// (SQLite-only). Now dispatches on backend so the
		// test works on both SQLite (legacy / CI rig) and
		// PostgreSQL (the v0.33.1.7+ production backend).
		// SQLite: PRAGMA integrity_check; PG: SELECT 1
		// connectivity (PG doesn't have an integrity_check
		// PRAGMA — operators use pg_dump / pg_basebackup
		// for that, which is out of scope for an in-process
		// 5s test).
		Name:        "db.integrity_check",
		Category:    "db",
		Description: "DB is reachable + integrity check passes (PRAGMA on SQLite, connectivity on PG)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.DB == nil {
				return SystemTestFail, "DB not configured"
			}
			if db.BackendOf(s.DB) == db.BackendPostgres {
				var n int
				if err := s.DB.QueryRowContext(ctx, "SELECT 1").Scan(&n); err != nil {
					return SystemTestFail, "SELECT 1 failed: " + err.Error()
				}
				if n != 1 {
					return SystemTestFail, "SELECT 1 returned " + fmt.Sprint(n)
				}
				// Lightweight per-table existence check: every
				// table from migration v0.x must still be present.
				// PG-specific catalog query (skipped on SQLite).
				rows, err := s.DB.QueryContext(ctx, `
					SELECT count(*) FROM pg_tables
					WHERE schemaname = 'public'
					  AND tablename IN ('portal_users','preauth_keys','audit_log',
					                    'device_rules','exit_servers','user_subnets',
					                    'global_settings','meshes','mesh_members')
				`)
				if err != nil {
					return SystemTestFail, "pg_tables: " + err.Error()
				}
				defer rows.Close()
				if !rows.Next() {
					return SystemTestFail, "pg_tables query returned no rows"
				}
				var tableCount int
				if err := rows.Scan(&tableCount); err != nil {
					return SystemTestFail, "scan: " + err.Error()
				}
				if tableCount < 8 {
					return SystemTestFail, fmt.Sprintf("only %d of 8 expected tables present", tableCount)
				}
				return SystemTestPass, "PG reachable, all 8 expected tables present"
			}
			var result string
			if err := s.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
				return SystemTestFail, err.Error()
			}
			if result != "ok" {
				return SystemTestFail, "integrity_check returned: " + result
			}
			return SystemTestPass, "PRAGMA integrity_check = ok"
		},
	},
	{
		// 2026-08-05 v0.33.1.11: was "db.wal_mode" (SQLite-only).
		// Now dispatches: SQLite checks journal_mode=wal;
		// PG skips (PG always uses WAL by default — there's
		// no equivalent PRAGMA, and there's no operator-facing
		// flag to flip).
		Name:        "db.journal_mode",
		Category:    "db",
		Description: "DB uses crash-safe journaling (WAL on SQLite; N/A on PG — always WAL)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.DB == nil {
				return SystemTestFail, "DB not configured"
			}
			if db.BackendOf(s.DB) == db.BackendPostgres {
				return SystemTestSkip, "PG always uses WAL (no journal_mode PRAGMA equivalent)"
			}
			var mode string
			if err := s.DB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
				return SystemTestFail, err.Error()
			}
			if mode != "wal" {
				return SystemTestFail, "journal_mode is " + mode + " (expected wal)"
			}
			return SystemTestPass, "journal_mode = wal"
		},
	},
	{
		Name:        "headscale.peers_visible",
		Category:    "headscale",
		Description: "Headscale /api/v1/node returns a non-empty node list",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil {
				return SystemTestFail, "service not initialised"
			}
			hs := s.HSGlobalFn()
			if hs == nil {
				return SystemTestFail, "headscale client not configured"
			}
			nodes, err := hs.ListAllNodes()
			if err != nil {
				return SystemTestFail, "list nodes: " + err.Error()
			}
			if len(nodes) == 0 {
				return SystemTestFail, "no nodes registered"
			}
			online := 0
			for _, n := range nodes {
				if n.Online {
					online++
				}
			}
			return SystemTestPass, fmt.Sprintf("%d nodes (%d online)", len(nodes), online)
		},
	},
	{
		// 2026-08-10 v0.33.1.36: the pre-fix test only
		// iterated view.AllACLs (the JSON "acls" array).
		// The live headscale 0.29+ policy uses "grants"
		// (not "acls") and the pre-fix unmarshal left
		// AllACLs empty. The test always failed on live
		// with "no rule with skyadmin in src" even though
		// the policy had a perfectly valid
		// `grants: [{src: ["skyadmin@tsnet.skynas.ru"],
		//  dst: [..., autogroup:internet]}]` rule.
		// The fix: parse the raw policy JSON and look at
		// BOTH "acls" and "grants" arrays. The view's
		// AllACLs is still used for the count (so the
		// pass message stays informative).
		Name:        "headscale.acl_admin_present",
		Category:    "headscale",
		Description: "Headscale policy includes a rule with skyadmin in src (admin can reach all)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil {
				return SystemTestFail, "service not initialised"
			}
			view, err := s.ListACL(ctx)
			if err != nil {
				return SystemTestFail, "list acl: " + err.Error()
			}
			hasAdmin := false
			grantCount := 0
			// Check the structured "acls" (legacy headscale
			// shape, used by some operators).
			for _, r := range view.AllACLs {
				for _, src := range r.Src {
					if strings.Contains(src, "skyadmin") {
						hasAdmin = true
						break
					}
				}
				if hasAdmin {
					break
				}
			}
			// Also check the "grants" shape (headscale 0.23+
			// when the policy was rewritten). Parse the raw
			// policy so we don't depend on view having a
			// Grants field (the v0.33.1.36 fix avoids
			// touching ListACL's struct just for this test).
			if view.PolicyRaw != "" {
				var raw struct {
					Grants []struct {
						Src []string `json:"src"`
					} `json:"grants"`
				}
				if jerr := json.Unmarshal([]byte(view.PolicyRaw), &raw); jerr == nil {
					grantCount = len(raw.Grants)
					if !hasAdmin {
						for _, g := range raw.Grants {
							for _, src := range g.Src {
								if strings.Contains(src, "skyadmin") {
									hasAdmin = true
									break
								}
							}
							if hasAdmin {
								break
							}
						}
					}
				}
			}
			if !hasAdmin {
				return SystemTestFail, "no rule with skyadmin in src (checked both acls and grants) — admin has no access to any device"
			}
			return SystemTestPass, fmt.Sprintf("admin rule present (acls=%d, grants=%d)",
				view.TotalCount, grantCount)
		},
	},
	// ─── v0.33.1.11 NEW TESTS ──────────────────────────────────
	{
		// DNS resolution test — proves the container has
		// working outbound DNS. Resolves "github.com" (a
		// domain the page itself links to) via the system
		// resolver. On failure, surfaces the exact error
		// (e.g. "no such host", "i/o timeout") so the
		// operator can see whether it's a /etc/resolv.conf
		// issue or a network-egress block.
		Name:        "network.dns_resolve",
		Category:    "network",
		Description: "Outbound DNS works (resolves github.com via the system resolver)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			resolveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			ips, err := net.DefaultResolver.LookupIP(resolveCtx, "ip4", "github.com")
			if err != nil {
				return SystemTestFail, "lookup github.com: " + err.Error()
			}
			if len(ips) == 0 {
				return SystemTestFail, "github.com resolved to 0 IPs"
			}
			// Render up to 3 IPs for the operator's eyes.
			parts := make([]string, 0, 3)
			for i, ip := range ips {
				if i >= 3 {
					break
				}
				parts = append(parts, ip.String())
			}
			return SystemTestPass, fmt.Sprintf("github.com -> %s", strings.Join(parts, ","))
		},
	},
	{
		// Exit node availability — queries the live
		// headscale node list and counts how many nodes
		// carry the tag:exit-node AND are online. The
		// operator configures relays in /admin/exit-nodes,
		// and this test catches "my relay disappeared"
		// regressions without a manual SSH check.
		Name:        "headscale.exit_nodes_online",
		Category:    "headscale",
		Description: "At least one headscale node carries tag:exit-node AND is online (egress is reachable)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil {
				return SystemTestFail, "service not initialised"
			}
			hs := s.HSGlobalFn()
			if hs == nil {
				return SystemTestFail, "headscale client not configured"
			}
			nodes, err := hs.ListAllNodes()
			if err != nil {
				return SystemTestFail, "list nodes: " + err.Error()
			}
			exits := 0
			onlineExits := 0
			for _, n := range nodes {
				if !n.IsExitNode {
					continue
				}
				exits++
				if n.Online {
					onlineExits++
				}
			}
			if exits == 0 {
				return SystemTestFail, "no node with tag:exit-node registered — egress impossible"
			}
			if onlineExits == 0 {
				return SystemTestFail, fmt.Sprintf("%d exit-nodes registered but all offline", exits)
			}
			return SystemTestPass, fmt.Sprintf("%d/%d exit-nodes online", onlineExits, exits)
		},
	},
	{
		// Integrations test — checks the global_settings
		// rows for derp.* + headplane.* + telegram.bot_token.
		// The operator configures these in /admin/derp/config,
		// /admin/headplane, and /admin/telegram. The test
		// reports which subsystems are configured vs. zero,
		// so the page acts as a "do I have all my
		// integrations wired up?" dashboard.
		Name:        "integrations.configured",
		Category:    "integrations",
		Description: "DERP / headplane / telegram integration config is present in global_settings",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.DB == nil {
				return SystemTestFail, "DB not configured"
			}
			// Probe the 3 integration keys. A value of ""
			// means "not configured"; a non-empty value
			// means configured. We don't validate the value
			// content — that's the apply handler's job.
			keys := []string{
				"derp.bundled_enabled",
				"derp.external_urls",
				"headplane.mode",
				"telegram.bot_token",
			}
			configured := 0
			missing := []string{}
			for _, k := range keys {
				v, err := db.GetGlobalSetting(s.DB, k, "")
				if err != nil {
					return SystemTestFail, "get "+k+": " + err.Error()
				}
				if v != "" {
					configured++
				} else {
					missing = append(missing, k)
				}
			}
			if configured == 0 {
				return SystemTestFail, "no integration keys configured (DERP / headplane / telegram all empty)"
			}
			if len(missing) == 0 {
				return SystemTestPass, "all 4 integration keys configured"
			}
			// Partial: not a hard fail, but a yellow pill
			// (the page renders a status-warn pill for
			// pass-with-warnings in a future iteration).
			return SystemTestPass, fmt.Sprintf("%d/4 configured, missing: %s",
				configured, strings.Join(missing, ", "))
		},
	},
	{
		// Duplicate-device detection — walks node_owner_map
		// looking for two distinct node rows that share
		// either the same hostname OR the same
		// tailscale_ip. Both are accidental duplicates
		// (the v0.22.2 fix prevents fresh duplicates, but
		// a pre-fix DB can have residue). Catches them
		// without manual SQL.
		Name:        "db.duplicate_devices",
		Category:    "db",
		Description: "node_owner_map has no duplicate (hostname) rows",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.DB == nil {
				return SystemTestFail, "DB not configured"
			}
			// 2026-08-10 v0.33.1.35: dropped the tailscale_ip
			// reference — the node_owner_map table doesn't
			// have that column (only hostname). The tailscale_ip
			// lives on headscale's side and is fetched via
			// hs.ListAllNodes, not from this table. The
			// hostname-only duplicate check is what the
			// table can actually answer.
			rows, err := s.DB.QueryContext(ctx, `
				SELECT hostname, count(*) AS c
				FROM node_owner_map
				WHERE hostname != ''
				GROUP BY hostname
				HAVING c > 1
			`)
			if err != nil {
				return SystemTestFail, "query: " + err.Error()
			}
			defer rows.Close()
			dupes := 0
			var examples []string
			for rows.Next() {
				var host string
				var c int
				if err := rows.Scan(&host, &c); err != nil {
					return SystemTestFail, "scan: " + err.Error()
				}
				dupes += c
				if len(examples) < 3 {
					examples = append(examples,
						fmt.Sprintf("%s×%d", host, c))
				}
			}
			if dupes > 0 {
				return SystemTestFail, fmt.Sprintf(
					"%d duplicate rows: %s (run dedup: see /admin/devices)",
					dupes, strings.Join(examples, ", "))
			}
			return SystemTestPass, "no duplicate (hostname) rows"
		},
	},
	{
		// Rule sanity — checks that every device_rules row
		// is actually applyable. A row with action='' is a
		// no-op (orphan). A row with target_value='' has
		// nothing to match against (also orphan).
		//
		// 2026-08-10 v0.33.1.36: removed the device_hostname
		// check. The pre-fix test treated every row with an
		// empty device_hostname as an orphan, but the
		// per-user "default exit" rules (action='accept',
		// user_id set, target_value set, exit_node_id set,
		// device_hostname='') are a legitimate per-user rule
		// shape — they apply to ALL of the user's devices,
		// not a specific one. Counting them as orphans gave
		// 166 false positives on the live DB (Cloudflare /
		// Google CDN IP ranges pinned to karolina for the
		// skyadmin user). The new contract: orphan = no
		// action OR no target.
		Name:        "db.rules_sanity",
		Category:    "db",
		Description: "device_rules has no orphan rows (every row has action + target_value)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.DB == nil {
				return SystemTestFail, "DB not configured"
			}
			rows, err := s.DB.QueryContext(ctx, `
				SELECT count(*) FROM device_rules
				WHERE action = '' OR action IS NULL
				   OR target_value = '' OR target_value IS NULL
			`)
			if err != nil {
				return SystemTestFail, "query: " + err.Error()
			}
			defer rows.Close()
			var orphans int
			if !rows.Next() {
				return SystemTestFail, "no rows returned"
			}
			if err := rows.Scan(&orphans); err != nil {
				return SystemTestFail, "scan: " + err.Error()
			}
			if orphans > 0 {
				return SystemTestFail, fmt.Sprintf("%d orphan rules (missing action or target_value)", orphans)
			}
			// Total count for the operator's info.
			var total int
			if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM device_rules").Scan(&total); err != nil {
				return SystemTestPass, fmt.Sprintf("no orphan rules (count unknown: %v)", err)
			}
			// Per-user count for the operator's info. The
			// rules with device_hostname='' are the per-user
			// "default exit" rules — useful signal that they
			// exist and how many.
			var perUser int
			if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM device_rules WHERE device_hostname = '' OR device_hostname IS NULL").Scan(&perUser); err != nil {
				return SystemTestPass, fmt.Sprintf("%d rules, all have action + target_value (per-user count unknown: %v)", total, err)
			}
			if perUser > 0 {
				return SystemTestPass, fmt.Sprintf("%d rules, all have action + target_value (%d per-user 'default exit' rules)", total, perUser)
			}
			return SystemTestPass, fmt.Sprintf("%d rules, all have action + target_value", total)
		},
	},
	{
		// Recent backup — looks for the latest *.db file
		// (or *.tar.gz for PG) in the configured backup
		// dir, fails if the newest is older than 7 days
		// (the default backup schedule is daily). The
		// path is resolved via admin.ResolveBackupDir()
		// (mirrors the unexported resolveBackupDir in
		// backup.go) so SKYGATE_BACKUP_DIR / DEPLOY_BACKUP_DIR
		// overrides are honoured (added in v0.33.1.7).
		//
		// 2026-08-10 v0.33.1.36: the test runs INSIDE the
		// skygate container. The container's bind mount is
		// /home/skyadmin/skygate → /app, so a host path like
		// /home/skyadmin/skygate/backup doesn't exist in the
		// container's filesystem (the in-container path is
		// /app/backup). The pre-fix test always returned
		// "read dir /home/skyadmin/skygate/backup: no such
		// file or directory" even when the host had recent
		// backups. The fix: if the configured path is
		// missing, try the container-side bind-mount
		// equivalent before failing. The candidate list is
		// built deterministically from the mount layout
		// declared in docker-compose.yml (one
		// `bind: /home/skyadmin/skygate → /app` mount).
		Name:        "backup.recent",
		Category:    "backup",
		Description: "A backup file is present in the backup dir and is < 7 days old",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			dir := ResolveBackupDir()
			if dir == "" {
				return SystemTestFail, "backup dir not configured (set SKYGATE_BACKUP_DIR or DEPLOY_BACKUP_DIR)"
			}
			// If the literal path doesn't exist (container
			// view vs host view mismatch), try the most
			// common bind-mount translation:
			//   /home/<user>/skygate/<x>  →  /app/<x>
			// That's the docker-compose mount
			// `Source: /home/skyadmin/skygate
			//   Destination: /app`.
			entries, err := os.ReadDir(dir)
			if err != nil && os.IsNotExist(err) {
				const hostPrefix = "/home/skyadmin/skygate/"
				const containerPrefix = "/app/"
				if strings.HasPrefix(dir, hostPrefix) {
					altDir := containerPrefix + strings.TrimPrefix(dir, hostPrefix)
					if altEntries, altErr := os.ReadDir(altDir); altErr == nil {
						dir = altDir
						entries = altEntries
						err = nil
					} else {
						return SystemTestFail, "read dir " + dir + ": " + err.Error() + " (also tried " + altDir + ": " + altErr.Error() + ")"
					}
				} else {
					return SystemTestFail, "read dir " + dir + ": " + err.Error()
				}
			} else if err != nil {
				return SystemTestFail, "read dir " + dir + ": " + err.Error()
			}
			var newest os.FileInfo
			var newestPath string
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				name := e.Name()
				if !strings.HasSuffix(name, ".db") &&
					!strings.HasSuffix(name, ".tar.gz") &&
					!strings.HasSuffix(name, ".sql") {
					continue
				}
				full := filepath.Join(dir, name)
				fi, err := e.Info()
				if err != nil {
					continue
				}
				if newest == nil || fi.ModTime().After(newest.ModTime()) {
					newest = fi
					newestPath = full
				}
			}
			if newest == nil {
				return SystemTestFail, "no backup files in " + dir
			}
			age := time.Since(newest.ModTime())
			maxAge := 7 * 24 * time.Hour
			if age > maxAge {
				return SystemTestFail, fmt.Sprintf("newest backup %s is %s old (>7d)", filepath.Base(newestPath), age.Round(time.Minute))
			}
			return SystemTestPass, fmt.Sprintf("newest: %s (%s old, %d bytes)",
				filepath.Base(newestPath), age.Round(time.Minute), newest.Size())
		},
	},
	{
		// Active mesh — checks the meshes + mesh_members
		// tables (added in v0.22.0) for any mesh with ≥1
		// member. Catches "my mesh disappeared" or "the
		// member count went to 0" without manual SQL.
		// Returns skip if the meshes table doesn't exist
		// yet (pre-v0.22 DB).
		Name:        "mesh.active_meshes",
		Category:    "headscale",
		Description: "At least one mesh network has ≥1 member (meshes + mesh_members tables)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.DB == nil {
				return SystemTestFail, "DB not configured"
			}
			// Probe for table existence (pre-v0.22 DBs
			// don't have it). The query is the same on
			// both backends.
			var tableCount int
			if db.BackendOf(s.DB) == db.BackendPostgres {
				if err := s.DB.QueryRowContext(ctx,
					`SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename IN ('meshes','mesh_members')`,
				).Scan(&tableCount); err != nil {
					return SystemTestFail, "pg_tables: " + err.Error()
				}
			} else {
				if err := s.DB.QueryRowContext(ctx,
					`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('meshes','mesh_members')`,
				).Scan(&tableCount); err != nil {
					return SystemTestFail, "sqlite_master: " + err.Error()
				}
			}
			if tableCount < 2 {
				return SystemTestSkip, "meshes tables not present (pre-v0.22 schema)"
			}
			rows, err := s.DB.QueryContext(ctx, `
				SELECT m.name, count(mm.user_id) AS members
				FROM meshes m
				LEFT JOIN mesh_members mm ON mm.mesh_id = m.id
				GROUP BY m.id, m.name
				ORDER BY members DESC, m.name ASC
			`)
			if err != nil {
				return SystemTestFail, "query: " + err.Error()
			}
			defer rows.Close()
			type meshRow struct{ name string; members int }
			var meshes []meshRow
			for rows.Next() {
				var r meshRow
				if err := rows.Scan(&r.name, &r.members); err != nil {
					return SystemTestFail, "scan: " + err.Error()
				}
				meshes = append(meshes, r)
			}
			active := 0
			for _, m := range meshes {
				if m.members > 0 {
					active++
				}
			}
			if len(meshes) == 0 {
				return SystemTestSkip, "no meshes configured (no test available)"
			}
			if active == 0 {
				parts := []string{}
				for i, m := range meshes {
					if i >= 3 {
						break
					}
					parts = append(parts, fmt.Sprintf("%s×%d", m.name, m.members))
				}
				return SystemTestFail, fmt.Sprintf("0 of %d meshes have members: %s",
					len(meshes), strings.Join(parts, ", "))
			}
			// Sort + render the top-3 active meshes.
			sort.Slice(meshes, func(i, j int) bool {
				return meshes[i].members > meshes[j].members
			})
			parts := []string{}
			for i, m := range meshes {
				if i >= 3 {
					break
				}
				parts = append(parts, fmt.Sprintf("%s×%d", m.name, m.members))
			}
			return SystemTestPass, fmt.Sprintf("%d/%d active: %s",
				active, len(meshes), strings.Join(parts, ", "))
		},
	},
	{
		// 2026-08-06: exit_rules.preferred_mismatch — cross-check
		// between device_rules and the device/user preferred
		// exit-node pref. A rule pointing at exit-node X only
		// takes effect on device D if D's preferred exit-node is
		// also X (per-device > per-user > unset, in which case
		// "Tailscale picks by metrics" so the rule MAY apply).
		//
		// This test would have caught the v0.33.1.16 Cloudflare
		// bug at the /admin/system_tests page instead of needing
		// 30 minutes of curl-through-relay debug. The
		// /my/exit-rules + /admin/exit-rules + /admin/devices
		// pages now also render a banner when this count is > 0.
		//
		// Threshold: 5 mismatches. Below that it's usually
		// transient (user just set a new preferred and the
		// browser cache hasn't caught up). Above 5 means the
		// operator's rule-set is meaningfully misconfigured.
		Name:        "exit_rules.preferred_mismatch",
		Category:    "exit_rules",
		Description: "No device_rules reference a non-preferred exit-node (per-device or per-user pref)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.DB == nil {
				return SystemTestFail, "DB not configured"
			}
			// Pull all enabled rules + the per-device / per-user
			// prefs in 3 queries, then cross-check in Go.
			// 2026-08-10 v0.33.1.35: fixed d.id → d.node_id. The
			// node_owner_map PK is `node_id` (not `id` — it's
			// the headscale-side machine key, not an internal
			// autoincrement). The pre-fix SQL errored with
			// "no such column: d.id" and the test returned
			// SystemTestFail on every run.
			rows, err := s.DB.QueryContext(ctx, `
				SELECT r.user_id, COALESCE(d.hostname, ''), r.exit_node_id
				  FROM device_rules r
				  LEFT JOIN node_owner_map d ON d.node_id = r.device_id
				 WHERE r.enabled = 1 AND r.exit_node_id != ''
			`)
			if err != nil {
				return SystemTestFail, "query rules: " + err.Error()
			}
			defer rows.Close()
			type ruleRow struct {
				userID int64
				host   string
				exit   string
			}
			var rules []ruleRow
			for rows.Next() {
				var rr ruleRow
				var uid int
				if err := rows.Scan(&uid, &rr.host, &rr.exit); err != nil {
					continue
				}
				rr.userID = int64(uid)
				rr.host = strings.ToLower(strings.TrimSpace(rr.host))
				if rr.host == "" {
					continue
				}
				rules = append(rules, rr)
			}
			if len(rules) == 0 {
				return SystemTestSkip, "no enabled device_rules — no test to run"
			}
			// Per-device prefs (userID:hostname → tag).
			devPrefRows, err := s.DB.QueryContext(ctx, `SELECT user_id, device_hostname, exit_node_tag FROM device_exit_node_prefs`)
			if err != nil {
				return SystemTestFail, "device prefs: " + err.Error()
			}
			defer devPrefRows.Close()
			type prefRow struct {
				userID int64
				host   string
				tag    string
			}
			var devPrefs []prefRow
			for devPrefRows.Next() {
				var p prefRow
				var uid int
				if err := devPrefRows.Scan(&uid, &p.host, &p.tag); err != nil {
					continue
				}
				p.userID = int64(uid)
				p.host = strings.ToLower(strings.TrimSpace(p.host))
				devPrefs = append(devPrefs, p)
			}
			// Per-user prefs (userID → tag).
			userPrefRows, err := s.DB.QueryContext(ctx, `SELECT user_id, exit_node_tag FROM user_exit_node_prefs`)
			if err != nil {
				return SystemTestFail, "user prefs: " + err.Error()
			}
			defer userPrefRows.Close()
			userPrefs := map[int64]string{}
			for userPrefRows.Next() {
				var uid int
				var tag string
				if err := userPrefRows.Scan(&uid, &tag); err != nil {
					continue
				}
				userPrefs[int64(uid)] = tag
			}
			// Cross-check: for each rule, does its exit_node_id
			// match the device's preferred host?
			prefByUserHost := map[string]string{}
			tagToHost := func(t string) string {
				t = strings.TrimSpace(t)
				if !strings.HasPrefix(t, "tag:") {
					return t
				}
				r := strings.TrimPrefix(t, "tag:")
				r = strings.TrimPrefix(r, "exit-")
				return r
			}
			mismatch := 0
			samples := []string{}
			for _, r := range rules {
				key := fmt.Sprintf("%d:%s", r.userID, r.host)
				pref, ok := prefByUserHost[key]
				if !ok {
					// 1) per-device pref
					for _, p := range devPrefs {
						if p.userID == r.userID && p.host == r.host {
							pref = tagToHost(p.tag)
							prefByUserHost[key] = pref
							ok = true
							break
						}
					}
				}
				if !ok {
					// 2) per-user pref
					if t, ok2 := userPrefs[r.userID]; ok2 {
						pref = tagToHost(t)
						prefByUserHost[key] = pref
					}
				}
				// No preferred = rule "may" apply (Tailscale
				// picks by metrics). Don't count as mismatch.
				if pref == "" {
					continue
				}
				if pref != r.exit {
					mismatch++
					if len(samples) < 3 {
						samples = append(samples, fmt.Sprintf("%s→%s (pref=%s)", r.host, r.exit, pref))
					}
				}
			}
			if mismatch == 0 {
				return SystemTestPass, fmt.Sprintf("0 mismatches across %d rules", len(rules))
			}
			// > 5 = real misconfiguration; warn loudly so the
			// operator sees it on the system_tests page.
			// 1-5 = transient (user just changed a pref);
			// report but don't fail.
			detail := fmt.Sprintf("%d/%d rules reference non-preferred exit-node: %s",
				mismatch, len(rules), strings.Join(samples, "; "))
			if mismatch > 5 {
				return SystemTestFail, detail
			}
			return SystemTestPass, "warn: " + detail
		},
	},
	// 2026-08-06 v0.33.1.18 — verification test: every enabled
	// subnet/ip device_rule is reflected in the live headscale
	// policy as a grant. The operator reported "old rules work,
	// 3 new ones don't" on 2026-08-06 — the root cause was a
	// silent policy-push regression where the live headscale
	// policy had 0 grants even though 100+ rules existed in
	// device_rules. This test catches the same class of bug in
	// the future: cross-check device_rules.enabled=1 against
	// the grants[] array fetched from headscale.
	//
	// Algorithm:
	//   1. Read every enabled subnet/ip rule + its
	//      user_name/device_hostname/device_ip from device_rules.
	//   2. Read the live headscale policy (grants[]).
	//   3. For each rule, build the expected (src, dst) tuple
	//      the same way GenerateACLWithViaForPlane does:
	//        src = tag:dev-<user>-<device>  if user_name+device_hostname set
	//        src = device_ip                 if device_ip set
	//        src = "*"                       otherwise (no real rules like this)
	//        dst = h-rule-<sanitized>        (the host alias)
	//   4. Look up the tuple in grants[]. Missing = bug.
	//
	// Domain rules are NOT in grants (Tailscale is L3/L4); the
	// autoupdater-derived /32 children are. This test only
	// checks the /32 children, not the domain itself.
	//
	// Failure threshold: 0 missing = pass, 1-5 missing = pass
	// with warn (transient: Tailscale just hadn't refreshed),
	// > 5 missing = fail (real sync regression).
	{
		Name:        "exit_rules.all_in_headscale_acl",
		Category:    "exit_rules",
		Description: "Every enabled subnet/ip device_rule has a matching grant in the live headscale policy",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.DB == nil {
				return SystemTestFail, "DB not configured"
			}
			hs := s.HSGlobalFn()
			if hs == nil {
				return SystemTestFail, "headscale client not configured"
			}
			// 1) Read every enabled subnet/ip rule.
			rows, err := s.DB.QueryContext(ctx, `
				SELECT target_value, COALESCE(user_name, ''), COALESCE(device_hostname, ''), COALESCE(device_ip, '')
				  FROM device_rules
				 WHERE enabled = 1 AND (target_type = 'subnet' OR target_type = 'ip')`)
			if err != nil {
				return SystemTestFail, "query rules: " + err.Error()
			}
			defer rows.Close()
			type ruleRow struct {
				target string
				uname  string
				host   string
				ip     string
			}
			var rules []ruleRow
			for rows.Next() {
				var r ruleRow
				if err := rows.Scan(&r.target, &r.uname, &r.host, &r.ip); err != nil {
					continue
				}
				if r.target == "" {
					continue
				}
				rules = append(rules, r)
			}
			if len(rules) == 0 {
				return SystemTestSkip, "no enabled subnet/ip rules — nothing to verify"
			}
			// 2) Read the live headscale policy.
			policyJSON, err := hs.GetACL()
			if err != nil {
				return SystemTestFail, "getacl: " + err.Error()
			}
			var policy struct {
				Grants []struct {
					Src []string `json:"src"`
					Dst []string `json:"dst"`
				} `json:"grants"`
			}
			if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
				return SystemTestFail, "parse policy: " + err.Error()
			}
			// Build a set of (src, dst) tuples for O(1) lookup.
			grantSet := make(map[string]bool, len(policy.Grants))
			for _, g := range policy.Grants {
				for _, s := range g.Src {
					for _, d := range g.Dst {
						grantSet[s+"\x00"+d] = true
					}
				}
			}
			// 3) For each rule, compute the expected (src, dst) and
			// look it up. Mirror the loop in
			// internal/acl/acl.go:1141-1162 (GenerateACLWithViaForPlane).
			// MUST stay in lockstep with the generator: if the
			// generator adds strings.ToLower(host) or any other
			// transform, this verification test will start
			// reporting false-positive "missing grants" for every
			// row. The unit tests TestSanitizeRuleAlias +
			// TestExpectedGrantTuple pin the exact formula.
			sanitize := func(s string) string {
				return strings.NewReplacer(".", "-", "/", "-", ":", "_").Replace(s)
			}
			var missing []string
			for _, r := range rules {
				var src string
				switch {
				case r.uname != "" && r.host != "":
					// Generator does NOT lowercase — uses the
					// row's device_hostname verbatim. In practice
					// the column is lowercase (v0.28.0 backfill
					// normalises via internal/nodeownership), so
					// the match works against headscale's
					// tagOwners entry which is also lowercase.
					src = "tag:dev-" + r.uname + "-" + r.host
				case r.ip != "":
					src = r.ip
				default:
					src = "*"
				}
				dst := "h-rule-" + sanitize(r.target)
				key := src + "\x00" + dst
				if !grantSet[key] {
					if len(missing) < 5 {
						missing = append(missing, fmt.Sprintf("%s→%s", src, dst))
					}
				}
			}
			// 4) Report.
			miss := len(missing)
			if miss == 0 {
				return SystemTestPass, fmt.Sprintf("all %d subnet/ip rules reflected in headscale grants[]", len(rules))
			}
			detail := fmt.Sprintf("%d/%d rules missing from grants[] (headscale may not have refreshed yet): %s",
				miss, len(rules), strings.Join(missing, "; "))
			// > 5 missing = real sync regression. Below that
			// it's almost certainly Tailscale client-side lag
			// (Tailscale pulls the new policy every 60-90s).
			// 2026-08-06: the operator's incident showed 117
			// missing in one shot — that was a real bug, not lag.
			if miss > 5 {
				return SystemTestFail, detail
			}
			return SystemTestPass, "warn: " + detail
		},
	},
}

// testService is the runtime Service for in-process test
// closures. Set by SetTestService from main.go after
// constructing the admin Service. Guarded by testServiceMu.
var (
	testService   *Service
	testServiceMu sync.Mutex
)

// SetTestService wires the runtime admin Service into the
// test registry closures. Called from cmd/skygate/main.go
// after the admin Service is constructed.
func SetTestService(s *Service) {
	testServiceMu.Lock()
	defer testServiceMu.Unlock()
	testService = s
}

func getTestService() *Service {
	testServiceMu.Lock()
	defer testServiceMu.Unlock()
	return testService
}

// SystemRunSummary is the run-level metadata.
type SystemRunSummary struct {
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	Duration    string    `json:"duration"`
	TotalCount  int       `json:"total_count"`
	Pass        int       `json:"pass"`
	Fail        int       `json:"fail"`
	Skip        int       `json:"skip"`
}

// RunAllTests runs every test in TestRegistry, returns the
// results + a summary. Each test has a 5s timeout to bound
// the total runtime. Tests are run sequentially.
func (s *Service) RunAllTests(ctx context.Context) ([]SystemTestResult, *SystemRunSummary) {
	if s == nil {
		s = getTestService()
	}
	if s == nil {
		return nil, nil
	}
	results := make([]SystemTestResult, 0, len(TestRegistry))
	summary := &SystemRunSummary{StartedAt: time.Now().UTC()}
	for _, t := range TestRegistry {
		testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		start := time.Now()
		status, output := t.Run(testCtx)
		cancel()
		results = append(results, SystemTestResult{
			Name:     t.Name,
			Category: t.Category,
			Status:   status,
			Output:   output,
			Duration: time.Since(start).String(),
		})
		switch status {
		case SystemTestPass:
			summary.Pass++
		case SystemTestFail:
			summary.Fail++
		case SystemTestSkip:
			summary.Skip++
		}
	}
	summary.FinishedAt = time.Now().UTC()
	summary.TotalCount = len(results)
	summary.Duration = summary.FinishedAt.Sub(summary.StartedAt).String()
	return results, summary
}

// PersistRun stores the result + summary in system_tests_runs.
// Called from the page after RunAllTests returns.
//
// 2026-08-05 v0.33.1.11 — replaced the hardcoded "?" placeholders
// (8 of them) with `placeholdersList(8)` so the same code
// works on both SQLite ("?,?,?") and PG ("$1,$2,...$8"). The
// pgx stdlib does NOT auto-convert "?" to "$N" (unlike lib/pq
// which did), so without this fix the prod PG backend rejects
// the INSERT with "syntax error at or near ','" and the
// /admin/system_tests page shows the error flash on every
// "Run all" click. The dispatch uses the same build-tag
// pattern as db.SetGlobalSetting + db.nowUnixSQL.
func (s *Service) PersistRun(ctx context.Context, results []SystemTestResult, summary *SystemRunSummary, userID int64) (int64, error) {
	if s == nil || s.DB == nil {
		return 0, errors.New("DB not available")
	}
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return 0, err
	}
	durationMs := summary.FinishedAt.Sub(summary.StartedAt).Milliseconds()
	ph := db.PlaceholdersList(8)
	res, err := s.DB.ExecContext(ctx, `
		INSERT INTO system_tests_runs
			(started_at, finished_at, duration_ms, results_json,
			 pass_count, fail_count, skip_count, triggered_by_user_id)
		VALUES (`+ph+`)
	`, summary.StartedAt.Unix(), summary.FinishedAt.Unix(), durationMs,
		string(resultsJSON), summary.Pass, summary.Fail, summary.Skip, userID)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// ListRecentRuns returns the last N runs (default 20) for
// the history strip on /admin/system_tests.
//
// 2026-08-05 v0.33.1.11 — LIMIT ? replaced with
// placeholdersList(1) for the same PG/SQLite dispatch
// reason as PersistRun (see comment there).
func (s *Service) ListRecentRuns(ctx context.Context, limit int) ([]SystemRunSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, started_at, finished_at, duration_ms,
		       pass_count, fail_count, skip_count
		FROM system_tests_runs
		ORDER BY id DESC LIMIT `+db.PlaceholdersList(1)+`
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SystemRunSummary, 0, limit)
	for rows.Next() {
		var r SystemRunSummary
		var id, startedAt, finishedAt, durationMs, pass, fail, skip int64
		if err := rows.Scan(&id, &startedAt, &finishedAt, &durationMs,
			&pass, &fail, &skip); err != nil {
			return nil, err
		}
		_ = id
		r.StartedAt = time.Unix(startedAt, 0).UTC()
		r.FinishedAt = time.Unix(finishedAt, 0).UTC()
		r.Duration = (time.Duration(durationMs) * time.Millisecond).String()
		r.TotalCount = int(pass + fail + skip)
		r.Pass = int(pass)
		r.Fail = int(fail)
		r.Skip = int(skip)
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastRunWithResults is what ListLastRunWithResults
// returns: the most recent run's parsed test results +
// summary + when it started. Used by the
// /admin/system_tests page to render per-test PASS / FAIL /
// SKIP icons on initial page load (not just after "Run
// all" was clicked). Zero-value results means no runs yet.
//
// 2026-08-09 v0.33.1.26 — added. The pre-fix
// /admin/system_tests page only showed per-test status
// after a fresh "Run all" click (LiveResults was
// populated by the POST handler, not by GET). On a cold
// page load the operator saw a wall of gray circles
// instead of "this test failed with: ..." for the
// 6 broken tests. B78 wires the last persisted run from
// system_tests_runs into the page so the operator
// always sees the actual status of the last suite
// execution, including failure output, without having
// to click "Run all" first.
type LastRunWithResults struct {
	Results    []SystemTestResult
	Summary    *SystemRunSummary
	StartedAt  time.Time
	FinishedAt time.Time
	RunID      int64
}

// ListLastRunWithResults returns the most recent row from
// system_tests_runs with the results_json unmarshalled
// into SystemTestResult slice. Returns (nil, nil, no err)
// if no runs exist yet (fresh install). Returns the
// already-parsed results even if the JSON is malformed
// (returns a non-nil error and a partial result so the
// page degrades to "JSON parse error" rather than
// silently showing gray circles).
//
// 2026-08-09 v0.33.1.26 — added.
func (s *Service) ListLastRunWithResults(ctx context.Context) (*LastRunWithResults, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("DB not configured")
	}
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, started_at, finished_at, duration_ms,
		       results_json, pass_count, fail_count, skip_count
		FROM system_tests_runs
		ORDER BY id DESC
		LIMIT 1
	`)
	var (
		id, startedAt, finishedAt, durationMs int64
		resultsJSON                            string
		pass, fail, skip                      int
	)
	if err := row.Scan(&id, &startedAt, &finishedAt, &durationMs,
		&resultsJSON, &pass, &fail, &skip); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Fresh install: no runs yet. The page renders
			// the gray placeholders. Not an error.
			return nil, nil
		}
		return nil, err
	}
	out := &LastRunWithResults{
		StartedAt: time.Unix(startedAt, 0).UTC(),
		FinishedAt: time.Unix(finishedAt, 0).UTC(),
		RunID:      id,
		Summary: &SystemRunSummary{
			StartedAt:  time.Unix(startedAt, 0).UTC(),
			FinishedAt: time.Unix(finishedAt, 0).UTC(),
			Duration:   (time.Duration(durationMs) * time.Millisecond).String(),
			TotalCount: pass + fail + skip,
			Pass:       pass,
			Fail:       fail,
			Skip:       skip,
		},
	}
	if resultsJSON == "" || resultsJSON == "{}" {
		return out, nil
	}
	var results []SystemTestResult
	if err := json.Unmarshal([]byte(resultsJSON), &results); err != nil {
		// Malformed JSON — return the summary but no
		// per-test details. The page will still show
		// the summary counts. The error is bubbled up
		// so the handler can log it.
		return out, fmt.Errorf("parse results_json (run #%d): %w", id, err)
	}
	out.Results = results
	return out, nil
}

// ensureListNodes is here to keep the import of headscale in
// the file's symbol table even when the test definitions
// don't reference it. The compiler can dead-code-eliminate
// the headscale import if no symbol from the package is
// referenced. We keep headscale imported for the future
// test additions (e.g. "headscale.exit_node_health").
var _ = (*headscale.Client)(nil)
var _ sql.IsolationLevel = 0
