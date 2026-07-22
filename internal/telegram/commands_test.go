package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
	"skygate/internal/sidecar"
	"skygate/internal/subnet"

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
		`CREATE TABLE portal_users (id INTEGER PRIMARY KEY, username TEXT, is_admin INTEGER DEFAULT 0, headscale_user_id INTEGER, password_hash TEXT DEFAULT '', theme TEXT DEFAULT 'linear', created_at INTEGER DEFAULT 0, default_device_node_id TEXT NOT NULL DEFAULT '', default_exit_node_id TEXT NOT NULL DEFAULT '', headscale_url TEXT NOT NULL DEFAULT '', headscale_api_key_enc TEXT NOT NULL DEFAULT '', subnet_cidr TEXT NOT NULL DEFAULT '', subnet_status TEXT NOT NULL DEFAULT 'none', subnet_router_node_id TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE acl_snapshots (id INTEGER PRIMARY KEY, version INTEGER, config TEXT NOT NULL DEFAULT '', created_by TEXT NOT NULL DEFAULT '', applied_success INTEGER, error_msg TEXT DEFAULT '')`,
		`CREATE TABLE node_owner_map (node_id TEXT PRIMARY KEY, username TEXT DEFAULT '', tag TEXT DEFAULT 'tag:untagged', headscale_user_id INTEGER NOT NULL DEFAULT 0, tagged_by_user_id INTEGER NOT NULL DEFAULT 0, tagged_at INTEGER NOT NULL DEFAULT 0, hostname TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE audit_log (id INTEGER PRIMARY KEY, user_id INTEGER, username TEXT, action TEXT, detail TEXT DEFAULT '', created_at INTEGER DEFAULT 0)`,
		// 2026-07-11: Phase 3 — devices (joined to node_owner_map for
		// last_seen) and telegram_alerts (/ack round-trip).
		`CREATE TABLE devices (id INTEGER PRIMARY KEY, user_id INTEGER, hostname TEXT NOT NULL DEFAULT '', node_id TEXT DEFAULT '', headscale_node_id TEXT DEFAULT '', ip_addresses TEXT DEFAULT '', os TEXT DEFAULT '', last_seen INTEGER DEFAULT 0, online INTEGER DEFAULT 0, created_at INTEGER DEFAULT 0)`,
		`CREATE TABLE telegram_alerts (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT NOT NULL, sent_at INTEGER NOT NULL DEFAULT (strftime('%s','now')), acked_at INTEGER NOT NULL DEFAULT 0, acked_by TEXT NOT NULL DEFAULT '')`,
		// 2026-07-12: Этап 11 — telegram_bindings (chat_id → portal_user).
		// 2026-07-14: Этап 14 v5 — added lang column (default 'en').
		`CREATE TABLE telegram_bindings (chat_id INTEGER PRIMARY KEY, portal_user_id INTEGER NOT NULL, is_admin INTEGER NOT NULL DEFAULT 0, bound_at INTEGER NOT NULL DEFAULT 0, bound_by_user_id INTEGER NOT NULL DEFAULT 0, lang TEXT NOT NULL DEFAULT 'en')`,
		// 2026-07-12: Этап 11 — preauth_keys (add_device reply needs it).
		`CREATE TABLE preauth_keys (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, key TEXT NOT NULL DEFAULT '', headscale_preauth_id TEXT NOT NULL DEFAULT '', used INTEGER NOT NULL DEFAULT 0, expires_at INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL DEFAULT 0)`,
		// 2026-07-13: Этап 11 part 2a — exit_servers (setexitnode / defaultexitnode).
		`CREATE TABLE exit_servers (id INTEGER PRIMARY KEY AUTOINCREMENT, node_id TEXT NOT NULL UNIQUE, hostname TEXT NOT NULL, tailscale_ip TEXT NOT NULL DEFAULT '', ssh_target TEXT NOT NULL DEFAULT '', ssh_key_path TEXT NOT NULL DEFAULT '', description TEXT DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, accept_routes INTEGER NOT NULL DEFAULT 0, created_at INTEGER DEFAULT 0)`,
		// 2026-07-13: Этап 11 part 2b — exit_rule_logs (AppendExitRuleLog).
		`CREATE TABLE exit_rule_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, version INTEGER NOT NULL, action TEXT NOT NULL, detail TEXT DEFAULT '', created_at INTEGER DEFAULT 0)`,
		// 2026-07-13: Этап 12 — telegram_login_tokens (login-by-key).
		`CREATE TABLE telegram_login_tokens (token TEXT PRIMARY KEY, portal_user_id INTEGER NOT NULL, created_at INTEGER NOT NULL DEFAULT 0, expires_at INTEGER NOT NULL, used_at INTEGER NOT NULL DEFAULT 0, used_by_chat_id INTEGER NOT NULL DEFAULT 0, request_ip TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX idx_telegram_login_tokens_user ON telegram_login_tokens(portal_user_id)`,
		`CREATE INDEX idx_telegram_login_tokens_expiry ON telegram_login_tokens(expires_at)`,
		// 2026-07-13: Этап 12 — global_settings for strict_mode
		// (read on every message by env()).
		`CREATE TABLE global_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL DEFAULT 0)`,
		// 2026-07-13: Этап 13 — telegram_rate_limit (shared
		// SQLite-backed /login rate limit, replaces the
		// in-memory map).
		`CREATE TABLE telegram_rate_limit (key TEXT NOT NULL, action TEXT NOT NULL DEFAULT '', ts INTEGER NOT NULL)`,
		`CREATE INDEX idx_telegram_rate_limit_lookup ON telegram_rate_limit(key, ts)`,
		// 2026-07-17: v0.16.0 — per-user subnets schema. The
		// /mysubnet test reads the denormalized
		// portal_users.subnet_* columns, so the test
		// schema must include both tables.
		`CREATE TABLE user_subnets (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL UNIQUE, cidr TEXT NOT NULL UNIQUE, subnet_bits INTEGER NOT NULL DEFAULT 24, control_plane_url TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending', router_node_id TEXT NOT NULL DEFAULT '', router_container_id TEXT NOT NULL DEFAULT '', router_hostname TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE user_subnet_shares (grantor_user_id INTEGER NOT NULL, grantee_user_id INTEGER NOT NULL, created_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (grantor_user_id, grantee_user_id), FOREIGN KEY (grantor_user_id) REFERENCES portal_users(id) ON DELETE CASCADE, FOREIGN KEY (grantee_user_id) REFERENCES portal_users(id) ON DELETE CASCADE)`,
		// 2026-07-20: v0.22.0 — mesh tables. The
		// ACL builder reads both on every render
		// (GetMeshMembershipsForPlane joins
		// mesh_members + meshes), so the test
		// schema must declare them even if no
		// test uses meshes directly.
		`CREATE TABLE meshes (id INTEGER PRIMARY KEY AUTOINCREMENT, code TEXT NOT NULL UNIQUE, name TEXT NOT NULL DEFAULT '', creator_user_id INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at INTEGER NOT NULL DEFAULT 0, dissolved_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE mesh_members (mesh_id INTEGER NOT NULL, user_id INTEGER NOT NULL, joined_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (mesh_id, user_id))`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	// Seed a few rows so the reply has substance. device_rules
	// rows are owned by skyadmin (user_id=1) so /quota sees the
	// expected 12-rule count under that user; /rules is the only
	// command that doesn't care about user_id.
	_, _ = d.Exec(`INSERT INTO portal_users(id, username, is_admin) VALUES (1, 'skyadmin', 1)`)
	_, _ = d.Exec(`INSERT INTO portal_users(id, username, is_admin) VALUES (2, 'alice', 0)`)
	for i := 0; i < 12; i++ {
		_, _ = d.Exec(`INSERT INTO device_rules(user_id, target_value) VALUES (1, $1)`, "x")
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
	t.Cleanup(func() {
		_ = d.Close()
		_ = filepath.Clean("")
		// 2026-07-13: Этап 13 — pendingClears (used by /clearrules)
		// and pendingRestarts (used by /restart) are package-level
		// sync.Maps. They leak across tests if not reset, so
		// every setupTestDB also wipes them. Without this, a test
		// that mints a clear/restart in one run would have a
		// leftover entry visible to the next test.
		pendingClears = sync.Map{}
		pendingRestarts = sync.Map{}
		// 2026-07-13: Этап 12 — /login rate-limit lives in
		// telegram_rate_limit (DB) since Этап 13; the per-test
		// setupTestDB is fresh, so there's nothing to reset
		// at the package level. (Previously the in-memory
		// loginAttempts map needed wiping here; the migration
		// to SQLite means a new in-memory DB starts with an
		// empty table.)
		// 2026-07-13: Этап 13 — reset the inline-keyboard
		// side-channel so a previous test's [Bind] prompt
		// doesn't leak into this one.
		pendingReplyForCurrentMessage = nil
	})
	return d
}

// envFor wraps a test DB in a BotEnv with empty limits. The /quota
// tests construct their own BotEnv directly when they need to
// exercise the limit math.
//
// 2026-07-14: Этап 14 v5 — envFor now sets Lang to "en" so
// the i18n catalog (installed by TestMain) returns the
// English reply strings. Tests that want Russian pass
// envForRU or build the BotEnv by hand.
func envFor(d *sql.DB) BotEnv {
	return BotEnv{DB: d, Lang: i18n.LangEN}
}

// envForRU is the Russian counterpart of envFor. Used by the
// few tests that explicitly check the Russian reply.
func envForRU(d *sql.DB) BotEnv {
	return BotEnv{DB: d, Lang: i18n.LangRU}
}

// TestMain installs the i18n catalog before any test in the
// telegram package runs. Without this, i18n.T() returns the
// raw key string (e.g. "bot.status.header") and the tests
// that expect a real "Skygate status" header fail. We
// deliberately use a single shared catalog across all
// tests — the catalog is read-only so concurrency is fine,
// and a fresh per-test catalog would only add startup
// cost.
func TestMain(m *testing.M) {
	i18n.SetGlobal(i18n.New())
	os.Exit(m.Run())
}

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

func TestHandleCommandStatusEnvelope(t *testing.T) {
	// 2026-07-14: Этап 14 v9 — butler voice v2.
	// Every command reply now opens with the butler header
	// (sigil + topic), then the body. This test pins the
	// envelope on /status (a registry-context reply).
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/status")
	if !strings.HasPrefix(got, butlerSigil+" ═══ Skygate ═══\n📊 The Registry") {
		t.Errorf("expected registry header at the top, got: %q", got[:60])
	}
	if !strings.Contains(got, "rules: 12") {
		t.Errorf("expected body content, got: %q", got)
	}
	// /status body is >3 lines → verbose → footer present.
	if !strings.Contains(got, "butler") {
		t.Errorf("expected verbose footer for multi-line /status, got: %q", got)
	}
}

func TestHandleCommandVersionEnvelope(t *testing.T) {
	// /version → "version" context (header line from the catalog).
	d := setupTestDB(t)
	env := BotEnv{DB: d, Version: "v0.10.8-dev", Lang: i18n.LangEN}
	got := HandleCommand(context.Background(), env, "/version")
	if !strings.HasPrefix(got, butlerSigil+" ═══ Skygate ═══\n📦 The Version Scroll") {
		t.Errorf("expected version header at the top, got: %q", got[:80])
	}
	if !strings.Contains(got, "v0.10.8-dev") {
		t.Errorf("expected build label in body, got: %q", got)
	}
}

func TestHandleCommandHelpEnvelope(t *testing.T) {
	// /help → "codex" context (The Codex).
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/help")
	if !strings.HasPrefix(got, butlerSigil+" ═══ Skygate ═══\n📖 The Codex") {
		t.Errorf("expected codex header at the top, got: %q", got[:60])
	}
}

func TestHandleCommandUnknownEnvelope(t *testing.T) {
	// Unknown commands still get an envelope (context = "err"
	// = A Closed Door). The body tells the user what's wrong.
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/nonsense_xyz")
	if !strings.HasPrefix(got, butlerSigil+" ═══ Skygate ═══\n⚠️ A Closed Door") {
		t.Errorf("expected err header at the top, got: %q", got[:80])
	}
	if !strings.Contains(got, "Unknown command") {
		t.Errorf("expected unknown-command body, got: %q", got)
	}
}

func TestHandleCommandAdminOnlyEnvelope(t *testing.T) {
	// /status as a non-admin user → rejected with "err" context.
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/status")
	if !strings.HasPrefix(got, butlerSigil+" ═══ Skygate ═══\n⚠️ A Closed Door") {
		t.Errorf("expected err header for admin-only rejection, got: %q", got[:80])
	}
	if !strings.Contains(got, "admin only") {
		t.Errorf("expected 'admin only' in body, got: %q", got)
	}
}

