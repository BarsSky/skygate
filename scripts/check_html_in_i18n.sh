#!/usr/bin/env bash
# scripts/check_html_in_i18n.sh — B-mod-reregister-fix2 (2026-09-14)
#
# Audit-only (not blocking): list i18n keys whose catalog value
# contains raw HTML tags (e.g. <b>, <code>, <br>). For each such key,
# the operator must confirm the template usage has `| safeHTML`,
# OR that the HTML was intentionally embedded for some other rendering
# path (escape-only text in command-preview blocks, etc.).
#
# Background: Go templates auto-escape `{{t "key"}}` and `{{tf "key" .x}}`,
# so an i18n string with HTML renders as the literal tag text in the
# page. The supported way to render formatted HTML from an i18n key
# is `{{t "key" | safeHTML}}` — `safeHTML` is a template helper that
# calls `template.HTML(s)` to skip escaping.
#
# Exit codes:
#   0 = audit complete (always — this is operator-facing summary, not a gate)
set -uo pipefail

cd "$(dirname "$0")/.."

echo "==============================================="
echo "B-mod-reregister-fix2: HTML-in-i18n audit"
echo "==============================================="
echo ""

# Step 1: collect keys whose catalog value contains raw HTML tags
# (literal '<' followed by a letter). Entity escapes like &lt; are
# kept — those are intentional in some strings.
HTML_KEYS_RAW=$(mktemp)
trap 'rm -f "$HTML_KEYS_RAW"' EXIT
grep -hoE '"[a-z][a-z0-9_.]+"\s*:\s*"[^"]*<[a-zA-Z][^"]*"' internal/i18n/catalog_*.go \
  | sed -nE 's/"([a-z][a-z0-9_.]+)".*/\1/p' | sort -u > "$HTML_KEYS_RAW"

HTML_COUNT=$(wc -l < "$HTML_KEYS_RAW")
echo "Step 1: i18n catalog keys with raw HTML tags (potential bug class)"
echo "  Found $HTML_COUNT keys"
echo "  Examples:"
head -10 "$HTML_KEYS_RAW" | sed 's/^/    /'
echo ""

# Step 2: scan all templates for {{t "key" | safeHTML}} / {{safeHTML (t "key")}}
# and build a set of keys wrapped with safeHTML.
WRAPPED_FILE=$(mktemp)
trap 'rm -f "$HTML_KEYS_RAW" "$WRAPPED_FILE"' EXIT
grep -rEh --include='*.html' -oE '\{\{[^}]*\}\}' internal/handlers/templates 2>/dev/null \
  | grep -oE '"[a-z][a-z0-9_.]+"[[:space:]]*(\|[^}]+)?' \
  | head -50000 > "$WRAPPED_FILE" || true
# Simpler approach: just grep for `t "key"` then check 'safeHTML' nearby
grep -rEh --include='*.html' 'safeHTML' internal/handlers/templates 2>/dev/null \
  | grep -oE '"[a-z][a-z0-9_.]+"' | sed 's/"//g' | sort -u > "$WRAPPED_FILE"
WRAPPED_COUNT=$(wc -l < "$WRAPPED_FILE")
echo ""
echo "Step 2: keys already wrapped with | safeHTML in some template"
echo "  Found $WRAPPED_COUNT unique keys"

# Step 3: per-key audit: which HTML keys are wrapped, which aren't.
echo ""
echo "Step 3: cross-reference (HTML key x | safeHTML coverage)"
echo ""

UNWRAPPED_COUNT=0
WRAPPED_NOT_HTML=0
TMP_HTML=$(mktemp)
TMP_WRAPPED=$(mktemp)
trap 'rm -f "$HTML_KEYS_RAW" "$WRAPPED_FILE" "$TMP_HTML" "$TMP_WRAPPED"' EXIT
sort -u "$HTML_KEYS_RAW" > "$TMP_HTML"
sort -u "$WRAPPED_FILE" > "$TMP_WRAPPED"

# Keys that have HTML but no safeHTML wrapper anywhere
comm -23 "$TMP_HTML" "$TMP_WRAPPED" > /tmp/html_unwrapped
UNWRAPPED_COUNT=$(wc -l < /tmp/html_unwrapped)

echo "  i18n keys with HTML tags, wrapped with | safeHTML:  $(comm -12 "$TMP_HTML" "$TMP_WRAPPED" | wc -l)"
echo "  i18n keys with HTML tags, NOT wrapped anywhere:       $UNWRAPPED_COUNT"

if [ "$UNWRAPPED_COUNT" -gt 0 ]; then
    echo ""
    echo "  First 20 unwrapped keys (operator review required):"
    head -20 /tmp/html_unwrapped | sed 's/^/    /'
fi

echo ""
echo "==============================================="
echo "Decision required from operator:"
echo "  For each 'unwrapped' key above, EITHER:"
echo "  - Add | safeHTML to the template usage if HTML rendering is wanted"
echo "  - Remove the HTML tags from the i18n string if escape-then-text is fine"
echo ""
echo "This check is audit-only (exit 0). Repeat-run to confirm progress."
echo "==============================================="

exit 0
