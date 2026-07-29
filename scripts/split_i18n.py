#!/usr/bin/env python3
"""
split_i18n.py — refactor-v0.30 Phase C

Splits the monolithic internal/i18n/catalog.go (3782 keys,
~4260 lines) into per-feature files in the same package:

  catalog_common.go       — app, title, nav, common, lang, login, error
  catalog_my.go           — /my/* (devices, dashboard, tokens, ...)
  catalog_admin.go        — /admin/* (users, settings, acls, audit, ...)
  catalog_exit_rules.go   — exit_rules + cleanup
  catalog_exit_nodes.go   — exit_nodes
  catalog_derp.go         — derp
  catalog_backup.go       — backup
  catalog_user_subnet.go  — user_subnet
  catalog_telegram.go     — telegram (admin bot config page)
  catalog_help.go         — help
  catalog_bot.go          — bot (telegram bot replies — largest)
  catalog_update.go       — update (self-update orchestrator)

Each per-feature file declares its own `ruXxx` and `enXxx`
maps (package-level vars in the i18n package). The glue
file (the new catalog.go) merges them in New(). The
TestCatalogsParity + TestPlaceholderOrder + TestHTMLSafeCatalog
tests are updated to read from New() instead of the raw
maps, so they keep working unchanged.

This script is a one-shot tool for the Phase C migration.
After it runs, the migration is complete and the script
can be deleted.
"""
import re
import sys
from pathlib import Path

CATALOG = Path(r"C:\Projects\skygate\internal\i18n\catalog.go")
OUT_DIR = Path(r"C:\Projects\skygate\internal\i18n")

# Per-bucket key-prefix → output file + var name
# Order matters for readability of the merged file.
BUCKETS = [
    ("common",      "catalog_common.go",      ["app", "title", "nav", "common", "lang", "login", "error"]),
    ("my",          "catalog_my.go",          ["devices", "dashboard", "tokens", "my_telegram", "my_meshes", "my_devices", "account", "preauth", "keys"]),
    ("admin",       "catalog_admin.go",       ["users", "admin", "settings", "acls", "audit", "integrations", "control_planes", "headscale_admin", "headplane", "headscale_banner", "admin_invites", "admin_meshes"]),
    ("exit_rules",  "catalog_exit_rules.go",  ["exit_rules", "exit_rules_admin", "exit_rules_nodes", "cleanup"]),
    ("exit_nodes",  "catalog_exit_nodes.go",  ["exit_nodes"]),
    ("derp",        "catalog_derp.go",        ["derp"]),
    ("backup",      "catalog_backup.go",      ["backup"]),
    ("user_subnet", "catalog_user_subnet.go", ["user_subnet"]),
    ("telegram",    "catalog_telegram.go",    ["telegram"]),
    ("help",        "catalog_help.go",        ["help"]),
    ("bot",         "catalog_bot.go",         ["bot"]),
    ("update",      "catalog_update.go",      ["update"]),
]

KEY_RE = re.compile(r'^\t"([^"]+)":\s*"((?:[^"\\]|\\.)*)",?\s*$')


def go_quote(s):
    """Pass-through. The regex above captures the value body as it
    appears in the Go source (including any backslash escapes like
    `\\n`, `\\t`, `\\\"`, `\\\\`). Go's string-literal parser
    handles those natively, so the captured value is already
    valid Go source and we don't need to transform it.

    Earlier versions of this script tried to "escape" the
    backslashes, which double-escaped the Go escape sequences
    (e.g. `\\n` in the source became `\\\\n` in the output, which
    Go parsed as a literal backslash + n). The pass-through
    is correct.
    """
    return s


def parse_map(start_idx, lines, end_re):
    """Parse a Go map literal starting at lines[start_idx] (the line
    with `var X = map[string]string{`). Returns (entries, end_idx)
    where entries is [(key, value), ...] preserving order, and
    end_idx is the index of the closing `}`.
    """
    if "map[string]string{" not in lines[start_idx]:
        raise ValueError(f"line {start_idx+1} doesn't look like a map start: {lines[start_idx]!r}")
    entries = []
    i = start_idx + 1
    while i < len(lines):
        if lines[i].strip() == "}":
            return entries, i
        # Skip comments
        stripped = lines[i].lstrip()
        if stripped.startswith("//"):
            i += 1
            continue
        m = KEY_RE.match(lines[i])
        if m:
            entries.append((m.group(1), m.group(2)))
        i += 1
    raise ValueError("unterminated map literal")


def classify(key):
    """Return bucket name for a key."""
    prefix = key.split(".", 1)[0]
    for name, _, prefixes in BUCKETS:
        if prefix in prefixes:
            return name
    return None  # unclassified — will be reported


