#!/usr/bin/env bash
# ============================================================================
# check_b479_live_skip.sh — B479
#
# «ГЕЙТ КРАСНЫЙ НА ЗДОРОВОМ ДЕРЕВЕ» — live-state checks that answer a question
# about a tailnet nobody is serving on this host.
#
# Measured 2026-11-15 while releasing v1.5.222: the full gate on the retired host
# (its skygate and headscale containers stopped deliberately by the migration, the
# docker daemon still reachable, the old derper container still up) ended with
#
#   GATE FAILED: 7 check(s) failed
#   failing: B243-duplicate-users B243-reconcile-audit B243-node-owner-map-orphans
#            B-mod-tag-owners-coverage B188.2 B188.3
#
# — six live-state contracts that fail identically on the BASE commit. They are
# not product regressions: `docker exec <stopped container>` is an environment
# answer to a live-state question, and AGENTS rule 1 says such a check must report
# SKIP, never FAIL. `lib/skip_if_no_docker.sh` already covered "no docker daemon",
# which is a DIFFERENT question: docker was perfectly reachable.
#
# Contracts:
#   A. the shared guard exists, is tracked, and skips (never fails) with no stack
#   B. its escape hatch lets the check run (the guard cannot hide a real failure)
#   C. all six live-state checks source it BEFORE touching docker/psql/sqlite
#   D. registered in the gate and in the block index
#   E. documented for the operator (EN + RU, both catalogues)
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b479.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

LIB=scripts/lib/skip_if_no_live_stack.sh
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
DOC=docs/internals.md
RUDOC=docs/ru/gate-live-skip.md
DOCIDX=docs/README.md
RUDOCIDX=docs/ru/README.md
LIVE_CHECKS=(
  scripts/check_b_duplicate_users.sh
  scripts/check_b_reconcile_audit_writes.sh
  scripts/check_b_node_owner_map_orphans.sh
  scripts/check_b_tag_owners.sh
  scripts/check_b188_2.sh
  scripts/check_b188_3.sh
)

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$LIB" "$VERIFY" "$AGENTS" "$DOC" "$RUDOC" "$DOCIDX" "$RUDOCIDX" "${LIVE_CHECKS[@]}"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the guard itself -----------------------------------------------------
if git ls-files --error-unmatch "$LIB" >/dev/null 2>&1; then
  ok "A1: the shared guard is tracked by git (AGENTS trap #11)"
else
  bad "A1: the guard is untracked — the pushed tag would not contain it"
fi

# Behavioural: with no docker and no curl on PATH the guard must SKIP and exit 0.
# A scratch PATH holding only the tools the guard itself uses (bash builtins do the
# rest) keeps this honest without mocking the real environment.
# B488 (TD-37) renegotiates these two cases: the guard now asks lib/live_db.sh, so "no live stack"
# also means "no native install" — on the PRODUCTION host /etc/skygate/skygate.env exists and the
# guard must NOT skip there. The override therefore pins the env file at a path that cannot exist,
# which is what this contract has always meant by "no stack on this host".
NOSTACK_ENV="$SKY_TMP/no-such.env"
OUT="$(env PATH="$SKY_TMP/bin" SKYGATE_LIVE_ENV_FILE="$NOSTACK_ENV" /bin/bash -c '. ./scripts/lib/skip_if_no_live_stack.sh' 2>&1)"; rc=$?
if [ "$rc" -eq 0 ] && case "$OUT" in SKIP:*) true;; *) false;; esac; then
  ok "A2: with no live stack the guard prints SKIP and exits 0"
else
  bad "A2: the guard did not SKIP (rc=$rc, output=$(printf '%s' "$OUT" | head -1))"
fi

# --- B: the escape hatch -----------------------------------------------------
OUT="$(env PATH="$SKY_TMP/bin" SKYGATE_LIVE_ENV_FILE="$NOSTACK_ENV" SKYGATE_SKIP_LIVE_GATE=1 /bin/bash -c '. ./scripts/lib/skip_if_no_live_stack.sh'; echo "rc=$?")"; rc=$?
if case "$OUT" in SKIP:*) false;; *) true;; esac && case "$OUT" in *"rc=0"*) true;; *) false;; esac; then
  ok "B1: SKYGATE_SKIP_LIVE_GATE=1 lets the check run (the guard cannot hide a finding)"
else
  bad "B1: the escape hatch does not work: $(printf '%s' "$OUT" | tr '\n' ' ')"
fi

# --- C: every live-state check uses it, and uses it FIRST --------------------
for f in "${LIVE_CHECKS[@]}"; do
  base="$(basename "$f")"
  if has "$f" 'lib/skip_if_no_live_stack.sh'; then
    ok "C1: $base sources the guard"
  else
    bad "C1: $base does not source the live-stack guard"
    continue
  fi
  gl="$(grep -n 'skip_if_no_live_stack.sh' "$f" | head -1 | cut -d: -f1)"
  # The first line that actually TOUCHES the live stack: docker/psql/sqlite3 calls
  # or the DB credential helper. The guard must come before it.
  touch="$(grep -nE 'docker (exec|inspect|ps|restart)[^#]*|psql[^#]*|sqlite3[^#]*|skygate_db_password\)' "$f" | grep -v ':[[:space:]]*#' | head -1 | cut -d: -f1)"
  if [ -n "$gl" ] && { [ -z "$touch" ] || [ "$gl" -lt "$touch" ]; }; then
    ok "C2: $base guards before the first live call (guard=${gl} first-touch=${touch:-none})"
  else
    bad "C2: $base touches the live stack before the guard (guard=${gl:-?} touch=${touch:-?})"
  fi
done

# --- D: registration ---------------------------------------------------------
if has "$VERIFY" '"B479"'; then
  ok "D1: the contract is registered in the gate"
else
  bad "D1: an unregistered check never runs"
fi
if has "$AGENTS" 'B479'; then
  ok "D2: the block index carries the one-line entry"
else
  bad "D2: the block is not in the index"
fi

# --- E: documentation --------------------------------------------------------
if has "$DOC" 'B479' && has "$LIB" 'SKIP'; then
  ok "E1: the EN internals guide explains when the live checks skip"
else
  bad "E1: the EN guide does not cover the SKIP rule"
fi
if has "$RUDOC" 'SKIP' && has "$RUDOC" 'B479'; then
  ok "E2: the RU doc explains the rule for the operator"
else
  bad "E2: the RU doc is missing"
fi
if has "$DOCIDX" 'gate-live-skip.md' && has "$RUDOCIDX" 'gate-live-skip.md'; then
  ok "E3: both catalogues index the RU doc"
else
  bad "E3: the RU doc is not indexed in both catalogues"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB479: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB479: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
