#!/usr/bin/env bash
# check_b339_refactor_surface_contracts.sh — B339: a contract reads a SURFACE,
# not a FILE.
#
# WHY THIS FILE EXISTS (refactor Phase D, 2026-10-01)
# ---------------------------------------------------
# The project spent months unable to split its 2000-line files, and the reason
# was not the code: it was the contracts. A `grep <pattern> <path>` contract is
# coupled to the code's LAYOUT, and the coupling only ever fires in the wrong
# direction — a pure code MOVE turns it red while the product is unchanged:
#
#   * internal/telegram/commands_user.go (1983 lines) → nine files  ⇒ B279 E2
#     reported "internal/telegram/commands_user.go no longer uses
#     PickPerNodeTag" while the telegram layer still used it, three files away.
#   * internal/feature/admin/tailscale.go (1963 lines) → seven files ⇒ B236,
#     B251, B258, B258.1, B259, B318, B320, B321 and two Go tests went red in
#     one step, none of them because a behaviour was lost.
#
# A path-pinned contract is also the WEAKER contract while it is green, because
# it cannot see the behaviour being re-introduced in a sibling file — which is
# exactly the regression it exists to catch.
#
# The repair is scripts/lib/gosurface.sh: `gosurface VAR <file-or-glob>…`
# concatenates the named Go files (skipping `_test.go`) into one file. This
# contract keeps the repair from being undone by the next new check, and pins
# the two properties of the helper that are easy to lose: the glob is expanded
# INSIDE the helper (so a check running with `set -f` still gets a surface —
# measured: B258.1 contract C went red with a "helper missing" message while the
# helper was plainly present, because `set -f` left the glob literal), and
# `_test.go` files are excluded (so a marker only a test mentions cannot satisfy
# a production contract).
#
# Usage:  bash scripts/check_b339_refactor_surface_contracts.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

LIB=scripts/lib/gosurface.sh

hdr "A. the surface helper exists and is real"

if [ -f "$LIB" ]; then
  ok "A1: $LIB is present"
else
  bad "A1: $LIB is missing — every surface contract below would read a literal glob"
fi
if git ls-files --error-unmatch "$LIB" >/dev/null 2>&1; then
  ok "A2: $LIB is tracked by git (AGENTS trap #11)"
else
  bad "A2: $LIB is NOT tracked by git — a .gitignore rule is eating it (AGENTS trap #11)"
fi
if grep -q '^gosurface()' "$LIB" 2>/dev/null; then
  ok "A3: gosurface() is defined at top level"
else
  bad "A3: gosurface() is not defined — the callers below would fail at source time"
fi
if grep -q 'set +f' "$LIB" 2>/dev/null; then
  ok "A4: the helper re-enables globbing before expanding (the \`set -f\` trap)"
else
  bad "A4: the helper does not handle a caller running with \`set -f\` — its glob stays a literal"
fi
if grep -q '_test.go' "$LIB" 2>/dev/null; then
  ok "A5: the helper excludes _test.go from the surface"
else
  bad "A5: the helper does not exclude _test.go — a test-only marker could satisfy a production contract"
fi
# A6/A7 pin the 2026-10-02 repair: a caller may read TWO surfaces in one shell
# (check_b251.sh reads the admin/tailscale surface and the cmd/skygate one), and
# the first version composed its cleanup by string-surgery on `trap -p` output,
# which re-quotes a body that itself contains quotes — the second call then
# installed an unparseable EXIT trap and bash printed
# "exit trap: line 1: unexpected EOF while looking for matching `'`" at exit.
if grep -q 'GOSURFACE_TMP' "$LIB" 2>/dev/null; then
  ok "A6: temp files are collected in an array, not spliced into the trap text"
else
  bad "A6: the helper still composes its cleanup from the caller's trap TEXT — a second surface in one shell corrupts it"
fi
if grep -q 'GOSURFACE_TRAP_INSTALLED' "$LIB" 2>/dev/null; then
  ok "A7: the cleanup handler is installed at most once per shell"
else
  bad "A7: the helper reinstalls (and re-splices) the EXIT trap on every call"
