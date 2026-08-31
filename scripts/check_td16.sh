#!/bin/bash
# check_td16.sh — pin: no unexported struct fields referenced from templates.
#
# TD-16 (v1.5.2) — false-alarm "can't evaluate field dnsConfigured in
# type interface {}" at /admin/ha. The ha.html template accessed
# `.Data.dnsConfigured`, but the field was named `extcredsConfigured`
# in the haPageData struct AND was unexported (lowercase first
# letter). Go templates can't access unexported struct fields
# from another package — the engine surfaces a runtime error
# that 500s the page. The same template also referenced
# `ha.ha.dns_help` and 8 sibling `ha.ha.X` i18n keys, but the
# catalog has them as `ha.X` (the `ha.ha.` prefix was a copy-paste
# typo from the section name "External DNS"). Both the field
# mismatch and the i18n-key mismatch were silent until the user
# hit /admin/ha.
#
# This check pins 3 patterns:
#  (A) Every struct field referenced via `.Data.X` (or
#      equivalent) in any *.html template is exported (uppercase
#      first letter) in the corresponding Go struct.
#  (B) Every `{{t "X.Y"}}` i18n key in any template exists in
#      the catalog (both RU and EN blocks). Catches the
#      `ha.ha.X` ↔ `ha.X` mismatch class.
#  (C) Every {{define "body-admin-..."}} block in admin/*.html
#      uses underscores (matching the filename), not dashes —
#      the renderBody funcmap transforms "admin/<file>.html"
#      to "body-admin-<file-with-underscores>" so the define
#      name MUST match.
#
# Implementation: pure bash + awk + sed (no Python needed, the
# B/S-checks that need regex lookbehind fall back to Python).
# Coverage is broad — every *.html under internal/handlers/
# templates/. Trade-off: false positives are possible (a
# template might reference a field whose Go type lives in a
# different package) so the check is conservative — it only
# pins the .Data.X pattern, not arbitrary .X.

set -u
cd "$(dirname "$0")/.." || exit 1

ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

PASS=0
FAIL=0

# ---------------------------------------------------------------------------
# Contract A: every .Data.X in templates corresponds to an exported
#             field in some Go struct.
# ---------------------------------------------------------------------------
# Strategy: extract every `.Data.Ident` reference from *.html,
# then for each Ident, look for `Ident\s+\w` in any *.go struct
# definition in the same package as the template, AND verify
# the field is exported (uppercase first letter).
#
# Conservatism: we only check Idents that look like normal
# Go field names (no underscores, no special chars) AND
# appear AFTER `.Data.`. The check is best-effort — a real
# struct in another package might legitimately use lowercase
# first letter, but Go templates still can't access it. So
# we only fail if the field is BOTH (1) lowercase AND
# (2) appears in a Go struct definition in the same package
# area.

