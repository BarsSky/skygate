#!/usr/bin/env bash
# check_b456_relay_weights_editor.sh — B456 (2026-10-31)
#
# «веса нагрузки на relay» — the last open item of B275.1 — was implemented in B453 as an ENGINE
# change (least load relative to a weight, read from global_settings key prefix_owner.weights) and
# left WITHOUT a panel surface: the only way to set a weight was SQL. This block is the editor.
#
# WHAT IT MUST KEEP TRUE (each line below is a contract):
#   * the form is numbers per relay, saved through db.SetGlobalSetting under the SAME key the engine
#     reads, and the handler proves the write by re-reading it through prefixowner.LoadWeights;
#   * ONE bad cell (0, negative, text, >99) refuses the WHOLE submit and names the relay and the
#     value — a half-saved form would leave a rule the operator believes is in force but is not;
#   * the page states what a weight DOES NOT do (it never moves a prefix that already has a healthy
#     owner — B453's sticky rule) and shows both halves: what each relay carries now, and the share
#     of the next N prefixes computed by the engine's own distribution function;
#   * an entry the parser rejects is DISPLAYED, not silently replaced by the default;
#   * the weight the table shows is the engine's effective weight (Weights.Of), so a relay at weight
#     1 is not decorated.
#
# CONTRACTS: 12.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

H=internal/feature/admin/relay_weights_b456.go
HT=internal/feature/admin/relay_weights_b456_test.go
PAGE=internal/feature/admin/exit_nodes_page.go
ADMIN=internal/feature/admin/prefix_admin_b277.go
DRIFT=internal/feature/admin/exit_nodes_prefix_drift.go
TPL=internal/handlers/templates/admin/exit_nodes.html
RT=internal/handlers/relay_weights_render_b456_test.go
ROUTES=cmd/skygate/routes.go
CAT=internal/i18n/catalog_exit_nodes.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$H" "$HT" "$PAGE" "$ADMIN" "$DRIFT" "$TPL" "$RT" "$ROUTES" "$CAT" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b456.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the editor writes the setting the ENGINE reads, and proves it"
GOFMT_OUT="$(gofmt -l "$H" "$HT" "$RT" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$H" 'db.SetGlobalSetting(s.dbc(), prefixowner.SettingWeights, serialized)'; then
  ok "A2: the form saves through db.SetGlobalSetting under prefixowner.SettingWeights"
else
  bad "A2: the editor does not write the key the engine reads"
fi
if has "$H" 'back, rejected := prefixowner.LoadWeights(s.dbc())' && has "$H" '!sameWeights(back, next)'; then
  ok "A3: after the write the handler re-reads it through the ENGINE's parser and refuses a mismatch"
else
  bad "A3: a write nobody verifies can claim success while the engine reads something else"
fi
if has "$H" 'len(problems) > 0 {' && has "$H" 'веса не сохранены'; then
  ok "A4: one bad cell refuses the whole submit (nothing is half-saved) and says so"
else
  bad "A4: a partially applied form would leave a weight in force that the operator did not set"
fi
if has "$H" 'n < prefixowner.DefaultWeight' && has "$H" 'n > WeightsMax' && has "$H" 'err != nil'; then
  ok "A5: zero, negative, text and an over-large weight are each rejected with a reason"
else
  bad "A5: a value the engine would clamp or drop must be refused in the form"
fi
if has "$H" 'prefixowner.WeightDistribution(ordered, load, w, target)'; then
  ok "A6: the share column is computed by the ENGINE's own distribution function (it cannot drift)"
else
  bad "A6: the share column invents arithmetic instead of using the engine's"
fi
if has "$H" 'Существующие префиксы не переезжают'; then
  ok "A7: the confirmation says a weight is not a redistribution (B453 keeps a healthy owner in place)"
else
  bad "A7: saving a weight must not read as 'traffic will move now'"
fi

hdr "B. the page shows the truth, including what it rejects"
if has "$TPL" 'id="relay-weights"' && has "$TPL" 'action="/admin/exit-nodes/relay-weights"'; then
  ok "B1: the weights card exists and posts to its own route"
