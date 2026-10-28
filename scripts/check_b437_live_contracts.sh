#!/usr/bin/env bash
# check_b437_live_contracts.sh — B437 (2026-10-28)
#
# «четыре КРАСНЫХ строки в гейте на рабочей установке» — a gate that cries wolf on a
# healthy system teaches the operator to ignore the colour, so the four live-state
# contracts B433 measured red are re-derived here from the architecture that exists
# instead of from the state of one tailnet on one afternoon.
#
# WHAT WAS MEASURED (reference VM, 2026-10-28, this tree)
#
#   1. B118 section G froze the FOUR relay names of the reference deployment
#      (`emilia, karolina, sharlotta, skygate-host`) and the counts "exactly 4". Two
#      more real relays (`aisha`, `svyatoslava`) had joined the tailnet, all six are
#      genuine infra nodes owned by `infra@`, so four FAIL rows fired on a healthy
#      system. Rewritten: the sanctioned set is now derived from headscale's live
#      node list (a tag is sanctioned iff its node exists), and the counts assert that
#      the two writers — the applied policy and node_owner_map — AGREE. Verified on
#      the VM: 22 pass / 0 fail, six relays.
#
#   2. B188.2 S/V/W hardcoded "exactly one pinned autogroup:internet grant" for two
#      named devices. The generator's rule changed twice underneath them (B356: the
#      preferred relay must be USABLE; B398: a DERIVED preference must not become a
#      permission filter). Live: `basic` has `via_enabled=1` with `set_by_user_id=0`,
#      i.e. exactly the case B398 exists for — so 0 pins is CORRECT and the contract
#      called a working policy a failure. Rewritten: all three ask
#      scripts/lib/acl_pin_expectation.sh, which states the generator's rule ONCE in
#      SQL. Verified on the VM: 20 pass / 0 fail.
#
#   3. B188.3 J runs B188.2 — so it was red only as a consequence, and it also hardcoded
#      /home/skyadmin/skygate, which made it read a DIFFERENT tree than the one under
#      test (measured: it ran the operator's v1.5.184 scripts while the tree under test
#      was a fresh clone). Both scripts now honour $SKYGATE_REPO. Verified: 12 pass / 0.
#
#   4. B243's orphan row (node_id=158, `svyatoslava`, headscale_user_id=2147455555) was
#      filed as "an impossible / overflowed id (T240)". It is neither: 2147455555 is the
#      id headscale reports as the user of EVERY tag-owned node (its `tagged-devices`
#      pseudo-user). The row is still wrong — it must carry the `infra` user's id — but
#      nothing repaired it: BackfillInfra's UPDATE only touched rows still sitting in a
#      user-portal bucket, and this row was already attributed to infra. FIXED IN THE
#      PRODUCT (internal/nodeownership/auto.go): the pass now re-asserts the infra
#      user's id on the rows it owns. The live row clears on the first reconcile tick
#      after the update, and this contract asserts the rule is in the source, with a
#      planted-snippet proof (the same shape B434 used for its context bug).
#
# CONTRACTS: 20.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

GATE=scripts/verify_pre_deploy.sh
LIB=scripts/lib/acl_pin_expectation.sh
B118=scripts/check_b118.sh
B1882=scripts/check_b188_2.sh
B1883=scripts/check_b188_3.sh
ORPH=scripts/check_b_node_owner_map_orphans.sh
for f in "$GATE" "$LIB" "$B118" "$B1882" "$B1883" "$ORPH"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
printf '  DIAG B437: lib=%s b118=%s b1882=%s orphans=%s pwd=%s\n' "$LIB" "$B118" "$B1882" "$ORPH" "$PWD"

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b437.XXXXXX" 2>/dev/null || true)"
if [ -z "${WORK:-}" ] || [ ! -d "$WORK" ]; then
  bad "cannot create a scratch directory under \$PWD/tmp (B340)"
  exit 1
fi
trap 'rm -rf "$WORK"' EXIT

