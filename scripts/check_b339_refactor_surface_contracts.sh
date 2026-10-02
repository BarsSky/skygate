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

# The catalog's own inline run_checks may name the path only as a gosurface
# argument (B19/B55/B58/B62/B64/B65/B68 do exactly that) or as the glob form
# (internal/feature/admin/exit_nodes*.go, which is what the exit_nodes split
# uses — quiet greps need no shell variable). Measured 2026-10-01: B64 and B68
# were still grepping internal/acl/acl.go after the acl split and were the ONLY
# two catalog entries that went red for it — the inline commands are easy to
# forget precisely because they are not scripts.
for CAT_PATH in 'internal/feature/admin/tailscale.go' 'internal/acl/acl.go' 'internal/feature/admin/exit_nodes.go'; do
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
