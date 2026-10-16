// Package telegram — htmlsafe.go implements the B389 send-boundary
// HTML sanitiser.
//
// Why this exists (measured live on the reference host, 2026-10-16):
// the bot catalogue and the reply builders disagreed about who owns
// HTML escaping. Values such as `bot.invite.usage_invite`
// ("Использование: /invite <username>") carried a RAW `<placeholder>`
// while their builder called markHTMLReply(), so the request went out
// with parse_mode=HTML and Telegram answered
//
//	400 Bad Request: can't parse entities: Unsupported start tag "username"
//
// sendPlain only logs that rejection, so the user received NOTHING —
// four commands (/invite, /accept, /mesh create, /mesh join without
// arguments) were silently dead. The mirror-image defect was just as
// bad: 61 catalogue values carried `&lt;placeholder&gt;` and were sent
// WITHOUT parse_mode, so the user read `&lt;username&gt;` literally.
//
// Neither half can be fixed value-by-value without the other half
// coming back, because the same key is rendered by both paths
// depending on the branch. The fix is to stop treating escaping as a
// property of the catalogue value and make it a property of the SEND
// PATH: values carry logical text (`<target>`, `<b>bold</b>`), and
// this sanitiser prepares the body for parse_mode=HTML at the last
// possible moment (after trimming and splitting, so a truncation can
// no longer leave a half tag behind).
//
// Rules (all pinned by TestSanitizeTelegramHTML_*):
//
//   - a tag whose NAME Telegram accepts (see allowedTelegramHTMLTags)
//     is passed through verbatim, attributes included
//     (`<a href="...">`, `<span class="tg-spoiler">`);
//   - any other `<word>` is escaped to `&lt;word&gt;` — that is the
//     `/invite <username>` class, and it can no longer 400;
//   - a `<` that cannot start a tag (`<3`, `< x`, `<=`) is escaped;
//   - an allowed tag that is NOT balanced (an open tag with no close)
//     is escaped too: Telegram rejects unbalanced entities with
//     "Can't find end tag corresponding to start tag", and a dropped
//     message is worse than a visible literal `<b>`;
//   - existing entities (`&lt;`, `&gt;`, `&amp;`, `&#123;`, `&#x1F;`)
//     are left alone, so the function is idempotent and mixes freely
//     with format.go's escapeHTML-based helpers;
//   - a bare `&` (one that does not start a valid entity) is escaped
//     to `&amp;`, which renders identically — the Windows install
//     command in the catalogue starts with `& "…tailscale.exe" up …`,
//     and an entity-free `&` is the one remaining way an HTML body
//     can be rejected.
package telegram

import (
	"strings"
	"unicode"
)

// allowedTelegramHTMLTags is the tag vocabulary Telegram accepts in
// parse_mode=HTML. Telegram's own list (core.telegram.org/bots/api
// → "HTML style") is: b/strong, i/em, u/ins, s/strike/del, a, code,
// pre, blockquote, span class="tg-spoiler", tg-spoiler, tg-emoji.
// The rich-message vocabulary (B389 verified: h1-h6, table, ul/ol,
// details, footer, mark, sub, sup, tg-time, ...) is deliberately NOT
// allowed here — it is only valid in rich_message.html, and this
// function guards parse_mode=HTML.
var allowedTelegramHTMLTags = map[string]bool{
	"b": true, "strong": true,
	"i": true, "em": true,
	"u": true, "ins": true,
	"s": true, "strike": true, "del": true,
	"a": true, "code": true, "pre": true, "blockquote": true,
	"span": true, "tg-spoiler": true, "tg-emoji": true,
}

// sanitizeTelegramHTML prepares a message body for parse_mode=HTML.
// It returns a body Telegram is guaranteed to accept: no unsupported
// start tag, no unbalanced entity. See the package comment for the
// rules and for why this is a send-boundary concern.
//
// The function never returns an empty string for a non-empty input:
// when every tag has to be escaped the text itself is preserved.
func sanitizeTelegramHTML(s string) string {
	if s == "" {
		return s
	}
	// Fast path: with no "<" there is no tag to validate, and with no
	// "&" there is no entity to check.
	if !strings.ContainsAny(s, "<&") {
		return s
	}

	var b strings.Builder
	b.Grow(len(s) + 16)
	runes := []rune(s)

	// First pass: escape everything that is not a supported tag or a
	// valid entity. Balance is checked in a second pass (a tag can
	// only be judged balanced once the whole body is known).
	for i := 0; i < len(runes); {
		if runes[i] == '&' {
			if n, ok := entityLen(runes[i:]); ok {
				b.WriteString(string(runes[i : i+n]))
				i += n
			} else {
				b.WriteString("&amp;")
				i++
			}
			continue
		}
		if runes[i] != '<' {
			b.WriteRune(runes[i])
			i++
			continue
		}
		end := indexRune(runes, i+1, '>')
		if end < 0 {
			// No closing ">" at all: a dangling "<" can never be a
			// tag, so show it as text.
			b.WriteString("&lt;")
			i++
			continue
		}
		inner := string(runes[i+1 : end])
		if _, ok := telegramTagName(inner); ok {
			b.WriteString("<")
			b.WriteString(inner)
			b.WriteString(">")
		} else {
			b.WriteString("&lt;")
			b.WriteString(inner)
			b.WriteString("&gt;")
		}
		i = end + 1
	}

	out := b.String()
	return escapeUnbalancedTags(out)
}

