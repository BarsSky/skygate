#!/usr/bin/env bash
# check_b515_relay_accept_routes_observed.sh — B515 (2026-11-23)
#
# «ЖЕЛЕЗОБЕТОННЫЙ ВЫВОД» — the operator's words when handing this block over, and the reason
# it is split in two halves.
#
# B514 established the rule (a relay that ADVERTISES routes must never ACCEPT them) and made
# the sync force `--accept-routes=false`. That is a statement about what skygate ASKS FOR.
# Nothing read the relay's own answer back, so the only way to learn that a relay ignored it
# was to measure interface counters on two relays during one download — which is exactly how
# the loop behind B514 was found.
#
# B515.1 is the read-back: one `tailscale debug prefs` over the SAME transport that applied
# the routes, parsed for `RouteAll`, stored per relay, rendered next to the desired value.
#
# THE PART THAT MUST NOT BREAK (contract B): the read-back lives ON the route-application
# path. A bug there does not degrade a page — it stops route application on every relay,
# i.e. it breaks the function that had just fixed the tailnet. So the contracts are written
# in the order of that risk:
#
#   A. the read-back exists and IS the same transport (same argv builder, same endpoint)
#   B. it cannot fail the apply: decision first, read after, errors swallowed, bounded
#   C. the store and the page (desired NEXT TO observed, unknown never rendered as ok)
#   D. i18n RU+EN (a missing key on either side fails B4/B325)
#   E. behaviour (the parser distinguishes "false" from "could not read")
#   F. registry (gate, AGENTS, ROADMAP EN+RU, RELEASE-NOTES, troubleshooting, git-tracked)

set -uo pipefail
PASS=0; FAIL=0; SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"; cd "$ROOT" || exit 1
# B339: the admin exit_nodes PAGE is a split SURFACE (exit_nodes.go + exit_nodes_page.go
# + the per-block files), so the contracts below read the surface, never one file — a
# pure code move inside it must not turn them red, and a re-introduction in a sibling
# file must still be caught.
. scripts/lib/gosurface.sh
HELP=internal/headscale/relay_accept_routes_b515.go
HELPT=internal/headscale/relay_accept_routes_b515_test.go
LADDER=internal/feature/exit_rules/relay_transport_tailnet_b310.go
SYNC=internal/feature/exit_rules/sync_routes.go
REC=internal/feature/exit_rules/relay_transport_b309.go
DB=internal/db/relay_accept_routes_b515.go
TPL=internal/handlers/templates/admin/exit_nodes.html
CAT=internal/i18n/catalog_exit_nodes.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
TRBL=docs/troubleshooting.md
RM=docs/ROADMAP.md
RMRU=docs/ru/ROADMAP.md
VIEW=internal/feature/admin/relay_accept_routes_view_b515.go

for f in "$HELP" "$HELPT" "$LADDER" "$SYNC" "$REC" "$DB" "$VIEW" "$TPL" "$CAT" "$GATE" "$AGENTS" "$NOTES" "$TRBL" "$RM" "$RMRU"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo; echo "B515 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done
# The row shape (exit_nodes.go) and the handler that fills it (exit_nodes_page.go) are
# two files of ONE surface; both contracts below read it whole.
gosurface ADMIN internal/feature/admin/exit_nodes.go internal/feature/admin/exit_nodes_*.go || {
  bad "cannot read the admin/exit_nodes surface"; exit 1;
}

echo "[A] the read-back is the SAME transport that applied the routes"
case "$(cat "$HELP")" in
  *'func ReadRelayRouteAll('*) ok "A1: ReadRelayRouteAll exists" ;;
  *) bad "A1: no read-back function" ;;
esac
case "$(cat "$HELP")" in
  *'buildSetAdvertisedRoutesArgv(keyPath, host, port, TailscaleReadPrefsCommand, jump)'*)
    ok "A2: it builds the ssh argv with the route-apply builder (one place decides the transport)" ;;
  *) bad "A2: the read-back builds its own ssh invocation — it could talk to a different host than the apply" ;;
