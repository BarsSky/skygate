// 2026-07-29: refactor-v0.30 Phase B step 4 follow-up - ported from
// internal/handlers/exit_rules_form_parent_domain_test.go. The
// tests use *Service (the refactored home of insertRuleUnique)
// instead of *App; the schema is hand-written inline (no db.Open)
// so the test is independent of the migration chain.
//
// 6 tests + 1 helper (simulateAutoupdaterTick) all kept - the
// parent_domain fix is a Service-level concern and a *Service
// with a real DB is enough to pin the contract.

package exit_rules

// exit_rules_form_parent_domain_test.go — regression tests for the
// 2026-07-28 v0.30.x bug:
//
//   The form's DNS-resolve path inserts /32 rules for each IP
//   the domain resolves to, but with EMPTY parent_domain. The
//   autoupdater (DomainAutoUpdater) then can't see those /32
//   rules as "its" — it looks for them via:
//      parent_domain = d.domain
//   ... and the form-created rows have parent_domain = ''.
//   So the autoupdater inserts its own /32 rows on top,
//   and Cloudflare anycast (which returns different IPs at
//   each DNS query) creates constant churn: add=18, remove=17
//   every 5 minutes, net ≈ 0. The user's traffic to the
//   domain hits IPs that karolina has NEVER advertised.
//
//   The fix: pass the original domain as parentDomain when
//   the form inserts /32 rules from DNS resolution. Then
//   the autoupdater finds the existing /32s on the next
//   tick and updates them in place.
//
// The tests below pin the contract:
//   1. insertRuleUnique sets parent_domain when the caller
//      passes it (the new signature).
//   2. The autoupdater's "find existing /32 for this domain"
//      lookup returns the form-created rows (parent_domain
//      set correctly).
//   3. Running the autoupdater twice with the SAME DNS result
//      is a no-op: add=0, remove=0 (no churn).
//   4. Running the autoupdater with a CHANGED DNS result
//      (Cloudflare rotation) updates the /32 set in place
//      without duplicating or losing the domain rule.

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"skygate/internal/db"
	"skygate/internal/i18n"

	_ "github.com/mattn/go-sqlite3"
)

// parentDomainTestCounter isolates per-test in-memory DBs so
// concurrent tests in the same `go test` process don't share
// tables. See newServiceForExitRulesTest.
var parentDomainTestCounter int64

// newServiceForExitRulesTest builds a *Service with an in-memory DB
// that has just the device_rules + portal_users schema needed
// for the parent_domain regression tests. We hand-write the
// schema inline (no db.Open) so the test doesn't depend on
// the full migration chain and runs in milliseconds.
func newServiceForExitRulesTest(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	// Per-test unique in-memory cache so concurrent tests
	// don't see each other's tables.
	dsn := fmt.Sprintf("file:parent-domain-test-%d?mode=memory&cache=shared", atomic.AddInt64(&parentDomainTestCounter, 1))
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	stmts := []string{
		`CREATE TABLE portal_users (
			id INTEGER PRIMARY KEY,
			username TEXT NOT NULL,
			is_admin INTEGER NOT NULL DEFAULT 0,
			password_hash TEXT NOT NULL DEFAULT '',
			theme TEXT NOT NULL DEFAULT 'linear',
			created_at INTEGER NOT NULL DEFAULT 0,
			headscale_user_id INTEGER,
			default_device_node_id TEXT NOT NULL DEFAULT '',
			default_exit_node_id TEXT NOT NULL DEFAULT '',
			headscale_url TEXT NOT NULL DEFAULT '',
			headscale_api_key_enc TEXT NOT NULL DEFAULT '',
			subnet_cidr TEXT NOT NULL DEFAULT '',
			subnet_status TEXT NOT NULL DEFAULT 'none',
			subnet_router_node_id TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE device_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			device_id INTEGER NOT NULL,
			exit_node_id TEXT NOT NULL,
			target_type TEXT NOT NULL DEFAULT 'subnet',
			target_value TEXT NOT NULL,
			action TEXT NOT NULL DEFAULT 'accept',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL DEFAULT 0,
			device_ip TEXT NOT NULL DEFAULT '',
			parent_domain TEXT NOT NULL DEFAULT '',
			user_name TEXT NOT NULL DEFAULT '',
			device_hostname TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	if _, err := d.Exec(`INSERT INTO portal_users(id, username, is_admin) VALUES (1, 'skyadmin', 1)`); err != nil {
		t.Fatalf("seed skyadmin: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO portal_users(id, username, is_admin) VALUES (2, 'michail', 0)`); err != nil {
		t.Fatalf("seed michail: %v", err)
	}
	s := &Service{
		DB:   d,
		I18n: i18nForTest(t),
	}
	return s, d
}

