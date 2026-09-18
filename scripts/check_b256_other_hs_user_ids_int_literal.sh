#!/usr/bin/env bash
# check_b254_other_hs_user_ids_int_literal.sh
#
# 2026-09-15 (B256) — regression check for the live PG error
#
#   ERROR: invalid input syntax for type integer: "" (SQLSTATE 22P02)
#
# which fired every ~5 min on the agent VM (192.168.13.69) from
# the AutoBackfill ticker (internal/nodeownership/auto.go) calling
# Backfill per portal user. The pre-fix query compared INTEGER
# headscale_user_id against the literal TEXT ''; PG refused the
# cast. The query text in queries.go was the only place; no DB
# migration needed — the fix is a one-line SQL change.
#
# What this checks:
#   A) queries.go no longer contains `headscale_user_id != ''`
#      (the buggy text-literal compare against an INTEGER column)
#   B) queries.go contains `headscale_user_id != 0` (the new
#      filter that handles both pre-v0.28 NULL and post-v0.28 zero
#      sentinels for the "not yet linked" state)
#   C) qSelectOtherHSUserIDs const is in queries.go (still
#      single-source-of-truth — pin to prevent re-introducing a
#      second copy)
#   D) portal_users.go GetOtherHSUserIDs function is still
#      present (the only Go-side caller still exists)
#   E) portal_users_test.go has TestGetOtherHSUserIDs_B256Regression
#      (the new test that pins the fix on real PG)
#   F) portal_users_test.go comment no longer says "filter the
#      empty string" (sanity check that the stale rationale was
#      replaced, not augmented)
#   G) verify_pre_deploy.sh includes check_b256 in its run_check list
#   H) AGENTS.md mentions B256 with a release-status entry
#   J) `go vet ./internal/db/...` is clean (skipped if `go` is
#      not on bash PATH; the operator can re-run by hand on the
#      VM / GitHub Actions where PATH is normal)
#   K) Live: on 192.168.13.69 skygate-pg-local, pg_stat_activity
#      shows no recent `invalid input syntax for type integer`
#      error in headscale stderr (the B256 error spam symptom)
#
# Run order: A-H as the source-contract gates (run on every commit),
# J if go is reachable, K after the operator deploys and
# AutoBackfill ticks at least once.

