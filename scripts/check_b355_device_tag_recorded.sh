#!/usr/bin/env bash
# check_b355_device_tag_recorded.sh
#
# 2026-10-06 (B355) — the attribution pass must record the per-device tag in the
# ownership ROW, not only in headscale.
#
# Live (node 150, `s24-fe--ned`, registered through OIDC):
#
#   19:39:04 DBG backfill node=150 name=s24-fe--ned matchedTag=tag:private api_tags=[] hasPrivate=false
#   19:39:05 DBG backfill AddTag called for node=150 (ensure tag:private)
#   19:42:56 [devices] s24-fe--ned has no device-to-device ACL entry:
#            no per-device tag is recorded for it, so the mesh has nothing to grant
#
# headscale carried `tag:dev-skyadmin-s24-fe--ned`; the row kept the strategy's scope
# tag, and the row is what the mesh/rules/page read. The device sat in «ожидание» until
# the operator transferred it by hand.
#
# CONTRACTS
#   A. the row is written with the per-device tag after a successful apply
#   B. the write is scoped to the portal user being attributed
#   C. the tests start from the live shape and pin the ROW
#   D. this check cannot be dropped silently
#
# SKIPs when the Go toolchain is unavailable (never FAIL).

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || { printf 'B355: cannot locate cmd/skygate/main.go\n' >&2; exit 2; }

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

NO=internal/nodeownership/nodeownership.go
MAP=internal/db/node_owner_map.go
TEST=internal/nodeownership/nodeownership_b355_test.go

hdr "B355 — the ownership row records the per-device tag"

if grep -q 'UpdateNodeOwnerTagForUser(db.Current(), n.ID, portalUsername, devTag, portalUserID)' "$NO"; then
  ok "A1: the attribution pass writes the per-device tag into the row (scoped to this user)"
else
  bad "A1: the row is still left holding the strategy's scope tag"
fi
if grep -q 'devTag := fmt.Sprintf("tag:dev-%s-%s", portalUsername, strings.ToLower(n.Hostname))' "$NO"; then
  ok "A2: the recorded tag is the same value that was applied to headscale"
else
  bad "A2: the row and headscale can disagree again"
fi
if grep -q 'func UpdateNodeOwnerTagForUser(' "$MAP" && grep -q 'WHERE node_id = \$3 AND username = \$4' "$MAP"; then
  ok "B1: the guarded helper only updates a row that belongs to the attributed user"
else
  bad "B1: a background pass can overwrite another user's ownership row"
fi
if grep -q 'func UpdateNodeOwnerTag(' "$MAP"; then
  ok "B2: the owner-agnostic variant stays for the admin tag action"
else
  bad "B2: the admin tag action lost its helper"
fi
if grep -q 'NowUnixSQL()' "$MAP"; then
  ok "B3: 'now' comes from the dialect-aware helper, not a hardcoded strftime"
else
  bad "B3: the helper hardcodes a SQLite-ism"
fi
if [ -f "$TEST" ] && grep -q 's24-fe--ned' "$TEST" && grep -q 'no per-device tag is recorded' "$TEST"; then
  ok "C1: the regression test starts from the live shape and quotes the live symptom"
else
  bad "C1: the regression test does not reproduce the reported case"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/nodeownership/ -run 'TestB355' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "C2: the row-tag tests pass"
  else
    bad "C2: B355 tests failed: $(tail -3 <<< "$OUT" | tr '\n' ' ')"
  fi
  OUT2="$(go test ./internal/db/ -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT2"; then
    ok "C3: internal/db stays green (the new helper + the dialect-safe now)"
  else
    bad "C3: internal/db failed: $(tail -3 <<< "$OUT2" | tr '\n' ' ')"
  fi
else
  skip "C2-C3: no Go toolchain — the B355 test contracts were not run"
fi

if git ls-files --error-unmatch scripts/check_b355_device_tag_recorded.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git"
else
  bad "D1: scripts/check_b355_device_tag_recorded.sh is NOT tracked by git"
fi
if grep -q 'check_b355_device_tag_recorded.sh' scripts/verify_pre_deploy.sh; then
  ok "D2: verify_pre_deploy.sh registers B355"
else
  bad "D2: the gate does not run this contract"
fi
if grep -q '^- \*\*B355\*\*' AGENTS.md; then
  ok "D3: AGENTS.md's block index carries B355"
else
  bad "D3: the block index does not know B355"
fi

hdr "B355 summary"
printf 'PASS=%d FAIL=%d SKIP=%d\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
