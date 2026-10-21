#!/usr/bin/env bash
# check_b405_not_configured.sh
#
# 2026-10-21 (B405) — «НЕ НАСТРОЕНО» — ЭТО НЕ «СЛОМАНО».
#
# WHY (measured in the catalogue)
#   /admin/system_tests answered a configuration question with a failure verdict:
#
#     integrations.configured :: FAIL "no integration keys configured
#                                      (DERP / headplane / telegram all empty)"
#     backup.recent           :: FAIL "backup dir not configured (set SKYGATE_BACKUP_DIR …)"
#
#   An installation that uses neither Telegram nor Headplane nor S3 backups is a
#   CORRECT installation. A red row for it is not a harmless cosmetic problem: the
#   catalogue's whole value is that a red row means "this is broken", and a red row
#   that means "I never turned this on" teaches the operator to ignore red rows.
#
#   A diagnostic row about an OPTIONAL subsystem has three answers, not two:
#     PASS — configured, and it works
#     FAIL — configured, and it is broken
#     SKIP — never configured: nothing was verified, and nothing is wrong
#
# CONTRACTS
#   A. one constructor for the third verdict, used by the optional-feature tests
#   B. FAIL for "not configured" survives ONLY for the dependencies without which
#      the process cannot work at all (its database, its headscale client) — every
#      other site is a block regression
#   C. the new optional-feature diagnostic exists, is reachable, and does not
#      pretend a configured-but-broken provider is fine
#   D. tests, registration, documentation
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/feature/admin/system_tests_notconfigured_b405.go ] || {
  printf 'B405: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b405.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

B405=internal/feature/admin/system_tests_notconfigured_b405.go
T_B405=internal/feature/admin/system_tests_notconfigured_b405_test.go
ST=internal/feature/admin/system_tests.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

B405_SRC="$(src "$B405")"; ST_SRC="$(src "$ST")"; TB405_SRC="$(src "$T_B405")"

hdr "B405 — «not configured» is not «broken»"

# ---------------------------------------------------------------------------
# A. one constructor for the third verdict
# ---------------------------------------------------------------------------
hdr "A — the third answer exists, and it names the fix"

if has 'func systemTestNotConfigured(' "$B405_SRC"; then
  ok "A1: systemTestNotConfigured is the single constructor for the third verdict"
else
  bad "A1: the third verdict has no constructor — each site would re-invent it"
fi
if has 'return SystemTestSkip, msg' "$B405_SRC"; then
  ok "A2: it returns SKIP (never FAIL) with the reason it was built from"
else
  bad "A2: the constructor can return something other than SKIP"
fi
if has 'notConfiguredMarker' "$B405_SRC"; then
  ok "A3: the phrase is a marker the page, the log and this contract can agree on"
else
  bad "A3: no marker — a grep cannot tell a configuration skip from a real skip"
fi
if has 'to enable it: ' "$B405_SRC"; then
  ok "A4: the skip says what to do (a bare 'not configured' leaves the operator stuck)"
else
  bad "A4: the skip names the state but not the fix"
fi

# ---------------------------------------------------------------------------
# B. FAIL for "not configured" is reserved for CORE dependencies
# ---------------------------------------------------------------------------
hdr "B — only a dependency without which nothing works may fail"

if has 'systemTestNotConfigured(' "$ST_SRC"; then
  ok "B1: the legacy catalogue uses the constructor (it no longer invents a FAIL)"
else
  bad "B1: the legacy catalogue still answers 'not configured' on its own"
fi
if has 'the optional integrations (DERP config / headplane / Telegram)' "$ST_SRC"; then
  ok "B2: integrations.configured names the integrations it did not find"
else
  bad "B2: integrations.configured lost its named reason"
fi
if has 'the in-app backup destination' "$ST_SRC"; then
  ok "B3: backup.recent names the destination it did not find"
else
  bad "B3: backup.recent lost its named reason"
fi
if has 'nothing is wrong' "$B405_SRC"; then
  ok "B4: the message states the verdict in words, not only by colour"
else
  bad "B4: the skip does not say that nothing is wrong"
fi

# Every remaining `SystemTestFail, "… not configured"` must be a CORE dependency.
SITES="$SKY_TMP/fail_notconfigured.txt"
: > "$SITES"
while IFS= read -r line; do
  case "$line" in
    *'SystemTestFail, "DB not configured"'*)            : ;;
    *'SystemTestFail, "headscale client not configured"'*) : ;;
    *'SystemTestFail, "service not initialised"'*)      : ;;
    *'SystemTestFail, "DB not configured'*)             : ;;
    *'SystemTestFail, "headscale client not configured'*) : ;;
    *) printf '%s\n' "$line" >> "$SITES" ;;
  esac
