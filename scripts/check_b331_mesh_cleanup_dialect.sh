#!/usr/bin/env bash
# check_b331_mesh_cleanup_dialect.sh
#
# 2026-10-01 (B331) — the smoke-mesh cleanup must work on BOTH backends.
#
# THE DEFECT (measured, not inferred)
# -----------------------------------
# `internal/mesh/cleanup.go` deleted its candidate rows with
#
#	WHERE id = ANY($1::bigint[])
#
# and passed the id list as a PostgreSQL array literal (`{1,2,3}`) in one
# parameter. That is PostgreSQL-only syntax. `DeleteSmokeMeshes` runs from
# `mesh.StartCleanupScheduler` (mounted in `main.go`) on **every** install
# kind, so on SQLite the statement failed with
#
#	SQL logic error: near "[]": syntax error (1)
#
# The failure was invisible until it mattered: the `Total == 0` early return
# means the DELETE is only reached when there is something to clean, so
# **B143's cleanup was silently dead on every SQLite install exactly when it
# had work to do** — the feature looked present and did nothing.
#
# Why no contract caught it: `cleanup_b143_test.go`'s own header said the SQL
# contract was "covered by scripts/check_b143.sh live on the VM", and the
# reference VM is PostgreSQL. The pure-Go tests covered
# `int64ArrayToPGArray` — the helper that PRODUCED the PostgreSQL-only
# argument — so the test suite was pinning the bug rather than the behaviour.
#
# THE FIX
# -------
# The id list is passed as N separate placeholders in an `IN (…)` list, built
# through `db.PlaceholdersList` / `db.PlaceholderAt`. `$N` is the universal
# form (SQLite accepts it as a named parameter and modernc.org/sqlite binds
# it by Go argument ordinal), so ONE statement text serves both backends.
#
# CONTRACTS
#   A. the fix is in place and the PostgreSQL array path is gone
#   B. the class guard: no live `ANY($…)` anywhere in non-test Go code
#   C. the regression test exists and actually catches the old form
#   D. tracked by git (AGENTS trap #11) and registered in the catalog

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B331: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# live_hits <pattern> → non-comment matching lines in TRACKED, non-test Go files.
# Capture-then-match only: never `producer | grep -q` under pipefail (AGENTS trap #9).
live_hits() {
  local pattern="$1" out="" f hits
  while IFS= read -r f; do
    case "$f" in *_test.go) continue ;; esac
    hits="$(grep -nF -- "$pattern" "$f" 2>/dev/null | grep -v ':[[:space:]]*//' || true)"
    if [ -n "$hits" ]; then
      out="${out}${f}:${hits}"$'\n'
    fi
  done < <(git ls-files '*.go')
  printf '%s' "$out"
}

hdr "B331 — the smoke-mesh cleanup speaks both dialects"

# --- A: the fix --------------------------------------------------------------
if grep -q 'DELETE FROM meshes' internal/mesh/cleanup.go; then
  ok "A1: the DELETE statement is still there (the check has a subject)"
else
  bad "A1: internal/mesh/cleanup.go no longer contains the DELETE — this contract is vacuous"
fi

if grep -qF 'db.PlaceholdersList(len(res.IDs))' internal/mesh/cleanup.go &&
   grep -qF 'db.PlaceholderAt(len(res.IDs)+1, len(res.IDs))' internal/mesh/cleanup.go; then
  ok "A2: the IN list is built through db.PlaceholdersList / db.PlaceholderAt (one text, both backends)"
else
  bad "A2: the DELETE does not build its placeholder list through the db helpers — the next edit will hand-type a form again"
fi

if grep -qF 'WHERE id IN (' internal/mesh/cleanup.go; then
  ok "A3: the statement uses the portable IN (…) form"
else
  bad "A3: the statement does not use IN (…) — SQLite cannot express ANY(…) at all"
fi

# The PG array helper produced the PostgreSQL-only argument; it must be gone
# (it was also the thing the old tests pinned).
if grep -q 'int64ArrayToPGArray' internal/mesh/*.go 2>/dev/null; then
  bad "A4: int64ArrayToPGArray is still present — it exists only to build a PostgreSQL array literal"
else
  ok "A4: the PostgreSQL array-literal helper is gone"
fi

# --- B: the class guard (TD-27) ---------------------------------------------
# `ANY($1::…)` is PostgreSQL-only. Comments may quote it (this fix's own
# rationale does), so only live code counts. `internal/db` legitimately builds
# dialect-specific SQL, but even there a LIVE `ANY($` would be a bug in a
# shared statement — so the guard is repo-wide.
HITS="$(live_hits 'ANY($')"
if [ -z "$HITS" ]; then
  ok "B1: no live \`ANY(\$…)\` in any non-test Go file (the class TD-23 belonged to)"
else
  bad "B1: live PostgreSQL-only \`ANY(\$…)\` found — SQLite rejects it outright:"
  printf '%s\n' "$HITS" | sed 's/^/       /' >&2
fi

# --- C: the regression test --------------------------------------------------
if grep -q '^func TestDeleteSmokeMeshes_SQLite' internal/mesh/cleanup_b143_test.go; then
  ok "C1: TestDeleteSmokeMeshes_SQLite exists (the SQL contract is no longer verified only on the PG VM)"
else
  bad "C1: the SQLite round-trip test is missing — the SQL contract would again be PG-only"
fi
if grep -q 'skygatedb.ApplyMigrations(conn, skygatedb.DialectSQLite)' internal/mesh/cleanup_b143_test.go; then
  ok "C2: the test runs against a REAL SQLite database that went through the migration chain"
else
  bad "C2: the test does not open a real migrated SQLite database"
fi

if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/mesh/ -run 'TestDeleteSmokeMeshes_SQLite' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "C3: the SQLite round trip passes"
  else
    bad "C3: the SQLite round trip failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  # A test that matches nothing exits 0 — verify the filter really selects it.
  LIST="$(go test ./internal/mesh/ -list 'TestDeleteSmokeMeshes_SQLite' 2>&1)"
  if grep -q 'TestDeleteSmokeMeshes_SQLite' <<< "$LIST"; then
    ok "C4: the -run filter actually matches the test (no vacuous 'no tests to run')"
  else
    bad "C4: the -run filter matches nothing — the contract would pass vacuously"
  fi
else
  skip "C3-C4: go not on PATH — run the mesh tests on the VM"
fi

# --- D: tracked + registered -------------------------------------------------
if git ls-files --error-unmatch scripts/check_b331_mesh_cleanup_dialect.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git"
else
  bad "D1: this script is NOT tracked — .gitignore can eat it silently"
fi
if grep -q 'check_b331_mesh_cleanup_dialect.sh' scripts/verify_pre_deploy.sh; then
  ok "D2: verify_pre_deploy.sh registers B331"
else
  bad "D2: verify_pre_deploy.sh does not register B331 — the contract would never run"
fi
if grep -q 'B331' AGENTS.md; then
  ok "D3: AGENTS.md's block index carries B331"
else
  bad "D3: AGENTS.md has no B331 entry (AGENTS rule 2)"
fi

printf '\n\033[1mB331 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