else
  bad "B1: the editor is not on the page"
fi
if has "$TPL" 'name="w_{{.Relay}}"' && has "$TPL" 'min="1" max="99"'; then
  ok "B2: one bounded number input per relay (the browser cannot submit an out-of-range value by accident)"
else
  bad "B2: the inputs do not carry the bounds the handler enforces"
fi
if has "$TPL" 'name="reset" value="1"'; then
  ok "B3: an explicit reset hands the tailnet back to an even split"
else
  bad "B3: without a reset the operator must type 1 into every cell to undo a weight"
fi
if has "$TPL" 'exit_nodes.weights.rejected_badge'; then
  ok "B4: an entry the engine cannot parse is displayed instead of silently becoming the default"
else
  bad "B4: a rejected weight means a relay runs at 1 while somebody believes otherwise"
fi
if has "$DRIFT" 'out[i].Weight = w.Of(strings.TrimSpace(out[i].ExitNode))' && has "$TPL" '{{if gt .Weight 1}}'; then
  ok "B5: the prefix table shows the engine's EFFECTIVE weight, and only when it is not the default"
else
  bad "B5: the table does not show why one relay carries more prefixes than another"
fi
if has "$PAGE" '"Weights":        prefixAdmin.Weights,'; then
  ok "B6: the page hands the weights view to the template (a view nobody renders is dead code)"
else
  bad "B6: the weights view is not passed to the template"
fi

hdr "C. registered, i18n-paired, and pinned by running the tests"
if has "$ROUTES" 'POST /admin/exit-nodes/relay-weights'; then
  ok "C1: the route is registered in cmd/skygate/routes.go"
else
  bad "C1: no route — the form would POST into a 404"
fi
I18N_FAIL=0
for key in weights.title weights.help weights.col_relay weights.col_weight weights.col_load \
           weights.col_share weights.stored weights.default weights.save weights.save_hint \
           weights.reset weights.raw weights.rejected_badge; do
  full="exit_nodes.$key"
  n="$(grep -c "\"$full\":" "$CAT" 2>/dev/null || true)"
  case "${n:-0}" in
    2) : ;;
    *) I18N_FAIL=1; bad "C2: $full appears ${n:-0} time(s) in the catalogue, want 2 (RU + EN)" ;;
  esac
done
if [ "$I18N_FAIL" -eq 0 ]; then
  ok "C2: every new key exists in BOTH catalogues (RU + EN)"
fi
if ! command -v go >/dev/null 2>&1; then
  skip "C3: no go toolchain on PATH — CI is authoritative"
else
  go test ./internal/feature/admin -run 'B456' -count=1 >"$WORK/admin.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "C3: the payload tests are green (even split, weighted share, refusals, round trip)"
  else
    bad "C3: admin B456 tests failed — $(tail -3 "$WORK/admin.log" | tr '\n' ' ')"
  fi
  go test ./internal/handlers -run 'B456' -count=1 >"$WORK/handlers.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "C4: the template RENDERS the card (and renders nothing when there is no view)"
  else
    bad "C4: render tests failed — $(tail -3 "$WORK/handlers.log" | tr '\n' ' ')"
  fi
fi
if git ls-files --error-unmatch scripts/check_b456_relay_weights_editor.sh >/dev/null 2>&1; then
  ok "C5: this script is tracked by git"
else
  bad "C5: this script is not tracked by git"
fi
if grep -qF 'check_b456_relay_weights_editor' "$GATE" 2>/dev/null; then
  ok "C6: registered in scripts/verify_pre_deploy.sh"
else
  bad "C6: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B456"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "C7: the B456 catalog description contains a backtick (TD-15)" ;;
  "")    bad "C7: the B456 catalog entry was not found" ;;
  *)     ok "C7: the B456 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B456**' "$AGENTS" 2>/dev/null; then
  ok "C8: AGENTS.md's block index carries B456"
else
  bad "C8: AGENTS.md's block index has no B456 line"
fi

printf '\n\033[1mB456 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