fi
# A8/A9 pin the SECOND half of the same bug (measured 2026-10-02): thirty
# scripts source this file twice (the tailscale/acl checks sourced it before the
# Phase-D sweep added a line-2 source), and re-sourcing RESET the installed flag
# while the trap stayed, so the next call chained the handler to itself —
# `eval _gosurface_cleanup` in a loop, stack overflow, exit 139 AFTER a full
# "RESULT: PASS". The gate called that 15 product FAILs.
if grep -q 'if \[ -z "\${GOSURFACE_TRAP_INSTALLED:-}" \]' "$LIB" 2>/dev/null; then
  ok "A8: re-sourcing the helper does not reset its state"
else
  bad "A8: the initialisers clobber existing state — a second source resets the installed flag while the trap stays"
fi
if grep -q '_gosurface_prev_trap" != "_gosurface_cleanup"' "$LIB" 2>/dev/null; then
  ok "A9: the handler refuses to chain itself (the recursion guard)"
else
  bad "A9: nothing stops the cleanup from eval-ing the handler itself"
fi

hdr "B. every contract that reads the SPLIT surfaces uses the surface form"

# The surfaces that have already been split. Adding a split here is how this
# contract covers the next refactor: the file name must stop appearing as a
# grep/sed/awk operand in every check script.
scan_split_path() {
  local path="$1" label="$2" hits
  # This guard is excluded from its own scan: it has to NAME the paths it
  # forbids (that is the whole contract), so it would otherwise always find
  # itself. `require_file` lines are allowed too — asserting that the split
  # file still EXISTS is not reading its contents (check_b236 does exactly that
  # next to its surface call). Nothing else is exempt.
  hits="$(grep -nF -- "$path" scripts/check_*.sh 2>/dev/null \
            | grep -v '^scripts/check_b339_refactor_surface_contracts.sh:' \
            | grep -v ':[[:space:]]*#' \
            | grep -v 'gosurface' \
            | grep -v 'require_file' || true)"
  if [ -n "$hits" ]; then
    bad "B: $label is still read as a single FILE (widen it to the surface — see scripts/lib/gosurface.sh):"
    printf '%s\n' "$hits" | sed 's/^/        /'
  else
    ok "B: no check script reads $label as a single file"
  fi
}

scan_split_path "internal/feature/admin/tailscale.go" "internal/feature/admin/tailscale.go"
scan_split_path "internal/telegram/commands_user.go" "internal/telegram/commands_user.go"
scan_split_path "internal/acl/acl.go" "internal/acl/acl.go"
scan_split_path "internal/feature/admin/exit_nodes.go" "internal/feature/admin/exit_nodes.go"
scan_split_path "internal/feature/admin/telegram.go" "internal/feature/admin/telegram.go"

# cmd/skygate/main.go is the next surface (4515 lines, 2026-10-02). It stays a
# REAL file — 236 occurrences are the "am I in the checkout?" existence test
# and ~100 more are human-readable messages — so only the OPERAND uses are
# forbidden here. The sweep that rewrote them (111 scripts) also had to teach
# each one to source scripts/lib/gosurface.sh; that is what these filters
# protect: a new contract that greps the path directly is the regression.
MAIN_HITS="$(grep -nF -- 'cmd/skygate/main.go' scripts/check_*.sh 2>/dev/null \
              | grep -v '^scripts/check_b339_refactor_surface_contracts.sh:' \
              | grep -v ':[[:space:]]*#' \
              | grep -v 'gosurface' \
              | grep -vE ':[[:space:]]*(\[|test)[[:space:]]' \
              | grep -vE ':[[:space:]]*(if|elif)[[:space:]]+\[[[:space:]]' \
              | grep -vE ':[[:space:]]*(ok|bad|pass|fail|warn|skip|check|echo|printf|hdr|run_check)[[:space:]]' \
              || true)"
if [ -n "$MAIN_HITS" ]; then
  bad "B: cmd/skygate/main.go is still read as a single file (widen it to the package surface — scripts/lib/gosurface.sh):"
  printf '%s\n' "$MAIN_HITS" | sed 's/^/        /'
else
  ok "B: no check script reads cmd/skygate/main.go as an operand (only existence tests and messages)"
fi
# ...and the same scan for the CATALOG's `printf "%s" "<script>" > "$f"` entries.
# Their operands sit INSIDE the payload on a line whose first command is
# `printf`, so the per-line classifier and the "does this line start with grep?"
# scan both treat it as a message — measured 2026-10-02, B66/B68/B69/B81 were the
# only four entries the Phase-D sweep missed and they went red on a pure move.
NESTED_HITS="$(grep -nF -- 'cmd/skygate/main.go' scripts/verify_pre_deploy.sh 2>/dev/null \
                | grep -v 'gosurface' \
                | grep -v ':[[:space:]]*#' \
                | grep -E 'printf "%s"|printf .%s.' || true)"
