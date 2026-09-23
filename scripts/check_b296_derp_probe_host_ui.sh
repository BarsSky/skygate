#!/usr/bin/env bash
# check_b296_derp_probe_host_ui.sh
#
# 2026-09-23 (B296) — «адрес проверки релея задаётся из веб-интерфейса и
# применяется сразу».
#
# B289.1 taught every DERP probe to dial the operator's address while still
# speaking the relay's public hostname (TLS SNI), and deploy.sh / .env.example
# documented `SKYGATE_DERP_PROBE_HOST` for it. One usability defect remained, and
# it is the one the operator hit: the skygate container is created from
# `env_file: .env`, and Docker freezes that environment at container CREATION —
# `docker compose restart` re-runs the entrypoint but keeps the old environment,
# so applying an edited value meant `docker compose up --force-recreate` (AGENTS
# trap #3) from an SSH session, for a value the operator can see is wrong right
# on /admin/derp/relays («пропущен: unreachable (probed …)»).
#
# The fix is a second, higher layer: `global_settings.derp.probe_host`, written
# by the new card, resolved PER PROBE (internal/derpcfg: DB > .env > none). No
# restart, no recreate, no docker-compose.yml edit.
#
# CONTRACTS
#   A. one resolver owns the knob (DB > env > none) and nothing reads the env
#      directly any more — that is what makes "applies immediately" true
#   B. the card exists: route + admin-gated handler + form + flash + cache drop
#   C. every message is translatable (RU + EN) and every refusal has its own code
#   D. the behaviour is pinned by Go tests that SAVE through the handler and
#      re-fetch the derpmap
#   E. docs / release notes mention it, and this script is tracked by git
#   F. the check is registered in the pre-deploy catalogue

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B296: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

CFG=internal/derpcfg/derpcfg.go
CFGTEST=internal/derpcfg/derpcfg_b296_test.go
RELAYS=internal/feature/admin/derp_relays.go
DASH=internal/feature/admin/derp_dashboard.go
STUN=internal/feature/admin/derp_stun.go
PROBE=internal/derphealth/probe.go
TYPES=internal/derphealth/types.go
MAP=internal/derphealth/map.go
TMPL=internal/handlers/templates/admin/derp_relays.html
I18N=internal/i18n/catalog_derp.go
MAIN=cmd/skygate/main.go
ADMINTEST=internal/feature/admin/derp_probe_host_b296_test.go

hdr "B296 — the relay probe address is editable from the panel (and applies live)"

# --- A: one resolver, two layers ---------------------------------------------
if [ -f "$CFG" ] && grep -q 'SettingKey = "derp.probe_host"' "$CFG" \
   && grep -q 'EnvKey = "SKYGATE_DERP_PROBE_HOST"' "$CFG"; then
  ok "A1: internal/derpcfg owns the knob (derp.probe_host row + .env variable)"
else
  bad "A1: internal/derpcfg is missing or does not name both layers"
fi
if grep -q '^func Resolve(' "$CFG" && grep -q '^func Validate(' "$CFG" && grep -q '^func Save(' "$CFG"; then
  ok "A2: Resolve / Validate / Save are the whole API (page writes, probes read)"
else
  bad "A2: derpcfg does not expose Resolve + Validate + Save"
fi
# The DB layer must be consulted BEFORE the env layer: an operator who typed an
# address on the page must not be overruled by a stale .env value.
DB_LINE=$(grep -n 'db.GetGlobalSetting(d, SettingKey' "$CFG" | head -1 | cut -d: -f1)
ENV_LINE=$(grep -n 'if v := EnvHost(); v != ""' "$CFG" | head -1 | cut -d: -f1)
if [ -n "$DB_LINE" ] && [ -n "$ENV_LINE" ] && [ "$DB_LINE" -lt "$ENV_LINE" ]; then
  ok "A3: the DB override is resolved before the .env layer (db > env > none)"
else
  bad "A3: derpcfg resolves .env before the DB override (db=${DB_LINE:-none} env=${ENV_LINE:-none})"