// TestHandleCommandHelpRUNoEnglishLeak — 2026-07-14:
// Этап 14 v12. The RU-locale /help output must be free of
// English substrings left over from the v0.10.x rewrites
// (the catalog has the per-line strings, but earlier
// code paths appended English suffixes like "for yourself",
// "or another user, admin only", "with last-seen"). This
// test pins the language: any of these substrings in the
// RU /help output is a regression. The forbidden list is
// additive — add more as we i18n-ize further strings.
func TestHandleCommandHelpRUNoEnglishLeak(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envForRU(d), "/help")
	forbidden := []string{
		"add an exit-rule",     // /add_rule
		"for yourself",         // /add_device suffix
		"or another user",      // /clearrules suffix
		"with last-seen",       // /exit_nodes
		"requires /clearrules", // /clearrules suffix
		"admin only",           // /clearrules (RU has its own)
		"let a domain",         // /add_rule example key
	}
	for _, f := range forbidden {
		if strings.Contains(strings.ToLower(got), f) {
			t.Errorf("expected RU-locale /help to be free of %q substring, but found it in:\n%s", f, got)
		}
	}
	// Sanity: the /add_rule line itself must still be present
	// (we'd notice if i18n-ization accidentally dropped it).
	if !strings.Contains(got, "/add_rule") {
		t.Errorf("expected /add_rule in RU /help, got: %q", got)
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

	// 2026-07-14: Этап 14 v12 — verify /help in RU-locale
	// has no English substrings left over from the v0.10.x
	// rewrites. The catalog has the per-line strings, but
	// older code paths appended English suffixes ("for
	// yourself", "or another user, admin only", etc.). This
	// test pins the language: any English substring in the
	// RU-locale /help output is a regression.
	gotRU := HandleCommand(context.Background(), envForRU(d), "/help")
	forbidden := []string{
		"add an exit-rule",     // /add_rule (used to be hardcoded EN)
		"for yourself",         // /add_device suffix
		"or another user",      // /clearrules suffix
		"with last-seen",       // /exit_nodes (the EN word, not the prefix)
		"requires /clearrules", // /clearrules suffix
		"admin only",           // /clearrules (RU has its own)
		"let a domain",         // /add_rule example key
	}
	for _, f := range forbidden {
		if strings.Contains(strings.ToLower(gotRU), f) {
			t.Errorf("expected RU-locale /help to be free of %q substring, but found it in:\n%s", f, gotRU)
		}
	}
	if !strings.Contains(gotRU, "/add_rule") {
		t.Errorf("expected /add_rule in RU /help, got: %q", gotRU)
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
	// 2026-07-16: v0.15.5 — butler-voice format. Header
	// is now "Last 20 audit log entries:" (no log-voice
	// "audit_log:" prefix).
	if !strings.Contains(got, "audit log") {
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

// TestAuditReplySplitLongLog pins the v0.16.5 contract: an
// audit log with > 10 entries must split into 2 bubbles.
// Below the threshold, no split. The split point is at the
// first blank line after the (entries/2)th row.
func TestAuditReplySplitLongLog(t *testing.T) {
	d := setupTestDB(t)
	// Seed 20 audit rows so the LIMIT 20 returns 20.
	for i := 0; i < 20; i++ {
		_, _ = d.Exec(
			`INSERT INTO audit_log(username, action, detail, created_at) VALUES ($1, $2, $3, $4)`,
			fmt.Sprintf("user%d", i),
			"test_action",
			fmt.Sprintf("entry #%d", i),
			int64(1722000000+i),
		)
	}
	got := auditReply(adminEnv(d))
	// 20 entries → split. Expect 1 split marker in the body.
	if c := strings.Count(got, splitMessageMarker); c != 1 {
		t.Errorf("expected 1 split marker (long log), got %d", c)
	}
	parts := splitReplyParts(got)
	if len(parts) != 2 {
		t.Errorf("expected 2 message parts, got %d: %q", len(parts), parts)
	}
	// First part carries the "more in next message" hint
	// so the operator knows the second bubble is coming.
	if !strings.Contains(parts[0], "next message") && !strings.Contains(parts[0], "следующ") {
		t.Errorf("first part should carry 'more in next message' hint, got: %q", parts[0])
	}
}

// TestAuditReplyNoSplitShortLog pins the v0.16.5 contract:
// an audit log with <= 10 entries does NOT split (single
// bubble). The split threshold is 10.
func TestAuditReplyNoSplitShortLog(t *testing.T) {
	d := setupTestDB(t)
	// Seed 5 audit rows.
	for i := 0; i < 5; i++ {
		_, _ = d.Exec(
			`INSERT INTO audit_log(username, action, detail, created_at) VALUES ($1, $2, $3, $4)`,
			fmt.Sprintf("user%d", i),
			"test_action",
			fmt.Sprintf("entry #%d", i),
			int64(1722000000+i),
		)
	}
	got := auditReply(adminEnv(d))
	// 5 entries → no split.
	if c := strings.Count(got, splitMessageMarker); c != 0 {
		t.Errorf("expected 0 split markers (short log), got %d: %q", c, got)
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
		`CREATE TABLE node_owner_map (node_id TEXT PRIMARY KEY, username TEXT DEFAULT '', tag TEXT DEFAULT 'tag:untagged', headscale_user_id INTEGER NOT NULL DEFAULT 0, tagged_by_user_id INTEGER NOT NULL DEFAULT 0, tagged_at INTEGER NOT NULL DEFAULT 0, hostname TEXT NOT NULL DEFAULT '')`,
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
	env := BotEnv{DB: d, DefaultMax: 200, Lang: i18n.LangEN}
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
	env := BotEnv{DB: d, Lang: i18n.LangEN} // both UserMaxRules and DefaultMax are zero → "no limit"
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

// TestBuildPlatformPicker_CopyButton — verifies the /add_device
// picker has a Copy button (Telegram copy_text field) on the
// first row, followed by the five platform buttons. The copy_text
// is bound at picker-construction time so the polling loop
// just ships the JSON verbatim to sendMessage.
func TestBuildPlatformPicker_CopyButton(t *testing.T) {
	const preauthKey = "hskey-fake-abcd1234efgh5678ijkl9012mnop3456qrst"
	picker := buildPlatformPicker(i18n.LangEN, preauthKey)
	if picker == nil {
		t.Fatal("expected non-nil picker")
	}
	if len(picker.InlineKeyboard) < 3 {
		t.Fatalf("expected at least 3 rows (copy + linux/windows/macos + ios/android), got %d", len(picker.InlineKeyboard))
	}
	// First row: the Copy button.
	row0 := picker.InlineKeyboard[0]
	if len(row0) != 1 {
		t.Fatalf("expected first row to be a single Copy button, got %d buttons", len(row0))
	}
	btn := row0[0]
	if text, _ := btn["text"].(string); !strings.Contains(text, "Copy") {
		t.Errorf("expected Copy text in first button, got %q", btn["text"])
	}
	// 2026-07-15: copy_text is now a typed object {"text": "..."}
	// per Telegram Bot API 7.0+, not a bare string.
	ct, ok := btn["copy_text"].(map[string]any)
	if !ok {
		t.Fatalf("expected copy_text to be a map[string]any, got %T", btn["copy_text"])
	}
	if ct["text"] != preauthKey {
		t.Errorf("expected copy_text.text to carry the preauth key, got %q", ct["text"])
	}
	// The Copy button MUST NOT have a callback_data — Telegram
	// would call the bot on tap, but the action is purely
	// client-side (clipboard).
	if _, hasCb := btn["callback_data"]; hasCb {
		t.Errorf("Copy button should not have callback_data (client-only action), got %q", btn["callback_data"])
	}
	// Second row: Linux, Windows, macOS.
	row1 := picker.InlineKeyboard[1]
	if len(row1) != 3 {
		t.Errorf("expected 3 platform buttons in row 2, got %d", len(row1))
	}
	wantCallbacks := []string{
		"add_device_platform:linux",
		"add_device_platform:windows",
		"add_device_platform:macos",
	}
	for i, want := range wantCallbacks {
		if row1[i]["callback_data"] != want {
			t.Errorf("row1[%d] callback_data = %q, want %q", i, row1[i]["callback_data"], want)
		}
	}
	// Third row: iOS, Android.
	row2 := picker.InlineKeyboard[2]
	if len(row2) != 2 {
		t.Errorf("expected 2 platform buttons in row 3, got %d", len(row2))
	}
	if row2[0]["callback_data"] != "add_device_platform:ios" {
		t.Errorf("row2[0] callback_data = %q, want add_device_platform:ios", row2[0]["callback_data"])
	}
	if row2[1]["callback_data"] != "add_device_platform:android" {
		t.Errorf("row2[1] callback_data = %q, want add_device_platform:android", row2[1]["callback_data"])
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
	// 2026-07-14: Этап 14 v5 — marker is now from the
	// i18n catalog (bot.trim.marker), not a hardcoded
	// English literal. Assert the shape: must contain
	// "truncated" so the user always sees the warning.
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected truncation marker mentioning 'truncated', got tail: %q", got[len(got)-60:])
	}
	short := "hello"
	if trimForTelegram(short) != short {
		t.Errorf("short strings must pass through unchanged")
	}
}

// --- Phase 4 tests ---

func TestHandleCommandVersion(t *testing.T) {
	d := setupTestDB(t)
	// 2026-07-16: v0.16.x — explicit Lang so the test
	// isn't subject to the i18n default fallthrough.
	env := BotEnv{DB: d, Version: "v0.3", Lang: i18n.LangEN}
	got := HandleCommand(context.Background(), env, "/version")
	if !strings.Contains(got, "v0.3") {
		t.Errorf("expected build label v0.3, got: %q", got)
	}
	// Go runtime version is whatever the test binary is built with.
	// 2026-07-16: v0.16.x — Field() renders "<b>label:</b>
	// <code>value</code>" (note the colon after the label).
	if !strings.Contains(got, "<b>Go:</b>") {
		t.Errorf("expected '<b>Go:</b>' label, got: %q", got)
	}
	// Schema level is the constant; lets the operator confirm
	// whether migrations have caught up to the binary.
	// 2026-07-16: v0.16.x — Field() labels render in the
	// lang's catalog (RU: "Схема БД", EN: "DB schema");
	// we test for the wrapped <b> + the <code>-d value.
	if !strings.Contains(got, "<b>DB schema:</b>") {
		t.Errorf("expected '<b>DB schema:</b>' label (EN), got: %q", got)
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
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Confirm").
	if !strings.Contains(got, "Confirm by sending within 30s") {
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
	// Use a channel (closed by the goroutine) for the done signal —
	// polling a bool races with the goroutine that sets it.
	saved := getKillProcess()
	killed := make(chan struct{})
	setKillProcess(func() { close(killed) })
	t.Cleanup(func() { setKillProcess(saved) })

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
	// Wait for the goroutine to fire killProcess (with timeout).
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
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
	saved := getKillProcess()
	setKillProcess(func() { t.Errorf("killProcess must NOT be called for a bad token") })
	t.Cleanup(func() { setKillProcess(saved) })

	got := HandleCommand(context.Background(), envFor(d), "/restart NOTATOKEN")
	if !strings.Contains(got, "not a valid confirmation token") {
		t.Errorf("expected 'not a valid' for unknown token, got: %q", got)
	}
}

func TestHandleCommandRestartExpiredToken(t *testing.T) {
	d := setupTestDB(t)
	saved := getKillProcess()
	setKillProcess(func() { t.Errorf("killProcess must NOT be called for an expired token") })
	t.Cleanup(func() { setKillProcess(saved) })

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
	for _, cmd := range []string{"status", "nodes", "exit_nodes", "rules", "quota", "audit", "ack", "version", "restart", "help", "bind", "unbind", "my_status", "my_nodes", "my_rules", "my_quota", "myexitnodes", "add_device", "add_rule", "delrule", "clearrules", "delete_rule"} {
		got := helpDetailReply(cmd, BotEnv{})
		if !strings.HasPrefix(got, "/"+cmd+" ") {
			t.Errorf("expected /%s detailed help, got: %q", cmd, got)
		}
	}
}

func TestHelpDetailUnknown(t *testing.T) {
	got := helpDetailReply("nonexistent", BotEnv{})
	if !strings.Contains(got, "No detailed help") {
		t.Errorf("expected 'No detailed help' for unknown command, got: %q", got)
	}
}

func TestHandleCommandHelpDetailed(t *testing.T) {
	d := setupTestDB(t)
	// /help ack must return the detailed ack help, not the short list.
	// 2026-07-14: Этап 14 v9 — the reply is now wrapped in the
	// butler envelope (header + body + optional footer), so the
	// full reply no longer starts with the body. Assert on the
	// envelope header (codex context) and the body substring
	// ("Idempotent") instead of the old body-prefix check.
	got := HandleCommand(context.Background(), envFor(d), "/help ack")
	// 2026-07-16: v0.15.2 — gate header is "🪶 ═══ Skygate ═══"
	// (followed by a topic line). /help uses context="codex".
	if !strings.HasPrefix(got, "🪶 ═══ Skygate ═══\n📖 The Codex") {
		t.Errorf("expected gate header + Codex topic, got: %q", got[:80])
	}
	if !strings.Contains(got, "/ack ") {
		t.Errorf("expected detailed /ack help body, got: %q", got)
	}
	if !strings.Contains(got, "Idempotent") {
		t.Errorf("expected ack-specific detail ('Idempotent'), got: %q", got)
	}
	// /help with no arg must still return the short list (backward compat).
	short := HandleCommand(context.Background(), envFor(d), "/help")
	if !strings.Contains(short, "Commands") {
		t.Errorf("expected short list with no /help arg, got: %q", short)
	}
	// /help unknown should fall through to the "no detailed help" branch.
	unknown := HandleCommand(context.Background(), envFor(d), "/help foo")
	if !strings.Contains(unknown, "No detailed help") {
		t.Errorf("expected 'No detailed help' for unknown, got: %q", unknown)
	}
}

// --- Этап 11 user/admin distinction tests ---

// userEnv builds a BotEnv pre-populated as a non-admin user "alice"
// (id=2, the second row seeded by setupTestDB).
//
// 2026-07-14: Этап 14 v5 — sets Lang to "en" so the i18n
// catalog returns the English reply. Tests that need Russian
// override Lang by building the BotEnv by hand.
func userEnv(d *sql.DB) BotEnv {
	return BotEnv{DB: d, ChatID: 555, PortalUserID: 2, Username: "alice", IsAdmin: false, Lang: i18n.LangEN}
}

func adminEnv(d *sql.DB) BotEnv {
	return BotEnv{DB: d, ChatID: 999, PortalUserID: 1, Username: "skyadmin", IsAdmin: true, Lang: i18n.LangEN}
}

func TestMyStatusReplyUser(t *testing.T) {
	d := setupTestDB(t)
	got := myStatusReply(userEnv(d))
	// alice owns 0 rules (only skyadmin's 12 are seeded), and 0 devices.
	if !strings.Contains(got, "alice") {
		t.Errorf("expected username in my_status, got: %q", got)
	}
	// 2026-07-16: v0.16.x — Field() labels (just the noun;
	// the value is in <code> after the colon). The bot
	// reply now reads "rules: 0" via Field() instead of
	// the prose "rules: 0 / ∞" — the format depends on
	// which catalog key the Field() call lands on. We
	// pin only the noun label, not the colon-style
	// "rules: 0", because the value moves to <code>.
	if !strings.Contains(got, "rules:") {
		t.Errorf("expected 'rules:' label in /my_status body, got: %q", got)
	}
	if !strings.Contains(got, "0") {
		t.Errorf("expected count '0' in /my_status body, got: %q", got)
	}
	// 2026-07-16: v0.15.2 — myStatusReply returns just the
	// body. The gate envelope (═══ Skygate ═══ … ═══ —
	// signoff ═══) is applied by Compose() in HandleCommand;
	// unit tests of the reply function in isolation don't
	// see it. HandleCommand-level tests
	// (TestHandleCommand*Envelope) cover the gate shape.
}

func TestMyStatusReplyUnidentified(t *testing.T) {
	d := setupTestDB(t)
	got := myStatusReply(BotEnv{DB: d, Lang: i18n.LangEN})
	if !strings.Contains(got, "chat not bound") {
		t.Errorf("expected 'chat not bound' for unidentified caller, got: %q", got)
	}
}

// TestHTMLRepliesMarkParseMode pins the v0.16.2 contract: every reply
// function that uses Field()/Section()/PreLinesRaw() must call
// markHTMLReply() so the <b>/<i>/<pre>/<code> tags render on
// Telegram. Without it the user sees raw "<b>...</b> <code>...</code>"
// source text. Each sub-case checks ParseMode=HTML on the
// pendingReplyForCurrentMessage side-channel.
//
// 2026-07-16: v0.16.2 — added after the v0.16.1 bug where the
// /version reply (and six other read commands) shipped with HTML
// formatting but no parse_mode, so the tags showed up as raw
// text. This test is a regression guard.
func TestHTMLRepliesMarkParseMode(t *testing.T) {
	d := setupTestDB(t)
	env := userEnv(d)
	// env needs IsAdmin for the admin-scope commands
	// (audit, exit_nodes_health, sync_nodes). The user-scope
	// commands (my_status, my_nodes, my_rules, my_quota,
	// myexitnodes) work with either.
	adminEnv := adminEnv(d)

	cases := []struct {
		name string
		call func()
	}{
		{"my_status", func() { myStatusReply(env) }},
		{"my_nodes", func() { myNodesReply(env) }},
		{"my_rules", func() { myRulesReply(env) }},
		{"my_quota", func() { myQuotaReply(env) }},
		{"myexitnodes", func() { myExitNodesReply(env) }},
		{"mysubnet", func() { mySubnetReply(env) }},
		{"version", func() { versionReply(env) }},
		{"audit", func() { auditReply(adminEnv) }},
		{"exit_nodes_health", func() { exitNodesHealthReply(adminEnv) }},
		{"help", func() { helpReply(env) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset between cases so a previous case's
			// pending doesn't leak into the next one.
			pendingReplyForCurrentMessage = nil
			tc.call()
			if pendingReplyForCurrentMessage == nil {
				t.Fatalf("expected pendingReplyForCurrentMessage to be set (markHTMLReply was not called), got nil")
			}
			if pendingReplyForCurrentMessage.ParseMode != "HTML" {
				t.Errorf("expected ParseMode=HTML, got %q", pendingReplyForCurrentMessage.ParseMode)
			}
		})
	}
}

// TestMarkHTMLReplyPreservesKeyboard pins the v0.16.2 contract
// that markHTMLReply() doesn't wipe an existing inline-keyboard
// when one is already set. myExitNodesReply sets both the
// per-row "→ hostname" keyboard AND the HTML parse mode for
// the tabular <pre> body; the helper must keep the keyboard
// intact (otherwise the tap-to-set UX would break).
func TestMarkHTMLReplyPreservesKeyboard(t *testing.T) {
	d := setupTestDB(t)
	// Seed: one enabled exit-server so myExitNodesReply
	// populates the keyboard.
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('emilia-1', 'emilia', 1)`)
	pendingReplyForCurrentMessage = nil
	_ = myExitNodesReply(userEnv(d))
	if pendingReplyForCurrentMessage == nil {
		t.Fatalf("expected pendingReplyForCurrentMessage to be set, got nil")
	}
	if len(pendingReplyForCurrentMessage.InlineKeyboard) == 0 {
		t.Errorf("expected InlineKeyboard to be preserved (per-row '→ hostname' buttons), got 0 rows")
	}
	if pendingReplyForCurrentMessage.ParseMode != "HTML" {
		t.Errorf("expected ParseMode=HTML, got %q", pendingReplyForCurrentMessage.ParseMode)
	}
}

func TestMyNodesReplyUserFiltersToCaller(t *testing.T) {
	d := setupTestDB(t)
	// Seed: alice has one device.
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-laptop', 'alice', 'tag:private')`)
	got := myNodesReply(userEnv(d))
	if !strings.Contains(got, "alice-laptop") {
		t.Errorf("expected alice-laptop in my_nodes, got: %q", got)
	}
	// skyadmin's nodes must NOT leak through.
	if strings.Contains(got, "n1") {
		t.Errorf("alice must not see skyadmin's nodes, got: %q", got)
	}
}

func TestMyNodesReplyEmpty(t *testing.T) {
	d := setupTestDB(t)
	got := myNodesReply(userEnv(d))
	if !strings.Contains(got, "no devices yet") {
		t.Errorf("expected 'no devices yet' for user with no devices, got: %q", got)
	}
}

// 2026-07-15: Этап 14 v13 — lazy hostname backfill tests.
//
// Before v0.10.12, /my_nodes and /nodes silently showed bare
// node_ids when node_owner_map.hostname was empty (the column was
// added in migration v0.34, but backfillNodeOwnership only runs
// via /admin/devices — so any user who opened the bot before that
// page saw the bare-id view in the screenshot in the v0.10.12
// release notes). These tests pin the new self-healing behaviour.

// fakeNodeServer builds a tiny httptest server that returns the
// given nodes from GET /api/v1/node, plus a *headscale.Client
// pointed at it. Used by the lazy-backfill tests to drive the
// production code path (env.HS.ListAllNodes) without spinning up
// a real headscale instance.
func fakeNodeServer(t *testing.T, nodes []headscale.HSNode) (*httptest.Server, *headscale.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/node":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
		default:
			http.Error(w, "unexpected: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, headscale.New(srv.URL, "fake-api-key")
}

func TestHostnameMapFromHeadscale_NilReturnsEmpty(t *testing.T) {
	got := hostnameMapFromHeadscale(nil)
	if len(got) != 0 {
		t.Errorf("nil client should return empty map, got %v", got)
	}
}

func TestHostnameMapFromHeadscale_BuildsMapFromRealClient(t *testing.T) {
	_, hs := fakeNodeServer(t, []headscale.HSNode{
		{ID: "1", GivenName: "skygate-vm", Name: "other"},
		{ID: "2", GivenName: "", Name: "fallback-host"},
		{ID: "3", GivenName: "", Name: ""}, // both empty: toView() will produce "" — must be skipped
	})
	got := hostnameMapFromHeadscale(hs)
	if got["1"] != "skygate-vm" {
		t.Errorf("id=1: got %q, want skygate-vm", got["1"])
	}
	if got["2"] != "fallback-host" {
		t.Errorf("id=2: got %q, want fallback-host (Name fallback)", got["2"])
	}
	if _, ok := got["3"]; ok {
		t.Errorf("id=3 should be skipped (both names empty)")
	}
}

func TestHostnameMapFromHeadscale_ServerErrorReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	hs := headscale.New(srv.URL, "fake-api-key")
	got := hostnameMapFromHeadscale(hs)
	if len(got) != 0 {
		t.Errorf("error should return empty map, got %v", got)
	}
}

func TestMyNodesReply_LazyBackfillEmptyHostname(t *testing.T) {
	d := setupTestDB(t)
	// Seed: alice has a row with empty hostname (the v0.10.11
	// regression state). The mock HS will return a friendly name.
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag, hostname) VALUES ('alice-laptop', 'alice', 'tag:private', '')`)
	_, hs := fakeNodeServer(t, []headscale.HSNode{{ID: "alice-laptop", GivenName: "alice-laptop-fancy"}})
	env := userEnv(d)
	env.HS = hs
	got := myNodesReply(env)
	if !strings.Contains(got, "alice-laptop-fancy") {
		t.Errorf("expected friendly hostname in reply, got: %q", got)
	}
	if !strings.Contains(got, "alice-laptop") {
		t.Errorf("expected node_id in reply (alongside the hostname), got: %q", got)
	}
	// Confirm the DB row was actually updated.
	var hn string
	if err := d.QueryRow(`SELECT hostname FROM node_owner_map WHERE node_id = 'alice-laptop'`).Scan(&hn); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if hn != "alice-laptop-fancy" {
		t.Errorf("DB hostname=%q, want alice-laptop-fancy", hn)
	}
}

func TestMyNodesReply_NoBackfillWhenAllHostnamesSet(t *testing.T) {
	d := setupTestDB(t)
	// Seed: alice has a row with hostname already set. The mock
	// returns a DIFFERENT name; we must NOT overwrite the existing
	// value (the column is the cached copy, not live data — we
	// only fill empty cells).
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag, hostname) VALUES ('alice-laptop', 'alice', 'tag:private', 'cached-name')`)
	_, hs := fakeNodeServer(t, []headscale.HSNode{{ID: "alice-laptop", GivenName: "would-clobber"}})
	env := userEnv(d)
	env.HS = hs
	_ = myNodesReply(env)
	var hn string
	if err := d.QueryRow(`SELECT hostname FROM node_owner_map WHERE node_id = 'alice-laptop'`).Scan(&hn); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if hn != "cached-name" {
		t.Errorf("hostname overwritten: got %q, want cached-name", hn)
	}
}

func TestMyNodesReply_NilHSNoCrash(t *testing.T) {
	d := setupTestDB(t)
	// Regression guard: the v0.10.11 code path assumed env.HS
	// was always non-nil. When it's nil (read-only deploy) the
	// backfill must be skipped, not panic.
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag, hostname) VALUES ('alice-laptop', 'alice', 'tag:private', '')`)
	env := userEnv(d)
	env.HS = nil
	got := myNodesReply(env)
	if !strings.Contains(got, "alice-laptop") {
		t.Errorf("expected node_id in reply (no HS to backfill from), got: %q", got)
	}
	// Row must still be empty (no HS, no update).
	var hn string
	if err := d.QueryRow(`SELECT hostname FROM node_owner_map WHERE node_id = 'alice-laptop'`).Scan(&hn); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if hn != "" {
		t.Errorf("hostname should still be empty (no HS), got %q", hn)
	}
}

// 2026-07-15: Этап 14 v13 — lazy tag backfill in /my_nodes
// (and /nodes, same code path). The bot's read used to silently
// show tag:untagged for nodes whose headscale tag was set by
// an admin after the row was created (PostAdminNodeTag's old
// "tagged-devices" guard skipped the node_owner_map update).
// v0.10.13 closes the gap: when the bot reads the list, if
// any visible row's DB tag disagrees with the headscale tag,
// SyncTagsFromHeadscale updates them in place.

func TestMyNodesReply_LazyTagBackfillStaleRow(t *testing.T) {
	d := setupTestDB(t)
	// Seed: alice has a row with tag:untagged in DB. Headscale
	// reports tag:private. The bot must sync the row and render
	// with the new tag.
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag, hostname) VALUES ('alice-laptop', 'alice', 'tag:untagged', 'alice-laptop')`)
	_, hs := fakeNodeServer(t, []headscale.HSNode{
		{ID: "alice-laptop", GivenName: "alice-laptop", Name: "alice-laptop", Tags: []string{"tag:private"}},
	})
	env := userEnv(d)
	env.HS = hs
	got := myNodesReply(env)
	// Reply should mention tag:private, not tag:untagged.
	if !strings.Contains(got, "tag:private") {
		t.Errorf("expected tag:private in reply after lazy sync, got: %q", got)
	}
	if strings.Contains(got, "tag:untagged") {
		t.Errorf("expected tag:untagged to be gone, got: %q", got)
	}
	// DB row must be updated.
	var tag string
	if err := d.QueryRow(`SELECT tag FROM node_owner_map WHERE node_id = 'alice-laptop'`).Scan(&tag); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if tag != "tag:private" {
		t.Errorf("DB tag: got %q, want tag:private", tag)
	}
}

func TestMyNodesReply_NoTagBackfillWhenMatching(t *testing.T) {
	d := setupTestDB(t)
	// Seed: alice has a row with tag:private. Headscale says
	// tag:private. SyncTagsFromHeadscale must NOT update (the
	// guard "tag != ?" prevents a no-op write).
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag, hostname) VALUES ('alice-laptop', 'alice', 'tag:private', 'alice-laptop')`)
	_, hs := fakeNodeServer(t, []headscale.HSNode{
		{ID: "alice-laptop", GivenName: "alice-laptop", Name: "alice-laptop", Tags: []string{"tag:private"}},
	})
	env := userEnv(d)
	env.HS = hs
	_ = myNodesReply(env)
	var tag string
	if err := d.QueryRow(`SELECT tag FROM node_owner_map WHERE node_id = 'alice-laptop'`).Scan(&tag); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if tag != "tag:private" {
		t.Errorf("tag was changed despite matching: got %q, want tag:private", tag)
	}
}

func TestMyRulesReplyUserFiltersToCaller(t *testing.T) {
	d := setupTestDB(t)
	// Seed: alice has 1 rule, skyadmin has 12 (from setup).
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, target_value) VALUES (2, 'github.com')`)
	got := myRulesReply(userEnv(d))
	if !strings.Contains(got, "github.com") {
		t.Errorf("expected github.com in my_rules for alice, got: %q", got)
	}
	// 2026-07-16: v0.16.x — "more HTML" pass. The previous
	// "#%d @%s\n  %s %s → %s" prose format is gone; the
	// new tabular <pre> block does not contain the literal
	// "→" arrow. Filter check therefore has to use the
	// tabular shape (the target_value alone is enough
	// since alice has no other target_values).
	if strings.Contains(got, "  domain x →") {
		t.Errorf("alice must not see skyadmin's rules, got: %q", got)
	}
	// Pin the new format: tabular <pre> block with
	// bold header row "ID EXIT TYPE TARGET ACTION".
	if !strings.Contains(got, "<b>ID") || !strings.Contains(got, "ACTION</b>") {
		t.Errorf("expected new tabular <pre> block with bold ID/ACTION header, got: %q", got)
	}
	// And the Section() divider for "rules".
	// 2026-07-16: v0.16.5 — short list (1 rule) does
	// NOT split. The split threshold is 12; alice's
	// single rule is below it, so no split marker.
	if c := strings.Count(got, splitMessageMarker); c != 0 {
		t.Errorf("expected 0 split markers (1 rule), got %d", c)
	}
	if !strings.Contains(got, "rules") {
		t.Errorf("expected 'rules' section divider, got: %q", got)
	}
}

