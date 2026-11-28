#!/usr/bin/env bash
# check_b534_duplicate_nodes.sh
#
# 2026-11-27 (B534) — «при OIDC выскочил skybars-1 хотя это устройство skybars».
#
# THE REPORTED SYMPTOM. During an OIDC login a device registered as `skybars-1`
# although the operator's device is `skybars`, and the operator asked the question
# this block exists to answer: why does the duplication happen at all, and how can
# skygate track it.
#
# WHY IT HAPPENS (not a skygate bug, and not preventable server-side). A node's
# identity in headscale is its MACHINE KEY. Clearing the Tailscale app's state — or
# logging in again under another account — destroys that key, so the client
# registers as a brand-new node; headscale must keep given names unique, so the
# newcomer becomes `<name>-1`. Live pair on 2026-11-27:
#
#	id=55  skybars    daniil    tag:dev-daniil-skybars      offline since 12:16
#	id=198 skybars-1  skyadmin  tag:dev-skyadmin-skybars-1  online, created 14:41
#
# THE COST the product was silent about: every per-device ACL row, exit-node pin and
# dev-tag is keyed by HOSTNAME, so the live device matches none of the rules written
# for the old name. The operator found it by sight.
#
# WHAT THIS CONTRACT PINS
#   A. the detector exists, names its evidence (three kinds), and — critically —
#      never ACTS: it reports a pair, it does not delete or rename a node;
#   B. the maintenance tick reports every pair through the existing alert sink
#      (audit + metric + rate-limited Telegram), so the fact is not only in a log;
#   C. /admin/devices shows the pair (card + paired i18n keys), so the operator can
#      act without reading a journal;
#   D. the LIVE fixture is a test: the skybars/skybars-1 pair must be detected;
#   E. bookkeeping (the detector is tracked by git — AGENTS trap #11).
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B534: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

DET=internal/nodeownership/duplicates_b534.go
DETT=internal/nodeownership/duplicates_b534_test.go
TICK=internal/nodeownership/auto.go
ADMIN=internal/feature/admin/devices.go
TPL=internal/handlers/templates/admin/devices.html
I18N=internal/i18n/catalog_admin.go

hdr "B534 — one device behind two nodes: report it, never guess it"

# --- A: the detector -----------------------------------------------------------
A_MISS=""
[ -f "$DET" ] || A_MISS="${A_MISS}${DET} is missing"$'\n'
grep -q 'func DetectDuplicates' "$DET" \
  || A_MISS="${A_MISS}DetectDuplicates is missing"$'\n'
grep -q 'func duplicateBase' "$DET" \
  || A_MISS="${A_MISS}the suffix parser is missing — a name ending in -N would be invisible"$'\n'
for kind in DuplicateSameHostStale DuplicateSameHostLive DuplicateDifferentHost; do
  grep -q "$kind" "$DET" \
    || A_MISS="${A_MISS}the kind ${kind} is missing: the page would report every collision as the same evidence"$'\n'
done
grep -q 'sameHostname' "$DET" \
  || A_MISS="${A_MISS}the same-hostname predicate is missing (a suffixed name alone is NOT proof of one device)"$'\n'
# The detector must not act: no delete/rename call anywhere in the file.
if grep -qE 'DeleteNode|RenameNode|RemoveNode|ExpireNode' "$DET"; then
  A_MISS="${A_MISS}the detector calls a destructive API — it must only report (a similar-looking name is not proof)"$'\n'
fi
grep -q 'func (d NodeDuplicate) Reason' "$DET" \
  || A_MISS="${A_MISS}Reason() is missing: the log/alert would carry no names"$'\n'
if [ -z "$A_MISS" ]; then ok "A: the detector names its evidence and never acts"
else while IFS= read -r l; do [ -n "$l" ] && bad "A: $l"; done <<< "$A_MISS"; fi

# --- B: the maintenance tick reports through the alert sink --------------------
B_MISS=""
grep -q 'DetectDuplicates(nodes)' "$TICK" \
  || B_MISS="${B_MISS}the maintenance tick does not run the detector — the fact would never leave a manual page load"$'\n'
