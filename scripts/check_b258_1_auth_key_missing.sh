#!/usr/bin/env bash
# check_b258_1_auth_key_missing.sh
#
# 2026-09-17 (B258.1) — regression check for the third
# "auth-key missing" visual state on /admin/tailscale.
#
# The B258 design modelled only two states (disabled-by-config
# vs enabled) and collapsed "configured regular file path but
# file missing" into the "enabled" branch. Live operator
# confusion (2026-09-17 10:25 MSK): status said "stopped" but
# the green "Disable Tailscale in container" button was active,
# AND clicking Start produced
#   "read auth key: open /data/ts/authkey: no such file or directory"
# which the operator couldn't translate into "paste a key here".
#
# B258.1 closes that gap with:
#   - TailscaleState.AuthKeyMissing = !disabled && !set
#     (helper: tailscaleAuthKeyMissingForStart)
#   - template: 3-way if/else if/else (Disabled → Enable btn,
#     Missing → warn banner + paste form + Start disabled,
#     else → Disable btn)
#   - handler: handleTailscaleStart refuses early with an
#     actionable message when the missing state is bypassed
#     (e.g. direct POST in a script).
#
# What this checks:
#   A) State struct has AuthKeyMissing field
#   B) readTailscaleState() populates AuthKeyMissing
#   C) tailscaleAuthKeyMissingForStart() helper exists
#   D) handler pre-flight check calls the helper
#   E) template has 3 branches (Disabled / Missing / Else)
#   F) template's Start button is disabled when Missing
#   G) i18n keys exist in catalog_tailscale.go (RU + EN):
#      missing_title, missing_help, missing_status_unset,
#      missing_start_tooltip
#   H) unit test file exists with the 5-case pattern
#      (PathButNoFile / DevNullIsNotMissing /
#       FileExistsEmpty / FileExistsWithContent /
#       MutuallyExclusiveWithDisabled)
#   I) go vet ./internal/feature/admin/... is clean
#   J) go test ./internal/feature/admin/ -run
#      TestTailscaleAuthKeyMissingForStart -count=1 passes

