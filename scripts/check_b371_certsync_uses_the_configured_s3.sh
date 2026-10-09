#!/usr/bin/env bash
# check_b371_certsync_uses_the_configured_s3.sh — B371 (2026-10-09): certsync must
# read the S3 configuration the OPERATOR set, refuse one it cannot work with, and
# not print the same failure every 30 seconds.
#
# THE MEASUREMENT (reference deployment, PostgreSQL, operator-visible)
# -------------------------------------------------------------------
# The operator configured S3 on /admin/backup (endpoint https://minio.skynas.ru,
# bucket skygate-backups) and the panel's own backup worked. The journal carried,
# every 30 seconds, forever:
#
#   certsync: get .version: Get "http://s3..amazonaws.com/skygate-backups/?location=":
#   no such host
#
# Root cause, and it is a STALE PREMISE rather than a typo. B147 built certsync's
# config with buildBackupConfigForCertSync, which read SKYGATE_S3_ENDPOINT and
# SKYGATE_S3_REGION from the ENVIRONMENT only, documented as "same source the
# backup subsystem uses, so operators only configure one place". That stopped being
# true when the backup subsystem moved to global_settings (backup.Load, written by
# /admin/backup). With those variables unset, S3Endpoint AND S3Region were both
# empty, and internal/backup/s3.go does
#
#   ep = fmt.Sprintf("https://s3.%s.amazonaws.com", c.S3Region)   // region == ""
#
# i.e. the double-dotted host `s3..amazonaws.com`, which minio-go normalised to
# http. The scheduler started happily (the failure is NOT an error the caller can
# see — it happens inside the first tick), retried every 30 s (2880 identical lines
# a day) and no surface anywhere said which S3 it was reading from.
#
# Three defects, three fixes:
#   1. the DB the panel writes is the FIRST source of the S3 transport settings,
#      with the SKYGATE_S3_* env vars as the documented fallback (the certsync
#      BUCKET stays its own setting — a standby's cert bucket need not be the
#      primary's backup destination bucket);
#   2. a configuration that cannot work is REFUSED BY NAME at boot, naming the
#      setting to fix, instead of being handed to minio-go;
#   3. the startup line names the endpoint the client will really use, and an
#      identical tick failure is reported at most once an hour (the B318 pattern —
#      discoveryErrorIsNew in the same file).
#
# WHAT THIS SCRIPT PINS
#   A. the DB is the first source and the env vars are the fallback — for every
#      one of the four transport fields, and the bucket stays certsync's own;
#   B. an unusable config is refused by name (no `s3..amazonaws.com` may reach
#      minio-go), and the startup line names the real endpoint;
#   C. main.go passes the DB, refuses to start on a bad config, and keeps the
#      logged `certsync: enabled` / `certsync: disabled` lines (B147);
#   D. a repeated identical tick failure is suppressed for an hour;
#   E. the behavioural halves run;
#   F. tracked by git (AGENTS trap #11), registered, indexed.
#
# SKIP (never FAIL) anything that needs a tool this host lacks.
#
# Usage:  bash scripts/check_b371_certsync_uses_the_configured_s3.sh
# Exit:   0 = contracts hold, 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/gosurface.sh

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

