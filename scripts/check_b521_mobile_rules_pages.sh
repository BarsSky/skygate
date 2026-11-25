#!/usr/bin/env bash
# check_b521_mobile_rules_pages.sh
#
# 2026-11-25 (B521) — the reworked rules pages measured in a REAL browser at a phone
# width, not asserted by their shape.
#
# WHY THE STATIC GUARDS WERE NOT ENOUGH. B357/B410/B411/B435.1 assert that tables sit
# inside `.table-wrap`, that no inline width exceeds a budget and that the `nowrap`
# count does not grow. All of them were green while the rework added a SEVEN-column
# row table, a selection bar with four buttons, a card-action bar and a numbered
# pager to the same page — and none of them can say whether the document actually
# overflows at 390px. The operator's standing requirement is «на телефоне ничего не
# должно наползать», and the honest way to answer it is to lay the page out.
#
# HOW. `internal/handlers/mobile_snapshot_b521_test.go` renders the three states a
# phone can be looking at (group list, one group open, the admin page) into
# self-contained HTML with the install's own themes.css; it is a TOOL guarded by
# SKYGATE_MOBILE_DIR, so a normal test run does nothing. This script exports those
# snapshots to a temp dir and hands them to `scripts/mobile-overflow-check.cjs`, which
# drives headless Edge/Chrome through the DevTools protocol, emulates 390x844 (and
# 360 wide, the bottom of the supported band) with touch + mobile flags, and reports
# the document's horizontal overflow plus the widest offending element.
#
# WHERE IT SKIPS: without a headless browser or without node the measurement cannot
# run — that is missing live state, so the contract prints SKIP and passes (rule 1),
# naming what is missing. The snapshot export itself still runs and is checked,
# because a broken exporter would silently turn this contract into a no-op.
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B521: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SNAP=internal/handlers/mobile_snapshot_b521_test.go
DRIVER=scripts/mobile-overflow-check.cjs

hdr "B521 — the reworked rules pages at 390px, measured by a browser"

# --- A: the tool exists and is still a TOOL ------------------------------------
A_MISS=""
grep -q 'func TestB521_ExportMobileSnapshots(' "$SNAP" \
  || A_MISS="${A_MISS}the snapshot exporter is missing"$'\n'
grep -q 'SKYGATE_MOBILE_DIR' "$SNAP" \
  || A_MISS="${A_MISS}the exporter is not guarded by SKYGATE_MOBILE_DIR (it would either run silently or write files in CI)"$'\n'
grep -q "Emulation.setDeviceMetricsOverride" "$DRIVER" \
  || A_MISS="${A_MISS}the driver does not emulate a device viewport"$'\n'
grep -q "mobile: true" "$DRIVER" \
  || A_MISS="${A_MISS}the driver does not set the mobile flag (a desktop-layout measurement would answer another question)"$'\n'
grep -q "Runtime.evaluate" "$DRIVER" \
  || A_MISS="${A_MISS}the driver never evaluates the overflow expression"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: the exporter is env-guarded and the driver emulates a mobile viewport before measuring"
else
  bad "A1: the mobile tooling is incomplete:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi

