#!/usr/bin/env python3
"""
Rewrite SQLite `?` placeholders to PostgreSQL `$N` placeholders in
Go source files. Operates on string literals that look like SQL
(contain SQL keywords like SELECT, INSERT, UPDATE, DELETE, PRAGMA,
CREATE, ALTER, etc.).

This is a mechanical conversion needed for v0.27.0 PG migration.
The script preserves the file's other content unchanged.

Usage:
  python3 rewrite_placeholders.py path/to/file.go [more.go ...]
  python3 rewrite_placeholders.py --dry-run path/to/file.go
"""
import re
import sys
from pathlib import Path

# SQL keywords that mark a string as a SQL literal.
# Conservative list — only strings starting with or containing these
# are candidates for placeholder rewriting. This avoids touching
# unrelated strings that happen to contain `?` (JSON paths, log
# messages, etc.).
SQL_KEYWORDS = (
    r'\b(SELECT|INSERT|UPDATE|DELETE|REPLACE|PRAGMA|CREATE|ALTER|DROP|'
    r'BEGIN|COMMIT|ROLLBACK|SAVEPOINT|EXPLAIN|ANALYZE|TRUNCATE|'
    r'CREATE\s+INDEX|CREATE\s+TABLE|CREATE\s+VIEW|CREATE\s+TRIGGER|'
    r'CREATE\s+UNIQUE\s+INDEX|'
    r'INSERT\s+OR\s+REPLACE|INSERT\s+OR\s+IGNORE|'
    r'UPDATE\s+OR|DELETE\s+FROM|'
    r'SELECT\s+COUNT|SELECT\s+EXISTS|SELECT\s+sql|'
    r'COALESCE|EXTRACT|GROUP_CONCAT|json_extract)\b'
)

# Regex to find a string literal that looks like SQL.
# Matches double-quoted or backtick raw strings. Newlines are
# excluded from the quoted-string body so a `"` on one line
# doesn't match a `"` on a later line (a real bug we hit when
# the script tried to process cmd/skygate/main.go and saw
# `QueryRow("SELECT ...", ...)` followed by `d.Exec(\`...\`)`,
# collapsing the two into one giant string).
STRING_PATTERN = re.compile(
    r'((?P<raw>`[^`]*`)|(?P<quoted>"(?:[^"\\\n]|\\.)*"))',
)


def looks_like_sql(s):
    """Return True if the string (without its quotes) looks like SQL.

    Strict heuristic: the string MUST contain a SQL keyword. The
    presence of `?` alone is NOT enough because `?` is also used
    in HTTP URL query strings (`/admin/foo?status=active`), in URL
    fragments (`#section?key=val`), in printf-style format hints
    (`%s?`), and in URL-encoding contexts. A previous version of
    this function used `?` as a strong signal and produced
    hundreds of false positives like `$1status=active` rewriting
    `/admin/subnets?status=active`.
    """
    if s.startswith('"') and s.endswith('"'):
        inner = s[1:-1]
    elif s.startswith('`') and s.endswith('`'):
        inner = s[1:-1]
    else:
        return False
    # Must contain a SQL keyword AND look like a query (not just
    # contain the word "select" as part of a regular string).
    return bool(re.search(SQL_KEYWORDS, inner, re.IGNORECASE))


def rewrite_sql(s):
    """Replace `?` placeholders in a SQL string with `$1, $2, $3, ...`.

    Counters reset per string. This is the right semantics for Go
    database/sql: each call is a single statement with its own
    positional parameters.
    """
    return rewrite_sql_with_counter(s, start_at=0)


