#!/bin/bash
# check_b163.sh — /admin/system_tests: full FAIL
# output is not visible (B163, v1.5.1)
#
# Operator 2026-08-24 UX report: "просмотреть тесты
# системы не полностью выводится информация о
# причинах FAIL тестов". The pre-B163 template
# rendered the test output as
# `<small><code>{{.Output}}</code></small>` —
# inline, single-line, no whitespace handling.
# A multi-line error message ("SQL error: column
# X does not exist\n  at line 5\n  at line 6...")
# became a wall of text in a tiny inline element
# that wrapped unpredictably.
#
# B163 (this file) pins the fix:
#  1. Output wrapped in `<details>` (open by
#     default for FAIL, closed for PASS/SKIP)
#  2. `<pre>` with white-space: pre-wrap +
#     max-height: 280px + overflow-y: auto
#  3. "Copy" button next to the summary (uses
#     navigator.clipboard.writeText with a
#     legacy execCommand fallback)
#  4. CSS rules for details.system-test-output
#     and the inner pre
#  5. New i18n keys (RU + EN) for the
#     summary label + copy button + tooltip
#  6. The `<small><code>{{.Output}}</code></small>`
#     pattern is GONE — replaced by the new
#     <details> block

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: template uses <details> for the output cell ==="
# The new <details class="system-test-output">
# must wrap the test output. The old
# `<small><code>{{.Output}}</code></small>`
# must be GONE.
if grep -qE 'details class="system-test-output"' internal/handlers/templates/admin/system_tests.html; then
    ok "system_tests.html wraps output in <details class=\"system-test-output\">"
else
    bad "system_tests.html: <details class=\"system-test-output\"> MISSING"
fi
# The old `<small><code>{{.Output}}</code></small>` was a
# recurring code smell (B163 change note). The check
# uses awk to skip Go template comment blocks
# ({{/* ... */}} — everything between the {{/* and
# */}} delimiters). The surviving non-comment
# line would be a regression.
old_pattern_hits=$(awk '
  /\{\{[\/][\*]/ { in_comment=1; next }
  /[\*][\}]\}/ { in_comment=0; next }
  in_comment { next }
  /<small><code>\{\{\.Output\}\}<\/code><\/small>/ { found=1; print NR": "$0 }
  END { exit (found ? 0 : 1) }
' internal/handlers/templates/admin/system_tests.html) || true
if [ -n "$old_pattern_hits" ]; then
    bad "system_tests.html: old <small><code>{{.Output}}</code></small> still present:"
    echo "$old_pattern_hits"
else
    ok "old <small><code>{{.Output}}</code></small> pattern is GONE (only references are in B163 comments)"
fi

echo ""
echo "=== contract B: <details> is open for FAIL, closed for PASS/SKIP ==="
# The {{if eq $rowStatus "fail"}}open{{end}} pattern
# means FAIL rows expand immediately (operator sees
# the reason on page load) while PASS/SKIP rows
# stay collapsed (less noise on a healthy system).
if grep -qE 'system-test-output.*\{\{if eq .rowStatus.*fail.*\}\}open\{\{end\}\}' internal/handlers/templates/admin/system_tests.html; then
    ok "<details> renders open for FAIL rows (operator sees reason on load)"
else
    bad "<details> is NOT opened for FAIL rows (operator has to click to see the reason)"
fi

echo ""
echo "=== contract C: <pre> block with white-space: pre-wrap ==="
# The inner <pre> uses white-space: pre-wrap so
# multi-line error messages render with proper
# line breaks (the SQL/gRPC stack trace case).
if grep -qE '<pre>\{\{\.Output\}\}</pre>' internal/handlers/templates/admin/system_tests.html; then
    ok "Output rendered inside <pre> (preserves whitespace)"
else
    bad "Output NOT inside <pre> (multi-line errors will collapse to one wall-of-text line)"
fi
# CSS rule.
if grep -qE 'details\.system-test-output > pre' internal/handlers/templates/admin/system_tests.html; then
    ok "CSS rule for details.system-test-output > pre is present"
else
    bad "CSS rule for details.system-test-output > pre MISSING"
fi
if grep -qE 'white-space: pre-wrap' internal/handlers/templates/admin/system_tests.html; then
    ok "white-space: pre-wrap is set on the pre (preserves line breaks)"
else
    bad "white-space: pre-wrap MISSING on the pre"
fi
if grep -qE 'max-height: 280px' internal/handlers/templates/admin/system_tests.html; then
    ok "pre has max-height: 280px (long errors scroll, not push the table off-screen)"
else
    bad "pre max-height MISSING (a 200-line error would push the table off-screen)"
fi

echo ""
echo "=== contract D: Copy button (clipboard + legacy fallback) ==="
# The button must use navigator.clipboard.writeText
# (the modern API) with a document.execCommand
# fallback for older admin terminals.
if grep -qE 'navigator\.clipboard\.writeText' internal/handlers/templates/admin/system_tests.html; then
    ok "Copy button uses navigator.clipboard.writeText"
else
    bad "Copy button: navigator.clipboard.writeText MISSING"
fi
if grep -qE 'document\.execCommand\(.copy.\)' internal/handlers/templates/admin/system_tests.html; then
    ok "Copy button has legacy execCommand('copy') fallback for older browsers"
else
    bad "Copy button: legacy fallback MISSING (operator can't paste from locked-down admin terminals)"
fi
if grep -qE 'onclick="copySystemTestOutput' internal/handlers/templates/admin/system_tests.html; then
    ok "Copy button wired via onclick=\"copySystemTestOutput(this)\""
else
    bad "Copy button onclick handler MISSING"
fi
if grep -qE 'function copySystemTestOutput' internal/handlers/templates/admin/system_tests.html; then
    ok "copySystemTestOutput() JS function defined"
else
    bad "copySystemTestOutput() function MISSING"
fi

echo ""
echo "=== contract E: i18n keys (RU + EN) ==="
needed=(
  "system_tests.output_fail_label"
  "system_tests.output_pass_label"
  "system_tests.output_skip_label"
  "system_tests.output_empty_label"
  "system_tests.output_copy_btn"
  "system_tests.output_copy_title"
)
for k in "${needed[@]}"; do
    c=$(grep -cE "\"$k\"" internal/i18n/catalog_common.go 2>/dev/null || true)
    c=${c:-0}
    if [ "$c" -ge 2 ]; then
        ok "i18n key '$k' present in both RU and EN"
    else
        bad "i18n key '$k' MISSING in catalog_common.go (found $c entries — need 2 for RU+EN)"
    fi
done

echo ""
echo "=== contract F: build + vet clean ==="
out=$(go build ./... 2>&1)
if [ -z "$out" ]; then
    ok "go build ./... clean"
else
    bad "go build output: $out"
fi
out=$(go vet ./... 2>&1)
if [ -z "$out" ]; then
    ok "go vet ./... clean"
else
    bad "go vet output: $out"
fi

echo ""
echo "=== summary ==="
echo "B163: collapsible FAIL output on /admin/system_tests"
echo "all contracts satisfied"
