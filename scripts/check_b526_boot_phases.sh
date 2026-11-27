#!/usr/bin/env bash
# check_b526_boot_phases.sh
#
# 2026-11-27 (B526) — the boot sequence must stop being ONE function.
#
# The audit measured main() at 2 272 lines / cc 139. The target is < 400, and the plan
# (docs/ROADMAP.md §8) is a sequence of VERBATIM moves, one phase at a time:
#
#   steps 1-3  the `skygate <command>` dispatch        main() 2 272 -> 1 971
#   steps 4-7  three monitors + bootCtx                main() 1 971 -> 1 871
#   step  8    background services + late wiring       main() 1 871 -> 1 369   (this file)
#
# WHAT THIS CONTRACT PINS
#   A. the SIZE is measured, frozen and falling (the number is in the budget file, and
#      this row prints it — a reader of the gate sees the distance to < 400);
#   B. every extracted phase is DECLARED exactly once and CALLED exactly once across
#      the cmd/skygate surface (a phase nobody calls is dead code, a phase called twice
#      starts a service twice);
#   C. the helper-return trap: neither the background-services block nor the late-wiring
#      block may contain a `defer` — a defer inside a helper stops the resource the
#      moment the helper returns (B372's paying lesson, and the reason step 8 needed a
#      lifecycle at all);
#   D. the lifecycle that makes C possible is REAL: main() defers ONE Stop(), the two
#      stoppers are registered on it, and the reverse-order/once-only behaviour is
#      pinned by Go tests that actually run.
#
# WHERE IT SKIPS: nowhere. Everything here is a source contract.
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B526: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