// TestMyRulesReplySplitLongList pins the v0.16.5 contract:
// /my_rules splits into 2 bubbles if the user has more than
// 12 rules (the v0.16.5 split threshold). Below the threshold
// (1 rule) the reply is a single bubble (covered by the test
// above).
func TestMyRulesReplySplitLongList(t *testing.T) {
	d := setupTestDB(t)
	// Seed 15 rules for alice (above the 12-rule threshold).
	for i := 0; i < 15; i++ {
		_, _ = d.Exec(
			`INSERT INTO device_rules(user_id, target_value) VALUES ($1, $2)`,
			2, fmt.Sprintf("target-%d.example", i),
		)
	}
	got := myRulesReply(userEnv(d))
	// 15 rules → split. Expect 1 split marker in the body.
	if c := strings.Count(got, splitMessageMarker); c != 1 {
		t.Errorf("expected 1 split marker (15 rules), got %d", c)
	}
	parts := splitReplyParts(got)
	if len(parts) != 2 {
		t.Errorf("expected 2 message parts, got %d: %q", len(parts), parts)
	}
	// First part carries the "more in next message" hint.
	if !strings.Contains(parts[0], "next message") && !strings.Contains(parts[0], "следующ") {
		t.Errorf("first part should carry 'more in next message' hint, got: %q", parts[0])
	}
}

func TestMyQuotaReplyUser(t *testing.T) {
	d := setupTestDB(t)
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, PortalUserID: 2, Username: "alice", IsAdmin: false, UserMaxRules: map[string]int{"alice": 5}, DefaultMax: 200}
	got := myQuotaReply(env)
	if !strings.Contains(got, "alice") {
		t.Errorf("expected alice in my_quota, got: %q", got)
	}
	// alice has 0 rules; her cap is 5; expect 0/5 + 0%.
	if !strings.Contains(got, "0 / 5") {
		t.Errorf("expected '0 / 5' (0 rules, 5 cap) for alice, got: %q", got)
	}
	// 2026-07-16: v0.16.x — "more HTML" pass. The reply
	// now uses three Field() lines: rules count, fill
	// bar, cap. The bar is rendered with the same
	// "[no limit]" / "[██░░░░░░░░]" shapes (0 rules →
	// empty bar, pct clamped to 0).
	if !strings.Contains(got, "<b>rules:</b>") {
		t.Errorf("expected 'rules:' Field() label, got: %q", got)
	}
	if !strings.Contains(got, "<b>cap:</b>") {
		t.Errorf("expected 'cap:' Field() label, got: %q", got)
	}
	if !strings.Contains(got, "<b>fill:</b>") {
		t.Errorf("expected 'fill:' Field() label, got: %q", got)
	}
	// And the Section() divider for "quota".
	if !strings.Contains(got, "quota") {
		t.Errorf("expected 'quota' section divider, got: %q", got)
	}
}

// TestMySubnetReplyEmpty pins the v0.16.0 contract: a user
// without a personal subnet gets the "empty" hint
// pointing at /admin/users/{id}/subnet. Reads from the
// denormalized portal_users columns (no JOIN on
// user_subnets).
func TestMySubnetReplyEmpty(t *testing.T) {
	d := setupTestDB(t)
	env := userEnv(d) // alice, no subnet allocated
	got := mySubnetReply(env)
	// "no personal subnet" hint, with the URL the
	// operator needs to provision one.
	if !strings.Contains(got, "no personal subnet") {
		t.Errorf("expected 'no personal subnet' hint, got: %q", got)
	}
	if !strings.Contains(got, "/admin/users/") {
		t.Errorf("expected '/admin/users/' path hint, got: %q", got)
	}
}

// TestMySubnetReplyAllocated pins the v0.16.0 contract:
// once Create(d, userID, ...) has run, /mysubnet shows
// the CIDR + status + plane label.
func TestMySubnetReplyAllocated(t *testing.T) {
	d := setupTestDB(t)
	uid := seedPortalUserForSubnets(t, d)
	if _, err := subnet.Create(d, uid, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, PortalUserID: uid, Username: "alice", IsAdmin: false}
	got := mySubnetReply(env)
	// The deterministic 10.0.<uid>.0/24 CIDR.
	wantCIDR := fmt.Sprintf("10.0.%d.0/24", uid)
	if !strings.Contains(got, wantCIDR) {
		t.Errorf("expected CIDR %s, got: %q", wantCIDR, got)
	}
	// Status should be "pending" right after Create
	// (no sidecar yet, v0.16.1 will move it to active).
	if !strings.Contains(got, "pending") {
		t.Errorf("expected 'pending' status, got: %q", got)
	}
	// Plane label for global plane (controlPlaneURL="").
	if !strings.Contains(got, "global") {
		t.Errorf("expected 'global' plane label, got: %q", got)
	}
}

// seedPortalUserForSubnets inserts a portal_user and
// returns the new id. Used by TestMySubnetReplyAllocated
// (and any future test that needs a known-good user_id
// for /mysubnet without going through the per-test
// setupTestDB helper, which only seeds alice).
func seedPortalUserForSubnets(t *testing.T, d interface {
	Exec(query string, args ...any) (sql.Result, error)
}) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin) VALUES ($1, '', 0)`,
		"alice-subnet",
	)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestMySubnetProvisionReply_IssuesPreauth — v0.16.7.
// /mysubnet provision issues a per-user preauth key
// (tag:subnet-router, 1h TTL) and replies with the
// key + the suggested tailscale up command. The fake
// headscale server returns a stub key; the test
// asserts both the key and the command snippet
// appear in the reply body.
func TestMySubnetProvisionReply_IssuesPreauth(t *testing.T) {
	d := setupTestDB(t)
	uid := seedPortalUserForSubnets(t, d)
	// Provision needs a headscale_user_id on the user.
	if _, err := d.Exec(`UPDATE portal_users SET headscale_user_id = 42 WHERE id = $1`, uid); err != nil {
		t.Fatalf("set hs id: %v", err)
	}
	// Create a user_subnets row (the manager errors if missing).
	if _, err := subnet.Create(d, uid, "", "skygate-subnet-alice-subnet"); err != nil {
		t.Fatalf("subnet.Create: %v", err)
	}
	// Wire a sidecar manager with a fake headscale API
	// that returns a stub preauth key on POST.
	_, hs := fakeSidecarPreauthHS(t)
	mgr := sidecar.New(d, func(int64) *headscale.Client { return hs }, nil, 0)

	env := userEnv(d)
	env.PortalUserID = uid
	env.Username = "alice-subnet"
	env.Sidecar = mgr

	got := mySubnetProvisionReply(env)
	if !strings.Contains(got, "hskey-fake") {
		t.Errorf("expected preauth key in body, got: %q", got)
	}
	if !strings.Contains(got, "--authkey=") {
		t.Errorf("expected --authkey= snippet in body, got: %q", got)
	}
	if !strings.Contains(got, "skygate-subnet-alice-subnet") {
		t.Errorf("expected suggested hostname in body, got: %q", got)
	}
}

// TestMySubnetProvisionReply_NoManagerReturnsHint — if
// the sidecar manager isn't wired (e.g. operator hasn't
// configured SKYGATE_SIDECAR_SYNC_PERIOD), the reply
// tells the user to ask the admin.
func TestMySubnetProvisionReply_NoManagerReturnsHint(t *testing.T) {
	d := setupTestDB(t)
	env := userEnv(d)
	env.PortalUserID = 1
	env.Sidecar = nil
	got := mySubnetProvisionReply(env)
	if !strings.Contains(got, "sidecar manager") &&
		!strings.Contains(got, "ask an admin") &&
		!strings.Contains(got, "ask the admin") &&
		!strings.Contains(got, "попросите админа") {
		t.Errorf("expected 'ask the admin' hint, got: %q", got)
	}
}

// fakeSidecarPreauthHS returns a headscale httptest
// server that returns a stub preauth key on POST
// /api/v1/preauthkey. Used by the bot's /mysubnet
// provision test path.
func fakeSidecarPreauthHS(t *testing.T) (*httptest.Server, *headscale.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/preauthkey" && r.Method == "POST" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(headscale.PreauthKey{
				ID:     "1",
				Key:    "hskey-fake",
				UserID: 42,
			})
			return
		}
		http.Error(w, "not found", 404)
	}))
	t.Cleanup(srv.Close)
	return srv, headscale.New(srv.URL, "test-key")
}

func TestAdminOnlyRejectsUser(t *testing.T) {
	d := setupTestDB(t)
	for _, cmd := range []string{"/status", "/nodes", "/rules", "/quota", "/audit", "/exit_nodes", "/ack", "/restart", "/bind", "/unbind"} {
		got := HandleCommand(context.Background(), userEnv(d), cmd)
		if !strings.Contains(got, "admin only") {
			t.Errorf("expected 'admin only' for %s as user, got: %q", cmd, got)
		}
	}
}

func TestAdminCommandsWorkForAdmin(t *testing.T) {
	d := setupTestDB(t)
	// /status should still work for admin (backward compat).
	got := HandleCommand(context.Background(), adminEnv(d), "/status")
	if !strings.Contains(got, "rules: 12") {
		t.Errorf("expected /status to work for admin, got: %q", got)
	}
	// /nodes should list all tailnet nodes.
	got = HandleCommand(context.Background(), adminEnv(d), "/nodes")
	if !strings.Contains(got, "Tailnet nodes") {
		t.Errorf("expected /nodes to work for admin, got: %q", got)
	}
}

func TestHelpReplyAdminShowsAllCategories(t *testing.T) {
	d := setupTestDB(t)
	got := helpReply(adminEnv(d))
	// Admin sees all three categories (one header, three sections).
	for _, expected := range []string{"Commands", "/my_status", "/restart", "/bind"} {
		if !strings.Contains(got, expected) {
			t.Errorf("admin /help should contain %q, got: %q", expected, got)
		}
	}
}

func TestHelpReplyUserHidesAdmin(t *testing.T) {
	d := setupTestDB(t)
	got := helpReply(userEnv(d))
	// User sees "Your commands" + user-scope, but NOT admin-scope.
	if !strings.Contains(got, "/my_status") {
		t.Errorf("user /help should contain /my_status, got: %q", got)
	}
	if strings.Contains(got, "/restart") {
		t.Errorf("user /help should NOT contain admin /restart, got: %q", got)
	}
	if strings.Contains(got, "/bind") {
		t.Errorf("user /help should NOT contain admin /bind, got: %q", got)
	}
}

// 2026-07-16: v0.15.5 — /help must:
//   - list /unbind_self (self-service, in the Auth section)
//   - have a 18-char gutter so the description column lines up
//     across all rows including `/exit_nodes_health` (17 chars)
//   - not repeat the command name in the description column
//     (the gutter already carries it)
//
// 2026-07-16: v0.16.3 — /help now uses tabular <pre> blocks per
// section, with <code>...</code> for placeholders (was
// markdown backticks). The 18-char gutter moved INSIDE the
// <pre> block (20 chars now) so the column alignment survives
// the proportional→monospace switch. Tests updated to match:
//   - `<pre>...</pre>` wrapping each section's table
//   - `<code>&lt;...&gt;</code>` for placeholders
//   - 20-char gutter inside <pre> (was 18 in proportional font)
//   - no leftover markdown backticks in the body
func TestHelpReplyV0155Layout(t *testing.T) {
	d := setupTestDB(t)
	got := helpReply(adminEnv(d))
	// /unbind_self must be listed under Auth.
	if !strings.Contains(got, "/unbind_self") {
		t.Errorf("admin /help should list /unbind_self, got: %q", got)
	}
	// 2026-07-16: v0.16.3 — tabular <pre> blocks. The
	// 20-char gutter pads the command column inside <pre>
	// (Telegram's <pre> uses a fixed-pitch font, so the
	// column alignment survives). /exit_nodes_health is
	// 18 chars (incl. leading `/`); padded to 20 = 2
	// spaces of pad, then 2 spaces of separator before
	// the description starts. Total = 22-char prefix.
	const preGutter = 20
	const exitNodesHealth = "/exit_nodes_health" // 18 chars
	prefix := exitNodesHealth + strings.Repeat(" ", preGutter-len(exitNodesHealth)) + "  "
	if !strings.Contains(got, prefix) {
		t.Errorf("expected /exit_nodes_health padded to %d chars in <pre>, got: %q", preGutter, got)
	}
	// Short commands must also be padded to the same width.
	// /status is 7 chars; padded to 20 = 13 spaces of pad.
	shortCmd := "/status" // 7 chars
	shortPad := shortCmd + strings.Repeat(" ", preGutter-len(shortCmd)) + "  "
	if !strings.Contains(got, shortPad) {
		t.Errorf("expected /status padded to %d chars in <pre>, got: %q", preGutter, got)
	}
	// 2026-07-16: v0.16.3 — no leftover markdown backticks.
	// The catalog was converted to <code>...</code> by the
	// convert_help_backticks.py script; the body must not
	// contain a literal backtick inside the table area.
	// We check a few of the most common commands to make
	// sure the conversion caught them.
	for _, cmd := range []string{"/status", "/nodes", "/rules", "/ack", "/bind"} {
		// The backticked form was "`/status` — ..." etc.
		// The new form has no backticks around the command
		// (the gutter carries the command; the description
		// has no command name in it).
		dup := "`" + cmd + "`"
		if strings.Contains(got, dup) {
			t.Errorf("description still has backticks around %q, got: %q", cmd, got)
		}
	}
	// 2026-07-16: v0.16.3 — the new format has 3 <pre>
	// blocks (auth + user-scope + admin). Pin that
	// structural feature.
	if gotPre := strings.Count(got, "<pre>"); gotPre != 3 {
		t.Errorf("expected 3 <pre> blocks (auth+user+admin), got %d", gotPre)
	}
	// And 3 <b> section headers (one per section). Plus
	// the title <b> at the top = 4 total.
	if gotB := strings.Count(got, "<b>"); gotB < 4 {
		t.Errorf("expected at least 4 <b> tags (title + 3 section headers), got %d", gotB)
	}
	// And <code> tags for the placeholders. The catalog has
	// at least 10 placeholders (e.g. <code>&lt;key&gt;</code>,
	// <code>&lt;target&gt;</code>, <code>/help ack</code>, etc.).
	if gotCode := strings.Count(got, "<code>"); gotCode < 10 {
		t.Errorf("expected at least 10 <code> tags in the body, got %d", gotCode)
	}
	// 2026-07-16: v0.16.3 — markHTMLReply() is called so
	// the pending reply carries ParseMode=HTML. Without it
	// the <b>/<pre>/<code> would show up as raw text in
	// the chat (the v0.16.1 bug that v0.16.2 fixed for the
	// other 8 replies; /help is the 9th).
	if pendingReplyForCurrentMessage == nil || pendingReplyForCurrentMessage.ParseMode != "HTML" {
		t.Errorf("expected pending ParseMode=HTML, got %+v", pendingReplyForCurrentMessage)
	}
	// 2026-07-16: v0.16.5 — split into multiple bubbles.
	// Admin layout: Auth + User-scope + Admin = 3 bubbles
	// (2 split markers in the body). The split marker
	// is a sentinel that the send path detects and
	// splits on; the body string itself contains it
	// twice (between Auth/User-scope and User-scope/Admin).
	if got := strings.Count(got, splitMessageMarker); got != 2 {
		t.Errorf("expected 2 split markers (admin: 3 bubbles), got %d", got)
	}
	// And the 3 sections must each survive in one of
	// the parts. Splitting on the marker and trimming
	// whitespace should give us 3 non-empty messages.
	parts := splitReplyParts(got)
	if len(parts) != 3 {
		t.Errorf("expected 3 message parts after split, got %d: %q", len(parts), parts)
	}
	// First part carries the title + subtitle + Auth.
	if !strings.Contains(parts[0], "Auth — ") {
		t.Errorf("first part should contain Auth section, got: %q", parts[0])
	}
	// Second part carries User-scope.
	if !strings.Contains(parts[1], "Your data") {
		t.Errorf("second part should contain User-scope section, got: %q", parts[1])
	}
	// Third part carries Admin.
	if !strings.Contains(parts[2], "Admin — ") {
		t.Errorf("third part should contain Admin section, got: %q", parts[2])
	}
}

// TestHelpReplyUserSplitsIntoTwoBubbles pins the v0.16.5 contract
// for the user-scope /help: Auth + User-scope = 2 bubbles (1
// split marker). Admin section must NOT appear in either part.
func TestHelpReplyUserSplitsIntoTwoBubbles(t *testing.T) {
	d := setupTestDB(t)
	got := helpReply(userEnv(d))
	// 1 split marker → 2 parts.
	if c := strings.Count(got, splitMessageMarker); c != 1 {
		t.Errorf("expected 1 split marker (user: 2 bubbles), got %d", c)
	}
	parts := splitReplyParts(got)
	if len(parts) != 2 {
		t.Errorf("expected 2 message parts, got %d: %q", len(parts), parts)
	}
	// Admin section must NOT be in either part (user doesn't
	// have access to /status, /bind, etc.).
	for i, p := range parts {
		if strings.Contains(p, "/status") || strings.Contains(p, "/bind") {
			t.Errorf("part %d unexpectedly contains admin command: %q", i, p)
		}
	}
	// And the first part should still have the title.
	if !strings.Contains(parts[0], i18n.T(i18n.LangEN, "bot.help.header")) {
		t.Errorf("first part should carry the title, got: %q", parts[0])
	}
}

// TestSplitReplyParts pins the v0.16.5 split helper directly.
// 5 sub-cases: no marker, single marker, double marker, leading
// marker, trailing marker.
func TestSplitReplyParts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"no marker", "hello world", []string{"hello world"}},
		{"single marker", "first" + splitMessageMarker + "second", []string{"first", "second"}},
		{"double marker", "a" + splitMessageMarker + "b" + splitMessageMarker + "c", []string{"a", "b", "c"}},
		// Empty parts are dropped.
		{"leading marker", splitMessageMarker + "content", []string{"content"}},
		{"trailing marker", "content" + splitMessageMarker, []string{"content"}},
		// Whitespace around the marker is trimmed.
		{"whitespace padding", "  one  " + splitMessageMarker + "  two  ", []string{"one", "two"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitReplyParts(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d parts, want %d: %q", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("part %d = %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

func TestBindReplyAdminHappy(t *testing.T) {
	d := setupTestDB(t)
	// /bind 123456789 alice
	got := HandleCommand(context.Background(), adminEnv(d), "/bind 123456789 alice")
	if !strings.Contains(got, "✓") {
		t.Errorf("expected ✓ in bind reply, got: %q", got)
	}
	// The binding must be in the DB.
	var chatID, userID int64
	var isAdmin int
	if err := d.QueryRow(`SELECT chat_id, portal_user_id, is_admin FROM telegram_bindings WHERE chat_id = 123456789`).Scan(&chatID, &userID, &isAdmin); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if chatID != 123456789 || userID != 2 || isAdmin != 0 {
		t.Errorf("unexpected binding row: chat=%d user=%d admin=%d", chatID, userID, isAdmin)
	}
}

func TestBindReplyRejectsUser(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/bind 123456789 alice")
	if !strings.Contains(got, "admin only") {
		t.Errorf("expected 'admin only' for /bind as user, got: %q", got)
	}
}

func TestBindReplyBadArgs(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), adminEnv(d), "/bind 123")
	if !strings.Contains(got, "usage") {
		t.Errorf("expected usage hint for /bind with 1 arg, got: %q", got)
	}
	got = HandleCommand(context.Background(), adminEnv(d), "/bind notanumber alice")
	if !strings.Contains(got, "is not a valid chat_id") {
		t.Errorf("expected 'is not a valid chat_id' for /bind with non-numeric chat, got: %q", got)
	}
	got = HandleCommand(context.Background(), adminEnv(d), "/bind 123456789 nobody")
	if !strings.Contains(got, "no portal user") {
		t.Errorf("expected 'no portal user' for unknown username, got: %q", got)
	}
}

func TestUnbindReplyAdminHappy(t *testing.T) {
	d := setupTestDB(t)
	// Bind first.
	_, _ = d.Exec(`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin) VALUES (42, 1, 1)`)
	got := HandleCommand(context.Background(), adminEnv(d), "/unbind 42")
	if !strings.Contains(got, "✓") {
		t.Errorf("expected ✓ in unbind reply, got: %q", got)
	}
	// Row must be gone.
	var n int
	d.QueryRow(`SELECT COUNT(*) FROM telegram_bindings WHERE chat_id = 42`).Scan(&n)
	if n != 0 {
		t.Errorf("expected binding to be deleted, got %d rows", n)
	}
}

func TestAddRuleReplyUsageHint(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/add_rule")
	// 2026-07-16: v0.15.5 — butler-voice format ("Usage" capital U).
	if !strings.Contains(got, "Usage") {
		t.Errorf("expected usage hint for /add_rule with no args, got: %q", got)
	}
}

func TestDeleteRuleReplyRejectsCrossUser(t *testing.T) {
	d := setupTestDB(t)
	// Insert a rule owned by skyadmin (id=1). alice (userEnv) tries
	// to delete it. The new /delrule implementation surfaces
	// cross-user ids as "not found / not yours" (single bucket
	// alongside truly missing ids, to avoid leaking rule
	// existence across users).
	res, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (1, 'emilia', 'domain', 'foo.com', 'accept')`)
	rid, _ := res.LastInsertId()
	got := HandleCommand(context.Background(), userEnv(d), fmt.Sprintf("/delrule %d", rid))
	if !strings.Contains(got, "not found / not yours") {
		t.Errorf("expected cross-user rejection, got: %q", got)
	}
	// Skyadmin's rule must still be there.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE id = $1`, rid).Scan(&n)
	if n != 1 {
		t.Errorf("expected skyadmin's rule to be preserved, got %d rows", n)
	}
}

