#!/usr/bin/env python3
"""Check verify_pre_deploy.sh for unescaped backticks in run_check descriptions.

Catches the TD-15 bug class: any markdown-style backticks inside a
double-quoted run_check description trigger bash command substitution
on parse, which then tries to exec the bracketed text as a command.

A backtick is "unescaped" if preceded by an EVEN number of backslashes
(0, 2, 4, ...). The LAST backslash escapes it; all earlier backslashes
are themselves escaped.

Usage: check_td15_unescaped_backticks.py  (called from check_td15.sh)
Exit code: 0 if 0 violations, 1 otherwise.
Output: each violation as "NAME: unescaped backtick at pos N: ...CTX..."
"""
import re
import sys


def find_violations(text: str):
    """Return list of (name, idx, context) for every unescaped backtick."""
    # Multi-line form: run_check "B###" "DESC" \n 'cmd'
    # Inline form:      run_check "B###" "DESC" 'cmd'
    pat_ml = re.compile(
        r'run_check\s+"(B\d+(?:_\d+)?|TD-\d+)"\s+'
        r'"((?:[^"\\]|\\.)*?)"\s*'
        r'\\\r?\n\s*'
        r"'([^']+)'",
        re.MULTILINE,
    )
    pat_inl = re.compile(
        r'run_check\s+"(B\d+(?:_\d+)?|TD-\d+)"\s+'
        r'"((?:[^"\\]|\\.)*?)"\s*'
        r"'([^']+)'",
    )
    out = []
    for m in list(pat_ml.finditer(text)) + list(pat_inl.finditer(text)):
        name = m.group(1)
        desc = m.group(2)
        pos = 0
        while True:
            idx = desc.find('`', pos)
            if idx < 0:
                break
            # Count preceding backslashes
            bcount = 0
            b = idx - 1
            while b >= 0 and desc[b] == '\\':
                bcount += 1
                b -= 1
            if bcount % 2 == 0:
                ctx = desc[max(0, idx - 30):idx + 30]
                out.append((name, idx, ctx))
            pos = idx + 1
    return out


def main():
    path = 'scripts/verify_pre_deploy.sh'
    try:
        with open(path, 'r', encoding='utf-8') as f:
            text = f.read()
    except FileNotFoundError:
        print(f"FAIL: {path} not found", file=sys.stderr)
        return 2
    violations = find_violations(text)
    if violations:
        for name, idx, ctx in violations:
            print(f"{name}: unescaped backtick at pos {idx}: ...{ctx}...")
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