gosurface MAIN cmd/skygate/*.go
HELPERS=cmd/skygate/main_helpers.go
CERTSYNC=internal/certsync/certsync.go
S3=internal/backup/s3.go
T_MAIN=cmd/skygate/cert_sync_s3_b371_test.go
T_CERTSYNC=internal/certsync/certsync_b371_test.go
SELF=scripts/check_b371_certsync_uses_the_configured_s3.sh

for f in "$HELPERS" "$CERTSYNC" "$S3" "$T_MAIN" "$T_CERTSYNC" "$MAIN"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B371 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# =====================================================================
hdr "A. the panel's database is the source, the env vars are the fallback"

if grep -q 'func certSyncS3Config(d \*sql.DB, cfg \*config.Config)' "$HELPERS"; then
  ok "A1: certSyncS3Config takes the DB — the config the operator actually saved"
else
  bad "A1: certSyncS3Config is gone or no longer receives the DB — certsync would read the environment alone again"
fi
if grep -q 'backup.Load(d)' "$HELPERS"; then
  ok "A2: it reads the same global_settings the backup subsystem does (backup.Load)"
else
  bad "A2: the panel's S3 settings are not read — this is exactly the stale B147 premise"
fi
# Every transport field needs its env fallback; a missing one silently becomes "".
for field in S3Endpoint:SKYGATE_S3_ENDPOINT S3Region:SKYGATE_S3_REGION S3AccessKey:SKYGATE_S3_ACCESS_KEY S3SecretKey:SKYGATE_S3_SECRET_KEY; do
  fld="${field%%:*}"; env="${field##*:}"
  if grep -q "${fld} == \"\"" "$HELPERS" && grep -q "\"${env}\"" "$HELPERS"; then
    ok "A3: ${fld} falls back to ${env} only when the DB leaves it empty"
  else
    bad "A3: ${fld} has no guarded ${env} fallback"
  fi
done
if grep -q 'out.S3Bucket = strings.TrimSpace(cfg.CertSyncBucket)' "$HELPERS" \
   && grep -q 'SKYGATE_S3_BUCKET' "$HELPERS"; then
  bad "A4: the certsync bucket was taken from the backup destination — it is a deliberately separate setting"
else
  ok "A4: the bucket stays certsync's own (cfg.CertSyncBucket); only the transport is shared"
fi

# =====================================================================
hdr "B. a config that cannot work is refused by name"

if grep -q 'func certSyncS3ConfigProblem(' "$HELPERS"; then
  ok "B1: one predicate decides whether the settings can carry a cert pull"
else
  bad "B1: nothing refuses an unusable config — minio-go gets it and the first tick fails with a host that cannot exist"
fi
if grep -q 's3\.\.amazonaws\.com' "$HELPERS" && grep -q 'neither the S3 endpoint nor the region is set' "$HELPERS"; then
  ok "B2: the refusal names the double-dotted host and the two settings to set"
else
  bad "B2: the endpoint+region refusal lost its explanation"
fi
if grep -q 'access key' "$HELPERS" && grep -q 'S3 bucket' "$HELPERS"; then
  ok "B3: missing credentials and a missing bucket are named too (not a generic 'certsync failed')"
else
  bad "B3: only some unusable combinations are named"
fi
# The comment in s3.go is where the nonsense host comes from: the guard must exist
# because the fallback is real, and the source of it must stay documented.
if grep -q 'fmt.Sprintf("https://s3.%s.amazonaws.com", c.S3Region)' "$S3"; then
  ok "B4: the AWS-default derivation that produced s3..amazonaws.com is still the one being guarded"
else
  skip "B4: the AWS-default derivation moved — re-read the guard in certSyncS3ConfigProblem"
fi
if grep -q 'func certSyncS3EndpointLog(' "$HELPERS"; then
  ok "B5: the startup line can name the endpoint the client will really use"
else
  bad "B5: 'which S3 is certsync reading from?' would again be answerable only from a failure"
fi

# =====================================================================
hdr "C. main.go wires the DB, refuses to start, and keeps B147's log lines"

if grep -q 'certSyncS3Config(d.DB, cfg)' "$MAIN"; then
  ok "C1: main.go passes the live DB"
else
  bad "C1: main.go does not pass the DB — the fix is unreachable"
fi
if grep -q 'certsync: disabled — %v' "$MAIN"; then
  ok "C2: an unusable config disables certsync with the named reason instead of starting a doomed 30 s loop"
else
  bad "C2: main.go starts certsync even when its S3 config cannot work"
fi
if grep -q 'certsync: enabled' "$MAIN" && grep -q 'certsync: disabled' "$MAIN"; then
  ok "C3: B147's startup log lines survive the renegotiation"
else
  bad "C3: the startup log lines were lost — B147's contracts break"
fi
if grep -q 'endpoint=%s, region=%s' "$MAIN"; then
  ok "C4: the enabled line reports endpoint + region"
else
  bad "C4: the enabled line does not say which S3 it uses"
fi
if grep -q 'buildBackupConfigForCertSync' "$MAIN"; then
  bad "C5: the env-only builder is still referenced — check which one runs"
else
  ok "C5: the env-only builder is gone from the surface"
fi

# =====================================================================
hdr "D. an identical tick failure is not printed 2880 times a day"

if grep -q 'func fetchFailureIsNew(' "$CERTSYNC"; then
  ok "D1: the cert pull has the B318 suppression window"
else
  bad "D1: the same fetch failure is logged on every tick again"
fi
if grep -q 'if fetchFailureIsNew(err) {' "$CERTSYNC"; then
  ok "D2: the tick consults it before logging (the 404 'no cert yet' path is untouched)"
else
  bad "D2: the window exists but the tick does not use it"
fi
if grep -q 'certFetchErrRepeatAfter = time.Hour' "$CERTSYNC"; then
  ok "D3: the window is an hour — a second, unrelated outage is never hidden"
else
  bad "D3: the suppression window changed without a deliberate decision"
fi

# =====================================================================
hdr "E. the behavioural halves run"

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
  skip "E1: no go binary in PATH — the behavioural contracts were not run"
else
  if out="$("$GOBIN" test ./cmd/skygate/ ./internal/certsync/ -run 'B371' -count=1 2>&1)"; then
    ok "E1: the B371 contracts pass"
  else
    bad "E1: go test -run B371 failed:
$(printf '%s\n' "$out" | tail -20)"
  fi
  # B147's own unit tests must keep passing next to the new tick guard.
  if out="$("$GOBIN" test ./internal/certsync/ -run 'TestNoVersionIsNoOp|TestVersionBumpTriggersPull|TestSHAMismatchTriggersPull|TestInvalidCertFails' -count=1 2>&1)"; then
    ok "E2: B147's certsync unit tests still pass beside the suppression window"
  else
    bad "E2: B147's certsync unit tests fail:
$(printf '%s\n' "$out" | tail -20)"
  fi
fi

# =====================================================================
hdr "F. tracked by git (AGENTS trap #11), registered, indexed"

UNTRACKED=""
for f in "$SELF" "$T_MAIN" "$T_CERTSYNC"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || UNTRACKED="${UNTRACKED}${f} "
done
if [ -z "$UNTRACKED" ]; then
  ok "F1: every new file of this block is tracked by git"
else
  skip "F1: not yet tracked by git (the lead commits this block): ${UNTRACKED}"
fi
if grep -q 'run_check "B371"' scripts/verify_pre_deploy.sh; then
  ok "F2: registered in scripts/verify_pre_deploy.sh"
else
  bad "F2: not registered — the contract would never run"
fi
if grep -q '^- \*\*B371\*\*' AGENTS.md; then
  ok "F3: recorded in the AGENTS.md block index (as its own line)"
else
  bad "F3: AGENTS.md has no B371 bullet at line start"
fi
# B147 is the block this one renegotiates: its own contract C must still pass.
if [ -f scripts/check_b147.sh ]; then
  if bash scripts/check_b147.sh >/dev/null 2>&1; then
    ok "F4: B147 still passes with the renegotiated wiring"
  else
    bad "F4: B147 fails — its contract C still expects buildBackupConfigForCertSync"
  fi
else
  skip "F4: scripts/check_b147.sh is missing"
fi

printf '\n\033[1mB371 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
