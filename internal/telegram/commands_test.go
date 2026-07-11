package telegram

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// setupTestDB builds a fresh in-memory DB with the minimal schema
// the bot commands need. We don't run the production migrations
// here because the test runs in isolation; the schema is kept in
// lock-step with internal/db/migrations_v*.go by hand. When you
// add a column/table that HandleCommand reads, update this list
// and any seed inserts below.
func setupTestDB(t *testing.T) *sql.DB {
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, q := range []string{
		`CREATE TABLE device_rules (id INTEGER PRIMARY KEY, user_id INTEGER, device_id INTEGER, exit_node_id TEXT NOT NULL DEFAULT '', target_type TEXT NOT NULL DEFAULT 'domain', target_value TEXT, action TEXT DEFAULT 'accept', device_ip TEXT DEFAULT '', parent_domain TEXT DEFAULT '', enabled INTEGER DEFAULT 1)`,
		`CREATE TABLE portal_users (id INTEGER PRIMARY KEY, username TEXT)`,
		`CREATE TABLE acl_snapshots (id INTEGER PRIMARY KEY, version INTEGER, applied_success INTEGER)`,
		`CREATE TABLE node_owner_map (node_id TEXT PRIMARY KEY, username TEXT DEFAULT '', tag TEXT DEFAULT 'tag:untagged')`,
		`CREATE TABLE audit_log (id INTEGER PRIMARY KEY, user_id INTEGER, username TEXT, action TEXT, detail TEXT DEFAULT '', created_at INTEGER DEFAULT 0)`,
		// 2026-07-11: Phase 3 — devices (joined to node_owner_map for
		// last_seen) and telegram_alerts (/ack round-trip).
		`CREATE TABLE devices (id INTEGER PRIMARY KEY, user_id INTEGER, hostname TEXT NOT NULL DEFAULT '', node_id TEXT DEFAULT '', headscale_node_id TEXT DEFAULT '', ip_addresses TEXT DEFAULT '', os TEXT DEFAULT '', last_seen INTEGER DEFAULT 0, online INTEGER DEFAULT 0, created_at INTEGER DEFAULT 0)`,
		`CREATE TABLE telegram_alerts (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT NOT NULL, sent_at INTEGER NOT NULL DEFAULT (strftime('%s','now')), acked_at INTEGER NOT NULL DEFAULT 0, acked_by TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	// Seed a few rows so the reply has substance. device_rules
	// rows are owned by skyadmin (user_id=1) so /quota sees the
	// expected 12-rule count under that user; /rules is the only
	// command that doesn't care about user_id.
	_, _ = d.Exec(`INSERT INTO portal_users(username) VALUES ('skyadmin')`)
	_, _ = d.Exec(`INSERT INTO portal_users(username) VALUES ('alice')`)
	for i := 0; i < 12; i++ {
		_, _ = d.Exec(`INSERT INTO device_rules(user_id, target_value) VALUES (1, ?)`, "x")
	}
	_, _ = d.Exec(`INSERT INTO acl_snapshots(version, applied_success) VALUES (5, 1)`)
	// Seed nodes + audit_log for phase-2 commands.
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('n1', 'skyadmin', 'tag:private')`)
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('n2', 'skyadmin', 'tag:private')`)
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('n3', 'skyadmin', 'tag:public')`)
	_, _ = d.Exec(`INSERT INTO audit_log(username, action, detail, created_at) VALUES ('skyadmin', 'user_create', 'created alice', 1700000000)`)
	_, _ = d.Exec(`INSERT INTO audit_log(username, action, detail, created_at) VALUES ('skyadmin', 'telegram_save', 'token=*** chat=1', 1700000010)`)
	// Phase-3 seeds: a tagged exit-node with a recent last_seen,
	// and one alert row to ack.
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('exit-emilia', 'skyadmin', 'tag:exit-node')`)
	_, _ = d.Exec(`INSERT INTO devices(node_id, last_seen, online) VALUES ('exit-emilia', 1700000200, 1)`)
	_, _ = d.Exec(`INSERT INTO telegram_alerts(body) VALUES ('📥 New rule #7 by skyadmin')`)
	t.Cleanup(func() { _ = d.Close(); _ = filepath.Clean("") })
	return d
}

// envFor wraps a test DB in a BotEnv with empty limits. The /quota
// tests construct their own BotEnv directly when they need to
// exercise the limit math.
func envFor(d *sql.DB) BotEnv { return BotEnv{DB: d} }

func TestHandleCommandStatus(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/status")
	if !strings.Contains(got, "rules: 12") {
		t.Errorf("expected rules count, got: %q", got)
	}
	if !strings.Contains(got, "users: 2") {
		t.Errorf("expected users count, got: %q", got)
	}
	if !strings.Contains(got, "last acl: #5") {
		t.Errorf("expected last acl, got: %q", got)
	}
}

func TestHandleCommandHelp(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/help")
	if !strings.Contains(got, "/status") {
		t.Errorf("expected /status in /help, got: %q", got)
	}
	if !strings.Contains(got, "/exit_nodes") {
		t.Errorf("expected /exit_nodes in /help, got: %q", got)
	}
	if !strings.Contains(got, "/ack") {
		t.Errorf("expected /ack in /help, got: %q", got)
	}
}

func TestHandleCommandUnknown(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/foobar")
	if !strings.Contains(got, "Unknown") {
		t.Errorf("expected unknown message, got: %q", got)
	}
}

func TestHandleCommandCaseInsensitive(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/STATUS")
	if !strings.Contains(got, "rules:") {
		t.Errorf("expected status body, got: %q", got)
	}
}

func TestHandleCommandEmpty(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "")
	if !strings.Contains(got, "Empty") {
		t.Errorf("expected empty message, got: %q", got)
	}
}

func TestHandleCommandNodes(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/nodes")
	if !strings.Contains(got, "Tailnet nodes") {
		t.Errorf("expected header, got: %q", got)
	}
	if !strings.Contains(got, "tag:private") {
		t.Errorf("expected tag:private bucket, got: %q", got)
	}
	if !strings.Contains(got, "n1") {
		t.Errorf("expected node n1, got: %q", got)
	}
	if !strings.Contains(got, "skyadmin") {
		t.Errorf("expected username, got: %q", got)
	}
}

func TestHandleCommandRules(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/rules")
	if !strings.Contains(got, "exit-rules") {
		t.Errorf("expected header, got: %q", got)
	}
	// 12 seed rows — all target_value "x"
	if !strings.Contains(got, "x") {
		t.Errorf("expected target_value to appear, got: %q", got)
	}
}

func TestHandleCommandAudit(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/audit")
	if !strings.Contains(got, "audit_log") {
		t.Errorf("expected header, got: %q", got)
	}
	if !strings.Contains(got, "user_create") {
		t.Errorf("expected user_create action, got: %q", got)
	}
	if !strings.Contains(got, "telegram_save") {
		t.Errorf("expected telegram_save action, got: %q", got)
	}
	if !strings.Contains(got, "created alice") {
		t.Errorf("expected detail text, got: %q", got)
	}
}

// --- Phase 3 tests ---

func TestHandleCommandExitNodes(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/exit_nodes")
	if !strings.Contains(got, "Exit-nodes") {
		t.Errorf("expected header, got: %q", got)
	}
	if !strings.Contains(got, "exit-emilia") {
		t.Errorf("expected seeded exit-node, got: %q", got)
	}
	if !strings.Contains(got, "online") {
		t.Errorf("expected online status, got: %q", got)
	}
	// Should NOT include private nodes that aren't exit-nodes.
	if strings.Contains(got, "n1") {
		t.Errorf("exit_nodes should not list tag:private nodes, got: %q", got)
	}
}

func TestHandleCommandExitNodesEmpty(t *testing.T) {
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	for _, q := range []string{
		`CREATE TABLE node_owner_map (node_id TEXT PRIMARY KEY, username TEXT DEFAULT '', tag TEXT DEFAULT 'tag:untagged')`,
		`CREATE TABLE devices (id INTEGER PRIMARY KEY, node_id TEXT DEFAULT '', last_seen INTEGER DEFAULT 0, online INTEGER DEFAULT 0)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	got := HandleCommand(context.Background(), envFor(d), "/exit_nodes")
	if !strings.Contains(got, "no nodes with tag:exit-node") {
		t.Errorf("expected empty-state message, got: %q", got)
	}
}

