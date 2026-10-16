// Package telegram — htmlsafe_test.go pins the B389 send-boundary
// HTML sanitiser (htmlsafe.go). The contract is behavioural: for
// every input, the string handed to Telegram with parse_mode=HTML
// must be one Telegram accepts. The cases below are the ones the
// live API actually rejected on 2026-10-16, plus the adversarial
// shapes that would reintroduce the same silent drop.
package telegram

import (
	"strings"
	"testing"

	"skygate/internal/i18n"
)

// TestSanitizeTelegramHTML_PlaceholdersEscaped — the exact production
// failure: a catalogue value with a raw <placeholder> went out with
// parse_mode=HTML and Telegram answered
// `400 can't parse entities: Unsupported start tag "username"`.
// sendPlain only logs that, so the user received nothing at all.
func TestSanitizeTelegramHTML_PlaceholdersEscaped(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Использование: /invite <username>", "Использование: /invite &lt;username&gt;"},
		{"Использование: /accept <код>", "Использование: /accept &lt;код&gt;"},
		{"Использование: /mesh create <имя>", "Использование: /mesh create &lt;имя&gt;"},
		{"нормальный текст", "нормальный текст"},
		{"x < 5 and y > 3", "x &lt; 5 and y &gt; 3"},
		{"<3", "&lt;3"},
		{"dangling <", "dangling &lt;"},
		{"<b", "&lt;b"},
	}
	for _, c := range cases {
		if got := sanitizeTelegramHTML(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeTelegramHTML_AllowedTagsPreserved — real markup must
// survive untouched, attributes included, or the fix would trade a
// dropped message for an ugly one.
func TestSanitizeTelegramHTML_AllowedTagsPreserved(t *testing.T) {
	for _, in := range []string{
		"<b>bold</b> and <i>italic</i>",
		"<code>skygate</code>",
		"<pre>col1\tcol2\nv1\tv2</pre>",
		`<a href="https://example.com/x?a=1&amp;b=2">link</a>`,
		`<span class="tg-spoiler">secret</span>`,
		"<blockquote expandable>quote</blockquote>",
		"<u>under</u> <s>strike</s>",
	} {
		if got := sanitizeTelegramHTML(in); got != in {
			t.Errorf("allowed markup changed:\n in: %q\nout: %q", in, got)
		}
	}
}

// TestSanitizeTelegramHTML_Idempotent — the bot mixes values that
// already carry entities (format.go escapes with escapeHTML) with
// values that do not. Running the sanitiser over an already-escaped
// body must not double-escape it.
func TestSanitizeTelegramHTML_Idempotent(t *testing.T) {
	in := "&lt;username&gt; и <b>bold</b> и &amp; и &#171;кавычки&#187; и &#x2603;"
	once := sanitizeTelegramHTML(in)
	if once != in {
		t.Fatalf("already-escaped body changed:\n in: %q\nout: %q", in, once)
	}
	if twice := sanitizeTelegramHTML(once); twice != once {
		t.Errorf("not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
}

// TestSanitizeTelegramHTML_BareAmpersandEscaped — the Windows install
// command in `bot.add_device.platform.windows` starts with
// `& "…tailscale.exe" up …`, and that reply now goes out with
// parse_mode=HTML. Escaping the bare `&` renders identically and
// removes the last way an HTML body can be rejected.
func TestSanitizeTelegramHTML_BareAmpersandEscaped(t *testing.T) {
	cases := []struct{ in, want string }{
		{`& "C:\Program Files\Tailscale\tailscale.exe" up`, `&amp; "C:\Program Files\Tailscale\tailscale.exe" up`},
		{"Q&A", "Q&amp;A"},
		{"a & b", "a &amp; b"},
		{"&amp;", "&amp;"},       // a real entity survives
		{"&#171;", "&#171;"},     // numeric survives
		{"&#x2603;", "&#x2603;"}, // hex survives
		{"&notanentity;", "&amp;notanentity;"},
		{"&;", "&amp;;"},
	}
	for _, c := range cases {
		if got := sanitizeTelegramHTML(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeTelegramHTML_UnbalancedAllowedTagEscaped — Telegram
// rejects an unterminated tag ("Can't find end tag corresponding to
// start tag"), which is what a byte-wise truncation of a formatted
// body produces. Such a tag is escaped rather than dropped.
func TestSanitizeTelegramHTML_UnbalancedAllowedTagEscaped(t *testing.T) {
	got := sanitizeTelegramHTML("<b>open with no close")
	if !strings.Contains(got, "&lt;b&gt;") {
		t.Errorf("unbalanced <b> not escaped: %q", got)
	}
	if strings.Contains(got, "<b>") {
		t.Errorf("unbalanced <b> still present: %q", got)
	}
	// The text itself is never lost.
	if !strings.Contains(got, "open with no close") {
		t.Errorf("body text lost: %q", got)
	}
	// A balanced pair is left alone even when a sibling is unbalanced.
	got = sanitizeTelegramHTML("<b>ok</b> <i>dangling")
	if !strings.Contains(got, "<b>ok</b>") {
		t.Errorf("balanced <b> was escaped: %q", got)
	}
	if !strings.Contains(got, "&lt;i&gt;") {
		t.Errorf("unbalanced <i> not escaped: %q", got)
	}
}

// TestSanitizeTelegramHTML_BalancedBodyUntouched — the whole point of
// the fast path: an ordinary formatted reply comes out byte-identical,
// so this change cannot alter any message that already rendered.
func TestSanitizeTelegramHTML_BalancedBodyUntouched(t *testing.T) {
	in := "<b>Сводка</b>\n\n<code>правил: 12</code>\n\n<i>— Ваш Дворецкий</i>"
	if got := sanitizeTelegramHTML(in); got != in {
		t.Errorf("body changed:\n in: %q\nout: %q", in, got)
	}
	if got := sanitizeTelegramHTML(""); got != "" {
		t.Errorf("empty input = %q, want empty", got)
	}
}

// TestSanitizeTelegramHTML_LiveCatalogueUsageStrings — the four
// commands that were silently dead in production. Their usage text is
// rendered by builders that opt into parse_mode=HTML, so the sanitiser
// is the thing that makes them deliverable. Live proof before the fix
// (bot token, bound chat, POST /sendMessage):
//
//	/invite <username>  -> 400 can't parse entities: Unsupported start tag "username"
//	/mesh join <код>    -> 400 can't parse entities: Unsupported start tag "код"
//	after escaping      -> 200 delivered
func TestSanitizeTelegramHTML_LiveCatalogueUsageStrings(t *testing.T) {
	cat := i18n.New()
	keys := []string{
		"bot.invite.usage_invite",
		"bot.invite.usage_accept",
		"bot.mesh.usage_create",
		"bot.mesh.usage_join",
		"bot.mesh.usage",
	}
	for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
		for _, key := range keys {
			raw := cat.T(lang, key)
			if raw == key {
				t.Fatalf("%s: catalogue key %q missing", lang, key)
			}
			got := sanitizeTelegramHTML(raw)
			// Whatever the catalogue says, the sanitised body must carry
			// no unsupported tag — that is the property Telegram checks.
			for i := 0; i < len(got); i++ {
				if got[i] != '<' {
					continue
				}
				end := strings.IndexByte(got[i:], '>')
				if end < 0 {
					t.Fatalf("%s: %q sanitised to a dangling '<': %q", lang, key, got)
				}
				inner := got[i+1 : i+end]
				if _, ok := telegramTagName(inner); !ok {
					t.Errorf("%s: %q still carries an unsupported tag %q after sanitising",
						lang, key, "<"+inner+">")
				}
				i += end
			}
		}
	}
}

// TestSanitizeTelegramHTML_PlatformInstructionsDeliverable — B389
// converted the /add_device platform instructions from Markdown to
// HTML (<code>/<pre>) and switched that path to parse_mode=HTML. The
// Windows command starts with a bare `&`, which is the other way an
// HTML body gets rejected, so this pins the whole family.
func TestSanitizeTelegramHTML_PlatformInstructionsDeliverable(t *testing.T) {
	cat := i18n.New()
	for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
		for _, platform := range []string{"linux", "windows", "macos", "ios", "android"} {
			key := "bot.add_device.platform." + platform
			raw := cat.Tf(lang, key, "tskey-auth-EXAMPLE")
			if raw == key {
				t.Fatalf("%s: catalogue key %q missing", lang, key)
			}
			got := sanitizeTelegramHTML(raw)
			for i := 0; i < len(got); i++ {
				if got[i] != '<' {
					continue
				}
				end := strings.IndexByte(got[i:], '>')
				if end < 0 {
					t.Fatalf("%s/%s: dangling '<' after sanitising: %q", lang, platform, got)
				}
				inner := got[i+1 : i+end]
				if _, ok := telegramTagName(inner); !ok {
					t.Errorf("%s/%s: unsupported tag <%s> survived", lang, platform, inner)
				}
				i += end
			}
			// The shell command has to reach the user inside a code
			// block, not as literal backticks.
			if platform == "ios" || platform == "android" {
				continue
			}
			if !strings.Contains(got, "<pre>") {
				t.Errorf("%s/%s: no <pre> block for the command:\n%s", lang, platform, got)
			}
			if strings.Contains(got, "`") {
				t.Errorf("%s/%s: Markdown backticks left in an HTML body", lang, platform)
			}
			if !strings.Contains(got, "&lt;HEADSCALE_URL&gt;") {
				t.Errorf("%s/%s: the <HEADSCALE_URL> placeholder was not escaped:\n%s", lang, platform, got)
			}
		}
	}
}

// TestSanitizeTelegramHTML_NoTagWordSurvives — a brute-force guard:
// no matter what the catalogue holds, nothing shaped like a tag
// survives unescaped unless the allowlist accepts it.
func TestSanitizeTelegramHTML_NoTagWordSurvives(t *testing.T) {
	adversarial := []string{
		"</b><script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"<b >space</b >",
		"<B>upper</B>",
		"<code><target></code>",
		"<a href='x'>y</a>",
	}
	for _, in := range adversarial {
		got := sanitizeTelegramHTML(in)
		for i := 0; i < len(got); i++ {
			if got[i] != '<' {
				continue
			}
			end := strings.IndexByte(got[i:], '>')
			if end < 0 {
				t.Fatalf("input %q produced a dangling '<': %q", in, got)
			}
			inner := got[i+1 : i+end]
			if _, ok := telegramTagName(inner); !ok {
				t.Errorf("input %q left unsupported tag <%s> in %q", in, inner, got)
			}
			i += end
		}
	}
}
