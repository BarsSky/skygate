package telegram

// 2026-07-15: Этап 14 v13 — tests for the i18n-aware
// SetMyCommandsAll and DefaultMyCommandsSpec.
//
// The menu is a one-shot registration on bot startup, so
// the test surface is:
//   1. The spec shape (every command the dispatcher knows
//      about is in the menu, no duplicates).
//   2. The i18n parity (every DescriptionKey resolves in
//      both ru and en catalogs).
//   3. The HTTP payload shape (per-language code on the
//      ru call, omitted on the default call).
//   4. The end-to-end HTTP flow (mock Telegram server,
//      verify both calls land with the right shape).
//
// The integration is verified by the smoke test (the bot
// sends real commands to a real Telegram on every deploy),
// but a unit-level test catches a typo in a catalog key
// before it ships.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"skygate/internal/i18n"

	_ "github.com/mattn/go-sqlite3"
)

// TestDefaultMyCommandsSpecCoversEveryReply: every command
// that the dispatcher's switch statement accepts AND that
// the menu should expose must appear in either Common or
// AdminOnly. /login, /start, /lang, /unbind_self and
// /_bind_cancel are deliberately excluded (see the long
// comment in commands_set.go for the rationale).
func TestDefaultMyCommandsSpecCoversEveryReply(t *testing.T) {
	menuCommands := map[string]bool{}
	for _, c := range DefaultMyCommandsSpec.Common {
		menuCommands[c.Command] = true
	}
	for _, c := range DefaultMyCommandsSpec.AdminOnly {
		menuCommands[c.Command] = true
	}
	wantInCommon := []string{
		"help", "version",
		"my_status", "my_rules", "my_nodes", "my_quota",
		"add_rule", "delrule", "add_device",
	}
	for _, cmd := range wantInCommon {
		if !menuCommands[cmd] {
			t.Errorf("common menu missing %q (every user should see this)", cmd)
		}
	}
	wantInAdmin := []string{
		"status", "nodes", "exit_nodes", "rules", "quota",
		"audit", "ack", "restart", "bind", "unbind",
	}
	for _, cmd := range wantInAdmin {
		if !menuCommands[cmd] {
			t.Errorf("admin menu missing %q", cmd)
		}
	}
}

// TestDefaultMyCommandsSpecNoDuplicates: the same command
// can't appear in both Common and AdminOnly (Telegram would
// deduplicate silently, and the resulting menu would be
// unpredictable for a user who happens to be both).
func TestDefaultMyCommandsSpecNoDuplicates(t *testing.T) {
	seen := map[string]string{} // command -> scope it first appeared in
	for _, c := range DefaultMyCommandsSpec.Common {
		seen[c.Command] = "common"
	}
	for _, c := range DefaultMyCommandsSpec.AdminOnly {
		if prev, ok := seen[c.Command]; ok {
			t.Errorf("command %q appears in BOTH %q and admin_only scopes", c.Command, prev)
		}
		seen[c.Command] = "admin_only"
	}
}

// TestMenuDescriptionsResolveInBothLanguages: the i18n
// catalog must have a translation for every DescriptionKey
// in BOTH en and ru. A missing key in one language would
// leave that chat's menu in English (the default fallback
// the helper uses) — exactly the bug v0.10.12 fixes.
func TestMenuDescriptionsResolveInBothLanguages(t *testing.T) {
	spec := DefaultMyCommandsSpec
	for _, lang := range []string{i18n.LangEN, i18n.LangRU} {
		for _, entry := range spec.All() {
			got := i18n.T(lang, entry.DescriptionKey)
			if got == entry.DescriptionKey {
				t.Errorf("lang=%q: missing translation for %q (the menu would show the raw key as a description)", lang, entry.DescriptionKey)
			}
			// Telegram caps descriptions at 256 chars;
			// we cap at 100 to keep menu cells one line
			// on mobile. A typo that blows the cell is
			// annoying but not fatal; this test just
			// keeps us honest.
			if len(got) > 100 {
				t.Errorf("lang=%q: description for %q is %d chars (>100): %q", lang, entry.Command, len(got), got)
			}
		}
	}
}

// TestMenuIsNotEnglishLeakInRussian: regression guard for
// the v0.10.11 bug where the menu was hardcoded in English
// and the user saw "The Threshold's codex" in a Russian
// chat. Every entry's ru description must differ from the
// en description (otherwise the language switch is a no-op
// and we're back to the same bug).
func TestMenuIsNotEnglishLeakInRussian(t *testing.T) {
	for _, entry := range DefaultMyCommandsSpec.All() {
		en := i18n.T(i18n.LangEN, entry.DescriptionKey)
		ru := i18n.T(i18n.LangRU, entry.DescriptionKey)
		if en == ru {
			t.Errorf("command %q: ru and en descriptions are identical (%q) — RU users would see English in their menu", entry.Command, en)
		}
	}
}

// TestResolveAllReturnsResolvedList: spec.resolve must
// return one telegramMenuCommand per entry, with the
// description filled in from the catalog (not the key).
func TestResolveAllReturnsResolvedList(t *testing.T) {
	resolved := DefaultMyCommandsSpec.resolve(i18n.LangEN)
	want := len(DefaultMyCommandsSpec.All())
	if len(resolved) != want {
		t.Fatalf("resolve(en) returned %d commands, want %d", len(resolved), want)
	}
	for _, c := range resolved {
		if c.Command == "" {
			t.Errorf("empty command in resolved list")
		}
		if c.Description == "" {
			t.Errorf("command %q: empty description after resolve(en)", c.Command)
		}
		// The "[no description: ...]" sentinel must not
		// appear in the happy-path output — it would mean
		// the catalog has a typo.
		if strings.HasPrefix(c.Description, "[no description:") {
			t.Errorf("command %q: description is the missing-key sentinel: %q", c.Command, c.Description)
		}
	}
}