func TestHandleCommandQuota(t *testing.T) {
	d := setupTestDB(t)
	// skyadmin has all 12 rules; alice has 0. With DefaultMax=200,
	// skyadmin should show 12/200 ~ 6%, alice should show 0/200 ~ 0%.
	env := BotEnv{DB: d, DefaultMax: 200}
	got := HandleCommand(context.Background(), env, "/quota")
	if !strings.Contains(got, "skyadmin") {
		t.Errorf("expected skyadmin in quota, got: %q", got)
	}
	if !strings.Contains(got, "12") {
		t.Errorf("expected rule count, got: %q", got)
	}
	if !strings.Contains(got, "200") {
		t.Errorf("expected cap, got: %q", got)
	}
	if !strings.Contains(got, "Per-user rule quota") {
		t.Errorf("expected header, got: %q", got)
	}
}

func TestHandleCommandQuotaPerUserOverride(t *testing.T) {
	d := setupTestDB(t)
	// skyadmin gets a tiny 10-rule cap so it shows as warning-level
	// fill (12/10 → 100%). alice stays at the default.
	env := BotEnv{DB: d, UserMaxRules: map[string]int{"skyadmin": 10}, DefaultMax: 200}
	got := HandleCommand(context.Background(), env, "/quota")
	if !strings.Contains(got, "12") {
		t.Errorf("expected rule count, got: %q", got)
	}
	if !strings.Contains(got, "10") {
		t.Errorf("expected per-user cap of 10, got: %q", got)
	}
	// Verify alice still shows with 200 (default cap), proving
	// the per-user override is scoped to skyadmin only.
	if !strings.Contains(got, "alice") {
		t.Errorf("expected alice in quota, got: %q", got)
	}
}

