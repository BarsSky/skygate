#!/usr/bin/env bash
# ============================================================================
# check_b480_ownership_policy.sh — B480
#
# «ЕСЛИ ПРАВИЛО ЗАРЕЗЕРВИРОВАНО ЗА EXIT NODE, ОСТАЛЬНЫЕ ПРИНИМАЮТ ЭТУ ПОЛИТИКУ»
#
# The operator stated the policy twice; this block is the code for it. Measured
# 2026-11-16 on the live installation: `basic` (exit node karolina, all 164 Discord
# rules naming karolina) and `skyworker` (opted OUT of the exit-node preference, rules
# naming emilia) claimed the same 17 Discord /32s. `prefixowner.Assign` counted both
# claims equally, the 1:1 tie broke alphabetically to emilia, and `basic` lost exactly
# those addresses while the rest of Discord worked. On top of that, adding a rule for a
# target that was already covered wrote a SECOND row when the relay differed, because
# the dedup key included the relay (`FindDeviceRuleID`), so two relays' rules competed
# for one prefix and a user needing another relay had no sanctioned way to ask.
#
# Contracts:
#   A. an inert claim (device with no exit-node choice) does not vote
#   B. an existing reservation is ACCEPTED, not re-decided, while it has a voter
#   C. a reservation whose voters are gone is re-decided (the Discord case)
#   D. a prefix nobody votes for inherits the ENCLOSING reservation (not auto-spread)
#   E. one rule per target: the any-relay dedup exists and the writer consults it
#   F. tests pin all of it, and the predicates discriminate the pre-B480 shape
#   G. registered in the gate and in the block index
#   H. the operator can find the symptom + the duplicate cleanup script (EN + RU)
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b480.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

PO=internal/prefixowner/prefixowner.go
POTEST=internal/prefixowner/prefixowner_b480_test.go
DBF=internal/db/device_rules.go
DBTEST=internal/db/device_rules_b480_test.go
STORE=internal/feature/exit_rules/store.go
CLEANUP=scripts/b480_dup_rule_cleanup.sh
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
DOC=docs/troubleshooting.md
RUDOC=docs/ru/prefix-ownership-acceptance.md
DOCIDX=docs/README.md
RUDOCIDX=docs/ru/README.md

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$PO" "$POTEST" "$DBF" "$DBTEST" "$STORE" "$CLEANUP" "$VERIFY" "$AGENTS" "$DOC" "$RUDOC" "$DOCIDX" "$RUDOCIDX"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the inert claim ------------------------------------------------------
if has "$PO" 'Inert bool'; then
  ok "A1: a claim can be marked inert (device did not choose an exit node)"
else
  bad "A1: claims have no notion of an inert device — such a claim still votes"
fi
if has "$PO" 'func loadInertDevices(' && has "$PO" 'opted_out = 1'; then
  ok "A2: inert devices are read from device_exit_node_prefs (opted_out / empty tag)"
else
  bad "A2: inert devices are not detected"
fi
if has "$PO" 'c.ExitNode != "" && !c.Inert'; then
  ok "A3: only a non-inert claim counts as a vote"
else
  bad "A3: inert claims still vote"
fi

# --- B/C: acceptance vs re-decision -----------------------------------------
if has "$PO" 'ACCEPTANCE' || has "$PO" 'reservation is ACCEPTED' || has "$PO" 'keeps its prefix'; then
  ok "B1: the reservation pass is documented where it runs"
else
  bad "B1: the acceptance rule is not recorded in the engine"
fi
if has "$PO" 'g.perNode[e.ExitNode] == 0'; then
  ok "B2: a reservation without a single voter is NOT protected (re-decided)"
else
  bad "B2: stale reservations would be protected forever"
fi

# --- D: inheritance ----------------------------------------------------------
if has "$PO" 'func mostSpecificEnclosing(' && has "$PO" 'netip.ParsePrefix'; then
  ok "D1: nested prefixes can inherit the enclosing range's owner"
else
  bad "D1: no inheritance helper — a /32 with no voter is auto-spread"
fi
if has "$PO" 'a.Source == "explicit" || a.Source == "manual" || a.Source == "global"'; then
  ok "D2: only a RESERVATION is inherited (an auto-spread range is not)"
else
  bad "D2: inheritance would override B275's even spread"
fi
if has "$PO" 'g.votes == 0'; then
  ok "D3: inheritance applies only where nothing voted"