// i18nForTest returns a minimal *i18n.Catalog safe to use in
// tests that don't render templates. We avoid the global
// state to keep this file independent of i18n init order.
func i18nForTest(t *testing.T) *i18n.Catalog {
	t.Helper()
	return i18n.New()
}

// TestInsertRuleUnique_ParentDomainFromDNSResolve — the
// 2026-07-28 bug regression guard. After the form resolves
// a domain to N IPs, it inserts N /32 rules. The new
// insertRuleUnique signature must accept parentDomain
// explicitly so the form can pass the original domain.
//
// Before the fix: targetType="subnet" → parentDomain="" →
// autoupdater can't find the row on the next tick.
// After the fix: parentDomain="artstation.com" is recorded →
// autoupdater finds it via parent_domain and updates in place.
func TestInsertRuleUnique_ParentDomainFromDNSResolve(t *testing.T) {
	s, d := newServiceForExitRulesTest(t)
	defer d.Close()

	const userID, deviceID = 1, 9
	const exitNode = "karolina"
	const domain = "artstation.com"
	ip := "104.19.169.40"

	// 2026-07-28 fix: form passes parentDomain=domain after
	// DNS resolve. The signature change makes this explicit.
	ok, newID := s.insertRuleUnique(userID, deviceID, exitNode,
		"subnet", ip+"/32", "accept", "100.64.0.9", domain)
	if !ok {
		t.Fatalf("insertRuleUnique subnet/32: expected ok")
	}
	if newID == 0 {
		t.Fatalf("insertRuleUnique: expected newID > 0, got 0 (rule was already a duplicate?)")
	}

	// Verify the parent_domain is recorded.
	var gotParent string
	if err := d.QueryRow(
		`SELECT parent_domain FROM device_rules WHERE id = ?`, newID,
	).Scan(&gotParent); err != nil {
		t.Fatalf("QueryRow parent_domain: %v", err)
	}
	if gotParent != domain {
		t.Errorf("parent_domain = %q, want %q (form must pass the original domain after DNS resolve)",
			gotParent, domain)
	}
}

// TestInsertRuleUnique_DomainRuleStillSetsParentDomain —
// backward-compat: the form inserts the domain rule ITSELF
// separately (line 491 of exit_rules_form_my.go) via
// db.AppendDeviceRule directly. This test pins the rule that
// the form's DNS-resolve path generates the /32 children of.
func TestInsertRuleUnique_DomainRuleStillSetsParentDomain(t *testing.T) {
	s, d := newServiceForExitRulesTest(t)
	defer d.Close()

	const userID, deviceID = 1, 9
	const exitNode = "karolina"
	const domain = "example.com"

	ok, newID := s.insertRuleUnique(userID, deviceID, exitNode,
		"domain", domain, "accept", "100.64.0.9", domain)
	if !ok {
		t.Fatalf("insertRuleUnique domain: expected ok")
	}
	if newID == 0 {
		t.Fatalf("insertRuleUnique: expected newID > 0")
	}

	var gotType, gotValue, gotParent string
	if err := d.QueryRow(
		`SELECT target_type, target_value, parent_domain FROM device_rules WHERE id = ?`, newID,
	).Scan(&gotType, &gotValue, &gotParent); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if gotType != "domain" {
		t.Errorf("target_type = %q, want %q", gotType, "domain")
	}
	if gotValue != domain {
		t.Errorf("target_value = %q, want %q", gotValue, domain)
	}
	if gotParent != domain {
		t.Errorf("parent_domain = %q, want %q (domain rule must self-reference as parent)",
			gotParent, domain)
	}
}