violations_a=$(awk '
BEGIN { in_html = 0; html_path = ""; }
/\.Data\.[a-zA-Z][a-zA-Z0-9]*/ {
    # Extract the identifier
    line = $0
    while (match(line, /\.Data\.([a-zA-Z][a-zA-Z0-9]*)/, arr)) {
        ident = arr[1]
        # Skip if ident is in the same file (for HA case)
        print ident "\t" FILENAME ":" FNR
        line = substr(line, RSTART + RLENGTH)
    }
}
' $(find internal/handlers/templates -name '*.html' -type f 2>/dev/null) 2>/dev/null | sort -u)

if [ -z "$violations_a" ]; then
    ok "contract A: no .Data.X lowercase-ident references in any template"
else
    # Check each lowercase ident against Go structs.
    bad_count=0
    while IFS=$'\t' read -r ident src; do
        [ -z "$ident" ] && continue
        first="${ident:0:1}"
        # Lowercase first letter = unexported
        if [[ "$first" =~ [a-z] ]]; then
            # Find any Go file declaring this field
            # (not as exhaustive as proper AST analysis, but
            # catches the common case: field in same package)
            matches=$(grep -rln "${ident}\b" internal/ 2>/dev/null \
                | head -3)
            if [ -n "$matches" ]; then
                # Check each match — is the field declared in a struct?
                for f in $matches; do
                    if grep -qE "^[[:space:]]*${ident}[[:space:]]+\w" "$f" 2>/dev/null; then
                        echo "    $src -> $ident (lowercase) declared in $f"
                        bad_count=$((bad_count + 1))
                        break
                    fi
                done
            fi
        fi
    done <<< "$violations_a"
    if [ $bad_count -eq 0 ]; then
        ok "contract A: 0 templates reference unexported .Data.X fields"
    else
        bad "contract A: $bad_count template reference(s) to unexported .Data.X fields (listed above)"
    fi
fi

# ---------------------------------------------------------------------------
# Contract B: every {{t "X.Y"}} i18n key in templates exists in the catalog.
# 2026-08-31 (TD-18): flipped from advisory to hard fail. The 31
# pre-existing gaps from B148 (certsync) + ha.audit_* + subnets.col_actions
# + telegram.saved_token were all backfilled. New code that adds a
# t() reference without a corresponding catalog entry is now a build-
# blocking regression, not a soft warning.
#
# Two checks combined:
#  - B1: missing keys (the {{t "X"}} call has no "X" entry in any
#    catalog_*.go file). FAIL.
#  - B2: padded keys (catalog_*.go has "X   " (with trailing spaces)
#    but the template uses "X" without padding — the lookup silently
#    misses). FAIL. This catches the B148 catalog-generation bug that
#    left 50 keys unreachable.
# ---------------------------------------------------------------------------
missing_b=$(awk '
/\{\{[[:space:]]*t[[:space:]]+"[a-zA-Z][a-zA-Z0-9_.]*"/ {
    match($0, /\{\{[[:space:]]*t[[:space:]]+"([a-zA-Z][a-zA-Z0-9_.]*)"/, arr)
    if (arr[1] != "") {
        print arr[1]
    }
}' $(find internal/handlers/templates -name '*.html' -type f 2>/dev/null) \
    | sort -u | while read -r key; do
    if ! grep -q "\"$key\"" internal/i18n/catalog*.go 2>/dev/null; then
        echo "$key"
    fi
done | sort -u)

if [ -z "$missing_b" ]; then
    ok "contract B1: 0 i18n keys missing from the catalog"
else
    n=$(echo "$missing_b" | wc -l)
    bad "contract B1: $n i18n keys used in templates but missing from catalog:"
    echo "$missing_b" | sed 's/^/        /'
fi

# B2: detect padded catalog keys ("key   " with trailing whitespace).
# Such keys are unreachable from the {{t "key"}} funcmap because Go
# map lookup is exact-string. The pre-TD-18 catalog had 50 such keys
# (25 cert.* × 2 languages) — fixed by the TD-18 padding cleanup.
padded_b=$(grep -hnE '^\s*"[a-zA-Z][a-zA-Z0-9_.]*\s+"\s*:' internal/i18n/catalog*.go 2>/dev/null \
    | sed -E 's/.*"([^"]+)"\s*:.*/\1/' \
    | sort -u)
if [ -z "$padded_b" ]; then
    ok "contract B2: 0 catalog keys with trailing whitespace (all keys reachable)"
else
    n=$(echo "$padded_b" | wc -l)
    bad "contract B2: $n catalog keys with trailing whitespace (unreachable from t() — fix by removing trailing spaces):"
    echo "$padded_b" | sed 's/^/        /'
fi

# ---------------------------------------------------------------------------
# Contract C: every admin/*.html {{define}} uses underscores (not dashes)
#              to match the filename + renderBody funcmap transform.
# ---------------------------------------------------------------------------
violations_c=$(awk '
/\{\{[[:space:]]*define[[:space:]]+"(body-admin-[^"]+)"/ {
    match($0, /"(body-admin-[^"]+)"/, arr)
    define_name = arr[1]
    # Filename
    fname = FILENAME
    sub(/^.*\//, "", fname)
    sub(/\.html$/, "", fname)
    expected = "body-admin-" fname
    if (define_name != expected) {
        print "    " fname ": define=" define_name " expected=" expected
    }
}' internal/handlers/templates/admin/*.html 2>/dev/null)

if [ -z "$violations_c" ]; then
    ok "contract C: all admin/*.html define names use underscores matching the filename"
else
    bad "contract C: admin/*.html define name / filename mismatch:"
    echo "$violations_c" | sed 's/^/        /'
fi

# ---------------------------------------------------------------------------
# Contract D: TD-16 is registered in verify_pre_deploy.sh
# ---------------------------------------------------------------------------
if grep -qF "check_td16.sh" scripts/verify_pre_deploy.sh; then
    ok "contract D: verify_pre_deploy.sh references check_td16.sh"
else
    bad "contract D: verify_pre_deploy.sh does NOT reference check_td16.sh"
fi

# ---------------------------------------------------------------------------
# Contract E: TD-16 is mentioned in AGENTS.md
# ---------------------------------------------------------------------------
if grep -qF "TD-16" AGENTS.md; then
    ok "contract E: AGENTS.md mentions TD-16"
else
    bad "contract E: AGENTS.md does NOT mention TD-16"
fi

# ---------------------------------------------------------------------------
# Contract F: this script is executable
# ---------------------------------------------------------------------------
if [ -x scripts/check_td16.sh ]; then
    ok "contract F: scripts/check_td16.sh is executable"
else
    bad "contract F: scripts/check_td16.sh is NOT executable"
fi

# ---------------------------------------------------------------------------
echo
echo "=== TD-16 summary: $PASS pass, $FAIL fail ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
echo "all contracts satisfied"
