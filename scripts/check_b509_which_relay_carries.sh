#!/usr/bin/env bash
# check_b509_which_relay_carries.sh — B509 (2026-11-25)
#
# THE MEASUREMENT DEFECT, not a product one, and the product statement that closes it.
#
# Asked "which exit node does this device go through?", the answer was given three times
# and each answer differed — because each was built on ONE echo endpoint, and an endpoint
# hosted behind a CDN whose range a relay advertises is carried by that relay's PREFIX, not
# by the selected exit node. The peer byte counters the earlier answers leaned on count
# prefix AND exit traffic together, so they cannot separate the two either.
#
# Measured once, properly, on the panel host with ONE exit node selected: 7 endpoints,
# 3 left through the selected relay and 3 through another one. The machine was fine; the
# question was under-specified. And the page where the choice is made said nothing about it.
#
# What is pinned here:
#   A. the tool exists, is tracked, is syntax-clean, is READ-ONLY, and probes a
#      HETEROGENEOUS endpoint set (the single-CDN measurement is what produced the wrong
#      answers — the check refuses a set that is all one CDN)
#   B. its prediction model is behaviourally correct on synthetic fixtures: a covering
#      prefix wins when an exit node is selected, a prefix that is not in effect (no exit
#      node + accept-routes off) is reported as such instead of pretended, and a
#      contradiction is reported WITHOUT failing the run (a finding is not an error)
#   C. /my/exit-nodes states the rule: the note is gated on the pinned-prefix count, the
#      count is passed by the handler, both catalogues carry the keys, and the count
#      reaches the template
#   D. the counting rule ignores the two catch-all prefixes (otherwise a tailnet with
#      nothing pinned starts explaining something that cannot happen to it)
#
# Every fixture in this file is synthetic (RFC 5737 / RFC 3849 addresses, invented relay
# names): a check must never carry a real address of the operator's tailnet.

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

TOOL=scripts/which_relay_carries.sh
TPL=internal/handlers/templates/user/exit_nodes.html
HANDLER=internal/feature/my/exit_nodes.go
RULE=internal/feature/my/prefix_override_b509.go
CAT=internal/i18n/catalog_exit_nodes.go
GATE=scripts/verify_pre_deploy.sh
NOTES=RELEASE-NOTES.md
AGENTS=AGENTS.md

for f in "$TOOL" "$TPL" "$HANDLER" "$RULE" "$CAT" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B509 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/b509.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

# ── A. the tool ─────────────────────────────────────────────────────────────
echo "[A] the measurement tool"

# A1: tracked by git, not merely present on this disk (AGENTS trap #11: .gitignore
#     silently ate a script that every local contract said existed).
if git ls-files --error-unmatch "$TOOL" >/dev/null 2>&1; then
  ok "A1: $TOOL is TRACKED by git (not merely on disk)"
else
  bad "A1: $TOOL is not tracked — a .gitignore rule would keep it out of the release"
fi

# A2: syntax.
if bash -n "$TOOL" 2>"$SKY_TMP/n.err"; then
  ok "A2: bash -n is clean"
else
  bad "A2: bash -n failed: $(tr '\n' ' ' <"$SKY_TMP/n.err")"
fi

# A3: READ-ONLY. Setting an exit node on a host someone is SSH'd into moves that host's
#     egress and can kill the session — the tool may inspect, never change.
case "$(grep -v '^[[:space:]]*#' "$TOOL")" in
  *"tailscale set"*|*"--accept-routes="*)
    bad "A3: the tool writes tailscale state (tailscale set / --accept-routes=) — it must be read-only" ;;
  *)
    ok "A3: the tool never runs 'tailscale set' (read-only by construction)" ;;
esac

# A4: --help works and is honest about WHAT it measures.
HELP_OUT="$(bash "$TOOL" --help 2>&1)"; HELP_RC=$?
if [ "$HELP_RC" -eq 0 ] && [ -n "$HELP_OUT" ]; then
  ok "A4: --help exits 0"
else
  bad "A4: --help exited $HELP_RC with no output"
fi
case "$HELP_OUT" in
  *"MACHINE IT RUNS ON"*|*"machine it runs on"*)
    ok "A4b: --help states that it measures THIS machine (the panel cannot observe a phone)" ;;
  *)
    bad "A4b: --help does not say it measures the machine it runs on" ;;
esac