esac
case "$(cat "$HELP")" in
  *'TailscaleReadPrefsCommand'*)
    ok "A3: it runs the shared 'tailscale debug prefs' constant, not a hand-rolled string" ;;
  *) bad "A3: the read command is not the shared constant" ;;
esac
case "$(cat "$LADDER")" in
  *'ReadRelayRouteAll(ep.Target(), keyPath, ep.Jump)'*)
    ok "A4: the ladder reads it over the endpoint that just carried the routes (target + key + jump)" ;;
  *) bad "A4: the ladder does not reuse the winning endpoint — the observation would be about a different path" ;;
esac
case "$(cat "$SYNC")" in
  *'headscale.ReadLocalRouteAll()'*)
    ok "A5: a relay that IS this host is read locally (no ssh involved)" ;;
  *) bad "A5: the local transport records no observation" ;;
esac

echo "[B] it can never break the route application (the reason this block is risky)"
# The decision must already be made when the read runs: res.OK is set, and the read's result
# only lands in a field.
if awk '/res\.OK = true/{ok=NR} /ReadRelayRouteAll\(/{rd=NR} END{exit !(ok && rd && rd > ok)}' "$LADDER"; then
  ok "B1: the apply decision (res.OK) is made BEFORE the read — the read cannot downgrade it"
else
  bad "B1: the read runs before the apply is marked OK, so a read failure could undo a working apply"
fi
if grep -q 'res.ObservedAcceptRoutes = headscale.ReadRelayRouteAll(' "$LADDER" 2>/dev/null; then
  ok "B2: the ladder stores the observation in its result (a field, never an error)"
else
  bad "B2: the ladder does not store the observation in the ladder result"
fi
if grep -qE 'ObservedAcceptRoutes[^=]*=[^=]*err' "$LADDER" "$SYNC" 2>/dev/null; then
  bad "B2b: the observation is wired to an error — a failed READ would become a failed APPLY"
else
  ok "B2b: the observation carries no error, so a failed read cannot fail the apply"
fi
case "$(cat "$HELP")" in
  *'RouteAllReadTimeout'*) ok "B3: the read is bounded in time (a wedged daemon cannot stall the sync goroutine)" ;;
  *) bad "B3: the read has no timeout" ;;
esac
if grep -qE 'panic\(' "$HELP" "$DB" "$VIEW" 2>/dev/null; then
  bad "B4: the new code can panic inside the sync path"
else
  ok "B4: no panic in the read-back, the store or the view"
fi
# The store is written through ONE best-effort helper next to recordRelayApply, and the
# failure of the storage write is logged, never returned.
case "$(cat "$REC")" in
  *'func recordRelayAcceptRoutes('*) ok "B5: one best-effort recorder (recordRelayAcceptRoutes)" ;;
  *) bad "B5: no recorder" ;;
esac
case "$(cat "$REC")" in
  *'SetRelayAcceptRoutesObserved(d, relay, value)'*) ok "B6: it writes through db.SetRelayAcceptRoutesObserved" ;;
  *) bad "B6: the observation is not persisted" ;;
esac
case "$(cat "$REC")" in
  *'could not record the observed value'*)
    ok "B7: a storage failure is LOGGED, not propagated — the sync never fails on it" ;;
  *) bad "B7: a storage failure is silent or fatal" ;;
esac
# And the recorder must run for BOTH transports, i.e. from recordRelayApply (shared tail).
case "$(cat "$REC")" in
  *'recordRelayAcceptRoutes(d, relay, out.AcceptRoutesObserved,'*)
    ok "B8: recorded from recordRelayApply, so both sync paths persist it" ;;
  *) bad "B8: the observation is recorded from one path only" ;;
esac