set -euo pipefail
# Disable bash pathname expansion so patterns like "/data/*" in
# grep invocations don't get glob-expanded into the list of
# matching files (which is what bit check_b254 — see that
# script's comment block for the gory details).
set -f

ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
fail(){ printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# Find Go (git-bash / MSYS2 subshell doesn't inherit Windows PATH).
if ! command -v go >/dev/null 2>&1; then
  for cand in '/mnt/c/Program Files/Go/bin' '/c/Program Files/Go/bin'; do
    if [ -f "$cand/go.exe" ] && "$cand/go.exe" version >/dev/null 2>&1; then
      export PATH="$cand:$PATH"
      break
    fi
  done
fi

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

TAILSCALE_GO="internal/feature/admin/tailscale.go"
TAILSCALE_HTML="internal/handlers/templates/admin/tailscale.html"
CATALOG="internal/i18n/catalog_tailscale.go"
TEST_FILE="internal/feature/admin/tailscale_b258_1_test.go"

hdr "B258.1 — Tailscale UI third visual state (auth-key missing)"

# --- A: State struct has AuthKeyMissing field ---
if grep -q '^\s*AuthKeyMissing\s\+bool' "$TAILSCALE_GO"; then
  ok "A: TailscaleState has AuthKeyMissing bool field"
else
  fail "A: TailscaleState missing AuthKeyMissing field — template can't branch on missing state"
fi

# --- B: readTailscaleState populates AuthKeyMissing ---
if grep -q 'st\.AuthKeyMissing\s*=' "$TAILSCALE_GO"; then
  ok "B: readTailscaleState computes AuthKeyMissing"
else
  fail "B: readTailscaleState doesn't compute AuthKeyMissing — field is dead in the struct"
fi

# --- C: tailscaleAuthKeyMissingForStart helper exists ---
if grep -q 'func.*tailscaleAuthKeyMissingForStart' "$TAILSCALE_GO"; then
  ok "C: tailscaleAuthKeyMissingForStart helper exists"
else
  fail "C: tailscaleAuthKeyMissingForStart helper missing — handler can't do pre-flight check"
fi

# --- D: handler pre-flight check calls the helper ---
if grep -q 'tailscaleAuthKeyMissingForStart' "$TAILSCALE_GO"; then
  n=$(grep -c 'tailscaleAuthKeyMissingForStart' "$TAILSCALE_GO")
  if [ "$n" -ge 2 ]; then
    ok "D: handleTailscaleStart uses tailscaleAuthKeyMissingForStart ($n references — definition + caller)"
  else
    fail "D: tailscaleAuthKeyMissingForStart only referenced once — handler doesn't call it pre-flight"
  fi
else
  fail "D: helper never called — handleTailscaleStart can't refuse early on missing state"
fi

# --- E: template has 3 branches (Disabled / Missing / Else) ---
if grep -q '\.State\.AuthKeyDisabled}}' "$TAILSCALE_HTML" \
   && grep -q '\.State\.AuthKeyMissing}}' "$TAILSCALE_HTML"; then
  ok "E: template branches on both AuthKeyDisabled AND AuthKeyMissing"
else
  fail "E: template doesn't have 3-way branch (Disabled → Missing → Else) — pre-B258.1 2-way branch still in place"
fi

# --- F: template's Start button is disabled when Missing ---
if grep -q '\.State\.AuthKeyMissing}}disabled' "$TAILSCALE_HTML"; then
  ok "F: Start button disabled when AuthKeyMissing=true"
else
  fail "F: Start button NOT disabled on Missing — clicking it still triggers the raw ENOENT error"
fi

# --- G: i18n keys exist (RU + EN) ---
for k in missing_title missing_help missing_status_unset missing_start_tooltip; do
  n=$(grep -c "\"tailscale\.${k}\"" "$CATALOG" 2>/dev/null || echo 0)
  # Use tr to strip any trailing whitespace from grep -c output.
  n=$(printf '%s' "$n" | tr -dc '0-9')
  n=${n:-0}
  if [ "$n" -ge 2 ]; then
    ok "G: i18n key tailscale.$k present (RU + EN, $n references)"
  else
    fail "G: i18n key tailscale.$k missing or only in one language (n=$n, need >=2)"
  fi
done

# --- H: unit test file exists with the 5-case pattern ---
if [ -f "$TEST_FILE" ]; then
  tests_present=0
  for t in PathButNoFile DevNullIsNotMissing FileExistsEmpty FileExistsWithContent MutuallyExclusiveWithDisabled; do
    if grep -q "TestTailscaleAuthKeyMissingForStart_$t\|TestAuthKeyMissingMutuallyExclusiveWithDisabled" "$TEST_FILE"; then
      tests_present=$((tests_present + 1))
    fi
  done
  if [ "$tests_present" -ge 5 ]; then
    ok "H: unit test file present with the 5-case pattern ($tests_present/5 cases detected)"
  else
    fail "H: only $tests_present/5 B258.1 test cases present — fix the gaps before commit"
  fi
else
  fail "H: $TEST_FILE missing — B258.1 has no regression lock-in"
fi

# --- I: go vet clean ---
if command -v go >/dev/null 2>&1; then
  if go vet ./internal/feature/admin/... >/dev/null 2>&1; then
    ok "I: go vet ./internal/feature/admin/... clean"
  else
    fail "I: go vet ./internal/feature/admin/... reported issues"
  fi
else
  ok "I: go vet skipped (go not reachable in this bash PATH — re-run manually: go vet ./internal/feature/admin/...)"
fi

# --- J: go test the B258.1 unit tests ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/feature/admin/ -run 'TestTailscaleAuthKeyMissingForStart|TestAuthKeyMissingMutuallyExclusive' -count=1 >/dev/null 2>&1; then
    ok "J: go test ./internal/feature/admin/ -run TestTailscaleAuthKeyMissingForStart -count=1 passes"
  else
    fail "J: go test failed — run manually: go test ./internal/feature/admin/ -run TestTailscaleAuthKeyMissingForStart -count=1 -v"
  fi
else
  ok "J: go test skipped (go not reachable in this bash PATH — re-run manually: go test ./internal/feature/admin/ -run TestTailscaleAuthKeyMissingForStart -v)"
fi

# --- K: the "Generate key" control is VISIBLE (operator report 2026-09-19) ---
# The B258.1 warning says "…or click «Сгенерировать ключ»", but the control was
# wrapped in a collapsed <details> whose summary read "Сгенерировать
# автоматически" (13px, muted) — the operator reported "кнопки нет". It is now a
# plain visible button in the auth-key card.
if grep -q 'action" value="generate_key"' "$TAILSCALE_HTML"; then
  ok "K: /admin/tailscale renders the generate_key form"
else
  fail "K: $TAILSCALE_HTML has no action=generate_key form"
fi
if grep -B6 'action" value="generate_key"' "$TAILSCALE_HTML" | grep -q '<details'; then
  fail "K: the generate_key form is still hidden inside a collapsed <details> (the operator cannot see the button)"
else
  ok "K: the generate_key form is not wrapped in <details> (visible button)"
fi
# and it must live in the non-disabled branch (the missing/unset states) of the
# auth-key card: the LAST `{{if .State.AuthKeyDisabled}}` before it, then that
# block's `{{else}}`, must both precede the generate form.
GEN_LINE=$(grep -n 'action" value="generate_key"' "$TAILSCALE_HTML" | head -1 | cut -d: -f1)
DIS_LINE=$(awk -v g="${GEN_LINE:-0}" 'NR<g && /\{\{if \.State\.AuthKeyDisabled\}\}/{l=NR} END{print l+0}' "$TAILSCALE_HTML")
ELSE_LINE=$(awk -v s="${DIS_LINE:-0}" 'NR>s && /^[[:space:]]*\{\{else\}\}/{print NR; exit}' "$TAILSCALE_HTML")
if [ -n "$GEN_LINE" ] && [ -n "$ELSE_LINE" ] && [ "$GEN_LINE" -gt "$ELSE_LINE" ]; then
  ok "K: generate_key renders in the {{else}} branch (i.e. for both unset and missing)"
else
  fail "K: generate_key is not in the non-disabled template branch (gen=$GEN_LINE if=$DIS_LINE else=$ELSE_LINE)"
fi

# --- L: the banner and the button use the SAME wording (RU + EN) ---
for pair in "Сгенерировать ключ:Сгенерировать ключ" "Generate key:Generate key"; do
  phrase="${pair%%:*}"
  ru_hits=$(grep -c "$phrase" "$CATALOG")
  if [ "$ru_hits" -ge 2 ]; then
    ok "L: '$phrase' appears in the banner and on the button ($ru_hits occurrences in the catalog)"
  else
    fail "L: '$phrase' occurs $ru_hits time(s) in $CATALOG — the banner still names a control the page does not label that way"
  fi
done

printf '\n\033[32mB258.1 regression check passed — safe to commit\033[0m\n'