else
  bad "D3: inheritance could move a prefix someone actually voted for"
fi

# --- E: one rule per target ---------------------------------------------------
if has "$DBF" 'func FindDeviceRuleIDAnyRelay('; then
  ok "E1: an any-relay dedup lookup exists"
else
  bad "E1: the only lookup keys on the relay, so a duplicate row is written"
fi
if has "$DBF" 'ORDER BY enabled DESC, id ASC LIMIT 1'; then
  ok "E2: the lookup is stable (oldest enabled row wins)"
else
  bad "E2: the lookup is not deterministic"
fi
if has "$STORE" 'db.FindDeviceRuleIDAnyRelay' && has "$STORE" 'NOT adding a duplicate'; then
  ok "E3: the rule writer refuses to add a duplicate for another relay"
else
  bad "E3: the form path can still write a second row for the same target"
fi
if has "$STORE" 'moving it is a request'; then
  ok "E4: the refusal names the sanctioned path (the B440 request)"
else
  bad "E4: the user is not told how to move a rule"
fi

# --- F: tests ----------------------------------------------------------------
for _t in TestB480_InertClaimDoesNotMoveTheReservation TestB480_ReservationIsAcceptedNotReDecided \
          TestB480_ReservationWithNoVotersIsReDecided TestB480_NestedPrefixInheritsTheEnclosingReservation \
          TestB480_ManualBeatsInheritance TestB480_AutoSpreadIsNotInherited; do
  if has "$POTEST" "$_t"; then ok "F1: $_t exists"; else bad "F1: $_t is missing"; fi
done
if has "$DBTEST" 'TestB480_FindDeviceRuleIDAnyRelay'; then
  ok "F1b: the any-relay lookup has a test"
else
  bad "F1b: the lookup is untested"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/prefixowner/ ./internal/db/ -run TestB480 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] && case "$OUT" in *"ok "*) true;; *) false;; esac; then
    ok "F2: go test ./internal/prefixowner/ ./internal/db/ -run TestB480 passes"
  else
    bad "F2: B480 tests fail or did not run: $(printf '%s' "$OUT" | tail -3)"
  fi
else
  printf '  \033[33m-\033[0m F2: SKIP — no go toolchain on PATH (%s)\n' "${BASH:-bash}"
fi

# --- G: registration ---------------------------------------------------------
if has "$VERIFY" '"B480"'; then ok "G1: the contract is registered in the gate"; else bad "G1: unregistered"; fi
if has "$AGENTS" 'B480'; then ok "G2: the block index carries the entry"; else bad "G2: not in the index"; fi
if [ -x "$CLEANUP" ] || grep -q 'b480_dup_rule_cleanup' "$VERIFY"; then
  ok "G3: the duplicate-rule cleanup reports itself in the catalog"
else
  bad "G3: the cleanup script is not reachable from the catalog"
fi

# --- H: the operator can find it --------------------------------------------
if has "$DOC" 'B480'; then ok "H1: the EN troubleshooting guide covers the policy"; else bad "H1: EN docs missing"; fi
if has "$RUDOC" 'B480' && has "$RUDOC" 'зарезервирован'; then
  ok "H2: the RU doc states the reservation policy"
else
  bad "H2: RU docs missing"
fi
if has "$DOCIDX" 'prefix-ownership-acceptance.md' && has "$RUDOCIDX" 'prefix-ownership-acceptance.md'; then
  ok "H3: both catalogues index the RU doc"
else
  bad "H3: RU doc not indexed"
fi

# --- I: the predicates discriminate the pre-B480 shape ----------------------
cat > "$SKY_TMP/old.go" <<'EOF'
type Claim struct {
	Prefix   string
	ExitNode string
	DeviceID int
}
	if c.ExitNode != "" {
		g.perNode[c.ExitNode]++
	}
EOF
discriminates=1
has "$PO" 'Inert bool' || discriminates=0
has "$PO" 'c.ExitNode != "" && !c.Inert' || discriminates=0
has "$PO" 'mostSpecificEnclosing' || discriminates=0
for _m in 'Inert bool' '!c.Inert' 'mostSpecificEnclosing'; do
  has "$SKY_TMP/old.go" "$_m" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "I1: every predicate is present now and absent from the pre-B480 shape"
else
  bad "I1: the predicates do not discriminate the pre-B480 shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB480: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB480: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