echo "[C] the store and the page"
case "$(cat "$DB")" in
  *'const relayAcceptRoutesPrefix = "relay_accept_routes:"'*) ok "C1: one key namespace (relay_accept_routes:<host>)" ;;
  *) bad "C1: the store key is not pinned" ;;
esac
case "$(cat "$DB")" in
  *'func RelayAcceptRoutesObserved('*) ok "C2: a reader exists for the page" ;;
  *) bad "C2: no reader" ;;
esac
if grep -q 'func classifyRelayAcceptRoutes(' "$VIEW" 2>/dev/null && grep -q 'relayAcceptRoutesLoop' "$VIEW" 2>/dev/null; then
  ok "C3: 'true' is classified as danger (an active loop), not as a warning"
else
  bad "C3: no loop classification"
fi
if grep -qE '^[[:space:]]+default:' "$VIEW" 2>/dev/null; then
  ok "C4: anything that is not the relay's own word falls to unknown"
else
  bad "C4: no default branch — an unread value could be rendered as ok"
fi
case "$(cat "$ADMIN")" in
  *'annotateObservedAcceptRoutes(nodes, db.RelayAcceptRoutesObserved(s.dbc()))'*)
    ok "C5: the handler fills the column from the STORE (no ssh while rendering)" ;;
  *) bad "C5: the handler does not fill the observed column" ;;
esac
case "$(cat "$ADMIN")" in
  *'ObserveAcceptRoutesTone'*) ok "C6: the row carries the observed value and its tone" ;;
  *) bad "C6: the row shape has no observed fields" ;;
esac
case "$(cat "$TPL")" in
  *'{{t "exit_nodes.accept_routes_observed"}}'*) ok "C7: the table has the new column header" ;;
  *) bad "C7: no column header" ;;
esac
if grep -q 'eq .ObserveAcceptRoutesTone "danger"' "$TPL" 2>/dev/null && grep -q 'eq .ObserveAcceptRoutesTone "ok"' "$TPL" 2>/dev/null; then
  ok "C8: the cell distinguishes danger (loop) from ok (false)"
else
  bad "C8: the cell does not distinguish the two verdicts"
fi
if grep -q 'accept_routes_observed_unknown' "$TPL" 2>/dev/null; then
  ok "C9: an unmeasured relay renders 'not measured', never a green tick"
else
  bad "C9: the unknown state is not rendered"
fi
if grep -q '{{if .AcceptRoutesLoops}}' "$TPL" 2>/dev/null; then
  ok "C10: a loop raises a page-level banner (the download-slowing case is not a subtle tag)"
else
  bad "C10: no loop banner"
fi

echo "[D] i18n RU + EN (a missing key on either side fails B4/B325)"
for k in accept_routes_observed accept_routes_observed_true accept_routes_observed_false \
         accept_routes_observed_unknown accept_routes_observed_loop_tip \
         accept_routes_observed_ok_tip accept_routes_observed_unknown_tip \
         accept_routes_observed_loop_banner accept_routes_observed_loop_banner_help; do
  n=$(grep -c "\"exit_nodes.$k\"[[:space:]]*:" "$CAT" 2>/dev/null || true)
  if [ "${n:-0}" -eq 2 ]; then
    ok "D1: exit_nodes.$k is declared in BOTH catalogues"
  else
    bad "D1: exit_nodes.$k is declared ${n:-0} time(s), want 2 (RU + EN)"
  fi
