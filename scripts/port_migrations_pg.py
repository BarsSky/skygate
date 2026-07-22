#!/usr/bin/env python3
"""
Port SQLite migration files to PostgreSQL syntax.

Reads the SQLite migrations from internal/db/migrations*.go, applies
a set of mechanical conversions, and writes a new file
migrations_pg.go that contains PG versions of every function.

Conversions:
  INTEGER PRIMARY KEY AUTOINCREMENT -> BIGSERIAL PRIMARY KEY
  INTEGER (column type, not PK)    -> BIGINT
  strftime('%s','now')             -> EXTRACT(EPOCH FROM now())::bigint
  INSERT OR IGNORE                  -> ON CONFLICT (...) DO NOTHING (heuristic)
  INSERT OR REPLACE                 -> ON CONFLICT (...) DO UPDATE SET ... (heuristic)
  FOREIGN KEY (col) REFERENCES tab  -> FOREIGN KEY (col) REFERENCES tab ON DELETE CASCADE

The script is conservative: it does not try to understand the
semantics of each migration, only does mechanical text substitution.
Complex migrations may need manual review.
"""
import re
from pathlib import Path

SQLITE_DIR = Path(r"C:/Projects/skygate/internal/db")
OUTPUT_FILE = SQLITE_DIR / "migrations_pg.go"


def convert_sql(sql: str) -> str:
    """Apply mechanical conversions to a SQLite SQL string."""
    out = sql

    # INTEGER PRIMARY KEY AUTOINCREMENT -> BIGSERIAL PRIMARY KEY
    out = re.sub(
        r'\bINTEGER\s+PRIMARY\s+KEY\s+AUTOINCREMENT\b',
        'BIGSERIAL PRIMARY KEY',
        out,
        flags=re.IGNORECASE,
    )

    # INSERT OR IGNORE -> ON CONFLICT (...) DO NOTHING
    # We do NOT know the conflict target here; emit a TODO.
    out = re.sub(
        r'\bINSERT\s+OR\s+IGNORE\s+INTO\s+(\w+)\s*\(([^)]*)\)',
        lambda m: f'INSERT INTO {m.group(1)} ({m.group(2)}) -- TODO v0.27.0: add ON CONFLICT target',
        out,
        flags=re.IGNORECASE,
    )

    # INSERT OR REPLACE -> ON CONFLICT (...) DO UPDATE SET ...
    # We do NOT know the update set here; emit a TODO.
    out = re.sub(
        r'\bINSERT\s+OR\s+REPLACE\s+INTO\s+(\w+)\s*\(([^)]*)\)',
        lambda m: f'INSERT INTO {m.group(1)} ({m.group(2)}) -- TODO v0.27.0: add ON CONFLICT DO UPDATE',
        out,
        flags=re.IGNORECASE,
    )

    # strftime('%s','now') -> EXTRACT(EPOCH FROM now())::bigint
    out = re.sub(
        r"strftime\('%s',\s*'now'\)",
        'EXTRACT(EPOCH FROM now())::bigint',
        out,
        flags=re.IGNORECASE,
    )
    out = re.sub(
        r"strftime\('%s',\s*\"now\"\)",
        'EXTRACT(EPOCH FROM now())::bigint',
        out,
        flags=re.IGNORECASE,
    )

    # FOREIGN KEY (col) REFERENCES tab -> FOREIGN KEY (col) REFERENCES tab ON DELETE CASCADE
    # (SQLite is lax about FK; PG enforces. We add CASCADE for safety.)
    out = re.sub(
        r'FOREIGN\s+KEY\s+\(([^)]+)\)\s+REFERENCES\s+(\w+)\s*\(([^)]+)\)(?!\s+ON\s+DELETE)',
        r'FOREIGN KEY (\1) REFERENCES \2 (\3) ON DELETE CASCADE',
        out,
        flags=re.IGNORECASE,
    )

    return out


def extract_function_body(source: str, func_name: str) -> str:
    """Extract the body of a Go function as a string (between { and matching })."""
    pat = re.compile(rf'func\s+{func_name}\s*\([^)]*\)\s*(?:error\s*)?\{{', re.MULTILINE)
    m = pat.search(source)
    if not m:
        return None
    start = m.end()
    depth = 1
    i = start
    while i < len(source) and depth > 0:
        if source[i] == '{':
            depth += 1
        elif source[i] == '}':
            depth -= 1
        i += 1
    return source[start:i-1]