if [ -n "$NESTED_HITS" ]; then
  bad "B: a catalog inline script reads cmd/skygate/main.go as one file (the operand hides inside the printf payload):"
  printf '%s\n' "$NESTED_HITS" | cut -c1-160 | sed 's/^/        /'
else
  ok "B: no catalog inline script greps cmd/skygate/main.go as one file"
fi
MAIN_SOURCED=0
for c in $(grep -lE 'gosurface [A-Za-z_][A-Za-z0-9_]* [^ ]*cmd/skygate/' scripts/check_*.sh 2>/dev/null); do
  if grep -q 'lib/gosurface.sh' "$c"; then
    MAIN_SOURCED=$((MAIN_SOURCED+1))
  else
    bad "B: $c calls gosurface for the cmd/skygate surface without sourcing scripts/lib/gosurface.sh"
  fi
done
if [ "$MAIN_SOURCED" -gt 0 ]; then
  ok "B: all $MAIN_SOURCED scripts that read the cmd/skygate surface source the helper"
fi

# The catalog's own inline run_checks may name the path only as a gosurface
# argument (B19/B55/B58/B62/B64/B65/B68 do exactly that) or as the glob form
# (internal/feature/admin/exit_nodes*.go, which is what the exit_nodes split
# uses — quiet greps need no shell variable). Measured 2026-10-01: B64 and B68
# were still grepping internal/acl/acl.go after the acl split and were the ONLY
# two catalog entries that went red for it — the inline commands are easy to
# forget precisely because they are not scripts.
for CAT_PATH in 'internal/feature/admin/tailscale.go' 'internal/acl/acl.go' 'internal/feature/admin/exit_nodes.go' 'internal/feature/admin/telegram.go'; do
  CATALOG_HITS="$(grep -nF -- "$CAT_PATH" scripts/verify_pre_deploy.sh 2>/dev/null \
                    | grep -v 'gosurface' \
                    | grep -v ':[[:space:]]*#' || true)"
  if [ -n "$CATALOG_HITS" ]; then
    bad "B: scripts/verify_pre_deploy.sh names $CAT_PATH outside a gosurface call:"
    printf '%s\n' "$CATALOG_HITS" | sed 's/^/        /'
  else
    ok "B: every verify_pre_deploy.sh reference to $CAT_PATH goes through gosurface"
  fi
done

# The eight checks that read the admin Tailscale surface must actually source
# the helper — a bare glob would include _test.go files and could be undone by
# `set -f` (both properties contract D pins behaviourally).
TS_CHECKS="check_b236.sh check_b251.sh check_b258_1_auth_key_missing.sh check_b258_tailscale_disabled.sh check_b259_tailscale_toggle.sh check_b318_tailscale_state_truth.sh check_b320_tailscale_self_name.sh check_b321_tailscale_survives_update.sh"
SOURCED=0
for c in $TS_CHECKS; do
  if grep -q 'lib/gosurface.sh' "scripts/$c" 2>/dev/null && grep -q 'gosurface ' "scripts/$c" 2>/dev/null; then
    SOURCED=$((SOURCED+1))
  else
    bad "B: scripts/$c does not source+call gosurface — its tailscale surface is a literal glob"
  fi
done
if [ "$SOURCED" -eq 8 ]; then
  ok "B: all 8 admin/tailscale contracts read the surface through gosurface"
fi

hdr "C. the helper behaves (behavioural, not grep)"

TMPD="$(mktemp -d)"
trap 'rm -rf "$TMPD"' EXIT
printf 'package p\n\nvar Prod = "B339_PROD_MARKER"\n' > "$TMPD/prod.go"
printf 'package p\n\nvar T = "B339_TEST_MARKER"\n' > "$TMPD/prod_test.go"

# Run the helper in a subshell with pathname expansion DISABLED, the way
# check_b258_1_auth_key_missing.sh runs. The glob the caller passes must still
# become a surface.
SURFACE_OUT="$(
  set -f
  . "$LIB" || exit 1
  gosurface S "$TMPD"/p*.go || exit 1
  cat "$S"
)" || SURFACE_OUT=""
case "$SURFACE_OUT" in
  *B339_PROD_MARKER*) ok "C1: a glob expands to a surface even under \`set -f\` (the literal-glob trap)" ;;
  *) bad "C1: gosurface returned nothing for a glob while \`set -f\` was in force" ;;