done
# Every RU value must be Russian: B325 pins the count of ASCII-only RU values at 0, and an
# English string sitting in the RU map passes the parity test happily.
NO_CYR=$(awk '
  /^var ruExitNodes = map\[string\]string\{/ { inru = 1; next }
  /^var enExitNodes = map\[string\]string\{/ { inru = 0; next }
  inru && /"exit_nodes\.accept_routes_observed/ { print }
' "$CAT" | grep -v '[А-Яа-яЁё]' | wc -l | tr -d ' ')
if [ "${NO_CYR:-0}" -eq 0 ]; then
  ok "D2: every new RU value carries Cyrillic (no English text in the RU catalogue)"
else
  bad "D2: ${NO_CYR} new RU value(s) without a single Cyrillic letter"
fi
# The new cell must render CATALOGUE strings: a hardcoded label in a table whose RU/EN
# wording exists is exactly what B325 C1 counts, and the pre-existing `true`/`false` tags
# elsewhere in this table are protocol tokens, not new text.
CELL_KEYS=0
for k in accept_routes_observed_true accept_routes_observed_false accept_routes_observed_unknown; do
  if grep -q "{{t \"exit_nodes.$k\"}}" "$TPL" 2>/dev/null || grep -q "{{t \"exit_nodes.$k\"" "$TPL" 2>/dev/null; then
    CELL_KEYS=$((CELL_KEYS + 1))
  fi
done
if [ "$CELL_KEYS" -eq 3 ]; then
  ok "D3: all three verdict labels go through the catalogue (RU + EN wording is reachable)"
else
  bad "D3: only $CELL_KEYS of 3 verdict labels use a catalogue key — the rest is hardcoded text"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/i18n/ -run TestCatalogsParity -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "D4: the RU+EN key-set parity test passes"
  else
    bad "D4: parity failed: $(printf '%s' "$OUT" | tail -4)"
  fi
else
  skip "D4: no Go toolchain on PATH"
fi

echo "[E] behaviour"
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/headscale/ -run 'B515' -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "E1: the parser tests pass (false ≠ could-not-read, absent field is not false)"
  else
    bad "E1: parser tests failed: $(printf '%s' "$OUT" | tail -c 400)"
  fi
  OUT="$(go test ./internal/feature/admin/ ./internal/feature/exit_rules/ -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "E2: the page and route-sync packages are green (B293/B300/B310/B370/B451/B514 contracts intact)"
  else
    bad "E2: package tests failed: $(printf '%s' "$OUT" | tail -c 400)"
  fi
  OUT="$(go test ./internal/handlers/ -run TestLoadTemplates -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "E3: every template still parses"
  else
    bad "E3: template parse failed: $(printf '%s' "$OUT" | tail -c 300)"
  fi
else
  skip "E1-E3: no Go toolchain on PATH"
fi

echo "[F] registry"
case "$(cat "$GATE")" in *'run_check "B515"'*) ok "F1: registered in the gate" ;; *) bad "F1: not registered in scripts/verify_pre_deploy.sh" ;; esac
case "$(cat "$AGENTS")" in *"**B515**"*) ok "F2: AGENTS.md block index" ;; *) bad "F2: no AGENTS.md entry" ;; esac
case "$(cat "$NOTES")" in *"B515"*) ok "F3: RELEASE-NOTES explain it" ;; *) bad "F3: no RELEASE-NOTES entry" ;; esac
case "$(cat "$TRBL")" in *"(B515)"*) ok "F4: troubleshooting carries the procedure" ;; *) bad "F4: no troubleshooting entry" ;; esac
case "$(cat "$RM")" in *"B515"*) ok "F5: ROADMAP (EN) lists it" ;; *) bad "F5: no ROADMAP (EN) entry" ;; esac
case "$(cat "$RMRU")" in *"B515"*) ok "F6: ROADMAP (RU) lists it" ;; *) bad "F6: no ROADMAP (RU) entry" ;; esac
# trap #11: `test -f` proves nothing about what was committed.
if git ls-files --error-unmatch scripts/check_b515_relay_accept_routes_observed.sh >/dev/null 2>&1; then
  ok "F7: this script is TRACKED by git (not merely present on disk)"
elif [ -n "${SKYGATE_B515_UNTRACKED_OK:-}" ]; then
  skip "F7: tracking check skipped (SKYGATE_B515_UNTRACKED_OK) — run before the commit that adds it"
else
  bad "F7: this script is not tracked by git — .gitignore may be eating it (trap #11)"
fi

echo
echo "B515 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
