#!/bin/bash
# check_b153.sh — dr_drill.sh (Phase 9 of v1.5.0 HA plan)
#
# B153 (v1.5.0) — live DR drill runbook. The script is
# operator-driven (interactive), so the B-check focuses on
# source-contract:
#
#  A. Source-contract (script exists, is executable, has the
#     5 steps in the right order, supports the --yes +
#     --skip-regapi-check + --skip-kill-both flags)
#  B. Safety contract (NEVER destroys data, always pauses
#     for operator confirmation, NEVER touches the DB)
#  C. Failover timing contract (60s + 90s timeouts match
#     the B145 elector's missed-threshold × heartbeat-interval)
set -euo pipefail

ok()  { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }
skip(){ echo "  SKIP  $1"; }
hdr() { echo; echo "=== $1 ==="; }

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO"

# ---------------------------------------------------------------------------
hdr "contract A: source file exists + has the right structure"

# A.1 — the script exists + is executable + bash.
if [ -x scripts/dr_drill.sh ] && head -1 scripts/dr_drill.sh | grep -qE '^#!/usr/bin/env bash$|^#!/bin/bash$'; then
    ok "scripts/dr_drill.sh exists + is executable + bash shebang"
else
    bad "scripts/dr_drill.sh MISSING or not executable or not bash"
fi

# A.2 — the 5 steps in the right order.
EXPECTED_STEPS=("step 1/5" "step 2/5" "step 3/5" "step 4/5" "step 5/5")
for step in "${EXPECTED_STEPS[@]}"; do
    if grep -qF "$step" scripts/dr_drill.sh; then
        ok "script has $step"
    else
        bad "script missing $step"
    fi
done

# A.3 — the 3 flags (--yes, --skip-regapi-check, --skip-kill-both).
for flag in "yes" "skip-regapi-check" "skip-kill-both"; do
    if grep -qE -- "\\-\\-$flag" scripts/dr_drill.sh; then
        ok "script supports flag --$flag"
    else
        bad "script missing flag --$flag (operator needs it for unattended maintenance windows)"
    fi
done

# A.4 — the script must use the B145 elector's role banner
# (`/readyz` returns `role=active` / `role=standby`). A regression
# that polled a different endpoint would silently miss the
# failover.
if grep -qE '/readyz' scripts/dr_drill.sh; then
    ok "script polls /readyz for the role banner (B145 contract)"
else
    bad "script does NOT poll /readyz (would silently miss the failover)"
fi

# A.5 — the script must call `docker kill -9` (the actual
# failure mode we want to test). A regression that used
# `docker stop` (graceful) would let skygate drain requests
# and miss the real-world failure.
if grep -qE 'docker kill -9' scripts/dr_drill.sh; then
    ok "script uses 'docker kill -9' (the actual failure mode)"
else
    bad "script does NOT use 'docker kill -9' (would test the wrong failure mode)"
fi

# A.6 — the script must check BOTH nodes are on the same
# skygate version before killing anything. A regression would
# start the drill with mismatched versions, fail at the end,
# and leave the operator unsure whether the failover itself
# was broken or whether the version mismatch was the issue.
if grep -qE 'different versions' scripts/dr_drill.sh; then
    ok "script aborts cleanly on version mismatch"
else
    bad "script does NOT abort on version mismatch (drill could fail for the wrong reason)"
fi

# ---------------------------------------------------------------------------
hdr "contract B: safety contract"

# B.1 — the script must NOT use `docker compose down -v`
# (the `-v` flag destroys volumes → data loss). A regression
# would silently destroy the operator's data.
if grep -qE 'docker compose down -v|docker-compose down -v' scripts/dr_drill.sh; then
    bad "script uses 'docker compose down -v' (DESTROYS VOLUMES — DATA LOSS)"
else
    ok "script does NOT use 'docker compose down -v' (safe — no data destruction)"
fi

# B.2 — the script must pause for operator confirmation
# between steps (unless --yes was passed). A regression that
# runs the drill headless would force the operator into an
# unsupervised run.
if grep -qE 'press ENTER to continue' scripts/dr_drill.sh \
   && grep -qE 'ASSUME_YES' scripts/dr_drill.sh; then
    ok "script pauses for operator confirmation (unless --yes)"
else
    bad "script does NOT pause for operator confirmation (would force an unsupervised run)"
fi

# B.3 — the script must NOT write to the database (Patroni
# keeps the replica in sync, and the drill should not interfere).
# A regression would let the script modify the DB during the
# drill and confuse the operator when the chain doesn't stabilize.
if grep -qE 'psql.*-c.*INSERT|skygate.*db.*INSERT' scripts/dr_drill.sh; then
    bad "script writes to the DB (Patroni is the source of truth — drill must NOT write)"
else
    ok "script does NOT write to the DB (Patroni is the source of truth)"
fi

# B.4 — the script must print a "what to do next" hint at
# the end (so the operator knows how to clean up the test
# state + tag the release).
if grep -qE 'what to do next|Next steps' scripts/dr_drill.sh; then
    ok "script prints next-step hints for the operator"
else
    bad "script does NOT print next-step hints (operator would not know what to do after the drill)"
fi

# ---------------------------------------------------------------------------
hdr "contract C: failover timing contract"

# C.1 — the step 2 timeout must be 60s (B145's missed-threshold
# × heartbeat-interval = 3 × 5s = 15s; the operator-facing
# transition takes 30-60s including the reg.ru DNS propagation).
# A regression that set the timeout to 30s would fail the drill
# on a slow reg.ru API.
if grep -qE 'Waiting up to 60s' scripts/dr_drill.sh \
   || grep -qE 'seq 1 60' scripts/dr_drill.sh; then
    ok "step 2 (kill active) timeout = 60s (matches B145 + reg.ru propagation)"
else
    bad "step 2 timeout is not 60s (the B145 failover + reg.ru propagation take 30-60s)"
fi

# C.2 — the step 5 timeout must be 90s (both nodes are dead →
# each one has to come back up, then the elector has to detect
# the chain + elect a new active. Each step takes 15-30s).
if grep -qE 'Waiting up to 90s' scripts/dr_drill.sh \
   || grep -qE 'seq 1 90' scripts/dr_drill.sh; then
    ok "step 5 (kill both) timeout = 90s (both nodes come back + elector re-elects)"
else
    bad "step 5 timeout is not 90s (both nodes coming back takes 30-60s + elector re-elect)"
fi

# ---------------------------------------------------------------------------
hdr "summary"
echo "B153: dr_drill.sh (Phase 9 — live DR drill runbook)"
echo "all contracts satisfied"