esac
case "$SURFACE_OUT" in
  *B339_TEST_MARKER*) bad "C2: the surface contains a _test.go file — a test-only marker can satisfy a production contract" ;;
  *) ok "C2: _test.go files are excluded from the surface" ;;
esac

if ( set -f; . "$LIB"; gosurface S "$TMPD"/nothing*.go ) >/dev/null 2>&1; then
  bad "C3: gosurface returned success when nothing matched — a typo would make a contract vacuous"
else
  ok "C3: gosurface fails when no file matches (a typo cannot turn a contract into a vacuous PASS)"
fi

# The real surface, not a synthetic one: a marker that lives in
# tailscale_config.go (not in tailscale.go) proves the production call sites in
# the catalog's style really concatenate several files.
REAL_OUT="$(
  . "$LIB" || exit 1
  gosurface S internal/feature/admin/tailscale.go internal/feature/admin/tailscale_*.go || exit 1
  cat "$S"
)" || REAL_OUT=""
case "$REAL_OUT" in
  *tailscaleAuthKeyMissingForStart*) ok "C4: the admin/tailscale surface spans its split files (marker from tailscale_config.go found)" ;;
  *) bad "C4: the admin/tailscale surface did not include the split files — the contracts above would silently read one file" ;;
esac

# C5 — TWO surfaces in ONE shell, the shape check_b251.sh (and 100+ other
# checks after the Phase-D sweep) actually uses. The pre-fix helper corrupted
# the EXIT trap on the second call and bash complained at exit about an
# unterminated quote, after the check had already printed its verdict.
TWO_OUT="$( { . "$LIB"; gosurface A internal/feature/admin/tailscale*.go cmd/skygate/*.go; gosurface B cmd/skygate/*.go; printf '%s|%s\n' "$(wc -l < "$A")" "$(wc -l < "$B")"; } 2>&1 )"
case "$TWO_OUT" in
  *"unexpected EOF"*|*"exit trap"*)
    bad "C5: a SECOND surface in one shell corrupts the EXIT trap: $TWO_OUT" ;;
  *"|"*)
    ok "C5: two surfaces in one shell both build and the shell exits cleanly ($TWO_OUT)" ;;
  *)
    bad "C5: two surfaces in one shell did not both build: $TWO_OUT" ;;
esac

