#!/usr/bin/env bash
# check_b366_onboard_keeps_the_operator_connected.sh — B366 (2026-10-09): two
# live follow-ups to the panel-only cluster onboarding, both found by running the
# v1.5.107 flow END TO END on a real second host: wipe → panel block → join →
# heartbeat → Approve.
#
# THE MEASUREMENTS
# ----------------
# A. STEP 1 CUT THE OPERATOR OFF. The rendered step passed `--accept-routes`, and
#    on the reference standby that installed every prefix the relays advertise
#    into routing table 52 (220 entries; 206 of them from `karolina`, because
#    exit rules resolve domains to single IPs). Policy rule 5270 consults table 52
#    BEFORE `main`, and ONE OF THE ACCEPTED PREFIXES WAS THE OPERATOR'S OWN
#    ADDRESS (`95.165.170.190/32`), so the host answered the operator's SSH over
#    `tailscale0` instead of its default gateway:
#
#      ip route get 95.165.170.190
#        → dev tailscale0 table 52 src 100.64.0.20        (accept-routes ON)
#        → via 10.0.0.1 dev net0 src 45.152.198.217       (accept-routes OFF)
#
#    `tcpdump -ni net0 'tcp port 22'` showed the operator's SYNs ARRIVING and no
#    SYN-ACK ever leaving, while a different client completed a full handshake in
#    the same capture — so sshd was healthy and only THAT path was broken.
#    Reproduced twice in each direction; `tailscale set --accept-routes=false`
#    restored the session within seconds. A standby is a SERVER: it needs the
#    tailnet for the cluster API and the heartbeat, not the relays' subnets.
#    (The pre-B365 hand-written runbook said exactly this — "do not add
#    --accept-routes" — and the generated block had lost it.)
#
# B. THE JOIN REGISTERED `unknown`. After the fresh join the live row read
#
#      node-disc-svyatoslava | ready | unknown
#      cluster_audit: node_join {"skygate_version": "unknown", …}
#
#    `skygate join` and `skygate init` built the version they send from the
#    environment variable SKYGATE_VERSION with a literal "unknown" fallback, and
#    NOTHING in the CLI sets that variable — the build identity is injected into
#    package main by ldflags. So B365's promise ("the version of the JOIN must
#    land on the row") replaced one placeholder with another: the operator still
#    could not see which build joined.
#
# WHAT THIS SCRIPT PINS (properties, not strings)
#   A. the RENDERED step 1 does not pass `--accept-routes`, in BOTH branches
#      (with and without a preauth key), and it keeps everything it needs
#      (hostname, login-server, the idempotent client install, nodivert,
#      --accept-dns=false) — so "drop the flag" cannot become "drop the setup".
#      The reason is recorded in the source, the panel note and the runbook.
#   B. the CLI reports the BUILD: one helper owns the rule, `join` and `init`
#      both use it, no `"unknown"` fallback survives for the version, the env
#      override still wins, and the string matches what /healthz displays.
#   C. both catalogues carry the explanation (RU + EN), and the runbook's step
#      names the measurement instead of a bare prohibition.
#   D. the behavioural contracts RUN when a `go` binary is available.
#   E. tracked by git (AGENTS trap #11), registered, and the index's SHAPE: no
#      bullet carries a second one (a swallowed anchor), every bullet starts its
#      line, the bullet count may only RISE, and the current window
#      (B360..B366) is present as its own line each. Writing THIS block produced
#      the class live — an insertion replaced B365's anchor line instead of
#      re-emitting it, the index still counted the same number of bullets and
#      every `grep '\*\*B365\*\*'` contract kept passing — and the same scan then
#      found B336/B337/B338 glued into ONE line and B363/B364 missing entirely.
#
# SKIP (never FAIL) anything that needs live state, a network, or a tool this
# host does not have (AGENTS rule 1).
#
# Usage:  bash scripts/check_b366_onboard_keeps_the_operator_connected.sh
# Exit:   0 = contracts hold (a SKIP is not a failure), 1 = regression
set -uo pipefail
cd "$(dirname "$0")/.."

SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

ONBOARD=internal/feature/admin/cluster_onboard_b342.go
ONBOARDTEST=internal/feature/admin/cluster_onboard_b366_test.go
CLI=cmd/skygate/cluster.go
INIT=cmd/skygate/init.go
SELFVER=cmd/skygate/self_version.go
SELFTEST=cmd/skygate/self_version_b366_test.go
MAIN=cmd/skygate/main.go
I18N=internal/i18n/catalog_admin.go
DOC=docs/operations.md
SELF=scripts/check_b366_onboard_keeps_the_operator_connected.sh

