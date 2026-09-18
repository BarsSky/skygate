// preauth_result_fields_test.go — 2026-09-18 regression guard for the
// template/data mismatch class.
//
// THE BUG (found during the R7 audit)
// ----------------------------------
// user/preauth_result.html referenced two fields that NO handler ever
// set:
//
//	{{.PreauthKey}}  → handlers pass "Key", so the re-register banner
//	                   rendered `tailscale up --authkey=<no value>`
//	{{.OSLabel}}     → set by nobody, so the page printed
//	                   "instructions for <no value>" in three places
//
// Go templates render a missing map key as "<no value>" instead of
// failing, so this is a silent, user-visible defect. The SAME class had
// already bitten this exact template once before (v0.18.1, {{.ControlURL}}
// rendering an empty --login-server=), which is why handlers.go now
// auto-injects a few fields — see handlers.go:778-791.
//
// This test makes the class fail in CI: it extracts every field the
// template references, subtracts the auto-injected ones, and requires the
// remainder to be supplied by at least one of the three issuance
// handlers. It is deliberately a union-check (not per-handler) because the
// three sites legitimately supply different subsets (ReregisteredFor only
// exists on the re-register path, guarded by {{if .ReregisteredFor}}).
package my

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Fields handlers.go injects into every RenderWithLayout call. Keep in
// sync with the data["..."] assignments in internal/handlers/handlers.go
// (lines ~712-836); the test asserts the ones this template needs.
var autoInjectedTemplateFields = map[string]bool{
	"Lang": true, "Page": true, "Username": true, "IsAdmin": true,
	"Theme": true, "ThemeLabel": true, "Version": true, "ControlURL": true,
	"DisplayFont": true, "DisplayScale": true, "DisplaySelBg": true,
	"UnreadCount": true, "UnreadNotifications": true,
	"GitHubOwner": true, "GitHubRepo": true,
	"BreadcrumbSection": true, "BreadcrumbPage": true,
	// Template builtins / layout plumbing that are not handler data.
	"FlashError": true, "FlashOk": true, "Toast": true,
}

var (
	tmplFieldRe = regexp.MustCompile(`\{\{\s*\.([A-Z][A-Za-z0-9]*)`)
	// {{/* ... */}} — non-greedy so multiple comments on one line each match.
	tmplCommentRe = regexp.MustCompile(`(?s)\{\{/\*.*?\*/\}\}`)
	// Quoted map keys of the form "Field": in the handler sources.
	goMapKeyRe = regexp.MustCompile(`"([A-Z][A-Za-z0-9]*)":`)
)

func TestOSLabelMapping(t *testing.T) {
	cases := map[string]string{
		"android": "Android",
		"ios":     "iOS",
		"linux":   "Linux",
		"macos":   "macOS",
		"windows": "Windows",
		"":        "your device",
		"plan9":   "plan9", // unknown codes pass through rather than blanking out
	}
	for in, want := range cases {
		if got := osLabel(in); got != want {
			t.Errorf("osLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPreauthResultTemplateFieldsAreSupplied is the actual guard.
func TestPreauthResultTemplateFieldsAreSupplied(t *testing.T) {
	// Test CWD is internal/feature/my, so the repo root is three levels up.
	root := filepath.Join("..", "..", "..")

	tmplPath := filepath.Join(root, "internal", "handlers", "templates", "user", "preauth_result.html")
	raw, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	// Strip Go-template comments before scanning: the file legitimately
	// documents the old {{.PreauthKey}} form in a {{/* ... */}} block, and
	// that is not a field reference.
	body := strings.Join(strings.Split(string(raw), "\n"), "\n")
	body = tmplCommentRe.ReplaceAllString(body, "")

	referenced := map[string]bool{}
	for _, m := range tmplFieldRe.FindAllStringSubmatch(body, -1) {
		referenced[m[1]] = true
	}
	if len(referenced) == 0 {
		t.Fatal("parsed 0 fields out of preauth_result.html — the regex is wrong, so this guard checks nothing")
	}

	// Union of everything the three issuance handlers pass.
	supplied := map[string]bool{}
	handlers := []string{
		filepath.Join(root, "internal", "feature", "my", "preauth.go"),
		filepath.Join(root, "internal", "feature", "my", "keys.go"),
		filepath.Join(root, "internal", "feature", "my", "devices.go"),
	}
	for _, h := range handlers {
		src, err := os.ReadFile(h)
		if err != nil {
			t.Fatalf("read %s: %v", h, err)
		}
		for _, m := range goMapKeyRe.FindAllStringSubmatch(string(src), -1) {
			supplied[m[1]] = true
		}
	}

	var missing []string
	for f := range referenced {
		if autoInjectedTemplateFields[f] || supplied[f] {
			continue
		}
		missing = append(missing, f)
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("preauth_result.html references fields that no handler supplies and the layout does not inject: %s\n"+
			"Go templates render these as the literal string \"<no value>\", which is exactly the "+
			"{{.PreauthKey}} / {{.OSLabel}} defect fixed on 2026-09-18. Either pass them from the handler "+
			"or remove them from the template.",
			strings.Join(missing, ", "))
	}

	// Positive pins for the two fields that were actually broken, so a
	// future rename of the handler keys is caught with a clear message.
	for _, required := range []string{"Key", "OSLabel", "OS", "Expires"} {
		if !supplied[required] {
			t.Errorf("no handler supplies %q for preauth_result.html any more", required)
		}
	}
}