done < <(grep -n 'SystemTestFail, ".*not configured' internal/feature/admin/system_tests*.go 2>/dev/null | grep -vE ':[0-9]+:[[:space:]]*(//|\*)')
if [ -s "$SITES" ]; then
  bad "B5: an OPTIONAL feature still reports FAIL for being unconfigured: $(head -3 "$SITES" | tr '\n' ' ')"
else
  ok "B5: every remaining 'not configured' FAIL is a core dependency (DB / headscale client)"
fi
N_CORE=$(grep -c 'SystemTestFail, "\(DB not configured\|headscale client not configured\)' internal/feature/admin/system_tests*.go 2>/dev/null | grep -v ':0$' | wc -l)
if [ "${N_CORE:-0}" -ge 1 ]; then
  ok "B6: the core-dependency FAIL path is still there (${N_CORE} file(s)) — the split is a rule, not a blanket downgrade"
else
  bad "B6: no core 'not configured' FAIL remains — a missing DB now looks like a skip"
fi

# ---------------------------------------------------------------------------
# C. the new optional-feature diagnostic
# ---------------------------------------------------------------------------
hdr "C — a configured-but-broken provider is still a FAIL"

if has '"integrations.oidc"' "$B405_SRC"; then
  ok "C1: integrations.oidc exists (the catalogue had no OIDC row at all)"
else
  bad "C1: integrations.oidc is missing"
fi
if has 'db.GetOIDCSettings(s.dbc())' "$B405_SRC"; then
  ok "C2: it reads the settings the operator actually saved"
else
  bad "C2: it does not read oidc_settings"
fi
if has 'ErrOIDCSettingsNotFound' "$B405_SRC"; then
  ok "C3: no row is a configuration fact (SKIP), not a failure"
else
  bad "C3: an absent row is treated as a failure again"
fi
if has 'OIDC is ENABLED but incomplete' "$B405_SRC"; then
  ok "C4: enabled-but-incomplete is a FAIL, and it names the missing pieces"
else
  bad "C4: a broken OIDC setup would pass silently"
fi
for f in client_id redirect_uris key_dir; do
  if has "$f" "$B405_SRC"; then ok "C5: the complete case checks $f"; else bad "C5: $f is not checked"; fi
done
if grep -q 'TestRegistry = append(TestRegistry, b405Tests...)' "$B405"; then
  ok "C6: the batch is registered in TestRegistry (an unreachable diagnostic is not a fix)"
else
  bad "C6: b405Tests is never appended to TestRegistry"
fi

# ---------------------------------------------------------------------------
# D. tests, registration, documentation
# ---------------------------------------------------------------------------
hdr "D — pinned, registered, documented"

for n in TestB405_NotConfiguredIsASkipThatNamesTheFix TestB405_NotConfiguredWithoutAFixStillSkips \
         TestB405_OIDCWithoutARowSkipsNotFails TestB405_OIDCEnabledButIncompleteFails \
         TestB405_OIDCCompletePasses TestB405_OIDCDisabledRowSkips; do
  if has "func $n(" "$TB405_SRC"; then ok "D1: $n pins a case"; else bad "D1: $n is missing"; fi
done
if ! has 'db.OpenWithDialect(":memory:")' "$TB405_SRC"; then
  bad "D2: the tests do not use a real database — a mock cannot prove the SQL path"
else
  ok "D2: the tests run against a real in-memory SQLite DB"
fi
if git ls-files --error-unmatch scripts/check_b405_not_configured.sh >/dev/null 2>&1; then
  ok "D3: this script is tracked by git (trap #11)"
else
  bad "D3: this script is not tracked"
fi
if grep -q 'check_b405_not_configured' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "D4: registered in scripts/verify_pre_deploy.sh"
else
  bad "D4: not registered in the catalog"
fi
if grep -q '^\- \*\*B405\*\*' AGENTS.md 2>/dev/null; then
  ok "D5: one-line entry in the AGENTS.md block index"
else
  bad "D5: no B405 entry in the AGENTS block index"
fi
if grep -q 'B405' docs/ROADMAP.md 2>/dev/null && grep -q 'B405' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "D6: both roadmaps record the block (rule 12)"
else
  bad "D6: a roadmap does not record the block"
fi

printf '\n\033[1mB405: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
