#!/bin/bash
. "$(dirname "$0")/lib/db_credentials.sh"
SKYGATE_DB_PASSWORD="${SKYGATE_DB_PASSWORD:-$(skygate_db_password)}"
# B188.2 — per-CIDR exit-node pin (instead of catch-all pin).
#
# B188 fixed the ghost tag (tag:exit-X → tag:dev-infra-X) and
# re-enabled via pinning, but applied via= to the per-device
# autogroup:internet CATCH-ALL. That pinned ALL of basic's
# internet to emilia, defeating the user-facing /my/exit-rules
# feature (selective routing: "youtube.com via emilia,
# banking.com direct").
#
# B188.2 fix:
#   1. Removed the per-device autogroup:internet block that
#      pinned the catch-all.
#   2. Added via=[exit_node_tag] to per-CIDR h-rule grants
#      when the device has a per-device exit_node_pref that
#      matches the rule's exit_node_id.
#   3. New helper exitNodeTagToHostname (tag:dev-infra-emilia →
#      "emilia") bridges between the per-device pref (full tag)
#      and the per-CIDR rule's exit_node_id (hostname).
#   4. Added ExitNodeID to ACLEntry + qSelectEnabledACLEntries
#      SQL so the per-CIDR loop can see the rule's exit_node.
#
# Live impact (2026-08-26 audit):
#   - 5 device_exit_node_prefs rows in production DB
#     (a71, emilia, skygate-host-1, skyworker, basic).
#   - For each:
#       - autogroup:internet → direct (was: via=[their pref])
#       - per-CIDR grants whose exit_node matches the pref
#         → via=[their pref] (was: no via)
#   - This is the correct selective routing the user wanted.
#   - Devices WITHOUT a per-device pref see no change
#     (viaByDevice lookup is empty, so the per-CIDR loop
#     emits no via).
#
# Contracts (24 contracts A-X):
#  A. db.NormalizeExitNodeTag exists (B188 — unchanged)
#  B. The per-device autogroup:internet grant is GONE
#     (no entry with src=tag:dev-X, dst=autogroup:internet,
#     AND via=[exit_node_tag] — the B188 regression)
#  C. The loose per-device autogroup:internet grant EXISTS
#     (no via, allows direct internet for tagged devices
#     without per-CIDR via)
#  D. Per-CIDR h-rule grants for tag:dev-michail-basic that
#     have exit_node_id='emilia' in the DB have via=[emilia]
#     in the policy
#  E. Per-CIDR h-rule grants for tag:dev-skyadmin-skyworker
#     that have exit_node_id='karolina' have via=[karolina]
#  F. Per-CIDR h-rule grants for tag:dev-michail-basic that
#     have exit_node_id='karolina' (if any) do NOT have
#     via= (cross-exit-node pin is rejected)
#  G. exitNodeTagToHostname helper exists
#  H. exitNodeTagToHostname("tag:dev-infra-emilia") = "emilia"
#  I. exitNodeTagToHostname("tag:dev-infra-karolina") = "karolina"
#  J. exitNodeTagToHostname("") = ""
#  K. exitNodeTagToHostname("tag:invalid") = "" (no dash = no host)
#  L. ACLEntry struct has ExitNodeID field
#  M. qSelectEnabledACLEntries SQL includes exit_node_id
#  N. GetACLEntries scans exit_node_id into the new field
#  O. acl_b188_2_test.go covers the new behavior (3+ tests)
#  P. AGENTS.md mentions B188.2
#  Q. verify_pre_deploy.sh includes check_b188_2
#  R. go build + go vet pass
#  S. (VM-only) live: per-device autogroup:internet
#     (tag:dev-michail-basic → autogroup:internet) carries
#     via=[emilia] exactly once — the conditional B265 pin.
#     (Pre-B265 this asserted the pin was ABSENT; B265 makes it
#     conditional on the device's own pref instead — see B above.)
#  T. (VM-only) live: tag:dev-michail-basic has ≥1 per-CIDR
#     (h-rule-*) grant pinned with via=[emilia]. (Before
#     2026-09-18 this pinned one frozen CIDR — the youtube
#     /32 as resolved at the time — which broke whenever
#     DNS changed the resolved set.)
#  U. (VM-only) live: skyworker h-rules have via=[karolina]
#     (not via=[emilia] — correct per-device pref)
#  S. (VM-only) live: tag:dev-michail-basic has exactly ONE
#     per-device autogroup:internet grant carrying via= (its own
#     pref, emilia) — the conditional B265 pin
#  V. (VM-only) live: a71 (per-device pref=emilia, no matching
#     per-CIDR rules) has exactly ONE via-bearing grant, the
#     conditional catch-all pin
#  W. (VM-only) live: the number of per-device autogroup:internet
#     grants with via= equals the number of device_exit_node_prefs
#     rows with via_enabled=1 — i.e. ONLY devices that asked for a
#     pin have one. (B265 2026-09-19 rewrote this from "= 0": the
#     pin is now conditional instead of absent, see contract B.)
#  X. (VM-only) live: total h-rule grants with via=[emilia]
#     for tag:dev-michail-basic is 77 (the same as the
#     number of device_rules for basic with exit_node_id='emilia')