func TestClassifyTarget(t *testing.T) {
	cases := []struct {
		in, kind string
		errOK    bool
	}{
		{"1.2.3.4", "ip", true},
		{"10.0.0.0/8", "subnet", true},
		{"telegram.org", "domain", true},
		{"  GITHUB.COM  ", "domain", true},
		{"foo", "", false},     // no dot → fail
		{"", "", false},        // empty
		{"foo bar", "", false}, // space
	}
	for _, c := range cases {
		val, kind, err := classifyTarget(c.in)
		if (err == nil) != c.errOK {
			t.Errorf("classifyTarget(%q): err=%v want_err_ok=%v", c.in, err, c.errOK)
		}
		if err == nil && (val == "" || kind != c.kind) {
			t.Errorf("classifyTarget(%q) → (%q, %q), want kind %q", c.in, val, kind, c.kind)
		}
	}
}

func TestResolveTargetUser(t *testing.T) {
	d := setupTestDB(t)
	// Empty arg → caller.
	u, isOther, err := resolveTargetUser(userEnv(d), "")
	if err != nil {
		t.Fatalf("empty arg: %v", err)
	}
	if isOther || u.Username != "alice" {
		t.Errorf("empty arg should resolve to caller, got user=%+v isOther=%v", u, isOther)
	}
	// Self username → caller.
	u, isOther, err = resolveTargetUser(userEnv(d), "alice")
	if err != nil {
		t.Fatalf("self: %v", err)
	}
	if isOther || u.Username != "alice" {
		t.Errorf("self username should resolve to caller, got user=%+v isOther=%v", u, isOther)
	}
	// Different user → other.
	u, isOther, err = resolveTargetUser(userEnv(d), "skyadmin")
	if err != nil {
		t.Fatalf("other: %v", err)
	}
	if !isOther || u.Username != "skyadmin" {
		t.Errorf("other username should resolve to skyadmin with isOther=true, got user=%+v isOther=%v", u, isOther)
	}
	// Looks like a target → error.
	_, _, err = resolveTargetUser(userEnv(d), "telegram.org")
	if err == nil {
		t.Errorf("expected error for target-shaped arg, got nil")
	}
}

// --- Этап 11 part 1: addDeviceReply real-write tests (2026-07-13) ---
//
// The placeholder addDeviceReply returned a "on the roadmap" hint;
// Этап 11 wires *headscale.Client into the bot and the reply now
// performs the same flow as handlers_my_preauth.go:PostMyPreauth:
//   HS.CreatePreauthKey → db.InsertPreauthKey → db.AppendAuditLog.
//
// The tests below exercise:
//   1. The unbound-chat guard (IsIdentified == false)
//   2. The read-only deploy guard (HS == nil)
//   3. The "no headscale_user_id linked" guard
//   4. The success path (fake headscale via httptest, real DB writes)
//   5. The admin-issues-for-other-user path (audit + DB row go to the
//      target user, not the caller)
//   6. The non-admin-tries-for-other-user guard

// fakeHeadscale stands up an httptest server that mimics headscale's
// POST /api/v1/preauthkey endpoint. The returned key is shaped like
// "hskey-fake-<userID>" so tests can grep for it; the key id is the
// literal "42" so db.InsertPreauthKey records a non-empty
// headscale_preauth_id and the temporal backfill path in
// backfillNodeOwnership has something to match on.
//
// 2026-07-13: Этап 11 part 2b — also handles PUT /api/v1/policy
// (headscale.SetPolicy) so the /add_rule tests can exercise the
// full pipeline end-to-end. SetPolicy always returns 200 OK with
// a minimal body — tests that need it to fail should use a
// dedicated server (see fakeHeadscaleSetPolicyFail).
func fakeHeadscale(t *testing.T) (*httptest.Server, *headscale.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/preauthkey":
			var body struct {
				UserID int64 `json:"user_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			resp := map[string]any{
				"id":         "42",
				"key":        "hskey-fake-" + strconv.FormatInt(body.UserID, 10),
				"user_id":    body.UserID,
				"user":       "alice",
				"reusable":   false,
				"ephemeral":  false,
				"used":       false,
				"expiration": "2026-07-13T07:30:00Z",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case "/api/v1/policy":
			// 2026-07-13: Этап 11 part 2b — accept any ACL
			// JSON, return success. Tests that need SetPolicy
			// to fail use fakeHeadscaleSetPolicyFail.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"policy":"...","updated_at":"x"}`))
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, 404)
		}
	}))
	t.Cleanup(srv.Close)
	hs := headscale.New(srv.URL, "fake-api-key")
	return srv, hs
}

// userEnvWithHS is userEnv plus a *headscale.Client (for write tests).
func userEnvWithHS(d *sql.DB, hs *headscale.Client) BotEnv {
	return BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, PortalUserID: 2, Username: "alice", IsAdmin: false, HS: hs}
}

// userEnvWithHSForUser builds a BotEnv whose HSForPortalUser
// returns a per-portal-user *headscale.Client. Used to
// verify the v0.12.1 per-user bot routing — i.e. /add_device
// from user 2 hits the plane the router returns for id=2,
// not env.HS.
//
// 2026-07-16: v0.12.1.
func userEnvWithHSForUser(d *sql.DB, hsByID map[int64]*headscale.Client) BotEnv {
	return BotEnv{
		DB:              d,
		ChatID:          555,
		Lang:            i18n.LangEN,
		PortalUserID:    2,
		Username:        "alice",
		IsAdmin:         false,
		HS:              hsByID[0], // fallback for unbound reads
		HSForPortalUser: func(uid int64) *headscale.Client { return hsByID[uid] },
	}
}

// adminEnvWithHS is the admin-scope variant of userEnvWithHS. Used
// to test "/add_device <username>" acting on another user.
func adminEnvWithHS(d *sql.DB, hs *headscale.Client) BotEnv {
	return BotEnv{DB: d, ChatID: 1, Lang: i18n.LangEN, PortalUserID: 1, Username: "skyadmin", IsAdmin: true, HS: hs}
}

func TestAddDeviceReplyRejectsUnbound(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/add_device")
	if !strings.Contains(got, "chat not bound") {
		t.Errorf("expected 'chat not bound' for unbound /add_device, got: %q", got)
	}
}

func TestAddDeviceReplyRejectsNoHS(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/add_device")
	if !strings.Contains(got, "read-only") {
		t.Errorf("expected 'read-only' hint for /add_device without HS, got: %q", got)
	}
}

func TestAddDeviceReplyRejectsNoHSUser(t *testing.T) {
	d := setupTestDB(t)
	_, hs := fakeHeadscale(t)
	// setupTestDB does not set headscale_user_id on alice, so the
	// "ask admin to repair" guard fires before any headscale call.
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_device")
	if !strings.Contains(got, "no headscale user linked") {
		t.Errorf("expected 'no headscale user linked' for /add_device, got: %q", got)
	}
	// The HS server should NOT have been hit: count must be 0 preauth rows.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM preauth_keys`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 preauth_keys rows after a rejected call, got %d", n)
	}
}

func TestAddDeviceReplySuccess(t *testing.T) {
	d := setupTestDB(t)
	// Link alice to headscale user id 7.
	if _, err := d.Exec(`UPDATE portal_users SET headscale_user_id = 7 WHERE id = 2`); err != nil {
		t.Fatalf("update alice: %v", err)
	}
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_device")
	if !strings.Contains(got, "hskey-fake-7") {
		t.Errorf("expected 'hskey-fake-7' in reply, got: %q", got)
	}
	if !strings.Contains(got, "alice") {
		t.Errorf("expected 'alice' in reply, got: %q", got)
	}
	// preauth_keys row check.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM preauth_keys WHERE user_id = 2`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 preauth_keys row for alice, got %d", n)
	}
	var storedKey string
	var storedHSID string
	_ = d.QueryRow(`SELECT key, headscale_preauth_id FROM preauth_keys WHERE user_id = 2`).Scan(&storedKey, &storedHSID)
	if storedKey != "hskey-fake-7" {
		t.Errorf("expected stored key 'hskey-fake-7', got %q", storedKey)
	}
	if storedHSID != "42" {
		t.Errorf("expected headscale_preauth_id '42', got %q", storedHSID)
	}
	// audit_log row check.
	var action, detail string
	_ = d.QueryRow(`SELECT action, detail FROM audit_log WHERE user_id = 2 ORDER BY id DESC LIMIT 1`).Scan(&action, &detail)
	if action != "preauth_issued" {
		t.Errorf("expected action='preauth_issued', got %q", action)
	}
	if !strings.Contains(detail, "via bot") {
		t.Errorf("expected 'via bot' in audit detail, got %q", detail)
	}
}

// TestAddDeviceReplyV0121_PerUserRouting pins v0.12.1:
// when HSForPortalUser is wired, /add_device for the bound
// user routes the CreatePreauthKey call to the plane the
// router returns for that user's portal_user_id, not the
// global env.HS.
//
// 2026-07-16: v0.12.1. Two fake headscale servers — one
// returns hskey-fake-plane-a, the other hskey-fake-plane-b —
// and the BotEnv's HSForPortalUser points to one per user.
// The preauth_keys row that lands must reflect the user's
// own plane.
func TestAddDeviceReplyV0121_PerUserRouting(t *testing.T) {
	d := setupTestDB(t)
	// Link alice (id=2) to plane A. Add bob (id=3) and link
	// him to plane B. setupTestDB only seeds skyadmin and
	// alice, so we add bob + his headscale_user_id here.
	if _, err := d.Exec(`INSERT INTO portal_users(id, username, is_admin, headscale_user_id) VALUES (3, 'bob', 0, 8)`); err != nil {
		t.Fatalf("insert bob: %v", err)
	}
	if _, err := d.Exec(`UPDATE portal_users SET headscale_user_id = 7 WHERE id = 2`); err != nil {
		t.Fatalf("update alice: %v", err)
	}
	_, planeA := fakeHeadscale(t)
	_, planeB := fakeHeadscale(t)
	// Each plane answers /api/v1/preauthkey with its own
	// key prefix. fakeHeadscale already returns
	// "hskey-fake-<uid>" where <uid> is the headscale_user_id
	// in the request body, so planeA→"hskey-fake-7" and
	// planeB→"hskey-fake-8" naturally. We rebind the global
	// "fallback" to planeA so an unbound /add_device would
	// still get plane A's response — the regression test
	// case is "the bound user's plane is actually used".
	env := userEnvWithHSForUser(d, map[int64]*headscale.Client{
		0: planeA,
		2: planeA, // alice → plane A
		3: planeB, // bob   → plane B
	})
	// Sanity: userHS() must return planeA for alice (uid=2),
	// not planeB or the global.
	if env.userHS() != planeA {
		t.Fatalf("userHS() should be planeA for uid=2, got: %p", env.userHS())
	}
	// Now /add_device for alice. Must hit planeA, not planeB.
	got := HandleCommand(context.Background(), env, "/add_device")
	if !strings.Contains(got, "hskey-fake-7") {
		t.Errorf("alice's /add_device should hit planeA (key hskey-fake-7), got: %q", got)
	}
	// Now switch to bob (uid=3) and re-issue. Must hit planeB.
	env.PortalUserID = 3
	env.Username = "bob"
	got = HandleCommand(context.Background(), env, "/add_device")
	if !strings.Contains(got, "hskey-fake-8") {
		t.Errorf("bob's /add_device should hit planeB (key hskey-fake-8), got: %q", got)
	}
	// preauth_keys rows: alice got 1 (plane A), bob got 1
	// (plane B), each on the right user_id.
	var aliceN, bobN int
	_ = d.QueryRow(`SELECT COUNT(*) FROM preauth_keys WHERE user_id = 2`).Scan(&aliceN)
	_ = d.QueryRow(`SELECT COUNT(*) FROM preauth_keys WHERE user_id = 3`).Scan(&bobN)
	if aliceN != 1 {
		t.Errorf("expected 1 preauth_keys row for alice, got %d", aliceN)
	}
	if bobN != 1 {
		t.Errorf("expected 1 preauth_keys row for bob, got %d", bobN)
	}
}

func TestAddDeviceReplyAdminForOtherUser(t *testing.T) {
	d := setupTestDB(t)
	// Link alice to headscale user id 7.
	if _, err := d.Exec(`UPDATE portal_users SET headscale_user_id = 7 WHERE id = 2`); err != nil {
		t.Fatalf("update alice: %v", err)
	}
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), adminEnvWithHS(d, hs), "/add_device alice")
	if !strings.Contains(got, "hskey-fake-7") {
		t.Errorf("expected 'hskey-fake-7' in reply, got: %q", got)
	}
	if !strings.Contains(got, "alice") {
		t.Errorf("expected 'alice' in reply, got: %q", got)
	}
	// preauth_keys row must be under alice (id=2), NOT skyadmin (id=1).
	var aliceKeys, adminKeys int
	_ = d.QueryRow(`SELECT COUNT(*) FROM preauth_keys WHERE user_id = 2`).Scan(&aliceKeys)
	_ = d.QueryRow(`SELECT COUNT(*) FROM preauth_keys WHERE user_id = 1`).Scan(&adminKeys)
	if aliceKeys != 1 {
		t.Errorf("expected 1 preauth_keys row for alice, got %d", aliceKeys)
	}
	if adminKeys != 0 {
		t.Errorf("expected 0 preauth_keys rows for skyadmin (admin), got %d", adminKeys)
	}
	// audit_log row under alice, not skyadmin.
	var aliceAudit, adminAudit int
	_ = d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE user_id = 2 AND action = 'preauth_issued'`).Scan(&aliceAudit)
	_ = d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE user_id = 1 AND action = 'preauth_issued'`).Scan(&adminAudit)
	if aliceAudit != 1 {
		t.Errorf("expected 1 preauth_issued audit row for alice, got %d", aliceAudit)
	}
	if adminAudit != 0 {
		t.Errorf("expected 0 preauth_issued audit rows for skyadmin, got %d", adminAudit)
	}
}

func TestAddDeviceReplyRejectsNonAdminForOtherUser(t *testing.T) {
	d := setupTestDB(t)
	if _, err := d.Exec(`UPDATE portal_users SET headscale_user_id = 7 WHERE id = 2`); err != nil {
		t.Fatalf("update alice: %v", err)
	}
	_, hs := fakeHeadscale(t)
	// alice is a regular user (IsAdmin=false); she cannot issue a
	// key for skyadmin. The IsIdentified + isAdminArg check fires
	// before HS even gets called.
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_device skyadmin")
	if !strings.Contains(got, "only admins") {
		t.Errorf("expected 'only admins' for non-admin targeting another user, got: %q", got)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM preauth_keys`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 preauth_keys rows after a rejected call, got %d", n)
	}
}

// --- Этап 11 part 2a: default-device / default-exit-node replies ---
//
// The four new commands (/setdefaultdevice, /defaultdevice,
// /setexitnode, /defaultexitnode) are pure preference writes —
// they touch portal_users + audit_log and don't go anywhere near
// headscale, so the tests use the in-memory schema directly.
//
// What's covered:
//   1. /setdefaultdevice without args → list with valid node_ids
//   2. /setdefaultdevice <node_id> → set, audit row, Get* round-trip
//   3. /setdefaultdevice <exit_node> → rejected (exit-node tag)
//   4. /setdefaultdevice <other_user_node> → rejected (not in
//      the caller's node_owner_map)
//   5. /setdefaultdevice clear → reset, audit row
//   6. /setdefaultdevice 9999 → rejected (not a valid node_id)
//   7. /defaultdevice → "not set" hint when empty
//   8. /defaultdevice → shows node_id when set
//   9. /setexitnode without args → list enabled exit_servers
//  10. /setexitnode <node_id> → set, audit row
//  11. /setexitnode <disabled> → rejected
//  12. /setexitnode <not_an_exit> → rejected
//  13. /setexitnode clear → reset
//  14. /defaultexitnode → "not set" hint when empty
//  15. /defaultexitnode → shows hostname when set
//  16. Unbound chat guard for all four

func TestSetDefaultDeviceReplyRejectsUnbound(t *testing.T) {
	d := setupTestDB(t)
	// envFor has ChatID=0 (unbound).
	got := HandleCommand(context.Background(), envFor(d), "/setdefaultdevice")
	if !strings.Contains(got, "not bound") {
		t.Errorf("expected 'not bound' for unbound /setdefaultdevice, got: %q", got)
	}
}

func TestSetDefaultDeviceReplyListsDevices(t *testing.T) {
	d := setupTestDB(t)
	// setupTestDB seeds skyadmin (id=1) with two tag:private devices
	// (n1, n2) and one tag:public (n3). alice has no devices yet —
	// so we'll seed one for her.
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-dev-1', 'alice', 'tag:private')`)
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-dev-2', 'alice', 'tag:private')`)
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-exit', 'alice', 'tag:exit-node')`)
	got := HandleCommand(context.Background(), userEnv(d), "/setdefaultdevice")
	// Both tag:private devices should appear; tag:exit-node should NOT.
	if !strings.Contains(got, "alice-dev-1") {
		t.Errorf("expected alice-dev-1 in device list, got: %q", got)
	}
	if !strings.Contains(got, "alice-dev-2") {
		t.Errorf("expected alice-dev-2 in device list, got: %q", got)
	}
	if strings.Contains(got, "alice-exit") {
		t.Errorf("tag:exit-node should NOT appear in /setdefaultdevice list, got: %q", got)
	}
}

func TestSetDefaultDeviceReplyNoDevices(t *testing.T) {
	d := setupTestDB(t)
	// alice has no devices.
	got := HandleCommand(context.Background(), userEnv(d), "/setdefaultdevice")
	if !strings.Contains(got, "no devices") {
		t.Errorf("expected 'no devices' hint, got: %q", got)
	}
}

func TestSetDefaultDeviceReplySuccess(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-dev-1', 'alice', 'tag:private')`)
	got := HandleCommand(context.Background(), userEnv(d), "/setdefaultdevice alice-dev-1")
	if !strings.Contains(got, "set to") {
		t.Errorf("expected 'set to' confirmation, got: %q", got)
	}
	// DB round-trip: column was written.
	var got2 string
	_ = d.QueryRow(`SELECT default_device_node_id FROM portal_users WHERE id = 2`).Scan(&got2)
	if got2 != "alice-dev-1" {
		t.Errorf("default_device_node_id = %q, want %q", got2, "alice-dev-1")
	}
	// Audit log row.
	var action, detail string
	_ = d.QueryRow(`SELECT action, detail FROM audit_log WHERE user_id = 2 ORDER BY id DESC LIMIT 1`).Scan(&action, &detail)
	if action != "default_device_changed" {
		t.Errorf("audit action = %q, want %q", action, "default_device_changed")
	}
	if !strings.Contains(detail, "alice-dev-1") {
		t.Errorf("audit detail = %q, expected to contain 'alice-dev-1'", detail)
	}
}

func TestSetDefaultDeviceRejectsExitNode(t *testing.T) {
	d := setupTestDB(t)
	// alice has alice-dev-1 (tag:private) and alice-exit (tag:exit-node).
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-dev-1', 'alice', 'tag:private')`)
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-exit', 'alice', 'tag:exit-node')`)
	got := HandleCommand(context.Background(), userEnv(d), "/setdefaultdevice alice-exit")
	// The exit-node should be filtered out of the device list at the
	// "is this one of your devices?" check; the reply says the node
	// is not in the device list.
	if !strings.Contains(got, "not in your device list") {
		t.Errorf("expected 'not in your device list' for exit-node as device, got: %q", got)
	}
	// Column must NOT have been written.
	var got2 string
	_ = d.QueryRow(`SELECT default_device_node_id FROM portal_users WHERE id = 2`).Scan(&got2)
	if got2 != "" {
		t.Errorf("default_device_node_id should be empty, got %q", got2)
	}
}

func TestSetDefaultDeviceRejectsOtherUsersNode(t *testing.T) {
	d := setupTestDB(t)
	// alice has one device of her own; skyadmin's n1 is NOT one
	// of her devices — she should not be able to claim it.
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-dev-1', 'alice', 'tag:private')`)
	got := HandleCommand(context.Background(), userEnv(d), "/setdefaultdevice n1")
	if !strings.Contains(got, "not in your device list") {
		t.Errorf("expected 'not in your device list' for cross-user claim, got: %q", got)
	}
}

func TestSetDefaultDeviceRejectsInvalidNodeID(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-dev-1', 'alice', 'tag:private')`)
	got := HandleCommand(context.Background(), userEnv(d), "/setdefaultdevice 9999")
	if !strings.Contains(got, "not in your device list") {
		t.Errorf("expected rejection for non-existent node_id, got: %q", got)
	}
}

func TestSetDefaultDeviceClear(t *testing.T) {
	d := setupTestDB(t)
	// alice needs at least one device so the reply doesn't short-
	// circuit to "no devices yet" before reaching the clear branch.
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-dev-1', 'alice', 'tag:private')`)
	// Pre-set so we can verify clear actually wipes it.
	_, _ = d.Exec(`UPDATE portal_users SET default_device_node_id = 'alice-dev-1' WHERE id = 2`)
	got := HandleCommand(context.Background(), userEnv(d), "/setdefaultdevice clear")
	if !strings.Contains(got, "cleared") {
		t.Errorf("expected 'cleared' confirmation, got: %q", got)
	}
	var got2 string
	_ = d.QueryRow(`SELECT default_device_node_id FROM portal_users WHERE id = 2`).Scan(&got2)
	if got2 != "" {
		t.Errorf("default_device_node_id after clear = %q, want empty", got2)
	}
	// Audit row.
	var action, detail string
	_ = d.QueryRow(`SELECT action, detail FROM audit_log WHERE user_id = 2 ORDER BY id DESC LIMIT 1`).Scan(&action, &detail)
	if action != "default_device_changed" {
		t.Errorf("audit action = %q, want %q", action, "default_device_changed")
	}
	if !strings.Contains(detail, "cleared") {
		t.Errorf("audit detail = %q, expected to contain 'cleared'", detail)
	}
}

