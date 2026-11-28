#!/usr/bin/env bash
# check_b528_route_registries.sh
#
# 2026-11-27 (B528) — a 1 000-line route table is read by nobody.
#
# The audit found `registerRoutes()` at 1 109 lines / 274 `mux.Handle` calls, and the
# reason is not laziness: every closure captures the same locals (`app`, `adminSvc`,
# `mySvc`, `authMW`, …), so splitting it is a COUPLING question, not a mechanical one.
# The plan (docs/ROADMAP.md §8) therefore moves ONE FEATURE GROUP at a time and lets the
# compiler derive the parameter list — every group so far needed exactly
# (mux, authMW, <its service>).
#
# WHAT THIS CONTRACT PINS
#   A. the size is measured, printed and frozen (B525 budget, only falling);
#   B. each registry is DECLARED once and CALLED once, and the call sits inside
#      registerRoutes (a registry nobody calls is dead routes; a registry called twice
#      registers the same handler twice);
#   C. NO ROUTE IS LOST: the total `mux.Handle` count across the entrypoint may not fall
#      below the frozen floor, and each moved family still carries the number of
#      registrations it had before the move (the move is verbatim, so the count is the
#      cheapest possible proof);
#   D. bookkeeping: the files are tracked by git, the block is in the catalog and in the
#      AGENTS index.
#
# WHERE IT SKIPS: nowhere — everything here is a source contract.
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B528: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

hdr "B528 — the route table is a set of feature registries"

# --- A: the size is measured, printed and frozen ---------------------------------
CUR="$(bash scripts/complexity_scan.sh --files cmd/skygate/routes.go 2>/dev/null |
  awk -F'\t' '$3 == "cmd/skygate/routes.go" && $4 == "registerRoutes" {print $1}')"
BUDGET="$(awk -F'\t' '$3 == "cmd/skygate/routes.go" && $4 == "registerRoutes" {print $1}' scripts/complexity_baseline.txt)"
if [ -z "$CUR" ]; then
  bad "A1: the scanner did not report registerRoutes() — the size is not measured"
else
  ok "A1: registerRoutes() is ${CUR} lines (1 109 at the audit; budget ${BUDGET:-?})"
fi
if [ -n "$CUR" ] && [ -n "$BUDGET" ] && [ "$CUR" -le "$BUDGET" ]; then
  ok "A2: registerRoutes() is at or below its frozen budget (${CUR} <= ${BUDGET}) — it can only fall"
else
  bad "A2: registerRoutes() grew past its budget (${CUR:-?} > ${BUDGET:-?})"
fi

# --- B: every registry declared once, called once, from registerRoutes -----------
B_MISS=""
for reg in registerHeadscaleRoutes registerAdminUpdateRoutes registerAdminExitNodesRoutes; do
  decls=$(grep -cE "^func ${reg}\(" cmd/skygate/*.go 2>/dev/null | awk -F: '{s+=$2} END{print s+0}')
  calls=$(grep -cE "^[[:space:]]+${reg}\(" cmd/skygate/*.go 2>/dev/null | awk -F: '{s+=$2} END{print s+0}')
  [ "$decls" = "1" ] || B_MISS="${B_MISS}${reg}: declared ${decls} time(s), must be exactly 1"$'\n'
  [ "$calls" = "1" ] || B_MISS="${B_MISS}${reg}: called ${calls} time(s), must be exactly 1"$'\n'
done
# the calls must live inside registerRoutes (one level down, not in main or a phase)
ROUTES_FILE="$(grep -rl '^func registerRoutes(' cmd/skygate/*.go 2>/dev/null | head -1)"
if [ -n "$ROUTES_FILE" ]; then
  for reg in registerHeadscaleRoutes registerAdminUpdateRoutes registerAdminExitNodesRoutes; do
    grep -qE "^[[:space:]]+${reg}\(" "$ROUTES_FILE" \
      || B_MISS="${B_MISS}${reg} is not called from registerRoutes (${ROUTES_FILE})"$'\n'
  done
else
  B_MISS="${B_MISS}cannot locate the file defining registerRoutes"$'\n'
fi
if [ -z "$B_MISS" ]; then
  ok "B: every registry is declared once, called once, and the call sits inside registerRoutes"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "B: $l"; done <<< "$B_MISS"
fi

# --- C: no route was lost -------------------------------------------------------
# The floor is the count measured immediately before the first split landed (274 calls
# in routes.go at the audit + the 2 framework endpoints main()/routes.go carry), so a
# move that silently drops registrations FAILS here instead of looking tidy.
TOTAL=0
for f in cmd/skygate/*.go; do
  case "$f" in *_test.go) continue ;; esac
  n="$(grep -c 'mux\.Handle(' "$f" 2>/dev/null || true)"
  TOTAL=$((TOTAL + ${n:-0}))
done
FLOOR=270
if [ "$TOTAL" -ge "$FLOOR" ]; then
  ok "C1: ${TOTAL} route registrations across the entrypoint (floor ${FLOOR}) — nothing was lost in the moves"
else
  bad "C1: only ${TOTAL} route registrations across the entrypoint (floor ${FLOOR}) — a move dropped routes"
fi
C_MISS=""
check_family() {
  file="$1"; family="$2"; want="$3"
  got="$(grep -c "mux\.Handle(\"[A-Z]* ${family}" "$file" 2>/dev/null || true)"
  [ "${got:-0}" = "$want" ] || C_MISS="${C_MISS}${file}: ${GOT:-0}${got} ${family} registrations, expected ${want}"$'\n'
}
check_family cmd/skygate/routes_admin_update.go "/admin/update" 11
check_family cmd/skygate/routes_admin_exit_nodes.go "/admin/exit-nodes" 21
check_family cmd/skygate/routes_headscale.go "/admin/headscale" 7
if [ -z "$C_MISS" ]; then
  ok "C2: each moved family still carries exactly the registrations it had (update 11, exit-nodes 21, headscale 7)"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "C2: $l"; done <<< "$C_MISS"
fi

# --- D: bookkeeping -------------------------------------------------------------
D_MISS=""
for f in cmd/skygate/routes_admin_update.go cmd/skygate/routes_admin_exit_nodes.go cmd/skygate/routes_headscale.go; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 \
    || D_MISS="${D_MISS}${f} is NOT tracked by git (trap 11)"$'\n'
done
grep -q '"B528"' scripts/verify_pre_deploy.sh || D_MISS="${D_MISS}verify_pre_deploy.sh does not register B528"$'\n'
git ls-files --error-unmatch scripts/check_b528_route_registries.sh >/dev/null 2>&1 \
  || D_MISS="${D_MISS}this script is NOT tracked by git"$'\n'
grep -q '^- \*\*B528\*\*' AGENTS.md || D_MISS="${D_MISS}AGENTS.md's block index carries no B528 line"$'\n'
if [ -z "$D_MISS" ]; then ok "D: tracked, registered and indexed"
else while IFS= read -r l; do [ -n "$l" ] && bad "D: $l"; done <<< "$D_MISS"; fi

printf '\nB528: %d pass, %d fail, %d skip\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