def process_file(path, dry_run=False):
    """Process one Go file. Returns (changed, num_rewrites).

    Strategy for placeholder numbering:
    1. For each SQL string literal, count the `?` placeholders.
    2. Assign $1, $2, $3... starting from a per-statement counter.
    3. If the same string is part of a concatenation (e.g.,
       `q := ...; q += ...`), use a SHARED counter so the
       placeholders stay globally numbered within the
       final statement.

    The detection of "concatenation" is heuristic: if a
    previous non-empty SQL string was assigned to the same
    variable, and the next SQL string is `+=`ed to the same
    variable, treat as a continuation.
    """
    text = Path(path).read_text(encoding='utf-8')
    out = []
    last = 0
    strings_rewritten = 0
    placeholders_rewritten = 0
    debug = dry_run

    # Map: variable name -> current $N counter for that variable's SQL.
    # Empty/unknown variables get a fresh counter (counter=0 means
    # next placeholder becomes $1).
    var_counter = {}

    # Pattern to find a statement that assigns or appends to a variable.
    # e.g. `q := `foo`` or `q += `bar``
    assignment = re.compile(
        r'(?P<var>[A-Za-z_][A-Za-z0-9_]*)\s*(?P<op>:=|\+=)\s*',
    )

    # Pre-pass: find positions of all assignment/append statements.
    # This lets us know when a string is being concatenated.
    # We track per-variable, in order, the position of the last
    # assignment that contained a SQL string literal.
    var_last_sql = {}  # var name -> (line_no, position_in_text)

    # We iterate twice: first to build var_last_sql, second to do the rewrite.
    # Actually, let's do it in one pass with a small lookahead.
    # Process matches in order. For each match, look at the text just
    # before it for `varname :=` or `varname +=`.

    for m in STRING_PATTERN.finditer(text):
        s = m.group(0)
        if not looks_like_sql(s):
            continue
        # Look at the text immediately before this string literal
        # to find the variable assignment.
        # Walk backward up to 200 chars, looking for `var :=` or
        # `var +=`.
        prefix = text[max(0, m.start() - 200):m.start()]
        # Find the LAST `var :=` or `var +=` before the string.
        matches = list(assignment.finditer(prefix))
        var_name = None
        is_continuation = False
        if matches:
            last_match = matches[-1]
            var_name = last_match.group('var')
            op = last_match.group('op')
            is_continuation = (op == '+=') and (var_name in var_last_sql)

        # Determine the starting counter for this string.
        if is_continuation:
            # Continue from where we left off for this var.
            counter = var_counter.get(var_name, 0)
        else:
            counter = 0

        new_s = rewrite_sql_with_counter(s, start_at=counter)
        # Update the counter for this var.
        new_count = counter + s.count('?')
        if var_name:
            var_counter[var_name] = new_count
            var_last_sql[var_name] = m.start()

        if debug and ('?' in s or '$' in s):
            cont = " (continuation)" if is_continuation else ""
            print(f"    [debug]{cont} var={var_name} counter={counter}->{new_count}: {s[:80]!r}")

        if new_s != s:
            placeholders_rewritten += s.count('?')
            strings_rewritten += 1
            out.append(text[last:m.start()])
            out.append(new_s)
            last = m.end()
    if strings_rewritten == 0:
        return False, 0
    out.append(text[last:])
    new_text = ''.join(out)
    if not dry_run:
        Path(path).write_text(new_text, encoding='utf-8')
    return True, placeholders_rewritten


def rewrite_sql_with_counter(s, start_at=0):
    """Replace `?` placeholders in a Go string literal with $N.

    The input `s` is a Go string literal (either backtick-delimited
    or double-quoted). We treat the *outer* quotes as content
    delimiters, and within the inner content we only skip `?`
    chars that are inside SQL single-quoted strings (e.g. `'now'`
    or `'literal value'`). Double-quotes inside the inner content
    don't matter (PG doesn't use them for string literals).
    """
    # Strip the outer Go string quotes.
    if s.startswith('`') and s.endswith('`'):
        inner = s[1:-1]
        out_quote = '`'
    elif s.startswith('"') and s.endswith('"'):
        inner = s[1:-1]
        out_quote = '"'
    else:
        return s  # not a Go string literal; leave alone

    new_inner = []
    counter = start_at
    in_sql_single_quote = False
    i = 0
    while i < len(inner):
        ch = inner[i]
        if ch == "\\" and i + 1 < len(inner):
            # Escape sequence — keep both chars verbatim.
            new_inner.append(inner[i:i+2])
            i += 2
            continue
        if ch == "'":
            in_sql_single_quote = not in_sql_single_quote
            new_inner.append(ch)
        elif not in_sql_single_quote and ch == '?':
            counter += 1
            new_inner.append(f'${counter}')
        else:
            new_inner.append(ch)
        i += 1
    return out_quote + ''.join(new_inner) + out_quote


def main():
    if len(sys.argv) < 2:
        print("Usage: rewrite_placeholders.py [--dry-run] file.go [file.go ...]")
        sys.exit(1)
    args = sys.argv[1:]
    dry_run = False
    if args and args[0] == '--dry-run':
        dry_run = True
        args = args[1:]
    total_changed = 0
    total_rewrites = 0
    for arg in args:
        changed, rewrites = process_file(arg, dry_run=dry_run)
        marker = "DRY-RUN" if dry_run else ("OK" if changed else "skip")
        print(f"  [{marker}] {arg}: {rewrites} placeholders")
        if changed:
            total_changed += 1
            total_rewrites += rewrites
    print(f"Total: {total_changed} files changed, {total_rewrites} placeholders rewritten")


if __name__ == '__main__':
    main()
