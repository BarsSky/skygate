#!/usr/bin/env bash
# check_b321_tailscale_survives_update.sh
#
# 2026-09-25 (B321, v1.5.86) — Tailscale must survive a container recreate, and the
# canonical name `skygate-host` must be the name the tailnet actually gets.
#
# OPERATOR REPORT (verbatim):
#
#   «при этом при каждом обновлении слетает запуск tailscale приходится прожимать
#    каждый раз старт и активным стал skygate-host-1 как skygate tailscale хотя
#    должен быть строго skygate-host (после обновления выскочило предложение сменить
#    имя и я его прожал, проверь что имя сменилось)»
#
# WHAT THE LIVE TOOLS FOUND (read-only, agent VM, after the operator's reclaim):
#
#   headscale node 87  given_name=skygate-host  name=skygate-host  100.64.0.10  ONLINE
#                      tags = [tag:dev-infra-skygate-host, tag:dev-infra-skygate-host-1]
#   client             tailscale status --json → .Self.HostName = skygate-host   (the
#                      rename DID land, and the ghost node 57 is gone)
#   container env      SKYGATE_TS_AUTHKEY_FILE=/dev/null   (docker-compose.yml:111)
#                      SKYGATE_TS_HOSTNAME=skygate-host-1   (docker-compose.yml:126)
#
# So the two complaints had two independent causes, and neither was the rename:
#
#   1. the ENTRYPOINT skips tailscaled when SKYGATE_TS_AUTHKEY_FILE is /dev/null, and
#      Docker freezes the environment at container CREATION — every update therefore
#      produced a container with Tailscale off, and only the operator's click brought
#      it back;
#   2. `tailscaleHostname()` returned the same frozen `skygate-host-1` pin, so that
#      click ran `tailscale up --hostname=skygate-host-1` and renamed the node back to
#      the legacy v0.33.1.9 placeholder (which B251 matches by STRICT equality, so a
#      suffixed client misses every "is this me?" check).
#
# CONTRACTS
#   A. the operator's intent is persisted (Start/Stop/Enable/Disable write it)
#   B. the PROCESS re-applies it: boot pass + tick, intent outranks the frozen
#      sentinel, the running daemon's name is enforced, and nothing in the path is fatal
#   C. the hostname resolves DB > env > default and the legacy placeholder is
#      rewritten, never honoured — while the reserved default stays in tailscale.go
#   D. reclaiming the name also removes the stale infra tag of the PREVIOUS name
#   E. UntagNode can no longer rewrite a tag set from a failed read
#   F. the page + i18n explain the persistence (RU + EN pairs)
#   G. tests exist and pass; the harness repairs of the audit are pinned; this script
#      is TRACKED BY GIT (AGENTS trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B321: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

BOOT=internal/feature/admin/tailscale_boot_b321.go
TS=internal/feature/admin/tailscale.go
NAME=internal/feature/admin/tailscale_selfname_b320.go
TAGS=internal/headscale/tags.go
MAIN=cmd/skygate/main.go
TPL=internal/handlers/templates/admin/tailscale.html
I18N=internal/i18n/catalog_tailscale.go
TEST1=internal/feature/admin/tailscale_boot_b321_test.go
TEST2=internal/headscale/untag_guard_b321_test.go

# func_body <file> <func-header-prefix> — the gofmt'ed body of a top-level function.
func_body() { sed -n "/^$2/,/^}/p" "$1"; }

# arm_of <file> <func-header-prefix> <case-label> — one `case` arm of a switch.
# The label is matched on the trimmed line, so the arm body is what is examined and not
# the preceding clauses.
arm_of() {
  func_body "$1" "$2" | awk -v pat="$3" '
    $0 ~ "^[[:space:]]*case " pat { f = 1; print; next }
    f && $0 ~ "^[[:space:]]*case " { exit }
    f && $0 ~ "^[[:space:]]*}" { exit }
    f { print }
  '
}

hdr "B321 — the tailnet name and the tailscale start must survive an update"

# --- A: the operator's intent is persisted -------------------------------------------
if grep -q 'tailscaleDesiredStateDBKey = "tailscale.desired_state"' "$BOOT"; then
  ok "A1: the intent is one named key (tailscale.desired_state)"
