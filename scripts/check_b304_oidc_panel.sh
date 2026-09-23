#!/usr/bin/env bash
# check_b304_oidc_panel.sh
#
# 2026-09-23 (B304, v1.5.69) — OIDC must be configurable entirely from the panel.
#
# LIVE REPORT (native host aro): the OIDC page still carried a warning that the
# configuration «требуется изменение в env и из вебинтерфейса это никак не
# изменить». Two real causes, plus what the operator asked for next:
#
#   1. key_dir was a form field that applied to NOTHING. The live applier (B290)
#      pushed only issuer/client_id/client_secret/redirect_uris, so a new
#      directory took effect — if ever — on the next restart. It is now applied
#      live (oidc.KeyStore.Reload) and the page renders what the running store
#      actually is: resolved path, existence, owner, mode, writability, active
#      kid, and a copy-paste fix per failure mode.
#   2. /admin/oidc/sync read the RAW env and refused to run without it
#      ("SKYGATE_OIDC_ISSUER is not set on the skygate container") even when the
#      operator had saved everything on /admin/oidc. It now uses the effective
#      configuration (the DB row wins), which is what the rest of the page shows.
#   3. The operator asked for env autofill + a script that deploys the headscale
#      side automatically: the page renders the exact .env block (secret never
#      included) and the one command that runs deploy/skygate-apply-oidc.sh
#      pinned to this build; `skygate oidc-export` carries the values (including
#      the secret, on the host only) to that script.
#
# CONTRACTS
#   A. the key store can be moved/created at runtime, safely
#   B. the panel applies key_dir live and refuses a path that cannot work
#   C. the key-dir state probe names every failure mode with a fix
#   D. the sync page uses the effectiive configuration, never the raw env
#   E. the env block + the apply command are rendered, secret-free
#   F. the headscale-side script behaves (real run against a temp config)
#   G. the Go tests exist and pass
#   H. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B304: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

KEYS=internal/oidc/keys.go
ACCESS=internal/oidc/keystore_access_b304.go
SETTINGS=internal/feature/admin/oidc_settings.go
KEYDIR=internal/feature/admin/oidc_keydir_b304.go
SYNC=internal/feature/admin/oidc_sync.go
EXPORT=cmd/skygate/oidc_export_b304.go
SCRIPT=deploy/skygate-apply-oidc.sh
TPL=internal/handlers/templates/admin/oidc_settings.html
TPLSYNC=internal/handlers/templates/admin/oidc_sync.html
RU=internal/i18n/catalog_admin.go
TEST1=internal/oidc/keys_b304_test.go
TEST2=internal/feature/admin/oidc_b304_test.go

hdr "B304 — OIDC fully configurable from the panel (no env edit required)"

# --- A: the key store can be moved at runtime ----------------------------------
if grep -q 'func (ks \*KeyStore) Reload(dir string) error' "$KEYS"; then
  ok "A1: KeyStore.Reload exists"
else
  bad "A1: the key store cannot be moved at runtime"
fi
if grep -q 'func loadOrGenerateKey(dir string) (\*SigningKey, error)' "$KEYS" \
   && grep -q 'func loadPrivateKeyFromDisk(privPath string) (\*rsa.PrivateKey, error)' "$KEYS"; then
  ok "A2: boot and reload share one load-or-generate path"
else
  bad "A2: the reload path diverged from the boot path"
fi
if grep -q 'key dir must be an absolute path' "$KEYS"; then
  ok "A3: a relative key dir is refused by the store itself"
else
  bad "A3: a relative path (B270's live failure) is still accepted"
fi
if grep -q 'the new key is loaded FIRST and swapped in under the write lock' "$KEYS" \
   || grep -q 'ks.mu.Lock()' "$KEYS"; then
  ok "A4: the swap is guarded (a bad path leaves the running key alone)"
else
  bad "A4: the store swap is unsynchronised"
fi
if grep -q 'func (s \*Service) KeysRef() \*KeyStore' "$ACCESS" \
   && grep -q 'func (s \*Service) SetKeys(ks \*KeyStore)' "$ACCESS" \
   && grep -q 's.KeysRef().ActiveKey()' internal/oidc/jwt.go \
   && grep -q 's.KeysRef().Ready()' internal/oidc/jwks.go; then
  ok "A5: every reader goes through the guarded accessor (a runtime repair cannot race the request path)"
else
  bad "A5: a reader still touches Service.Keys directly"
fi

# --- B: the panel applies key_dir live ----------------------------------------
if grep -q 'OIDCKeyDirApplier func(dir string) error' internal/feature/admin/service.go \
   && grep -q 'OIDCKeyDirFn func() (dir, kid string, ready bool)' internal/feature/admin/service.go; then
  ok "B1: the service exposes a live key-dir applier + reporter"
else
  bad "B1: the key-dir applier is not part of the service surface"
fi
if grep -q 's.OIDCKeyDirApplier(keyDir)' "$SETTINGS"; then
  ok "B2: the save path applies the new directory"
else
  bad "B2: key_dir is saved without being applied"
