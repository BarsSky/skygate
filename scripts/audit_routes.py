#!/usr/bin/env python3
"""scripts/audit_routes.py — verify that every app.X wired up in main.go is
actually defined as either a method on *App or a field on the App struct
in internal/handlers.

This catches the class of regression where a refactor deletes handler
methods from handlers.go but leaves the route registration in
cmd/skygate/main.go pointing at a now-undefined identifier. Without
this check, such a typo produces a compile error (loud) — but only
when the package is actually built. If the broken file is excluded
from the build (e.g. left untracked after a partial commit), the
typo is silent until the next CI run on a clean clone. This script
catches it statically, in a couple of seconds, with no Go toolchain.

It also surfaces "dead handlers": methods that are defined but no
longer wired into any route. That's a code-smell, not a bug, so it's
reported as a warning, not a failure.

Usage:
    python3 scripts/audit_routes.py
    python3 scripts/audit_routes.py --strict-dead   # fail on dead handlers too

Exit codes:
    0 — all wired handlers exist (dead handlers are warnings unless --strict-dead)
    1 — at least one app.X in main.go has no matching method/field
    2 — could not locate main.go / handlers/ (e.g. run from wrong cwd)
"""
import argparse
import os
import re
import sys


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MAIN_GO   = os.path.join(REPO_ROOT, "cmd", "skygate", "main.go")
HANDLERS  = os.path.join(REPO_ROOT, "internal", "handlers")


# --------------------------------------------------------------------------- #
# Parsing helpers
# --------------------------------------------------------------------------- #

# app.SomeName — used inside main.go. Exported identifiers only (lowercase
# refs would be either package-level vars or unexported struct fields, not
# something we'd ever route to).
RE_APP_REF  = re.compile(r"\bapp\.([A-Z]\w+)\b")

# func (a *App) MethodName( ...  — method definition.
# Receiver form is always `(name *App)` — Go requires a receiver variable.
RE_METHOD   = re.compile(r"^func\s+\(\w+\s+\*App\s*\)\s+(\w+)\s*\(")

# Top of struct: type App struct {
RE_STRUCT   = re.compile(r"^type\s+App\s+struct\s*\{")


def read(path):
    with open(path, "r", encoding="utf-8") as f:
        return f.read()


def find_used_in_main(path):
    """Return the sorted set of app.X identifiers referenced in main.go."""
    text = read(path)
    used = set()
    # Strip line comments to avoid picking up `// app.Foo` mentions. Block
    # comments are rare in main.go but handled the same way.
    for line in text.splitlines():
        code = line.split("//", 1)[0]
        for m in RE_APP_REF.finditer(code):
            used.add(m.group(1))
    return sorted(used)


def find_methods_in_handlers(dirpath):
    """Return sorted set of (name, file) for every (a *App) Method(...) def."""
    methods = {}
    for name in sorted(os.listdir(dirpath)):
        if not name.endswith(".go") or name == "templates.go":
            # templates.go is //go:embed only; it has no handler methods.
            continue
        path = os.path.join(dirpath, name)
        for line in read(path).splitlines():
            m = RE_METHOD.match(line)
            if m:
                methods.setdefault(m.group(1), name)
    return methods


def find_fields_in_app_struct(dirpath):
    """Find type App struct { ... } in any file under dirpath and return
    the set of field names declared inside it.

    Only walks the first struct definition it finds that matches; the
    project has exactly one App struct (in handlers.go), so this is safe.
    """
    struct_re_field = re.compile(r"^\s*([A-Z]\w*)\s+")
    for name in sorted(os.listdir(dirpath)):
        if not name.endswith(".go"):
            continue
        path = os.path.join(dirpath, name)
        lines = read(path).splitlines()
        for i, line in enumerate(lines):
            if RE_STRUCT.match(line):
                fields = set()
                for sub in lines[i + 1:]:
                    stripped = sub.strip()
                    if stripped.startswith("}"):
                        break
                    if not stripped or stripped.startswith("//"):
                        continue
                    m = struct_re_field.match(sub)
                    if m:
                        fields.add(m.group(1))
                return fields
    return set()


# --------------------------------------------------------------------------- #
# Output helpers
# --------------------------------------------------------------------------- #

def ok(msg):
    print(f"PASS: {msg}")


def fail(msg):
    print(f"FAIL: {msg}")


def warn(msg):
    print(f"WARN: {msg}")


# --------------------------------------------------------------------------- #

def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--strict-dead", action="store_true",
                    help="also exit 1 when dead handlers are present")
    args = ap.parse_args()

    if not os.path.isfile(MAIN_GO):
        print(f"ERROR: {MAIN_GO} not found (run from repo root?)")
        return 2
    if not os.path.isdir(HANDLERS):
        print(f"ERROR: {HANDLERS} not found")
        return 2

    used     = find_used_in_main(MAIN_GO)
    methods  = find_methods_in_handlers(HANDLERS)
    fields   = find_fields_in_app_struct(HANDLERS)

    # Filter out fields from "used" — main.go legitimately references fields
    # like app.RateLimiter = ...  These aren't handler typos, they're struct
    # field assignments.
    method_or_field = set(methods) | fields
    used_methods    = [u for u in used if u not in fields]
    used_fields     = [u for u in used if u in fields]

    missing = [u for u in used_methods if u not in methods]
    dead    = sorted(m for m in methods if m not in used)

    print(f"Audit: cmd/skygate/main.go vs internal/handlers/*.go")
    print(f"  routes wired:    {len(used_methods)}")
    print(f"  field refs:      {len(used_fields)}  "
          f"({', '.join(used_fields) if used_fields else 'none'})")
    print(f"  methods defined: {len(methods)}")
    print(f"  fields on App:   {len(fields)}")
    print()

    if not missing:
        ok(f"all {len(used_methods)} wired app.X references resolve "
           f"to a method or field")
    else:
        fail(f"{len(missing)} wired handler(s) have no implementation:")
        for name in missing:
            # Be helpful: try to suggest the closest defined method by
            # Levenshtein-ish heuristic (case-insensitive prefix match).
            hint = suggest(name, methods)
            where = f"  → did you mean {hint}?" if hint else ""
            print(f"  - app.{name}{where}")

    if dead:
        msg = (f"{len(dead)} defined method(s) not wired into any route")
        if args.strict_dead:
            fail(msg + " (--strict-dead)")
            for m in dead:
                print(f"  - {m}  (defined in {methods[m]})")
        else:
            warn(msg + " (use --strict-dead to fail):")
            for m in dead:
                print(f"  - {m}  (defined in {methods[m]})")
    else:
        ok("no dead handlers — every defined method is wired into a route")

    return 1 if missing or (args.strict_dead and dead) else 0


def suggest(name, pool):
    """Return the closest pool entry by case-insensitive common prefix,
    or None if no candidate shares at least 3 leading characters."""
    lname = name.lower()
    best, best_score = None, 0
    for cand in pool:
        lc = cand.lower()
        score = 0
        for a, b in zip(lname, lc):
            if a == b:
                score += 1
            else:
                break
        if score > best_score:
            best, best_score = cand, score
    return best if best_score >= 3 else None


if __name__ == "__main__":
    sys.exit(main())