fi
# Every probe must go through the resolver — a leftover os.Getenv would keep the
# old "edit .env and recreate" behaviour for that one path, silently.
LEFTOVER=$(git grep -l 'os.Getenv("SKYGATE_DERP_PROBE_HOST")' -- internal/feature/admin internal/derphealth 2>/dev/null | grep -v '_test.go' | grep -v 'derpcfg' || true)
if [ -z "$LEFTOVER" ]; then
  ok "A4: no probe reads the environment variable directly any more"
else
  bad "A4: these files bypass the resolver: $(echo "$LEFTOVER" | tr '\n' ' ')"
fi
if grep -q 'derpcfg.DialHost(db)' "$DASH" || grep -q 'derpcfg.DialHost(s.dbc())' "$DASH"; then
  ok "A5: the map guard and the status probes resolve the hint through derpcfg"
else
  bad "A5: the derpmap guard still uses a hard-coded probe host"
fi
if grep -q '^func dialTargetFor(' "$PROBE" && grep -q 'derpcfg.EnvHost()' "$PROBE"; then
  ok "A6: the derp_health cron takes the hint from the row, with the .env fallback"
else
  bad "A6: dialTargetFor does not use the per-row hint"
fi
if grep -q 'ProbeHost string' "$TYPES" && grep -q 'probeHost := derpcfg.DialHost(db)' "$MAP"; then
  ok "A7: own DERP rows carry the resolved hint (FetchOwnDERPs fills it once)"
else
  bad "A7: DERPInfo.ProbeHost is not filled by FetchOwnDERPs"
fi

# --- B: the card -------------------------------------------------------------
if grep -q 'POST /admin/derp/relays/probe-host' "$MAIN" \
   && grep -q 'PostAdminDerpRelaysProbeHost' "$MAIN"; then
  ok "B1: POST /admin/derp/relays/probe-host is routed"
else
  bad "B1: the probe-host route is not registered in cmd/skygate/main.go"
fi
if grep -q '^func (s \*Service) PostAdminDerpRelaysProbeHost(' "$RELAYS" \
   && grep -q 'c == nil || !c.IsAdmin' "$RELAYS"; then
  ok "B2: the handler is admin-gated"
else
  bad "B2: PostAdminDerpRelaysProbeHost is missing or not admin-gated"
fi
if grep -q 'derpcfg.Save(s.dbc(), raw)' "$RELAYS" && grep -q 'derp_relay.probe_host' "$RELAYS"; then
  ok "B3: the handler validates through derpcfg and audits the change"
else
  bad "B3: the handler does not save via derpcfg.Save / does not audit"
fi
if grep -q 'mapStatusInvalidate()' "$RELAYS"; then
  ok "B4: the page's 30s map cache is dropped on save (the verdict is fresh)"
else
  bad "B4: the cached map verdicts survive a probe-host change"
fi
if grep -q 'derpcfg.Resolve(s.dbc())' "$RELAYS" \
   && grep -q '"ProbeHostSaved"' "$RELAYS" && grep -q '"ProbeHostErr"' "$RELAYS"; then
  ok "B5: the page renders the effective value, its source and the flash"
else
  bad "B5: the relays page does not pass the probe-host state to the template"
fi
if grep -q 'action="/admin/derp/relays/probe-host"' "$TMPL" \
   && grep -q 'name="probe_host"' "$TMPL" \
   && grep -q 'value="clear"' "$TMPL"; then
  ok "B6: the template has the form, the input and the «Очистить» button"
else
  bad "B6: the probe-host card is missing from admin/derp_relays.html"
fi
if grep -q 'no container recreate' "$I18N" && grep -q 'safeHTML' "$TMPL"; then
  ok "B7: the card (and its translated help) explains why no recreate is needed"
else
  bad "B7: the card does not explain the no-recreate behaviour"
fi