fi
if grep -q 'key_dir was NOT changed' "$SETTINGS" \
   && grep -q '\.Reload(dir)' cmd/skygate/main.go; then
  ok "B3: a failed move refuses the save and names the reason"
else
  bad "B3: a failed move is silent"
fi
if grep -q 'keys\.Reload\|Reload(dir)' cmd/skygate/main.go \
   && grep -q 'NewKeyStore(dir)' cmd/skygate/main.go; then
  ok "B4: main.go can also CREATE the store a failed boot never made (B270 repair path)"
else
  bad "B4: a key store that failed at boot cannot be repaired from the panel"
fi
if grep -q 'oidcKeyDirIsAbsolute(keyDir)' "$SETTINGS" \
   && grep -q 'OIDCDefaultKeyDir' "$SETTINGS"; then
  ok "B5: absolute-path validation + an explicit default are wired into the save"
else
  bad "B5: absolute-path validation is missing"
fi

# --- C: the state probe names every failure mode -------------------------------
for issue in relative_path missing unwritable world_readable unavailable; do
  if grep -q "\"$issue\"" "$KEYDIR"; then
    ok "C1: the probe distinguishes $issue"
  else
    bad "C1: the probe cannot report $issue"
  fi
done
if grep -q 'func oidcDirWritable(dir string) bool' "$KEYDIR" && grep -q 'os.CreateTemp(dir' "$KEYDIR"; then
  ok "C2: writability is asked of the kernel, not guessed from a user name"
else
  bad "C2: writability is guessed"
fi
if grep -q 'oidcStatOwner' "$KEYDIR" && [ -f internal/feature/admin/oidc_keydir_owner_unix.go ]; then
  ok "C3: the owner is reported on Unix without breaking the Windows build"
else
  bad "C3: the owner probe is missing or not split per platform"
fi

# --- D: the sync page must not depend on env ----------------------------------
if grep -q 's.effectiveOIDCSettings()' "$SYNC"; then
  ok "D1: /admin/oidc/sync resolves the effective configuration"
else
  bad "D1: the sync page does not use the effective configuration"
fi
if grep -Eq 's\.Cfg\.OIDC(IssuerURL|ClientSecret|ClientID|RedirectURIs)' "$SYNC"; then
  bad "D2: the sync page reads the raw env again — a panel-configured install would be told to edit .env"
else
  ok "D2: the sync page never falls back to the raw env"
fi
if grep -q 'is not set on the skygate container' "$SYNC"; then
  bad "D3: the env-only refusal wording is back"
else
  ok "D3: refusals point at the panel field (env named only as a fallback)"
fi
if grep -q 'saved_on_panel_tag\|SavedOnPanel' "$TPLSYNC" && grep -q 'SavedOnPanel' "$SYNC"; then
  ok "D4: the page says where the values come from"
else
  bad "D4: the page does not explain the value sources"
fi

# --- E: env block + apply command, secret-free ---------------------------------
if grep -q 'func buildOIDCEnvBlock(eff EffectiveOIDCSettings) string' "$SETTINGS" \
   && grep -q 'skygate oidc-export --secret' "$SETTINGS"; then
  ok "E1: the .env block is generated and points at the host-side secret command"
else
  bad "E1: the .env block generator is missing"
fi
if grep -q 'EnvBlock' "$TPL" && grep -q 'oidc.envblock_title' "$TPL"; then
  ok "E2: the page renders the env block (copy/paste)"
else
  bad "E2: the env block is not rendered"
fi
if grep -q 'func oidcApplyScriptCommand(buildVersion string) string' "$SYNC" \
   && grep -q 'raw.githubusercontent.com/BarsSky/skygate/' "$SYNC" \
   && grep -q 'ApplyScriptCmd' "$TPLSYNC"; then
  ok "E3: the one-command apply script is rendered, pinned to this build"
else
  bad "E3: the apply command is missing from the page"
fi
if grep -q 'runOIDCExportSubcommand' cmd/skygate/main.go \
   && grep -q 'case "oidc-export"' cmd/skygate/main.go \
   && grep -q 'renderHeadscaleOIDCBlock' "$EXPORT"; then
  ok "E4: skygate oidc-export (env / headscale / --secret) exists and is dispatched"
else
  bad "E4: the oidc-export CLI is missing"
fi

# --- F: the headscale-side script behaves --------------------------------------
if [ -f "$SCRIPT" ]; then
  if bash -n "$SCRIPT" 2>/dev/null; then
    ok "F1: deploy/skygate-apply-oidc.sh parses"
  else
    bad "F1: deploy/skygate-apply-oidc.sh does not parse"
  fi
else
  bad "F1: deploy/skygate-apply-oidc.sh is missing"
fi

if [ -f "$SCRIPT" ] && command -v mktemp >/dev/null 2>&1; then
  WORK="$(mktemp -d)"
  cat > "$WORK/skygate" <<'STUB'
#!/usr/bin/env bash
if [ "${1:-}" = "oidc-export" ]; then
  printf 'oidc:\n  issuer: https://gate.example.com\n  client_id: headscale\n  client_secret: test-secret-value\n'
  exit 0
