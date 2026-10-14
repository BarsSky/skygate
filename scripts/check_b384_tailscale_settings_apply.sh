#!/usr/bin/env bash
# check_b384_tailscale_settings_apply.sh
#
# 2026-10-14 (B384, v1.5.123) — "the client's settings must actually APPLY, and the node
# must be able to say whether they did".
#
# THE LIVE DEFECT (reference host, v1.5.122)
# ------------------------------------------
# 	tailscale-autostart: boot: start FAILED: tailscale up: exit status 1
# 	  (out=Error: changing settings via 'tailscale up' requires mentioning all
# 	   non-default flags. To proceed, either re-run your command with --reset or
# 	   use the command below to explicitly mention the current va…)
#
# …on EVERY boot and EVERY 5-minute tick, forever. The node itself was healthy
# (`skygate-host`, `tag:dev-infra-skygate-host`, BackendState=Running), so nothing looked
# broken — but NOTHING the invocation asked for was ever applied, because `tailscale up`
# refuses to change ANY setting unless the call lists EVERY persisted non-default pref,
# and this container's state carries `NoStatefulFiltering=true` + `NetfilterMode=2`.
# An operator editing the hostname or the tags in the panel would therefore wait forever,
# and the journal held a permanent failure line that hid real ones.
#
# The entrypoint carried the same bug PLUS the retired name: its advertise-tags fallback
# was the literal `tag:dev-infra-skygate-host-1,tag:private` — the `skygate-host-<n>`
# shape B320/B321 exist to remove, which names a node that does not exist while the real
# node stays untagged.
#
# WHAT B384 SHIPS (both halves, one behaviour)
#   * `tailscale set` FIRST whenever the daemon answers — upstream documents it as "does
#     not require the complete set of desired settings", the one property `up` cannot
#     offer on a drifted state;
#   * `up` for the first join and as the fallback, `up --reset` as the drift remedy
#     upstream itself prints (tried ONCE, with a named log line);
#   * the advertise-tags default is DERIVED from the canonical hostname
#     (`AdvertiseTagForHostname` / `skygate_tc_advertise_tag`), never the `-1` literal,
#     and an explicit SKYGATE_TS_ADVERTISE_TAGS still wins verbatim;
#   * after applying, the intent is VERIFIED against the daemon's own view
#     (`.Self.HostName` + `.Self.Tags`) and reported in ONE named line —
#     `tailscale-apply: OK …` or a line that names BOTH sides.
#
# THE TRAP THIS CONTRACT EXISTS FOR (found by the Go unit test while writing it)
# -----------------------------------------------------------------------------
# The live message is WRAPPED across lines, so `grep -q 'requires mentioning all
# non-default flags'` never matches it: every classifier must collapse whitespace first.
# Both halves do, and D3/D4 pin the wrapped text.
#
# CONTRACTS
#   A. the retired placeholder is gone from the CODE (history comments may keep it)
#   B. the ladder exists in both halves, in the right order, `--reset` only for drift
#   C. both halves VERIFY and report one named line
#   D. behavioural: the extracted shell helpers (normalisation, derivation, the WRAPPED
#      error classifier, and a real verify() against a stubbed `tailscale`)
#   E. the Go half: the pure helpers + the unit tests exist, `tailscaleUp` delegates
#   F. registered, tracked, documented
#
# Scratch files live under mktemp -d (trap #13); nothing here touches the network or a
# real daemon.

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B384: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

export LC_ALL=C

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

EP=entrypoint.sh
GO_APPLY=internal/feature/admin/tailscale_apply_b384.go
GO_RUNTIME=internal/feature/admin/tailscale_runtime.go
GO_TEST=internal/feature/admin/tailscale_apply_b384_test.go
CATALOG=scripts/verify_pre_deploy.sh
SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b384.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

hdr "B384 (v1.5.123) — the Tailscale settings must apply, and the node must say so"

# ---------------------------------------------------------------------------
# A. the retired placeholder is gone from the CODE
# ---------------------------------------------------------------------------
# Comments are allowed to keep the history (and do: they explain what was removed).
ep_code_hits="$(grep -n -- '-skygate-host-1\|tag:dev-infra-skygate-host-1' "$EP" 2>/dev/null | grep -v ':[[:space:]]*#' || true)"
if [ -z "$ep_code_hits" ]; then
  ok "A1: no CODE line in $EP carries the retired skygate-host-1 placeholder (only the history comments do)"
else
  bad "A1: $EP still uses the placeholder in code: $(printf '%s' "$ep_code_hits" | head -2 | tr '\n' ' ')"
