package telegram

// 2026-07-14: regression test for the v0.10.3 dispatch fix.
//
// Background: prior to v0.10.3, RealNotifier.Run and
// handleCallback passed `effectiveChatID` (the first return
// value of resolveBootstrapAdmin, which is 0 for chats that
// have neither a binding row nor a global chat_id match) to
// n.env(), which set env.ChatID from that argument. As a
// result, every handler saw env.ChatID=0 for unbound chats
// and the loginReply / startReply handlers refused with
// "login: internal error (chat_id missing); contact admin".
//
// The fix: envForMessage(chatID) accepts the actual chat_id
// of the message sender and applies the bootstrap-admin
// fallback internally. Run and handleCallback now call
// envForMessage(updateChatID) instead of the inline dance.
//
// This test verifies that envForMessage preserves the
// caller's chat_id argument in env.ChatID across the three
// states the old code broke on:
//   (a) token set, no global chat_id, no binding (the failure case)
//   (b) token set, no global chat_id, binding exists
//   (c) token set, global chat_id set, chat_id matches the global

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
)

// envForMessagePreservesRealChatID: the regression guard.
// Sets up a Notifier with a token and NO global chat_id
// (the configuration that the production VM is in right
// now) and asserts that envForMessage(555).ChatID == 555,
// not 0.
//
// This is the bug the user reported on Telegram: "login:
// internal error (chat_id missing); contact admin" — the
// bot received their /login <token>, but env.ChatID was 0
// (because the dispatcher had passed effectiveChatID to
// n.env), so loginReply bailed out before doing anything.
func TestEnvForMessagePreservesRealChatID(t *testing.T) {
	d := setupTestDB(t)
	// Save the token; deliberately leave chat_id empty.
	if err := db.SaveTelegramToken(d, "test-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	n := &RealNotifier{
		apiBase: "https://api.telegram.org",
		db:      d,
		client:  nil, // not exercised by this test
		pollInt: 0,
	}
	got, _ := n.envForMessage(555, "")
	if got.ChatID != 555 {
		t.Errorf("env.ChatID = %d, want 555 (was 0 before the v0.10.3 fix; the dispatcher was passing effectiveChatID=0 from resolveBootstrapAdmin when neither binding nor global chat_id was set)", got.ChatID)
	}
}

// envForMessageStillTriggersBindingLookup: the fix MUST NOT
// regress the binding-lookup path. The env() function uses
// chatID to look up telegram_bindings and populate
// PortalUserID/IsAdmin/Username. envForMessage must continue
// to do this — otherwise the "preserve real chat_id" fix
// would break strict-mode permission checks (env.IsAdmin
// would be false for an actually-admin binding).
//
// Seed alice's binding, call envForMessage(alice's chat),
// expect env.IsAdmin == true and env.Username == "alice".
func TestEnvForMessageStillTriggersBindingLookup(t *testing.T) {
	d := setupTestDB(t)
	if err := db.SaveTelegramToken(d, "test-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	// Bind chat 555 to user 2 (alice, is_admin=0 in the seed
	// — we override to 1 below for clarity of the assertion).
	if _, err := d.Exec(
		`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES ($1, $2, $3, $4)`,
		555, 2, 1, 1700000000,
	); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	n := &RealNotifier{apiBase: "https://api.telegram.org", db: d, client: nil, pollInt: 0}
	got, _ := n.envForMessage(555, "")
	if got.ChatID != 555 {
		t.Errorf("env.ChatID = %d, want 555", got.ChatID)
	}
	if !got.IsAdmin {
		t.Errorf("env.IsAdmin = false, want true (binding had is_admin=1)")
	}
	if got.Username != "alice" {
		t.Errorf("env.Username = %q, want alice", got.Username)
	}
	if got.PortalUserID != 2 {
		t.Errorf("env.PortalUserID = %d, want 2", got.PortalUserID)
	}
}

// envForMessageBootstrapAdminFlag: the bootstrap-admin
// fallback (chat_id matches the configured admin chat
// globally, no binding row) must still apply. The fix must
// not regress this — it's the legacy single-admin deploy
// path.
//
// Seed token + global chat_id=999, then call envForMessage
// with chatID=999 (the bootstrap admin) and chatID=888
// (NOT the admin). Expect:
//   * chatID=999 → env.IsAdmin == true (bootstrap), env.ChatID == 999
//   * chatID=888 → env.IsAdmin == false (no bootstrap, no binding), env.ChatID == 888
func TestEnvForMessageBootstrapAdminFlag(t *testing.T) {
	d := setupTestDB(t)
	if err := db.SaveTelegramToken(d, "test-token", "999"); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	n := &RealNotifier{apiBase: "https://api.telegram.org", db: d, client: nil, pollInt: 0}

	// Bootstrap admin chat: should get IsAdmin=true even
	// without a binding row.
	env, isBootstrap := n.envForMessage(999, "")
	if !isBootstrap {
		t.Errorf("isBootstrapAdmin = false, want true for chat_id == global chat_id")
	}
	if env.ChatID != 999 {
		t.Errorf("env.ChatID = %d, want 999", env.ChatID)
	}
	if !env.IsAdmin {
		t.Errorf("env.IsAdmin = false, want true (bootstrap admin fallback)")
	}

	// Some other chat: should NOT be admin, should keep its
	// real chat_id (this is the fix).
	env, isBootstrap = n.envForMessage(888, "")
	if isBootstrap {
		t.Errorf("isBootstrapAdmin = true, want false for chat_id != global chat_id")
	}
	if env.ChatID != 888 {
		t.Errorf("env.ChatID = %d, want 888 (this is the v0.10.3 fix)", env.ChatID)
	}
	if env.IsAdmin {
		t.Errorf("env.IsAdmin = true, want false (no binding, not bootstrap)")
	}
}

// envForMessageLoginHandler: the integration-level test the
// original bug report triggered. Simulates a /login <token>
// dispatch path: no global chat_id, alice's token is valid,
// chat 555 is unbound. Expect loginReply to produce a
// "Logged in as alice" success message — NOT the
// "internal error (chat_id missing)" the user saw in
// production.
//
// We call HandleCommand directly (skipping the getUpdates
// loop) with an env that envForMessage would have built.
// This isolates the test from network and timer concerns
// while still exercising the full handler path that broke.
func TestEnvForMessageLoginHandler(t *testing.T) {
	d := setupTestDB(t)
	if err := db.SaveTelegramToken(d, "test-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	insertValidLoginToken(t, d, testLoginToken, 2, 300) // for alice

	n := &RealNotifier{apiBase: "https://api.telegram.org", db: d, client: nil, pollInt: 0}
	env, _ := n.envForMessage(555, "")
	got := HandleCommand(nil, env, "/login "+testLoginToken)
	if strings.Contains(got, "internal error") {
		t.Errorf("loginReply returned the v0.10.2 'chat_id missing' error: %q — the v0.10.3 fix should prevent this", got)
	}
	if !strings.Contains(got, "Logged in as alice") {
		t.Errorf("loginReply did not return success: %q", got)
	}
	// And the binding is now in the DB.
	var bound int64
	if err := d.QueryRow(`SELECT chat_id FROM telegram_bindings WHERE chat_id = $1`, 555).Scan(&bound); err != nil {
		if err == sql.ErrNoRows {
			t.Fatalf("binding was not created (loginReply reported success but the DB has no row for chat 555)")
		}
		t.Fatalf("read binding: %v", err)
	}
	if bound != 555 {
		t.Errorf("bound chat = %d, want 555", bound)
	}
}
