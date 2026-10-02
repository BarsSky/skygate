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
# The caller never has to clean up — the temp file is removed on shell exit.
#
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
gosurface() {
  local var="$1"; shift
  local out arg f
  local -a files=()
  local had_noglob=0
  case "$-" in
    *f*) had_noglob=1 ;;
  esac
  set +f

  for arg in "$@"; do
    # shellcheck disable=SC2086  # deliberate: this IS the glob expansion
    for f in $arg; do
      [ -f "$f" ] || continue
      case "$f" in
        *_test.go) continue ;;
      esac
      files+=("$f")
    done
  done

  if [ "$had_noglob" -eq 1 ]; then
    set -f
  fi

  if [ "${#files[@]}" -eq 0 ]; then
    printf 'gosurface: no Go files matched: %s\n' "$*" >&2
    return 1
  fi

  out="$(mktemp "${TMPDIR:-/tmp}/gosurface.XXXXXX")" || {
    printf 'gosurface: mktemp failed\n' >&2
    return 1
  }
  cat "${files[@]}" >"$out" || {
    rm -f "$out"
    printf 'gosurface: could not read %s\n' "${files[*]}" >&2
    return 1
  }

  # Register the file for removal when the check exits. Checks run under
  # `bash -c` / as their own process (verify_pre_deploy.sh run_check), so this
  # trap belongs to the check alone; append rather than replace so a check that
  # already has an EXIT trap keeps it.
  local cleanup prev_trap
  cleanup="rm -f '$out'"
  prev_trap="$(trap -p EXIT)"
  if [ -n "$prev_trap" ]; then
    prev_trap="${prev_trap#trap -- \'}"
    prev_trap="${prev_trap%\' EXIT}"
    cleanup="$prev_trap; $cleanup"
  fi
  # shellcheck disable=SC2064
  trap "$cleanup" EXIT

  eval "$var=\$out"
}
