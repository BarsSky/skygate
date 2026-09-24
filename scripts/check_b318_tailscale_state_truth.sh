#!/usr/bin/env bash
# check_b318_tailscale_state_truth.sh
#
# 2026-09-24 (B318, v1.5.83) — two pages, one daemon, one story.
#
# OPERATOR REPORT (verbatim):
#
#   «теперь на странице admin/exit-nodes показывает что skygate не включает tailscale
#    однако он работает если переходить на соответствующую вкладку посмотри в чем дело»
#
# WHAT THE LIVE TOOLS FOUND (read-only, reference host):
#
#   /admin/exit-nodes   «skygate НЕ в tailnet … tailscaled is not running: failed to
#                        connect to local tailscaled; it doesn't appear to be running»
#   /admin/tailscale    rendered the ENABLED branch (green card, clickable Start),
#                       because global_settings[tailscale.auth_key_path] pointed at
#                       /data/ts/authkey and that file exists
#   container env       SKYGATE_TS_AUTHKEY_FILE=/dev/null  → entrypoint logged
#                       «[init] TS_AUTHKEY_FILE not set — Tailscale skipped (non-RF mode)»
#   data/ts/tailscaled.log1.txt (previous container instance):
#                       «wgengine.NewUserspaceEngine(tun "tailscale0") error:
#                        tstun.New("tailscale0"): CreateTUN("tailscale0") failed;
#                        /dev/net/tun does not exist»
#
# So both pages were internally honest and both named the wrong thing: nothing was
# running because the ENTRYPOINT was told (at container creation, frozen) to skip the
# daemon, and because the last start attempt had died for a missing TUN device — two
# facts neither page rendered. The DB override only changes what the page and its Start
# button do; it can never change what the entrypoint already decided.
#
# CONTRACTS
#   A. the shared facts live in internal/tsstate and BOTH features import them
#   B. /admin/tailscale can no longer look "enabled" with a dead daemon: it carries the
#      boot-skip, the TUN device and the daemon's own last failure, and renders a banner
#   C. /admin/exit-nodes appends the same explanation to its reason
#   D. a repeated discovery failure is logged AND audited at most once an hour
#   E. i18n RU+EN pairs
#   F. tests exist and pass; this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B318: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

TS=internal/tsstate/tsstate.go
TSADMIN=internal/feature/admin/tailscale.go
TPL=internal/handlers/templates/admin/tailscale.html
EXIT=internal/feature/exit_rules/relay_transport_tailnet_b310.go
MAIN=cmd/skygate/main.go
I18N=internal/i18n/catalog_tailscale.go

hdr "B318 — the Tailscale state must read the same on both pages"

# --- A: the shared facts ------------------------------------------------------------
if [ -f "$TS" ]; then
  ok "A1: internal/tsstate exists (both features can import it without a cycle)"
else
  bad "A1: internal/tsstate is missing"
fi
if grep -q 'EnvKey = "SKYGATE_TS_AUTHKEY_FILE"' "$TS" && grep -q 'case "", "/dev/null":' "$TS"; then
  ok "A2: the entrypoint's skip conditions (unset / empty / /dev/null) are encoded once"
else
  bad "A2: the disabled sentinel is not recognised"
fi
if grep -q 'TunDevice = "/dev/net/tun"' "$TS" && grep -q 'func tunPresent() bool' "$TS"; then
  ok "A3: the TUN device is checked (without it tailscaled can never create its interface)"
else
  bad "A3: the TUN device is not checked"
fi
if grep -q 'func lastDaemonError(stateDir string) (string, time.Time)' "$TS" && grep -q 'tailscaled.log1.txt' "$TS"; then
  ok "A4: the daemon's own last failure is read from its log, with its timestamp"
else
  bad "A4: the tailscaled log is not read"
fi
if grep -q 'staleTun := s.TunPresent && mentionsTun(line)' "$TS" \
   && grep -q 'EARLIER container instance' "$TS"; then
  ok "A4b: a failure recorded while the device IS present is attributed to the earlier instance"
else
  bad "A4b: a stale TUN failure would be blamed on the running container"
fi
if grep -q 'func (s State) Explain() string' "$TS" && grep -q 'SKIPS tailscaled at boot' "$TS"; then
  ok "A5: Explain() composes the operator sentence (boot skip first)"
else
  bad "A5: Explain() is missing or silent about the boot skip"
fi
if grep -q '"skygate/internal/tsstate"' "$TSADMIN" && grep -q '"skygate/internal/tsstate"' "$EXIT"; then
  ok "A6: BOTH pages consume the same facts (one story, two renderings)"
else
  bad "A6: only one of the two pages uses the shared state"
fi