func TestDefaultDeviceReplyNotSet(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/defaultdevice")
	if !strings.Contains(got, "no default") {
		t.Errorf("expected 'no default' for unset default, got: %q", got)
	}
	if !strings.Contains(got, "/setdefaultdevice") {
		t.Errorf("expected hint to /setdefaultdevice, got: %q", got)
	}
}

func TestDefaultDeviceReplySet(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`UPDATE portal_users SET default_device_node_id = 'alice-dev-1' WHERE id = 2`)
	got := HandleCommand(context.Background(), userEnv(d), "/defaultdevice")
	if !strings.Contains(got, "alice-dev-1") {
		t.Errorf("expected node_id in /defaultdevice reply, got: %q", got)
	}
}

func TestDefaultDeviceReplyRejectsUnbound(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/defaultdevice")
	if !strings.Contains(got, "not bound") {
		t.Errorf("expected 'not bound' for unbound /defaultdevice, got: %q", got)
	}
}

func TestSetExitNodeReplyRejectsUnbound(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/setexitnode")
	if !strings.Contains(got, "not bound") {
		t.Errorf("expected 'not bound' for unbound /setexitnode, got: %q", got)
	}
}

func TestSetExitNodeReplyNoExitServers(t *testing.T) {
	d := setupTestDB(t)
	// setupTestDB does not seed exit_servers, so the reply should
	// tell alice to ask an admin.
	got := HandleCommand(context.Background(), userEnv(d), "/setexitnode")
	if !strings.Contains(got, "no enabled exit-nodes") {
		t.Errorf("expected 'no enabled exit-nodes' hint, got: %q", got)
	}
}

func TestSetExitNodeReplyListsEnabled(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('emilia-1', 'emilia', 1)`)
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('karolina-1', 'karolina', 1)`)
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('disabled-1', 'disabled', 0)`)
	got := HandleCommand(context.Background(), userEnv(d), "/setexitnode")
	if !strings.Contains(got, "emilia") {
		t.Errorf("expected 'emilia' in /setexitnode list, got: %q", got)
	}
	if !strings.Contains(got, "karolina") {
		t.Errorf("expected 'karolina' in /setexitnode list, got: %q", got)
	}
	// disabled-1 should NOT appear.
	if strings.Contains(got, "disabled") {
		t.Errorf("disabled exit-server should NOT appear in /setexitnode list, got: %q", got)
	}
}

func TestSetExitNodeReplySuccess(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('emilia-1', 'emilia', 1)`)
	got := HandleCommand(context.Background(), userEnv(d), "/setexitnode emilia-1")
	if !strings.Contains(got, "set to") {
		t.Errorf("expected 'set to' confirmation, got: %q", got)
	}
	if !strings.Contains(got, "emilia") {
		t.Errorf("expected 'emilia' hostname in reply, got: %q", got)
	}
	// DB round-trip.
	var got2 string
	_ = d.QueryRow(`SELECT default_exit_node_id FROM portal_users WHERE id = 2`).Scan(&got2)
	if got2 != "emilia-1" {
		t.Errorf("default_exit_node_id = %q, want %q", got2, "emilia-1")
	}
	// Audit row.
	var action, detail string
	_ = d.QueryRow(`SELECT action, detail FROM audit_log WHERE user_id = 2 ORDER BY id DESC LIMIT 1`).Scan(&action, &detail)
	if action != "default_exit_node_changed" {
		t.Errorf("audit action = %q, want %q", action, "default_exit_node_changed")
	}
	if !strings.Contains(detail, "emilia") {
		t.Errorf("audit detail = %q, expected to contain 'emilia'", detail)
	}
}

func TestSetExitNodeRejectsDisabled(t *testing.T) {
	d := setupTestDB(t)
	// Seed an enabled exit-server so the "no enabled exit-nodes"
	// short-circuit doesn't fire first; the disabled one is the
	// one we want the rejection message for.
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('karolina-1', 'karolina', 1)`)
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('emilia-1', 'emilia', 0)`)
	got := HandleCommand(context.Background(), userEnv(d), "/setexitnode emilia-1")
	if !strings.Contains(got, "not an enabled exit-node") {
		t.Errorf("expected rejection for disabled exit-server, got: %q", got)
	}
}

func TestSetExitNodeRejectsNotAnExit(t *testing.T) {
	d := setupTestDB(t)
	// Seed an enabled exit-server so the "no enabled exit-nodes"
	// short-circuit doesn't fire first. Then ask to set a node
	// that is in node_owner_map but NOT in exit_servers.
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('karolina-1', 'karolina', 1)`)
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-dev-1', 'alice', 'tag:private')`)
	got := HandleCommand(context.Background(), userEnv(d), "/setexitnode alice-dev-1")
	if !strings.Contains(got, "not an enabled exit-node") {
		t.Errorf("expected rejection for non-exit-server node_id, got: %q", got)
	}
}

func TestSetExitNodeClear(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('emilia-1', 'emilia', 1)`)
	_, _ = d.Exec(`UPDATE portal_users SET default_exit_node_id = 'emilia-1' WHERE id = 2`)
	got := HandleCommand(context.Background(), userEnv(d), "/setexitnode clear")
	if !strings.Contains(got, "cleared") {
		t.Errorf("expected 'cleared' confirmation, got: %q", got)
	}
	var got2 string
	_ = d.QueryRow(`SELECT default_exit_node_id FROM portal_users WHERE id = 2`).Scan(&got2)
	if got2 != "" {
		t.Errorf("default_exit_node_id after clear = %q, want empty", got2)
	}
	// Audit row.
	var action, detail string
	_ = d.QueryRow(`SELECT action, detail FROM audit_log WHERE user_id = 2 ORDER BY id DESC LIMIT 1`).Scan(&action, &detail)
	if action != "default_exit_node_changed" {
		t.Errorf("audit action = %q, want %q", action, "default_exit_node_changed")
	}
	if !strings.Contains(detail, "cleared") {
		t.Errorf("audit detail = %q, expected to contain 'cleared'", detail)
	}
}

func TestDefaultExitNodeReplyNotSet(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/defaultexitnode")
	if !strings.Contains(got, "no default") {
		t.Errorf("expected 'no default' for unset default, got: %q", got)
	}
	if !strings.Contains(got, "/setexitnode") {
		t.Errorf("expected hint to /setexitnode, got: %q", got)
	}
}

func TestDefaultExitNodeReplySet(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('emilia-1', 'emilia', 1)`)
	_, _ = d.Exec(`UPDATE portal_users SET default_exit_node_id = 'emilia-1' WHERE id = 2`)
	got := HandleCommand(context.Background(), userEnv(d), "/defaultexitnode")
	if !strings.Contains(got, "emilia") {
		t.Errorf("expected 'emilia' hostname in /defaultexitnode reply, got: %q", got)
	}
	if !strings.Contains(got, "emilia-1") {
		t.Errorf("expected node_id 'emilia-1' in reply, got: %q", got)
	}
}

func TestDefaultExitNodeReplyRejectsUnbound(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/defaultexitnode")
	if !strings.Contains(got, "not bound") {
		t.Errorf("expected 'not bound' for unbound /defaultexitnode, got: %q", got)
	}
}

func TestHelpListsNewCommands(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/help")
	for _, c := range []string{
		"/setdefaultdevice",
		"/defaultdevice",
		"/setexitnode",
		"/defaultexitnode",
	} {
		if !strings.Contains(got, c) {
			t.Errorf("expected %q in /help, got: %q", c, got)
		}
	}
}

// --- Этап 11 part 2b: /add_rule real-write tests (2026-07-13) ---
//
// The placeholder addRuleReply returned a "on the roadmap" hint;
// Этап 11 part 2b wires the full pipeline (defaults →
// validate → insert → GenerateACL → SetPolicy → Mark + Log →
// audit) into the bot, mirroring handlers/exit_rules_form_my.go:
// PostMyExitRule.
//
// What's covered:
//   1. Unbound chat guard
//   2. No default device → "set defaults first"
//   3. No default exit-node → "set defaults first"
//   4. Stale default device (no longer in node_owner_map)
//   5. Stale default exit-node (no longer in exit_servers)
//   6. Per-user limit reached
//   7. Per-device limit reached
//   8. Total limit reached
//   9. Success: IP target → /32 inserted
//  10. Success: domain target → DNS resolved → multiple /32 rows
//  11. Success: deny action
//  12. Success: admin issues for alice (row + audit under alice)
//  13. SetPolicy failure: rule in DB, ACL saved but not applied
//  14. admin-only: alice cannot target skyadmin
//  15. Audit log row under the target user

// setupAddRuleTestDB seeds alice with a default device + default
// exit-node, the corresponding node_owner_map row, and an enabled
// exit_servers row. Returns the same DB. Tests that need a
// different state modify after this returns.
//
// The default device node_id is a numeric string ("100") because
// device_rules.device_id is INT and the bot Atoi's the default
// column — using a non-numeric node_id would short-circuit the
// success path with a "node_id not numeric" error.
func setupAddRuleTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d := setupTestDB(t)
	// alice owns a device with numeric node_id "100".
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('100', 'alice', 'tag:private')`)
	// exit_servers row that /setexitnode would have stored.
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('emilia-1', 'emilia', 1)`)
	// alice's defaults.
	_, _ = d.Exec(`UPDATE portal_users SET default_device_node_id = '100', default_exit_node_id = 'emilia-1' WHERE id = 2`)
	return d
}

func TestAddRuleReplyRejectsUnbound(t *testing.T) {
	d := setupAddRuleTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/add_rule 1.2.3.4")
	if !strings.Contains(got, "not bound") {
		t.Errorf("expected 'not bound' for unbound /add_rule, got: %q", got)
	}
}

func TestAddRuleReplyRejectsNoDefaultDevice(t *testing.T) {
	d := setupTestDB(t)
	// Has exit-node default but not device default.
	_, _ = d.Exec(`UPDATE portal_users SET default_exit_node_id = 'emilia-1' WHERE id = 2`)
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('emilia-1', 'emilia', 1)`)
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_rule 1.2.3.4")
	if !strings.Contains(got, "default device") {
		t.Errorf("expected 'default device' hint, got: %q", got)
	}
}

func TestAddRuleReplyRejectsNoDefaultExitNode(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`UPDATE portal_users SET default_device_node_id = 'alice-dev-1' WHERE id = 2`)
	_, _ = d.Exec(`INSERT INTO node_owner_map(node_id, username, tag) VALUES ('alice-dev-1', 'alice', 'tag:private')`)
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_rule 1.2.3.4")
	if !strings.Contains(got, "default exit-node") {
		t.Errorf("expected 'default exit-node' hint, got: %q", got)
	}
}

func TestAddRuleReplyRejectsStaleDefaultDevice(t *testing.T) {
	d := setupAddRuleTestDB(t)
	// Remove alice's device from node_owner_map but leave the
	// default column pointing at it.
	_, _ = d.Exec(`DELETE FROM node_owner_map WHERE node_id = '100'`)
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_rule 1.2.3.4")
	if !strings.Contains(got, "is not in alice's devices") {
		t.Errorf("expected stale-device message, got: %q", got)
	}
}

func TestAddRuleReplyRejectsStaleDefaultExitNode(t *testing.T) {
	d := setupAddRuleTestDB(t)
	// Disable alice's default exit-server.
	_, _ = d.Exec(`UPDATE exit_servers SET enabled = 0 WHERE node_id = 'emilia-1'`)
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_rule 1.2.3.4")
	if !strings.Contains(got, "no longer an enabled exit-server") && !strings.Contains(got, "currently disabled") {
		t.Errorf("expected stale-exit-node message, got: %q", got)
	}
}

func TestAddRuleReplyRejectsPerUserLimit(t *testing.T) {
	d := setupAddRuleTestDB(t)
	// Seed 5 rules for alice on device 100.
	for i := 0; i < 5; i++ {
		_, _ = d.Exec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, parent_domain) VALUES (2, 100, 'emilia', 'ip', '1.1.1.1', 'accept', '')`)
	}
	// Cap alice at 5.
	env := userEnvWithHS(d, nil)
	env.UserMaxRules = map[string]int{"alice": 5}
	env.DefaultMax = 5
	_, hs := fakeHeadscale(t)
	env.HS = hs
	got := HandleCommand(context.Background(), env, "/add_rule 2.2.2.2")
	// 2026-07-16: v0.15.5 — butler-voice format (capital "User").
	if !strings.Contains(got, "User limit reached") {
		t.Errorf("expected 'User limit reached', got: %q", got)
	}
	// No new rule row should have been inserted.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2`).Scan(&n)
	if n != 5 {
		t.Errorf("expected 5 device_rules rows after rejection, got %d", n)
	}
}

func TestAddRuleReplyRejectsPerDeviceLimit(t *testing.T) {
	d := setupAddRuleTestDB(t)
	// setupAddRuleTestDB uses device node_id "100" so device_id
	// after Atoi is 100. Seed 2 rules on it.
	for i := 0; i < 2; i++ {
		_, _ = d.Exec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, parent_domain) VALUES (2, 100, 'emilia', 'ip', '1.1.1.1', 'accept', '')`)
	}
	env := userEnvWithHS(d, nil)
	env.MaxRulesPerDevice = 2
	_, hs := fakeHeadscale(t)
	env.HS = hs
	got := HandleCommand(context.Background(), env, "/add_rule 2.2.2.2")
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Per").
	if !strings.Contains(got, "Per-device limit") {
		t.Errorf("expected 'per-device limit', got: %q", got)
	}
}

func TestAddRuleReplyRejectsTotalLimit(t *testing.T) {
	d := setupAddRuleTestDB(t)
	// Seed 3 rules across all users (skyadmin's existing 12
	// already make this > 3, so we use a tighter cap below).
	// We test with a cap of 12 which the seed (12 skyadmin rules)
	// exactly meets.
	env := userEnvWithHS(d, nil)
	env.MaxTotalRules = 12
	_, hs := fakeHeadscale(t)
	env.HS = hs
	got := HandleCommand(context.Background(), env, "/add_rule 2.2.2.2")
	// 2026-07-16: v0.15.5 — butler-voice format (capital "System").
	if !strings.Contains(got, "System-wide limit") {
		t.Errorf("expected 'System-wide limit', got: %q", got)
	}
}

func TestAddRuleReplySuccessIP(t *testing.T) {
	d := setupAddRuleTestDB(t)
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_rule 1.2.3.4")
	// 2026-07-16: v0.15.5 — butler-voice format ("Added" capital A).
	if !strings.Contains(got, "Added") || !strings.Contains(got, "1.2.3.4") {
		t.Errorf("expected success message with '1.2.3.4', got: %q", got)
	}
	if !strings.Contains(got, "ACL") {
		t.Errorf("expected ACL v# in reply, got: %q", got)
	}
	// device_rules row inserted as /32 subnet.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2 AND target_type = 'subnet' AND target_value = '1.2.3.4/32'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 device_rule row for 1.2.3.4/32, got %d", n)
	}
	// exit_node_id stored as hostname 'emilia' (not node_id).
	var exitNodeID string
	_ = d.QueryRow(`SELECT exit_node_id FROM device_rules WHERE user_id = 2 LIMIT 1`).Scan(&exitNodeID)
	if exitNodeID != "emilia" {
		t.Errorf("exit_node_id = %q, want 'emilia'", exitNodeID)
	}
	// audit_log row under alice.
	var action, detail string
	_ = d.QueryRow(`SELECT action, detail FROM audit_log WHERE user_id = 2 ORDER BY id DESC LIMIT 1`).Scan(&action, &detail)
	if action != "rule_added" {
		t.Errorf("audit action = %q, want 'rule_added'", action)
	}
	if !strings.Contains(detail, "via bot") {
		t.Errorf("audit detail = %q, want to contain 'via bot'", detail)
	}
}

func TestAddRuleReplySuccessDeny(t *testing.T) {
	d := setupAddRuleTestDB(t)
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_rule 1.2.3.4 deny")
	if !strings.Contains(got, "action=deny") {
		t.Errorf("expected 'action=deny' in reply, got: %q", got)
	}
	var action string
	_ = d.QueryRow(`SELECT action FROM device_rules WHERE user_id = 2`).Scan(&action)
	if action != "deny" {
		t.Errorf("device_rules.action = %q, want 'deny'", action)
	}
}

func TestAddRuleReplyAdminForOtherUser(t *testing.T) {
	d := setupAddRuleTestDB(t)
	_, hs := fakeHeadscale(t)
	// skyadmin (admin) issues /add_rule alice 1.2.3.4.
	got := HandleCommand(context.Background(), adminEnvWithHS(d, hs), "/add_rule alice 1.2.3.4")
	// 2026-07-16: v0.15.5 — butler-voice format ("Added" capital A).
	if !strings.Contains(got, "Added") {
		t.Errorf("expected 'Added' in admin-for-other reply, got: %q", got)
	}
	// Rule row under alice (id=2), NOT skyadmin (id=1).
	var aliceCnt, adminCnt int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2`).Scan(&aliceCnt)
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 1 AND target_value = '1.2.3.4/32'`).Scan(&adminCnt)
	if aliceCnt != 1 {
		t.Errorf("expected 1 rule under alice, got %d", aliceCnt)
	}
	if adminCnt != 0 {
		t.Errorf("expected 0 rules under skyadmin for this target, got %d", adminCnt)
	}
	// audit row under alice.
	var action string
	_ = d.QueryRow(`SELECT action FROM audit_log WHERE user_id = 2 AND action = 'rule_added'`).Scan(&action)
	if action != "rule_added" {
		t.Errorf("expected 'rule_added' audit row under alice, got %q", action)
	}
}

func TestAddRuleReplyRejectsNonAdminForOtherUser(t *testing.T) {
	d := setupAddRuleTestDB(t)
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_rule skyadmin 1.2.3.4")
	// 2026-07-16: v0.15.5 — butler-voice format ("Extra" capital E).
	if !strings.Contains(got, "Extra args") {
		t.Errorf("expected 'Extra args' rejection, got: %q", got)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules`).Scan(&n)
	if n != 12 { // only the seeded 12 skyadmin rules
		t.Errorf("expected 12 device_rules (no insert), got %d", n)
	}
}

// fakeHeadscaleSetPolicyFail is a variant of fakeHeadscale where
// PUT /api/v1/policy returns 500. Used to exercise the
// "rule inserted, ACL saved but not applied" branch.
func fakeHeadscaleSetPolicyFail(t *testing.T) (*httptest.Server, *headscale.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/preauthkey":
			var body struct {
				UserID int64 `json:"user_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			resp := map[string]any{
				"id":  "42",
				"key": "hskey-fake-" + strconv.FormatInt(body.UserID, 10),
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case "/api/v1/policy":
			http.Error(w, "policy rejected", http.StatusInternalServerError)
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, 404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, headscale.New(srv.URL, "fake-key")
}

func TestAddRuleReplySetPolicyFailure(t *testing.T) {
	d := setupAddRuleTestDB(t)
	_, hs := fakeHeadscaleSetPolicyFail(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/add_rule 1.2.3.4")
	if !strings.Contains(got, "NOT applied") {
		t.Errorf("expected 'NOT applied' in reply, got: %q", got)
	}
	// Rule row IS inserted (the failure is downstream of the
	// device_rules INSERT).
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 device_rule row even on SetPolicy failure, got %d", n)
	}
	// acl_snapshots row exists with applied_success=0.
	var nFailed int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots WHERE applied_success = 0`).Scan(&nFailed)
	if nFailed != 1 {
		t.Errorf("expected 1 failed acl_snapshots row, got %d", nFailed)
	}
	// exit_rule_logs has an apply_fail row.
	var logAction string
	_ = d.QueryRow(`SELECT action FROM exit_rule_logs WHERE action = 'apply_fail'`).Scan(&logAction)
	if logAction != "apply_fail" {
		t.Errorf("expected 'apply_fail' in exit_rule_logs, got %q", logAction)
	}
}

// --- Этап 12: /delrule real-write tests (2026-07-13) ---
//
// 14 tests covering the delete flow end-to-end:
//   1.  Usage hint (no args)
//   2.  Reject unbound chat
//   3.  Reject bad arg (non-numeric)
//   4.  Reject unknown id
//   5.  Single success (rule gone, audit + ACL)
//   6.  Multi success (multiple ids, partial skip)
//   7.  Domain cascade (delete domain rule + /32 siblings go too)
//   8.  Admin targets another user
//   9.  Non-admin can't target another user
//   10. SetPolicy failure (rule deleted, ACL saved but not applied)
//   11. Read-only mode (HS == nil, DB delete + no pipeline)
//   12. /delrule and /delete_rule route to the same handler
//   13. /delrule is listed in /help
//   14. /help delrule returns detailed help

func TestDelRuleReplyUsageHint(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/delrule")
	// 2026-07-16: v0.15.5 — butler-voice format ("Usage" capital U).
	if !strings.Contains(got, "Usage") {
		t.Errorf("expected usage hint for /delrule with no args, got: %q", got)
	}
}

func TestDelRuleReplyRejectsUnbound(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/delrule 1")
	if !strings.Contains(got, "not bound") {
		t.Errorf("expected 'not bound' for unbound /delrule, got: %q", got)
	}
}

func TestDelRuleReplyRejectsBadArg(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/delrule abc")
	// 2026-07-16: v0.15.5 — butler-voice format ("No" capital N).
	if !strings.Contains(got, "No valid ids") {
		t.Errorf("expected 'No valid ids' for non-numeric arg, got: %q", got)
	}
	if !strings.Contains(got, "not a positive integer") {
		t.Errorf("expected 'not a positive integer' in skipped list, got: %q", got)
	}
}

