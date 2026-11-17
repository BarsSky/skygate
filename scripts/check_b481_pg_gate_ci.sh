#!/usr/bin/env bash
# ============================================================================
# check_b481_pg_gate_ci.sh — B481
#
# THE GATE MUST BE ABLE TO RUN ON THE DIALECT THE INSTALL USES — WITHOUT A LOCAL PG.
#
# Two measured facts after the 2026-11-15/16 migration:
#   1. The reference VM that used to run a PostgreSQL-enabled gate is RETIRED (the host
#      the migration moved off is unreachable), and the operator's instruction is
#      explicit: PostgreSQL must NOT be installed on the production panel host
#      `svyatoslava` — it is too heavy for that machine. So "run the PG gate on the VM"
#      is not available any more, and the only remaining place with a PostgreSQL server
#      is CI.
#   2. `verify_pre_deploy.sh` never sets SKYGATE_TEST_PG_DSN, so the catalog job in CI
#      exercises SQLite ONLY: ten packages gate real PG tests on that variable, and
#      their PostgreSQL half ran in a separate `go test` job that never runs the
#      catalog's own contracts. TD-24's remaining half was exactly this.
#
# The fix is a main-only CI job that runs the WHOLE catalog through the sanctioned
# runner against the service's scratch database. This contract pins the parts that make
# that job meaningful rather than decorative.
#
# Contracts:
#   A. a CI job runs the catalog through scripts/gate_with_pg.sh with the DSN set, on a
#      postgres service container, and is scoped to main pushes (PR latency stays)
#   B. the job FAILS when the catalog's own banner does not say coverage is enabled, so a
#      run that silently fell back to SQLite cannot look green
#   C. the runner it calls still refuses a non-scratch database before connecting
#   D. the PG constraint on the production host is written down (the operator's rule)
#   E. the debts this session found are recorded in the roadmap
#   F. registered in the gate and in the block index
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b481.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

CI=.github/workflows/ci.yml
RUNNER=scripts/gate_with_pg.sh
DOC=docs/operations.md
ROADMAP=docs/ROADMAP.md
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$CI" "$RUNNER" "$DOC" "$ROADMAP" "$VERIFY" "$AGENTS"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# The job block, isolated so a mention in a comment cannot satisfy the contract.
JOB="$SKY_TMP/ci-job.txt"
mkdir -p "$(dirname "$JOB")"
awk '/^  verify-pre-pg:/{f=1} f && /^  [a-z0-9-]+:$/ && !/^  verify-pre-pg:/{exit} f{print}' "$CI" > "$JOB"

if [ -s "$JOB" ]; then
  ok "A1: a dedicated verify-pre-pg job exists"
else
  bad "A1: there is no verify-pre-pg job — the catalog still runs on SQLite only"
fi
if has "$JOB" 'gate_with_pg.sh'; then
  ok "A2: the job runs the catalog through the sanctioned PG runner"
else
  bad "A2: the job does not use scripts/gate_with_pg.sh (it would bypass the scratch-DB guard)"
fi
if has "$JOB" 'SKYGATE_TEST_PG_DSN:'; then
  ok "A3: the job exports a DSN, so the catalog's PG-gated checks really run"
else
  bad "A3: the job exports no DSN — the banner would read 'SQLite only' and the run would prove nothing"
fi
if has "$JOB" 'postgres:' && has "$JOB" 'POSTGRES_DB:'; then
  ok "A4: the job carries its own PostgreSQL service container"
else
  bad "A4: the job has no PostgreSQL service"
fi
if has "$JOB" "if: github.event_name == 'push'"; then
  ok "A5: the PG catalog runs on main pushes only (PR latency is not doubled)"
else
  bad "A5: the PG catalog is not scoped, so every PR pays for a second full catalog"
fi

# --- B: a silent fallback to SQLite must not look green ---------------------
if has "$JOB" 'PostgreSQL' && has "$JOB" 'coverage: enabled'; then
  ok "B1: the job fails when the catalog did not report PostgreSQL coverage as enabled"
else
  bad "B1: a job that silently fell back to SQLite would still pass"
fi
if has "$JOB" 'FAIL_RE='; then
  ok "B2: the job keeps the catalog's own FAIL/TIMEOUT detection"
else
  bad "B2: the job does not fail on catalog FAILs"
fi

# --- C: the guard in the runner -------------------------------------------
if has "$RUNNER" 'skygate_staging' && has "$RUNNER" '*test*|*Test*|*probe*|*scratch*|*gate*'; then
  ok "C1: the runner still refuses a non-scratch database before connecting"
else
  bad "C1: the runner's scratch-DB guard was weakened"
fi

# --- D: the operator's constraint is written down -------------------------
if has "$DOC" 'svyatoslava' || has "$DOC" 'production host'; then
  if has "$DOC" 'must NOT' || has "$DOC" 'нельзя' || has "$DOC" 'too heavy'; then
    ok "D1: the docs record that PostgreSQL must not be installed on the production panel host"
  else
    bad "D1: the docs mention the host but not the constraint"
  fi
else
  bad "D1: the PG-on-the-production-host constraint is not documented"
fi
if has "$DOC" 'gate_with_pg.sh'; then
  ok "D2: the docs point at the PG catalog runner"
else
  bad "D2: the runner is undocumented"
fi

# --- E: the session's debts are in the roadmap ----------------------------
for td in TD-36 TD-37 TD-38; do
  if has "$ROADMAP" "$td"; then ok "E1: $td is registered"; else bad "E1: $td is missing from the register"; fi
done

# --- F: registration ------------------------------------------------------
if has "$VERIFY" '"B481"'; then ok "F1: the contract is registered in the gate"; else bad "F1: unregistered"; fi
if has "$AGENTS" 'B481'; then ok "F2: the block index carries the entry"; else bad "F2: not in the index"; fi

echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB481: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB481: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