else
  bad "A1: the intent key is missing"
fi
if grep -q 'tailscaleDesiredOn  = "on"' "$BOOT" && grep -q 'tailscaleDesiredOff = "off"' "$BOOT"; then
  ok "A2: on/off are constants, not string literals sprinkled around"
else
  bad "A2: the intent values are not centralised"
fi
START_BODY="$(func_body "$TS" 'func (s \*Service) handleTailscaleStart(')"
if grep -q 'setTailscaleDesiredState(tailscaleDesiredOn)' <<< "$START_BODY"; then
  ok "A3: Start records the intent (that is what makes the next update keep it up)"
else
  bad "A3: Start does not persist the intent — the start still dies with the container"
fi
STOP_BODY="$(func_body "$TS" 'func (s \*Service) handleTailscaleStop(')"
if grep -q 'setTailscaleDesiredState(tailscaleDesiredOff)' <<< "$STOP_BODY"; then
  ok "A4: Stop records the intent"
else
  bad "A4: Stop does not persist the intent (the autostart would undo it)"
fi
EN_BODY="$(func_body "$TS" 'func (s \*Service) handleTailscaleEnableInContainer(')"
DIS_BODY="$(func_body "$TS" 'func (s \*Service) handleTailscaleDisableInContainer(')"
if grep -q 'setTailscaleDesiredState(tailscaleDesiredOn)' <<< "$EN_BODY" \
   && grep -q 'setTailscaleDesiredState(tailscaleDesiredOff)' <<< "$DIS_BODY"; then
  ok "A5: the Enable/Disable toggle records the intent too"
else
  bad "A5: the in-container toggle does not persist the intent"
fi

# --- B: the process re-applies it -----------------------------------------------------
if grep -q 'func (s \*Service) RunTailscaleAutostart(ctx context.Context)' "$BOOT"; then
  ok "B1: the autostart entry point exists"
else
  bad "B1: RunTailscaleAutostart is missing"
fi
if grep -q 'adminSvc.RunTailscaleAutostart(context.Background())' "$MAIN"; then
  ok "B2: main.go starts it (the entrypoint cannot see the DB)"
else
  bad "B2: the autostart is never started"
fi
if grep -q 'tailscaleAutostartBootDelay' "$BOOT" && grep -q 'TailscaleAutostartInterval' "$BOOT"; then
  ok "B3: a boot pass plus a periodic re-assert"
else
  bad "B3: no boot pass / ticker"
fi
ON_ARM="$(arm_of "$BOOT" 'func (s \*Service) tailscaleAutostartDecision(' 'tailscaleDesiredOn:')"
if [ -n "$ON_ARM" ] && ! grep -q 'tailscaleAuthKeyDisabled' <<< "$ON_ARM"; then
  ok "B4: the recorded ON intent outranks the frozen /dev/null sentinel"
else
  bad "B4: the ON branch still consults the container sentinel (the live bug)"
fi
ENSURE_BODY="$(func_body "$BOOT" 'func (s \*Service) EnsureTailscaleUp(')"
if grep -q 'renameSelfClient(ctx, name)' <<< "$ENSURE_BODY"; then
  ok "B5: the resolved name is enforced on the RUNNING daemon (tailscale up does not rename)"
else
  bad "B5: nothing renames an already-registered client"
fi
if ! grep -qE 'log\.Fatal|os\.Exit|panic\(' "$BOOT"; then
  ok "B6: nothing on the autostart path is fatal"
else
  bad "B6: the autostart path can kill the process"
fi

# --- C: the hostname ------------------------------------------------------------------
if grep -q 'func NormalizeTailscaleHostname(name string) (string, bool)' "$BOOT" \
   && grep -q 'tailscalePlaceholderRe = regexp.MustCompile(`\^skygate-host(-\\d+)+\$`)' "$BOOT"; then
  ok "C1: the legacy placeholder shape is one named regexp"
else
  bad "C1: the placeholder shape is not centralised"
fi
if grep -q 'name, rewritten := NormalizeTailscaleHostname(raw)' "$BOOT" \
   && grep -q 'return tailscaleHostnameDefault(), true' "$BOOT"; then
  ok "C2: a suffixed skygate-host-N value is REWRITTEN to the canonical name, never honoured"