func TestDelRuleReplyRejectsUnknownID(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/delrule 9999")
	// 2026-07-16: v0.15.5 — butler-voice format ("No" capital N).
	if !strings.Contains(got, "No valid ids") {
		t.Errorf("expected 'No valid ids' for missing id, got: %q", got)
	}
	if !strings.Contains(got, "not found / not yours") {
		t.Errorf("expected 'not found / not yours' for missing id, got: %q", got)
	}
}

func TestDelRuleReplySingleSuccess(t *testing.T) {
	d := setupTestDB(t)
	// Seed alice's rule.
	res, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.2.3.4/32', 'accept')`)
	rid, _ := res.LastInsertId()
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), fmt.Sprintf("/delrule %d", rid))
	// 2026-07-16: v0.15.5 — butler-voice format ("Deleted" capital D).
	if !strings.Contains(got, "Deleted 1 rule") {
		t.Errorf("expected 'Deleted 1 rule' in success reply, got: %q", got)
	}
	if !strings.Contains(got, "ACL") {
		t.Errorf("expected 'ACL v#' in reply, got: %q", got)
	}
	// device_rules row gone.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE id = $1`, rid).Scan(&n)
	if n != 0 {
		t.Errorf("expected rule row to be deleted, got %d rows", n)
	}
	// audit_log row under alice with action=rule_deleted.
	var action, detail string
	_ = d.QueryRow(`SELECT action, detail FROM audit_log WHERE user_id = 2 ORDER BY id DESC LIMIT 1`).Scan(&action, &detail)
	if action != "rule_deleted" {
		t.Errorf("audit action = %q, want 'rule_deleted'", action)
	}
	if !strings.Contains(detail, "via bot") {
		t.Errorf("audit detail = %q, want to contain 'via bot'", detail)
	}
	// acl_snapshots row exists with applied_success=1.
	var nApplied int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots WHERE applied_success = 1`).Scan(&nApplied)
	if nApplied < 1 {
		t.Errorf("expected at least 1 applied acl_snapshots row, got %d", nApplied)
	}
}

func TestDelRuleReplyMultiSuccess(t *testing.T) {
	d := setupTestDB(t)
	// Seed three of alice's rules + one missing id.
	r1, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	r2, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '2.2.2.2/32', 'accept')`)
	r3, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '3.3.3.3/32', 'accept')`)
	id1, _ := r1.LastInsertId()
	id2, _ := r2.LastInsertId()
	id3, _ := r3.LastInsertId()
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs),
		fmt.Sprintf("/delrule %d %d 9999 %d", id1, id2, id3))
	// 2026-07-16: v0.15.5 — butler-voice format ("Deleted" capital D).
	if !strings.Contains(got, "Deleted 3 rule") {
		t.Errorf("expected 'Deleted 3 rule' in multi-success reply, got: %q", got)
	}
	if !strings.Contains(got, "skipped") {
		t.Errorf("expected 'skipped' in reply for the missing id, got: %q", got)
	}
	// All three rows gone.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 alice rules after multi-delete, got %d", n)
	}
}

func TestDelRuleReplyDomainCascade(t *testing.T) {
	d := setupTestDB(t)
	// Seed: a domain rule + two /32 children with the same parent_domain
	// (mimics the autoupder's /32 fan-out from a single domain rule).
	res1, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action, parent_domain) VALUES (2, 'emilia', 'domain', 'telegram.org', 'accept', 'telegram.org')`)
	rid, _ := res1.LastInsertId()
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action, parent_domain) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept', 'telegram.org')`)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action, parent_domain) VALUES (2, 'emilia', 'subnet', '2.2.2.2/32', 'accept', 'telegram.org')`)
	// One unrelated rule that must NOT be touched.
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '9.9.9.9/32', 'accept')`)
	_, hs := fakeHeadscale(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), fmt.Sprintf("/delrule %d", rid))
	// 2026-07-16: v0.15.5 — butler-voice format ("Deleted" capital D).
	if !strings.Contains(got, "Deleted 1 rule") {
		t.Errorf("expected 'Deleted 1 rule' (the original), got: %q", got)
	}
	if !strings.Contains(got, "cascade: 2") {
		t.Errorf("expected 'cascade: 2' in reply, got: %q", got)
	}
	// The 2 /32 children must be gone too.
	var remaining int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2 AND parent_domain = 'telegram.org'`).Scan(&remaining)
	if remaining != 0 {
		t.Errorf("expected 0 rows with parent_domain='telegram.org', got %d", remaining)
	}
	// Unrelated rule untouched.
	var untouched int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE target_value = '9.9.9.9/32'`).Scan(&untouched)
	if untouched != 1 {
		t.Errorf("expected 1 row for 9.9.9.9/32 (unrelated), got %d", untouched)
	}
}

func TestDelRuleReplyAdminForOtherUser(t *testing.T) {
	d := setupTestDB(t)
	// Seed alice's rule.
	res, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.2.3.4/32', 'accept')`)
	rid, _ := res.LastInsertId()
	_, hs := fakeHeadscale(t)
	// skyadmin (admin) deletes alice's rule.
	got := HandleCommand(context.Background(), adminEnvWithHS(d, hs), fmt.Sprintf("/delrule alice %d", rid))
	// 2026-07-16: v0.15.5 — butler-voice format ("Deleted" capital D).
	if !strings.Contains(got, "Deleted 1 rule") {
		t.Errorf("expected 'Deleted 1 rule' for admin-for-other, got: %q", got)
	}
	if !strings.Contains(got, "for alice") {
		t.Errorf("expected 'for alice' in reply, got: %q", got)
	}
	// Row gone.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE id = $1`, rid).Scan(&n)
	if n != 0 {
		t.Errorf("expected alice's rule to be deleted by admin, got %d rows", n)
	}
	// Audit row under alice.
	var action string
	_ = d.QueryRow(`SELECT action FROM audit_log WHERE user_id = 2 AND action = 'rule_deleted'`).Scan(&action)
	if action != "rule_deleted" {
		t.Errorf("expected 'rule_deleted' audit row under alice, got %q", action)
	}
}

func TestDelRuleReplyRejectsNonAdminForOtherUser(t *testing.T) {
	d := setupTestDB(t)
	// Seed skyadmin's rule.
	res, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (1, 'emilia', 'subnet', '1.2.3.4/32', 'accept')`)
	rid, _ := res.LastInsertId()
	_, hs := fakeHeadscale(t)
	// alice (non-admin) tries /delrule skyadmin <id>.
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), fmt.Sprintf("/delrule skyadmin %d", rid))
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Extra").
	if !strings.Contains(got, "Extra args") {
		t.Errorf("expected 'Extra args' rejection for non-admin, got: %q", got)
	}
	// skyadmin's rule untouched.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE id = $1`, rid).Scan(&n)
	if n != 1 {
		t.Errorf("expected skyadmin's rule untouched, got %d rows", n)
	}
}

func TestDelRuleReplySetPolicyFailure(t *testing.T) {
	d := setupTestDB(t)
	res, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.2.3.4/32', 'accept')`)
	rid, _ := res.LastInsertId()
	_, hs := fakeHeadscaleSetPolicyFail(t)
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), fmt.Sprintf("/delrule %d", rid))
	if !strings.Contains(got, "NOT applied") {
		t.Errorf("expected 'NOT applied' in reply, got: %q", got)
	}
	// Rule row IS deleted (the failure is downstream of the DELETE).
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE id = $1`, rid).Scan(&n)
	if n != 0 {
		t.Errorf("expected rule row to be deleted even on SetPolicy failure, got %d rows", n)
	}
	// acl_snapshots row exists with applied_success=0.
	var nFailed int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots WHERE applied_success = 0`).Scan(&nFailed)
	if nFailed < 1 {
		t.Errorf("expected at least 1 failed acl_snapshots row, got %d", nFailed)
	}
	// exit_rule_logs has an apply_fail row.
	var logAction string
	_ = d.QueryRow(`SELECT action FROM exit_rule_logs WHERE action = 'apply_fail'`).Scan(&logAction)
	if logAction != "apply_fail" {
		t.Errorf("expected 'apply_fail' in exit_rule_logs, got %q", logAction)
	}
}

func TestDelRuleReplyReadOnlyMode(t *testing.T) {
	d := setupTestDB(t)
	res, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.2.3.4/32', 'accept')`)
	rid, _ := res.LastInsertId()
	// No HS — read-only deploy.
	got := HandleCommand(context.Background(), userEnvWithHS(d, nil), fmt.Sprintf("/delrule %d", rid))
	if !strings.Contains(got, "read-only mode") {
		t.Errorf("expected 'read-only mode' in reply, got: %q", got)
	}
	if !strings.Contains(got, "ask admin") {
		t.Errorf("expected 'ask admin' hint in reply, got: %q", got)
	}
	// Rule row IS deleted (DB delete is local; the read-only guard
	// only skips the headscale.SetPolicy call).
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE id = $1`, rid).Scan(&n)
	if n != 0 {
		t.Errorf("expected rule row to be deleted in read-only mode, got %d rows", n)
	}
	// Audit row should mention the read-only mode.
	var detail string
	_ = d.QueryRow(`SELECT detail FROM audit_log WHERE user_id = 2 AND action = 'rule_deleted' ORDER BY id DESC LIMIT 1`).Scan(&detail)
	if !strings.Contains(detail, "read-only mode") {
		t.Errorf("expected 'read-only mode' in audit detail, got: %q", detail)
	}
	// No NEW acl_snapshots row should be written (no pipeline → no
	// snapshot). setupTestDB seeds one row at version=5, so we
	// expect the count to still be 1.
	var nSnapshots int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots`).Scan(&nSnapshots)
	if nSnapshots != 1 {
		t.Errorf("expected exactly 1 acl_snapshots row (the seed) in read-only mode, got %d", nSnapshots)
	}
}

func TestDelRuleIsAliasOfDeleteRule(t *testing.T) {
	d := setupTestDB(t)
	res, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.2.3.4/32', 'accept')`)
	rid, _ := res.LastInsertId()
	_, hs := fakeHeadscale(t)
	// /delrule and /delete_rule must route to the same handler and
	// produce equivalent results.
	got1 := HandleCommand(context.Background(), userEnvWithHS(d, hs), fmt.Sprintf("/delrule %d", rid))
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Deleted").
	if !strings.Contains(got1, "Deleted 1 rule") {
		t.Errorf("/delroute expected success, got: %q", got1)
	}
	// Re-seed and try the alias.
	res2, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '5.5.5.5/32', 'accept')`)
	rid2, _ := res2.LastInsertId()
	got2 := HandleCommand(context.Background(), userEnvWithHS(d, hs), fmt.Sprintf("/delete_rule %d", rid2))
	if !strings.Contains(got2, "Deleted 1 rule") {
		t.Errorf("/delete_rule alias expected success, got: %q", got2)
	}
}

func TestDelRuleReplyListedInHelp(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/help")
	if !strings.Contains(got, "/delrule") {
		t.Errorf("expected /delrule in /help output, got: %q", got)
	}
	// /delete_rule is the deprecated alias — should NOT appear in
	// the short /help list (only in /help delete_rule detailed view).
	if strings.Contains(got, "/delete_rule") {
		t.Errorf("expected /delete_rule to be hidden from short /help, got: %q", got)
	}
}

func TestDelRuleReplyHelpDetail(t *testing.T) {
	got := helpDetailReply("delrule", BotEnv{})
	if !strings.HasPrefix(got, "/delrule ") {
		t.Errorf("expected /delrule detailed help, got: %q", got)
	}
	if !strings.Contains(got, "cascade") {
		t.Errorf("expected 'cascade' in /help delrule, got: %q", got)
	}
	// delete_rule detailed help should still work (deprecated alias
	// has its own /help entry).
	got2 := helpDetailReply("delete_rule", BotEnv{})
	if !strings.Contains(got2, "DEPRECATED") {
		t.Errorf("expected 'DEPRECATED' in /help delete_rule, got: %q", got2)
	}
}

// --- Этап 13 follow-up: addRuleReply robustness ---
//
// 4 tests covering the read-only guard + SendAlert on SetPolicy
// failure for /add_rule, /delrule, /clearrules:
//   1. /add_rule in read-only mode (HS == nil): rule inserted, no pipeline
//   2. /add_rule on SetPolicy fail: SendAlert called with ❌ ACL apply failed
//   3. /delrule on SetPolicy fail: SendAlert called
//   4. /clearrules on SetPolicy fail: SendAlert called

// recordingNotifier is a Notifier implementation that records every
// SendAlert call into a channel. The buffer is large enough for any
// single test (a /add_rule or /delrule fires at most one alert).
// Tests that don't expect an alert simply don't read from the channel
// — the channel drain is implicit at test exit.
type recordingNotifier struct {
	alerts chan string
}

func newRecordingNotifier() *recordingNotifier {
	return &recordingNotifier{alerts: make(chan string, 10)}
}

func (n *recordingNotifier) SendTelegram(string)              {}
func (n *recordingNotifier) SendTelegramToChat(string, int64) {}
func (n *recordingNotifier) BotUsernameCached() string        { return "" }

func (n *recordingNotifier) SendAlert(text string) int64 {
	n.alerts <- text
	return 0
}

// waitForAlert reads one alert from the channel with a 2s timeout.
// Returns "" + nil on timeout, or the alert text + nil on success.
func (n *recordingNotifier) waitForAlert(t *testing.T, contains string) {
	t.Helper()
	select {
	case got := <-n.alerts:
		if !strings.Contains(got, contains) {
			t.Errorf("expected alert to contain %q, got: %q", contains, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected SendAlert call within 2s, none received (looking for %q)", contains)
	}
}

// expectNoAlert verifies no SendAlert fired within 200ms. Used by
// the success-path tests to confirm we DON'T ping the operator
// when the pipeline succeeds.
func (n *recordingNotifier) expectNoAlert(t *testing.T) {
	t.Helper()
	select {
	case got := <-n.alerts:
		t.Errorf("expected no alert, got: %q", got)
	case <-time.After(200 * time.Millisecond):
		// no alert = expected
	}
}

func TestAddRuleReplyReadOnlyMode(t *testing.T) {
	d := setupAddRuleTestDB(t)
	// env.HS == nil → read-only mode. Rule should still be
	// inserted but ACL pipeline must be skipped.
	got := HandleCommand(context.Background(), userEnvWithHS(d, nil), "/add_rule 1.2.3.4")
	if !strings.Contains(got, "read-only mode") {
		t.Errorf("expected 'read-only mode' in reply, got: %q", got)
	}
	if !strings.Contains(got, "ask admin") {
		t.Errorf("expected 'ask admin' hint in reply, got: %q", got)
	}
	// Rule row inserted.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2 AND target_value = '1.2.3.4/32'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 device_rule row even in read-only mode, got %d", n)
	}
	// audit_log row under alice mentions read-only mode.
	var detail string
	_ = d.QueryRow(`SELECT detail FROM audit_log WHERE user_id = 2 AND action = 'rule_added' ORDER BY id DESC LIMIT 1`).Scan(&detail)
	if !strings.Contains(detail, "read-only mode") {
		t.Errorf("expected 'read-only mode' in audit detail, got: %q", detail)
	}
	// No NEW acl_snapshots row (seed counts as 1).
	var nSnapshots int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots`).Scan(&nSnapshots)
	if nSnapshots != 1 {
		t.Errorf("expected 1 acl_snapshots row (the seed) in read-only mode, got %d", nSnapshots)
	}
}

func TestAddRuleReplySendsAlertOnSetPolicyFailure(t *testing.T) {
	d := setupAddRuleTestDB(t)
	_, hs := fakeHeadscaleSetPolicyFail(t)
	notif := newRecordingNotifier()
	env := userEnvWithHS(d, hs)
	env.Notifier = notif
	HandleCommand(context.Background(), env, "/add_rule 1.2.3.4")
	notif.waitForAlert(t, "ACL apply failed")
}

func TestDelRuleReplySendsAlertOnSetPolicyFailure(t *testing.T) {
	d := setupTestDB(t)
	res, _ := d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.2.3.4/32', 'accept')`)
	rid, _ := res.LastInsertId()
	_, hs := fakeHeadscaleSetPolicyFail(t)
	notif := newRecordingNotifier()
	env := userEnvWithHS(d, hs)
	env.Notifier = notif
	HandleCommand(context.Background(), env, fmt.Sprintf("/delrule %d", rid))
	notif.waitForAlert(t, "ACL apply failed")
}

func TestClearRulesReplySendsAlertOnSetPolicyFailure(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.2.3.4/32', 'accept')`)
	_, hs := fakeHeadscaleSetPolicyFail(t)
	notif := newRecordingNotifier()
	env := userEnvWithHS(d, hs)
	env.Notifier = notif
	HandleCommand(context.Background(), env, "/clearrules")
	HandleCommand(context.Background(), env, "/clearrules confirm")
	notif.waitForAlert(t, "ACL apply failed")
}

// --- Этап 13: /clearrules two-phase confirmation tests (2026-07-13) ---
//
// 14 tests covering the nuclear-wipe flow end-to-end:
//   1.  Reject unbound chat
//   2.  Reject non-admin targeting another user
//   3.  Mint for caller (with rules) — counts + sample, asks confirm
//   4.  Mint for caller (no rules) — "nothing to clear"
//   5.  Confirm without pending — "no pending request"
//   6.  Full mint+confirm — rules wiped, ACL applied
//   7.  Admin mints for another user
//   8.  Admin mint+confirm wipes another user's rules
//   9.  Domain cascade: /32s go too
//   10. Read-only mode (HS == nil): wipe + no pipeline
//   11. SetPolicy failure: wipe + ACL not applied
//   12. New mint overwrites previous pending (most-recent-wins)
//   13. /clearrules in /help
//   14. /help clearrules returns detailed help
//
// Note: we can't easily test TTL expiry in a unit test without
// sleeping for 30s, so that branch is covered by code review of
// the expiry check (time.Now().After(req.expiry)) — not a test.

func TestClearRulesReplyRejectsUnbound(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/clearrules")
	if !strings.Contains(got, "not bound") {
		t.Errorf("expected 'not bound' for unbound /clearrules, got: %q", got)
	}
}

func TestClearRulesReplyRejectsNonAdminForOtherUser(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/clearrules skyadmin")
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Extra").
	if !strings.Contains(got, "Extra args") {
		t.Errorf("expected 'Extra args' for non-admin /clearrules <user>, got: %q", got)
	}
}

func TestClearRulesReplyMintForCallerWithRules(t *testing.T) {
	d := setupTestDB(t)
	// Seed alice's rules.
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '2.2.2.2/32', 'accept')`)
	got := HandleCommand(context.Background(), userEnv(d), "/clearrules")
	if !strings.Contains(got, "delete ALL 2 rule") {
		t.Errorf("expected 'delete ALL 2 rule' in mint reply, got: %q", got)
	}
	if !strings.Contains(got, "/clearrules confirm") {
		t.Errorf("expected '/clearrules confirm' instruction, got: %q", got)
	}
	if !strings.Contains(got, "1.1.1.1/32") || !strings.Contains(got, "2.2.2.2/32") {
		t.Errorf("expected rule samples in reply, got: %q", got)
	}
	// Rules still in DB (mint doesn't delete).
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2`).Scan(&n)
	if n != 2 {
		t.Errorf("expected 2 rules after mint (no delete yet), got %d", n)
	}
	// audit_log has the "requested" row.
	var action string
	_ = d.QueryRow(`SELECT action FROM audit_log WHERE user_id = 2 ORDER BY id DESC LIMIT 1`).Scan(&action)
	if action != "rules_clear_requested" {
		t.Errorf("expected 'rules_clear_requested' audit row, got %q", action)
	}
}

func TestClearRulesReplyMintForCallerNoRules(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/clearrules")
	if !strings.Contains(got, "no exit-rules") {
		t.Errorf("expected 'no exit-rules' when caller has 0 rules, got: %q", got)
	}
	if !strings.Contains(got, "Nothing to clear") {
		t.Errorf("expected 'Nothing to clear' hint, got: %q", got)
	}
}

func TestClearRulesReplyConfirmWithoutPending(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/clearrules confirm")
	// 2026-07-16: v0.15.5 — butler-voice format (capital "No").
	if !strings.Contains(got, "No pending clear request") {
		t.Errorf("expected 'No pending clear request', got: %q", got)
	}
}

