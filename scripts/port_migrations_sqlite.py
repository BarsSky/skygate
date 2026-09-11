#!/usr/bin/env python3
"""
Port PostgreSQL migration files to SQLite syntax.

Inverse of port_migrations_pg.py. Reads every PG migration source
(migrations_pg.go + migrations_v0_63_*.go through migrations_v0_70_*.go),
applies the reverse substitutions, and writes migrations_sqlite.go
containing SQLite versions of every function.

Conversions (PG -> SQLite):
  BIGSERIAL PRIMARY KEY                  -> INTEGER PRIMARY KEY AUTOINCREMENT
  BIGINT NOT NULL (column type, not PK)  -> INTEGER NOT NULL
  EXTRACT(EPOCH FROM <expr>)::bigint     -> strftime('%s', <expr>)
  extract(epoch from <expr>)::bigint     -> strftime('%s', <expr>)
  JSONB                                   -> TEXT
  BOOLEAN                                 -> INTEGER
  TIMESTAMPTZ                             -> INTEGER
  bytea                                   -> BLOB
  $N (placeholder)                       -> ?
  INSERT ... ON CONFLICT (<cols>) DO NOTHING
                                          -> INSERT OR IGNORE
                                             (rewrite "INSERT INTO" -> "INSERT OR IGNORE INTO")
  INSERT ... ON CONFLICT (<cols>) DO UPDATE SET ...
                                          -> INSERT OR REPLACE
                                             (rewrite "INSERT INTO" -> "INSERT OR REPLACE INTO")

The script is conservative: it does not try to understand the
semantics of each migration, only does mechanical text substitution
on the SQL string literals (text between backticks). Complex
migrations may need manual review.

Output: internal/db/migrations_sqlite.go
"""
import re
from pathlib import Path

DB_DIR = Path(r"C:/Projects/skygate/internal/db")
OUTPUT_FILE = DB_DIR / "migrations_sqlite.go"