fi
exit 1
STUB
  chmod +x "$WORK/skygate"
  CFG="$WORK/config.yaml"
  printf 'server_url: http://head.example.com\n' > "$CFG"
  BEFORE="$(cat "$CFG")"
  FFAIL=0

  # dry run: nothing changes
  SKYGATE_OIDC_APPLY_ALLOW_NONROOT=1 SKYGATE_BIN="$WORK/skygate" \
    bash "$SCRIPT" --headscale-config "$CFG" --mode none --dry-run >"$WORK/dry.log" 2>&1 || FFAIL=1
  [ "$(cat "$CFG")" = "$BEFORE" ] || FFAIL=1

  # apply: managed block + backup + preserved content
  SKYGATE_OIDC_APPLY_ALLOW_NONROOT=1 SKYGATE_BIN="$WORK/skygate" \
    bash "$SCRIPT" --headscale-config "$CFG" --mode none --no-restart >"$WORK/apply.log" 2>&1 || FFAIL=1
  grep -q '^# >>> skygate oidc (B304)' "$CFG" || FFAIL=1
  grep -q 'client_secret: test-secret-value' "$CFG" || FFAIL=1
  grep -q 'server_url: http://head.example.com' "$CFG" || FFAIL=1
  ls "$CFG".pre-oidc-b304.* >/dev/null 2>&1 || FFAIL=1

  # idempotent re-run
  SKYGATE_OIDC_APPLY_ALLOW_NONROOT=1 SKYGATE_BIN="$WORK/skygate" \
    bash "$SCRIPT" --headscale-config "$CFG" --mode none --no-restart >"$WORK/apply2.log" 2>&1 || FFAIL=1
  [ "$(grep -c '^# >>> skygate oidc (B304)' "$CFG")" -eq 1 ] || FFAIL=1

  if [ "$FFAIL" -eq 0 ]; then
    ok "F2: dry-run/apply/re-run behave (nothing written early, managed block, one block)"
  else
    bad "F2: the apply script misbehaved — see $WORK/apply.log"
  fi

  # an unmanaged oidc: section is refused, not clobbered
  CFG2="$WORK/unmanaged.yaml"
  printf 'server_url: http://head.example.com\noidc:\n  issuer: https://old.example.com\n' > "$CFG2"
  BEFORE2="$(cat "$CFG2")"
  if SKYGATE_OIDC_APPLY_ALLOW_NONROOT=1 SKYGATE_BIN="$WORK/skygate" \
       bash "$SCRIPT" --headscale-config "$CFG2" --mode none --no-restart >"$WORK/unmanaged.log" 2>&1; then
    bad "F3: an unmanaged oidc: section was accepted (it must be refused)"
  elif [ "$(cat "$CFG2")" = "$BEFORE2" ] && grep -q 'refusing to rewrite' "$WORK/unmanaged.log"; then
    ok "F3: an unmanaged oidc: section is refused with a named reason"
  else
    bad "F3: the refusal did not leave the file untouched / did not name the reason"
  fi

  # HuJSON is never edited by string surgery
  CFG3="$WORK/config.hujson"
  printf '{\n  "server_url": "http://head.example.com",\n}\n' > "$CFG3"
  if SKYGATE_OIDC_APPLY_ALLOW_NONROOT=1 SKYGATE_BIN="$WORK/skygate" \
       bash "$SCRIPT" --headscale-config "$CFG3" --mode none --no-restart >"$WORK/hujson.log" 2>&1; then
    bad "F4: a HuJSON config was rewritten by string surgery"
  else
    ok "F4: HuJSON is refused (the block is printed for a manual merge)"
  fi

  # rollback removes the managed block (it must pick a pre-apply backup)
  SKYGATE_OIDC_APPLY_ALLOW_NONROOT=1 SKYGATE_BIN="$WORK/skygate" \
    bash "$SCRIPT" --headscale-config "$CFG" --mode none --no-restart --rollback >"$WORK/rollback.log" 2>&1 || true
  if grep -q 'skygate oidc (B304)' "$CFG"; then
    bad "F5: --rollback left the managed block in place"
  else
    ok "F5: --rollback restores the pre-apply config"
  fi
  rm -rf "$WORK"
else
  skip "F2-F5: no mktemp/bash — behaviour probe skipped"
fi

# --- G: the regression tests ---------------------------------------------------
for f in "$TEST1" "$TEST2"; do
  if [ -f "$f" ]; then ok "G1: $f exists"; else bad "G1: $f is missing"; fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/oidc/ ./internal/feature/admin/ -run 'B304' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "G2: the B304 Go tests pass"
  else
    bad "G2: the B304 Go tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "G2: go not on PATH — run the B304 tests on the VM"
fi

# --- H: git --------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b304_oidc_panel.sh >/dev/null 2>&1 \
   && git ls-files --error-unmatch "$SCRIPT" >/dev/null 2>&1; then
  ok "H1: the contract and the apply script are tracked by git"
else
  bad "H1: scripts/check_b304_oidc_panel.sh or $SCRIPT is NOT tracked"
fi

printf '\n\033[1mB304 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