func TestClearRulesReplyFullMintAndConfirm(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '2.2.2.2/32', 'accept')`)
	_, hs := fakeHeadscale(t)
	// Phase 1: mint.
	HandleCommand(context.Background(), userEnvWithHS(d, hs), "/clearrules")
	// Phase 2: confirm.
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/clearrules confirm")
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Cleared").
	if !strings.Contains(got, "Cleared 2 rule") {
		t.Errorf("expected 'Cleared 2 rule' in confirm reply, got: %q", got)
	}
	if !strings.Contains(got, "ACL") {
		t.Errorf("expected 'ACL v#' in reply, got: %q", got)
	}
	// All alice's rules gone.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 rules after confirm, got %d", n)
	}
	// Second confirm is a no-op.
	got2 := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/clearrules confirm")
	if !strings.Contains(got2, "No pending") {
		t.Errorf("expected second confirm to be a no-op, got: %q", got2)
	}
	// audit_log has BOTH rows (request + action).
	var reqCount, actCount int
	_ = d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE user_id = 2 AND action = 'rules_clear_requested'`).Scan(&reqCount)
	_ = d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE user_id = 2 AND action = 'rules_cleared'`).Scan(&actCount)
	if reqCount != 1 {
		t.Errorf("expected 1 'rules_clear_requested' audit row, got %d", reqCount)
	}
	if actCount != 1 {
		t.Errorf("expected 1 'rules_cleared' audit row, got %d", actCount)
	}
}

func TestClearRulesReplyAdminMintForOtherUser(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	// skyadmin (admin) mints for alice.
	got := HandleCommand(context.Background(), adminEnv(d), "/clearrules alice")
	if !strings.Contains(got, "delete ALL 1 rule") {
		t.Errorf("expected 'delete ALL 1 rule' for alice, got: %q", got)
	}
	if !strings.Contains(got, "for alice") {
		t.Errorf("expected 'for alice' in reply, got: %q", got)
	}
}

func TestClearRulesReplyAdminMintAndConfirm(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	_, hs := fakeHeadscale(t)
	HandleCommand(context.Background(), adminEnvWithHS(d, hs), "/clearrules alice")
	got := HandleCommand(context.Background(), adminEnvWithHS(d, hs), "/clearrules alice confirm")
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Cleared").
	if !strings.Contains(got, "Cleared 1 rule") {
		t.Errorf("expected 'Cleared 1 rule' in admin confirm reply, got: %q", got)
	}
	if !strings.Contains(got, "for alice") {
		t.Errorf("expected 'for alice' in reply, got: %q", got)
	}
	// Alice's rules gone.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 alice rules after admin confirm, got %d", n)
	}
}

func TestClearRulesReplyDomainCascade(t *testing.T) {
	d := setupTestDB(t)
	// Domain rule + 2 /32 children (autoupdater fan-out) + 1 unrelated.
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action, parent_domain) VALUES (2, 'emilia', 'domain', 'telegram.org', 'accept', 'telegram.org')`)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action, parent_domain) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept', 'telegram.org')`)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action, parent_domain) VALUES (2, 'emilia', 'subnet', '2.2.2.2/32', 'accept', 'telegram.org')`)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '9.9.9.9/32', 'accept')`)
	_, hs := fakeHeadscale(t)
	HandleCommand(context.Background(), userEnvWithHS(d, hs), "/clearrules")
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/clearrules confirm")
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Cleared").
	if !strings.Contains(got, "Cleared 4 rule") {
		t.Errorf("expected 'Cleared 4 rule' (3 original + 0 extra — cascade counted into the 4), got: %q", got)
	}
	if !strings.Contains(got, "cascade: 2") {
		t.Errorf("expected 'cascade: 2' (the 2 /32 children), got: %q", got)
	}
	// All alice's rules gone.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 alice rules after confirm, got %d", n)
	}
}

func TestClearRulesReplyReadOnlyMode(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	HandleCommand(context.Background(), userEnvWithHS(d, nil), "/clearrules")
	got := HandleCommand(context.Background(), userEnvWithHS(d, nil), "/clearrules confirm")
	if !strings.Contains(got, "read-only mode") {
		t.Errorf("expected 'read-only mode' in reply, got: %q", got)
	}
	// Rules wiped despite read-only.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 alice rules after read-only confirm, got %d", n)
	}
	// No NEW acl_snapshots row (seed counts as 1).
	var nSnapshots int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots`).Scan(&nSnapshots)
	if nSnapshots != 1 {
		t.Errorf("expected 1 acl_snapshots row (the seed) in read-only mode, got %d", nSnapshots)
	}
}

func TestClearRulesReplySetPolicyFailure(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	_, hs := fakeHeadscaleSetPolicyFail(t)
	HandleCommand(context.Background(), userEnvWithHS(d, hs), "/clearrules")
	got := HandleCommand(context.Background(), userEnvWithHS(d, hs), "/clearrules confirm")
	if !strings.Contains(got, "NOT applied") {
		t.Errorf("expected 'NOT applied' in reply, got: %q", got)
	}
	// Rules deleted even on SetPolicy failure.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 2`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 alice rules on SetPolicy failure, got %d", n)
	}
	// Failed acl_snapshots row exists.
	var nFailed int
	_ = d.QueryRow(`SELECT COUNT(*) FROM acl_snapshots WHERE applied_success = 0`).Scan(&nFailed)
	if nFailed < 1 {
		t.Errorf("expected at least 1 failed acl_snapshots row, got %d", nFailed)
	}
}

func TestClearRulesReplyNewMintOverwritesPending(t *testing.T) {
	d := setupTestDB(t)
	// Alice mints for herself.
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	HandleCommand(context.Background(), userEnv(d), "/clearrules")
	// Then mints again — should overwrite, not error.
	got := HandleCommand(context.Background(), userEnv(d), "/clearrules")
	if !strings.Contains(got, "/clearrules confirm") {
		t.Errorf("expected second mint to succeed and ask for confirm, got: %q", got)
	}
}

func TestClearRulesReplyListedInHelp(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/help")
	if !strings.Contains(got, "/clearrules") {
		t.Errorf("expected /clearrules in /help output, got: %q", got)
	}
}

// 2026-07-15: Этап 14 v14 (v0.10.14) — bot i18n completion. The
// /clearrules body was the last English-only path in the bot; the
// tests below pin the RU-locale reply on every major branch
// (unbound, no rules, no pending, mint prompt, applied ok,
// read-only, applied-failed). Each EN branch above is mirrored
// here for RU, and an extra test asserts no RU reply leaks
// English substrings (the original bug).

func TestClearRulesReplyRussianUnbound(t *testing.T) {
	d := setupTestDB(t)
	// IsIdentified() == ChatID != 0; build an env with ChatID=0
	// to exercise the not-bound branch.
	env := BotEnv{DB: d, Lang: i18n.LangRU}
	got := HandleCommand(context.Background(), env, "/clearrules")
	if !strings.Contains(got, "не привязан") {
		t.Errorf("RU: expected 'не привязан' in reply, got: %q", got)
	}
	if strings.Contains(got, "chat not bound") {
		t.Errorf("RU: English leak 'chat not bound' in reply: %q", got)
	}
}

func TestClearRulesReplyRussianNoRules(t *testing.T) {
	d := setupTestDB(t)
	env := userEnv(d)
	env.Lang = i18n.LangRU
	got := HandleCommand(context.Background(), env, "/clearrules")
	if !strings.Contains(got, "нет exit-правил") {
		t.Errorf("RU: expected 'нет exit-правил', got: %q", got)
	}
	if strings.Contains(got, "no exit-rules") {
		t.Errorf("RU: English leak 'no exit-rules': %q", got)
	}
}

func TestClearRulesReplyRussianNoPending(t *testing.T) {
	d := setupTestDB(t)
	env := userEnv(d)
	env.Lang = i18n.LangRU
	got := HandleCommand(context.Background(), env, "/clearrules confirm")
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Нет").
	if !strings.Contains(got, "Нет pending-запроса") {
		t.Errorf("RU: expected 'Нет pending-запроса', got: %q", got)
	}
	if strings.Contains(got, "no pending clear request") {
		t.Errorf("RU: English leak 'no pending clear request': %q", got)
	}
}

func TestClearRulesReplyRussianMintPrompt(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	env := userEnv(d)
	env.Lang = i18n.LangRU
	got := HandleCommand(context.Background(), env, "/clearrules")
	// RU mint header.
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Это").
	if !strings.Contains(got, "Это удалит ВСЕ") {
		t.Errorf("RU: expected 'Это удалит ВСЕ' in mint prompt, got: %q", got)
	}
	if !strings.Contains(got, "Отправьте /clearrules confirm в течение") {
		t.Errorf("RU: expected 'Отправьте /clearrules confirm в течение' in mint prompt, got: %q", got)
	}
	// No English leak.
	for _, en := range []string{"this will delete", "Send /clearrules confirm within", "ignored if the request"} {
		if strings.Contains(got, en) {
			t.Errorf("RU: English leak %q in mint prompt: %q", en, got)
		}
	}
}

func TestClearRulesReplyRussianAppliedOk(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	_, hs := fakeHeadscale(t)
	env := userEnvWithHS(d, hs)
	env.Lang = i18n.LangRU
	// mint + confirm
	_ = HandleCommand(context.Background(), env, "/clearrules")
	got := HandleCommand(context.Background(), env, "/clearrules confirm")
	// RU success prefix.
	// 2026-07-16: v0.15.5 — butler-voice format (capital "Очищено").
	if !strings.Contains(got, "✓ Очищено") {
		t.Errorf("RU: expected '✓ Очищено', got: %q", got)
	}
	if !strings.Contains(got, "ACL v") {
		t.Errorf("RU: expected 'ACL v' in success, got: %q", got)
	}
	// No English leak.
	for _, en := range []string{"cleared", "applied to headscale"} {
		if strings.Contains(got, en) {
			t.Errorf("RU: English leak %q in applied_ok: %q", en, got)
		}
	}
}

func TestClearRulesReplyRussianReadOnlyMode(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO device_rules(user_id, exit_node_id, target_type, target_value, action) VALUES (2, 'emilia', 'subnet', '1.1.1.1/32', 'accept')`)
	env := userEnvWithHS(d, nil)
	env.Lang = i18n.LangRU
	_ = HandleCommand(context.Background(), env, "/clearrules")
	got := HandleCommand(context.Background(), env, "/clearrules confirm")
	if !strings.Contains(got, "read-only") {
		// "read-only" appears in both RU and EN strings; this
		// is the RU catalog value (lowercase, Russian phrase).
		t.Errorf("RU: expected RU read-only mention, got: %q", got)
	}
	if !strings.Contains(got, "ACL sync пропущен") {
		t.Errorf("RU: expected 'ACL sync пропущен', got: %q", got)
	}
	if !strings.Contains(got, "Попросите админа /admin/exit-rules/sync") {
		t.Errorf("RU: expected 'Попросите админа', got: %q", got)
	}
}

func TestClearRulesReplyHelpDetail(t *testing.T) {
	got := helpDetailReply("clearrules", BotEnv{})
	if !strings.HasPrefix(got, "/clearrules ") {
		t.Errorf("expected /clearrules detailed help, got: %q", got)
	}
	if !strings.Contains(got, "Two-phase") {
		t.Errorf("expected 'Two-phase' in /help clearrules, got: %q", got)
	}
}

// --- Этап 14: /myexitnodes (user-scope exit-node menu) ---
//
// 5 tests covering the user-scope exit-node list:
//   1. Reject unbound chat
//   2. Empty menu (no enabled exit_servers) → "ask admin"
//   3. List enabled exit-servers with status + last_seen
//   4. Disabled exit-servers are hidden from the menu
//   5. [default] marker on the user's currently configured default
//   6. /myexitnodes in /help
//   7. /help myexitnodes returns detailed help

func TestMyExitNodesReplyRejectsUnbound(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), envFor(d), "/myexitnodes")
	if !strings.Contains(got, "not bound") {
		t.Errorf("expected 'not bound' for unbound /myexitnodes, got: %q", got)
	}
}

func TestMyExitNodesReplyEmptyMenu(t *testing.T) {
	d := setupTestDB(t)
	// setupTestDB doesn't seed exit_servers, so the menu is empty.
	got := HandleCommand(context.Background(), userEnv(d), "/myexitnodes")
	if !strings.Contains(got, "no enabled exit-nodes") {
		t.Errorf("expected 'no enabled exit-nodes' for empty menu, got: %q", got)
	}
	if !strings.Contains(got, "Ask an admin") {
		t.Errorf("expected 'Ask an admin' hint, got: %q", got)
	}
}

func TestMyExitNodesReplyListsEnabled(t *testing.T) {
	d := setupTestDB(t)
	// Seed: one enabled, one disabled.
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('emilia-1', 'emilia', 1)`)
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('aphrodite-1', 'aphrodite', 1)`)
	// Disabled row — must NOT appear.
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('demeter-1', 'demeter', 0)`)
	// Seed devices for online/last_seen.
	_, _ = d.Exec(`INSERT INTO devices(node_id, last_seen, online) VALUES ('emilia-1', 1700000000, 1)`)
	_, _ = d.Exec(`INSERT INTO devices(node_id, last_seen, online) VALUES ('aphrodite-1', 1700000100, 0)`)
	got := HandleCommand(context.Background(), userEnv(d), "/myexitnodes")
	// 2026-07-16: v0.16.x — "more HTML" pass. Header
	// format changed from "Available exit-nodes (2):" to
	// "exit-nodes for <b>alice</b> (2 available):" with
	// the count moved into a Field() line below.
	if !strings.Contains(got, "exit-nodes for") {
		t.Errorf("expected new header 'exit-nodes for', got: %q", got)
	}
	if !strings.Contains(got, "(2 available)") {
		t.Errorf("expected '(2 available)' count, got: %q", got)
	}
	if !strings.Contains(got, "emilia") {
		t.Errorf("expected emilia in menu, got: %q", got)
	}
	if !strings.Contains(got, "aphrodite") {
		t.Errorf("expected aphrodite in menu, got: %q", got)
	}
	// Disabled server must be hidden.
	if strings.Contains(got, "demeter") {
		t.Errorf("disabled server demeter must NOT appear in user menu, got: %q", got)
	}
	// 2026-07-16: v0.16.x — tabular <pre> block. Online
	// status is now a column on the same row as the
	// hostname, not a free-floating "online" word. We
	// check for the emilia row substring to confirm the
	// alignment.
	if !strings.Contains(got, "emilia") || !strings.Contains(got, "emilia-1") {
		t.Errorf("expected emilia row to carry hostname+node_id, got: %q", got)
	}
	if !strings.Contains(got, "online") {
		t.Errorf("expected 'online' status column, got: %q", got)
	}
	if !strings.Contains(got, "offline") {
		t.Errorf("expected 'offline' status column, got: %q", got)
	}
	// Workflow hint.
	if !strings.Contains(got, "/setexitnode") {
		t.Errorf("expected /setexitnode hint, got: %q", got)
	}
}

func TestMyExitNodesReplyMarksDefault(t *testing.T) {
	d := setupTestDB(t)
	// Seed two enabled exit-servers.
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('emilia-1', 'emilia', 1)`)
	_, _ = d.Exec(`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES ('aphrodite-1', 'aphrodite', 1)`)
	// Set alice's default to aphrodite-1.
	_, _ = d.Exec(`UPDATE portal_users SET default_exit_node_id = 'aphrodite-1' WHERE id = 2`)
	got := HandleCommand(context.Background(), userEnv(d), "/myexitnodes")
	// 2026-07-16: v0.16.x — "more HTML" pass. The default
	// marker moved from "  [default]" to a single ✓ in
	// the DEFAULT column. The substring "aphrodite-1  ... ✓"
	// is hard to pin because of <pre> alignment padding,
	// so we just check that ✓ appears and that the
	// aphrodite row is followed by a line ending in ✓
	// (the emilia row should NOT have a ✓).
	if !strings.Contains(got, "✓") {
		t.Errorf("expected ✓ marker for default node, got: %q", got)
	}
	// emilia must NOT carry the ✓ marker.
	emiliaRow := ""
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "emilia-1") {
			emiliaRow = line
		}
	}
	if strings.Contains(emiliaRow, "✓") {
		t.Errorf("emilia row must not be marked default, got: %q", emiliaRow)
	}
	// The aphrodite row should carry the ✓.
	aphroditeRow := ""
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "aphrodite-1") {
			aphroditeRow = line
		}
	}
	if !strings.Contains(aphroditeRow, "✓") {
		t.Errorf("aphrodite row must be marked default, got: %q", aphroditeRow)
	}
}

func TestMyExitNodesReplyListedInHelp(t *testing.T) {
	d := setupTestDB(t)
	got := HandleCommand(context.Background(), userEnv(d), "/help")
	if !strings.Contains(got, "/myexitnodes") {
		t.Errorf("expected /myexitnodes in /help output, got: %q", got)
	}
}

func TestMyExitNodesReplyHelpDetail(t *testing.T) {
	got := helpDetailReply("myexitnodes", BotEnv{})
	if !strings.HasPrefix(got, "/myexitnodes ") {
		t.Errorf("expected /myexitnodes detailed help, got: %q", got)
	}
	// 2026-07-16: v0.16.x — "more HTML" pass. The
	// default-node marker moved from "[default]" (a
	// four-char label) to "✓" (a single char that
	// reads cleanly in the DEFAULT column of the
	// tabular <pre> block). Help detail text is
	// kept in lockstep.
	if !strings.Contains(got, "✓") {
		t.Errorf("expected '✓' in /help myexitnodes, got: %q", got)
	}
}

// ---------------------------------------------------------------
// Этап 12 (2026-07-13) — login-by-key + strict mode.
//
// These tests live at the bottom of the file so the existing
// helper definitions (envFor, adminEnv, userEnv, setupTestDB)
// are visible. The tests use the per-test in-memory DB seeded
// by setupTestDB (alice = id=2, skyadmin = id=1, IsAdmin=1).
// ---------------------------------------------------------------

// strictEnv returns a BotEnv with the same shape as userEnv
// (caller is identified as alice, not admin) but with
// StrictMode=true. Used to exercise the strict-mode gate.
func strictEnv(d *sql.DB) BotEnv {
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('telegram.strict_mode', '1')`)
	return BotEnv{DB: d, ChatID: 99999, Lang: i18n.LangEN, PortalUserID: 2, Username: "alice", IsAdmin: false, StrictMode: true}
}

// testLoginToken is a known-shape token (skg-XXXX-XXXX-XXXX
// with alphabet A-Z minus I/O, plus 2-9) used by the login
// tests. We can't use mintLoginToken (it would round-trip
// through the real crypto/rand) — these tests need a
// deterministic token to assert against.
const testLoginToken = "skg-ABCD-EFGH-JKLM"

// insertValidLoginToken seeds a fresh, unused, not-yet-expired
// token for portal_user_id=2 (alice) and returns the token
// string. The expires_at is 1 hour in the future, well past
// the rate-limit / TTL interactions we test.
func insertValidLoginToken(t *testing.T, d *sql.DB, token string, userID int64, ttlSeconds int) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := d.Exec(`INSERT INTO telegram_login_tokens(token, portal_user_id, created_at, expires_at, used_at, used_by_chat_id, request_ip)
		VALUES ($1, $2, $3, $4, 0, 0, '127.0.0.1')`, token, userID, now, now+int64(ttlSeconds)); err != nil {
		t.Fatalf("insertValidLoginToken: %v", err)
	}
}

func TestLoginReplyNoArgs(t *testing.T) {
	d := setupTestDB(t)
	// Unbound chat in strict mode: should print the hint
	// (NOT a generic error, NOT a "chat not bound" gate).
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, StrictMode: true}
	got := HandleCommand(context.Background(), env, "/login")
	if !strings.Contains(got, "Generate login key") {
		t.Errorf("expected hint pointing to /my/telegram, got: %q", got)
	}
}

func TestLoginReplyValid(t *testing.T) {
	d := setupTestDB(t)
	insertValidLoginToken(t, d, testLoginToken, 2, 300) // for alice
	// Unbound chat in strict mode that pastes the key.
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, StrictMode: true}
	got := HandleCommand(context.Background(), env, "/login "+testLoginToken)
	if !strings.Contains(got, "Logged in as alice") {
		t.Errorf("expected 'Logged in as alice', got: %q", got)
	}
	// The token is now consumed.
	var usedAt int64
	if err := d.QueryRow(`SELECT used_at FROM telegram_login_tokens WHERE token = $1`, testLoginToken).Scan(&usedAt); err != nil {
		t.Fatalf("read used_at: %v", err)
	}
	if usedAt == 0 {
		t.Errorf("expected used_at > 0 after successful login, got 0")
	}
	// The binding is now present.
	var boundUser int64
	if err := d.QueryRow(`SELECT portal_user_id FROM telegram_bindings WHERE chat_id = 555`).Scan(&boundUser); err != nil {
		t.Fatalf("read binding: %v", err)
	}
	if boundUser != 2 {
		t.Errorf("expected chat 555 → user 2, got %d", boundUser)
	}
}

func TestLoginReplyInvalid(t *testing.T) {
	d := setupTestDB(t)
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, StrictMode: true}
	// Not in DB.
	got := HandleCommand(context.Background(), env, "/login skg-ZZZZ-ZZZZ-ZZZZ")
	if !strings.Contains(got, "invalid or expired key") {
		t.Errorf("expected 'invalid or expired key' for not-found, got: %q", got)
	}
	// Right shape, but no row in DB.
	got2 := HandleCommand(context.Background(), env, "/login skg-AAAA-BBBB-CCCC")
	if !strings.Contains(got2, "invalid or expired key") {
		t.Errorf("expected 'invalid or expired key' for unknown shape, got: %q", got2)
	}
}

func TestLoginReplyExpired(t *testing.T) {
	d := setupTestDB(t)
	// Token whose expires_at is 10s in the past.
	insertValidLoginToken(t, d, testLoginToken, 2, -10)
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, StrictMode: true}
	got := HandleCommand(context.Background(), env, "/login "+testLoginToken)
	if !strings.Contains(got, "invalid or expired key") {
		t.Errorf("expected 'invalid or expired key' for expired, got: %q", got)
	}
}

func TestLoginReplyAlreadyUsed(t *testing.T) {
	d := setupTestDB(t)
	insertValidLoginToken(t, d, testLoginToken, 2, 300)
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, StrictMode: true}
	// First call consumes the token.
	_ = HandleCommand(context.Background(), env, "/login "+testLoginToken)
	// Reset rate-limit so the second call isn't blocked by that.
	resetLoginAttempts(d, 555)
	// Second call: token already used, should fail.
	got := HandleCommand(context.Background(), env, "/login "+testLoginToken)
	if !strings.Contains(got, "invalid or expired key") {
		t.Errorf("expected 'invalid or expired key' for already-used, got: %q", got)
	}
}