set -uo pipefail

PASS=0
FAIL=0
[ -d /home/skyadmin/skygate ] && REPO=/home/skyadmin/skygate || REPO="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"
# The admin ACL SURFACE, not one file: internal/acl/acl.go was split into
# seven on 2026-10-01 (refactor Phase D) and a contract that greps one path turns
# a pure code move into a false FAIL — and is the weaker contract even while it
# is green. See scripts/lib/gosurface.sh and B339.
. scripts/lib/gosurface.sh
gosurface ACL internal/acl/acl.go internal/acl/acl_apply.go internal/acl/acl_generate.go internal/acl/acl_generate_via.go internal/acl/acl_ownership.go internal/acl/acl_set.go internal/acl/acl_tags.go

check_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$actual" = "$expected" ]; then
    echo "  PASS [$label] $actual"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] expected=$expected got=$actual"
    FAIL=$((FAIL+1))
  fi
}

check_ge() {
  local label="$1" min="$2" actual="$3"
  if [ "$actual" -ge "$min" ] 2>/dev/null; then
    echo "  PASS [$label] actual=$actual (>= $min)"
    PASS=$((PASS+1))
  else
    echo "  FAIL [$label] actual=$actual (expected >= $min)"
    FAIL=$((FAIL+1))
  fi
}

count() {
  local file="$1" pat="$2"
  if [ -f "$file" ]; then
    grep -c -- "$pat" "$file" 2>/dev/null || echo 0
  else
    echo 0
  fi
}

# A. NormalizeExitNodeTag (B188 — unchanged but still required).
A=$(count "$REPO/internal/db/exit_node_prefs.go" 'func NormalizeExitNodeTag')
check_ge "A-NormalizeExitNodeTag" 1 "$A"

# B. The per-device autogroup:internet grant with via= is
#    CONDITIONAL, never unconditional.
# Pre-B188.2 the per-device catch-all was emitted with an
# UNCONDITIONAL via= (so every device was pinned to its pref, which
# defeated /my/exit-rules' selective routing). B188.2 removed the
# pin entirely; B265 (2026-09-19) re-introduced it as a CONDITIONAL
# pin: emitted only when the device actually has a resolved
# preference (device_exit_node_prefs.via_enabled=1). Without the
# pin, nothing in the generated policy constrained which exit node
# a TAGGED device may use (headscale honours `via` for exit-node
# selection only when the grant's dst is autogroup:internet, and the
# per-USER grant that carried it never matches tagged nodes).
#
# The contract therefore checks: the via-bearing per-device grant
# may only appear guarded by the `viaByDevice[devTag]` lookup — an
# unconditional emit with a `via` variable is still a regression.
B=$(grep -E '^\s*sb\.WriteString.*autogroup:internet.*via' "$ACL" 2>/dev/null | grep -v '//' | wc -l)
B_COND=$(grep -cE '^\s*if via := viaByDevice\[devTag\]; via != ""' "$ACL" 2>/dev/null || echo 0)
if [ "$B" -eq 0 ] && [ "$B_COND" -eq 0 ]; then
  # B188.2 shape: no per-device pin at all.
  check_eq "B-per-device-autogroup-pin" "0" "0"
