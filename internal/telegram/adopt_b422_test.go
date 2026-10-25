// internal/telegram/adopt_b422_test.go — B422.
//
// B421 made an ADMIN binding an acceptable alert recipient. That fixes DELIVERY but leaves
// the configuration in a state nobody wrote down: the page says "chat_id: absent" while
// messages arrive, and a future change to the fallback would silently re-break it. B422
// offers to persist the chat that is already being used; these tests pin the guards that
// make the offer safe — never a member's chat, never over an operator's own choice.
package telegram

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

func b422DB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.OpenFastSQLiteForTest(t.TempDir() + "/b422.db")
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
	return d
}

func b422Seed(t *testing.T, d *sql.DB, key, value string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES ($1, $2)`, key, value); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

func b422Bind(t *testing.T, d *sql.DB, chatID, userID int64, admin bool) {
	t.Helper()
	flag := 0
	if admin {
		flag = 1
	}
	if _, err := d.Exec(
		`INSERT INTO telegram_bindings (chat_id, portal_user_id, is_admin, bound_by_user_id) VALUES ($1,$2,$3,0)`,
		chatID, userID, flag); err != nil {
		t.Fatalf("bind %d: %v", chatID, err)
	}
}

// TestB422_AdoptableChatIsTheAdminBinding is the operator's exact situation: a token, no
// chat_id, one bound ADMIN chat — the page must be able to pin that chat in one click.
func TestB422_AdoptableChatIsTheAdminBinding(t *testing.T) {
	d := b422DB(t)
	b422Seed(t, d, "telegram.bot_token", "123:abc")
	b422Bind(t, d, 328946535, 1, true)
	if got := AdoptableAdminChat(d); got != 328946535 {
		t.Fatalf("AdoptableAdminChat = %d, want 328946535", got)
	}
}

// TestB422_AdoptableChatRefusesAMember is the security guard: a member chat is never
// proposed, because adopting it would also SEND to it (alerts name hosts, IPs, certs).
func TestB422_AdoptableChatRefusesAMember(t *testing.T) {
	d := b422DB(t)
	b422Seed(t, d, "telegram.bot_token", "123:abc")
	b422Bind(t, d, 555, 7, false)
	if got := AdoptableAdminChat(d); got != 0 {
		t.Fatalf("AdoptableAdminChat = %d, want 0 for a member binding", got)
	}
}

// TestB422_AdoptableChatIsZeroWhenAlreadyConfigured: the button must not offer to overwrite
// a recipient the operator chose.
func TestB422_AdoptableChatIsZeroWhenAlreadyConfigured(t *testing.T) {
	d := b422DB(t)
	b422Seed(t, d, "telegram.bot_token", "123:abc")
	b422Seed(t, d, "telegram.chat_id", "4242")
	b422Bind(t, d, 328946535, 1, true)
	if got := AdoptableAdminChat(d); got != 0 {
		t.Fatalf("AdoptableAdminChat = %d, want 0 when chat_id is already set", got)
	}
}

// TestB422_TargetAvailabilityDescribesTheRecipient is what the canary reports and what the
// page shows: a secret-free description, or the named reason there is none.
func TestB422_TargetAvailabilityDescribesTheRecipient(t *testing.T) {
	d := b422DB(t)
	ok, desc := AlertTargetAvailability(d)
	if ok {
		t.Fatalf("ok = true with no token: %q", desc)
	}
	if !strings.Contains(desc, "bot_token") {
		t.Fatalf("desc %q does not name the missing key", desc)
	}

	b422Seed(t, d, "telegram.bot_token", "123:abc")
	ok, desc = AlertTargetAvailability(d)
	if ok {
		t.Fatalf("ok = true with no chat and no binding: %q", desc)
	}
	if !strings.Contains(desc, "chat_id") && !strings.Contains(desc, "no_chat") {
		t.Fatalf("desc %q does not name the missing recipient", desc)
	}

	b422Seed(t, d, "telegram.chat_id", "4242")
	ok, desc = AlertTargetAvailability(d)
	if !ok {
		t.Fatalf("ok = false for a configured pair: %q", desc)
	}
	if !strings.HasPrefix(desc, TargetGlobalChat) {
		t.Fatalf("desc = %q, want it to start with %q", desc, TargetGlobalChat)
	}
	if strings.Contains(desc, "4242") {
		t.Fatalf("desc leaks the chat id in full: %q", desc)
	}
}
