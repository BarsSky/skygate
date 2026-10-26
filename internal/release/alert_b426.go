// internal/release/alert_b426.go — B426 (2026-10-26).
//
// «В АЛЕРТЕ ПЕЧАТАЕТСЯ СЫРОЙ MARKDOWN».
//
// Measured: the update-available message the operator received contained
//
//	## v1.5.171 — the update-available alert arrived ~4 hours late (B425)
//
// — the release body's Markdown heading, shown literally, in a message that had already been
// converted to HTML by B391/B421. Two changes collided: FormatAlert was written when alerts
// went out as a fenced plain-text block (where `##` at least looked like text), and B421
// moved the alert transport to parse_mode=HTML, where Markdown is not a markup language at
// all — so it survives verbatim.
//
// WHAT THIS FILE DOES. The update notification becomes a COMPOSED alert (the B391 composer):
// an i18n title, the three facts an operator acts on (running / latest / released), and the
// release body as DETAILS after being reduced to readable text. The cleaning is a pure
// function with a table test, because "strip Markdown" is exactly the kind of regex that
// quietly eats code fences and URLs.
package release

import (
	"fmt"
	"strings"
	"time"

	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// maxNoteLines / maxNoteChars bound the excerpt that reaches a phone. The composer truncates
// as well (it always says how much it dropped), so this is a second, deliberate bound: a
// release body is documentation, and an alert is a notification.
const (
	maxNoteLines = 12
	maxNoteChars = 700
)

// UpdateAlertTitleKey is the catalogue key of the notification title.
const UpdateAlertTitleKey = "alert.update_available.title"

// cleanReleaseNotes turns a GitHub release body (Markdown) into plain, readable lines.
//
// Rules, each measured against the bodies this repo actually publishes:
//   - fenced code blocks are dropped: they are commands, not news, and the composer escapes
//     them into noise;
//   - ATX headings (`## v1.5.171 — …`) lose the hashes but keep the text, so the release
//     title still reads as a title;
//   - `**bold**`, `__bold__`, backticks and `[text](url)` links lose their markers (a link
//     keeps `text (url)` so the operator can still follow it);
//   - leading list markers are kept as "-" (they carry meaning), blank lines collapse, and
//     the result is capped by both line count and characters.
func cleanReleaseNotes(body string) string {
	var out []string
	inFence := false
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		trimmed = stripHeading(trimmed)
		trimmed = replaceMarkdownLinks(trimmed)
		trimmed = strings.NewReplacer("**", "", "__", "", "`", "").Replace(trimmed)
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	// Collapse runs of blank lines and trim the ends.
	var compact []string
	for _, l := range out {
		if l == "" && len(compact) > 0 && compact[len(compact)-1] == "" {
			continue
		}
		compact = append(compact, l)
	}
	for len(compact) > 0 && compact[len(compact)-1] == "" {
		compact = compact[:len(compact)-1]
	}
	if len(compact) > maxNoteLines {
		compact = append(compact[:maxNoteLines], "…")
	}
	res := strings.Join(compact, "\n")
	if len(res) > maxNoteChars {
		res = res[:maxNoteChars] + "…"
	}
	return res
}

// stripHeading removes ATX heading markers ("## Title" → "Title").
func stripHeading(line string) string {
	if !strings.HasPrefix(line, "#") {
		return line
	}
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i == 0 || i > 6 {
		return line
	}
	rest := strings.TrimSpace(line[i:])
	if rest == "" {
		return ""
	}
	return rest
}

// replaceMarkdownLinks rewrites `[text](url)` as `text (url)`; a bare `](` mismatch is left
// alone (release bodies contain literal brackets in code and in prose).
func replaceMarkdownLinks(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '[' {
			b.WriteByte(s[i])
			continue
		}
		close := strings.IndexByte(s[i:], ']')
		if close < 0 || i+close+1 >= len(s) || s[i+close+1] != '(' {
			b.WriteByte(s[i])
			continue
		}
		open := i + close + 1
		end := strings.IndexByte(s[open:], ')')
		if end < 0 {
			b.WriteByte(s[i])
			continue
		}
		text := s[i+1 : i+close]
		url := s[open+1 : open+end]
		if strings.HasPrefix(url, "http") {
			fmt.Fprintf(&b, "%s (%s)", text, url)
		} else {
			b.WriteString(text)
		}
		i = open + end
	}
	return b.String()
}

// formatReleased renders the release timestamp for a human. GitHub sends RFC 3339 in the
// JSON (Release.PublishedAt is a string); an unparseable value is shown as-is rather than
// swallowed, because a date the operator cannot read is still better than a dash.
func formatReleased(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "—"
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC().Format("2006-01-02 15:04 UTC")
	}
	return raw
}

// BuildUpdateAlert is the composed notification for a newer release. Pure, so the message
// the operator will read is testable without a notifier, a clock or a network.
func BuildUpdateAlert(current string, latest *Release, lang string) telegram.Alert {
	if lang == "" {
		lang = i18n.LangRU
	}
	if latest == nil {
		latest = &Release{}
	}
	return telegram.Alert{
		Severity: telegram.SeverityInfo,
		TitleKey: UpdateAlertTitleKey,
		Facts: []telegram.AlertFact{
			{Label: i18n.T(lang, "alert.update_available.fact_running"), Value: current},
			{Label: i18n.T(lang, "alert.update_available.fact_latest"), Value: latest.TagName},
			{Label: i18n.T(lang, "alert.update_available.fact_released"), Value: formatReleased(latest.PublishedAt)},
		},
		Details:  strings.TrimSpace(cleanReleaseNotes(latest.Body) + "\n\n→ " + latest.HTMLURL),
		DedupKey: "release.update_available." + latest.TagName,
		// The tag itself is the dedup identity; a day is longer than any tick, shorter than
		// the next release, and a restart is already covered by release.notified_tag (B425).
		DedupTTL: 24 * time.Hour,
		Lang:     lang,
	}
}