grep -q 'duplicate-node: %s' "$TICK" \
  || B_MISS="${B_MISS}the tick does not log the pair"$'\n'
# B536 (v1.5.277): the report has its OWN channel. It used to go through
# ReportFailure, which made a report wear the failure's costume («❌ skygate tag
# autoupdate FAILED … failed tag … reason: unknown») and cost the operator an evening of
# chasing a tag write that never happened. The three signals are the same; the words are not.
grep -q 'alertSink.ReportDuplicate(dup)' "$TICK" \
  || B_MISS="${B_MISS}the pair is not reported through the duplicate channel (no audit row, no metric, no Telegram)"$'\n'
if grep -q 'ReportFailure(dup\.' "$TICK"; then
  B_MISS="${B_MISS}a duplicate is still reported as a TAG FAILURE — the metric and the message would lie"$'\n'
fi
if [ -z "$B_MISS" ]; then ok "B: the tick reports every pair through the duplicate channel (audit + its own metric + Telegram)"
else while IFS= read -r l; do [ -n "$l" ] && bad "B: $l"; done <<< "$B_MISS"; fi

# --- C: the page shows it ------------------------------------------------------
C_MISS=""
grep -q 'nodeownership.DetectDuplicates(allNodes)' "$ADMIN" \
  || C_MISS="${C_MISS}/admin/devices does not compute the duplicates from the node list it already fetched"$'\n'
grep -q '"Duplicates"' "$ADMIN" \
  || C_MISS="${C_MISS}the page does not pass the pairs to the template"$'\n'
grep -q '{{if .Duplicates}}' "$TPL" \
  || C_MISS="${C_MISS}the template has no duplicate card"$'\n'
grep -q 'devices.duplicates_kind_stale' "$TPL" \
  || C_MISS="${C_MISS}the card does not distinguish a re-registration from a live conflict"$'\n'
DUP_BAD="$(grep -o '"devices\.duplicates_[a-z_]*"' "$I18N" | sort | uniq -c | awk '$1 != 2 {print $2" (x"$1")"}')"
if [ -n "$DUP_BAD" ]; then
  while IFS= read -r l; do [ -n "$l" ] && bad "C: i18n key appears $l — RU+EN must be a pair"; done <<< "$DUP_BAD"
else ok "C: /admin/devices renders the pair with paired RU+EN keys"; fi
if [ -z "$C_MISS" ]; then :; else while IFS= read -r l; do [ -n "$l" ] && bad "C: $l"; done <<< "$C_MISS"; fi

# --- D: the live fixture is a test --------------------------------------------
D_MISS=""
[ -f "$DETT" ] || D_MISS="${D_MISS}${DETT} is missing"$'\n'
grep -q 'TestB534_TheLiveSkybarsPairIsReported' "$DETT" \
  || D_MISS="${D_MISS}the live skybars/skybars-1 pair is not pinned as a test"$'\n'
grep -q '"198"' "$DETT" \
  || D_MISS="${D_MISS}the fixture does not carry the live node ids — a detector that does not fire on that pair detects nothing worth having"$'\n'
grep -q 'TestB534_WhatIsNotADuplicate' "$DETT" \
  || D_MISS="${D_MISS}the negative cases are not pinned (work-2 alone, counter 0, empty names)"$'\n'
if [ -z "$D_MISS" ]; then ok "D: the live pair and the negatives are tests"
else while IFS= read -r l; do [ -n "$l" ] && bad "D: $l"; done <<< "$D_MISS"; fi

# --- E: bookkeeping ------------------------------------------------------------
if git ls-files --error-unmatch "$DET" >/dev/null 2>&1; then
  ok "E: ${DET} is tracked by git"
else
  bad "E: ${DET} is NOT tracked by git (AGENTS trap #11: the detector the release ships would be absent)"
fi

printf '\nB534: %d pass, %d fail, %d skip\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