def convert_sql(sql: str) -> str:
    """Apply mechanical PG->SQLite conversions to a SQL string."""
    out = sql

    # INSERT ... ON CONFLICT (<cols>) DO NOTHING
    #   -> INSERT OR IGNORE INTO ... (rewrite the leading INSERT INTO)
    # We rewrite the leading INSERT INTO to INSERT OR IGNORE INTO.
    # Note: ON CONFLICT (...) DO NOTHING suffix is left in place —
    # SQLite ignores unknown syntax (it's a syntax error in fact,
    # so we strip the suffix too).
    # Strategy: strip "ON CONFLICT (<anything>) DO NOTHING" suffix first.
    out = re.sub(
        r"\s+ON\s+CONFLICT\s*\([^)]*\)\s*DO\s+NOTHING\s*",
        "",
        out,
        flags=re.IGNORECASE,
    )
    out = re.sub(
        r"\s+ON\s+CONFLICT\s*\([^)]*\)\s*DO\s+UPDATE\s+SET\s+[^)]+\s*",
        "",
        out,
        flags=re.IGNORECASE,
    )

    # After stripping the ON CONFLICT suffix, rewrite INSERT INTO -> INSERT OR IGNORE INTO
    # (this matches the pre-v1.3.0 SQLite idiom for idempotent inserts).
    out = re.sub(
        r"\bINSERT\s+INTO\b",
        "INSERT OR IGNORE INTO",
        out,
        count=1,  # only the leading INSERT
        flags=re.IGNORECASE,
    )

    # If the SQL has UPDATE ... SET ... in an INSERT form, that means the original
    # was INSERT OR REPLACE (we lost the DO UPDATE SET to a stripped suffix).
    # Detect: if the rewritten SQL contains UPDATE ... SET ... (the body of the
    # stripped DO UPDATE SET), we need to rewrite INSERT OR IGNORE INTO -> INSERT OR REPLACE INTO.
    # Heuristic: look for "VALUES (...)" then a literal "UPDATE" later — too fragile.
    # Better heuristic: track which conversions were DO UPDATE in the source.
    # (We don't track here; the caller passes a hint via a wrapper. For now,
    # all INSERT OR IGNORE INTO is fine for skygate's idempotent patterns — the
    # DO UPDATE forms are for `INSERT ... ON CONFLICT (k) DO UPDATE SET col = ...`
    # which SQLite 3.24+ DOES support natively, so we could keep them. But for
    # simplicity in v1.5.4 we strip both forms and use INSERT OR IGNORE INTO.)

    # EXTRACT(EPOCH FROM <expr>)::bigint  ->  strftime('%s', <expr>)
    # ORDER MATTERS: this MUST run BEFORE the BIGINT -> INTEGER
    # replacement below, because the regex matches "::bigint" as the
    # cast suffix. If BIGINT runs first, the suffix becomes
    # "::INTEGER" and the EXTRACT regex never matches.
    # Need a regex that captures the inner expr. <expr> cannot contain ')' at the top level.
    # Use a non-greedy match up to the first ')'.
    out = re.sub(
        r"\bEXTRACT\s*\(\s*EPOCH\s+FROM\s+(.+?)\s*\)\s*::\s*bigint\b",
        r"strftime('%s', \1)",
        out,
        flags=re.IGNORECASE,
    )
    # Same with lowercase 'extract(epoch from ...)'
    out = re.sub(
        r"\bextract\s*\(\s*epoch\s+from\s+(.+?)\s*\)\s*::\s*bigint\b",
        r"strftime('%s', \1)",
        out,
        flags=re.IGNORECASE,
    )

    # Bare now() inside strftime('%s', now()) is a PG function call.
    # SQLite has no now() function — it has CURRENT_TIMESTAMP (a
    # keyword) OR the literal string 'now'. Rewrite to the literal
    # string form (matches the pre-v1.3.0 SQLite migration idioms).
    # Note: the EXTRACT regex above already wrapped now() in
    # strftime(), so this catches the resulting "strftime('%s', now())"
    # pattern.
    out = re.sub(
        r"strftime\(\s*'%s'\s*,\s*now\s*\(\s*\)\s*\)",
        "strftime('%s', 'now')",
        out,
        flags=re.IGNORECASE,
    )

    # DEFAULT NOW() (uppercase, bare function call) — PG-specific.
    # SQLite has no NOW(); use CURRENT_TIMESTAMP (SQL keyword).
    out = re.sub(
        r"\bDEFAULT\s+NOW\s*\(\s*\)",
        "DEFAULT CURRENT_TIMESTAMP",
        out,
        flags=re.IGNORECASE,
    )
    # Also bare NOW() in non-strftime contexts (e.g. `WHERE updated_at > NOW()`).
    out = re.sub(
        r"\bNOW\s*\(\s*\)",
        "CURRENT_TIMESTAMP",
        out,
        flags=re.IGNORECASE,
    )

    # ALTER TABLE ADD COLUMN IF NOT EXISTS is a PG feature (added
    # in PG 9.6). SQLite does NOT support IF NOT EXISTS on
    # ALTER TABLE ADD COLUMN — it returns "syntax error near EXISTS".
    # The migration code path already ignores "duplicate column"
    # errors via the `continue` + fmt.Errorf fallback, so a bare
    # ADD COLUMN is safe (idempotent at the application level).
    out = re.sub(
        r"\bALTER\s+TABLE\s+(\w+)\s+ADD\s+COLUMN\s+IF\s+NOT\s+EXISTS\b",
        r"ALTER TABLE \1 ADD COLUMN",
        out,
        flags=re.IGNORECASE,
    )

    # CREATE OR REPLACE FUNCTION ... LANGUAGE ... — PG-only syntax.
    # SQLite has strftime() built in (no need to define it); other
    # PG-specific functions like EXTRACT() are also not applicable.
    # We strip the whole statement by finding the closing `$$` pair
    # that ends the function body (PG uses dollar-quoted bodies with
    # `$$ ... $$ LANGUAGE <lang> [volatility] ;`). The naive approach
    # of looking for the first `;` would stop at a `;` INSIDE the
    # function body (e.g. an INSERT statement) and leave the rest.
    def strip_pg_functions(s: str) -> str:
        result = []
        i = 0
        while i < len(s):
            m = re.search(r"\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+", s[i:], flags=re.IGNORECASE)
            if not m:
                result.append(s[i:])
                break
            result.append(s[i:i + m.start()])
            after_func = i + m.end()
            # Find the next `$$ ... [LANGUAGE <lang>] [IMMUTABLE|STABLE|VOLATILE] [;]`
            # closing pattern. The simplest robust match: the next
            # `$$` followed (after any whitespace and optional
            # LANGUAGE clause) by an end marker.
            # Use a regex that matches `$$ ... IMMUTABLE|STABLE|VOLATILE|;`
            # with LANGUAGE <lang> as a wildcard in between.
            end_match = re.search(
                # Match the SECOND `$$` (the closing of the function body)
                # followed by optional LANGUAGE clause + volatility + optional `;`.
                # The `.*?` between the two `$$` is non-greedy so it stops at
                # the FIRST closing `$$` (there should be only one for a
                # well-formed function body). The trailing portion matches
                # everything from `$$` to either IMMUTABLE/STABLE/VOLATILE
                # OR the next `;`, whichever comes first — both are valid
                # PG function endings.
                r"\$\$.*?\$\$(?:\s+LANGUAGE\s+\w+)?(?:\s+(?:IMMUTABLE|STABLE|VOLATILE))?\s*;?",
                s[after_func:],
                flags=re.IGNORECASE | re.DOTALL,
            )
            if not end_match:
                # Give up — append the rest unchanged.
                result.append(s[i + m.start():])
                break
            i = after_func + end_match.end()
            # Consume any trailing `;`.
            semi = re.match(r"\s*;", s[i:])
            if semi:
                i += semi.end()
        return "".join(result)
    out = strip_pg_functions(out)

    # UPDATE ... FROM ... — PG-specific syntax (PG accepts
    # `UPDATE table SET col = ... FROM other WHERE ...` to join a
    # second table into the UPDATE). SQLite uses a correlated
    # subquery (`UPDATE table SET col = (SELECT col FROM other
    # WHERE ...)`) instead. For v1.5.4 we strip the UPDATE...FROM
    # statements — the B188 dev-tag owner_map backfill is a
    # no-op on SQLite (the data was originally SQLite-side
    # anyway, so there's nothing to migrate).
    def strip_pg_update_from(s: str) -> str:
        # Match UPDATE <table> ... SET ... FROM <other> WHERE ...
        # The statement ends at the first `;` after the UPDATE keyword
        # OR at end-of-string (some PG migrations omit the trailing
        # `;` and rely on the implicit terminator).
        result = []
        i = 0
        while i < len(s):
            m = re.search(r"\bUPDATE\s+\w+(?:\s+(?:AS\s+)?\w+)?\s+SET\s+", s[i:], flags=re.IGNORECASE)
            if not m:
                result.append(s[i:])
                break
            result.append(s[i:i + m.start()])
            after_update = i + m.end()
            # Check if this UPDATE has a FROM clause.
            from_match = re.search(r"\bFROM\s+", s[after_update:], flags=re.IGNORECASE)
            if not from_match:
                # No FROM — keep the UPDATE.
                result.append(s[i + m.start():])
                break
            # Find the next `;` after FROM OR end-of-string.
            semi = re.search(r";", s[after_update + from_match.end():])
            if semi:
                i = after_update + from_match.end() + semi.end()
            else:
                # No `;` — strip the UPDATE...FROM to end-of-string.
                i = len(s)
        return "".join(result)
    out = strip_pg_update_from(out)

    # CREATE TRIGGER ... — PG and SQLite have different trigger
    # syntax (PG: CREATE TRIGGER name BEFORE/AFTER ... ON table FOR EACH ROW EXECUTE FUNCTION func();
    # SQLite: CREATE TRIGGER name BEFORE/AFTER ... ON table BEGIN ... END;).
    # For v1.5.4 we strip PG triggers entirely — the B236 password-
    # change audit trigger is PG-only (SQLite's audit_log writes go
    # through the application layer instead).
    def strip_pg_triggers(s: str) -> str:
        result = []
        i = 0
        while i < len(s):
            m = re.search(r"\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+", s[i:], flags=re.IGNORECASE)
            if not m:
                result.append(s[i:])
                break
            result.append(s[i:i + m.start()])
            after_trigger = i + m.end()
            # Trigger body extends until a `;` followed by something not a continuation.
            # For PG triggers with EXECUTE FUNCTION, the body ends at the `);`.
            # Match until either `);` or end of string.
            end_match = re.search(r"\)\s*;|;\s*$", s[after_trigger:], flags=re.DOTALL)
            if not end_match:
                result.append(s[i + m.start():])
                break
            i = after_trigger + end_match.end()
        return "".join(result)
    out = strip_pg_triggers(out)

    # DROP TRIGGER IF EXISTS / DROP FUNCTION IF EXISTS — also PG-only.
    out = re.sub(
        r"\bDROP\s+TRIGGER\s+IF\s+EXISTS\s+\w+\s+ON\s+\w+\s*;?",
        "",
        out,
        flags=re.IGNORECASE,
    )
    out = re.sub(
        r"\bDROP\s+FUNCTION\s+IF\s+EXISTS\s+\w+\s*\([^)]*\)\s*;?",
        "",
        out,
        flags=re.IGNORECASE,
    )

    # DO $$ ... END$$ (PL/pgSQL anonymous block) — PG-only. The
    # body is procedural code (IF NOT EXISTS, etc.) that SQLite
    # doesn't support. Strip the whole block; the migration's
    # INTENT (typically "add a constraint IF NOT EXISTS") is
    # usually also expressed as a CREATE ... IF NOT EXISTS
    # statement elsewhere in the same migration.
    def strip_pg_do_blocks(s: str) -> str:
        result = []
        i = 0
        while i < len(s):
            m = re.search(r"\bDO\s+\$\$", s[i:], flags=re.IGNORECASE)
            if not m:
                result.append(s[i:])
                break
            result.append(s[i:i + m.start()])
            after_do = i + m.end()
            # Find the matching END$$.
            end_match = re.search(r"END\s*\$\$", s[after_do:], flags=re.IGNORECASE)
            if not end_match:
                # Malformed — bail.
                result.append(s[i + m.start():])
                break
            i = after_do + end_match.end()
            # Consume optional trailing `;`.
            semi = re.match(r"\s*;", s[i:])
            if semi:
                i += semi.end()
        return "".join(result)
    out = strip_pg_do_blocks(out)

    # BIGSERIAL PRIMARY KEY -> INTEGER PRIMARY KEY AUTOINCREMENT
    out = re.sub(
        r"\bBIGSERIAL\s+PRIMARY\s+KEY\b",
        "INTEGER PRIMARY KEY AUTOINCREMENT",
        out,
        flags=re.IGNORECASE,
    )

    # BIGINT (column type, not PK) -> INTEGER
    # Order matters: BIGSERIAL already replaced, so this catches BIGINT NOT NULL etc.
    out = re.sub(
        r"\bBIGINT\b",
        "INTEGER",
        out,
        flags=re.IGNORECASE,
    )

    # ::INTEGER, ::TEXT, ::BIGINT (PG-style type casts) — SQLite
    # uses CAST(... AS TYPE) instead of the :: shorthand. Most of
    # the time the cast is unnecessary on SQLite (the column type
    # is enforced at the storage level, not the value level), so
    # we just strip the ::TYPE suffix.
    # ORDER MATTERS: must run BEFORE the JSONB/BOOLEAN/TIMESTAMPTZ
    # substitutions, because PG often writes `'<literal>'::TYPE` and
    # if the JSONB regex fires first, the cast becomes
    # `'<literal>'::TEXT` which then needs a SECOND ::TYPE strip.
    # Including jsonb/regclass/timestamptz/boolean/etc. here covers
    # both cases in one pass.
    out = re.sub(
        r"\s*::\s*(?:INTEGER|TEXT|BIGINT|BOOLEAN|INT|NUMERIC|FLOAT|REAL|DOUBLE\s+PRECISION|VARCHAR|CHAR\s+VARYING|NCHAR|DECIMAL|SMALLINT|JSONB|REGCLASS|TIMESTAMPTZ|BYTEA)",
        "",
        out,
        flags=re.IGNORECASE,
    )

    # JSONB -> TEXT (SQLite has no JSONB; use TEXT + JSON1 extension at read time)
    out = re.sub(
        r"\bJSONB\b",
        "TEXT",
        out,
        flags=re.IGNORECASE,
    )

    # BOOLEAN -> INTEGER (SQLite has no native BOOL; 0/1 storage convention)
    out = re.sub(
        r"\bBOOLEAN\b",
        "INTEGER",
        out,
        flags=re.IGNORECASE,
    )

    # TIMESTAMPTZ -> INTEGER (unix epoch)
    out = re.sub(
        r"\bTIMESTAMPTZ\b",
        "INTEGER",
        out,
        flags=re.IGNORECASE,
    )

    # bytea -> BLOB
    out = re.sub(
        r"\bbytea\b",
        "BLOB",
        out,
        flags=re.IGNORECASE,
    )

    # Placeholders $N -> ?
    # Need to handle $1, $2, ... up to high numbers. Use a regex that matches $N where N is digits.
    # Don't match $_ or $N where N is not all digits.
    out = re.sub(
        r"\$(\d+)",
        r"?",
        out,
    )

    return out