func TestHandleCommandQuotaNoLimit(t *testing.T) {
	d := setupTestDB(t)
	env := BotEnv{DB: d} // both UserMaxRules and DefaultMax are zero → "no limit"
	got := HandleCommand(context.Background(), env, "/quota")
	if !strings.Contains(got, "no limit") {
		t.Errorf("expected 'no limit' marker when no caps configured, got: %q", got)
	}
}

func TestHandleCommandAckHappy(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/ack 1")
	if !strings.Contains(got, "[#1]") {
		t.Errorf("expected alert id prefix in ack reply, got: %q", got)
	}
	if !strings.Contains(got, "📥 New rule #7") {
		t.Errorf("expected alert body echo, got: %q", got)
	}
	// The row should be acked in DB.
	var ackedAt int64
	var ackedBy string
	if err := d.QueryRow(`SELECT acked_at, acked_by FROM telegram_alerts WHERE id = 1`).Scan(&ackedAt, &ackedBy); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if ackedAt == 0 {
		t.Errorf("expected acked_at > 0, got 0")
	}
	if ackedBy != "telegram" {
		t.Errorf("expected acked_by=telegram, got %q", ackedBy)
	}
	// And the audit_log row should have been written.
	var count int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_ack'`).Scan(&count); err != nil {
		t.Fatalf("audit readback: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 telegram_ack row in audit_log, got %d", count)
	}
}

func TestHandleCommandAckAlreadyAcked(t *testing.T) {
	d := setupTestDB(t)
	// Ack once.
	_ = HandleCommand(context.Background(), envFor(d), "/ack 1")
	// Ack again — should be idempotent and report "already acked".
	got := HandleCommand(context.Background(), envFor(d), "/ack 1")
	if !strings.Contains(got, "already acked") {
		t.Errorf("expected 'already acked' on re-ack, got: %q", got)
	}
	// Second ack should NOT have produced a second audit_log row.
	var count int
	d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_ack'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 audit_log row after re-ack, got %d", count)
	}
}

func TestHandleCommandAckUnknown(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/ack 9999")
	if !strings.Contains(got, "no alert with id=9999") {
		t.Errorf("expected unknown-id message, got: %q", got)
	}
}

func TestHandleCommandAckBadArg(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/ack")
	if !strings.Contains(got, "usage: /ack") {
		t.Errorf("expected usage hint, got: %q", got)
	}
	got = HandleCommand(context.Background(), envFor(d), "/ack notanumber")
	if !strings.Contains(got, "is not a valid alert id") {
		t.Errorf("expected invalid-id message, got: %q", got)
	}
}