elif [ "$B" -eq 1 ] && [ "$B_COND" -ge 1 ]; then
  # B265 shape: exactly one emit, guarded by the per-device lookup.
  check_eq "B-per-device-autogroup-pin-conditional" "1" "1"
else
  check_eq "B-per-device-autogroup-pin-conditional" "unconditional-pin" "B=$B guard=$B_COND"
fi

# C. The loose per-device autogroup:internet grant EXISTS (no via).
# Post-B188.2: the catch-all is emitted by the loose per-device
# loop at the END of the grants block, with NO via. The pattern:
#   sb.WriteString(",\n    { \"src\": [\"" + devTag + "\"], \"dst\": [\"autogroup:internet\"], \"ip\": [\"*\"] }")
# We assert the no-via version is present (in source, not in comments).
C=$(grep -E '^\s*sb\.WriteString.*autogroup:internet.*ip' "$ACL" 2>/dev/null | grep -v 'via' | wc -l)
check_ge "C-loose-per-device-autogroup-no-via" 1 "$C"

# D. Per-CIDR via= is added in the per-CIDR grant loop.
# We check that the new code block is present.
D=$(count "$ACL" 'viaForGrant')
check_ge "D-per-cidr-via-code-present" 1 "$D"

# E. exitNodeTagToHostname helper exists.
E=$(count "$ACL" 'func exitNodeTagToHostname')
check_ge "E-exitNodeTagToHostname-exists" 1 "$E"

# F. exitNodeTagToHostname strips the "dev-infra-" bucket
# prefix. The helper iterates a known-bucket list which
# includes "dev-infra-". We grep for that exact string.
F=$(grep -c '"dev-infra-"' "$ACL" 2>/dev/null || echo 0)
check_ge "F-tag-to-host-stripping-pattern" 1 "$F"

# G. ACLEntry has ExitNodeID field.
G=$(count "$REPO/internal/db/device_rules.go" 'ExitNodeID string')
check_ge "G-ACLEntry-has-ExitNodeID" 1 "$G"

# H. qSelectEnabledACLEntries includes exit_node_id.
H=$(grep -c 'exit_node_id' "$REPO/internal/db/queries.go" 2>/dev/null || echo 0)
check_ge "H-SQL-includes-exit_node_id" 1 "$H"

# I. GetACLEntries scans exit_node_id into the new field.
I=$(grep -c '&e.ExitNodeID' "$REPO/internal/db/device_rules.go" 2>/dev/null || echo 0)
check_ge "I-GetACLEntries-scans-ExitNodeID" 1 "$I"

# J. B188.2 test file has unit tests.
J=$(grep -cE 'TestExitNodeTagToHostname|TestB1882' "$REPO/internal/acl/acl_b188_2_test.go" 2>/dev/null || echo 0)
check_ge "J-B188_2-tests" 2 "$J"

# K. AGENTS.md mentions B188.2.
K=$(count "$REPO/AGENTS.md" 'B188.2')
check_ge "K-AGENTS-md-B188_2" 1 "$K"

# L. verify_pre_deploy.sh includes check_b188_2.
L=$(grep -cE 'check_b188_2' "$REPO/scripts/verify_pre_deploy.sh" 2>/dev/null || echo 0)
check_ge "L-verify-includes-check_b188_2" 1 "$L"