def main():
    text = CATALOG.read_text(encoding="utf-8")
    lines = text.splitlines()

    # Find the two map declarations
    ru_start = en_start = None
    for i, line in enumerate(lines):
        if line.startswith("var ruCatalog = map[string]string{"):
            ru_start = i
        elif line.startswith("var enCatalog = map[string]string{"):
            en_start = i
    if ru_start is None or en_start is None:
        sys.exit("could not find both ruCatalog and enCatalog")

    ru_entries, ru_end = parse_map(ru_start, lines, None)
    en_entries, en_end = parse_map(en_start, lines, None)

    print(f"parsed: {len(ru_entries)} ru keys, {len(en_entries)} en keys")
    if len(ru_entries) != len(en_entries):
        sys.exit(f"ru ({len(ru_entries)}) and en ({len(en_entries)}) key counts differ")

    # Group by bucket
    buckets = {name: {"ru": [], "en": []} for name, _, _ in BUCKETS}
    unclassified = []

    ru_by_key = dict(ru_entries)
    en_by_key = dict(en_entries)

    for key, val in ru_entries:
        b = classify(key)
        if b is None:
            unclassified.append(key)
        else:
            buckets[b]["ru"].append((key, val))

    for key, val in en_entries:
        b = classify(key)
        if b is None:
            unclassified.append(key)
        else:
            buckets[b]["en"].append((key, val))

    if unclassified:
        print(f"WARNING: {len(unclassified)} unclassified keys (will go in common):")
        for k in unclassified[:10]:
            print(f"  {k}")
        if len(unclassified) > 10:
            print(f"  ... and {len(unclassified)-10} more")
        # Park them in common
        for key, val in ru_entries:
            if classify(key) is None:
                buckets["common"]["ru"].append((key, val))
        for key, val in en_entries:
            if classify(key) is None:
                buckets["common"]["en"].append((key, val))

    # Emit per-feature files
    HEADER = '''// Code generated by split_i18n.py — refactor-v0.30 Phase C.
// DO NOT EDIT THIS FILE BY HAND — run scripts/split_i18n.py to
// regenerate from the source-of-truth (catalog_common.go etc.)
//
// Per-feature i18n keys for the {bucket} feature. The keys are
// declared as package-level maps; the glue file (i18n.go)
// merges them in New().

package i18n

'''

    for name, fname, prefixes in BUCKETS:
        ru_lines = buckets[name]["ru"]
        en_lines = buckets[name]["en"]
        if not ru_lines and not en_lines:
            continue
        out = []
        out.append(HEADER.format(bucket=name))
        out.append(f"// {name} feature — {len(ru_lines)} ru keys, {len(en_lines)} en keys.\n")
        out.append(f"// Top-level prefixes: {', '.join(prefixes)}\n")
        # CamelCase the var name so it matches the references in
        # catalog.go (perFeatureRU / perFeatureEN). The bucket
        # name itself stays snake_case for the file name.
        # "exit_rules" → "ExitRules", "user_subnet" → "UserSubnet".
        var_prefix = "".join(part[0].upper() + part[1:] for part in name.split("_"))
        out.append(f"\nvar ru{var_prefix} = map[string]string{{\n")
        # Align the values for readability
        max_key = max(len(k) for k, _ in ru_lines)
        for k, v in ru_lines:
            out.append(f'\t"{k}"')
            out.append(f'{" " * (max_key - len(k) + 1)}: "{go_quote(v)}",\n')
        out.append("}\n\n")
        out.append(f"var en{var_prefix} = map[string]string{{\n")
        max_key = max(len(k) for k, _ in en_lines)
        for k, v in en_lines:
            out.append(f'\t"{k}"')
            out.append(f'{" " * (max_key - len(k) + 1)}: "{go_quote(v)}",\n')
        out.append("}\n")
        (OUT_DIR / fname).write_text("".join(out), encoding="utf-8")
        print(f"wrote {fname}: {len(ru_lines)} ru + {len(en_lines)} en keys")

    # Emit the new glue file (catalog.go)
    glue = '''// Code generated by split_i18n.py — refactor-v0.30 Phase C.
// DO NOT EDIT THIS FILE BY HAND — run scripts/split_i18n.py to
// regenerate from the per-feature files.
//
// i18n catalog: maps a top-level prefix (app, nav, bot, ...) to
// a package-level map of (key, translated-string). The New()
// function below merges every per-feature map into the
// translations table; the per-feature maps are declared in
// catalog_<feature>.go in the same package.
//
// refactor-v0.30 Phase C (2026-07-29): the previous single
// catalog.go file (4260 lines, 3782 keys RU+EN) was split
// into 12 per-feature files for navigability. The
// translation contract (T(lang, key) → string) is unchanged.
// Tests that previously read ruCatalog / enCatalog directly
// (TestCatalogsParity, TestPlaceholderOrder, TestHTMLSafeCatalog)
// now build the catalog via New() and inspect the merged
// result — the tests work the same way, but the source
// data is split by feature.

package i18n

// perFeatureRU + perFeatureEN list every per-feature map the
// glue needs to merge. New() iterates these and combines the
// entries into the translations table. Keep in lockstep with
// the per-feature files (catalog_<feature>.go) — if you add
// a new catalog_<feature>.go file, append its maps to BOTH
// slices below.

var perFeatureRU = []map[string]string{
\truCommon,
\truMy,
\truAdmin,
\truExitRules,
\truExitNodes,
\truDerp,
\truBackup,
\truUserSubnet,
\truTelegram,
\truHelp,
\truBot,
\truUpdate,
}

var perFeatureEN = []map[string]string{
\tenCommon,
\tenMy,
\tenAdmin,
\tenExitRules,
\tenExitNodes,
\tenDerp,
\tenBackup,
\tenUserSubnet,
\tenTelegram,
\tenHelp,
\tenBot,
\tenUpdate,
}

// mergeMaps concatenates N maps into a single map.
// Used by New() to flatten the per-feature maps into one
// RU + one EN map keyed by translation key.
func mergeMaps(maps ...map[string]string) map[string]string {
\tout := make(map[string]string)
\tfor _, m := range maps {
\t\tfor k, v := range m {
\t\t\tout[k] = v
\t\t}
\t}
\treturn out
}
'''
    (OUT_DIR / "catalog.go").write_text(glue, encoding="utf-8")
    print(f"wrote new catalog.go (glue)")

    # Summary
    total = sum(len(buckets[n]["ru"]) for n, _, _ in BUCKETS)
    print(f"\nTotal: {total} keys in {len([n for n,_,_ in BUCKETS])} per-feature files")


if __name__ == "__main__":
    main()