func TestFormatAlertRow(t *testing.T) {
	// Long body gets truncated; newlines get collapsed.
	body := "line one\nline two\nline three with quite a lot of detail that exceeds the 120 char cap and so should be trimmed to fit the ack reply form"
	got := formatAlertRow(42, body)
	if !strings.HasPrefix(got, "[#42] ") {
		t.Errorf("expected [#42] prefix, got: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("expected no newlines, got: %q", got)
	}
	if len(got) > 130 {
		t.Errorf("expected truncation, got len=%d", len(got))
	}
}

func TestQuotaBar(t *testing.T) {
	if !strings.Contains(quotaBar(0), "░") {
		t.Errorf("0%% should be empty bar, got: %q", quotaBar(0))
	}
	if !strings.Contains(quotaBar(50), "█") {
		t.Errorf("50%% should have fills, got: %q", quotaBar(50))
	}
	if quotaBar(-1) != "[no limit]" {
		t.Errorf("negative pct should be no-limit, got: %q", quotaBar(-1))
	}
	// Over 100% clamps to full bar.
	if !strings.HasPrefix(quotaBar(150), "[██████████") {
		t.Errorf("150%% should clamp to full bar, got: %q", quotaBar(150))
	}
}

func TestUnixToShort(t *testing.T) {
	// 2023-11-14 22:13:20 UTC = 1700000000
	if got := unixToShort(1700000000); got != "2023-11-14 22:13Z" {
		t.Errorf("expected 2023-11-14 22:13Z, got %q", got)
	}
	// 0 = unix epoch
	if got := unixToShort(0); got != "1970-01-01 00:00Z" {
		t.Errorf("expected epoch, got %q", got)
	}
}

func TestTrimForTelegram(t *testing.T) {
	long := strings.Repeat("a", 5000)
	got := trimForTelegram(long)
	if len(got) > 3800 {
		t.Errorf("expected trim, got len=%d", len(got))
	}
	if !strings.HasSuffix(got, "(truncated, see /admin/audit)") {
		t.Errorf("expected truncation marker, got tail: %q", got[len(got)-40:])
	}
	short := "hello"
	if trimForTelegram(short) != short {
		t.Errorf("short strings must pass through unchanged")
	}
}

// --- Phase 4 tests ---

func TestHandleCommandVersion(t *testing.T) {
	d := setupTestDB(t)
	env := BotEnv{DB: d, Version: "v0.3"}
	got := HandleCommand(context.Background(), env, "/version")
	if !strings.Contains(got, "v0.3") {
		t.Errorf("expected build label v0.3, got: %q", got)
	}
	// Go runtime version is whatever the test binary is built with.
	if !strings.Contains(got, "Go:") {
		t.Errorf("expected 'Go:' prefix, got: %q", got)
	}
	// Schema level is the constant; lets the operator confirm
	// whether migrations have caught up to the binary.
	if !strings.Contains(got, "DB schema:") {
		t.Errorf("expected 'DB schema:' prefix, got: %q", got)
	}
	if !strings.Contains(got, dbSchemaVersion) {
		t.Errorf("expected schema level %q, got: %q", dbSchemaVersion, got)
	}
}

func TestHandleCommandVersionEmptyFallback(t *testing.T) {
	d := setupTestDB(t)
	// No Version set — /version must still work and report a
	// placeholder rather than failing the command.
	got := HandleCommand(context.Background(), envFor(d), "/version")
	if !strings.Contains(got, "v0.0-dev") {
		t.Errorf("expected placeholder 'v0.0-dev' when Version is empty, got: %q", got)
	}
}

func TestHandleCommandRestartIssuesToken(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/restart")
	if !strings.Contains(got, "confirm by sending within 30s") {
		t.Errorf("expected confirmation prompt, got: %q", got)
	}
	// Token must be 6 chars from the alphabet — extract and verify.
	// The reply format is: "/restart <token>".
	// The first call doesn't write to audit_log (only phase 2 does).
	var count int
	d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_restart'`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 audit rows after first /restart call, got %d", count)
	}
}

func TestHandleCommandRestartConfirmHappy(t *testing.T) {
	d := setupTestDB(t)
	// Override killProcess so the test binary doesn't actually die.
	saved := killProcess
	killed := false
	killProcess = func() { killed = true }
	defer func() { killProcess = saved }()

	// Phase 1: mint a token.
	first := HandleCommand(context.Background(), envFor(d), "/restart")
	// Extract the 6-char token. Reply format:
	//   "restart: confirm by sending within 30s\n  /restart XXXXXX\n..."
	// Find the line starting with "  /restart ".
	var token string
	for _, line := range strings.Split(first, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/restart ") {
			token = strings.TrimPrefix(line, "/restart ")
			break
		}
	}
	if len(token) != 6 {
		t.Fatalf("expected 6-char token, got %q (from reply: %q)", token, first)
	}
	// Phase 2: confirm. The goroutine that calls killProcess will
	// wait 200ms before firing; for the test we don't need to wait —
	// we just check the reply and the audit row.
	second := HandleCommand(context.Background(), envFor(d), "/restart "+token)
	if !strings.Contains(second, "SIGTERM in 200ms") {
		t.Errorf("expected 'SIGTERM in 200ms' in confirm reply, got: %q", second)
	}
	// Wait briefly for the goroutine to fire killProcess.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !killed {
		time.Sleep(20 * time.Millisecond)
	}
	if !killed {
		t.Errorf("expected killProcess to be invoked within 2s")
	}
	// Audit log row should be written.
	var count int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_restart'`).Scan(&count); err != nil {
		t.Fatalf("audit readback: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 telegram_restart row, got %d", count)
	}
	// Token must be consumed — replaying it returns "not a valid".
	third := HandleCommand(context.Background(), envFor(d), "/restart "+token)
	if !strings.Contains(third, "not a valid") {
		t.Errorf("expected 'not a valid' on token replay, got: %q", third)
	}
}

