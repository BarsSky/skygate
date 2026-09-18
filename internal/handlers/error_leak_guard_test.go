// error_leak_guard_test.go — 2026-09-18: guards the "raw error text on an
// HTML page" class (R6) against growth.
//
// THE CLASS
// ---------
// skygate has no shared error surface: meanwhile the only inline mechanism
// is a redirect with ?err= plus a {{.FlashError}} block in the template.
// Handlers that reach for
//
//	http.Error(w, err.Error(), http.StatusInternalServerError)
//
// therefore do two bad things at once:
//
//  1. replace the whole page with a text/plain body (the operator's
//     "the DB error is shown as a separate page" report), and
//  2. hand the user the raw error text — which for DB failures is a SQL
//     snippet, a column name, or a driver message (information disclosure,
//     audit finding U2).
//
// WHY A GUARD AND NOT A BIG REFACTOR
// ----------------------------------
// There are ~480 http.Error calls in total, but most of them are
// legitimate: "forbidden" (403 on an admin URL), form-validation messages,
// and JSON endpoints. The genuinely harmful subset is the one that
// interpolates an error value, and at the time of writing that is 43 sites
// across 20 files — a bounded, reviewable amount of work, not a rewrite.
//
// This test freezes that number. It may only go DOWN: each converted
// handler should lower the baseline in the same commit. Adding a new site
// fails the build with the file list, so the class cannot quietly grow
// while the migration is in progress.
//
// WHEN THE BASELINE REACHES 0: replace this test with a hard rule (no
// http.Error with an interpolated error anywhere in these packages).
package handlers

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// rawErrorHTTPErrorBaseline is the number of
// `http.Error(w, <error value>, ...)` sites in the HTML-serving packages as
// of 2026-09-18. Lower it in the same commit that converts a handler.
//
// Note the count covers EVERY string form that interpolates an error —
// `err.Error()`, `"prefix: "+err.Error()`, `fmt.Sprintf("…: %v", err)` and
// the multi-line variants — which is why it is larger than a naive grep for
// `http.Error(w, err.Error()`. All of them leak the internal error text
// into the response body, so all of them belong to this class.
const rawErrorHTTPErrorBaseline = 101

// Scanned packages: everything that renders templates for a browser.
var rawErrorScanRoots = []string{
	filepath.Join("..", "feature"),
	filepath.Join("..", "handlers"),
}

func isRawErrorHTTPError(line string) bool {
	trimmed := strings.TrimSpace(line)
	// Comments explain the pattern; they are not sites.
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
		return false
	}
	idx := strings.Index(line, "http.Error(")
	if idx < 0 {
		return false
	}
	rest := line[idx+len("http.Error("):]
	// Any of these interpolate an error value into the response body.
	for _, bad := range []string{"err.Error()", "listErr.Error()", "lerr.Error()", "perr.Error()", "gerr.Error()", "kerr.Error()"} {
		if strings.Contains(rest, bad) {
			return true
		}
	}
	return false
}

func TestNoNewRawErrorPages(t *testing.T) {
	counts := map[string]int{}
	total := 0

	for _, root := range rawErrorScanRoots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			n := 0
			for _, line := range strings.Split(string(data), "\n") {
				if isRawErrorHTTPError(line) {
					n++
				}
			}
			if n > 0 {
				rel := filepath.ToSlash(strings.TrimPrefix(path, ".."+string(os.PathSeparator)))
				counts[rel] = n
				total += n
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	// Print the debt so it is visible in CI output, worst first.
	type kv struct {
		f string
		n int
	}
	var rows []kv
	for f, n := range counts {
		rows = append(rows, kv{f, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].f < rows[j].f
	})
	t.Logf("raw-error http.Error sites: %d across %d files (baseline %d)",
		total, len(counts), rawErrorHTTPErrorBaseline)
	for _, r := range rows {
		t.Logf("  %3d  %s", r.n, r.f)
	}

	if total > rawErrorHTTPErrorBaseline {
		t.Errorf("raw-error http.Error sites went UP: %d, baseline %d.\n"+
			"A new handler is answering with http.Error(w, err.Error(), ...): that replaces the "+
			"page with a text/plain body AND leaks the internal error text.\n"+
			"Use the existing pattern instead: redirect back with ?err=<i18n key> and let the "+
			"template's {{.FlashError}} block render it (see internal/feature/admin/exit_nodes.go "+
			"AdminExitNodes for a GET, or the POST handlers in the same file).\n"+
			"See docs/plans/2026-09-17-sqlite-pg-and-headscale-hardening.md R6.",
			total, rawErrorHTTPErrorBaseline)
	}
}
