// internal/release/alert_b426_test.go — B426 (2026-10-26).
//
// The operator's screenshot showed the update notification carrying
//
//	## v1.5.171 — the update-available alert arrived ~4 hours late (B425)
//
// i.e. the Markdown heading of the release body, verbatim, in a message that B421 had already
// moved to parse_mode=HTML. FormatAlert had been written for the old fenced plain-text block;
// Markdown means nothing in HTML, so it arrived as characters.
//
// These tests pin the replacement: the notification is a COMPOSED alert (i18n title + the
// three facts + a cleaned excerpt), the cleaner is a table-tested pure function, and a sink
// that only knows SendAlert still gets the legacy sentence (no silent loss for Noop/tests).
package release

import (
	"context"
	"strings"
	"testing"

	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// TestB426_CleanReleaseNotesIsReadable is the table: what the operator reads instead of raw
// Markdown. Every case is a shape this repository's release bodies actually contain.
func TestB426_CleanReleaseNotesIsReadable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		drop []string
	}{
		{
			name: "heading loses its hashes",
			in:   "## v1.5.171 — the alert arrived late (B425)\n\nBody text.",
			want: []string{"v1.5.171 — the alert arrived late (B425)", "Body text."},
			drop: []string{"##"},
		},
		{
			name: "bold and code markers go",
			in:   "**Date:** 2026-10-26 · **Compatibility:** `none`.",
			want: []string{"Date: 2026-10-26 · Compatibility: none."},
			drop: []string{"**", "`"},
		},
		{
			name: "links keep text and url",
			in:   "See [docs](https://example.test/docs) and [x](not-a-url).",
			want: []string{"See docs (https://example.test/docs) and x."},
			drop: []string{"]("},
		},
		{
			name: "fenced code is dropped",
			in:   "Intro\n\n```bash\ngh run rerun 1 --failed\n```\n\nOutro",
			want: []string{"Intro", "Outro"},
			drop: []string{"gh run rerun"},
		},
		{
			name: "blank runs collapse",
			in:   "a\n\n\n\nb",
			want: []string{"a", "b"},
		},
		{
			name: "long bodies are capped",
			in:   strings.Repeat("line\n", 40),
			want: []string{"…"},
		},
	}
	for _, c := range cases {
		got := cleanReleaseNotes(c.in)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: cleaned notes lost %q:\n%s", c.name, w, got)
			}
		}
		for _, d := range c.drop {
			if strings.Contains(got, d) {
				t.Errorf("%s: cleaned notes still contain %q:\n%s", c.name, d, got)
			}
		}
	}
}

// TestB426_UpdateAlertIsComposed pins the message the operator receives: an i18n title, the
// facts to act on, the released date formatted for a human, and a dedup key per tag.
func TestB426_UpdateAlertIsComposed(t *testing.T) {
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	latest := &Release{
		TagName:     "v9.9.9",
		PublishedAt: "2026-10-26T16:33:11Z",
		HTMLURL:     "https://example.test/releases/tag/v9.9.9",
		Body:        "## v9.9.9 — something\n\n**Fixed:** the thing.",
	}
	a := BuildUpdateAlert("v1.0.0", latest, i18n.LangRU)

	if a.TitleKey != UpdateAlertTitleKey {
		t.Fatalf("title key = %q", a.TitleKey)
	}
	if a.Severity != telegram.SeverityInfo {
		t.Fatalf("severity = %v", a.Severity)
	}
	if len(a.Facts) != 3 {
		t.Fatalf("facts = %+v, want running/latest/released", a.Facts)
	}
	joined := ""
	for _, f := range a.Facts {
		joined += f.Label + "=" + f.Value + ";"
	}
	for _, want := range []string{"v1.0.0", "v9.9.9", "2026-10-26 16:33 UTC"} {
		if !strings.Contains(joined, want) {
			t.Errorf("facts lost %q: %s", want, joined)
		}
	}
	if strings.Contains(a.Details, "##") || strings.Contains(a.Details, "**") {
		t.Errorf("details still carry Markdown:\n%s", a.Details)
	}
	if !strings.Contains(a.Details, "https://example.test/releases/tag/v9.9.9") {
		t.Errorf("details lost the release URL:\n%s", a.Details)
	}
	if a.DedupKey != "release.update_available.v9.9.9" {
		t.Errorf("dedup key = %q (a repeated announce of the same tag must be suppressible)", a.DedupKey)
	}
	if a.DedupTTL <= 0 {
		t.Errorf("DedupTTL = %v", a.DedupTTL)
	}
}

// b426Sink records both entry points so the test can prove WHICH one the monitor used.
type b426Sink struct {
	events []telegram.Alert
	legacy []string
}

func (s *b426Sink) SendAlertEvent(a telegram.Alert) int64 {
	s.events = append(s.events, a)
	return int64(len(s.events))
}

func (s *b426Sink) SendAlert(text string) int64 {
	s.legacy = append(s.legacy, text)
	return int64(len(s.legacy))
}

// legacyOnly hides SendAlertEvent, so the monitor must fall back to the plain sentence.
type legacyOnly struct{ inner *b426Sink }

func (l legacyOnly) SendAlert(text string) int64 { return l.inner.SendAlert(text) }

// TestB426_ComposedPathIsUsedWhenAvailable: the monitor must send the composed alert through
// a sink that supports it, and fall back to the legacy sentence otherwise.
func TestB426_ComposedPathIsUsedWhenAvailable(t *testing.T) {
	srv := b425Server(t, "v9.9.9", 200, nil)
	composed := &b426Sink{}
	m := &Monitor{HTTP: srv.Client(), APIBase: srv.URL, Current: "v1.0.0", Notifier: composed}
	m.tick(context.Background())
	if len(composed.events) != 1 {
		t.Fatalf("the composed path was not used (events=%d, legacy=%d)", len(composed.events), len(composed.legacy))
	}
	if composed.events[0].TitleKey != UpdateAlertTitleKey {
		t.Fatalf("title key = %q", composed.events[0].TitleKey)
	}

	srv2 := b425Server(t, "v9.9.9", 200, nil)
	legacy := &b426Sink{}
	m2 := &Monitor{HTTP: srv2.Client(), APIBase: srv2.URL, Current: "v1.0.0", Notifier: legacyOnly{legacy}}
	m2.tick(context.Background())
	if len(legacy.legacy) != 1 {
		t.Fatalf("a SendAlert-only sink did not get the legacy sentence (legacy=%d, events=%d)",
			len(legacy.legacy), len(legacy.events))
	}
}
