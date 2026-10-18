#!/usr/bin/env bash
# check_b396_diagnostic_honesty.sh
#
# 2026-10-18 (B396) — THE DIAGNOSTIC CATALOGUE MUST NOT ANSWER ONE QUESTION FROM TWO
# SOURCES, AND IT MUST NAME THE CAUSE IT HAS ALREADY MEASURED.
#
# WHY
#   The B394 catalogue was run on the operator's live installation on 2026-10-18 and
#   four of its failures were about the TESTS, not the product:
#
#     backup.recent            FAIL "newest backup skygate-full-20260913_205723.tar.gz
#                                    is 819h39m0s old (>7d)"
#     backup.verify            PASS "last backup 20h38m0s ago:
#                                    s3://skygate-backups/portal/…20261017_040030.tar.gz"
#         → two tests, ONE question, TWO sources (a local directory and the run
#           records). The destination had moved to S3; the local directory kept an
#           archive from before the move, and the test that read it lied.
#
#     derp.reachable           FAIL "dial tcp 127.0.0.1:443: connect: connection refused"
#         → the relay NAME resolves to 127.0.0.1 INSIDE the container (a host
#           /etc/hosts / compose extra_hosts leak, AGENTS trap #2). The message said
#           "check the firewall/DNS" — true and useless.
#
#     prefix_owner.consistency FAIL "2 prefix(es) are not served as recorded"
#         → "not advertised" and "advertised but not APPROVED" are different faults
#           with different buttons (Re-sync vs Approve), and the test merged them.
#
#     exit_rules.domain_prefix FAIL "7 of 12 address(es) are stale"
#         → "the auto-updater will rewrite them" is FALSE when the auto-updater is
#           off, which is exactly the case where the rows rot forever.
#
#   The block makes each failure say what it measured and what to press, and makes the
#   backup pair read the same record. Behaviourally pinned by
#   internal/feature/admin/system_tests_b396_test.go (the pair must PASS on a fresh run
#   record with no local archive, and BOTH must FAIL on an 819 h old one).
#
# CONTRACTS
#   A. the backup pair reads ONE source (the run records), and the loopback classifier
#   B. derp.reachable names a loopback resolution as the cause and the fix
#   C. prefix_owner.consistency separates "not advertised" from "not approved"
#   D. exit_rules.domain_prefix says whether the auto-updater will fix it by itself
#   E. registered, tracked, documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9/B386).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/feature/admin/system_tests.go ] || {
  printf 'B396: cannot locate internal/feature/admin from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b396.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

# The admin system-tests SURFACE (B339: read a surface, not one file), non-test files.
SURF="$SKY_TMP/admin_surface.go"
for f in internal/feature/admin/system_tests*.go; do
  case "$f" in *_test.go) continue ;; esac
  cat "$f" >> "$SURF" 2>/dev/null || true
done
GO_TEST=internal/feature/admin/system_tests_b396_test.go

count_of() { grep -cF -- "$1" "$2" 2>/dev/null || true; }
has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }

hdr "B396 — the diagnostics name the cause, and the backup pair agrees with itself"

SURF_SRC="$(cat "$SURF" 2>/dev/null || true)"
TEST_SRC="$(cat "$GO_TEST" 2>/dev/null || true)"

# ---------------------------------------------------------------------------
# A. the backup pair reads ONE source
# ---------------------------------------------------------------------------
hdr "A — backup.recent and backup.verify read the same record"

readers="$(count_of 'backup.last_run' "$SURF")"
if [ "${readers:-0}" -ge 2 ]; then
  ok "A1: both backup tests read the same run record (backup.last_run appears $readers times)"
else
  bad "A1: only $readers reader(s) of backup.last_run — the pair can still disagree (one reads the filesystem)"
fi
if has 'B396: THIS TEST AND backup.verify USED TO DISAGREE' "$SURF_SRC"; then
  ok "A2: the reason is written where the next reader will hit it"
else
  bad "A2: the B396 explanation is missing from the test it belongs to"
fi
if has 'reads the same record the backup.verify test does' "$SURF_SRC"; then
  ok "A3: the description says which source it reads (an operator can tell why it changed)"
else
  bad "A3: the description does not say the source"