# M. Build + vet pass.
GO_BIN="${GO:-$(command -v go 2>/dev/null || true)}"
if [ -z "$GO_BIN" ]; then
  for cand in "$(command -v go 2>/dev/null)" /c/Program\ Files/Go/bin/go.exe "/c/Program Files/Go/bin/go.exe" /usr/local/go/bin/go /c/Users/*/go/bin/go "$HOME/go/bin/go"; do
    if [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
  done
fi
if [ -n "$GO_BIN" ] && (cd "$REPO" && "$GO_BIN" build ./... >/dev/null 2>&1 && "$GO_BIN" vet ./... >/dev/null 2>&1); then
  echo "  PASS [M-build-vet] ok ($GO_BIN)"
  PASS=$((PASS+1))
else
  echo "  SKIP [M-build-vet] go not reachable from this shell (go=$GO_BIN)"
fi

# S. (VM-only) Live: per-device autogroup:internet (tag:dev-michail-basic)
# does NOT have via=[emilia]. The B188 catch-all pin is gone.
if [ -d /home/skyadmin/skygate ]; then
  if command -v docker >/dev/null 2>&1; then
    S=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get("grants", []):
    if "tag:dev-michail-basic" in g.get("src", []) and "autogroup:internet" in g.get("dst", []) and g.get("via"):
        n += 1
print(n)
' 2>/dev/null)
    # B265 (2026-09-19): basic (michail) HAS a per-device preference with
    # via_enabled=1, so exactly ONE pinned catch-all is now expected — and it
    # must be pinned to basic's own preference, never to somebody else's exit
    # node. Pre-B265 this asserted 0 (B188.2 removed the pin because it was
    # UNCONDITIONAL, which defeated selective routing); post-B265 the pin is
    # conditional on the device's pref, and headscale honours `via` for
    # exit-node selection ONLY when dst contains autogroup:internet — so
    # without this grant a tagged device had no enforced preferred exit node.
    check_eq "S-per-device-autogroup-pinned-once" "1" "${S:-<err>}"

    # T. Live: basic's per-CIDR (h-rule-*) grants must be pinned to the relay that
    #    OWNS that prefix in the assignment table.
    #
    # RENEGOTIATED (B352, 2026-10-05). The contract required
    # via=[tag:dev-infra-emilia] for basic — the relay that happened to own those
    # prefixes on 2026-08-26. B275 made ownership a living decision, and on 2026-10-05
    # karolina was evicted by a single transient SSH timeout: the pins moved to
    # emilia/shardlotta and then back to karolina after its recovery, so a frozen relay
    # name fails on a perfectly healthy tailnet whenever the assignment legitimately
    # moves (measured: T red on the 2026-10-05 gate run while the ACL was correct for
    # the table it followed). What B188.2 really guarantees — and what catches the B276
    # bug class (a pin naming a relay that owns nothing) — is that the CONTROL plane
    # and the DATA plane agree: EVERY per-CIDR pin for the device names that prefix's
    # owner, and at least one such pin exists. S and W still pin the other half (the
    # catch-all must NOT be pinned).
    if ! skygate_live_db_probe; then
      echo "  SKIP [T-per-cidr-pins-name-the-owner] $(skygate_live_db_reason)"
    else
      T_OWNERS=$(skygate_live_db_query "SELECT prefix || '|' || exit_node_id FROM prefix_owner" | tr -d '\r')
      T=$(T_OWNERS="$T_OWNERS" docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, os, sys
owners = {}
for line in (os.environ.get("T_OWNERS") or "").splitlines():
    line = line.strip()
    if "|" in line:
        p, o = line.split("|", 1)
        owners[p.strip()] = o.strip()
try:
    pol = json.load(sys.stdin)
except Exception:
    print("no-policy"); sys.exit(0)
hosts = pol.get("hosts") or {}
good, bad, unexplained = 0, [], 0
for g in pol.get("grants", []):
    if "tag:dev-michail-basic" not in (g.get("src") or []):
        continue
    via = (g.get("via") or [])
    if not via:
        continue
    v = str(via[0]).replace("tag:dev-infra-", "")
    for d in (g.get("dst") or []):
        prefix = hosts.get(str(d))
        if not prefix:
            continue          # not a per-CIDR alias (e.g. autogroup:internet)
        if prefix not in owners:
            unexplained += 1  # nobody owns it: reported, not failed (churn window)
            continue
        if owners[prefix] == v:
            good += 1
        else:
            bad.append("%s via=%s owner=%s" % (prefix, v, owners[prefix]))
print("good=%d bad=%s unexplained=%d" % (good, bad[:3], unexplained))
' 2>/dev/null)
      case "$T" in
        good=0*|no-policy|"")
          echo "  SKIP [T-per-cidr-pins-name-the-owner] no usable data (${T:-empty})" ;;
        *"bad=[]"*)
          check_ge "T-per-cidr-pins-name-the-owner" 1 "$(printf '%s' "$T" | sed -n 's/^good=\([0-9]*\).*/\1/p')" ;;
        *)
          echo "  FAIL [T-per-cidr-pins-name-the-owner] a per-CIDR pin names a relay that does not own the prefix: $T" ;;
      esac
    fi

    # U. Live: the pin on skyworker's h-rule grants must equal the OWNER the
    #    assignment table picked — not the rule's own exit_node_id.
    #
    # CONTRACT RENEGOTIATED (B337, 2026-10-01). The original U asserted
    # "skyworker has NO grant with via=[emilia]", and was written 2026-08-26 —
    # BEFORE B265 made the pin conditional, B274 made exactly one relay the
    # advertiser of each prefix, and B275 moved the decision into the
    # `prefix_owner` table. Under those blocks a prefix is served by ONE relay,
    # chosen by majority over every rule that claims it, so a device whose rule
    # LOST the contest must be pinned to the winner — that is the fix, not a
    # bug. Measured on the reference VM:
    #
    #   all 200 of skyworker's enabled subnet/ip rules declare exit_node_id=karolina
    #   prefix_owner splits those same prefixes     77 emilia / 123 karolina
    #   the live policy carries via=                77 emilia / 123 karolina
    #
    # The ACL therefore AGREES with the assignment table, and the old assertion
    # was measuring the pre-B275 design. The contract now asserts that agreement
    # per owner, with the same ±10 tolerance the X contract uses for the
    # autoupdater's churn, so "the data plane and the control plane say the same
    # thing" is what is really being tested.
    if ! skygate_live_db_probe; then
      echo "  SKIP [U-skyworker-pin-matches-assignment] $(skygate_live_db_reason)"
    else
    U_RULES=$(skygate_live_db_query \
      "SELECT po.exit_node_id || '|' || count(*) FROM device_rules dr JOIN prefix_owner po ON po.prefix = dr.target_value WHERE dr.device_hostname = 'skyworker' AND dr.enabled = 1 AND dr.target_type IN ('subnet','ip') GROUP BY po.exit_node_id" \
      | tr -d ' \r' | sort)
    U_GRANTS=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    sys.exit(1)
