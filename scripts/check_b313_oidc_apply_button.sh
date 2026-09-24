#!/usr/bin/env bash
# check_b313_oidc_apply_button.sh
#
# 2026-09-23 (B313, v1.5.78) — OIDC must be APPLIED from the panel, not copy-pasted.
#
# OPERATOR REPORT (native host aro):
#
#   «на aro все еще висит в OIDC предложение по исправлению env и появилось поле со
#    скриптом однако никаких автоматической настройки кнопок нет»
#
# The panel already held every value (B290/B304) — what it lacked was the WRITE: with
# ProtectSystem=strict the skygate unit cannot touch headscale's config and the restart
# belongs to root. That is the same shape the policy applier solved (B272.3 / B283), so
# B313 reuses it: the panel stages a DATA-ONLY request and a root-owned
# skygate-oidc.path unit applies it, restarts headscale, verifies and records the verdict
# where the page reads it.
#
# CONTRACTS
#   A. the request carries the FINISHED headscale oidc: block, rendered by the same
#      function `skygate oidc-export` uses (the two paths cannot drift)
#   B. the request is written atomically and 0600 (it carries the client secret), and an
#      empty or non-oidc block is refused instead of staged
#   C. the result the applier writes is parsed back (ok / failed / none-yet), so a button
#      press can never end in "nothing happened"
#   D. "the helper is installed" is a stat of the applier AND the path unit — a request
#      staged where nothing consumes it would look like a successful press
#   E. the applier script really has the request mode (reads the block, writes the
#      result, consumes the request only on success, and never needs the skygate binary)
#   F. the installer writes the unit pair, so a normal install HAS the button
#   G. the handler is wired: route, admin gate, refusals as flashes, audit without the
#      secret, and the panel renders the button / the fallback command
#   H. tests exist and pass; this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B313: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

REQ=internal/oidc/apply_request_b313.go
BLOCK=internal/oidc/block_b313.go
HANDLER=internal/feature/admin/oidc_apply_b313.go
PANEL=internal/feature/admin/oidc_settings.go
TPL=internal/handlers/templates/admin/oidc_settings.html
APPLIER=deploy/skygate-apply-oidc.sh
INSTALL=deploy/install-common.sh
ROUTE=cmd/skygate/main.go
I18N=internal/i18n/catalog_admin.go

hdr "B313 — apply the saved OIDC configuration to headscale from the panel"

# --- A: one renderer --------------------------------------------------------------
if grep -q 'func RenderHeadscaleBlock(' "$BLOCK"; then
  ok "A1: the headscale oidc: block has ONE renderer"
else
  bad "A1: the block renderer is missing"
fi
if grep -q 'oidc.RenderHeadscaleBlock(oidc.HeadscaleBlockValues{' cmd/skygate/oidc_export_b304.go; then
  ok "A2: skygate oidc-export delegates to it (the CLI output cannot drift)"
else
  bad "A2: the exporter still renders the block itself"
fi
if grep -q 'oidc.RenderHeadscaleBlock(oidc.HeadscaleBlockValues{' "$HANDLER"; then
  ok "A3: the button stages the SAME block"
else
  bad "A3: the button renders a different block"
fi

# --- B: the request ---------------------------------------------------------------
if grep -q 'func WriteOIDCApplyRequest(' "$REQ" && grep -q 'os.Rename(tmpName, path)' "$REQ"; then
  ok "B1: the request is published atomically (temp + rename)"
else
  bad "B1: the request can be read half-written (that crash-looped headscale on the policy path)"
fi
if grep -q 'tmp.Chmod(0o600)' "$REQ"; then
  ok "B2: the request file is 0600 (it carries the client secret)"
else
  bad "B2: the request carries the secret without a restrictive mode"
fi
if grep -q 'refusing to request an empty block' "$REQ" && grep -q 'does not look like a headscale oidc: section' "$REQ"; then
  ok "B3: an empty or non-oidc block is refused, not staged"
else
  bad "B3: a request the applier cannot apply could be staged"
fi
if grep -q 'DATA ONLY — the applier must never source this file' "$REQ"; then
  ok "B4: the request says it is data, not a script (the helper never sources it)"
else
  bad "B4: the data-only marker is gone"
fi

# --- C: the result ---------------------------------------------------------------
if grep -q 'func ReadOIDCApplyResult()' "$REQ" && grep -q 'Present: true' "$REQ"; then
  ok "C1: the applier's verdict is read back"
else
  bad "C1: the page cannot show what the apply did"
fi
if grep -q 'if out.Status == "" {' "$REQ" && grep -q 'out.Status = "unknown"' "$REQ"; then
  ok "C2: an unparseable result never reads as success"
else
  bad "C2: a broken result file could look like success"
fi

# --- D: armed is a stat ----------------------------------------------------------
if grep -q 'func OIDCHelperArmed()' "$REQ" && grep -q 'SKYGATE_OIDC_PATH_UNIT' "$REQ"; then
  ok "D1: 'the helper is installed' checks the applier AND the path unit"
else
  bad "D1: the button could be offered where nothing consumes the request"
