// internal/telegram/alert_ledger_b421_test.go — B421.
//
// These tests exist because the operator's report («уведомления не приходили») was
// caused by a path that had NO test: sendAlert's "not configured" branch returned 0 and
// nothing else ever asserted what happens to an alert that cannot be addressed. Each
// case below is one way an installation can be configured, and the requirement is the
// same for all of them: the alert either arrives, or the ledger names why it did not.
package telegram

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// b421DB opens a migrated SQLite database with NO telegram settings at all.
func b421DB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.OpenFastSQLiteForTest(t.TempDir() + "/b421.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("MigrateSQLite: %v", err)
	}
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	ResetAlertDeliveryForTest()
	t.Cleanup(ResetAlertDeliveryForTest)
	return d
}

func b421Seed(t *testing.T, d *sql.DB, key, value string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES ($1, $2)`, key, value); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

func b421Bind(t *testing.T, d *sql.DB, chatID, userID int64, isAdmin bool) {
	t.Helper()
	flag := 0
	if isAdmin {
		flag = 1
	}
	if _, err := d.Exec(
		`INSERT INTO telegram_bindings (chat_id, portal_user_id, is_admin, bound_by_user_id) VALUES ($1, $2, $3, 0)`,
		chatID, userID, flag); err != nil {
		t.Fatalf("bind chat %d: %v", chatID, err)
	}
}

// --- the resolution rule ---------------------------------------------------

// TestB421_NoTokenIsNamedNotSilent: a token-less install must say WHICH half is missing.
// Pre-B421 this returned 0 with no log and no counter.
func TestB421_NoTokenIsNamedNotSilent(t *testing.T) {
	d := b421DB(t)
	_, outcome, reason := resolveAlertTarget(d)
	if outcome != AlertNoToken {
		t.Fatalf("outcome = %q, want %q", outcome, AlertNoToken)
	}
	if !strings.Contains(reason, "bot_token") {
		t.Fatalf("reason %q does not name the missing key", reason)
	}
}

// TestB421_TokenWithoutAnyChatNamesTheMissingHalf is the operator's configuration class:
// a working bot, no global chat_id, no bindings.
func TestB421_TokenWithoutAnyChatNamesTheMissingHalf(t *testing.T) {
	d := b421DB(t)
	b421Seed(t, d, "telegram.bot_token", "123:abc")
	_, outcome, reason := resolveAlertTarget(d)
	if outcome != AlertNoChat {
		t.Fatalf("outcome = %q, want %q", outcome, AlertNoChat)
	}
	if !strings.Contains(reason, "telegram.chat_id") {
		t.Fatalf("reason %q does not name telegram.chat_id", reason)
	}
}

// TestB421_GlobalChatIDWins: the configured pair is used first, as before.
func TestB421_GlobalChatIDWins(t *testing.T) {
	d := b421DB(t)
	b421Seed(t, d, "telegram.bot_token", "123:abc")
	b421Seed(t, d, "telegram.chat_id", "4242")
	b421Bind(t, d, 777, 1, true)
	tgt, outcome, reason := resolveAlertTarget(d)
	if outcome != AlertSent || reason != "" {
		t.Fatalf("outcome=%q reason=%q, want a resolved target", outcome, reason)
	}
	if tgt.source != TargetGlobalChat || tgt.chatID != 4242 {
		t.Fatalf("target = %+v, want global chat 4242", tgt)
	}
}

// TestB421_AdminBindingFallbackWorks: the bot configured by BINDING an admin chat — the
// configuration that used to lose every alert — now delivers.
func TestB421_AdminBindingFallbackWorks(t *testing.T) {
	d := b421DB(t)
	b421Seed(t, d, "telegram.bot_token", "123:abc")
	b421Bind(t, d, 555, 9, true)
	tgt, outcome, reason := resolveAlertTarget(d)
	if outcome != AlertSent || reason != "" {
		t.Fatalf("outcome=%q reason=%q, want the admin binding to be used", outcome, reason)
	}
	if tgt.source != TargetAdminBind || tgt.chatID != 555 {
		t.Fatalf("target = %+v, want admin binding 555", tgt)
	}
}

// TestB421_MemberBindingIsNeverUsedForAlerts is the SECURITY half: a binding belongs to a
// portal user, most users are not admins, and alerts name hostnames, IPs and certificate
// subjects. Falling back to a member's chat would turn a delivery fix into a leak.
func TestB421_MemberBindingIsNeverUsedForAlerts(t *testing.T) {
	d := b421DB(t)
	b421Seed(t, d, "telegram.bot_token", "123:abc")
	b421Bind(t, d, 111, 5, false) // a member, NOT an admin
	tgt, outcome, reason := resolveAlertTarget(d)
	if outcome != AlertNoChat {
		t.Fatalf("outcome = %q, want %q — a member chat must never receive alerts", outcome, AlertNoChat)
	}
	if tgt.chatID != 0 {
		t.Fatalf("target chat = %d, want 0 (refused)", tgt.chatID)
	}
	if !strings.Contains(reason, "admin") {
		t.Fatalf("reason %q does not explain the admin-only rule", reason)
	}
}

// TestB421_MixedBindingsPickTheAdmin: with both kinds present the admin one is used, and
// deterministically (lowest chat id) so two ticks cannot disagree.
func TestB421_MixedBindingsPickTheAdmin(t *testing.T) {
	d := b421DB(t)
	b421Seed(t, d, "telegram.bot_token", "123:abc")
	b421Bind(t, d, 111, 5, false)
	b421Bind(t, d, 900, 6, true)
	b421Bind(t, d, 700, 7, true)
	tgt, outcome, _ := resolveAlertTarget(d)
	if outcome != AlertSent {
		t.Fatalf("outcome = %q, want a resolved admin binding", outcome)
	}
	if tgt.chatID != 700 {
		t.Fatalf("chat = %d, want the lowest admin chat 700", tgt.chatID)
	}
}

// TestB421_InvalidChatIDIsNamed: "abc" used to reach ParseInt deep in the transport.
func TestB421_InvalidChatIDIsNamed(t *testing.T) {
	d := b421DB(t)
	b421Seed(t, d, "telegram.bot_token", "123:abc")
	b421Seed(t, d, "telegram.chat_id", "abc")
	_, outcome, reason := resolveAlertTarget(d)
	if outcome != AlertBadChat {
		t.Fatalf("outcome = %q, want %q", outcome, AlertBadChat)
	}
	if !strings.Contains(reason, "chat_id") {
		t.Fatalf("reason %q does not name the field", reason)
	}
}

// --- the ledger ------------------------------------------------------------

// TestB421_LedgerCountsAndKeepsTheLastOutcome: the page reads this.
func TestB421_LedgerCountsAndKeepsTheLastOutcome(t *testing.T) {
	d := b421DB(t)
	notifier := &RealNotifier{db: d} // no token → not configured
	if id := notifier.SendAlert("first"); id != 0 {
		t.Fatalf("SendAlert returned %d on an unconfigured bot, want 0", id)
	}
	stats := AlertDeliverySnapshot()
	if stats.NotConfigured != 1 || stats.Sent != 0 || stats.Failed != 0 || stats.Suppressed != 0 {
		t.Fatalf("stats = %+v, want exactly 1 NotConfigured", stats)
	}
	if stats.LastOutcome != AlertNoToken {
		t.Fatalf("last outcome = %q, want %q", stats.LastOutcome, AlertNoToken)
	}
	if stats.LastReason == "" || stats.LastAt.IsZero() {
		t.Fatalf("last reason/at not recorded: %+v", stats)
	}
	if stats.Configured() {
		t.Fatalf("Configured() = true for an unconfigured bot")
	}

	// A second alert on the same broken configuration must still be COUNTED
	// (the log line is rate-limited, the truth is not).
	notifier.SendAlert("second")
	if got := AlertDeliverySnapshot().NotConfigured; got != 2 {
		t.Fatalf("NotConfigured after a second alert = %d, want 2", got)
	}
}

// TestB421_SuppressedAlertIsCountedNotLost: a dedup suppression is a decision, not a
// failure — and it must be visible as its own number.
func TestB421_SuppressedAlertIsCountedNotLost(t *testing.T) {
	d := b421DB(t)
	b421Seeder(t, d)
	notifier, _ := b391CapturingNotifier(t, d, 200, `{"ok":true,"result":{"message_id":1}}`)
	alert := Alert{Severity: SeverityInfo, Title: "same event", DedupKey: "b421-test-dedup", DedupTTL: time.Minute}
	if id := notifier.SendAlertEvent(alert); id == 0 {
		t.Fatal("the first alert was not delivered")
	}
	if id := notifier.SendAlertEvent(alert); id != 0 {
		t.Fatalf("the repeat returned %d, want 0 (suppressed)", id)
	}
	stats := AlertDeliverySnapshot()
	if stats.Sent != 1 || stats.Suppressed != 1 {
		t.Fatalf("stats = %+v, want Sent=1 Suppressed=1", stats)
	}
	if stats.LastOutcome != AlertSuppressed {
		t.Fatalf("last outcome = %q, want %q", stats.LastOutcome, AlertSuppressed)
	}
}

// b421Seeder configures the bot the way the reference install does.
func b421Seeder(t *testing.T, d *sql.DB) {
	t.Helper()
	b421Seed(t, d, "telegram.bot_token", "123:abc")
	b421Seed(t, d, "telegram.chat_id", "4242")
}

// --- end to end through the real transport --------------------------------

// TestB421_DeliveryIsRecordedThroughTheRealPath drives the REAL sendAlert against a fake
// Bot API: the ledger must say "sent", the /ack row must exist, and the payload must
// carry parse_mode=HTML and the [#id] prefix.
func TestB421_DeliveryIsRecordedThroughTheRealPath(t *testing.T) {
	d := b421DB(t)
	b421Seeder(t, d)
	notifier, payloads := b391CapturingNotifier(t, d, 200, `{"ok":true,"result":{"message_id":7}}`)

	id := notifier.SendAlertEvent(Alert{Severity: SeverityInfo, Title: "delivered <b>ok</b>"})
	if id == 0 {
		t.Fatal("SendAlertEvent returned 0 for a configured bot")
	}
	stats := AlertDeliverySnapshot()
	if stats.Sent != 1 || stats.LastOutcome != AlertSent {
		t.Fatalf("stats = %+v, want Sent=1 last=sent", stats)
	}
	if stats.LastAlertID != id {
		t.Fatalf("LastAlertID = %d, want %d", stats.LastAlertID, id)
	}
	if stats.TargetSource != TargetGlobalChat {
		t.Fatalf("TargetSource = %q, want %q", stats.TargetSource, TargetGlobalChat)
	}
	if !stats.Configured() {
		t.Fatal("Configured() = false for a configured bot")
	}
	if stats.LastPath == "" {
		t.Fatal("LastPath is empty — the page could not say which transport carried the alert")
	}
	if len(*payloads) != 1 {
		t.Fatalf("Bot API received %d requests, want 1", len(*payloads))
	}
	body := (*payloads)[0]
	if pm, _ := body["parse_mode"].(string); pm != "HTML" {
		t.Fatalf("parse_mode = %v, want HTML", body["parse_mode"])
	}
	if text, _ := body["text"].(string); !strings.HasPrefix(text, "[#") {
		t.Fatalf("text does not start with the /ack id: %q", text)
	}
	var rows int
	if err := d.QueryRow(`SELECT COUNT(*) FROM telegram_alerts`).Scan(&rows); err != nil {
		t.Fatalf("count telegram_alerts: %v", err)
	}
	if rows != 1 {
		t.Fatalf("telegram_alerts rows = %d, want 1", rows)
	}
}

// TestB421_SendFailureIsRecordedAndRedacted: a 400 from the Bot API must be a NAMED
// failure (and must not leak the token), not a silent zero.
func TestB421_SendFailureIsRecordedAndRedacted(t *testing.T) {
	d := b421DB(t)
	b421Seeder(t, d)
	notifier, _ := b391CapturingNotifier(t, d, 400, `{"ok":false,"description":"chat not found"}`)

	if id := notifier.SendAlert("will fail"); id != 0 {
		t.Fatalf("SendAlert returned %d for a rejected send, want 0", id)
	}
	stats := AlertDeliverySnapshot()
	if stats.Failed != 1 || stats.LastOutcome != AlertSendFailed {
		t.Fatalf("stats = %+v, want Failed=1 last=send_failed", stats)
	}
	if stats.LastReason == "" {
		t.Fatal("LastReason is empty — the page could not explain the failure")
	}
	if strings.Contains(stats.LastReason, "123:abc") {
		t.Fatalf("LastReason leaks the bot token: %q", stats.LastReason)
	}
}

// TestB421_AdminBindingDeliversEndToEnd: the fallback configuration must actually reach
// the Bot API (the pre-B421 code failed before it ever built a request).
func TestB421_AdminBindingDeliversEndToEnd(t *testing.T) {
	d := b421DB(t)
	b421Seed(t, d, "telegram.bot_token", "123:abc")
	b421Bind(t, d, 555, 9, true)
	notifier, payloads := b391CapturingNotifier(t, d, 200, `{"ok":true,"result":{"message_id":3}}`)

	if id := notifier.SendAlert("admin chat"); id == 0 {
		t.Fatal("SendAlert returned 0 with an admin binding and a token")
	}
	stats := AlertDeliverySnapshot()
	if stats.Sent != 1 || stats.TargetSource != TargetAdminBind {
		t.Fatalf("stats = %+v, want Sent=1 via admin_binding", stats)
	}
	if len(*payloads) != 1 {
		t.Fatalf("Bot API received %d requests, want 1", len(*payloads))
	}
	if chat, _ := (*payloads)[0]["chat_id"].(float64); int64(chat) != 555 {
		t.Fatalf("chat_id sent = %v, want 555", (*payloads)[0]["chat_id"])
	}
}

// TestB421_LegacySendAlertReachesTheSameTransport proves the ~40 triggers that still call
// SendAlert("plain sentence") are covered by the same path — the page's counters do not
// describe a niche API that nobody uses.
func TestB421_LegacySendAlertReachesTheSameTransport(t *testing.T) {
	d := b421DB(t)
	b421Seeder(t, d)
	notifier, _ := b391CapturingNotifier(t, d, 200, `{"ok":true,"result":{"message_id":1}}`)

	id := notifier.SendAlert("[monitor] exit node is down")
	if id == 0 {
		t.Fatal("the legacy SendAlert did not deliver through the real transport")
	}
	stats := AlertDeliverySnapshot()
	if stats.Sent != 1 || stats.LastOutcome != AlertSent {
		t.Fatalf("stats = %+v, want the legacy path counted and marked sent", stats)
	}
	if stats.LastAt.IsZero() || time.Since(stats.LastAt) > time.Minute {
		t.Fatalf("LastAt = %v, want a fresh timestamp", stats.LastAt)
	}
	var rows int
	if err := d.QueryRow(`SELECT COUNT(*) FROM telegram_alerts`).Scan(&rows); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if rows != 1 {
		t.Fatalf("telegram_alerts rows = %d, want 1 (the /ack bookkeeping row)", rows)
	}
}

// TestB421_BootTargetLogIsSafeOnAnEmptyDatabase: the boot line must never be the reason a
// process fails to start (a nil handle, an empty database, an unconfigured bot).
func TestB421_BootTargetLogIsSafeOnAnEmptyDatabase(t *testing.T) {
	LogAlertTargetAtBoot(nil)
	d := b421DB(t)
	LogAlertTargetAtBoot(d) // no token
	b421Seeder(t, d)
	LogAlertTargetAtBoot(d) // configured
	if got := AlertDeliverySnapshot(); got.LastOutcome != "" {
		t.Fatalf("the boot log must not record a delivery outcome: %+v", got)
	}
}

// --- small pure helpers ----------------------------------------------------

func TestB421_MaskChatID(t *testing.T) {
	if got := maskChatID(1234567890); got != "12******90" {
		t.Fatalf("maskChatID = %q", got)
	}
	if got := maskChatID(42); got != "42" {
		t.Fatalf("maskChatID(short) = %q, want the value unchanged", got)
	}
}

func TestB421_TransportPathNamesTheRelay(t *testing.T) {
	if got := alertTransportPath(EgressSnapshot{}); got != "direct" {
		t.Fatalf("no relay → %q, want direct", got)
	}
	fresh := EgressSnapshot{Relay: "karolina", RelayAt: time.Now()}
	if got := alertTransportPath(fresh); got != "relay:karolina" {
		t.Fatalf("fresh relay → %q, want relay:karolina", got)
	}
	stale := EgressSnapshot{Relay: "karolina", RelayAt: time.Now().Add(-time.Hour)}
	if got := alertTransportPath(stale); got != "direct" {
		t.Fatalf("stale relay → %q, want direct", got)
	}
}

// TestB421_PayloadIsJSONDocumentsTheFakeAPI keeps the capture helper honest: the fake
// server parses the body as JSON, so a payload that is not JSON would silently capture
// nothing and the assertions above would pass vacuously.
func TestB421_PayloadIsJSONDocumentsTheFakeAPI(t *testing.T) {
	d := b421DB(t)
	b421Seeder(t, d)
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(srv.Close)
	(&RealNotifier{apiBase: srv.URL, client: srv.Client(), db: d}).SendAlert("json check")
	if got == nil {
		t.Fatal("the Bot API received a body that is not JSON — the capture assertions elsewhere would be vacuous")
	}
}
