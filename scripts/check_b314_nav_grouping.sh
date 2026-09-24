#!/usr/bin/env bash
# check_b314_nav_grouping.sh
#
# 2026-09-23 (B314, v1.5.79) — the admin navigation grouped the way the operator asked.
#
# OPERATOR REQUEST (verbatim):
#
#   «Группы по OIDC - явно две страницы, группа по DERP с релеями и здоровьем derp,
#    вынести сертификаты в настройки так как они относяться к настройке самого skygate,
#    группа по deploy кластер highAvailability - так как имеют один смысл по настройке и
#    связаны между собой, группа tailscale headscale headplane так как непосредственно
#    тоже связаны ну и реальные интеграции - это телеграм а сама страница интеграции
#    больше должна называться Сервисы и иметь больше переходных ссылок на остальные
#    сервисы что появились во время разработки и разнесены по группам.»
#
# The old sidebar had ONE «Integrations» section carrying twelve unrelated pages, so
# nothing about a page could be inferred from where it sat.
#
# CONTRACTS
#   A. the ten sections exist and every one has a title key + an open conditional
#   B. OIDC is a group of its own with exactly its two pages
#   C. DERP is a group of its own with the map, the relays AND their health
#   D. deploy + cluster + HA share one group (they configure one physical cluster)
#   E. tailscale + headscale + headplane share one group
#   F. certificates live in Settings (they configure skygate itself, not an integration)
#   G. the Integrations page is called «Сервисы» and carries cross-links to the other
#      service pages; the availability page keeps a name of its own
#   H. sectionPageSet (Go) and layout.html agree in BOTH directions, and B96's
#      renegotiated expectations match
#   I. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B314: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

LAYOUT=internal/handlers/templates/layout.html
MAP=internal/handlers/handlers.go
COMMON=internal/i18n/catalog_common.go
ADMINI18N=internal/i18n/catalog_admin.go
INTEG=internal/handlers/templates/admin/integrations.html
B96=scripts/check_b96.sh
TEST=internal/handlers/layout_v1_1_0_test.go

hdr "B314 — the sidebar grouped the way the operator asked"

# --- A: ten sections, each complete ------------------------------------------------
count=$(grep -cF '<details class="sidebar-section"' "$LAYOUT" || true)
if [ "${count:-0}" -eq 10 ]; then
  ok "A1: ten sidebar sections"
else
  bad "A1: found ${count:-0} sections, want 10 (Services / Providers / DERP / Deploy / OIDC + the five that already existed)"
fi
for s in Services Providers DERP Deploy OIDC; do
  if grep -qF "{{if .InSection$s}}open{{end}}" "$LAYOUT"; then
    ok "A2: the new section $s has its open conditional"
  else
    bad "A2: InSection$s has no open conditional (the section never auto-opens)"
  fi
done
for k in nav.section_services nav.section_providers nav.section_derp nav.section_deploy nav.section_oidc; do
  c="$(grep -cF "\"$k\"" "$COMMON" 2>/dev/null || true)"
  if [ "${c:-0}" -eq 2 ]; then
    ok "A3: i18n key present exactly once per map: $k"
  else
    bad "A3: i18n key $k appears ${c:-0} time(s), want 2 (RU+EN)"
  fi
done

# --- B: OIDC on its own ------------------------------------------------------------
oidc_block="$(awk '/InSectionOIDC}}open/,/<\/details>/' "$LAYOUT")"
n=$(printf '%s' "$oidc_block" | grep -c 'href="/admin/oidc' || true)
if printf '%s' "$oidc_block" | grep -q 'href="/admin/oidc"' \
   && printf '%s' "$oidc_block" | grep -q 'href="/admin/oidc/sync"' \
   && [ "${n:-0}" -eq 2 ]; then
  ok "B1: OIDC is its own group with exactly its two pages"
else
  bad "B1: the OIDC group does not hold exactly /admin/oidc + /admin/oidc/sync (found ${n:-0} links)"
fi
if ! printf '%s' "$oidc_block" | grep -q '/admin/certificates'; then
  ok "B2: certificates are no longer bundled with OIDC"
else
  bad "B2: certificates are still in the OIDC group"
fi

# --- C: DERP with relays and health ------------------------------------------------
derp_block="$(awk '/InSectionDERP}}open/,/<\/details>/' "$LAYOUT")"
if printf '%s' "$derp_block" | grep -q 'href="/admin/derp"' \
   && printf '%s' "$derp_block" | grep -q 'href="/admin/derp/relays"' \
   && printf '%s' "$derp_block" | grep -q 'href="/admin/derp/dashboard"'; then
  ok "C1: DERP holds the map, the relays and the relay health"
else
  bad "C1: the DERP group is incomplete (map + relays + health)"
fi