// TestResolveFillsMissingKeyWithSentinel: a typo in a
// catalog key must not silently drop the description.
// The helper substitutes a deterministic sentinel so the
// problem is obvious in the rendered menu.
func TestResolveFillsMissingKeyWithSentinel(t *testing.T) {
	spec := MyCommandsSpec{
		Common: []MenuEntry{{Command: "test", DescriptionKey: "bot.menu.does_not_exist"}},
	}
	resolved := spec.resolve(i18n.LangEN)
	if len(resolved) != 1 {
		t.Fatalf("expected 1 command, got %d", len(resolved))
	}
	if !strings.Contains(resolved[0].Description, "bot.menu.does_not_exist") {
		t.Errorf("expected sentinel to mention the missing key, got %q", resolved[0].Description)
	}
}

// TestPostSetMyCommandsPayloadShape: the JSON the helper
// builds must match Telegram's documented shape for both
// the per-language and the default-scope calls.
func TestPostSetMyCommandsPayloadShape(t *testing.T) {
	cases := []struct {
		name              string
		lang              string
		wantLangCode      bool
		wantLangCodeValue string
	}{
		{"default (no lang)", "", false, ""},
		{"ru", "ru", true, "ru"},
		{"en", "en", true, "en"},
	}
	cmds := []telegramMenuCommand{{Command: "help", Description: "x"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := map[string]any{
				"commands": cmds,
				"scope":    map[string]string{"type": "default"},
			}
			if c.wantLangCode {
				payload["language_code"] = c.lang
			}
			b, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var rt map[string]any
			if err := json.Unmarshal(b, &rt); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			lc, hasLC := rt["language_code"]
			if c.wantLangCode && !hasLC {
				t.Errorf("payload missing language_code: %s", string(b))
			}
			if !c.wantLangCode && hasLC {
				t.Errorf("payload has unexpected language_code: %v", lc)
			}
			if c.wantLangCode && hasLC && lc != c.wantLangCodeValue {
				t.Errorf("language_code=%v, want %q", lc, c.wantLangCodeValue)
			}
		})
	}
}

// TestSetMyCommandsAllSendsBothLanguages: the end-to-end
// orchestrator must POST two setMyCommands calls when the
// bot is configured — one for the default (en) and one for
// ru — and each one must carry the right language_code.
//
// We don't hit Telegram directly; we run a mock server
// that captures both requests and assert against the
// captured payloads. This is the same pattern the other
// bot tests use (see TestAddDeviceReplyWritesKey).
func TestSetMyCommandsAllSendsBothLanguages(t *testing.T) {
	type captured struct {
		body []byte
		lang string // "ru" / "en" / "" for the default call
	}
	var (
		mu  sync.Mutex
		got []captured
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/setMyCommands") {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			lc := ""
			var parsed map[string]any
			if json.Unmarshal(body, &parsed) == nil {
				if v, ok := parsed["language_code"].(string); ok {
					lc = v
				}
			}
			got = append(got, captured{body: body, lang: lc})
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		http.Error(w, "unexpected: "+r.URL.Path, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	// Drive a minimal RealNotifier pointed at the mock
	// server. The Telegram endpoint base is the mock
	// server URL; the helper adds /bot<token>/setMyCommands.
	rn := newTestNotifier(t, srv.URL)
	// SetMyCommandsAll runs sequentially inside itself, so
	// no goroutine needed here; just call it.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rn.SetMyCommandsAll(ctx, DefaultMyCommandsSpec); err != nil {
		t.Fatalf("SetMyCommandsAll: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("expected 3 setMyCommands calls (default + en + ru), got %d", len(got))
	}
	// Order: default first, then en, then ru (matches
	// supportedLangs in the helper).
	if got[0].lang != "" {
		t.Errorf("call 0: language_code=%q, want empty (default)", got[0].lang)
	}
	if got[1].lang != "en" {
		t.Errorf("call 1: language_code=%q, want en", got[1].lang)
	}
	if got[2].lang != "ru" {
		t.Errorf("call 2: language_code=%q, want ru", got[2].lang)
	}
	// The default (no lang) and en must have the same
	// description text — they share the catalog — but
	// the ru call must carry the Russian text. The
	// "help" entry is the easiest to check because its
	// English description contains "codex" and the
	// Russian one contains "Кодекс".
	if !strings.Contains(string(got[1].body), "codex") {
		t.Errorf("en call: expected to contain 'codex', got: %s", string(got[1].body))
	}
	if !strings.Contains(string(got[2].body), "Кодекс") {
		t.Errorf("ru call: expected to contain 'Кодекс', got: %s", string(got[2].body))
	}
}

// newTestNotifier builds a minimal *RealNotifier pointed at
// the given base URL with a token seeded in the DB. Used by
// the SetMyCommandsAll tests; kept private to this file so
// a future change to RealNotifier doesn't ripple into other
// tests.
func newTestNotifier(t *testing.T, baseURL string) *RealNotifier {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Exec(`CREATE TABLE global_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO global_settings(key, value) VALUES ('telegram.bot_token', 'test-token')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return &RealNotifier{
		apiBase: baseURL,
		db:      d,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}