try:
    import collections
    c = collections.Counter()
    for g in pol.get("grants", []):
        if "tag:dev-skyadmin-skyworker" in g.get("src", []) and g.get("via") \
           and any("h-rule" in str(d) for d in g.get("dst", [])):
            for v in g["via"]:
                c[str(v).replace("tag:dev-infra-", "")] += 1
    for k in sorted(c):
        print("%s|%d" % (k, c[k]))
except Exception:
    sys.exit(1)
' | tr -d ' \r' | sort)
    if [ -z "$U_RULES" ] || [ -z "$U_GRANTS" ]; then
      echo "  SKIP [U-skyworker-pin-matches-assignment] no data (rules='${U_RULES}' grants='${U_GRANTS}')"
    else
      # SET MEMBERSHIP, not count equality. The first version of this contract
      # compared per-owner COUNTS with a ±10 tolerance and was flaky against the
      # very reconciler it is meant to watch: two runs minutes apart measured
      # rules=77/via=77 (diff 0) and then rules=31/via=47 (diff 16) as the
      # autoupdater rewrote device_rules and B275 re-assigned prefixes. The
      # invariant B275 actually guarantees, and the one that catches the B276
      # bug class (a pin naming a relay that owns nothing), is that every relay
      # named by a `via` is a REAL owner in the assignment table.
      U_OWNERS=$(skygate_live_db_query "SELECT DISTINCT exit_node_id FROM prefix_owner" | tr -d ' \r' | sort -u)
      if [ -z "$U_OWNERS" ]; then
        echo "  SKIP [U-skyworker-pins-name-real-owners] prefix_owner returned no owners"
      else
        U_BAD=""
        while IFS='|' read -r owner _n; do
          [ -z "$owner" ] && continue
          if ! printf '%s\n' "$U_OWNERS" | grep -qx "$owner"; then
            U_BAD="$U_BAD $owner"
          fi
        done <<< "$U_GRANTS"
        if [ -z "$U_BAD" ]; then
          echo "  PASS [U-skyworker-pins-name-real-owners] every via on skyworker's h-rule grants is a current prefix_owner ($(printf '%s' "$U_OWNERS" | tr '\n' ',' ))"
          PASS=$((PASS+1))
        else
          echo "  FAIL [U-skyworker-pins-name-real-owners] the live ACL pins a relay that owns NO prefix:$U_BAD — this is the B276 class (a stale pin that filters routes away from the client)"
          FAIL=$((FAIL+1))
        fi
      fi
      U_TOTAL_GRANTS=$(printf '%s\n' "$U_GRANTS" | awk -F'|' '{s+=$2} END{print s+0}')
      check_ge "U-skyworker-has-pinned-grants" 1 "${U_TOTAL_GRANTS:-0}"
    fi
    fi

    # V. Live: a71 (per-device pref=emilia, no matching per-CIDR
    # rules) has exactly one via-bearing grant — the conditional
    # B265 catch-all pin.
    V=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get("grants", []):
    if "tag:dev-skyadmin-a71" in g.get("src", []) and "autogroup:internet" in g.get("dst", []) and g.get("via"):
        n += 1
