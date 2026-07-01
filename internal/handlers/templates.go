package handlers

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path"
	"strings"
)

//go:embed templates/*.html templates/*/*.html
var tplFS embed.FS

type Templates struct {
	t *template.Template
}

// LoadTemplates parses all templates and returns a renderer.
// Uses {{define "body-..."}} blocks for body content and a single "layout"
// template that calls {{renderBody .BodyTemplate .}} to inject the body.
func LoadTemplates() *Templates {
	t := template.New("root")

	// First pass: register renderBody placeholder so ParseFS doesn't fail.
	// We'll re-register with the real impl after parsing bodies.
	t.Funcs(template.FuncMap{
		"safeJS": func(s string) template.JS { return template.JS(s) },
		"dividefloat": func(a, b float64) float64 {
			if b == 0 { return 0 }
			return a / b
		},
		"add": func(a, b int) int { return a + b },
		"bytesfmt": func(n int64) string {
			const k = 1024
			if n >= 1024*1024 {
				return fmt.Sprintf("%.1f MB", float64(n)/float64(k*k))
			}
			if n >= 1024 {
				return fmt.Sprintf("%.1f KB", float64(n)/float64(k))
			}
			return fmt.Sprintf("%d B", n)
		},
		"renderBody": func(name string, data any) (template.HTML, error) {
			return template.HTML("<!-- placeholder -->"), nil
		},
	})

	// Collect body files (everything except layout.html)
	var bodyFiles []string
	entries, err := fs.ReadDir(tplFS, "templates")
	if err != nil {
		panic("read tplFS: " + err.Error())
	}
	for _, e := range entries {
		if e.IsDir() {
			sub, _ := fs.ReadDir(tplFS, path.Join("templates", e.Name()))
			for _, s := range sub {
				if strings.HasSuffix(s.Name(), ".html") {
					bodyFiles = append(bodyFiles, path.Join("templates", e.Name(), s.Name()))
				}
			}
		} else if strings.HasSuffix(e.Name(), ".html") && e.Name() != "layout.html" {
			bodyFiles = append(bodyFiles, path.Join("templates", e.Name()))
		}
	}

	// Parse all body files first — each {{define "body-..."}} becomes a named template.
	for _, f := range bodyFiles {
		if _, err := t.ParseFS(tplFS, f); err != nil {
			panic("parse " + f + ": " + err.Error())
		}
	}

	// Now overwrite renderBody with the real implementation. Funcs mutates
	// the template and returns it; subsequent ExecuteTemplate calls use the
	// updated funcmap.
	t.Funcs(template.FuncMap{
		"renderBody": func(name string, data any) (template.HTML, error) {
			base := strings.ReplaceAll(name, "/", "-")
			base = strings.TrimSuffix(base, ".html")
			defineName := "body-" + base
			var buf bytes.Buffer
			if err := t.ExecuteTemplate(&buf, defineName, data); err != nil {
				return "", err
			}
			return template.HTML(buf.String()), nil
		},
	})

	// Finally parse layout.html which {{define "layout"}} and uses {{renderBody .BodyTemplate .}}.
	if _, err := t.ParseFS(tplFS, "templates/layout.html"); err != nil {
		panic("parse layout: " + err.Error())
	}

	return &Templates{t: t}
}

func (t *Templates) ExecuteTemplate(w io.Writer, name string, data any) error {
	return t.t.ExecuteTemplate(w, name, data)
}