else
  bad "C2: the placeholder is not normalised"
fi
RESOLVE_BODY="$(func_body "$TS" 'func (s \*Service) tailscaleHostname(')"
if grep -q 's.tailscaleHostnameResolved()' <<< "$RESOLVE_BODY"; then
  ok "C3: tailscaleHostname delegates to the DB-first resolution"
else
  bad "C3: tailscaleHostname still reads the frozen env value directly"
fi
if grep -q 'return "skygate-host"' "$TS"; then
  ok "C4: the reserved default literal stays in tailscale.go (check_b251 contract C)"
else
  bad "C4: the reserved default moved — check_b251 contract C would break"
fi
if grep -q 'tailscaleHostnameDBKey = "tailscale.hostname"' "$BOOT" \
   && grep -q 'case "save_hostname":' "$TS" \
   && grep -q 'func (s \*Service) handleTailscaleSaveHostname(' "$BOOT"; then
  ok "C5: the name is persistable from the page (DB key + action + handler)"
else
  bad "C5: the operator cannot persist a name"
fi

# --- D: reclaim leftovers -------------------------------------------------------------
if grep -q 'func (s \*Service) cleanupStaleSelfTags(previousName, canonical string) \[\]string' "$BOOT"; then
  ok "D1: the stale-tag cleaner exists"
else
  bad "D1: cleanupStaleSelfTags is missing"
fi
if grep -q 's.cleanupStaleSelfTags(state.Live, ReservedSelfHostname)' "$NAME"; then
  ok "D2: the reclaim action calls it (live: node 87 kept BOTH the -1 and the canonical tag)"
else
  bad "D2: reclaiming the name leaves the old tag behind"
fi
CLEAN_BODY="$(func_body "$BOOT" 'func (s \*Service) cleanupStaleSelfTags(')"
if grep -q 'if !n.Online {' <<< "$CLEAN_BODY" \
   && grep -q 'strings.EqualFold(name, canon)' <<< "$CLEAN_BODY" \
   && grep -q 'staleInfraTags(n.Tags, canon)' <<< "$CLEAN_BODY"; then
  ok "D3: it only touches an ONLINE self node and only infra tags that do not describe it"
else
  bad "D3: the cleaner is not scoped to the self node / the canonical name"
fi
# B321.1 — the operator's live state after a successful reclaim is "ghost gone, the OLD
# tag still on the node", and the reclaim button answers "no stale node holds ..." once the
# ghost is gone; so the leftover must be cleared by the autostart tick, and the selection
# must be a pure function the tests can pin.
if grep -q 'func staleInfraTags(tags \[\]string, canonical string) \[\]string' "$BOOT"; then
  ok "D4: the stale-tag SELECTION is a pure function (unit-tested, no headscale needed)"
else
  bad "D4: the stale-tag selection is not separable"
fi
TICK_BODY="$(func_body "$BOOT" 'func (s \*Service) EnsureTailscaleUp(')"
if grep -q 's.cleanupStaleSelfTags(name, name)' <<< "$TICK_BODY"; then
  ok "D5: the autostart tick clears a leftover tag without an operator click"
else
  bad "D5: the leftover tag is only removed by the reclaim button (which refuses once the ghost is gone)"
fi
RECLAIM_BODY="$(func_body "$NAME" 'func (s \*Service) PostAdminTailscaleReclaimName(')"
if grep -n 'cleanupStaleSelfTags' <<< "$RECLAIM_BODY" | head -1 | cut -d: -f1 | \
   { read -r clean_line; ghost_line=$(grep -n 'if state.GhostID == ""' <<< "$RECLAIM_BODY" | head -1 | cut -d: -f1); [ -n "$clean_line" ] && [ -n "$ghost_line" ] && [ "$clean_line" -lt "$ghost_line" ]; }; then
  ok "D6: the reclaim action cleans the leftover BEFORE the 'nothing to delete' early return"
else
  bad "D6: the reclaim action returns before cleaning when the ghost is already gone"
fi

# --- E: UntagNode can no longer wipe a tag set ----------------------------------------
UNTAG_BODY="$(func_body "$TAGS" 'func (c \*Client) UntagNode(')"
if grep -q 'untag: read the tags of node' <<< "$UNTAG_BODY"; then
  ok "E1: a failed read is propagated instead of being swallowed"
