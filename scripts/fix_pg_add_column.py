#!/usr/bin/env python3
"""
fix_pg_add_column.py — one-shot script to add IF NOT EXISTS to
every ALTER TABLE ... ADD COLUMN in migrations_pg.go.

v0.32.24 — the pre-fix code was a mechanical port from the
SQLite migrations, where ALTER TABLE ADD COLUMN fails on a
re-run and the error is caught + ignored. That doesn't work
on PG (the error is "column already exists" with SQLSTATE
42701, which IS fatal in PG). This script rewrites every
ALTER TABLE ADD COLUMN to ALTER TABLE ADD COLUMN IF NOT EXISTS
in migrations_pg.go so the migrations are idempotent on PG.

Usage:
  python3 scripts/fix_pg_add_column.py internal/db/migrations_pg.go
"""
import re
import sys
from pathlib import Path

PATTERN = re.compile(
    r'ALTER TABLE\s+(\w+)\s+ADD COLUMN\s+(?!IF NOT EXISTS)(\w+)',
    re.IGNORECASE
)

def main():
    if len(sys.argv) != 2:
        print(f"Usage: {sys.argv[0]} <path-to-migrations_pg.go>", file=sys.stderr)
        sys.exit(1)
    path = Path(sys.argv[1])
    text = path.read_text()
    fixed = 0
    def replace(m):
        nonlocal fixed
        fixed += 1
        return f'ALTER TABLE {m.group(1)} ADD COLUMN IF NOT EXISTS {m.group(2)}'
    new_text = PATTERN.sub(replace, text)
    if new_text == text:
        print(f"No changes needed in {path}")
        return
    path.write_text(new_text)
    print(f"Updated {fixed} ALTER TABLE ADD COLUMN statements in {path}")

if __name__ == '__main__':
    main()
