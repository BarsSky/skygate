#!/bin/bash
# check_td15.sh - pin: no unescaped backticks in run_check descriptions.
#
# TD-15 (v1.5.2) - false-alarm "headscale: command not found" at line
# 3221 of scripts/verify_pre_deploy.sh. The run_check "B160" call had
# its description argument in double quotes, but the description
# contained unescaped backticks around `headscale nodes expire
# --disable` (visual formatting). Bash treats backticks inside a
# double-quoted string as command substitution: it tried to exec
# `headscale nodes expire --disable`, the `headscale` binary isn't on
# PATH on the operator's Windows host, and bash emitted
# "scripts/verify_pre_deploy.sh: line 3221: headscale: command not
# found" to stderr. The run_check function captured stderr via
# `$(bash -c "$cmd" 2>&1)`, the failure bubbled up via $?, and
# the entire verify_pre_deploy.sh exited non-zero - even though
# the B160 check itself was clean. The pre-push hook printed
# "catalog RED - push ABORTED" but git push actually succeeded
# (the script's non-zero exit was treated as a false alarm by
# the operator's background task).
#
# This check pins: every run_check description in
# scripts/verify_pre_deploy.sh is free of unescaped backticks.
# Catches the regression class - any future B-description with
# markdown-style backticks will fail the catalog instead of
# silently tripping command substitution.
#
# Two known-safe patterns are explicitly allowed:
#  - escaped backticks: \`  (the backslash is what bash sees
#    inside the double-quoted string, the `\` is a no-op
#    visual)
#  - backticks inside single-quoted arguments to echo (the
#    inner "cmd" in `run_check` is in single quotes by
#    convention, but we don't depend on that - we just check
#    the description argument)
#
# Contract A: 0 unescaped backticks in any run_check description
#             (handles B### AND TD-### names)
# Contract B: 0 unescaped backticks in any echo line of
#             scripts/check_*.sh (the same bug class).
# Contract C: the original B160 fix is preserved
#             ('headscale nodes expire --disable' is in the
#             B160 description, not the backtick form).
# Contract D: TD-15 is mentioned in AGENTS.md.
# Contract E: TD-15 is registered in verify_pre_deploy.sh.
# Contract F: this script is executable.

cd "$(dirname "$0")/.." || exit 1

ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

PASS=0
FAIL=0

# ---------------------------------------------------------------------------
# Contract A: no unescaped backticks in run_check descriptions
#             (B### AND TD-### names)
# ---------------------------------------------------------------------------
# We use Python because the lookbehind-for-odd-number-of-
# preceding-backslashes is a classic regex foot-gun in sed/awk.
# Python is available on both Linux and Windows (with the
# python3 || python fallback for Windows portability).

PYTHON_BIN=""
if command -v python3 >/dev/null 2>&1; then
    PYTHON_BIN=python3
elif command -v python >/dev/null 2>&1; then
    PYTHON_BIN=python
elif command -v py.exe >/dev/null 2>&1; then
    # Windows Python launcher (real binary, not the WindowsApps alias
    # that intercepts 'python3' and prints the Microsoft Store prompt).
    PYTHON_BIN=py.exe
fi

# On Windows, the 'python3' / 'python' shims at
# C:\Users\<user>\AppData\Local\Microsoft\WindowsApps\python3.exe are
# Microsoft Store app execution aliases — they don't actually run a
# Python interpreter; they print "Python was not found; run without
# arguments to install from the Microsoft Store". Detect this by
# checking the binary's actual identity. If PYTHON_BIN resolves to
# the WindowsApps path, fall back to py.exe (the real launcher).
if [ -n "$PYTHON_BIN" ] && command -v cygpath >/dev/null 2>&1; then
    REAL_PATH=$(cygpath -wa "$(command -v "$PYTHON_BIN")" 2>/dev/null || echo "")
    case "$REAL_PATH" in
        *WindowsApps*python*|*Microsoft*WindowsApps*)
            # WindowsApps alias — try py.exe
            if command -v py.exe >/dev/null 2>&1; then
                PYTHON_BIN=py.exe
            else
                PYTHON_BIN=""
            fi
            ;;
    esac
fi