# --- D: deploy + cluster + HA ------------------------------------------------------
deploy_block="$(awk '/InSectionDeploy}}open/,/<\/details>/' "$LAYOUT")"
for p in deploy cluster ha; do
  if printf '%s' "$deploy_block" | grep -q "href=\"/admin/$p\""; then
    ok "D1: /admin/$p is in the deployment group"
  else
    bad "D1: /admin/$p is not in the deployment group"
  fi
done

# --- E: the providers --------------------------------------------------------------
prov_block="$(awk '/InSectionProviders}}open/,/<\/details>/' "$LAYOUT")"
for p in tailscale headscale headplane; do
  if printf '%s' "$prov_block" | grep -q "href=\"/admin/$p\""; then
    ok "E1: /admin/$p is in the providers group"
  else
    bad "E1: /admin/$p is not in the providers group"
  fi
done

# --- F: certificates in Settings ---------------------------------------------------
settings_block="$(awk '/InSectionSettings}}open/,/<\/details>/' "$LAYOUT")"
if printf '%s' "$settings_block" | grep -q 'href="/admin/certificates"'; then
  ok "F1: certificates live in Settings"
else
  bad "F1: certificates are not in Settings"
fi
if grep -q '"admin/certificates"' "$MAP" && awk '/InSectionSettings/,/},/' "$MAP" | grep -q '"admin/certificates"'; then
  ok "F2: the Go section map agrees (Settings owns admin/certificates)"
else
  bad "F2: sectionPageSet does not place certificates in Settings (the section will not auto-open)"
fi

# --- G: «Сервисы» + cross-links ----------------------------------------------------
if grep -q '"integrations.title":                      "Сервисы"' "$ADMINI18N" \
   && grep -q '"integrations.title":                      "Services"' "$ADMINI18N"; then
  ok "G1: the Integrations page is titled «Сервисы» / Services (RU+EN)"
else
  bad "G1: the Integrations page was not renamed"
fi
if grep -q '"title.admin_services": "Доступность сервисов"' "$COMMON" \
   && grep -q '"title.admin_services":      "Service availability"' "$COMMON"; then
  ok "G2: the availability page keeps a name of its own (two pages cannot both be «Сервисы»)"
else
  bad "G2: the availability page still claims the old integration-status name"
fi
links=$(grep -c 'class="btn btn-secondary"' "$INTEG" || true)
if [ "${links:-0}" -ge 12 ]; then
  ok "G3: the Services page carries ${links} cross-links to the other service pages"
else
  bad "G3: only ${links:-0} cross-links on the Services page (want at least 12)"
fi
for p in /admin/tailscale /admin/headscale /admin/headplane /admin/derp/relays /admin/oidc /admin/telegram /admin/certificates /admin/deploy /admin/cluster /admin/ha; do
  if grep -qF "href=\"$p\"" "$INTEG"; then
    ok "G4: cross-link present: $p"
  else
    bad "G4: cross-link missing: $p"
  fi
done

# --- H: the map and the layout agree; B96 renegotiated -----------------------------
if grep -q 'InSectionOIDC": {' "$MAP" && grep -q 'InSectionDeploy": {' "$MAP" \
   && grep -q 'InSectionDERP": {' "$MAP" && grep -q 'InSectionProviders": {' "$MAP" \
   && grep -q 'InSectionServices": {' "$MAP"; then
  ok "H1: sectionPageSet defines all five new groups"
else
  bad "H1: sectionPageSet is missing one of the new groups"
fi
if grep -q 'if \[ "$SECTION_COUNT" != "10" \]' "$B96" \
   && grep -q 'InSectionOIDC' "$B96" && grep -q 'nav.section_oidc' "$B96"; then
  ok "H2: B96 was renegotiated for the ten-section layout"
else
  bad "H2: B96 still pins the old six-section layout"
fi
if grep -q 'sectionCount != 10' "$TEST" && grep -q '"OIDC":      true' "$TEST"; then
  ok "H3: the Go layout test expects the ten sections"
else
  bad "H3: the Go layout test still expects six sections"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/handlers/ ./internal/i18n/ -run 'B96|SectionPageSet|Parity|LoadTemplates' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "H4: the layout/i18n tests pass"
  else
    bad "H4: the layout/i18n tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "H4: go not on PATH — run the layout tests on the VM"
fi
if bash "$B96" >/dev/null 2>&1; then
  ok "H5: the renegotiated B96 check passes"
else
  skip "H5: B96 could not run here (it needs go on PATH)"
fi

# --- I: git ------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b314_nav_grouping.sh >/dev/null 2>&1; then
  ok "I1: scripts/check_b314_nav_grouping.sh is tracked by git"
else
  bad "I1: scripts/check_b314_nav_grouping.sh is NOT tracked"
fi

printf '\n\033[1mB314 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