has() { # has <file> <fixed-string>
  case "$(cat "$1" 2>/dev/null)" in
    *"$2"*) return 0 ;;
    *) return 1 ;;
  esac
}

hdr "A. the pin expectation lives in ONE place, and it is the generator's rule"
if bash -n "$LIB" 2>/dev/null; then
  ok "A1: the shared helper parses"
else
  bad "A1: $LIB does not parse"
fi
if git ls-files --error-unmatch "$LIB" >/dev/null 2>&1; then
  ok "A2: the helper is tracked by git (trap 11)"
else
  bad "A2: the helper is not tracked by git — a local-only helper is not shipped"
fi
# Each of the three generator conditions the old contracts missed or duplicated.
MISSING=""
has "$LIB" "set_by_user_id > 0"            || MISSING="$MISSING b398(set_by_user_id)"
has "$LIB" "LOWER(r.hostname) = LOWER(p.device_hostname)" || MISSING="$MISSING b284(tag-from-node_owner_map)"
has "$LIB" "exit_node_health"              || MISSING="$MISSING b356(relay-usable)"
has "$LIB" "via_enabled = 1"               || MISSING="$MISSING opt-in(via_enabled)"
if [ -z "$MISSING" ]; then
  ok "A3: the helper encodes opt-in, the node_owner_map tag (B284), the derived-pref rule (B398) and relay health (B356)"
else
  bad "A3: the helper is missing:$MISSING — a contract that restates the rule in its own words drifts from it"
fi
if has "$B1882" 'lib/acl_pin_expectation.sh'; then
  ok "A4: B188.2 sources the helper instead of carrying its own SQL"
else
  bad "A4: B188.2 does not source $LIB"
fi
# The old frozen numbers must be GONE, or the drift comes back the moment they are
# read again: S/V said "expected exactly 1", W compared with a via_enabled count.
case "$(cat "$B1882")" in
  *'check_eq "S-per-device-autogroup-pinned-once" "1"'*)
    bad "A5: S still hardcodes 'exactly 1' — the B398 case (a derived preference) makes that wrong" ;;
  *'W_EXPECT=$(skygate_live_db_query'*)
    bad "A5: W still carries its own expectation query — it must ask the shared helper" ;;
  *)
    if has "$B1882" 'skygate_pin_expectation_for_tag' && has "$B1882" 'skygate_pinned_catchall_total'; then
      ok "A5: S, V and W ask the helper (no frozen 1, no private query)"
    else
      bad "A5: B188.2 does not use the helper's two entry points"
    fi ;;
esac

hdr "B. B118's sanctioned set is derived from headscale, not from one tailnet"
if has "$B118" 'headscale nodes list -o json'; then
  ok "B1: the sanctioned infra tags come from headscale's live node list"
else
  bad "B1: B118 does not read the live node list — the sanctioned set is frozen again"
fi
case "$(cat "$B118")" in
  *"SANCTIONED=\"'tag:dev-infra-"*)
    bad "B2: the frozen four-name SANCTIONED list is back (a new relay would redden a healthy gate)" ;;
  *) ok "B2: no frozen relay-name list remains" ;;
esac
if has "$B118" 'policy and node_owner_map'; then
  ok "B3: the counts became an AGREEMENT between the two writers (policy vs database)"
else
  bad "B3: B118 lost the policy/database agreement assertion"
fi
if has "$B118" "tag LIKE '%-legacy%'"; then
  ok "B4: a legacy/mirror marker is still a red row whatever headscale says"
else
  bad "B4: the legacy-marker assertion is gone"
fi

hdr "C. the orphan rule is in the PRODUCT, and the test can catch its removal"
SRC=internal/nodeownership/auto.go
TEST=internal/nodeownership/infra_test.go
if [ -f "$SRC" ]; then
  if has "$SRC" "WHERE username = 'infra'" && has "$SRC" 'headscale_user_id <> $1'; then
    ok "C1: BackfillInfra re-asserts the infra user id on the rows it owns"
  else
    bad "C1: the infra id repair is missing — a stray sentinel would survive every tick"
  fi
  # Narrowness is the safety property: the repair must never touch another owner.
  if has "$SRC" "SET headscale_user_id = \$1"; then
    ok "C2: the repair sets one column only (it cannot move an operator's ownership decision)"
  else
    bad "C2: the repair does not look like a one-column id assertion"
  fi