# --- B: /admin/tailscale cannot look enabled with a dead daemon ---------------------
for f in EnvDisabled DSentinel TunPresent DaemonError NotRunningReason; do
  if grep -q "$f" "$TSADMIN"; then
    ok "B.$f: TailscaleState carries $f"
  else
    bad "B.$f: TailscaleState does not carry $f"
  fi
done
if grep -q 'boot := tsstate.Detect(st.StateDir)' "$TSADMIN" \
   && grep -q 'st.NotRunningReason = boot.Explain()' "$TSADMIN"; then
  ok "B6: the state is filled from tsstate on both the error and the not-running paths"
else
  bad "B6: the page does not fill the reason"
fi
if [ "$(grep -c 'st.NotRunningReason = boot.Explain()' "$TSADMIN")" -ge 2 ]; then
  ok "B7: the reason is set both when the status call fails and when it reports stopped"
else
  bad "B7: a stopped daemon can still render without a reason"
fi
if grep -q 'tailscale.not_running_title' "$TPL" && grep -q '.State.NotRunningReason' "$TPL"; then
  ok "B8: the template renders the banner with the reason"
else
  bad "B8: the template still renders the enabled branch silently"
fi
if grep -q 'and (not .State.Running) .State.NotRunningReason' "$TPL"; then
  ok "B9: the banner is conditional on the daemon actually being down"
else
  bad "B9: the banner condition is missing (it would show on a running daemon)"
fi

# --- C: the exit-nodes page tells the same story ------------------------------------
if grep -q 'tsstate.Detect("").Explain()' "$EXIT"; then
  ok "C1: the tailnet reason is extended with the shared explanation"
else
  bad "C1: /admin/exit-nodes still reports only the daemon's socket error"
fi
if grep -q 'B318' "$EXIT"; then
  ok "C2: the cross-page contract is documented where the reason is built"
else
  bad "C2: no note explaining why the two pages share the facts"
fi

# --- D: a repeated discovery failure cannot bury the journal -------------------------
if grep -q 'func discoveryErrorIsNew(err error) bool' "$MAIN" \
   && grep -q 'discoveryErrRepeatAfter = time.Hour' "$MAIN"; then
  ok "D1: the discovery failure throttle exists (once an hour)"
else
  bad "D1: the discovery failure is still logged every tick"
fi
if grep -q 'if discoveryErrorIsNew(err) {' "$MAIN"; then
  ok "D2: the log AND the audit row are behind the throttle"
else
  bad "D2: the error path does not consult the throttle"
fi
if grep -q 'func discoveryErrorCleared()' "$MAIN" \
   && grep -q 'discoveryErrorCleared()' "$MAIN" \
   && awk '/peers, err := cluster.DiscoverNewNodes/{f=1} f&&/discoveryErrorCleared\(\)/{print "found"; exit}' "$MAIN" | grep -q found; then
  ok "D3: a success clears the throttle, so the next failure is reported at once"
else
  bad "D3: the throttle is never cleared"
fi

# --- E: i18n parity ------------------------------------------------------------------
MISSING=""
for k in tailscale.not_running_title tailscale.not_running_help; do
  n=$(grep -c "\"$k\"" "$I18N")
  if [ "$n" -lt 2 ]; then MISSING="$MISSING $k($n)"; fi
done
if [ -z "$MISSING" ]; then
  ok "E1: every B318 key is defined in BOTH catalogues (rule 10)"
else
  bad "E1: keys missing from one side (count in parentheses):$MISSING"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/i18n/ -run TestCatalogsParity -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "E2: the i18n parity test passes"
  else
    bad "E2: i18n parity failed:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "E2: go not on PATH — run the i18n parity test on the VM"
fi

# --- F: tests + git ------------------------------------------------------------------
if [ -f internal/tsstate/tsstate_b318_test.go ]; then
  ok "F1: internal/tsstate/tsstate_b318_test.go exists"
else
  bad "F1: the tsstate tests are missing"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/tsstate/ -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "F2: the tsstate tests pass"
  else
    bad "F2: the tsstate tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go vet ./internal/tsstate/ ./internal/feature/admin/ ./internal/feature/exit_rules/ ./cmd/skygate/ 2>&1)"
  if [ -z "$OUT" ]; then
    ok "F3: go vet is clean on every package this block touches"
  else
    bad "F3: go vet reported:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "F2/F3: go not on PATH — run the B318 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b318_tailscale_state_truth.sh >/dev/null 2>&1; then
  ok "F4: scripts/check_b318_tailscale_state_truth.sh is tracked by git"
else
  bad "F4: scripts/check_b318_tailscale_state_truth.sh is NOT tracked (AGENTS trap #11)"
fi

printf '\n\033[1mB318 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