. scripts/lib/gosurface.sh
gosurface SURFACE cmd/skygate/*.go || {
  printf '  \033[31mFAIL\033[0m B: cannot read the cmd/skygate surface\n' >&2
  printf '\nB526: 0 pass, 1 fail, 0 skip\n'
  exit 1
}
BOOT_SERVICES="$(grep -rl 'func startBackgroundServices(' cmd/skygate/*.go 2>/dev/null | head -1)"
LIFECYCLE="$(grep -rl 'type bootLifecycle struct' cmd/skygate/*.go 2>/dev/null | head -1)"
LIFECYCLE_TEST="$(grep -rl 'func TestBootLifecycle_StopsInReverseOrder' cmd/skygate/*.go 2>/dev/null | head -1)"

hdr "B526 — the boot sequence is a list of phases, not one function"

# --- A: the size is measured, frozen and falling --------------------------------
# B339: the size is read from the WHOLE TREE and filtered to the entrypoint package,
# never from one hardcoded path — a contract that names a file turns the next pure
# code move into a false FAIL, which is exactly what this block does to main().
CUR="$(bash scripts/complexity_scan.sh 2>/dev/null |
  awk -F'\t' '$4 == "main" && $3 ~ /^cmd\/skygate\// {print $1; exit}')"
BUDGET="$(awk -F'\t' '$4 == "main" && $3 ~ /^cmd\/skygate\// {print $1; exit}' scripts/complexity_baseline.txt)"
if [ -z "$CUR" ]; then
  bad "A1: the scanner did not report main() — the size is not measured"
else
  ok "A1: main() is ${CUR} lines (2 272 at the audit, target < 400)"
fi
if [ -n "$CUR" ] && [ -n "$BUDGET" ] && [ "$CUR" -le "$BUDGET" ]; then
  ok "A2: main() is at or below its frozen budget (${CUR} <= ${BUDGET}) — it can only fall"
else
  bad "A2: main() grew past its budget (${CUR:-?} > ${BUDGET:-?})"
fi
if grep -q 'main() .*<400\|<400' docs/ROADMAP.md 2>/dev/null; then
  ok "A3: the target (<400 lines) is stated in the roadmap, so the distance is traceable"
else
  bad "A3: the roadmap no longer states the <400 target"
fi

# --- B: every phase declared once, called once ----------------------------------
B_MISS=""
for fn in startHeadscaleUpdateMonitor startExitNodeMonitor startDBHealthSampler \
          startBackgroundServices wireLateServices wireOptionalSchedulers wireRoutes; do
  decls=$(grep -cE "^func (\([a-zA-Z]+ [a-zA-Z*]+\) )?${fn}\(" "$SURFACE" 2>/dev/null || true)
  # A call may be bare (`fn(...)`) or the right-hand side of an assignment with one OR
  # several targets (`x := fn(...)`, `mux, adminSvc := fn(...)`) — treating the multi
  # form as "not called" is the kind of grep that reports a fact about the check.
  calls=$(grep -cE "^[[:space:]]+([a-zA-Z_][a-zA-Z0-9_]*(, ?[a-zA-Z_][a-zA-Z0-9_]*)* :?= )?([a-zA-Z]+\.)?${fn}\(" "$SURFACE" 2>/dev/null || true)
  [ "${decls:-0}" = "1" ] || B_MISS="${B_MISS}${fn}: declared ${decls:-0} time(s), must be exactly 1"$'\n'
  [ "${calls:-0}" = "1" ] || B_MISS="${B_MISS}${fn}: called ${calls:-0} time(s), must be exactly 1"$'\n'
done
if [ -z "$B_MISS" ]; then
  ok "B: all seven extracted phases are declared once and called once"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "B: $l"; done <<< "$B_MISS"
fi

# --- C: no FUNCTION-SCOPE defer inside the extracted blocks ----------------------
# The rule is `^\tdefer ` — one tab — because that is where a statement sits at function
# scope. A defer inside a CLOSURE (three tabs, e.g. a ticker inside a goroutine) runs
# when the closure exits and is harmless; a defer at function scope would stop the
# resource the moment the helper returns.
ROUTES_FILE="$(grep -rl 'func wireRoutes(' cmd/skygate/*.go 2>/dev/null | head -1)"
if [ -z "$BOOT_SERVICES" ]; then
  bad "C1: the file declaring startBackgroundServices is missing"
else
  if awk '/^func startBackgroundServices\(/,/^}/' "$BOOT_SERVICES" | grep -qE '^\tdefer '; then
    bad "C1: startBackgroundServices holds a function-scope defer — it would stop a service at helper-return"
  else
    ok "C1: startBackgroundServices holds no defer (the lifecycle owns the stoppers)"
  fi
  if awk '/^func wireLateServices\(/,/^}/' "$BOOT_SERVICES" | grep -qE '^\tdefer '; then
    bad "C2: wireLateServices holds a function-scope defer — it would stop a service at helper-return"
  else
    ok "C2: wireLateServices holds no defer"
  fi
fi
if [ -z "$ROUTES_FILE" ]; then
  bad "C3: the file declaring wireRoutes is missing"
elif awk '/^func wireRoutes\(/,/^}/' "$ROUTES_FILE" | grep -qE '^\tdefer '; then
  bad "C3: wireRoutes holds a function-scope defer — the availability checker / DB-health sampler / metrics collector / modules would stop the moment it returns"
else
  ok "C3: wireRoutes holds no defer (its four stoppers are on the lifecycle)"
fi

# --- D: the lifecycle is real and tested -----------------------------------------
D_MISS=""
[ -n "$LIFECYCLE" ] || D_MISS="${D_MISS}type bootLifecycle is missing — the defers could not have moved safely"$'\n'
if [ -n "$LIFECYCLE" ]; then
  grep -q 'func (l \*bootLifecycle) Add(' "$LIFECYCLE" \
    || D_MISS="${D_MISS}bootLifecycle.Add is missing"$'\n'
  grep -q 'func (l \*bootLifecycle) Stop(' "$LIFECYCLE" \
    || D_MISS="${D_MISS}bootLifecycle.Stop is missing"$'\n'
  grep -q 'for i := len(stops) - 1; i >= 0; i--' "$LIFECYCLE" \
    || D_MISS="${D_MISS}Stop does not run the stoppers in REVERSE order (nested defers did)"$'\n'
fi
# Anchored on CODE (`^\t...defer`), not on the bare text: the lifecycle's own doc comment
# quotes `defer lc.Stop()` in its usage example, and a comment must not be able to
# satisfy (or break) a contract about the boot path.
lc_stops=$(grep -cE '^[[:space:]]+defer lc\.Stop\(\)' "$SURFACE" || true)
[ "$lc_stops" = "1" ] \
  || D_MISS="${D_MISS}main() must defer lc.Stop() exactly once (found ${lc_stops:-0} code line(s))"$'\n'
for pair in 'lc.Add("dbmigrate-watchdog", wd.Stop)' 'lc.Add("ha-elector", el.Stop)'; do
  grep -qF "$pair" "$BOOT_SERVICES" 2>/dev/null \
    || D_MISS="${D_MISS}missing registration: ${pair}"$'\n'
done
[ -n "$LIFECYCLE_TEST" ] || D_MISS="${D_MISS}no Go test pins the lifecycle behaviour"$'\n'
if [ -z "$D_MISS" ]; then
  ok "D1: the lifecycle exists, runs stoppers in reverse order, and main() defers one Stop()"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "D1: $l"; done <<< "$D_MISS"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 -run 'BootLifecycle' ./cmd/skygate/ 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    ok "D2: the lifecycle contracts pass (reverse order, once-only, nil-safe, late add)"
  else
    bad "D2: the lifecycle contracts failed: $OUT"
  fi
else
  skip "D2: go not on PATH"
fi

# --- E: bookkeeping ---------------------------------------------------------------
E_MISS=""
git ls-files --error-unmatch "$BOOT_SERVICES" >/dev/null 2>&1 \
  || E_MISS="${E_MISS}${BOOT_SERVICES} is NOT tracked by git (trap 11)"$'\n'
git ls-files --error-unmatch "$LIFECYCLE" >/dev/null 2>&1 \
  || E_MISS="${E_MISS}${LIFECYCLE} is NOT tracked by git (trap 11)"$'\n'
git ls-files --error-unmatch "$LIFECYCLE_TEST" >/dev/null 2>&1 \
  || E_MISS="${E_MISS}${LIFECYCLE_TEST} is NOT tracked by git (trap 11)"$'\n'
grep -q '"B526"' scripts/verify_pre_deploy.sh || E_MISS="${E_MISS}verify_pre_deploy.sh does not register B526"$'\n'
git ls-files --error-unmatch scripts/check_b526_boot_phases.sh >/dev/null 2>&1 \
  || E_MISS="${E_MISS}this script is NOT tracked by git"$'\n'
if [ -z "$E_MISS" ]; then ok "E: tracked, registered and indexed"
else while IFS= read -r l; do [ -n "$l" ] && bad "E: $l"; done <<< "$E_MISS"; fi

printf '\nB526: %d pass, %d fail, %d skip\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