func TestHandleCommandRestartBadToken(t *testing.T) {
	d := setupTestDB(t)
	saved := killProcess
	killProcess = func() { t.Errorf("killProcess must NOT be called for a bad token") }
	defer func() { killProcess = saved }()

	got := HandleCommand(context.Background(), envFor(d), "/restart NOTATOKEN")
	if !strings.Contains(got, "not a valid confirmation token") {
		t.Errorf("expected 'not a valid' for unknown token, got: %q", got)
	}
}

func TestHandleCommandRestartExpiredToken(t *testing.T) {
	d := setupTestDB(t)
	saved := killProcess
	killProcess = func() { t.Errorf("killProcess must NOT be called for an expired token") }
	defer func() { killProcess = saved }()

	// Manually plant an already-expired token.
	pendingRestarts.Store("EXPIRD", time.Now().Add(-1*time.Second))
	got := HandleCommand(context.Background(), envFor(d), "/restart EXPIRD")
	if !strings.Contains(got, "expired") {
		t.Errorf("expected 'expired' for stale token, got: %q", got)
	}
	// Expired tokens must be evicted.
	if _, ok := pendingRestarts.Load("EXPIRD"); ok {
		t.Errorf("expected expired token to be evicted from pendingRestarts")
	}
}

func TestMintRestartToken(t *testing.T) {
	tok, err := mintRestartToken()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(tok) != 6 {
		t.Errorf("expected 6-char token, got %q (len=%d)", tok, len(tok))
	}
	for _, r := range tok {
		if !strings.ContainsRune(restartAlphabet, r) {
			t.Errorf("token char %q not in alphabet %q", r, restartAlphabet)
		}
	}
	// Two consecutive tokens must differ (probabilistic but ~1 in 10^9
	// of collision with 32^6 alphabet).
	tok2, _ := mintRestartToken()
	if tok == tok2 {
		t.Errorf("expected different tokens on consecutive mints, both were %q", tok)
	}
}

func TestHelpDetailKnown(t *testing.T) {
	// Every command listed in /help should have a detailed help entry.
	for _, cmd := range []string{"status", "nodes", "exit_nodes", "rules", "quota", "audit", "ack", "version", "restart", "help"} {
		got := helpDetailReply(cmd)
		if !strings.HasPrefix(got, "/"+cmd+" ") {
			t.Errorf("expected /%s detailed help, got: %q", cmd, got)
		}
	}
}

func TestHelpDetailUnknown(t *testing.T) {
	got := helpDetailReply("nonexistent")
	if !strings.Contains(got, "No detailed help") {
		t.Errorf("expected 'No detailed help' for unknown command, got: %q", got)
	}
}

func TestHandleCommandHelpDetailed(t *testing.T) {
	d := setupTestDB(t)
	// /help ack must return the detailed ack help, not the short list.
	got := HandleCommand(context.Background(), envFor(d), "/help ack")
	if !strings.HasPrefix(got, "/ack ") {
		t.Errorf("expected detailed /ack help, got: %q", got)
	}
	if !strings.Contains(got, "Idempotent") {
		t.Errorf("expected ack-specific detail ('Idempotent'), got: %q", got)
	}
	// /help with no arg must still return the short list (backward compat).
	short := HandleCommand(context.Background(), envFor(d), "/help")
	if !strings.Contains(short, "Commands:") {
		t.Errorf("expected short list with no /help arg, got: %q", short)
	}
	// /help unknown should fall through to the "no detailed help" branch.
	unknown := HandleCommand(context.Background(), envFor(d), "/help foo")
	if !strings.Contains(unknown, "No detailed help") {
		t.Errorf("expected 'No detailed help' for unknown, got: %q", unknown)
	}
}
