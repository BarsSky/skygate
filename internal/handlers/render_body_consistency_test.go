package handlers

// render_body_consistency_test.go — static check that every
// RenderWithLayout(name, ...) call in the admin handlers resolves
// to a body template that actually exists.
//
// Why this matters: the renderBody funcmap in templates.go
// transforms the name as follows:
//
//   base := strings.ReplaceAll(name, "/", "-")
//   base = strings.TrimSuffix(base, ".html")
//   defineName := "body-" + base
//
// So:
//
//   "admin/control_planes.html" -> "admin-control_planes" -> "body-admin-control_planes" ✓
//   "admin-control-planes"      -> "admin-control-planes"  -> "body-admin-control-planes" ✗ (underscore lost!)
//
// Pre-v0.33.1.3, 6 admin handlers passed the hyphenated form
// ("admin-control-planes", "admin-derp-config", "admin-user-subnet",
// "admin-user-control-plane", "admin-backup") instead of the
// file-path form ("admin/<file>.html"). The renderBody transform
// preserves the hyphen, so the resolved body name was
// "body-admin-control-planes" (with hyphens) which the templates
// do not define. The pages returned 200 with an empty body
// (silent fail — renderBody funcmap ignores the
// "body-admin-X is undefined" error so the layout prints
// an empty string).
//
// The fix: every RenderWithLayout call uses the
// file-path form "admin/<template>.html". This test reads the
// admin handler source files, extracts every RenderWithLayout
// name argument via the Go AST, runs the transform, and asserts
// the result matches a defined body template.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// renderCallSite captures a single RenderWithLayout call for
// error reporting.
type renderCallSite struct {
	File string // absolute path (for log only)
	Line int
	Name string // the string passed as the 3rd arg
}

// extractRenderNames parses a Go file with the AST and returns
// the (file, line, name) of every Backend.RenderWithLayout(name, ...)
// call. Using the AST avoids the regex-over-Go-source pitfalls
// (string literals with embedded quotes, multi-line calls, etc.).
func extractRenderNames(path string) ([]renderCallSite, error) {
	fset := token.NewFileSet()
	src, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []renderCallSite
	ast.Inspect(src, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// We want <receiver>.Backend.RenderWithLayout(...).
		// The receiver chain is s.Backend — sel.X is the
		// SelectorExpr `s.Backend`, not a bare Ident. So
		// accept any of:
		//   x.Backend.RenderWithLayout(...)
		//   s.Backend.RenderWithLayout(...)
		// by matching the method name + the last-but-one
		// selector name.
		if sel.Sel.Name != "RenderWithLayout" {
			return true
		}
		// sel.X must be a SelectorExpr whose Sel is "Backend"
		// and whose X is any Ident.
		innerSel, ok := sel.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if innerSel.Sel.Name != "Backend" {
			return true
		}
		if _, ok := innerSel.X.(*ast.Ident); !ok {
			return true
		}
		// RenderWithLayout(w, r, name, c, data) — 3rd arg is the
		// body name. Anything else is a refactor.
		if len(call.Args) < 3 {
			return true // let the test flag it via a separate check
		}
		lit, ok := call.Args[2].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		pos := fset.Position(call.Pos())
		out = append(out, renderCallSite{File: path, Line: pos.Line, Name: name})
		return true
	})
	return out, nil
}

// resolveBodyName mirrors the transform in templates.go's
// renderBody funcmap so the test can compute the resolved body
// name for a given handler argument.
func resolveBodyName(handlerName string) string {
	base := strings.ReplaceAll(handlerName, "/", "-")
	base = strings.TrimSuffix(base, ".html")
	return "body-" + base
}

// collectBodyNames returns the set of `{{define "body-admin-..."}}`
// names from all admin/* templates.
func collectBodyNames(t *testing.T) map[string]bool {
	t.Helper()
	defined := map[string]bool{}
	defineRe := regexp.MustCompile(`\{\{define\s+"([^"]+)"\s*\}\}`)
	err := fs.WalkDir(templatesFS, "templates/admin", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return nil
		}
		data, err := templatesFS.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range defineRe.FindAllStringSubmatch(string(data), -1) {
			defined[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk admin templates: %v", err)
	}
	return defined
}

// adminHandlerFiles returns every .go file in internal/feature/admin
// that uses RenderWithLayout at least once. Walking at test time
// means adding a new admin/* file does not require touching this
// test.
func adminHandlerFiles(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs("../../internal/feature/admin")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() ||
			!strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), "RenderWithLayout(") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk admin handlers: %v", err)
	}
	return files
}

// relPathForLog trims an absolute path to "skygate/internal/..." for
// compact error messages.
func relPathForLog(p string) string {
	const marker = "skygate"
	idx := strings.Index(p, marker)
	if idx < 0 {
		return p
	}
	return p[idx:]
}

// TestRenderWithLayout_BodyNamesResolve — every Backend.RenderWithLayout
// call in the admin package must use a name that, after the
// renderBody funcmap transform, maps to a defined body template.
// Pre-v0.33.1.3, 6 handlers passed the hyphenated form
// ("admin-control-planes" etc.) which resolves to
// "body-admin-control-planes" (hyphenated) instead of
// "body-admin-control_planes" (underscored, the actual define).
// Those pages rendered 200 + empty body (silent fail).
func TestRenderWithLayout_BodyNamesResolve(t *testing.T) {
	defined := collectBodyNames(t)
	if len(defined) == 0 {
		t.Fatal("no body- defines found — LoadTemplates() may have failed")
	}
	files := adminHandlerFiles(t)
	if len(files) == 0 {
		t.Fatal("no admin handler files found — test path may need updating")
	}

	var missing []string
	for _, f := range files {
		calls, err := extractRenderNames(f)
		if err != nil {
			t.Errorf("extractRenderNames: %v", err)
			continue
		}
		for _, cs := range calls {
			bodyName := resolveBodyName(cs.Name)
			if !defined[bodyName] {
				missing = append(missing, fmt.Sprintf(
					"  %s:%d  RenderWithLayout(%q) -> %q  (no template defines this body)",
					relPathForLog(cs.File), cs.Line, cs.Name, bodyName))
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("the following RenderWithLayout calls resolve to UNDEFINED body names.\n"+
			"Either rename the {{define}} in the template to match, or change the\n"+
			"handler to pass \"admin/<file>.html\" so the renderBody funcmap\n"+
			"transforms it correctly:\n\n%s",
			strings.Join(missing, "\n"))
	}
}
