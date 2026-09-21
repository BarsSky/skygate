#!/usr/bin/env bash
# check_b275_1_prefix_ui.sh — B275.1: the operator surface for prefix assignment.
#
# The engine (B275) decides which relay owns a prefix. This block gives the
# operator the page, the per-row pin and the help text — plus the rule-form
# wording that stops implying "your device will use the exit node you pick".
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || { printf 'B275.1: cannot locate cmd/skygate/main.go\n' >&2; exit 2; }

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

ADMIN=internal/feature/admin/exit_nodes.go
TPL=internal/handlers/templates/admin/exit_nodes.html
CAT=internal/i18n/catalog_exit_nodes.go
MAIN=cmd/skygate/main.go
DOC=docs/troubleshooting.md

hdr "B275.1 — operator surface for prefix assignment"

grep -q 'type PrefixOwnerRow struct' "$ADMIN" && ok "A.1 PrefixOwnerRow exists" || bad "A.1 PrefixOwnerRow missing"
# CONTRACT RENEGOTIATION (2026-09-21, B276): the loader now returns the rows AND the
# drift summary (the page needs both from ONE headscale read), so it asserts the
# two-value form instead of the original one-value signature. The property — the page
# reads the assignment table and hands rows to the template — is unchanged.
grep -q 'func (s \*Service) loadPrefixOwnerRows() (\[\]PrefixOwnerRow, PrefixDriftStats)' "$ADMIN" && ok "A.2 loadPrefixOwnerRows() reads the table (rows + drift summary, B276)" || bad "A.2 loadPrefixOwnerRows() missing"
grep -q 'func (s \*Service) PostAdminExitPrefixOwner(w http.ResponseWriter, r \*http.Request)' "$ADMIN" && ok "A.3 the pin handler exists" || bad "A.3 PostAdminExitPrefixOwner missing"
grep -q 'prefixowner.SetManual(s.dbc(), prefix, relay)' "$ADMIN" && ok "A.4 the handler calls SetManual (manual pin / hand back to auto)" || bad "A.4 the handler must call prefixowner.SetManual"
grep -q 's.Backend.Audit(c.UserID, c.Username, action' "$ADMIN" && ok "A.5 pinning is audited" || bad "A.5 the pin must write an audit row"
grep -q 'prefix_owner_pin\|prefix_owner_auto' "$ADMIN" && ok "A.6 pin and auto-release are distinct audit actions" || bad "A.6 distinct audit actions missing"
grep -q 'RelayChoices' "$ADMIN" && ok "A.7 the relay list for the select is passed to the template" || bad "A.7 RelayChoices missing"
grep -q 'Advertised bool' "$ADMIN" && ok "A.8 the row carries the advertised/assigned drift flag" || bad "A.8 the Advertised drift flag missing"
grep -q '"POST /admin/exit-nodes/prefix-owner"' "$MAIN" && ok "A.9 the route is registered (admin + auth middleware)" || bad "A.9 route not registered"

grep -q 'exit_nodes.prefix_owner.title' "$TPL" && ok "B.1 the template renders the section" || bad "B.1 template section missing"
grep -q 'action="/admin/exit-nodes/prefix-owner"' "$TPL" && ok "B.2 the per-row pin form posts to the handler" || bad "B.2 the row form is missing"
grep -q 'exit_nodes.prefix_owner.option_auto' "$TPL" && ok "B.3 the select offers handing the prefix back to the engine" || bad "B.3 the auto option is missing"
grep -q 'exit_nodes.prefix_owner.help' "$TPL" && ok "B.4 the page explains the model (in-page help)" || bad "B.4 in-page help missing"

for k in title help col_prefix col_owner col_source col_claims col_advertised col_action \
         source_explicit source_manual source_auto option_auto save empty; do
  n=$(grep -c "\"exit_nodes.prefix_owner.${k}\"" "$CAT" || true)
  if [ "$n" -ge 2 ]; then
    ok "C $k in RU + EN"
  else
    bad "C exit_nodes.prefix_owner.${k} appears ${n} time(s) — need RU + EN"
  fi
done

grep -qi 'prefix_owner\|владелец префикса\|prefix assignment' "$DOC" && ok "D.1 the operator docs cover the assignment model" || bad "D.1 docs/troubleshooting.md must explain the prefix-owner model"

if command -v go >/dev/null 2>&1; then
  out="$(go test ./internal/i18n/ ./internal/handlers/ ./internal/feature/admin/ 2>&1)"
  if grep -q '^ok' <<< "$out" && ! grep -q '^FAIL' <<< "$out"; then
    ok "E.1 i18n parity + template parse + admin tests pass"
  else
    bad "E.1 tests failed: $(printf '%s' "$out" | tail -n 6)"
  fi
else
  skip "E go toolchain not on PATH"
fi

printf '\n\033[1mB275.1: %d PASS, %d FAIL, %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