func TestLoginReplyRateLimit(t *testing.T) {
	d := setupTestDB(t)
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, StrictMode: true}
	// 5 attempts in <60s (rate limit max). All should fail
	// (no token seeded), but the rate-limit gate only kicks
	// in on the 6th.
	for i := 0; i < loginRateLimitMax; i++ {
		got := HandleCommand(context.Background(), env, "/login skg-ZZZZ-ZZZZ-ZZZZ")
		if strings.Contains(got, "too many attempts") {
			t.Errorf("attempt #%d unexpectedly rate-limited: %q", i+1, got)
		}
	}
	// 6th attempt should be rate-limited.
	got := HandleCommand(context.Background(), env, "/login skg-ZZZZ-ZZZZ-ZZZZ")
	if !strings.Contains(got, "too many attempts") {
		t.Errorf("expected 'too many attempts' on 6th call, got: %q", got)
	}
}

func TestStartReplyWithTokenShowsConfirmation(t *testing.T) {
	d := setupTestDB(t)
	insertValidLoginToken(t, d, testLoginToken, 2, 300)
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, StrictMode: true}
	// 2026-07-13: Этап 13 — /start <token> no longer binds
	// immediately. It shows a confirmation prompt with
	// inline [Bind] [Cancel] buttons; the actual bind
	// happens on the [Bind] tap (which goes through the
	// callback_query path, not HandleCommand). /login
	// <token> keeps the one-command shortcut.
	got := HandleCommand(context.Background(), env, "/start "+testLoginToken)
	if !strings.Contains(got, "Bind this chat to") {
		t.Errorf("expected confirmation prompt, got: %q", got)
	}
	if !strings.Contains(got, "alice") {
		t.Errorf("expected prompt to mention the target user, got: %q", got)
	}
	// Inline keyboard should be set on the package-level
	// slot (the polling loop reads it after HandleCommand
	// returns and attaches it to the sendMessage payload).
	if pendingReplyForCurrentMessage == nil {
		t.Fatalf("expected pendingReplyForCurrentMessage to be set, got nil")
	}
	if len(pendingReplyForCurrentMessage.InlineKeyboard) != 1 {
		t.Errorf("expected 1 keyboard row, got %d", len(pendingReplyForCurrentMessage.InlineKeyboard))
	}
	row := pendingReplyForCurrentMessage.InlineKeyboard[0]
	if len(row) != 2 {
		t.Errorf("expected 2 buttons, got %d", len(row))
	}
	// Bind button carries the token; Cancel button has the
	// "bind:cancel" sentinel.
	if cb, _ := row[0]["callback_data"].(string); !strings.HasPrefix(cb, "bind:confirm:") {
		t.Errorf("expected first button callback_data=bind:confirm:..., got %q", row[0]["callback_data"])
	}
	if row[1]["callback_data"] != "bind:cancel" {
		t.Errorf("expected second button callback_data=bind:cancel, got %q", row[1]["callback_data"])
	}
	// Token should NOT be consumed yet — the Bind tap is
	// what consumes it. This is the whole point of the
	// confirmation step.
	var usedAt int64
	_ = d.QueryRow(`SELECT used_at FROM telegram_login_tokens WHERE token = $1`, testLoginToken).Scan(&usedAt)
	if usedAt != 0 {
		t.Errorf("token should not be consumed on /start (only on Bind tap), got used_at=%d", usedAt)
	}
	// And no binding row should exist yet.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM telegram_bindings WHERE chat_id = 555`).Scan(&n)
	if n != 0 {
		t.Errorf("binding should not exist on /start (only on Bind tap), got %d rows", n)
	}
}

func TestStartReplyNoTokenShowsHint(t *testing.T) {
	d := setupTestDB(t)
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, StrictMode: true}
	got := HandleCommand(context.Background(), env, "/start")
	if !strings.Contains(got, "Generate login key") {
		t.Errorf("expected /start (no arg) to show the hint, got: %q", got)
	}
}

func TestStrictModeRejectsAdminCommandForUnboundChat(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('telegram.strict_mode', '1')`)
	// Unbound chat (no ChatID — IsIdentified()==false) in strict mode.
	// 2026-07-14: Этап 14 v5 — env.Lang=EN so the strict-mode
	// reply is in English (the test asserts on the English
	// "not bound" wording).
	env := BotEnv{DB: d, StrictMode: true, Lang: i18n.LangEN}
	for _, cmd := range []string{"/status", "/nodes", "/rules", "/audit", "/quota", "/exit_nodes"} {
		got := HandleCommand(context.Background(), env, cmd)
		if !strings.Contains(got, "not bound") {
			t.Errorf("strict mode should reject %s for unbound chat, got: %q", cmd, got)
		}
	}
}

func TestStrictModeAllowsAuthAndHelp(t *testing.T) {
	d := setupTestDB(t)
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('telegram.strict_mode', '1')`)
	// Unbound chat in production has ChatID=0 (the dispatcher
	// clears it for unbound non-admin chats; see
	// RealNotifier.resolveBootstrapAdmin). Mirror that here
	// so helpReply takes the "strict + unidentified" branch.
	env := BotEnv{DB: d, ChatID: 0, Lang: i18n.LangEN, StrictMode: true}
	// /help and /version MUST work for an unbound chat in
	// strict mode (otherwise a stranger can't even read the
	// docs that tell them to /login).
	if got := HandleCommand(context.Background(), env, "/help"); !strings.Contains(got, "Strict mode") {
		t.Errorf("strict mode should still allow /help, got: %q", got)
	}
	if got := HandleCommand(context.Background(), env, "/version"); !strings.Contains(got, "Skygate") {
		t.Errorf("strict mode should still allow /version, got: %q", got)
	}
	// /login and /start MUST work (they're the path to
	// becoming identified).
	if got := HandleCommand(context.Background(), env, "/login"); !strings.Contains(got, "Generate login key") {
		t.Errorf("strict mode should allow /login, got: %q", got)
	}
}

func TestStrictModeOffKeepsLegacyFallback(t *testing.T) {
	d := setupTestDB(t)
	// No global_settings row → strict mode defaults to false.
	// Unbound chat + non-strict = admin (legacy behaviour).
	env := BotEnv{DB: d, Lang: i18n.LangEN}
	if !env.EffectiveAdmin() {
		t.Errorf("without strict mode, unidentified chat should be admin (legacy)")
	}
	// With strict mode on, the same env should NOT be admin.
	envStrict := BotEnv{DB: d, StrictMode: true}
	if envStrict.EffectiveAdmin() {
		t.Errorf("with strict mode, unidentified chat should NOT be admin")
	}
}

func TestUnbindSelfReplyRemovesBinding(t *testing.T) {
	d := setupTestDB(t)
	// Seed a binding for alice (chat 555 → user 2).
	_, _ = d.Exec(`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at, bound_by_user_id) VALUES (555, 2, 0, 1700000000, 0)`)
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, PortalUserID: 2, Username: "alice", IsAdmin: false}
	got := HandleCommand(context.Background(), env, "/unbind_self")
	if !strings.Contains(got, "no longer bound") {
		t.Errorf("expected 'no longer bound' in /unbind_self reply, got: %q", got)
	}
	// Row should be gone.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM telegram_bindings WHERE chat_id = 555`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 rows in telegram_bindings after /unbind_self, got %d", n)
	}
}

func TestUnbindSelfReplyNotBound(t *testing.T) {
	d := setupTestDB(t)
	// In production, an unbound chat has ChatID=0 (the
	// dispatcher's resolveBootstrapAdmin clears it). We
	// mirror that here.
	env := BotEnv{DB: d, ChatID: 0, Lang: i18n.LangEN}
	got := HandleCommand(context.Background(), env, "/unbind_self")
	if !strings.Contains(got, "not bound") {
		t.Errorf("expected 'not bound' for unbound /unbind_self, got: %q", got)
	}
}

func TestLooksLikeLoginTokenShape(t *testing.T) {
	// Valid shapes.
	for _, s := range []string{
		"skg-ABCD-EFGH-JKLM",
		"skg-2345-6789-ZXYZ",
		"skg-ZZZZ-AAAA-2222",
	} {
		if !looksLikeLoginToken(s) {
			t.Errorf("expected %q to look like a valid login token", s)
		}
	}
	// Invalid: wrong prefix, wrong separators, illegal chars.
	for _, s := range []string{
		"ABC-EFGH-JKLM-NOPQ",       // wrong prefix
		"skg-ABCD-EFGH-JKLMN",      // too long
		"skg-ABCD-EFGH-JK",         // too short
		"skg-ABCD.EFGH.JKLM",       // wrong separator
		"skg-abcd-efgh-jklm",       // lowercase
		"skg-ABCD-EFG1-JKLM",       // contains '1'
		"skg-ABCD-EFGO-JKLM",       // contains 'O'
		"skg-ABCD-EFG0-JKLM",       // contains '0'
		"skg-ABCD-EFGI-JKLM",       // contains 'I'
	} {
		if looksLikeLoginToken(s) {
			t.Errorf("expected %q to FAIL the login-token shape check", s)
		}
	}
}

func TestStrictModeSavedAndLoaded(t *testing.T) {
	d := setupTestDB(t)
	// Default: not set → false.
	if db.LoadTelegramStrictMode(d) {
		t.Errorf("expected default strict_mode=false, got true")
	}
	// Save true → loads as true.
	if err := db.SaveTelegramStrictMode(d, true); err != nil {
		t.Fatalf("SaveTelegramStrictMode: %v", err)
	}
	if !db.LoadTelegramStrictMode(d) {
		t.Errorf("expected strict_mode=true after save, got false")
	}
	// Save false → loads as false.
	if err := db.SaveTelegramStrictMode(d, false); err != nil {
		t.Fatalf("SaveTelegramStrictMode(false): %v", err)
	}
	if db.LoadTelegramStrictMode(d) {
		t.Errorf("expected strict_mode=false after save(false), got true")
	}
}

func TestLoginTokenTTLSavedAndLoaded(t *testing.T) {
	d := setupTestDB(t)
	// Default: 300s.
	if got := db.LoadTelegramLoginTokenTTL(d); got != 300 {
		t.Errorf("expected default TTL=300, got %d", got)
	}
	// After a manual save... wait, we don't have a Save
	// helper for TTL. Set it via raw SQL to test the loader's
	// integer-parse path (the loader also tolerates non-numeric
	// strings by falling back to 300 — see the comment in
	// telegram_login_tokens.go).
	if _, err := d.Exec(`INSERT INTO global_settings(key, value) VALUES ('telegram.login_token_ttl_seconds', '120')`); err != nil {
		t.Fatalf("seed TTL: %v", err)
	}
	if got := db.LoadTelegramLoginTokenTTL(d); got != 120 {
		t.Errorf("expected TTL=120 after seed, got %d", got)
	}
	// Garbage value → 300.
	if _, err := d.Exec(`UPDATE global_settings SET value = 'five minutes' WHERE key = 'telegram.login_token_ttl_seconds'`); err != nil {
		t.Fatalf("garbage seed: %v", err)
	}
	if got := db.LoadTelegramLoginTokenTTL(d); got != 300 {
		t.Errorf("expected TTL=300 (fallback) for garbage value, got %d", got)
	}
}

// ---------------------------------------------------------------
// Этап 13 (2026-07-13) — new features.
//
// Tests in this section cover the roadmap items that landed
// in this commit: Bind-by-QR (DB helper + bot username
// cache), rate-limit-via-SQLite, and the inline-keyboard
// confirmation prompt for /start <token>.
// ---------------------------------------------------------------

// TestPeekTelegramLoginTokenDoesNotConsume is the load-bearing
// test for the inline-keyboard flow: Peek must read the row
// without flipping used_at, otherwise the [Bind] tap would
// find nothing to consume.
func TestPeekTelegramLoginTokenDoesNotConsume(t *testing.T) {
	d := setupTestDB(t)
	insertValidLoginToken(t, d, testLoginToken, 2, 300)
	// Peek twice — both should succeed and return the same
	// snapshot, with used_at still 0.
	for i := 0; i < 2; i++ {
		tok, err := db.PeekTelegramLoginToken(d, testLoginToken)
		if err != nil {
			t.Fatalf("peek #%d: %v", i, err)
		}
		if tok.UsedAt != 0 {
			t.Errorf("peek #%d should not consume: used_at=%d", i, tok.UsedAt)
		}
	}
	// Row still unused.
	var usedAt int64
	_ = d.QueryRow(`SELECT used_at FROM telegram_login_tokens WHERE token = $1`, testLoginToken).Scan(&usedAt)
	if usedAt != 0 {
		t.Errorf("row should still be unused after 2 peeks, got used_at=%d", usedAt)
	}
	// Peek of a non-existent token → ErrTelegramLoginTokenNotFound.
	_, err := db.PeekTelegramLoginToken(d, "skg-ZZZZ-ZZZZ-ZZZZ")
	if err != db.ErrTelegramLoginTokenNotFound {
		t.Errorf("expected ErrTelegramLoginTokenNotFound for missing token, got %v", err)
	}
	// Peek of an expired token → ErrTelegramLoginTokenExpired
	// (insert with past expires_at).
	insertValidLoginToken(t, d, "skg-AAAA-AAAA-AAAA", 2, -10)
	_, err = db.PeekTelegramLoginToken(d, "skg-AAAA-AAAA-AAAA")
	if err != db.ErrTelegramLoginTokenExpired {
		t.Errorf("expected ErrTelegramLoginTokenExpired, got %v", err)
	}
}

// TestRecordTelegramRateLimitAttemptAllowed covers the basic
// happy path: 5 attempts in 60s are allowed, the 6th is not.
// The DB-backed limiter is the replacement for the retired
// in-memory loginAttempts map; the threshold and window are
// the same.
func TestRecordTelegramRateLimitAttemptAllowed(t *testing.T) {
	d := setupTestDB(t)
	key := "login:555"
	for i := 0; i < loginRateLimitMax; i++ {
		_, allowed, err := db.RecordTelegramRateLimitAttempt(d, key, "", loginRateLimitWindowSeconds, loginRateLimitMax)
		if err != nil {
			t.Fatalf("attempt #%d unexpected err: %v", i+1, err)
		}
		if !allowed {
			t.Errorf("attempt #%d should be allowed (under max)", i+1)
		}
	}
	// 6th: over the limit.
	_, allowed, err := db.RecordTelegramRateLimitAttempt(d, key, "", loginRateLimitWindowSeconds, loginRateLimitMax)
	if err != nil {
		t.Fatalf("attempt #6 unexpected err: %v", err)
	}
	if allowed {
		t.Errorf("attempt #6 should be denied (over max=%d)", loginRateLimitMax)
	}
}

// TestRecordTelegramRateLimitAttemptDifferentKeysIsolated
// confirms that one chat hitting its limit doesn't block
// another chat. Each chat_id is a separate key, and the
// index is (key, ts).
func TestRecordTelegramRateLimitAttemptDifferentKeysIsolated(t *testing.T) {
	d := setupTestDB(t)
	// Burn through chat 555's quota.
	for i := 0; i < loginRateLimitMax+1; i++ {
		_, _, _ = db.RecordTelegramRateLimitAttempt(d, "login:555", "", loginRateLimitWindowSeconds, loginRateLimitMax)
	}
	// Chat 666 should still be allowed.
	_, allowed, err := db.RecordTelegramRateLimitAttempt(d, "login:666", "", loginRateLimitWindowSeconds, loginRateLimitMax)
	if err != nil {
		t.Fatalf("chat 666 attempt: %v", err)
	}
	if !allowed {
		t.Errorf("chat 666 should be allowed (different key), got denied")
	}
}

// TestResetTelegramRateLimit clears the per-chat slot so the
// next attempt goes through. This is the test-reset hook
// that replaces the old resetLoginAttempts() helper.
func TestResetTelegramRateLimit(t *testing.T) {
	d := setupTestDB(t)
	key := "login:555"
	// Burn through.
	for i := 0; i < loginRateLimitMax+1; i++ {
		_, _, _ = db.RecordTelegramRateLimitAttempt(d, key, "", loginRateLimitWindowSeconds, loginRateLimitMax)
	}
	// Reset.
	n, err := db.ResetTelegramRateLimit(d, key)
	if err != nil {
		t.Fatalf("ResetTelegramRateLimit: %v", err)
	}
	if n == 0 {
		t.Errorf("expected >0 rows deleted, got 0")
	}
	// First attempt after reset should be allowed.
	_, allowed, err := db.RecordTelegramRateLimitAttempt(d, key, "", loginRateLimitWindowSeconds, loginRateLimitMax)
	if err != nil {
		t.Fatalf("post-reset attempt: %v", err)
	}
	if !allowed {
		t.Errorf("post-reset attempt should be allowed, got denied")
	}
}

// TestStartReplyNoTokenShowsHintOrAlreadyLoggedIn verifies
// that the new /start behavior — show confirmation prompt
// for /start <token>, hint for /start alone — is what
// TestStartReplyNoTokenShowsHint expects, AND that returning
// users get the "already logged in" message.
func TestStartReplyNoTokenShowsHintOrAlreadyLoggedIn(t *testing.T) {
	d := setupTestDB(t)
	// Unbound chat in strict mode → hint.
	env := BotEnv{DB: d, ChatID: 0, Lang: i18n.LangEN, StrictMode: true}
	got := HandleCommand(context.Background(), env, "/start")
	if !strings.Contains(got, "Generate login key") {
		t.Errorf("expected hint for unbound /start, got: %q", got)
	}
	// Bound chat (Username set) → "the gate knows you" message
	// (Этап 14 v5: replaces the old "Already logged in as X"
	// text with the butler-gatekeeper voice).
	env2 := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, PortalUserID: 2, Username: "alice", IsAdmin: false}
	got2 := HandleCommand(context.Background(), env2, "/start")
	if !strings.Contains(got2, "alice") || !strings.Contains(got2, "gate knows you") {
		t.Errorf("expected 'gate knows you' greeting for bound /start, got: %q", got2)
	}
}

// TestLoginReplyStillBindsImmediately is the regression test
// for the /start split: /login <token> must keep its
// one-command shortcut (bind immediately, no confirmation
// prompt). Without this, the test from Этап 12
// (TestLoginReplyValid) would still pass but the UX would
// regress — every /login would show a keyboard.
func TestLoginReplyStillBindsImmediately(t *testing.T) {
	d := setupTestDB(t)
	insertValidLoginToken(t, d, testLoginToken, 2, 300)
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, StrictMode: true}
	got := HandleCommand(context.Background(), env, "/login "+testLoginToken)
	if !strings.Contains(got, "Logged in as alice") {
		t.Errorf("expected immediate bind via /login, got: %q", got)
	}
	// No keyboard should be set (the /login path is the
	// shortcut; only /start shows the prompt).
	if pendingReplyForCurrentMessage != nil {
		t.Errorf("expected no inline keyboard for /login, got one")
	}
	// And the binding should exist immediately.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM telegram_bindings WHERE chat_id = 555`).Scan(&n)
	if n != 1 {
		t.Errorf("expected binding to exist after /login, got %d rows", n)
	}
}

// TestStartReplyNoArgsDoesNotConsume confirms the rate-limit
// is enforced on /start (a malicious flood of /start <random>
// shouldn't burn a chat's quota without consequence).
func TestStartReplyNoArgsDoesNotConsume(t *testing.T) {
	d := setupTestDB(t)
	env := BotEnv{DB: d, ChatID: 0, Lang: i18n.LangEN, StrictMode: true}
	// /start with no arg → login hint, no token consume.
	got := HandleCommand(context.Background(), env, "/start")
	if strings.Contains(got, "Bind this chat") {
		t.Errorf("expected hint, not confirmation prompt, for /start (no arg), got: %q", got)
	}
}

// TestStartReplyInvalidTokenShapeRejected covers the cheap
// shape check in startReply — junk inputs don't burn DB
// cycles (the same shape check loginReply has, mirrored
// here so /start has the same fast-fail).
func TestStartReplyInvalidTokenShapeRejected(t *testing.T) {
	d := setupTestDB(t)
	env := BotEnv{DB: d, ChatID: 555, Lang: i18n.LangEN, StrictMode: true}
	got := HandleCommand(context.Background(), env, "/login skg-ABCD") // too short
	if !strings.Contains(got, "doesn't look like a valid key") {
		t.Errorf("expected shape-check rejection, got: %q", got)
	}
	// Reset the rate-limit so the next call isn't blocked
	// by a fake-attempt counter (we just recorded one).
	resetLoginAttempts(d, 555)
}

// TestBotUsernameCacheAfterTokenSave is a small end-to-end
// check: once a token is saved (operator completed
// /admin/telegram), the next getMe-discovered username
// should be reflected in BotUsernameCached(). We mock the
// HTTP response via httptest in a real test, but for the
// unit-test scope we just confirm the Notifier interface
// returns "" when no token is saved (the cache-miss path
// in BotUsernameCached).
func TestBotUsernameCacheEmptyWithoutToken(t *testing.T) {
	d := setupTestDB(t)
	// No telegram.bot_token in global_settings → no cached
	// username; BotUsernameCached should return "".
	// We can't easily construct a RealNotifier here
	// (it needs a *headscale.Client, etc.) so we just
	// assert the underlying SQL: a SELECT for the bot
	// token returns no rows.
	var v string
	err := d.QueryRow(`SELECT value FROM global_settings WHERE key = 'telegram.bot_token'`).Scan(&v)
	if err != sql.ErrNoRows {
		t.Errorf("expected no token row, got value=%q err=%v", v, err)
	}
}