fi
if grep -q 'SKYGATE_TS_HOSTNAME:-skygate-host}' "$EP"; then
  ok "A2: the hostname default is the canonical skygate-host"
else
  bad "A2: the hostname default is not the canonical name"
fi
if grep -q 'skygate_tc_advertise_tag' "$EP" && grep -q 'skygate_tc_normalize_hostname' "$EP"; then
  ok "A3: the shell half derives the tag from the normalised hostname"
else
  bad "A3: the shell half does not derive the tag (the -1 literal would come back)"
fi
if grep -q 'func AdvertiseTagForHostname(hostname string) string' "$GO_APPLY" \
   && grep -q 'NormalizeTailscaleHostname(hostname)' "$GO_APPLY"; then
  ok "A4: the Go half derives the tag through the ONE normaliser (the placeholder cannot come back out)"
else
  bad "A4: the Go derivation does not canonicalise the hostname"
fi
if ! grep -n -- 'tag:dev-infra-skygate-host-1' "$GO_APPLY" 2>/dev/null | grep -v ':[[:space:]]*//' | grep -q .; then
  ok "A5: the Go apply path contains no literal -1 tag (only the history comment does)"
else
  bad "A5: $GO_APPLY still hardcodes the retired tag in code"
fi

# ---------------------------------------------------------------------------
# B. the ladder, in both halves, in the right order
# ---------------------------------------------------------------------------
b_set="$(grep -n 'tailscale set --hostname=' "$EP" | head -1 | cut -d: -f1)"
# The FIRST `timeout 30 tailscale up` is the plain join/fallback; the `--reset` one is
# matched separately and must come later.
b_up="$(grep -n 'timeout 30 tailscale up' "$EP" | head -1 | cut -d: -f1)"
b_reset="$(grep -n 'tailscale up --reset' "$EP" | head -1 | cut -d: -f1)"
b_drift="$(grep -n 'skygate_tc_error_kind "$TC_OUT"' "$EP" | head -1 | cut -d: -f1)"
if [ -n "$b_set" ] && [ -n "$b_up" ] && [ -n "$b_reset" ] && [ "$b_set" -lt "$b_up" ] && [ "$b_up" -lt "$b_reset" ]; then
  ok "B1: shell ladder is set ($b_set) → up ($b_up) → up --reset ($b_reset)"
else
  bad "B1: shell ladder order is wrong (set=$b_set up=$b_up reset=$b_reset)"
fi
if [ -n "$b_drift" ] && [ "$b_drift" -lt "$b_reset" ]; then
  ok "B2: --reset is reachable only through the drift verdict ($b_drift < $b_reset), not as a default"
else
  bad "B2: --reset is not gated on the drift verdict (drift=$b_drift reset=$b_reset)"
fi
gs_set="$(grep -n '"set", "--hostname=" + host' "$GO_APPLY" | head -1 | cut -d: -f1)"
gs_up="$(grep -n '"up", "--login-server=" + loginServer' "$GO_APPLY" | head -1 | cut -d: -f1)"
gs_reset="$(grep -n '"up", "--reset"' "$GO_APPLY" | head -1 | cut -d: -f1)"
if [ -n "$gs_set" ] && [ -n "$gs_up" ] && [ -n "$gs_reset" ] && [ "$gs_set" -lt "$gs_up" ] && [ "$gs_up" -lt "$gs_reset" ]; then
  ok "B3: Go ladder is set ($gs_set) → up ($gs_up) → up --reset ($gs_reset)"
else
  bad "B3: Go ladder order is wrong (set=$gs_set up=$gs_up reset=$gs_reset)"
fi
if grep -q 'if tailscaleDaemonAnswers() {' "$GO_APPLY" && grep -q 'tailscalePrefDriftKind(out) == "drift"' "$GO_APPLY"; then
  ok "B4: the Go half gates 'set' on a live daemon and '--reset' on the drift verdict"
else
  bad "B4: the Go half does not gate set/reset the way the shell half does"
fi
if grep -q 'return s.applyTailscaleSettings(key)' "$GO_RUNTIME"; then
  ok "B5: tailscaleUp delegates to the ladder, so every existing caller gets the drift-proof path"
else
  bad "B5: tailscaleUp does not delegate — the old up-only body is still there"
fi

# ---------------------------------------------------------------------------
# C. both halves VERIFY and report
# ---------------------------------------------------------------------------
if grep -q 'tailscale-apply: \$(skygate_tc_verify' "$EP"; then
  ok "C1: the entrypoint reports the verification as a named 'tailscale-apply:' line"
else
  bad "C1: the entrypoint does not report the verification"