# A5: the endpoint set is HETEROGENEOUS. This is the actual root cause of the three wrong
#     answers: measuring only a CDN endpoint attributes a relay's PREFIX traffic to the
#     selected exit node (or the reverse). A set that is all Cloudflare would reintroduce it.
ENDPOINTS="$(sed -n '/^DEFAULT_ENDPOINTS=(/,/^)/p' "$TOOL")"
EP_COUNT="$(printf '%s\n' "$ENDPOINTS" | grep -c 'https://' || true)"
if [ "${EP_COUNT:-0}" -ge 5 ]; then
  ok "A5: the default probe set has $EP_COUNT endpoints"
else
  bad "A5: the default probe set has ${EP_COUNT:-0} endpoints, want >=5 (one endpoint cannot answer this)"
fi
N_CDN=0
case "$ENDPOINTS" in *"api.ipify.org"*) N_CDN=$((N_CDN + 1)) ;; esac
case "$ENDPOINTS" in *"icanhazip.com"*) N_CDN=$((N_CDN + 1)) ;; esac
case "$ENDPOINTS" in *"ifconfig.co"*) N_CDN=$((N_CDN + 1)) ;; esac
case "$ENDPOINTS" in *"checkip.amazonaws.com"*) N_NON=1 ;; *) N_NON=0 ;; esac
case "$ENDPOINTS" in *"ipinfo.io/ip"*) N_NON=1 ;; esac
if [ "$N_CDN" -ge 1 ] && [ "$N_NON" -ge 1 ]; then
  ok "A5b: the set mixes CDN-hosted and non-CDN endpoints (the two answer differently)"
else
  bad "A5b: the set is homogeneous (cdn=$N_CDN non-cdn=$N_NON) — that is how the wrong answers were produced"
fi

# A6: no real address or hostname of the operator's tailnet may be committed here.
#     (The rule is repo-wide; this contract names the classes this tool was tempted by.)
if grep -Eq '(^|[^0-9])(100\.64\.|45\.152\.|193\.233\.|138\.124\.|213\.176\.|156\.67\.)' "$TOOL" ||
   grep -Eq 'skynas|svyatoslava' "$TOOL"; then
  bad "A6: the tool carries a real tailnet address or hostname"
else
  ok "A6: no real address/hostname is committed in the tool"
fi

# ── B. behaviour on synthetic fixtures ──────────────────────────────────────
echo "[B] the prediction model (hermetic fixtures)"

if ! command -v python3 >/dev/null 2>&1; then
  skip "B1-B6: python3 is not available in this environment (the tool needs it for JSON+CIDR)"
else
  cat >"$SKY_TMP/status.json" <<'JSON'
{"Peer":{
  "4":{"ID":4,"HostName":"relay-b","PrimaryRoutes":[],"ExitNodeOption":true},
  "11":{"ID":11,"HostName":"relay-a","PrimaryRoutes":["192.0.2.0/24"],"ExitNodeOption":true},
  "3":{"ID":3,"HostName":"relay-c","PrimaryRoutes":["203.0.113.64/26"],"ExitNodeOption":true}}}
JSON
  cat >"$SKY_TMP/prefs_exit.json" <<'JSON'
{"ExitNodeID":"4","RouteAll":false}
JSON
  cat >"$SKY_TMP/prefs_direct.json" <<'JSON'
{"ExitNodeID":"","RouteAll":false}
JSON
  # Three rows, chosen to exercise all three verdicts:
  #   row a: covered by relay-a's prefix, observed relay-a  -> yes
  #   row b: covered by relay-c's prefix, observed relay-b  -> NO (a real contradiction)
  #   row c: covered by nothing,        observed relay-b    -> yes (the exit node)
  cat >"$SKY_TMP/observed.tsv" <<'TSV'