set -euo pipefail

ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
fail(){ printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# Git-Bash / MSYS2 subshell doesn't inherit the Windows PATH, so `go`
# isn't on the bash PATH inside scripts/. The Windows-native go.exe
# is at /mnt/c/Program Files/Go/bin/go.exe under git-bash/WSL, OR
# sometimes at /c/Program Files/Go/bin (older git-bash). Probe both
# via a real execution test (not `command -v`, which can return 0 +
# empty on miss). A successful `go version` is the only reliable
# signal that PATH-up is needed.
if ! go version >/dev/null 2>&1; then
  for cand in '/mnt/c/Program Files/Go/bin' '/c/Program Files/Go/bin' '/c/Program Files (x86)/Go/bin'; do
    if [ -f "$cand/go.exe" ] && "$cand/go.exe" version >/dev/null 2>&1; then
      export PATH="$cand:$PATH"
      break
    fi
  done
fi

# Last resort: if go still isn't reachable, the J contract becomes
# a no-op. The A-H contracts still block the regression — J is a
# belt-and-braces confirmation, not the primary pin.
HAS_GO=0
if go version >/dev/null 2>&1; then HAS_GO=1; fi

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

QUERIES=internal/db/queries.go
PORTAL_USERS=internal/db/portal_users_test.go
PORTAL_USERS_SRC=internal/db/portal_users.go
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md

hdr "B256 — GetOtherHSUserIDs SQLSTATE 22P02 regression"

# --- A: the buggy `!= ''` filter is gone from the actual SQL constant ---
#     (the comment block may still reference the old SQL for context,
#     but the const assignment line itself MUST not contain `!= ''`).
if grep -A1 "qSelectOtherHSUserIDs = \`" "$QUERIES" 2>/dev/null \
     | grep -nF "headscale_user_id != ''" >/dev/null 2>&1; then
  fail "A: qSelectOtherHSUserIDs const still has '!= ''' — PG will keep raising SQLSTATE 22P02"
else
  ok "A: qSelectOtherHSUserIDs const no longer contains '!= ''' (the INTEGER-vs-TEXT cast bug)"
fi

# --- B: the new `!= 0` filter is in the const ---
if grep -A1 "qSelectOtherHSUserIDs = \`" "$QUERIES" 2>/dev/null \
     | grep -nF "headscale_user_id != 0" >/dev/null 2>&1; then
  ok "B: qSelectOtherHSUserIDs const contains '!= 0' (post-v0.28 zero sentinel filter)"
else
  fail "B: qSelectOtherHSUserIDs const missing '!= 0' — pre-v0.28 NULL users also need filtering"
fi

# --- C: qSelectOtherHSUserIDs const is in queries.go ---
if grep -nF "qSelectOtherHSUserIDs" "$QUERIES" >/dev/null 2>&1; then
  ok "C: qSelectOtherHSUserIDs const still defined in queries.go (single source of truth)"
else
  fail "C: qSelectOtherHSUserIDs const missing from queries.go — single-source guarantee broken"
fi

# --- D: GetOtherHSUserIDs function still present ---
if grep -nF "func GetOtherHSUserIDs" "$PORTAL_USERS_SRC" >/dev/null 2>&1; then
  ok "D: portal_users.go GetOtherHSUserIDs function still present (no caller regression)"
else
  fail "D: GetOtherHSUserIDs function removed — nodeownership.Backfill calls it on every 5-min tick"
fi

# --- E: TestGetOtherHSUserIDs_B256Regression is in portal_users_test.go ---
if grep -nF "TestGetOtherHSUserIDs_B256Regression" "$PORTAL_USERS" >/dev/null 2>&1; then
  ok "E: portal_users_test.go has TestGetOtherHSUserIDs_B256Regression (real-PG regression pin)"
else
  fail "E: B256 regression test missing — the SQLSTATE 22P02 fix has no test lock-in"
fi

# --- F: stale "filter the empty string" rationale is replaced ---
if ! grep -nF "filter the empty string" "$PORTAL_USERS" >/dev/null 2>&1; then
  ok "F: portal_users_test.go stale 'filter the empty string' comment replaced"
else
  fail "F: stale 'filter the empty string' comment still present — the rationale is wrong now"
fi

# --- G: verify_pre_deploy.sh includes check_b256 ---
if grep -nE "check_b256" "$VERIFY" >/dev/null 2>&1; then
  ok "G: verify_pre_deploy.sh references check_b256 in run_check"
else
  fail "G: verify_pre_deploy.sh does not run check_b256 — pre-deploy gate is silent on this regression"
fi

# --- H: AGENTS.md mentions B256 ---
if grep -nE "B256" "$AGENTS" >/dev/null 2>&1; then
  ok "H: AGENTS.md mentions B256 in release-status notes"
else
  fail "H: AGENTS.md missing B256 entry — release-status gap"
fi

# --- J: go vet clean (final gate before commit) ---
#     Skipped if `go` isn't reachable from this bash (PATH probe in
#     header failed). On Linux/WSL/CI everything works; on dev
#     machines with weird PATH exports the operator can re-run J
#     by hand: `go vet ./internal/db/...`
if [ "$HAS_GO" = "0" ]; then
  ok "J: go vet skipped (go not reachable in this bash PATH — re-run manually: go vet ./internal/db/...)"
elif go vet ./internal/db/... >/dev/null 2>&1; then
  ok "J: go vet ./internal/db/... clean"
else
  fail "J: go vet ./internal/db/... reported issues — run 'go vet ./internal/db/...' for detail"
fi

# --- K: live-state prompt (operator-side gate after deploy) ---
#    This check is intentionally a printf + dry-run. The actual
#    ssh + grep is up to the operator once they've deployed the
#    B256 build + AutoBackfill has ticked at least once. The
#    command they need is in the inline note.
hdr "K: live-state (operator-side, post-deploy)"
cat <<'NOTE'
  Confirm on agent VM (192.168.13.69) that the AutoBackfill
  ticker has stopped spamming the headscale PG log with
  SQLSTATE 22P02. Run after deploy + at least one 5-min tick:

    ssh hermes-debug@192.168.13.69 \
      'docker exec skygate-pg-local \
         psql -U skygate -d skygate -c "\
           SELECT count(*) FROM pg_stat_activity \
             WHERE state = ''active'' \
               AND query LIKE ''%headscale_user_id%'';" \
       && journalctl -u headscale --since "5 minutes ago" \
            | grep -E "SQLSTATE 22P02|invalid input syntax for type integer" \
            | wc -l'

  Pre-fix count: ≥1 SQLSTATE 22P02 per 5-min tick
  Post-fix count: 0
NOTE

ok "K: live-state note printed (run manually after deploy)"

printf '\n\033[32mB256 regression check passed — safe to commit\033[0m\n'