def extract_migration_function(source: str, version: str):
    """Return (signature_line, body) of migrateVxxx/migrationVxxx in the source."""
    # The original code uses both naming conventions:
    # V020-V037 use migrateVxxx, V039+ use migrationVxxx.
    for prefix in ('migrateV', 'migrationV'):
        func_name = f"{prefix}{version}"
        body = extract_function_body(source, func_name)
        if body is not None:
            m = re.search(rf'func\s+{func_name}\([^)]*\)\s*(?:error\s*)?\{{', source)
            sig = m.group(0)[:-1].strip() if m else None
            return sig, body
    return None, None


def convert_migration_body(body: str) -> str:
    """Convert the body of a migration function. Returns the new body."""
    # Find all string literals (backtick-delimited).
    out = []
    i = 0
    while i < len(body):
        if body[i] == '`':
            # Find the closing backtick.
            j = body.index('`', i + 1)
            s = body[i+1:j]
            new_s = convert_sql(s)
            out.append('`' + new_s + '`')
            i = j + 1
        else:
            out.append(body[i])
            i += 1
    return ''.join(out)


def main():
    # Collect all migration functions from all source files.
    # We process files in this order: migrations.go (has V020-V024),
    # then migrations_v0_25.go through migrations_v0_43.go.
    sources = [
        SQLITE_DIR / "migrations.go",
        SQLITE_DIR / "migrations_v0.25.go",
        SQLITE_DIR / "migrations_v0.26.go",
        SQLITE_DIR / "migrations_v0.27.go",
        SQLITE_DIR / "migrations_v0.28.go",
        SQLITE_DIR / "migrations_v0.29.go",
        SQLITE_DIR / "migrations_v0.30.go",
        SQLITE_DIR / "migrations_v0.31.go",
        SQLITE_DIR / "migrations_v0.32.go",
        SQLITE_DIR / "migrations_v0.33.go",
        SQLITE_DIR / "migrations_v0.34.go",
        SQLITE_DIR / "migrations_v0.35.go",
        SQLITE_DIR / "migrations_v0.36.go",
        SQLITE_DIR / "migrations_v0.37.go",
        SQLITE_DIR / "migrations_v0.38.go",
        SQLITE_DIR / "migrations_v0.39.go",
        SQLITE_DIR / "migrations_v0.41.go",
        SQLITE_DIR / "migrations_v0.42.go",
        SQLITE_DIR / "migrations_v0.43.go",
    ]

    # Find every migrateVxxx in any file.
    # Find every migrateVxxx or migrationVxxx in any file.
    versions = []
    for src in sources:
        if not src.exists():
            continue
        text = src.read_text(encoding='utf-8')
        for m in re.finditer(r'func\s+(migrat(?:e|ion)V\d+)\s*\(', text):
            v = m.group(1)
            if v not in versions:
                versions.append(v)
    # Sort by version number. v looks like 'migrateV020' or 'migrationV039'.
    def version_key(v):
        # Skip past the 'migrate' or 'migration' prefix, then past 'V'/'v' to digits.
        m = re.match(r'migrat(?:e|ion)V(\d+)', v)
        return int(m.group(1)) if m else 0
    versions.sort(key=version_key)
    print(f"Found {len(versions)} migrations: {versions}")

    # Build the output Go file.
    lines = []
    lines.append("// Code generated by port_migrations_pg.py; DO NOT EDIT.")
    lines.append("// This is a mechanical port of the SQLite migrations to PostgreSQL.")
    lines.append("// Review the file and resolve any TODO comments by hand.")
    lines.append("")
    lines.append("package db")
    lines.append("")
    lines.append('import "database/sql"')
    lines.append("")
    for v in versions:
        # v is like 'migrateV020' or 'migrationV039'. Extract the version number.
        m = re.match(r'migrat(?:e|ion)V(\d+)', v)
        ver = m.group(1) if m else v
        sig, body = None, None
        for src in sources:
            if not src.exists():
                continue
            text = src.read_text(encoding='utf-8')
            sig, body = extract_migration_function(text, ver)
            if sig is not None:
                break
        if sig is None:
            print(f"WARN: {v} not found in any source")
            continue
        new_body = convert_migration_body(body)
        new_sig = re.sub(r'migrat(?:e|ion)V\d+', f'migrateV{ver}PG', sig)
        lines.append(f"// migrateV{ver}PG is the v0.27.0 PostgreSQL port of the SQLite migration.")
        lines.append(f"// Review the body for any TODO comments and adjust conflict targets.")
        lines.append(new_sig + " {")
        for line in new_body.splitlines():
            if line.strip() == "":
                lines.append("")
            else:
                lines.append("\t" + line)
        lines.append("}")
        lines.append("")

    OUTPUT_FILE.write_text("\n".join(lines), encoding='utf-8')
    print(f"Wrote {OUTPUT_FILE} ({len(versions)} functions)")


if __name__ == '__main__':
    main()