fi
if grep -q 'func OIDCHelperScriptPath()' "$REQ"; then
  ok "D2: the fallback can name the exact script"
else
  bad "D2: the fallback message cannot name a command"
fi

# --- E: the applier ---------------------------------------------------------------
if grep -q -- '--from-request' "$APPLIER" && grep -q 'BLOCK_BEGIN' "$APPLIER"; then
  ok "E1: the applier has a request mode that reads the staged block"
else
  bad "E1: nothing consumes a staged request"
fi
if grep -q 'write_result ok' "$APPLIER" && grep -q 'write_result failed' "$APPLIER"; then
  ok "E2: the applier records both outcomes"
else
  bad "E2: the applier's verdict is not recorded"
fi
if grep -q 'rm -f "$REQUEST_FILE"' "$APPLIER"; then
  ok "E3: a successful apply consumes the request (the path unit does not loop)"
else
  bad "E3: the request is left behind and re-applied forever"
fi
if grep -q 'if \[ "$FROM_REQUEST" -eq 0 \] && \[ -z "$SKYGATE_BIN" \]' "$APPLIER"; then
  ok "E4: request mode does not need the skygate binary (the unit runs with its own environment)"
else
  bad "E4: the applier still requires skygate in request mode"
fi
if bash -n "$APPLIER" 2>/dev/null; then
  ok "E5: the applier parses"
else
  bad "E5: deploy/skygate-apply-oidc.sh has a syntax error"
fi

# --- F: the installer -------------------------------------------------------------
if grep -q 'write_oidc_units()' "$INSTALL" && grep -q 'write_oidc_units "\$update_dir"' "$INSTALL"; then
  ok "F1: the installer writes and calls the OIDC unit pair"
else
  bad "F1: a normal install has no OIDC apply helper (the button stays a fallback)"
fi
if grep -q 'PathExists=${update_dir}/oidc.request.props' "$INSTALL" \
   && grep -q -- '--from-request --request ${update_dir}/oidc.request.props --result ${update_dir}/oidc.result.props' "$INSTALL"; then
  ok "F2: the path unit triggers the applier on the request file"
else
  bad "F2: the unit pair does not watch the request file"
fi

# --- G: the panel -----------------------------------------------------------------
if grep -q 'POST /admin/oidc/apply-headscale' "$ROUTE"; then
  ok "G1: the route is registered"
else
  bad "G1: the route is missing"
fi
if grep -q 's.Backend.CurrentUser(r)' "$HANDLER" && grep -q 'http.StatusForbidden' "$HANDLER"; then
  ok "G2: admin-only (a non-admin gets 403, not a request)"
else
  bad "G2: the apply is not gated to admins"
fi
if grep -q 'oidc.apply.err_no_helper' "$HANDLER" && grep -q 'oidc.OIDCHelperScriptPath()' "$HANDLER"; then
  ok "G3: without the helper the refusal NAMES the command"
else
  bad "G3: 'apply it by hand' with no command is the complaint this block closes"
fi
if grep -q 'oidc_apply_requested' "$HANDLER" && ! grep -q 'ClientSecret' "$HANDLER" | grep -q 'Audit'; then
  ok "G4: the audit row names the request, never the secret"
else
  bad "G4: the audit may carry the secret"
fi
if grep -q 'OIDCApplyArmed' "$PANEL" && grep -q 'OIDCApplyResult' "$PANEL"; then
  ok "G5: the panel knows whether the button works and what happened last time"
else
  bad "G5: the page cannot render the button state"
fi
if grep -q '/admin/oidc/apply-headscale' "$TPL" && grep -q 'oidc.apply.not_armed' "$TPL"; then
  ok "G6: the template renders the button and the documented fallback"
else
  bad "G6: the button is not rendered"
fi
for k in title help button async_help status_ok status_failed status_none not_armed fallback_help requested err_incomplete err_no_secret err_no_helper err_write; do
  c="$(grep -cF "\"oidc.apply.$k\"" "$I18N" 2>/dev/null || true)"
  if [ "${c:-0}" -eq 2 ]; then
    ok "G7: i18n key present exactly once per map: oidc.apply.$k"
  else
    bad "G7: i18n key oidc.apply.$k appears ${c:-0} time(s), want 2 (RU+EN)"
  fi
done

# --- H: tests + git ----------------------------------------------------------------
for f in internal/oidc/apply_request_b313_test.go internal/feature/admin/oidc_apply_b313_test.go; do
  if [ -f "$f" ]; then ok "H1: $f exists"; else bad "H1: $f is missing"; fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/oidc/ ./internal/feature/admin/ ./cmd/skygate/ -run 'B313|OIDCApply|RenderHeadscaleBlock|OIDCExport' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "H2: the B313 tests pass"
  else
    bad "H2: the B313 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "H2: go not on PATH — run the B313 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b313_oidc_apply_button.sh >/dev/null 2>&1; then
  ok "H3: scripts/check_b313_oidc_apply_button.sh is tracked by git"
else
  bad "H3: scripts/check_b313_oidc_apply_button.sh is NOT tracked"
fi

printf '\n\033[1mB313 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