https://example.org/a	192.0.2.55	198.51.100.10
https://example.org/b	203.0.113.70	203.0.113.20
https://example.org/c	203.0.113.200	203.0.113.20
TSV
  MAPS='relay-a=198.51.100.10,relay-b=203.0.113.20,relay-c=198.51.100.60'

  # B1: with an exit node selected, a covered destination is predicted to the prefix OWNER.
  OUT="$(SKYGATE_RELAY_PUBLIC_IPS="$MAPS" bash "$TOOL" \
    --status-file "$SKY_TMP/status.json" --prefs-file "$SKY_TMP/prefs_exit.json" \
    --observed-file "$SKY_TMP/observed.tsv" 2>&1)"; RC=$?
  case "$OUT" in
    *"192.0.2.55"*"relay-a"*) ok "B1: a covered destination is attributed to the prefix owner (relay-a)" ;;
    *) bad "B1: the covered destination was not attributed to its prefix owner. Output: $(printf '%s' "$OUT" | head -c 400)" ;;
  esac
  case "$OUT" in
    *"203.0.113.20"*) ok "B1b: the observed address is reported verbatim" ;;
    *) bad "B1b: the observed address is missing from the report" ;;
  esac

  # B2: the tool reports a CONTRADICTION and still exits 0 — a finding is not an error.
  [ "$RC" -eq 0 ] && ok "B2: a contradiction does not fail the run (rc=0)" || bad "B2: rc=$RC on a run that only reported findings"
  case "$OUT" in
    *"DIFFERENT RELAY"*) ok "B2b: the contradiction is named in words, not left to column-reading" ;;
    *) bad "B2b: no contradiction section in the output" ;;
  esac

  # B3: no exit node + accept-routes off -> the prefix is NOT in effect, and the tool says so
  #     instead of pretending it applied. (Measured live: egress was the host's own address.)
  OUT2="$(SKYGATE_RELAY_PUBLIC_IPS='self=198.51.100.99' bash "$TOOL" \
    --status-file "$SKY_TMP/status.json" --prefs-file "$SKY_TMP/prefs_direct.json" \
    --observed-file "$SKY_TMP/observed.tsv" 2>&1)"
  case "$OUT2" in
    *"not in effect"*) ok "B3: a prefix that cannot apply (no exit + accept-routes off) is explained, not faked" ;;
    *) bad "B3: a non-applying prefix was not explained. Output: $(printf '%s' "$OUT2" | head -c 400)" ;;
  esac
  case "$OUT2" in
    *"direct"*) ok "B3b: the egress is predicted as direct in that state" ;;
    *) bad "B3b: the direct case is not reported" ;;
  esac

  # B4: --fail-on-mismatch is the opt-in for scripts.
  SKYGATE_RELAY_PUBLIC_IPS="$MAPS" bash "$TOOL" --status-file "$SKY_TMP/status.json" \
    --prefs-file "$SKY_TMP/prefs_exit.json" --observed-file "$SKY_TMP/observed.tsv" \
    --fail-on-mismatch >/dev/null 2>&1; RC=$?
  [ "$RC" -eq 1 ] && ok "B4: --fail-on-mismatch exits 1 when a contradiction exists" \
                   || bad "B4: --fail-on-mismatch exited $RC, want 1"

  # B5: --json is machine-readable and carries the state + the per-endpoint verdicts.
  JSON_OUT="$(SKYGATE_RELAY_PUBLIC_IPS="$MAPS" bash "$TOOL" --status-file "$SKY_TMP/status.json" \
    --prefs-file "$SKY_TMP/prefs_exit.json" --observed-file "$SKY_TMP/observed.tsv" --json 2>&1)"
  case "$JSON_OUT" in
    *'"selected_exit_node": "relay-b"'*) ok "B5: --json reports the selected exit node" ;;
    *) bad "B5: --json does not report the selected exit node. Output: $(printf '%s' "$JSON_OUT" | head -c 300)" ;;
  esac
  case "$JSON_OUT" in
    *'"match": "yes"'*) ok "B5b: --json carries per-endpoint verdicts (match)" ;;
    *) bad "B5b: --json carries no per-endpoint verdicts" ;;
  esac

  # B6: the fixtures are synthetic (this file is committed, so it may not carry a real
  #     address of the operator's tailnet either).
  if grep -Eq '(^|[^0-9])(100\.64\.|45\.152\.|193\.233\.|138\.124\.|213\.176\.|156\.67\.)' "$0"; then
    bad "B6: this check carries a real tailnet address"
  else
    ok "B6: every fixture address here is synthetic (RFC 5737 / RFC 3849)"
  fi
fi

# ── C. the product statement on /my/exit-nodes ──────────────────────────────
echo "[C] the page states the rule"

# C1: the count is computed by the handler, not invented by the template.
case "$(cat "$HANDLER")" in
  *"countPinnedPrefixes(s.dbc())"*) ok "C1: the handler computes the pinned-prefix count from the assignment table" ;;
  *) bad "C1: the handler does not pass a pinned-prefix count" ;;
