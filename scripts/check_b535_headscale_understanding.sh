#!/usr/bin/env bash
# check_b535_headscale_understanding.sh
#
# 2026-11-27 (B535) — «чтобы можно было понимать, что не так с headscale».
#
# B533 (v1.5.265) answered three questions: who manages the service, is it running,
# and where does its control name point. The operator's NEXT question on the same day
# was «а что именно произошло» — and the card could answer it with ONE line (the last
# line containing "error"). This contract pins the two measurements that close it:
#
#   A. THE JOURNAL — the unit's own last lines, read for EVERY manager (systemd via
#      journalctl, docker via docker logs, OpenRC honestly saying it has no journal),
#      capped, counted, and REDACTED: a credential must never reach the panel, and an
#      EMPTY window must never read as «no errors».
#   B. THE HANDOVER — a request still waiting for the applier is visible, and the last
#      verdict only counts when it is OLDER than the measurement (PostRestartVerdict).
#      A verdict written after the measurement is «pending», not «ok»: that ordering
#      is the whole difference between «перезапуск сработал» and «ещё не смотрели».
#   C. THE ACTION — «Проверить сейчас» posts to a routed endpoint, is audited, and the
#      applier WAITS for the unit to be actually up before it writes `ok`.
#
# WHERE IT SKIPS: the live leg needs a host that manages the service.
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B535: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