if [ -z "$PYTHON_BIN" ]; then
    bad "contract A: no working python in PATH (WindowsApps alias intercepts python3; py.exe not found either — install Python from python.org or set PATH to a real install)"
else
    violations_a=$("$PYTHON_BIN" scripts/check_td15_unescaped_backticks.py 2>&1)
    rc=$?
    if [ $rc -eq 0 ] && [ -z "$violations_a" ]; then
        ok "contract A: 0 unescaped backticks in any run_check description in verify_pre_deploy.sh (handles B### AND TD-###)"
    else
        bad "contract A: unescaped backticks in run_check descriptions:"
        echo "$violations_a" | sed 's/^/        /'
    fi
fi

# ---------------------------------------------------------------------------
# Contract B: no unescaped backticks in echo lines of scripts/check_*.sh
# ---------------------------------------------------------------------------
if [ -n "$PYTHON_BIN" ]; then
    violations_b=$("$PYTHON_BIN" - <<'PY' 2>/dev/null
import re, os, sys

violations = []
for fname in sorted(os.listdir('scripts')):
    if not (fname.startswith('check_') and fname.endswith('.sh')):
        continue
    path = os.path.join('scripts', fname)
    with open(path, 'r', encoding='utf-8') as f:
        for lineno, line in enumerate(f, 1):
            # Skip pure comment lines
            stripped = line.lstrip()
            if stripped.startswith('#'):
                continue
            if 'echo' not in line:
                continue
            if '"' not in line:
                continue
            m = re.search(r'echo\s+(?:-[a-z]+\s+)?"([^"]*)"', line)
            if not m:
                continue
            content = m.group(1)
            pos = 0
            while True:
                idx = content.find('`', pos)
                if idx < 0:
                    break
                bcount = 0
                b = idx - 1
                while b >= 0 and content[b] == '\\':
                    bcount += 1
                    b -= 1
                if bcount % 2 == 0:
                    ctx = content[max(0,idx-20):idx+20]
                    violations.append(f"{path}:{lineno}: ...{ctx}...")
                pos = idx + 1

if violations:
    for v in violations:
        print(v)
    sys.exit(1)
PY
)
    rc=$?
    if [ $rc -eq 0 ] && [ -z "$violations_b" ]; then
        ok "contract B: 0 unescaped backticks in any echo line of scripts/check_*.sh"
    else
        bad "contract B: unescaped backticks in echo lines:"
        echo "$violations_b" | sed 's/^/        /'
    fi
else
    bad "contract B: skipped (no python3 in PATH)"
fi

# ---------------------------------------------------------------------------
# Contract C: the B160 fix is preserved
# ---------------------------------------------------------------------------
if grep -qF "'headscale nodes expire --disable'" scripts/verify_pre_deploy.sh; then
    ok "contract C: B160 description uses 'headscale nodes expire --disable' (single-quoted, not backticked)"
else
    bad "contract C: B160 description is missing the single-quoted 'headscale nodes expire --disable' fix"
fi

# ---------------------------------------------------------------------------
# Contract D: TD-15 is mentioned in AGENTS.md
# ---------------------------------------------------------------------------
if grep -qF "TD-15" AGENTS.md; then
    ok "contract D: AGENTS.md mentions TD-15"
else
    bad "contract D: AGENTS.md does NOT mention TD-15"
fi

# ---------------------------------------------------------------------------
# Contract E: TD-15 is registered in verify_pre_deploy.sh
# ---------------------------------------------------------------------------
if grep -qF "check_td15.sh" scripts/verify_pre_deploy.sh; then
    ok "contract E: verify_pre_deploy.sh references check_td15.sh"
else
    bad "contract E: verify_pre_deploy.sh does NOT reference check_td15.sh"
fi

# ---------------------------------------------------------------------------
# Contract F: this script is executable
# ---------------------------------------------------------------------------
if [ -x scripts/check_td15.sh ]; then
    ok "contract F: scripts/check_td15.sh is executable"
else
    bad "contract F: scripts/check_td15.sh is NOT executable"
fi

# ---------------------------------------------------------------------------
echo
echo "=== TD-15 summary: $PASS pass, $FAIL fail ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
echo "all contracts satisfied"