fi
verify_calls="$(grep -c 'verifyTailscaleSettings(host, tags)' "$GO_APPLY")"
if [ "$verify_calls" -ge 4 ]; then
  ok "C2: every Go exit path verifies the intent ($verify_calls call sites)"
else
  bad "C2: only $verify_calls Go path(s) verify — a failed apply could pass unreported"
fi
if grep -q 'tailscale-apply: OK hostname=' "$GO_APPLY" && grep -q 'SETTINGS NOT APPLIED' "$GO_APPLY"; then
  ok "C3: the Go half names both sides when the settings did not land"
else
  bad "C3: the Go half does not report a mismatch explicitly"
fi

# ---------------------------------------------------------------------------
# D. behavioural: the shell helpers, extracted from the entrypoint
# ---------------------------------------------------------------------------
HELPERS="$SKY_TMP/b384_helpers.sh"
if sed -n '/^# >>> B384 helpers (begin)/,/^# <<< B384 helpers (end)/p' "$EP" > "$HELPERS" 2>/dev/null \
   && [ -s "$HELPERS" ]; then
  ok "D1: the B384 helper block is extractable from $EP (the contract tests the code the container runs)"
  # shellcheck disable=SC1090
  . "$HELPERS"
else
  bad "D1: the B384 helper block (markers '# >>> B384 helpers (begin)' / '# <<< B384 helpers (end)') is missing"
fi

if declare -F skygate_tc_normalize_hostname >/dev/null 2>&1; then
  norm_fail=""
  check_norm() { # in want
    got="$(skygate_tc_normalize_hostname "$1")"
    [ "$got" = "$2" ] || norm_fail="${norm_fail} [$1→$got want $2]"
  }
  check_norm "skygate-host-1" "skygate-host"
  check_norm "skygate-host-1-1" "skygate-host"
  check_norm "skygate-host" "skygate-host"
  check_norm "SKYGATE-HOST-1" "skygate-host"
  check_norm "aro" "aro"
  check_norm "" ""
  if [ -z "$norm_fail" ]; then
    ok "D2: the legacy placeholder shape normalises to the canonical name (and a real name is left alone)"
  else
    bad "D2: normalisation is wrong:$norm_fail"
  fi

  if [ "$(skygate_tc_advertise_tag skygate-host)" = "tag:dev-infra-skygate-host" ] \
     && [ "$(skygate_tc_advertise_tag SKYGATE-HOST-1)" = "tag:dev-infra-skygate-host" ] \
     && [ -z "$(skygate_tc_advertise_tag "")" ]; then
    ok "D3: the derived tag is tag:dev-infra-<canonical name> and can never be the retired -1 shape"
  else
    bad "D3: the derived tag is wrong: [$(skygate_tc_advertise_tag skygate-host)] [$(skygate_tc_advertise_tag SKYGATE-HOST-1)]"
  fi

  # The WRAPPED live text — the regression this contract exists for.
  wrapped="Error: changing settings via 'tailscale up' requires mentioning all
non-default flags. To proceed, either re-run your command with --reset or
use the command below to explicitly mention the current values:
  tailscale up --advertise-exit-node=false --netfilter-mode=on"
  k1="$(skygate_tc_error_kind "$wrapped")"
  k2="$(skygate_tc_error_kind "requested tags [tag:dev-infra-x] are invalid or not permitted")"
  k3="$(skygate_tc_error_kind "failed to connect to local tailscaled")"
  k4="$(skygate_tc_error_kind "context deadline exceeded")"
  if [ "$k1" = "drift" ] && [ "$k2" = "tags-not-permitted" ] && [ "$k3" = "" ] && [ "$k4" = "timeout" ]; then
    ok "D4: the classifier reads the WRAPPED live drift text as 'drift' (a plain substring search never matches it) and does not guess at anything else"
  else
    bad "D4: classifier verdicts wrong: drift=[$k1] tags=[$k2] other=[$k3] timeout=[$k4]"
  fi
fi

# D5/D6 — verify() against a stubbed tailscale (real python3, no real daemon).
if declare -F skygate_tc_verify >/dev/null 2>&1 && command -v python3 >/dev/null 2>&1; then
  stub="$SKY_TMP/bin"; mkdir -p "$stub"
  cat > "$stub/tailscale" <<'STUBEOF'
#!/usr/bin/env bash
# Only `status --json` is exercised here. NOTE: do NOT write the default as
# ${VAR:-{"Self":…}} — bash's parameter expansion counts braces, so the closing ones
# leak into the output and the JSON becomes invalid ("Extra data: line 1 column …").
if [ "${1:-}" = "status" ]; then
  if [ -n "${STUB_STATUS_JSON:-}" ]; then
    printf '%s\n' "$STUB_STATUS_JSON"
  else
    printf '%s\n' '{"Self":{"HostName":"stub-host","Tags":[]}}'
  fi
  exit 0
