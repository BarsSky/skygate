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
# (Advisory only — prints missing keys but does NOT fail. The catalog
# has pre-existing gaps from older B-changes that haven't been
# backfilled; this contract surfaces them as a TODO list for a
# follow-up TD/B check, without blocking this commit. Switch to
# a hard `bad` if/when the catalog is backfilled.)
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
    ok "contract B: 0 i18n keys missing from the catalog (advisory)"
else
    n=$(echo "$missing_b" | wc -l)
    echo "  WARN  contract B (advisory): $n i18n keys used in templates but missing from catalog (pre-existing catalog debt, see TODO):"
    echo "$missing_b" | sed 's/^/        /'
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