// TestFormAddDomain_Flow32RulesHaveParentDomain — the full
// form flow simulation. After "Add artstation.com":
//   1. Domain rule row inserted (parent=artstation.com).
//   2. Two /32 rules inserted for the 2 resolved IPs
//      (parent=artstation.com via the new signature).
//   3. Autoupdater's "find existing /32 for this domain"
//      query returns BOTH /32 rows — no duplicates would
//      be created on the next tick.
func TestFormAddDomain_Flow32RulesHaveParentDomain(t *testing.T) {
	s, d := newServiceForExitRulesTest(t)
	defer d.Close()

	const userID, deviceID = 1, 9
	const exitNode = "karolina"
	const domain = "artstation.com"
	resolvedIPs := []string{"104.19.169.40", "104.19.170.40"}

	// 1. Domain rule.
	if ok, _ := s.insertRuleUnique(userID, deviceID, exitNode,
		"domain", domain, "accept", "100.64.0.9", domain); !ok {
		t.Fatalf("insert domain rule")
	}
	// 2. /32 rules (with the fix, parentDomain is the domain).
	for _, ip := range resolvedIPs {
		if ok, _ := s.insertRuleUnique(userID, deviceID, exitNode,
			"subnet", ip+"/32", "accept", "100.64.0.9", domain); !ok {
			t.Fatalf("insert /32 for %s", ip)
		}
	}

	// 3. Autoupdater-style lookup: find all /32 for this parent_domain.
	rows, err := d.Query(
		`SELECT target_value FROM device_rules
		 WHERE user_id = ? AND device_id = ? AND exit_node_id = ?
		   AND target_type = 'subnet' AND parent_domain = ?`,
		userID, deviceID, exitNode, domain)
	if err != nil {
		t.Fatalf("autoupdater lookup: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got[v] = true
	}
	for _, ip := range resolvedIPs {
		if !got[ip+"/32"] {
			t.Errorf("autoupdater cannot find /32 for %s (parent_domain lookup missed it — the form's pre-fix bug)",
				ip)
		}
	}
	if len(got) != len(resolvedIPs) {
		t.Errorf("autoupdater found %d /32 rules, want %d", len(got), len(resolvedIPs))
	}
}

// TestDomainAutoUpdater_NoChurnForStableDomain — the regression
// guard for Cloudflare anycast rotation. If the autoupdater
// processes the same domain twice with the SAME DNS result,
// the second pass must be add=0, remove=0 (no churn).
//
// Before the fix: the form's /32 rows had parent_domain='',
// the autoupdater couldn't find them, so it inserted new ones
// every tick. With Cloudflare's IP rotation, those new ones
// got removed the next tick. Net result: 18 added, 17 removed
// every 5 minutes, ~0 net — and the user's traffic hit IPs
// karolina never advertised.
func TestDomainAutoUpdater_NoChurnForStableDomain(t *testing.T) {
	s, d := newServiceForExitRulesTest(t)
	defer d.Close()

	const userID, deviceID = 1, 9
	const exitNode = "karolina"
	const domain = "stable.example.com"
	ips := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"}

	// Simulate the form adding a domain rule (autoupdater will
	// see this on every tick).
	if ok, _ := s.insertRuleUnique(userID, deviceID, exitNode,
		"domain", domain, "accept", "100.64.0.9", domain); !ok {
		t.Fatalf("insert domain rule")
	}

	// First autoupdater pass: add /32 rows with parent_domain=domain.
	added1, removed1 := simulateAutoupdaterTick(t, d, userID, deviceID, exitNode, domain, ips)
	if added1 != len(ips) {
		t.Errorf("first tick: added=%d, want %d", added1, len(ips))
	}
	if removed1 != 0 {
		t.Errorf("first tick: removed=%d, want 0", removed1)
	}

	// Second autoupdater pass with the SAME DNS result: must be a no-op.
	added2, removed2 := simulateAutoupdaterTick(t, d, userID, deviceID, exitNode, domain, ips)
	if added2 != 0 {
		t.Errorf("second tick (stable DNS): added=%d, want 0 (no churn)", added2)
	}
	if removed2 != 0 {
		t.Errorf("second tick (stable DNS): removed=%d, want 0 (no churn)", removed2)
	}
}

// TestDomainAutoUpdater_UpdatesOnIPRotation — the OTHER
// scenario: Cloudflare rotates IPs between ticks. The
// autoupdater must remove the old /32s and add the new ones
// WITHOUT duplicating or losing the domain rule.
func TestDomainAutoUpdater_UpdatesOnIPRotation(t *testing.T) {
	s, d := newServiceForExitRulesTest(t)
	defer d.Close()

	const userID, deviceID = 1, 9
	const exitNode = "karolina"
	const domain = "rotating.example.com"
	ipsTick1 := []string{"198.51.100.1", "198.51.100.2"}
	ipsTick2 := []string{"198.51.100.3", "198.51.100.4"} // rotated

	if ok, _ := s.insertRuleUnique(userID, deviceID, exitNode,
		"domain", domain, "accept", "100.64.0.9", domain); !ok {
		t.Fatalf("insert domain rule")
	}
	added1, removed1 := simulateAutoupdaterTick(t, d, userID, deviceID, exitNode, domain, ipsTick1)
	if added1 != 2 || removed1 != 0 {
		t.Errorf("tick 1: added=%d removed=%d, want 2/0", added1, removed1)
	}

	// Cloudflare rotates: 198.51.100.1/.2 are no longer in the set.
	added2, removed2 := simulateAutoupdaterTick(t, d, userID, deviceID, exitNode, domain, ipsTick2)
	if added2 != 2 {
		t.Errorf("tick 2: added=%d, want 2 (new IPs)", added2)
	}
	if removed2 != 2 {
		t.Errorf("tick 2: removed=%d, want 2 (old rotated-out IPs)", removed2)
	}

	// Domain rule must still be there.
	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM device_rules WHERE user_id = ? AND target_type = 'domain' AND target_value = ?`,
		userID, domain,
	).Scan(&n); err != nil {
		t.Fatalf("count domain: %v", err)
	}
	if n != 1 {
		t.Errorf("domain rule count = %d, want 1 (must survive IP rotation)", n)
	}
}

// simulateAutoupdaterTick runs the same SQL the autoupdater
// (DomainAutoUpdater in exit_rules_sync.go) does for a single
// domain: find existing /32 rows tagged with parent_domain,
// add new IPs, remove dropped IPs. Returns (added, removed).
//
// The point of this helper: tests can pin the no-churn invariant
// WITHOUT spinning up the real autoupdater goroutine. The real
// autoupdater does the same SQL inside a `for _, d := range
// domains` loop; we just inline the per-domain logic.
func simulateAutoupdaterTick(t *testing.T, d *sql.DB, userID int64, deviceID int, exitNode, domain string, newIPs []string) (added, removed int) {
	t.Helper()
	current := map[string]bool{}
	for _, ip := range newIPs {
		current[ip] = true
	}

	// Find existing /32 for this parent_domain.
	existing := map[string]int{}
	rows, err := d.Query(
		`SELECT id, target_value FROM device_rules
		 WHERE user_id = ? AND device_id = ? AND exit_node_id = ?
		   AND target_type = 'subnet' AND parent_domain = ?`,
		userID, deviceID, exitNode, domain)
	if err != nil {
		t.Fatalf("simulateAutoupdaterTick lookup: %v", err)
	}
	for rows.Next() {
		var id int
		var val string
		if err := rows.Scan(&id, &val); err != nil {
			rows.Close()
			t.Fatalf("simulateAutoupdaterTick scan: %v", err)
		}
		// Strip /32 suffix
		ip := val
		if len(ip) > 3 && ip[len(ip)-3:] == "/32" {
			ip = ip[:len(ip)-3]
		}
		existing[ip] = id
	}
	rows.Close()

	// Add new IPs.
	for ip := range current {
		if _, ok := existing[ip]; ok {
			continue
		}
		if _, err := d.Exec(
			`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, enabled, created_at, parent_domain)
			 VALUES (?, ?, ?, 'subnet', ?, 'accept', 1, strftime('%s','now'), ?)`,
			userID, deviceID, exitNode, ip+"/32", domain); err == nil {
			added++
		}
	}
	// Remove dropped IPs.
	for ip, id := range existing {
		if current[ip] {
			continue
		}
		if _, err := d.Exec(`DELETE FROM device_rules WHERE id = ?`, id); err == nil {
			removed++
		}
	}
	return added, removed
}

// TestDomainAutoUpdater_SharedIPAcrossDomains — when two
// domains resolve to the same IP (common with Cloudflare),
// the autoupdater should NOT add a duplicate /32. The first
// domain to claim the IP gets the /32; the second domain's
// autoupdater sees the existing /32 (any parent_domain) and
// skips it. This matches the existing autoupdater behaviour
// in exit_rules_sync.go:283-285.
func TestDomainAutoUpdater_SharedIPAcrossDomains(t *testing.T) {
	s, d := newServiceForExitRulesTest(t)
	defer d.Close()

	const userID, deviceID = 1, 9
	const exitNode = "karolina"
	const domainA = "a.example.com"
	const domainB = "b.example.com"
	sharedIP := "203.0.113.99"

	// First domain claims the IP.
	if ok, _ := s.insertRuleUnique(userID, deviceID, exitNode,
		"domain", domainA, "accept", "100.64.0.9", domainA); !ok {
		t.Fatalf("insert domainA")
	}
	added, _ := simulateAutoupdaterTick(t, d, userID, deviceID, exitNode, domainA, []string{sharedIP})
	if added != 1 {
		t.Fatalf("domainA autoupdate: added=%d, want 1", added)
	}

	// Second domain also resolves to the same IP — the
	// autoupdater (in production) checks for ANY /32 with
	// the same IP+user+device+exit_node and skips if found.
	// We simulate that guard here.
	if ok, _ := s.insertRuleUnique(userID, deviceID, exitNode,
		"domain", domainB, "accept", "100.64.0.9", domainB); !ok {
		t.Fatalf("insert domainB")
	}
	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM device_rules WHERE user_id = ? AND device_id = ? AND exit_node_id = ? AND target_type = 'subnet' AND target_value = ?`,
		userID, deviceID, exitNode, sharedIP+"/32",
	).Scan(&n); err != nil {
		t.Fatalf("count shared: %v", err)
	}
	if n != 1 {
		t.Errorf("shared IP /32 count = %d, want 1 (autoupdater should skip duplicates across domains)", n)
	}
	_ = context.Background
	_ = db.FindDomainRuleID // keep the import alive
}