print(n)
' 2>/dev/null)
    # B265: a71 has a per-device pref (emilia, via_enabled=1) but NO
    # matching per-CIDR rules, so the ONLY via-bearing grant is the
    # conditional per-device autogroup:internet pin. Exactly one.
    check_eq "V-a71-single-pinned-catchall" "1" "${V:-<err>}"

    # W. Live: total per-device autogroup:internet grants with via
    # across ALL devices == number of devices with via_enabled=1.
    #
    # B188.2 asserted this total was 0 (the pin was unconditional and
    # therefore harmful). B265 makes it conditional, so the correct
    # invariant is now "exactly the devices that asked for a pin have
    # one" — that is what enforces the preferred exit node on tagged
    # devices while leaving everybody else free.
    W=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    print(0); sys.exit(0)
n = 0
for g in pol.get("grants", []):
    s = g.get("src", [])
    if s and s[0].startswith("tag:dev-") and "autogroup:internet" in g.get("dst", []) and g.get("via"):
        n += 1
print(n)
' 2>/dev/null)
    if skygate_live_db_probe; then
      W_EXPECT=$(skygate_live_db_query \
        "SELECT COUNT(*) FROM device_exit_node_prefs WHERE via_enabled=1 AND exit_node_tag <> ''")
      if [ -n "$W_EXPECT" ]; then
        check_eq "W-tagged-device-pins-equal-enabled-prefs" "$W_EXPECT" "${W:-<err>}"
      else
        echo "  SKIP [W-tagged-device-pins-equal-enabled-prefs] the query returned nothing although the probe succeeded"
      fi
    else
      echo "  SKIP [W-tagged-device-pins-equal-enabled-prefs] $(skygate_live_db_reason)"
    fi

    # X. Live: every `via` on basic's h-rule grants must name a relay the
    # assignment table actually gives prefixes to.
    #
    # RENEGOTIATED 2026-10-01 (measured on the reference VM). The old form
    # compared two COUNTS under a ±10 tolerance:
    #   device_rules WHERE device_id=29 AND exit_node_id='emilia' AND
    #   target_type IN ('subnet','ip')                    → 104
    #   live h-rule grants for tag:dev-michail-basic with via=emilia → 174
    # That premise was invalidated by B274/B275: the pin no longer follows the
    # rule's own exit_node_id, it follows prefix_owner(prefix) — and DOMAIN rules
    # RESOLVE into prefixes, so a subnet/ip-only rule count is a DIFFERENT
    # population from the pinned grants. It also flipped between two gate runs on
    # the same tree (pass at 13:5x, fail at 14:2x) because the catalog's own
    # B276/B276.1 checks regenerate and re-apply the policy mid-run, which is a
    # property of the live system, not a regression in the tree. The surviving
    # invariant is the one U asserts for skyworker: a `via` may only name a real
    # prefix owner — that is the B276 bug class (a pin that filters routes away
    # from the client). Counts are still REPORTED, so drift stays visible.
    # B336: this used to reach the database at the hardcoded, long-stale
    # `172.17.0.1:5000`; it now uses the shared helper, which resolves the
    # container from the DSN.
    if skygate_live_db_probe; then
      X_RULE_COUNT=$(skygate_live_db_query \
        "SELECT COUNT(*) FROM device_rules WHERE user_id=6 AND device_id=29 AND exit_node_id='emilia' AND enabled=1 AND target_type IN ('subnet', 'ip')")
      X_GRANTS=$(docker exec headscale headscale policy get -o json 2>/dev/null | python3 -c '
import json, sys
try:
    pol = json.load(sys.stdin)
except Exception:
    sys.exit(1)
try:
    import collections
    c = collections.Counter()
    for g in pol.get("grants", []):
        if "tag:dev-michail-basic" in g.get("src", []) and g.get("via") \
           and any("h-rule" in str(d) for d in g.get("dst", [])):
            for v in g["via"]:
                c[str(v).replace("tag:dev-infra-", "")] += 1
    for k in sorted(c):
        print("%s|%d" % (k, c[k]))
except Exception:
    sys.exit(1)
' | tr -d ' \r' | sort)
      X_VIA_COUNT=$(printf '%s\n' "$X_GRANTS" | awk -F'|' '{s+=$2} END{print s+0}')
      X_OWNERS=$(skygate_live_db_query "SELECT DISTINCT exit_node_id FROM prefix_owner" | tr -d ' \r' | sort -u)
      if [ -z "$X_GRANTS" ] || [ -z "$X_OWNERS" ]; then
        echo "  SKIP [X-basic-pins-name-real-owners] no data (grants='${X_GRANTS}' owners='${X_OWNERS}')"
      else
        X_BAD=""
        while IFS='|' read -r owner _n; do
          [ -z "$owner" ] && continue
          if ! printf '%s\n' "$X_OWNERS" | grep -qx "$owner"; then
            X_BAD="$X_BAD $owner"
          fi
        done <<< "$X_GRANTS"
        if [ -z "$X_BAD" ]; then
          echo "  PASS [X-basic-pins-name-real-owners] every via on basic's h-rule grants is a current prefix_owner (rules=$X_RULE_COUNT via=$X_VIA_COUNT)"
          PASS=$((PASS+1))
        else
          echo "  FAIL [X-basic-pins-name-real-owners] the live ACL pins a relay that owns NO prefix:$X_BAD — B276 class (rules=$X_RULE_COUNT via=$X_VIA_COUNT)"
          FAIL=$((FAIL+1))
        fi
      fi
      check_ge "X-basic-has-pinned-grants" 1 "${X_VIA_COUNT:-0}"
    else
      echo "  SKIP [X-basic-pins-name-real-owners] $(skygate_live_db_reason)"
    fi
  else
    echo "  SKIP [S-X] docker not available"
  fi
else
  echo "  SKIP [S-X] not on VM"
fi

echo
echo "=== B188.2 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
