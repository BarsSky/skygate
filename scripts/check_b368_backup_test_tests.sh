#!/usr/bin/env bash
# check_b368_backup_test_tests.sh — B368 (2026-10-09): the S3 "Test" button must
# test something.
#
# THE MEASUREMENT
# ---------------
# The reference deployment's in-app S3 backup had been failing for weeks:
#
#   backup.last_error: s3 upload: s3 bucket check: Head "http://172.18.0.5:9000/
#                      skygate-backups/": dial tcp 172.18.0.5:9000: connect:
#                      connection refused
#
# while the panel's «Test» answered
#
#   S3 доступен: http://172.18.0.5:9000 · корзина: skygate-backups · регион: us-east-1
#
# The S3 branch of `TestConnection` validated only that the fields were non-empty
# and echoed the endpoint back, and its comment claimed the probe "happens in
# uploadToS3 at run time" — true and useless: a green tick that cannot go red is
# how a MinIO that had MOVED TO ANOTHER HOST stayed unnoticed. Measured directly
# while diagnosing it: the button reported OK for the dead address, and a real
# AWS-SigV4 probe from the same host proved the stored credentials are rejected by
# the MinIO that IS running.
#
# WHAT THIS SCRIPT PINS
#   A. the S3 branch performs a REAL probe (BucketExists) bounded by a deadline,
#      and the "we don't do a real HEAD here" rationale is gone;
#   B. the behavioural regression runs: the same config that used to answer OK on
#      a refusing address now fails, and a half-filled form is still reported as
#      missing fields rather than as a network error;
#   C. the page's success message still has its RU+EN keys (it is only reached on
#      a real success now);
#   D. tracked by git (AGENTS trap #11), registered, indexed.
#
# SKIP (never FAIL) anything that needs live state or a tool this host lacks.
#
# Usage:  bash scripts/check_b368_backup_test_tests.sh
# Exit:   0 = contracts hold, 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MOUNT=internal/backup/mount.go
TEST=internal/backup/mount_b368_test.go
HANDLER=internal/feature/admin/backup_config.go
I18N=internal/i18n/catalog_backup.go
SELF=scripts/check_b368_backup_test_tests.sh

for f in "$MOUNT" "$TEST" "$HANDLER" "$I18N"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B368 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# =====================================================================
hdr "A. the S3 test branch really probes the endpoint"

# The branch's own source region, so a BucketExists elsewhere in the file (the
# upload path has one) cannot satisfy this.
S3_BRANCH="$(awk '/case ProtocolS3:/{p=1} p{print} p && /^	default:/{exit}' "$MOUNT")"
if [ -z "$S3_BRANCH" ]; then
  bad "A1: the ProtocolS3 branch of TestConnection is gone"
elif printf '%s\n' "$S3_BRANCH" | grep -q 'BucketExists'; then
  ok "A1: the S3 branch calls BucketExists — the click now measures the endpoint it names"
else
  bad "A1: the S3 branch validates fields and returns OK without touching the network — the dead-endpoint green tick is back"
fi

if grep -q 's3TestTimeout' "$MOUNT"; then
  ok "A2: the probe is bounded by s3TestTimeout, so a wrong endpoint cannot hang the page"
else
  bad "A2: the probe has no deadline"
fi

if grep -q "We don't do a real HEAD request here" "$MOUNT"; then
  bad "A3: the old rationale is back in the source — it is what allowed a red endpoint to look green"
else
  ok "A3: the 'we don't do a real HEAD here' rationale is gone"
fi

# =====================================================================
hdr "B. the behavioural regression exists and runs"

if grep -q 'func TestB368_S3TestFailsOnAnUnreachableEndpoint' "$TEST" \
   && grep -q 'func TestB368_S3TestStillReportsMissingFieldsFirst' "$TEST"; then
  ok "B1: both regressions are present (dead endpoint fails; missing fields are not masked as transport errors)"
else
  bad "B1: the behavioural regressions are missing"
fi

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
  skip "B2: no go binary in PATH — the behavioural contracts were not run"
else
  if out="$("$GOBIN" test ./internal/backup/ -run 'B368' -count=1 2>&1)"; then
    ok "B2: the B368 contracts pass"
  else
    bad "B2: go test ./internal/backup/ -run B368 failed:
$(printf '%s\n' "$out" | tail -20)"
  fi
fi

# =====================================================================
hdr "C. the success message is still wired (it is only reached on a real success)"

RU_HITS="$(grep -c '"backup.s3_test_ok"' "$I18N" || true)"
if [ "$RU_HITS" -eq 2 ]; then
  ok "C1: backup.s3_test_ok is defined in BOTH catalogues"
else
  bad "C1: backup.s3_test_ok appears $RU_HITS time(s) in $I18N, want 2 (RU+EN)"
fi
if grep -q 'tc.Fields\["bucket"\]' "$HANDLER"; then
  ok "C2: the success message names the bucket the probe verified"
else
  bad "C2: the success message no longer names the bucket"
fi

# =====================================================================
hdr "D. tracked by git (AGENTS trap #11), registered, indexed"

if git ls-files --error-unmatch "$SELF" >/dev/null 2>&1 \
   && git ls-files --error-unmatch "$TEST" >/dev/null 2>&1; then
  ok "D1: the new files are tracked by git"
else
  skip "D1: not yet tracked by git (the lead commits this block)"
fi

# D1b — the reason D1 needs a force-add at all. The unanchored `backup/` pattern
# matched `internal/backup/` at any depth, so the new test file was INVISIBLE to
# `git status` (measured 2026-10-09). Anchoring it to the root is what makes the
# assertion above meaningful for this package rather than a silent no-op.
if grep -qx '/backup/' .gitignore; then
  ok "D1b: .gitignore anchors the backup-output directory to the repo root (/backup/), so internal/backup/ is not swallowed"
else
  bad "D1b: .gitignore has no anchored '/backup/' rule — an unanchored 'backup/' hides NEW files in internal/backup/ from git add (AGENTS trap #11)"
fi
if grep -q 'run_check "B368"' scripts/verify_pre_deploy.sh; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B368\*\*' AGENTS.md; then
  ok "D3: recorded in the AGENTS.md block index (as its own line)"
else
  bad "D3: AGENTS.md has no B368 bullet at line start"
fi

printf '\n\033[1mB368 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
