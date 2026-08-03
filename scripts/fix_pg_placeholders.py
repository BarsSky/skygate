"""
fix_pg_placeholders.py — convert SQLite `?` placeholders to PG `$N`.

v0.32.22 + v0.32.27 rewrites missed a lot of non-queries.go files. This
script walks a default list and rewrites `?` to `$1, $2, ...` in any
backtick or double-quote string that looks like SQL.

Heuristic: only rewrite if the string contains a SQL keyword (SELECT,
UPDATE, INSERT, DELETE) AND at least one `?`. Each `?` is replaced in
order with $1, $2, etc.

Usage:
  python3 scripts/fix_pg_placeholders.py
  python3 scripts/fix_pg_placeholders.py path/to/file.go [...]
"""
import re
import sys
from pathlib import Path

DEFAULT_FILES = [
    "internal/db/db.go",
    "internal/db/migration_tracking.go",
    "internal/mesh/mesh.go",
    "internal/invite/invite.go",
    "internal/subnet/manager.go",
    "internal/subnet/shares.go",
    "internal/feature/my/devices.go",
    "internal/feature/admin/user_subnet_remove.go",
    "internal/telegram/commands_user.go",
    "internal/feature/exit_rules/api.go",
    "internal/feature/exit_rules/form_my.go",
    "internal/feature/exit_rules/cleanup.go",
    "internal/feature/exit_rules/sync.go",
    "internal/feature/exit_rules/rollback.go",
    "internal/feature/admin/devices.go",
    "internal/feature/admin/subnets.go",
    "internal/feature/admin/users.go",
    "internal/feature/admin/headscale.go",
    "internal/feature/admin/backup_config.go",
    "internal/feature/admin/integrations.go",
    "internal/feature/admin/telegram_strict.go",
    "internal/feature/admin/telegram.go",
    "internal/feature/my/tokens.go",
    "internal/feature/my/keys.go",
    "internal/feature/my/preauth.go",
    "internal/feature/my/account.go",
    "internal/feature/my/exit_nodes.go",
    "internal/feature/my/audit.go",
    "internal/feature/my/meshes.go",
    "internal/feature/my/telegram.go",
    "internal/handlers/*.go",
    "cmd/skygate/*.go",
    "cmd/apply_pg_migrations/*.go",
]

SQL_KEYWORDS = ("SELECT ", "UPDATE ", "INSERT ", "DELETE ", "REPLACE ")


def looks_like_sql(s: str) -> bool:
    upper = s.upper()
    return any(k in upper for k in SQL_KEYWORDS) and "?" in s


def rewrite_placeholders(s: str) -> str:
    counter = [0]

    def repl(_m):
        counter[0] += 1
        return f"${counter[0]}"

    return re.sub(r"\?", repl, s)


def fix_file(path: Path) -> int:
    content = path.read_text(encoding="utf-8")
    out = []
    i = 0
    rewrites = 0
    n = len(content)
    while i < n:
        c = content[i]
        if c == "`":
            # backtick string
            j = i + 1
            while j < n and content[j] != "`":
                if content[j] == "\\" and j + 1 < n:
                    j += 2
                else:
                    j += 1
            if j >= n:
                out.append(content[i:])
                break
            body = content[i + 1 : j]
            out.append(content[i : i + 1])
            if looks_like_sql(body):
                new_body = rewrite_placeholders(body)
                if new_body != body:
                    rewrites += 1
                out.append(new_body)
            else:
                out.append(body)
            out.append("`")
            i = j + 1
        elif c == '"':
            # double-quote string
            j = i + 1
            while j < n and content[j] != '"':
                if content[j] == "\\" and j + 1 < n:
                    j += 2
                else:
                    j += 1
            if j >= n:
                out.append(content[i:])
                break
            body = content[i + 1 : j]
            out.append(content[i : i + 1])
            if looks_like_sql(body):
                new_body = rewrite_placeholders(body)
                if new_body != body:
                    rewrites += 1
                out.append(new_body)
            else:
                out.append(body)
            out.append('"')
            i = j + 1
        else:
            out.append(c)
            i += 1
    new_content = "".join(out)
    if new_content != content:
        path.write_text(new_content, encoding="utf-8")
    return rewrites


def main():
    if len(sys.argv) > 1:
        paths = [Path(f) for f in sys.argv[1:]]
    else:
        paths = []
        for pattern in DEFAULT_FILES:
            if "*" in pattern:
                paths.extend(Path(".").glob(pattern))
            else:
                paths.append(Path(pattern))

    total = 0
    for p in paths:
        if not p.exists():
            continue
        n = fix_file(p)
        if n:
            print(f"  {p}: {n} string(s) rewritten")
            total += n
    print(f"\nTotal: {total} string(s) rewritten across {len(paths)} file(s)")


if __name__ == "__main__":
    main()