fi
exit 0
STUBEOF
  chmod +x "$stub/tailscale" 2>/dev/null || true

  match_json='{"Self":{"HostName":"skygate-host","Tags":["tag:dev-infra-skygate-host"]}}'
  mismatch_json='{"Self":{"HostName":"skygate-host","Tags":["tag:dev-infra-skygate-host","tag:private"]}}'

  d5_out="$(PATH="$stub:$PATH" STUB_STATUS_JSON="$match_json" skygate_tc_verify skygate-host "tag:dev-infra-skygate-host" 2>&1)"; d5_rc=$?
  if [ "$d5_rc" -eq 0 ] && printf '%s' "$d5_out" | grep -q '^OK hostname=skygate-host'; then
    ok "D5: verify() says OK when the daemon's own hostname and tag set match the intent"
  else
    bad "D5: verify() did not confirm a matching node (rc=$d5_rc out=[$d5_out])"
  fi

  d6_out="$(PATH="$stub:$PATH" STUB_STATUS_JSON="$mismatch_json" skygate_tc_verify skygate-host "tag:dev-infra-skygate-host" 2>&1)"; d6_rc=$?
  if [ "$d6_rc" -ne 0 ] && printf '%s' "$d6_out" | grep -q 'SETTINGS NOT APPLIED'; then
    ok "D6: verify() fails LOUDLY and names both sides when the daemon carries an extra tag"
  else
    bad "D6: verify() accepted a node that does not match the intent (rc=$d6_rc out=[$d6_out])"
  fi
else
  skip "D5/D6: python3 (or the verify helper) is unavailable — the stub-based verification is skipped"
fi

# ---------------------------------------------------------------------------
# E. the Go half
# ---------------------------------------------------------------------------
for fn in 'func AdvertiseTagForHostname(hostname string) string' 'func tailscalePrefDriftKind(out string) string' \
          'func verifyTailscaleSettings(wantHost string, wantTags []string) bool' \
          'func (s *Service) applyTailscaleSettings(key string) (string, error)'; do
  if grep -qF "$fn" "$GO_APPLY"; then
    ok "E1: $GO_APPLY defines ${fn%%(*}"
  else
    bad "E1: $GO_APPLY does not define ${fn%%(*}"
  fi
done
if grep -q 'strings.Join(strings.Fields(strings.ToLower(out)), " ")' "$GO_APPLY"; then
  ok "E2: the Go classifier collapses whitespace before matching (the wrapped live text)"
else
  bad "E2: the Go classifier matches raw text — the wrapped live message would be missed"
fi
if [ -f "$GO_TEST" ] && grep -q 'TestTailscalePrefDriftKind_B384' "$GO_TEST" \
   && grep -q 'TestAdvertiseTagForHostname_B384' "$GO_TEST"; then
  ok "E3: the pure halves are unit-tested, including the real wrapped drift text"
else
  bad "E3: $GO_TEST is missing or does not cover the classifier/derivation"
fi

# ---------------------------------------------------------------------------
# F. registered, tracked, documented
# ---------------------------------------------------------------------------
for f in "$GO_APPLY" "$GO_TEST" scripts/check_b384_tailscale_settings_apply.sh; do
  if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then
    ok "F1: $f is tracked by git (trap #11)"
  else
    bad "F1: $f is NOT tracked by git"
  fi
done
if grep -q 'check_b384_tailscale_settings_apply.sh' "$CATALOG"; then
  ok "F2: registered in $CATALOG"
else
  bad "F2: not registered in $CATALOG (rule 2)"
fi
if grep -q 'B384' RELEASE-NOTES.md 2>/dev/null; then
  ok "F3: RELEASE-NOTES.md carries the B384 entry"
else
  bad "F3: RELEASE-NOTES.md has no B384 entry"
fi
if grep -q 'B384' docs/ROADMAP.md 2>/dev/null; then
  ok "F4: docs/ROADMAP.md records B384"
else
  bad "F4: docs/ROADMAP.md does not mention B384"
fi
if grep -q 'SKYGATE_TS_ADVERTISE_TAGS' docs/UPDATE.md 2>/dev/null; then
  ok "F5: docs/UPDATE.md documents the operator knob"
else
  bad "F5: docs/UPDATE.md does not document SKYGATE_TS_ADVERTISE_TAGS"
fi

printf '\n\033[1mB384 summary: %d passed, %d failed, %d skipped\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