else
  bad "E1: the read error is still swallowed"
fi
if grep -q 'is not in headscale (nothing written)' <<< "$UNTAG_BODY"; then
  ok "E2: an unknown node id is refused instead of being treated as tagless"
else
  bad "E2: an unknown node id still falls through to a write"
fi
if grep -q 'if !present {' <<< "$UNTAG_BODY"; then
  ok "E3: an absent tag is a no-op (no equal rewrite, no lossy one)"
else
  bad "E3: an absent tag still triggers a write"
fi
if ! grep -q 'if nodes, err := c.ListAllNodes(); err == nil {' "$TAGS"; then
  ok "E4: the pre-B321 swallow pattern is gone (AGENTS: on read error do NOT write)"
else
  bad "E4: the swallow pattern survives in tags.go"
fi

# --- F: the page + i18n ---------------------------------------------------------------
MISSING=""
for k in tailscale.persist_heading tailscale.persist_hostname tailscale.persist_hostname_src \
         tailscale.persist_src_db tailscale.persist_src_env tailscale.persist_src_default \
         tailscale.persist_intent tailscale.persist_intent_on tailscale.persist_intent_off \
         tailscale.persist_intent_unset tailscale.persist_autostart \
         tailscale.persist_autostart_yes tailscale.persist_autostart_no \
         tailscale.persist_placeholder tailscale.persist_help tailscale.persist_btn; do
  n=$(grep -c "\"$k\"" "$I18N")
  if [ "$n" -lt 2 ]; then MISSING="$MISSING $k($n)"; fi
done
if [ -z "$MISSING" ]; then
  ok "F1: every B321 key is defined in BOTH catalogues (rule 10)"
else
  bad "F1: keys missing from one side (count in parentheses):$MISSING"
fi
if grep -q 'tailscale.persist_heading' "$TPL" \
   && grep -q 'name="action" value="save_hostname"' "$TPL" \
   && grep -q '.State.SelfName.AutostartWhy' "$TPL"; then
  ok "F2: the page renders the effective name, the intent and the autostart verdict"
else
  bad "F2: the page does not explain the persistence"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/i18n/ -run TestCatalogsParity -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F3: the i18n parity test passes"
  else
    bad "F3: i18n parity failed:"; printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "F3: go not on PATH — run the i18n parity test on the VM"
fi

# --- G: tests + harness integrity + git ----------------------------------------------
if [ -f "$TEST1" ] && [ -f "$TEST2" ]; then
  ok "G1: both B321 test files exist"
else
  bad "G1: a B321 test file is missing"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/admin/ -run 'B321' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "G2: the intent/hostname/decision tests pass"
  else
    bad "G2: the B321 admin tests failed:"; printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/headscale/ -run 'UntagNode' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "G3: the UntagNode no-write-path tests pass"
  else
    bad "G3: the UntagNode tests failed:"; printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go vet ./internal/feature/admin/ ./internal/headscale/ ./cmd/skygate/ 2>&1)"
  if [ -z "$OUT" ]; then ok "G4: go vet is clean"; else
    bad "G4: go vet reported:"; printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "G2/G3/G4: go not on PATH — run them on the VM"
fi
# The audit (B1–B320 regression-detection review) found check_b251.sh could NEVER fail:
# bad() printed a FAIL line while the script still exited 0, and the gate hides a
# check's output on PASS. That file guards the reserved-name logic B321 depends on.
if grep -q 'FAIL=$((FAIL+1))' scripts/check_b251.sh && grep -q '\[ "$FAIL" -eq 0 \] || exit 1' scripts/check_b251.sh; then
  ok "G5: check_b251.sh now counts failures and exits non-zero"
else
  bad "G5: check_b251.sh is still a decorative check (no counters / no exit)"
fi
if git ls-files --error-unmatch scripts/check_b321_tailscale_survives_update.sh >/dev/null 2>&1; then
  ok "G6: this script is TRACKED by git (AGENTS trap #11)"
else
  bad "G6: this script is NOT tracked by git"
fi

printf '\n\033[1mB321 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