# --- C: i18n pairs + one code per refusal ------------------------------------
KEYS="derp.relays_probe_host_title
derp.relays_probe_host_help
derp.relays_probe_host_saved
derp.relays_probe_host_current
derp.relays_probe_host_none
derp.relays_probe_host_field
derp.relays_probe_host_field_help
derp.relays_probe_host_save
derp.relays_probe_host_clear
derp.relays_probe_host_env
derp.relays_probe_host_src_db
derp.relays_probe_host_src_env
derp.relays_probe_host_src_default
derp.relays_probe_host_err_parse_form
derp.relays_probe_host_err_port
derp.relays_probe_host_err_scheme
derp.relays_probe_host_err_path
derp.relays_probe_host_err_spaces
derp.relays_probe_host_err_invalid"
MISSING=""
for k in $KEYS; do
  n=$(grep -c "\"$k\":" "$I18N")
  [ "$n" -ge 2 ] || MISSING="$MISSING $k"
done
if [ -z "$MISSING" ]; then
  ok "C1: all $(echo "$KEYS" | wc -l | tr -d ' ') card keys exist in RU and EN"
else
  bad "C1: missing RU/EN pairs:$MISSING"
fi
CODES_OK=1
for c in port scheme path spaces invalid; do
  grep -q "Code: \"$c\"" "$CFG" || CODES_OK=0
done
if [ "$CODES_OK" = "1" ]; then
  ok "C2: every refused shape has its own code (the page can say WHY)"
else
  bad "C2: derpcfg.Validate does not emit one code per refused shape"
fi
if grep -q 'prefix "derp.relays_probe_host_err_%s"' "$TMPL" \
   || grep -q 'printf "derp.relays_probe_host_err_%s"' "$TMPL"; then
  ok "C3: the refusal code is turned into a translated message in the template"
else
  bad "C3: the template does not translate the refusal code"
fi

# --- D: behaviour ------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/derpcfg/ -run 'B296' -count=1 2>&1)"
  if printf '%s' "$OUT" | grep -q '^ok' && ! printf '%s' "$OUT" | grep -q 'FAIL'; then
    ok "D1: the resolver tests pass (precedence db > env, clearing, validation)"
  else
    bad "D1: internal/derpcfg B296 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go test ./internal/feature/admin/ -run 'B296' -count=1 2>&1)"
  if printf '%s' "$OUT" | grep -q '^ok' && ! printf '%s' "$OUT" | grep -q 'FAIL'; then
    ok "D2: the handler tests pass (save -> the next derpmap fetch publishes the region)"
  else
    bad "D2: the admin B296 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "D1/D2: go is not on PATH — run the B296 tests on the VM"
fi
if [ -f "$ADMINTEST" ] && grep -q 'PostAdminDerpRelaysProbeHost(rec, req)' "$ADMINTEST" \
   && grep -q 'GetAdminDerpRelaysDerpmap(rec' "$ADMINTEST"; then
  ok "D3: the regression drives the real handler AND the real derpmap endpoint"
else
  bad "D3: the handler regression does not exercise save -> derpmap"
fi
if grep -q 'after clearing the override' "$ADMINTEST" \
   && grep -q 'must not be stored' "$ADMINTEST"; then
  ok "D4: both failure directions are pinned (clear falls back, a refused value is not stored)"
else
  bad "D4: the tests do not pin the clear/refuse behaviour"
fi

# --- E: docs, notes, tracking ------------------------------------------------
if grep -q 'probe_host\|Проба\|probe address' docs/derp.md; then
  ok "E1: docs/derp.md documents the panel-set probe address"
else
  bad "E1: docs/derp.md does not mention the UI override"
fi
if grep -q '^## v1.5.62' RELEASE-NOTES.md; then
  ok "E2: RELEASE-NOTES.md has the v1.5.62 section"
else
  bad "E2: RELEASE-NOTES.md has no v1.5.62 entry"
fi
if git ls-files --error-unmatch scripts/check_b296_derp_probe_host_ui.sh >/dev/null 2>&1; then
  ok "E3: this script is tracked by git (trap #11)"
else
  bad "E3: scripts/check_b296_derp_probe_host_ui.sh is NOT tracked"
fi

# --- F: registration ---------------------------------------------------------
if grep -q 'run_check "B296"' scripts/verify_pre_deploy.sh; then
  ok "F1: the check is registered in scripts/verify_pre_deploy.sh"
else
  bad "F1: B296 is not registered in the pre-deploy catalogue"
fi

printf '\n\033[1mB296 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