# C6 — a caller's OWN EXIT trap must still run (the helper must not eat it).
TRAPDIR="$(mktemp -d)"
(
  . "$LIB"
  trap 'printf caller-trap-ran > "$TRAPDIR/marker"' EXIT
  gosurface A cmd/skygate/*.go || exit 1
  gosurface B cmd/skygate/*.go || exit 1
) >/dev/null 2>&1
if [ -f "$TRAPDIR/marker" ] && [ "$(cat "$TRAPDIR/marker")" = "caller-trap-ran" ]; then
  ok "C6: a pre-existing caller EXIT trap still runs (traps are chained, not replaced)"
else
  bad "C6: the caller's own EXIT trap did not run after gosurface — cleanup in the check is lost"
fi
rm -rf "$TRAPDIR"

# C7 — the shape that produced the 139: source the helper TWICE (thirty checks
# do) and read two surfaces. The shell must exit 0 with an empty stderr; the
# pre-fix code recursed inside its own EXIT trap until the stack ran out, AFTER
# printing a green verdict, so the check looked correct and the gate called it a
# product failure.
TWICE_OUT="$( { . "$LIB"; . "$LIB"; gosurface A internal/feature/admin/tailscale*.go; gosurface B cmd/skygate/*.go; echo twice-ok; } 2>&1 )"
TWICE_RC=$?
case "$TWICE_OUT" in
  *twice-ok*)
    if [ "$TWICE_RC" -eq 0 ] && ! printf '%s' "$TWICE_OUT" | grep -qiE 'segmentation|stack|recursion'; then
      ok "C7: sourcing the helper twice and reading two surfaces exits cleanly (rc=$TWICE_RC)"
    else
      bad "C7: a double source broke the exit path (rc=$TWICE_RC): $TWICE_OUT"
    fi ;;
  *)
    bad "C7: a double source did not get as far as the second surface: $TWICE_OUT" ;;
esac

hdr "E. the split itself stays split"
# the split's two structural promises against a later re-inlining, which is the
# only way the 4655-line file comes back: the route table lives in routes.go and
# is reached through one call, and the boot helpers live in their own files.
if grep -q '^func registerRoutes(' cmd/skygate/routes.go 2>/dev/null; then
  ok "E1: the route table is a function in cmd/skygate/routes.go"
else
  bad "E1: cmd/skygate/routes.go no longer defines registerRoutes — has the table moved back into main()?"
fi
CALLS="$(grep -c '^	registerRoutes(' cmd/skygate/main.go 2>/dev/null || echo 0)"
if [ "$CALLS" = "1" ]; then
  ok "E2: main() calls registerRoutes exactly once"
else
  bad "E2: main() calls registerRoutes $CALLS time(s) — expected exactly 1"
fi
REGS="$(grep -c 'mux\.Handle' cmd/skygate/routes.go 2>/dev/null || echo 0)"
if [ "$REGS" -ge 200 ]; then
  ok "E3: routes.go holds $REGS route registrations (the table, not a stub)"
else
  bad "E3: routes.go holds only $REGS registrations — the table was split back into the boot sequence"
fi
MOVED_OK=0
for pair in "main_subcommands.go:func runMigrateOnly" "main_bootstrap.go:func ensureInfraUser" \
            "main_helpers.go:func tailscaleBackendState"; do
  f="cmd/skygate/${pair%%:*}"; sym="${pair#*:}"
  if grep -q "^$sym" "$f" 2>/dev/null; then
    MOVED_OK=$((MOVED_OK + 1))
  else
    bad "E4: $sym is not in cmd/skygate/$f — a moved helper came back"
  fi
  if grep -q "^$sym" cmd/skygate/main.go 2>/dev/null; then
    bad "E4: $sym is defined in BOTH cmd/skygate/$f and main.go"
  fi
done
if [ "$MOVED_OK" -eq 3 ]; then
  ok "E4: the three tail files still own their anchor helpers (and main.go does not duplicate them)"
fi
MAIN_LINES="$(wc -l < cmd/skygate/main.go 2>/dev/null || echo 0)"
if [ "$MAIN_LINES" -gt 0 ] && [ "$MAIN_LINES" -lt 3000 ]; then
  ok "E5: main.go is $MAIN_LINES lines (was 4655 before the split)"
else
  bad "E5: main.go is $MAIN_LINES lines — the boot sequence grew back into a monolith"
fi

# C8 — the target variable may be named `f`. The helper used `local f` for its
# own loop variable, so `gosurface f …` (check_b194.sh's shape) assigned the temp
# path to the LOCAL and left the caller's `$f` empty: seven B194 contracts
# reported "main.go missing deployrun.NewService" on a tree where it was in
# main.go. Every local is now prefixed _gs_.
F_OUT="$( { . "$LIB"; gosurface f cmd/skygate/*.go && grep -q '^func main(' "$f" && echo f-target-works; } 2>&1 )"
case "$F_OUT" in
  *f-target-works*) ok "C8: a caller whose target variable is named f still receives the surface (no local shadowing)" ;;
  *) bad "C8: gosurface f … did not leave a readable surface in \$f: $F_OUT" ;;
esac

hdr "D. tracked, registered, indexed"

if git ls-files --error-unmatch scripts/check_b339_refactor_surface_contracts.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git (AGENTS trap #11)"
else
  bad "D1: this script is NOT tracked by git — a .gitignore rule is eating it (AGENTS trap #11)"
fi
if grep -q 'check_b339_refactor_surface_contracts.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered in scripts/verify_pre_deploy.sh — it would never run"
fi
if grep -q 'B339' AGENTS.md 2>/dev/null; then
  ok "D3: recorded in the AGENTS.md block index"
else
  bad "D3: no B339 line in the AGENTS.md block index"
fi
if grep -q 'gosurface' docs/internals.md 2>/dev/null; then
  ok "D4: the surface helper is documented in docs/internals.md"
else
  bad "D4: scripts/lib/gosurface.sh is not documented in docs/internals.md"
fi

hdr "B339 summary: $PASS passed, $FAIL failed, $SKIP skipped"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