# --- B: export the snapshots (needs Go; a broken exporter must not silently skip) -
WORK="$(mktemp -d /tmp/skygate-b521.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
if command -v go >/dev/null 2>&1; then
  OUT="$(SKYGATE_MOBILE_DIR="$WORK" go test ./internal/handlers/ -run TestB521_ExportMobileSnapshots -count=1 2>&1)"
  RC=$?
  if [ "$RC" -eq 0 ]; then
    N=$(ls "$WORK"/*.html 2>/dev/null | wc -l | tr -d ' ')
    if [ "${N:-0}" -eq 3 ]; then
      ok "B1: the exporter rendered all three states (group list, one group, admin page)"
    else
      bad "B1: the exporter produced ${N:-0} file(s), want 3 — a state stopped rendering"
      printf '%s\n' "$OUT" | tail -6 | sed 's/^/       /' >&2
    fi
    # B2 — the measurement must be of the PAGE the operator sees. The render helpers
    # shared with the structural tests stub `t`/`tf` to ECHO the catalogue key (that is
    # how those tests assert "this block renders key K"), so an unlocalized export
    # measures "exit_rules.target_type_subnet" instead of «Подсеть»: a 28-character
    # unbreakable token per label, which answers a different question and makes the
    # page look unfixable. The exporter localizes with the real catalogue; this proves
    # it stayed that way and that the Russian catalogue actually reached the file.
    FID=""
    for f in "$WORK"/*.html; do
      [ -f "$f" ] || continue
      # Trap #15 applies inside this very contract: a measure that FAILED must not be
      # reported as a product claim. Both greps below are checked for their own error
      # output first.
      ERRF="$(LC_ALL=C.UTF-8 grep -oE '>exit_rules[a-z_]*\.[a-z0-9_.]+<' "$f" 2>&1 >/dev/null || true)"
      if [ -n "$ERRF" ]; then
        FID="${FID}$(basename "$f"): could not scan for keys: ${ERRF}"$'\n'
        continue
      fi
      L="$(LC_ALL=C.UTF-8 grep -oE '>exit_rules[a-z_]*\.[a-z0-9_.]+<' "$f" || true)"
      if [ -n "$L" ]; then
        FID="${FID}$(basename "$f"): unlocalized key(s): $(printf '%s' "$L" | tr '\n' ' ')"$'\n'
      fi
      # "Is there Russian text?" is measured in BYTES under LC_ALL=C, never with a
      # [А-Я] bracket range: GNU grep 3.x answers "Invalid collation character" for a
      # multibyte range in some locales, and the empty read that produced was reported
      # as «the catalogue was not applied» — a fact about this check dressed up as a
      # fact about the page. Measured on the reference VM 2026-11-25: the range failed
      # there while the same line worked in Git Bash. Two consecutive high-bit bytes
      # are one Cyrillic character in UTF-8, and 50 of them cannot be a stray glyph.
      CYERR="$(LC_ALL=C grep -o $'[\x80-\xff][\x80-\xff]' "$f" 2>&1 >/dev/null || true)"
      if [ -n "$CYERR" ]; then
        FID="${FID}$(basename "$f"): could not measure Cyrillic: ${CYERR}"$'\n'
        continue
      fi
      CYT="$(LC_ALL=C grep -o $'[\x80-\xff][\x80-\xff]' "$f" 2>/dev/null | wc -l | tr -d ' ')"
      if [ "${CYT:-0}" -lt 50 ]; then
        FID="${FID}$(basename "$f"): only ${CYT:-0} Cyrillic character(s) — the catalogue was not applied"$'\n'
      fi
    done
    if [ -z "$FID" ]; then
      ok "B2: the snapshots carry the real Russian catalogue, not raw catalogue keys"
    else
      bad "B2: a snapshot is not the page the operator sees:"
      printf '%s' "$FID" | sed 's/^/       /' >&2
    fi
  else
    bad "B1: the snapshot exporter failed (rc=$RC):"
    printf '%s\n' "$OUT" | tail -8 | sed 's/^/       /' >&2
  fi
else
  skip "B1: no Go on PATH — the snapshots cannot be exported here"
fi

# --- C: measure at 390 and 360 -------------------------------------------------
if [ ! -d "$WORK" ] || [ -z "$(ls "$WORK"/*.html 2>/dev/null)" ]; then
  skip "C1: no snapshots to measure"
elif ! command -v node >/dev/null 2>&1; then
  skip "C1: no node on PATH — the DevTools driver cannot run"
else
  C_FAIL=""
  for W in 390 360; do
    OUT="$(node "$DRIVER" "$WORK" "$W" 2>&1)"
    RC=$?
    case "$OUT" in
      *"SKIP: no headless browser"*)
        skip "C1: no headless browser found — the ${W}px measurement cannot run here"
        continue ;;
    esac
    if [ "$RC" -eq 0 ]; then
      ok "C${W}: no horizontal overflow at ${W}px on any of the three rule pages"
    else
      C_FAIL="${C_FAIL}${OUT}"$'\n'
    fi
  done
  if [ -n "$C_FAIL" ]; then
    bad "C1: a rule page overflows a phone viewport:"
    printf '%s' "$C_FAIL" | sed 's/^/       /' >&2
  fi
fi

# --- D: bookkeeping -------------------------------------------------------------
D_MISS=""
git ls-files --error-unmatch "$SNAP" >/dev/null 2>&1 \
  || D_MISS="${D_MISS}$SNAP is NOT tracked by git (AGENTS trap #11)"$'\n'
git ls-files --error-unmatch "$DRIVER" >/dev/null 2>&1 \
  || D_MISS="${D_MISS}$DRIVER is NOT tracked by git"$'\n'
git ls-files --error-unmatch scripts/check_b521_mobile_rules_pages.sh >/dev/null 2>&1 \
  || D_MISS="${D_MISS}this script is NOT tracked by git"$'\n'
grep -q 'check_b521_mobile_rules_pages.sh' scripts/verify_pre_deploy.sh \
  || D_MISS="${D_MISS}verify_pre_deploy.sh does not register B521"$'\n'
grep -q '\*\*B521\*\*' AGENTS.md \
  || D_MISS="${D_MISS}AGENTS.md's block index has no B521 entry (AGENTS rule 2)"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: tracked, registered and indexed"
else
  bad "D1: the block bookkeeping is incomplete:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

printf '\n\033[1mB521 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
