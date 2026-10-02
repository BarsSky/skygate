#!/usr/bin/env bash
# scripts/lib/gosurface.sh — read a Go SURFACE, not a Go FILE.
#
#   . "$(dirname "$0")/lib/gosurface.sh"
#   gosurface TS internal/feature/admin/tailscale.go internal/feature/admin/tailscale_*.go
#   grep -q 'dockerBridgeRanges' "$TS"
#
# WHY THIS EXISTS (refactor Phase D, 2026-10-01)
# ----------------------------------------------
# Most pre-Phase-D contracts were written as `grep PATTERN <one file>`. That
# couples the contract to the physical layout of the code instead of to the
# behaviour it is supposed to protect, and the coupling only ever fires in the
# wrong direction: a pure CODE MOVE turns a green contract red while the product
# is unchanged. Two measured examples from one afternoon of splitting files:
#
#   * internal/telegram/commands_user.go (1983 lines) was split into nine
#     focused files and B279 contract E2 went red — "internal/telegram/
#     commands_user.go no longer uses PickPerNodeTag" — although the telegram
#     user-command layer still uses it, three files away.
#   * internal/feature/admin/tailscale.go (1963 lines) was split into seven and
#     B236/B251/B258/B258_1/B259/B318/B320/B321 all went red for the same
#     reason: not one of them failed because a behaviour was lost.
#
# The contract that was actually wanted is about a SURFACE — "does the admin
# Tailscale surface still do X?" — and a surface is a package prefix, not the
# file the author happened to edit. Reading the whole surface is also STRONGER
# than pinning one file: moving the behaviour anywhere inside the surface keeps
# it covered, and re-introducing the old behaviour anywhere inside the surface
# is caught (the old form only watched one file, so a re-introduction in a
# sibling file was invisible).
#
# WHY CONCATENATE INSTEAD OF PASSING A GLOB TO grep
# -------------------------------------------------
# `grep -c PATTERN a.go b.go` prints ONE COUNT PER FILE and switches the
# surrounding arithmetic of every existing contract from a number to a
# `file:number` list. Concatenating into one file keeps `grep -c` returning
# exactly one number, so an existing contract keeps its meaning verbatim and
# only the path it reads changes.
#
# `_test.go` files are excluded on purpose: a marker that only a test mentions
# must not be able to satisfy a contract about production behaviour.
#
# The caller never has to clean up — every temp file this helper creates is
# removed when the shell exits.
#
# WHY THIS IS AN ARRAY AND NOT `trap -p` STRING SURGERY (measured 2026-10-02)
# -------------------------------------------------------------------------
# A caller may read MORE THAN ONE surface in the same shell: check_b251.sh
# reads the admin/tailscale surface and the cmd/skygate surface, and the
# Phase-D sweep made that the normal shape (108 scripts gained a
# `gosurface SKY_MAIN cmd/skygate/*.go`). The first version composed its
# cleanup by editing the text of the caller's own EXIT trap:
#
#   prev_trap="${prev_trap#trap -- \'}"; prev_trap="${prev_trap%\' EXIT}"
#
# `trap -p EXIT` prints the body RE-QUOTED, and the body this helper installs
# contains quotes of its own (`trap "rm -f '<tmp>'" EXIT`), so on the SECOND
# call within one shell the surgery produced an unparseable action and bash
# printed
#
#   scripts/check_b251.sh: exit trap: line 1: unexpected EOF while looking for
#   matching `''
#
# at exit — a check that had already reported 13/0. The temp files are now an
# array, the handler is installed at most once, and the caller's own EXIT trap
# (captured once, before ours) is unquoted with a single `eval` and re-run.
#
# WHY A SCRIPT MAY SOURCE THIS FILE TWICE (measured 2026-10-02, the second half
# of the same bug). Eight checks already sourced the helper before the Phase-D
# sweep gave them a line-2 `. …/lib/gosurface.sh` as well (check_b236.sh reads
# the admin/tailscale surface at its own line 33). Re-sourcing re-ran the
# initialisers below and RESET `GOSURFACE_TRAP_INSTALLED` to 0 while the trap
# was still installed, so the next `gosurface` captured `trap -p EXIT` — the
# handler itself — into `_gosurface_prev_trap` and the cleanup `eval`-ed itself
# at every shell exit:
#
#   eval _gosurface_cleanup → _gosurface_cleanup → eval _gosurface_cleanup → …
#
# an infinite recursion that ran the process out of stack and exited 139
# (SIGSEGV) AFTER printing a full "RESULT: PASS". The gate reported it as a
# product FAIL — 15 checks (B66/B68/B69/B81/B194/B236/B258/B276/B288/B293/B305/
# B318/B320/B321/B-mod-db-retry) went red on an otherwise green tree. Two
# guards: the initialisers are idempotent, and the previous trap is never
# chained when it IS this handler.
if [ -z "${GOSURFACE_TRAP_INSTALLED:-}" ]; then
  GOSURFACE_TMP=()
  GOSURFACE_TRAP_INSTALLED=0
  _gosurface_prev_trap=""