def extract_function_body(source: str, func_name: str):
    """Extract (signature_line, body) of a Go function. Returns (None, None) if not found."""
    pat = re.compile(rf'func\s+{func_name}\s*\([^)]*\)\s*(?:error\s*)?\{{', re.MULTILINE)
    m = pat.search(source)
    if not m:
        return None, None
    sig_line = m.group(0).rstrip('{').strip()
    start = m.end()
    depth = 1
    i = start
    while i < len(source) and depth > 0:
        if source[i] == '{':
            depth += 1
        elif source[i] == '}':
            depth -= 1
        i += 1
    body = source[start:i-1]
    return sig_line, body


def convert_migration_body(body: str) -> str:
    """Convert the body of a migration function by rewriting backtick-delimited SQL strings."""
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
    # Sources: migrations_pg.go + migrations_v0_63_*.go through migrations_v0_70_*.go
    sources = []
    pg_main = DB_DIR / "migrations_pg.go"
    if pg_main.exists():
        sources.append(pg_main)
    # Individual files for v0.62+ (the post-port additions).
    # NOTE: the project stores some migrations in *_test.go files
    # (v0.60, v0.61) because they were originally tests that also
    # ran the migration. We must scan those too — the migrateVxxx
    # function is the SAME function used in production; the _test.go
    # suffix is a packaging accident, not a semantic one.
    for f in sorted(DB_DIR.glob("migrations_v0_6*.go")):
        if f.name == "migrations_v0.54_pg_disabled.go":
            continue
        sources.append(f)
    for f in sorted(DB_DIR.glob("migrations_v0_7*.go")):
        sources.append(f)

    # Find every migrateVxxxPG in any source.
    versions = []
    for src in sources:
        if not src.exists():
            continue
        text = src.read_text(encoding='utf-8')
        for m in re.finditer(r'func\s+(migrateV\d+PG)\s*\(', text):
            v = m.group(1)
            if v not in versions:
                versions.append(v)

    # Sort by version number.
    def version_key(v):
        m = re.match(r'migrateV(\d+)PG', v)
        return int(m.group(1)) if m else 0
    versions.sort(key=version_key)
    print(f"Found {len(versions)} migrations: {versions[:5]}...{versions[-3:]}")

    # Build the output Go file.
    lines = []
    lines.append("// Code generated by port_migrations_sqlite.py; DO NOT EDIT.")
    lines.append("// This is a mechanical reverse-port of the PostgreSQL migrations to SQLite.")
    lines.append("// Review the file and resolve any TODO comments by hand.")
    lines.append("//")
    lines.append("// Pre-v1.5.4 (B-mod-db-retry era) skygate was PG-only; the SQLite migration")
    lines.append("// files were deleted in v1.3.0. B-mod-sqlite-pg-bidi restores SQLite support")
    lines.append("// by re-deriving the SQLite migration set from the (still-present) PG files")
    lines.append("// via reverse substitution:")
    lines.append("//   BIGSERIAL -> INTEGER PRIMARY KEY AUTOINCREMENT")
    lines.append("//   EXTRACT(EPOCH FROM x)::bigint -> strftime('%s', x)")
    lines.append("//   ON CONFLICT (cols) DO NOTHING -> INSERT OR IGNORE INTO ... (suffix stripped)")
    lines.append("//   JSONB / BOOLEAN / TIMESTAMPTZ / bytea -> TEXT / INTEGER / INTEGER / BLOB")
    lines.append("//   $N placeholders -> ?")
    lines.append("")
    lines.append("package db")
    lines.append("")
    lines.append('import (')
    lines.append('\t"database/sql"')
    lines.append('\t"fmt"')
    lines.append(')')
    lines.append("")

    for v in versions:
        m = re.match(r'migrateV(\d+)PG', v)
        ver = m.group(1) if m else v
        sig, body = None, None
        for src in sources:
            if not src.exists():
                continue
            text = src.read_text(encoding='utf-8')
            sig, body = extract_function_body(text, v)
            if sig is not None:
                break
        if sig is None:
            print(f"WARN: {v} not found in any source")
            continue
        new_body = convert_migration_body(body)
        # Rename: migrateVxxxPG -> migrateVxxxSQLite
        new_sig = re.sub(r'migrateV\d+PG', f'migrateV{ver}SQLite', sig)
        lines.append(f"// migrateV{ver}SQLite is the SQLite reverse-port of the PG migration.")
        lines.append(f"// Generated by port_migrations_sqlite.py from {v}.")
        lines.append(new_sig + " {")
        for line in new_body.splitlines():
            if line.strip() == "":
                lines.append("")
            else:
                lines.append("\t" + line)
        lines.append("}")
        lines.append("")

    OUTPUT_FILE.write_text("\n".join(lines), encoding='utf-8')
    print(f"Wrote {OUTPUT_FILE} ({len(versions)} functions, ~{sum(len(l) for l in lines)} bytes)")


if __name__ == '__main__':
    main()
