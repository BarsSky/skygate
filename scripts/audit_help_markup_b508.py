#!/usr/bin/env python3
"""scripts/audit_help_markup_b508.py — B508's two measurements, one script.

1. TRUSTED MARKUP: a catalogue string that carries HTML (`<b>`, `<code>`, `<a href>`)
   must be rendered through `th` (which escapes the ARGUMENTS), never through `t`/`tf`
   (which html/template escapes wholesale, so the tags reach the browser as text).
   Measured 2026-11-20 for B508: the help page had 144 such usages rendered escaped and
   only 3 piped through `safeHTML`; 13 other templates carried another 44.

2. LINKS: every `href="/…"` in a template or in a help catalogue string must name a path
   that the server actually registers. `/docs/windows-client.md` did not — it was never a
   route, and the page taught the operator to open a 404.

Exit 0 = clean, 1 = the defect is present (with the offenders printed).
"""
import glob
import os
import re
import sys

TEMPLATES = "internal/handlers/templates"
CATALOGS = "internal/i18n/catalog_*.go"
# Routes are registered in BOTH files: routes.go holds the table, main.go holds the
# framework endpoints (/static/, /favicon.*) and the root handler. Scanning one of them
# reported every favicon as a dead link (measured while writing this audit).
ROUTE_FILES = ("cmd/skygate/routes.go", "cmd/skygate/main.go")

TAG_RE = re.compile(r"<(b|i|code|u|a|br|span|em|strong)\b")
HREF_RE = re.compile(r'href=\\?(?:"|\')?(/[^"\'\s\\>]+)')
# Static assets are served by app.StaticHandler / the favicon handlers, not by the mux
# table's route strings.
STATIC_PREFIXES = ("/static/", "/favicon.")


def catalogue_html_keys():
    keys = {}
    for path in glob.glob(CATALOGS):
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                m = re.match(r'\s*"([A-Za-z0-9_.\-]+)":\s+"(.*)",?\s*$', line)
                if not m:
                    continue
                key, val = m.group(1), m.group(2)
                if TAG_RE.search(val):
                    keys.setdefault(key, val)
    return keys


def template_usages():
    for path in glob.glob(os.path.join(TEMPLATES, "**", "*.html"), recursive=True):
        with open(path, encoding="utf-8") as fh:
            for n, line in enumerate(fh, 1):
                for m in re.finditer(r"\{\{(t|tf|th)\s+\"([^\"]+)\"", line):
                    yield path, n, m.group(1), m.group(2), line


def measured_defect(html_keys):
    bad = []
    for path, n, fn, key, line in template_usages():
        if key not in html_keys:
            continue
        if fn == "th":
            continue
        rest = line
        if "safeHTML" in rest:
            continue
        bad.append((path.replace("\\", "/"), n, fn, key))
    return bad


def registered_paths():
    paths = set()
    for path in ROUTE_FILES:
        try:
            text = open(path, encoding="utf-8").read()
        except OSError:
            continue
        for m in re.finditer(r'"(?:GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\s+(/[^"\s]*)?"', text):
            if m.group(1):
                paths.add(m.group(1))
        for m in re.finditer(r'HandleFunc\("(?:GET |POST )?(/[^"]*)"', text):
            paths.add(m.group(1))
    return paths


def links_to_check(html_keys):
    """Every internal href in a template and in a help catalogue string."""
    out = []
    for path in glob.glob(os.path.join(TEMPLATES, "**", "*.html"), recursive=True):
        with open(path, encoding="utf-8") as fh:
            for n, line in enumerate(fh, 1):
                for m in HREF_RE.finditer(line):
                    out.append((path.replace("\\", "/"), n, m.group(1), ""))
    for key, val in html_keys.items():
        if not key.startswith("help.exit_rules_help."):
            continue
        for m in HREF_RE.finditer(val):
            out.append(("catalog_help.go", 0, m.group(1), key))
    return out


def normalise(href):
    path = href.split("?")[0].split("#")[0]
    if path.endswith("/"):
        path = path[:-1]
    return path


def literal_head(href):
    """The static part of a templated href, e.g. `/admin/users/{{.UserID}}/plane` →
    `/admin/users/`. A route registered with the same literal head is the same page."""
    head = href.split("{{")[0]
    cut = head.rfind("/")
    return head[: cut + 1] if cut >= 0 else head


def main():
    html_keys = catalogue_html_keys()
    problems = 0

    bad = measured_defect(html_keys)
    if bad:
        problems += len(bad)
        print("ESCAPED MARKUP (%d): the tag reaches the browser as text" % len(bad))
        for path, n, fn, key in bad[:20]:
            print("  %s:%d  %s %s  → use th" % (path, n, fn, key))
        if len(bad) > 20:
            print("  … %d more" % (len(bad) - 20))
    else:
        print("escaped markup: 0")

    routes = registered_paths()
    if not routes:
        print("  WARNING: no routes parsed out of %s — the link check is blind" % (", ".join(ROUTE_FILES),))
    missing = []
    for path, n, href, key in links_to_check(html_keys):
        p = normalise(href)
        if not p.startswith("/"):
            continue
        if p.startswith(STATIC_PREFIXES):
            continue
        if p in routes:
            continue
        # A prefix route (e.g. "/static/") covers deeper paths.
        if any(r.endswith("/") and p.startswith(r) for r in routes):
            continue
        # A templated href: compare the literal head with the registered prefixes.
        if "{{" in href:
            head = literal_head(href)
            if head and any(r.startswith(head) for r in routes):
                continue
        missing.append((path, n, href, key))
    if missing:
        problems += len(missing)
        print("DEAD LINKS (%d): no route serves these" % len(missing))
        for path, n, href, key in missing[:20]:
            where = "%s:%d" % (path, n) if n else "%s %s" % (path, key)
            print("  %s  → %s" % (where, href))
    else:
        print("dead links: 0")

    return 0 if problems == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