fi

_gosurface_cleanup() {
  if [ "${#GOSURFACE_TMP[@]}" -gt 0 ]; then
    rm -f "${GOSURFACE_TMP[@]}"
  fi
  if [ -n "$_gosurface_prev_trap" ] && [ "$_gosurface_prev_trap" != "_gosurface_cleanup" ]; then
    eval "$_gosurface_prev_trap"
  fi
}

_gosurface_install_trap() {
  if [ "$GOSURFACE_TRAP_INSTALLED" -eq 1 ]; then
    return 0
  fi
  # Read the caller's trap BEFORE installing ours — reading it afterwards would
  # capture this handler and chain it to itself (the 139 above).
  local prev
  prev="$(trap -p EXIT)"
  GOSURFACE_TRAP_INSTALLED=1
  trap _gosurface_cleanup EXIT
  if [ -n "$prev" ]; then
    prev="${prev#trap -- }"
    prev="${prev% EXIT}"
    case "$prev" in
      _gosurface_cleanup|"_gosurface_cleanup"|"'"_gosurface_cleanup"'") return 0 ;;
    esac
    # Unquote the single-quoted body exactly once; the body may contain quotes.
    eval "_gosurface_prev_trap=$prev" || _gosurface_prev_trap=""
  fi
}

# Globs are expanded HERE, not by the caller, because a check is allowed to run
# with `set -f` and one does: check_b258_1_auth_key_missing.sh disables pathname
# expansion so that a grep pattern like "/data/*" is not turned into a file list
# (see its own comment). With `set -f` in force the caller's `tailscale_*.go`
# reaches this function as a literal, `[ -f ]` rejects it, and the "surface"
# silently becomes one file — measured: contract C of B258.1 reported
# "tailscaleAuthKeyMissingForStart helper missing" while the helper was plainly
# there, two files away. Expanding inside the function makes the result
# independent of the caller's shell options. (File paths here never contain
# spaces, so the deliberate unquoted expansion below is safe.)
# WHY EVERY LOCAL IS NAMED _gs_* (measured 2026-10-02, the third bug in this
# helper). The obvious `local f` shadowed the CALLER's variable of the same name,
# so `gosurface f cmd/skygate/*.go` — the shape check_b194.sh uses — assigned the
# temp path to the local and left the caller's `$f` empty; the next
# `grep -q … "$f"` then matched nothing and seven B194 contracts reported
# "main.go missing deployrun.NewService" on a tree where it sat in main.go.
# `local out` had the same defect. The prefix makes the collision impossible.
gosurface() {
  local _gs_var="$1"; shift
  local _gs_out _gs_arg _gs_file
  local -a _gs_files=()
  local _gs_had_noglob=0
  case "$-" in
    *f*) _gs_had_noglob=1 ;;
  esac
  set +f

  for _gs_arg in "$@"; do
    # shellcheck disable=SC2086  # deliberate: this IS the glob expansion
    for _gs_file in $_gs_arg; do
      [ -f "$_gs_file" ] || continue
      case "$_gs_file" in
        *_test.go) continue ;;
      esac
      _gs_files+=("$_gs_file")
    done
  done

  if [ "$_gs_had_noglob" -eq 1 ]; then
    set -f
  fi

  if [ "${#_gs_files[@]}" -eq 0 ]; then
    printf 'gosurface: no Go files matched: %s\n' "$*" >&2
    return 1
  fi

  _gs_out="$(mktemp "${TMPDIR:-/tmp}/gosurface.XXXXXX")" || {
    printf 'gosurface: mktemp failed\n' >&2
    return 1
  }
  cat "${_gs_files[@]}" >"$_gs_out" || {
    rm -f "$_gs_out"
    printf 'gosurface: could not read %s\n' "${_gs_files[*]}" >&2
    return 1
  }

  # Register the file for removal when the check exits. Checks run under
  # `bash -c` / as their own process (verify_run_check), so this trap belongs to
  # the check alone; install it ONCE per shell and let the handler walk the whole
  # array, so a second surface cannot corrupt it.
  GOSURFACE_TMP+=("$_gs_out")
  _gosurface_install_trap

  eval "$_gs_var=\$_gs_out"
}