esac
case "$(cat "$HANDLER")" in
  *'"PinnedPrefixes":'*) ok "C1b: the count reaches the template as .PinnedPrefixes" ;;
  *) bad "C1b: .PinnedPrefixes is not passed to the template" ;;
esac

# C2: the template gates the note and renders the body as TRUSTED-MARKUP (th) — the B508
#     defect was the opposite: catalogue markup escaped into the page as literal tags.
case "$(cat "$TPL")" in
  *'{{if .PinnedPrefixes}}'*) ok "C2: the note is gated on the count (silent when nothing is pinned)" ;;
  *) bad "C2: the note is not gated on .PinnedPrefixes" ;;
esac
case "$(cat "$TPL")" in
  *'{{th "exit_nodes.prefix_override_body" .PinnedPrefixes}}'*)
    ok "C2b: the body is rendered with th (catalogue markup trusted, the count escaped)" ;;
  *) bad "C2b: the body is not rendered via th with the count" ;;
esac

# C3: both catalogues carry the keys, and the body expects exactly one %d.
RU_HITS="$(grep -c '"exit_nodes.prefix_override_\(title\|body\)"' "$CAT" || true)"
if [ "${RU_HITS:-0}" -ge 4 ]; then
  ok "C3: the keys exist in both catalogues ($RU_HITS entries)"
else
  bad "C3: expected 4 catalogue entries (title+body in RU and EN), found ${RU_HITS:-0}"
fi
case "$(cat "$CAT")" in
  *"закреплено %d префикс"*) ok "C3b: the RU body carries the count as %d" ;;
  *) bad "C3b: the RU body does not interpolate the count" ;;
esac
case "$(cat "$CAT")" in
  *"prefix(es) in this tailnet are pinned"*) ok "C3c: the EN body states the same rule" ;;
  *) bad "C3c: the EN body is missing or does not state the rule" ;;
esac
case "$(cat "$CAT")" in
  *"which_relay_carries.sh"*) ok "C3d: the note names the tool that verifies it" ;;
  *) bad "C3d: the note names no way to check the claim" ;;
esac

# C4: the counting rule ignores the catch-all prefixes.
case "$(cat "$RULE")" in
  *'case "0.0.0.0/0", "::/0":'*) ok "C4: the catch-all prefixes are excluded by name" ;;
  *) bad "C4: the counting rule does not exclude 0.0.0.0/0 and ::/0" ;;
esac
case "$(cat "$RULE")" in
  *"if d == nil"*) ok "C4b: a missing database answers 0 instead of failing the page" ;;
  *) bad "C4b: countPinnedPrefixes is not nil-safe" ;;
esac

# ── D. the Go halves run ────────────────────────────────────────────────────
echo "[D] behaviour"

if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/my/ -run 'TestB509' -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "D1: the counting-rule tests pass (filter TestB509 in internal/feature/my)"
  else
    bad "D1: go test ./internal/feature/my/ (filter TestB509) failed: $(printf '%s' "$OUT" | tail -c 400)"
  fi
  OUT="$(go test ./internal/handlers/ -run 'TestB509' -count=1 2>&1)"; RC=$?
  if [ "$RC" -eq 0 ]; then
    ok "D2: the render test passes (the count reaches the template)"
  else
    bad "D2: go test ./internal/handlers/ (filter TestB509) failed: $(printf '%s' "$OUT" | tail -c 400)"
  fi
else
  skip "D1-D2: no Go toolchain on PATH"
fi

# ── E. it is registered where a block must be ───────────────────────────────
echo "[E] registry"

case "$(cat "$GATE")" in
  *'run_check "B509"'*) ok "E1: registered in scripts/verify_pre_deploy.sh" ;;
  *) bad "E1: B509 is not registered in the gate (a block without a contract is a rumour)" ;;
esac
case "$(cat "$AGENTS")" in
  *"**B509**"*) ok "E2: one-line entry in the AGENTS.md block index" ;;
  *) bad "E2: no AGENTS.md block-index entry" ;;
esac
if [ -f "$NOTES" ] && grep -q 'which_relay_carries' "$NOTES"; then
  ok "E3: the release notes explain the tool and the rule"
else
  bad "E3: RELEASE-NOTES.md does not mention the tool"
fi

echo
echo "B509 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