fi
if has 'TestB396_BackupPairAgreeOnTheSource' "$TEST_SRC" && has 'TestB396_BackupRecentReadsTheRunRecords' "$TEST_SRC"; then
  ok "A4: the pair is pinned behaviourally (fresh run record with no local archive → PASS; 819 h → both FAIL)"
else
  bad "A4: no behavioural test that the pair agrees on the source"
fi
if has 'func b396AllLoopback(' "$SURF_SRC" && has 'b396AllLoopback(addrs)' "$SURF_SRC"; then
  ok "A5: the loopback classifier exists and is called on the resolved addresses"
else
  bad "A5: the loopback classifier is missing or unused"
fi

# ---------------------------------------------------------------------------
# B. derp.reachable names the loopback cause
# ---------------------------------------------------------------------------
hdr "B — a DERP name that resolves to the container's own loopback says so"

if has 'dialling its own loopback' "$SURF_SRC"; then
  ok "B1: the failure names what it measured (the panel is dialling its own loopback)"
else
  bad "B1: a loopback resolution is still reported as a generic 'unreachable'"
fi
if has 'extra_hosts' "$SURF_SRC" && has 'trap #2' "$SURF_SRC"; then
  ok "B2: the message names the mechanism (extra_hosts) and the trap that documents it"
else
  bad "B2: the message does not name the fix"
fi
if has 'LookupHost' "$SURF_SRC"; then
  ok "B3: the address is resolved before dialling, so the cause is available"
else
  bad "B3: nothing resolves the name, so the cause cannot be known"
fi

# ---------------------------------------------------------------------------
# C. prefix ownership: two faults, two buttons
# ---------------------------------------------------------------------------
hdr "C — prefix_owner.consistency separates 'not advertised' from 'not APPROVED'"

if has 'advertised but NOT APPROVED' "$SURF_SRC"; then
  ok "C1: an advertised-but-unapproved prefix is its own named class"
else
  bad "C1: 'not approved' is still merged into 'not served' — the operator is sent to the wrong button"
fi
if has 'not APPROVED there' "$SURF_SRC"; then
  ok "C2: the per-prefix problem line says which relay and that approval is what is missing"
else
  bad "C2: the per-prefix line does not name the approval gap"
fi
if has 'ApprovedRoutes' "$SURF_SRC"; then
  ok "C3: the approved set is read from headscale (advertised ≠ approved, B273's lesson)"
else
  bad "C3: the test never reads ApprovedRoutes"
fi

# ---------------------------------------------------------------------------
# D. the domain-rule failure says whether anything will fix it
# ---------------------------------------------------------------------------
hdr "D — exit_rules.domain_prefix reports the auto-updater's real state"

if has 'globalSettingsKeyDNSAutoUpdate' "$SURF_SRC" && has 'ParseDomainResolveInterval' "$SURF_SRC"; then
  ok "D1: the test reads the effective auto-update switch and the interval the updater itself parses"
else
  bad "D1: the message cannot know whether the updater will fix the rows"
fi
if has 'DNS auto-update is OFF' "$SURF_SRC" && has 'DNS auto-update is ON' "$SURF_SRC"; then
  ok "D2: both states are named (OFF = nothing will rewrite them until you act)"
else
  bad "D2: only one state is described"
fi

# ---------------------------------------------------------------------------
# E. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "E — registered, tracked, documented"

if git ls-files --error-unmatch scripts/check_b396_diagnostic_honesty.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git (trap #11)"
else
  bad "E1: this script is not tracked"
fi
if grep -q 'check_b396_diagnostic_honesty' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered in the catalog"
fi
if grep -q '^\- \*\*B396\*\*' AGENTS.md 2>/dev/null; then
  ok "E3: one-line entry in the AGENTS.md block index"
else
  bad "E3: no B396 entry in the AGENTS block index"
fi
if grep -q 'B396' docs/ROADMAP.md 2>/dev/null; then
  ok "E4: docs/ROADMAP.md records the block (rule 12)"
else
  bad "E4: the ROADMAP does not record the block"
fi
if grep -q 'B396' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "E5: the RU roadmap records it too"
else
  bad "E5: the RU roadmap does not record it"
fi

printf '\n\033[1mB396: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