else
  bad "C1/C2: $SRC is missing"
fi
if [ -f "$TEST" ] && has "$TEST" 'TestB437_BackfillInfraRepairsAStrayInfraUserID'; then
  ok "C3: a test pins the repair (sentinel row repaired, foreign owner untouched)"
else
  bad "C3: no test pins the repair"
fi
# Prove the contract itself can fail: plant the OLD statement shape (restricted to
# user-portal buckets) in a snippet and require the detector to call it broken.
cat > "$WORK/old_shape.go" <<'GO'
package nodeownership
func plant(dbConn db.DBSource, infraHSID int64) {
	_, _ = dbConn.Current().Exec(
		`UPDATE node_owner_map SET headscale_user_id = $1 WHERE node_id = $2
		   AND username IN (SELECT username FROM portal_users WHERE username != 'infra')`,
		infraHSID, "158")
}
GO
if has "$WORK/old_shape.go" "WHERE username = 'infra'"; then
  bad "C4: the planted pre-B437 shape satisfies the detector — the detector is too loose"
else
  if has "$WORK/old_shape.go" "username != 'infra'"; then
    ok "C4: the pre-B437 shape (rows in user-portal buckets only) does NOT satisfy the repair rule"
  else
    bad "C4: the planted snippet is not the shape it claims to be"
  fi
fi

hdr "D. the other two contracts keep their live sections (no silent deletion as a fix)"
if has "$B1882" 'S-per-device-autogroup-pin-matches-the-generator-rule' \
   && has "$B1882" 'V-a71-pin-matches-the-generator-rule' \
   && has "$B1882" 'W-tagged-device-pins-equal-the-generator-rule'; then
  ok "D1: S, V and W still exist as their own live assertions"
else
  bad "D1: one of S/V/W was dropped instead of re-derived"
fi
if has "$B1883" 'J-B188.2-still-passes-after-refactor'; then
  ok "D2: B188.3 J still re-runs B188.2"
else
  bad "D2: B188.3's J contract is gone"
fi
if has "$B1882" 'SKYGATE_REPO' && has "$B1883" 'SKYGATE_REPO'; then
  ok "D3: both scripts honour \$SKYGATE_REPO (a hardcoded /home/skyadmin path reads another tree)"
else
  bad "D3: the checkout path is hardcoded again — the two checks can inspect different trees"
fi
if has "$ORPH" 'tagged-devices'; then
  ok "D4: the orphan hint names headscale's tagged-devices sentinel instead of calling it an overflow"
else
  bad "D4: the orphan hint still sends the operator to T240 for a known, self-healing condition"
fi

hdr "E. registered, indexed, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b437_live_contracts.sh >/dev/null 2>&1; then
  ok "E1: this script is tracked by git"
else
  bad "E1: this script is not tracked by git"
fi
if grep -qF 'check_b437_live_contracts' "$GATE" 2>/dev/null; then
  ok "E2: registered in scripts/verify_pre_deploy.sh"
else
  bad "E2: not registered in the catalog"
fi
if grep -q '^\- \*\*B437\*\*' AGENTS.md 2>/dev/null; then
  ok "E3: one-line entry in the AGENTS.md block index"
else
  bad "E3: no B437 entry in the AGENTS block index"
fi
B437_DESC="$(grep -A3 'run_check "B437"' "$GATE" 2>/dev/null)"
case "$B437_DESC" in
  *'`'*) bad "E4: the B437 catalog description contains a backtick (TD-15: it is evaluated by bash)" ;;
  "")    bad "E4: the B437 catalog entry was not found" ;;
  *)     ok "E4: the B437 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB437 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
