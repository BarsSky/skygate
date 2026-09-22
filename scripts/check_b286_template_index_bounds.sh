#!/usr/bin/env bash
# check_b286_template_index_bounds.sh
#
# 2026-09-22 (B286) — "an empty slice must not take a page down".
#
# Live on `aro`, after the relay was moved to the infra owner:
#
#	render: template: layout.html:290:2: executing "layout" at : error calling
#	renderBody: template: exit_rules.html:264:35: executing "body-exit_rules"
#	at : error calling index: reflect: slice index out of range
#
# `/my/exit-rules` answered a Go template error instead of the user's rules.
# The template initialised its per-device preferred-exit fallback with
# `{{$pref := index $.DeviceInfos 0}}` — an unguarded index into a slice that is
# legitimately EMPTY (the page had rules, but no device rows to render them
# for). The same unguarded pattern existed in four more templates:
#
#	user/exit_nodes.html    {{index .IPAddresses 0}}      (per node)
#	user/devices.html       {{index .IPAddresses 0}}      (per device)
#	admin/devices.html      {{index .IPAddresses 0}}      (per device)
#	admin/derp_dashboard.html  {{if index .DERPs 0}}      (no DERPs configured)
#
# A node/device without addresses (or a fresh install with no DERP relays) would
# have failed the same way.
#
# CONTRACTS
#   A. no unguarded `index <slice> 0` left in any template
#   B. the guarded replacements are in place
#   C. the templates parse and the handlers package passes
#   D. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B286: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

TPL=internal/handlers/templates

hdr "B286 — an empty slice must not take a page down"

# --- A: no unguarded index ---------------------------------------------------
# Patterns are anchored on the template ACTION (`{{…}}`) so the explanatory
# comments that quote the broken construct are not counted as violations.
bad_count=0
for pat in '\{\{index \.IPAddresses 0\}\}' '\{\{if index \.DERPs 0\}\}' '\{\{index \$\.DeviceInfos 0\}\}'; do
  n=$(grep -rE "$pat" --include='*.html' "$TPL" 2>/dev/null | wc -l || true)
  if [ "${n:-0}" -eq 0 ]; then
    ok "A1: no unguarded '$pat' action in any template"
  else
    bad "A1: $n template(s) still index a slice that may be empty ($pat) — the page panics with index out of range"
    grep -rE "$pat" --include='*.html' "$TPL" | sed 's/^/       /' >&2 || true
    bad_count=$((bad_count+1))
  fi
done

# --- B: the guarded replacements --------------------------------------------
if grep -q '{{with .IPAddresses}}{{index . 0}}{{end}}' "$TPL/user/exit_nodes.html" \
   && grep -q '{{with .IPAddresses}}{{index . 0}}{{end}}' "$TPL/user/devices.html" \
   && grep -q '{{with .IPAddresses}}{{index . 0}}{{end}}' "$TPL/admin/devices.html"; then
  ok "B1: device/node address cells use {{with .IPAddresses}}{{index . 0}}{{end}}"
else
  bad "B1: an address cell is not guarded"
fi
if grep -q '{{with .DERPs}}' "$TPL/admin/derp_dashboard.html"; then
  ok "B2: the DERP recommendation block guards .DERPs"
else
  bad "B2: admin/derp_dashboard.html still indexes .DERPs unguarded"
fi
if grep -q '{{$pref := ""}}' "$TPL/exit_rules.html"; then
  ok "B3: the Exit Rules preferred-exit fallback is a plain string, not index 0"
else
  bad "B3: exit_rules.html still initialises \$pref with index 0"
fi

# --- C: templates parse and the package passes -------------------------------
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/handlers/ -run 'TestLoadTemplates' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "C1: every embedded template still parses"
  else
    bad "C1: template parsing failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/handlers/ -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "C2: the handlers package passes"
  else
    bad "C2: the handlers package failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "C1-C2: go not on PATH — run the handlers tests on the VM"
fi

# --- D: tracked by git (trap #11) -------------------------------------------
# NOTE on coverage: the live failure was a RUNTIME panic, and this contract is
# static — it pins the absence of the exact construct that caused it
# (`index <slice> 0` on a possibly-empty slice) plus the presence of the guarded
# replacement. A full render of /my/exit-rules with an empty device list needs a
# stub Backend + DB and does not exist in that package yet; until it does, treat
# this contract as "the construct is gone", not "the page is proven to render".
if git ls-files --error-unmatch scripts/check_b286_template_index_bounds.sh >/dev/null 2>&1; then
  ok "D1: scripts/check_b286_template_index_bounds.sh is tracked by git"
else
  bad "D1: scripts/check_b286_template_index_bounds.sh is NOT tracked"
fi

printf '\n\033[1mB286 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