for f in "$ONBOARD" "$ONBOARDTEST" "$CLI" "$INIT" "$SELFVER" "$SELFTEST" "$MAIN" "$I18N" "$DOC"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B366 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# fn_body <file> <func-name-prefix> — print a Go function body, so the assertions
# below are made against the COMMAND the step renders, not against a file that
# merely mentions the word somewhere (the fix's own comment quotes the old
# command, so a whole-file grep would fire on the documentation).
fn_body() {
  awk -v fn="$2" 'index($0, fn) == 1 && /func / {p=1} p {print} p && /^}/ {exit}' "$1"
}

# =====================================================================
hdr "A. the tailnet step must not capture the operator's own management path"

# Read the EMITTED command: clusterOnboardSteps() builds it, so the assertions
# run on the builder's body minus its comment lines (comments quote the defect).
STEP1="$(fn_body "$ONBOARD" "func clusterOnboardSteps(")"
STEP1_CODE="$(printf '%s\n' "$STEP1" | sed 's://.*::')"
if [ -z "$STEP1" ]; then
  bad "A1: clusterOnboardSteps() is gone — there is no block to inspect"
elif [ -n "$(printf '%s\n' "$STEP1_CODE" | grep -F -- '--accept-routes')" ]; then
  bad "A1: the rendered tailnet step still passes --accept-routes — measured live: it installed the operator's own /32 into table 52 and every SSH session died (SYNs in, no SYN-ACK out)"
else
  ok "A1: the rendered step no longer accepts the relays' routes (the live lock-out)"
fi

if [ -z "$STEP1" ]; then
  bad "A2: no command to check"
else
  MISS=""
  for want in 'hostnamectl set-hostname' 'tailscale up' '--login-server=' '--hostname=' '--netfilter-mode=nodivert' '--accept-dns=false' 'command -v tailscale >/dev/null ||'; do
    printf '%s\n' "$STEP1_CODE" | grep -qF -- "$want" || MISS="${MISS}${want} "
  done
  if [ -z "$MISS" ]; then
    ok "A2: the step keeps the hostname, the login server, the idempotent client install, nodivert and --accept-dns=false"
  else
    bad "A2: removing the routes flag also removed required setup: ${MISS}"
  fi
fi

# A3 — BOTH branches. A fix applied to only the authkey branch (or only the
# keyless one) would pass A1 while leaving half the deployments broken.
BRANCHES="$(printf '%s\n' "$STEP1_CODE" | grep -c 'tailscale up' || true)"
if [ "$BRANCHES" -ge 2 ]; then
  BAD_BRANCH="$(printf '%s\n' "$STEP1_CODE" | grep 'tailscale up' | grep -c -- '--accept-routes' || true)"
  if [ "$BAD_BRANCH" -eq 0 ]; then
    ok "A3: neither the authkey branch nor the keyless branch passes --accept-routes ($BRANCHES invocations)"
  else
    bad "A3: $BAD_BRANCH of $BRANCHES tailscale invocations still accept routes"
  fi
else
  bad "A3: expected two tailscale up invocations (authkey + keyless), found $BRANCHES"
fi

# A4 — the reason must be recorded where the next reader looks: at the code that
# renders the command.
if printf '%s\n' "$STEP1" | grep -qi 'accept-routes' \
   && [ -n "$(printf '%s\n' "$STEP1" | grep -E 'route|table 52|SYN' | head -1)" ]; then
  ok "A4: the source records WHY the flag is gone (the measured routing-table capture), so it cannot be re-added as an 'obvious' improvement"
else
  bad "A4: the flag was removed without recording the measurement — the next author will add it back"
fi

# =====================================================================
hdr "B. a join must register the BUILD, not the word unknown"

if [ -f "$SELFVER" ] && grep -q 'func selfVersion(' "$SELFVER"; then
  ok "B1: selfVersion() exists as the single owner of the reported version"
else
  bad "B1: selfVersion() is gone — the CLI would have to guess its own version again"
fi

for f in "$CLI" "$INIT"; do
  base="$(basename "$f")"
  # The version assignment must go through the helper...
  if grep -q 'skygateVersion := selfVersion()' "$f"; then
    ok "B2: $base takes its version from selfVersion()"
  else
    bad "B2: $base does not use selfVersion()"
  fi
  # ...and the old env-with-unknown-fallback must be GONE: that is the defect.
  if [ -n "$(grep -n 'SKYGATE_VERSION' "$f")" ]; then
    bad "B2b: $base still reads SKYGATE_VERSION directly ($(grep -n 'SKYGATE_VERSION' "$f" | head -1)) — the override belongs in selfVersion(), where the fallback cannot be forgotten"
  else
    ok "B2b: $base no longer reads SKYGATE_VERSION with a literal fallback"
  fi
done

if [ -z "$(grep -n '"unknown"' "$CLI" "$INIT" 2>/dev/null)" ]; then
  ok "B3: no \"unknown\" version fallback survives in the join/init path"
else
  bad "B3: a literal \"unknown\" version fallback is back: $(grep -n '"unknown"' "$CLI" "$INIT" | head -2)"
fi

# B4 — the env override is still honoured (documented escape hatch).
if grep -q 'SKYGATE_VERSION' "$SELFVER" && grep -q 'TrimSpace' "$SELFVER"; then
  ok "B4: selfVersion() keeps the SKYGATE_VERSION override (trimmed) ahead of the build identity"
else
  bad "B4: the SKYGATE_VERSION override is gone — a packager that injects the identity differently is overruled"
fi

# B5 — ONE rule for both surfaces: the panel's displayed build and the version a
# join registers must come from the same helper.
if grep -q 'app.BuildVersion = buildVersionString()' "$MAIN"; then
  ok "B5: /healthz and the panel footer use buildVersionString(), the same rule the join reports"
else
  bad "B5: main.go builds the displayed version by hand — it can drift from the registered one"
fi

# B6 — the shell-side property: the join request carries the computed value (a
# grep cannot run Go, so this pins the wiring; the BEHAVIOUR is pinned in
# cmd/skygate/self_version_b366_test.go and run in section D).
if grep -q 'SkygateVersion: *skygateVersion' "$CLI" \
   || grep -q 'SkygateVersion: *skygateVersion,' "$CLI"; then
  ok "B6: the join request sends the computed version (not a re-read env var)"
else
  bad "B6: the join request no longer sends the computed version"
fi

# =====================================================================
hdr "C. the page and the runbook explain it, in both languages"

NOTE_HITS="$(grep -c '"cluster.onboard_note_tailnet"' "$I18N" || true)"
if [ "$NOTE_HITS" -eq 2 ]; then
  ok "C1: cluster.onboard_note_tailnet is defined in BOTH catalogues"
else
  bad "C1: the tailnet note is not defined twice (RU+EN): $NOTE_HITS"
fi

NOTE_LINES="$(grep -h '"cluster.onboard_note_tailnet"' "$I18N")"
if [ -n "$(printf '%s\n' "$NOTE_LINES" | grep -F 'accept-routes')" ]; then
  ok "C2: the panel note names the flag the block deliberately does not pass"
else
  bad "C2: the panel note is silent about --accept-routes — the operator cannot know why it is missing"
fi
if [ -n "$(printf '%s\n' "$NOTE_LINES" | grep -E 'таблиц|table 52|routing table' | head -1)" ]; then
  ok "C3: the note says WHERE the flag does its damage (routing table 52), not just 'do not use it'"
else
  bad "C3: the note forbids the flag without naming the mechanism"
fi

if grep -q 'accept-routes' "$DOC" && [ -n "$(grep -n 'tcpdump\|SYN-ACK\|no SYN-ACK' "$DOC" | head -1)" ]; then
  ok "C4: the runbook carries the measurement (tcpdump / no SYN-ACK), not a bare prohibition"
else
  bad "C4: docs/operations.md does not record the accept-routes measurement"
fi

# =====================================================================
hdr "D. the behavioural contracts actually run"

GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
  skip "D1: no go binary in PATH — the B366 behavioural contracts were not run"
  skip "D2: no go binary in PATH — the tailnet-step render test was not run"
else
  if out="$("$GOBIN" test ./cmd/skygate/... -run 'B366' -count=1 2>&1)"; then
    ok "D1: the build-identity contracts pass ($(printf '%s\n' "$out" | grep -c '^ok'))"
  else
    bad "D1: go test ./cmd/skygate/... -run B366 failed:
$(printf '%s\n' "$out" | tail -20)"
  fi
  if out="$("$GOBIN" test ./internal/feature/admin/... -run 'B366' -count=1 2>&1)"; then
    ok "D2: the rendered-step contracts pass ($(printf '%s\n' "$out" | grep -c '^ok'))"
  else
    bad "D2: go test ./internal/feature/admin/... -run B366 failed:
$(printf '%s\n' "$out" | tail -20)"
  fi
fi

# =====================================================================
hdr "E. tracked by git (AGENTS trap #11), registered, indexed"

UNTRACKED=""
for f in "$SELF" "$ONBOARDTEST" "$SELFVER" "$SELFTEST"; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || UNTRACKED="${UNTRACKED}${f} "
done
if [ -z "$UNTRACKED" ]; then
  ok "E1: every new file of this block is tracked by git"
else
  skip "E1: not yet tracked by git (the lead commits this block; verify .gitignore does not eat them): ${UNTRACKED}"
fi

if grep -q 'run_check "B366"' scripts/verify_pre_deploy.sh; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered — the contract would never run"
fi

if grep -q '^- \*\*B366\*\*' AGENTS.md; then
  ok "E3: recorded in the AGENTS.md block index (as its own line)"
else
  bad "E3: AGENTS.md has no B366 bullet at line start — a substring match is not an index entry"
fi

# E4/E5 — the index's SHAPE. Measured 2026-10-09, while writing this very block:
# an insertion that replaced the anchor line instead of re-emitting it GLUED the
# new bullet onto the previous block's text, so B365 stopped being a bullet while
# every `grep -q '\*\*B365\*\*'` contract still passed (L-64(3), paid for again).
# A detector that counts the bullet PREFIX per line catches the class; the same
# scan found three blocks (B336/B337/B338) already glued into one line in the
# shipped index, which is why this contract exists at all.
GLUED="$(awk '/- \*\*B[0-9]/ { n = gsub(/- \*\*B[0-9]/, "&"); if (n > 1) print FILENAME": line "NR" carries "n" bullets" }' AGENTS.md)"
if [ -z "$GLUED" ]; then
  ok "E4: no AGENTS.md index line carries more than one block bullet (no swallowed anchor)"
else
  bad "E4: the AGENTS.md index has a glued bullet — a block lost its line and every substring contract would still pass:
$GLUED"
fi

MIDLINE="$(grep -n -- '- \*\*B[0-9]' AGENTS.md | grep -v '^[0-9]*:- ' | head -5)"
if [ -z "$MIDLINE" ]; then
  ok "E5: every block bullet starts its own line"
else
  bad "E5: a block bullet does not start its line:
$MIDLINE"
fi

# E6 — the count RATCHET. The failure mode E4/E5 cannot see is a bullet
# replaced by a new one (one in, one out): the line count does not move, the new
# block looks indexed and the OLD one silently stops being an entry. Measured
# 2026-10-09: B366 was inserted by replacing B365's anchor line, so the index
# still had 388 bullets and every substring contract still passed. The count may
# only RISE — raise the frozen number whenever you add a block.
BULLETS_N="$(grep -c '^- \*\*[A-Za-z0-9._-]*\*\*' AGENTS.md || true)"
FROZEN_BULLETS=390
if [ "$BULLETS_N" -ge "$FROZEN_BULLETS" ]; then
  ok "E6: the index carries $BULLETS_N block bullets (frozen minimum $FROZEN_BULLETS — it may only rise)"
else
  bad "E6: the index dropped to $BULLETS_N bullets (minimum $FROZEN_BULLETS) — a block lost its entry; an insertion must ADD a line, never replace the anchor (L-64(3))"
fi

# E7 — the live window, named. The blocks of the current release cycle must each
# be their own bullet: this is the row that catches a swallowed anchor for the
# blocks a session is actually touching.
WINDOW_MISS=""
for id in B360 B361 B362 B363 B364 B365 B366; do
  grep -q "^- \*\*${id}\*\*" AGENTS.md || WINDOW_MISS="${WINDOW_MISS}${id} "
done
if [ -z "$WINDOW_MISS" ]; then
  ok "E7: every block of the current window (B360..B366) is its own index bullet"
else
  bad "E7: these blocks have no bullet at line start: ${WINDOW_MISS}"
fi

printf '\n\033[1mB366 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