// telegramTagName reports whether the text between "<" and ">" starts
// a tag Telegram accepts, and returns that tag's lower-cased name.
// It rejects a name that is empty or does not start with a letter, so
// "<3", "< x" and "<= 5" are treated as text rather than as tags.
func telegramTagName(inner string) (string, bool) {
	body := strings.TrimPrefix(inner, "/")
	if body == "" {
		return "", false
	}
	// The name ends at the first character that cannot be part of it
	// (whitespace, "/" for a self-closing tag, or the end).
	name := body
	if idx := strings.IndexFunc(body, func(r rune) bool {
		return unicode.IsSpace(r) || r == '/'
	}); idx >= 0 {
		name = body[:idx]
	}
	if name == "" {
		return "", false
	}
	lower := strings.ToLower(name)
	if !allowedTelegramHTMLTags[lower] {
		return "", false
	}
	return lower, true
}

// escapeUnbalancedTags escapes any allowed tag whose open/close counts
// disagree. Telegram rejects the whole message with "Can't find end
// tag corresponding to start tag" in that case, and a truncated body
// (trimForTelegram) is the realistic way to produce one.
func escapeUnbalancedTags(s string) string {
	// Count opens and closes for every allowed tag name.
	counts := map[string]int{}
	runes := []rune(s)
	for i := 0; i < len(runes); {
		if runes[i] != '<' {
			i++
			continue
		}
		end := indexRune(runes, i+1, '>')
		if end < 0 {
			i++
			continue
		}
		inner := string(runes[i+1 : end])
		if name, ok := telegramTagName(inner); ok {
			if strings.HasPrefix(inner, "/") {
				counts[name]--
			} else {
				counts[name]++
			}
		}
		i = end + 1
	}
	unbalanced := map[string]bool{}
	for name, n := range counts {
		if n != 0 {
			unbalanced[name] = true
		}
	}
	if len(unbalanced) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(runes); {
		if runes[i] != '<' {
			b.WriteRune(runes[i])
			i++
			continue
		}
		end := indexRune(runes, i+1, '>')
		if end < 0 {
			b.WriteString("&lt;")
			i++
			continue
		}
		inner := string(runes[i+1 : end])
		if name, ok := telegramTagName(inner); ok && unbalanced[name] {
			b.WriteString("&lt;")
			b.WriteString(inner)
			b.WriteString("&gt;")
		} else {
			b.WriteString("<")
			b.WriteString(inner)
			b.WriteString(">")
		}
		i = end + 1
	}
	return b.String()
}

// indexRune returns the index of the first occurrence of r in runes
// at or after start, or -1.
func indexRune(runes []rune, start int, r rune) int {
	for i := start; i < len(runes); i++ {
		if runes[i] == r {
			return i
		}
	}
	return -1
}

// entityLen reports the length, in runes, of the valid HTML entity at
// the start of runes (which begins with '&'), or ok=false when the text
// is not an entity Telegram accepts. Recognised forms: the five named
// entities, a decimal reference (&#123;) and a hex reference (&#x1F;).
func entityLen(runes []rune) (int, bool) {
	if len(runes) == 0 || runes[0] != '&' {
		return 0, false
	}
	end := indexRune(runes, 1, ';')
	if end < 0 || end > 12 {
		return 0, false
	}
	body := string(runes[1:end])
	switch body {
	case "amp", "lt", "gt", "quot", "apos":
		return end + 1, true
	}
	if strings.HasPrefix(body, "#x") || strings.HasPrefix(body, "#X") {
		digits := body[2:]
		if digits == "" || len(digits) > 6 {
			return 0, false
		}
		for _, r := range digits {
			if !unicode.Is(unicode.ASCII_Hex_Digit, r) {
				return 0, false
			}
		}
		return end + 1, true
	}
	if strings.HasPrefix(body, "#") {
		digits := body[1:]
		if digits == "" || len(digits) > 7 {
			return 0, false
		}
		for _, r := range digits {
			if r < '0' || r > '9' {
				return 0, false
			}
		}
		return end + 1, true
	}
	return 0, false
}