. scripts/lib/gosurface.sh
gosurface BOOT cmd/skygate/*.go || {
  printf '  \033[31mFAIL\033[0m F: cannot read the cmd/skygate surface\n' >&2
  printf '\nB535: 0 pass, 1 fail, 0 skip\n'
  exit 1
}
GO=internal/headscale/service_insight_b535.go
GOT=internal/headscale/service_insight_b535_test.go
CTL=internal/headscale/service_control_b533.go
ADMIN=internal/feature/admin/headscale.go
TPL=internal/handlers/templates/admin/headscale.html
APPLIER=deploy/skygate-apply-service.sh
I18N=internal/i18n/catalog_admin.go

hdr "B535 — headscale: журнал, замер по кнопке и честная проверка после перезапуска"

# --- A: the journal ------------------------------------------------------------
A_MISS=""
[ -f "$GO" ] || A_MISS="${A_MISS}${GO} is missing"$'\n'
grep -q 'func CollectServiceJournal' "$GO" \
  || A_MISS="${A_MISS}CollectServiceJournal is missing — the card would fall back to one grep'd line"$'\n'
grep -q 'func RedactServiceLine' "$GO" \
  || A_MISS="${A_MISS}RedactServiceLine is missing — journal lines would reach the panel verbatim"$'\n'
grep -q 'tskey-' "$GO" \
  || A_MISS="${A_MISS}the auth-key shape (tskey-) is not redacted"$'\n'
grep -q 'bearer' "$GO" \
  || A_MISS="${A_MISS}a bearer token is not redacted"$'\n'
grep -q 'ServiceJournalShown' "$GO" && grep -q 'ServiceJournalRead' "$GO" \
  || A_MISS="${A_MISS}the read window and the shown window are not distinguished (a crash loop would flood the page)"$'\n'
grep -q 'case "systemd":' "$GO" && grep -q '"journalctl", "-u"' "$GO" \
  || A_MISS="${A_MISS}the systemd window is not read with journalctl -u"$'\n'
grep -q '"docker", "logs", "--tail"' "$GO" \
  || A_MISS="${A_MISS}the docker window is not read with docker logs --tail"$'\n'
grep -q 'an OpenRC install keeps no journal' "$GO" \
  || A_MISS="${A_MISS}the OpenRC case does not say it has no journal — it would look like a quiet service"$'\n'
grep -q "empty window is not 'no errors'" "$GO" \
  || A_MISS="${A_MISS}an empty journalctl answer is not separated from a quiet service"$'\n'
grep -q 'ServiceJournalTimeout' "$GO" \
  || A_MISS="${A_MISS}the journal read has no deadline (a hung journald would hold the page)"$'\n'
if [ -z "$A_MISS" ]; then ok "A: the journal is read per manager, capped, counted and redacted"
else while IFS= read -r l; do [ -n "$l" ] && bad "A: $l"; done <<< "$A_MISS"; fi

# --- B: the handover ordering --------------------------------------------------
B_MISS=""
grep -q 'f.Journal = CollectServiceJournal' "$CTL" \
  || B_MISS="${B_MISS}CollectServiceFacts never reads the journal (the field would stay empty)"$'\n'
grep -q 'f.MeasuredAt = time.Now().UTC()' "$CTL" \
  || B_MISS="${B_MISS}the measurement has no timestamp, so a verdict cannot be ordered against it"$'\n'
grep -q 'func PostRestartVerdict' "$GO" \
  || B_MISS="${B_MISS}PostRestartVerdict is missing — «сработал ли перезапуск» would be a guess"$'\n'
grep -q 'if !f.MeasuredAt.IsZero() && !ts.Before(f.MeasuredAt)' "$GO" \
  || B_MISS="${B_MISS}the verdict is not compared with the measurement time (a verdict from the future would count as proof)"$'\n'
grep -q 'func ReadServicePending' "$GO" \
  || B_MISS="${B_MISS}ReadServicePending is missing — «нажал и ничего» stays invisible"$'\n'
grep -q 'r.ActiveAfter' "$CTL" && grep -q 'r.Waited' "$CTL" \
  || B_MISS="${B_MISS}the applier's ACTIVE_AFTER/WAITED are not parsed, so «активна после перезапуска» cannot be shown"$'\n'
grep -q 'v.Pending = headscale.ReadServicePending()' "$ADMIN" \
  || B_MISS="${B_MISS}the page never fills Pending"$'\n'
grep -q 'v.PostRestart = headscale.PostRestartVerdict' "$ADMIN" \
  || B_MISS="${B_MISS}the page never computes the ordered verdict"$'\n'
if [ -z "$B_MISS" ]; then ok "B: the measurement is timestamped, the request is visible, and the verdict is ordered"
else while IFS= read -r l; do [ -n "$l" ] && bad "B: $l"; done <<< "$B_MISS"; fi

# --- C: the action on the page -------------------------------------------------
C_MISS=""
grep -q 'POST /admin/headscale/service/check' "$BOOT" \
  || C_MISS="${C_MISS}the «Проверить сейчас» route is not registered"$'\n'
grep -q 'func (s \*Service) PostAdminHeadscaleServiceCheck' "$ADMIN" \
  || C_MISS="${C_MISS}the check handler is missing"$'\n'
grep -q 'headscale.service_checked' "$ADMIN" \
  || C_MISS="${C_MISS}the check is not audited (a measurement nobody can date afterwards)"$'\n'
grep -q 'action="/admin/headscale/service/check"' "$TPL" \
  || C_MISS="${C_MISS}the page has no form pointing at the check route"$'\n'
grep -q 'headscale_service.flash_checked' "$ADMIN" \
  || C_MISS="${C_MISS}the flash does not carry the verdict numbers"$'\n'
grep -q 'headscale_service.journal_title' "$TPL" \
  || C_MISS="${C_MISS}the journal block is not rendered"$'\n'
grep -q 'Facts.MeasuredAt.Format' "$TPL" \
  || C_MISS="${C_MISS}the page does not say WHEN it measured — a stale card would look live"$'\n'
grep -q 'headscale_service.journal_redacted' "$TPL" \
  || C_MISS="${C_MISS}the page does not tell the operator that secrets are masked in the journal block"$'\n'
if [ -z "$C_MISS" ]; then ok "C: «Проверить сейчас» is routed, audited, and the card dates its own measurement"
else while IFS= read -r l; do [ -n "$l" ] && bad "C: $l"; done <<< "$C_MISS"; fi

# --- D: the applier waits for the unit to be UP --------------------------------
D_MISS=""
grep -q 'wait_active()' "$APPLIER" \
  || D_MISS="${D_MISS}the applier does not wait for the unit to be up (a restart that returns is not a restart that worked)"$'\n'
grep -q 'systemctl is-active --quiet' "$APPLIER" \
  || D_MISS="${D_MISS}the wait does not use systemctl is-active"$'\n'
grep -q 'ACTIVE_AFTER=' "$APPLIER" \
  || D_MISS="${D_MISS}ACTIVE_AFTER is not published"$'\n'
grep -q 'WAITED=' "$APPLIER" \
  || D_MISS="${D_MISS}WAITED is not published"$'\n'
grep -q 'did not stay active within' "$APPLIER" \
  || D_MISS="${D_MISS}a unit that dies after the restart would still be reported ok"$'\n'
if [ -z "$D_MISS" ]; then ok "D: the applier waits for the unit to be active and says whether it was"
else while IFS= read -r l; do [ -n "$l" ] && bad "D: $l"; done <<< "$D_MISS"; fi

# --- E: i18n pairs and the Go tests -------------------------------------------
E_BAD="$(grep -o '"headscale_service\.[a-z_0-9]*"' "$I18N" | sort | uniq -c | awk '$1 != 2 {print $2" (x"$1")"}')"
if [ -z "$E_BAD" ]; then
  ok "E1: every headscale_service.* key exists in both catalogues"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "E1: i18n key appears $l — RU+EN must be a pair"; done <<< "$E_BAD"
fi
[ -f "$GOT" ] || bad "E2: ${GOT} is missing (the rules are not pinned)"
for t in TestB535_RedactServiceLine TestB535_CollectServiceJournal_EmptyWindowIsNotNoErrors \
         TestB535_PostRestartVerdict_Matrix TestB535_ReadServicePending \
         TestB535_LastErrorLine_ComesFromTheShownAndRedactedTail; do
  grep -q "func $t" "$GOT" || bad "E2: $t is missing"
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 -run 'B535' ./internal/headscale/ 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    ok "E3: the B535 Go contracts pass (redaction, empty window, ordering, pending)"
  else
    bad "E3: the B535 Go contracts failed: $OUT"
  fi
else
  skip "E3: go not on PATH"
fi

# --- F: bookkeeping ------------------------------------------------------------
F_MISS=""
git ls-files --error-unmatch "$GOT" >/dev/null 2>&1 || F_MISS="${F_MISS}${GOT} is NOT tracked by git (trap 11)"$'\n'
grep -q '"B535"' scripts/verify_pre_deploy.sh || F_MISS="${F_MISS}verify_pre_deploy.sh does not register B535"$'\n'
git ls-files --error-unmatch scripts/check_b535_headscale_understanding.sh >/dev/null 2>&1 \
  || F_MISS="${F_MISS}this script is NOT tracked by git"$'\n'
if [ -z "$F_MISS" ]; then ok "F: tracked, registered and indexed"
else while IFS= read -r l; do [ -n "$l" ] && bad "F: $l"; done <<< "$F_MISS"; fi

# --- G: live leg ---------------------------------------------------------------
if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files --no-legend --no-pager headscale.service 2>/dev/null | grep -q headscale.service; then
  unit_state="$(systemctl show headscale --property=ActiveState --value 2>/dev/null)"
  if [ -n "$unit_state" ]; then
    ok "G: the live leg reads headscale's state on this host ($unit_state)"
  else
    bad "G: this host lists headscale.service but its state could not be read"
  fi
else
  skip "G: this host does not manage a headscale unit (expected off the reference install)"
fi

printf '\nB535: %d pass, %d fail, %d skip\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
