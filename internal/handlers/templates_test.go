package handlers

import (
	"embed"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"skygate/internal/i18n"
)

//go:embed templates
var templatesFS embed.FS

// TestLoadTemplates — LoadTemplates() panics on any template parse error
// (including a missing {{.T}} helper when a template references it).
// Catches typos like {{ .T "foo" }} or {{.t "foo" }} (lowercase).
func TestLoadTemplates(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("LoadTemplates panicked: %v", r)
		}
	}()
	_ = LoadTemplates()
}

// tCallRe matches `{{ t "key" arg1 arg2 ... }}` and `{{ t "key" }}`.
// Each arg is a Go template path: identifier or dotted (.Foo.Bar) form.
var tCallRe = regexp.MustCompile(`\{\{\s*t(f)?\s+"([\w.]+)"((?:\s+\.?\w+(?:\.\w+)*)*)\s*\}\}`)

// TestTemplateArgsMatchCatalog — every `{{t ...}}` or `{{tf ...}}` call
// in the templates must pass exactly the right number of args for the
// catalog key. Mismatch fails at template execute time with
// "wrong number of args for t: want 1 got N" (regression guard for
// the v0.16.6 layout.html bug — `{{t "update.banner_body" .Version
// .UpdateLatest.TagName}}` should have been `{{tf ...}}`).
func TestTemplateArgsMatchCatalog(t *testing.T) {
	countPlaceholders := func(s string) int {
		// Each %s / %d / %v etc. counts as one placeholder.
		// `%%` is a literal % (escape), not a placeholder.
		// Replace `%%` with a sentinel, count remaining `%`, restore.
		s = strings.ReplaceAll(s, "%%", "\x00")
		return strings.Count(s, "%")
	}

	// Load the EN catalog as source of truth (parity test already
	// ensures ru matches en for placeholder count).
	enValues := map[string]string{}
	ruValues := map[string]string{}
	collect := func(target map[string]string, prefix string) {
		// We can't import the package-internal catalogs, but i18n
		// exposes a public New() that uses them. We just need to
		// enumerate via the test hook below.
		_ = target
		_ = prefix
	}
	_ = collect

	// Walk the templates.
	var bad []string
	err := fs.WalkDir(templatesFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		data, err := templatesFS.ReadFile(path)
		if err != nil {
			return err
		}
		// Strip {{/* ... */}} comments to avoid matching demo code.
		commentRe := regexp.MustCompile(`\{\{/\*.*?\*/\}\}`)
		clean := commentRe.ReplaceAllString(string(data), "")
		for _, m := range tCallRe.FindAllStringSubmatch(clean, -1) {
			isFormat := m[1] == "f"
			key := m[2]
			args := strings.Fields(strings.TrimSpace(m[3]))
			nargs := len(args)

			// Look up catalog value (try EN first, then RU as fallback).
			cat := i18n.New()
			val := cat.T(i18n.LangEN, key)
			if val == key {
				val = cat.T(i18n.LangRU, key)
			}
			if val == key {
				// Key not in catalog — covered by other tests.
				continue
			}
			expected := countPlaceholders(val)
			if isFormat {
				if nargs != expected {
					bad = append(bad, keyPath(path, key, nargs, expected, "tf"))
				}
			} else {
				if nargs != 0 {
					bad = append(bad, keyPath(path, key, nargs, 0, "t"))
				}
			}
			_ = enValues
			_ = ruValues
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(bad) > 0 {
		t.Fatalf("template/catalog arg mismatch:\n%s", strings.Join(bad, "\n"))
	}
}

func keyPath(path, key string, got, want int, fn string) string {
	return "  " + path + ": " + fn + " \"" + key + "\" passed " +
		i2s(got) + " arg(s), catalog expects " + i2s(want)
}

func i2s(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
