#!/usr/bin/env bash
# check_b274_prefix_ownership.sh
#
# 2026-09-20 (B274) — a prefix must be advertised by exactly ONE exit
# node, and a device's rule must never point at a relay that cannot
# serve it.
#
# Live case (host SKYWORKER, the operator's own Windows box):
#
#   /my/exit-nodes      : online, accept-routes, NO exit node selected
#   device_rules        : 183 rules for skyworker, ALL exit=karolina
#   device_rules (basic):  28 of the same CIDRs, exit=emilia
#   headscale           : both relays advertised those 28 ranges, and the
#                         single primary per prefix MOVED between two
#                         `nodes list` dumps on the same day
#                         (emilia served them first, karolina later,
#                         after a staggeredSync pass rewrote both route
#                         sets)
#   symptom             : the Cloudflare/Google/Akamai destinations were
#                         broken for skyworker (whose per-CIDR grant
#                         names karolina) while every device WITHOUT a
#                         pin kept working — its unpinned grant follows
#                         whatever primary exists.
#
# Root cause: `StaggeredSync` / `SyncAdvertisedRoutes` built each relay's
# `--advertise-routes` from that relay's OWN rules only, so two relays
# advertised the same prefix whenever two devices (of different users)
# pointed their rules at different relays. The CDN expansion makes that
# routine rather than exotic: one domain rule for a Cloudflare-fronted
# site yields the CDN's whole published range set, so discord.* (basic →
# emilia) and auth.docker.io / registry.npmjs.org (skyworker → karolina)
# collide on 28 CIDRs.
#
# CONTRACTS
#   A  prefix ownership exists and is deterministic (pure helper)
#   B  both sync paths advertise only the owned set
#   C  the losers are reported (log + result/audit), never silent
#   D  duplicate derived rows are collapsed (dedup)
#   E  the B188.3 fixture cleanup exists, is dry-run by default and
#      cannot match a real destination
#   F  Go behaviour tests (ownership, tie-break, losers, owned filter)
#   G  live state on this host (SKIPs when headscale/DB are unavailable)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B274: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

OWNER=internal/feature/exit_rules/prefix_owner.go
TEST=internal/feature/exit_rules/prefix_owner_b274_test.go
SYNC=internal/feature/exit_rules/sync.go
CLEAN=scripts/b188_3_fixture_cleanup.sh

hdr "B274 — one advertising relay per prefix, and the losers named"

# --- A: the ownership helper ------------------------------------------------
if grep -q 'func PrefixOwnership(claims \[\]PrefixClaim) map\[string\]string' "$OWNER"; then
  ok "A.1 PrefixOwnership exists (pure)"
else
  bad "A.1 PrefixOwnership missing from $OWNER"
fi
if grep -q 'func PrefixLosers(claims \[\]PrefixClaim) map\[string\]\[\]string' "$OWNER"; then
  ok "A.2 PrefixLosers exists (pure)"
else
  bad "A.2 PrefixLosers missing from $OWNER"
fi
if grep -q 'return nodes\[i\] < nodes\[j\]' "$OWNER"; then
  ok "A.3 the tie-break is a hostname comparison (cannot oscillate)"
else
  bad "A.3 PrefixOwnership must tie-break deterministically on the hostname"
fi
if grep -q 'func OwnedPrefixes(node string, candidates \[\]string, owners map\[string\]string) \[\]string' "$OWNER"; then
  ok "A.4 OwnedPrefixes exists (keeps the exit-node bases)"
else
  bad "A.4 OwnedPrefixes missing from $OWNER"
fi

# --- B: both sync paths use it ---------------------------------------------
if grep -q 'owners := PrefixOwnership(claims)' "$SYNC"; then
  ok "B.1 SyncAdvertisedRoutes computes the ownership map"
else
  bad "B.1 SyncAdvertisedRoutes must compute PrefixOwnership"
fi
if grep -q 'syncOneExitNode(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, node, OwnedPrefixes(node, routes, owners), result)' "$SYNC"; then
  ok "B.2 the all-nodes path advertises only the owned prefixes"
else
  bad "B.2 SyncAdvertisedRoutes must pass OwnedPrefixes(...) to syncOneExitNode"
fi
if grep -q 'owned := OwnedPrefixes(n.name, routeList, owners)' "$SYNC"; then
  ok "B.3 the staggered path advertises only the owned prefixes"
else
  bad "B.3 StaggeredSync must filter routeList through OwnedPrefixes"
fi
if grep -q 'syncOneExitNode(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, node, OwnedPrefixes(node, routes, owners), result)' "$SYNC" && \
   grep -q 'allClaims = append(allClaims, PrefixClaim{Node: n2, Prefix: p2})' "$SYNC"; then
  ok "B.4 the per-node Re-sync button uses the same global ownership map"
else
  bad "B.4 SyncAdvertisedRoutesForNode must use the global ownership map too"
fi

# --- C: the losers are reported --------------------------------------------
if grep -q 'func reportPrefixLosers(' "$SYNC"; then
  ok "C.1 reportPrefixLosers exists"
else
  bad "C.1 reportPrefixLosers missing from $SYNC"
fi
if grep -q 'prefix-ownership(' "$SYNC"; then
  ok "C.2 the dropped prefixes are logged with the relay that dropped them"
else
  bad "C.2 a dropped prefix must be logged (operator has to be able to see WHY a site broke)"
fi
if grep -q 'result\["prefix_conflicts"\]' "$SYNC"; then
  ok "C.3 the conflict count reaches the sync result (/admin/exit-rules)"
else
  bad "C.3 the sync result must carry the conflict count"
fi

# --- D: dedup ---------------------------------------------------------------
if grep -q 'func (s \*Service) CollapseDuplicateDerivedRules() (int64, error)' "$OWNER"; then
  ok "D.1 CollapseDuplicateDerivedRules exists"
else
  bad "D.1 CollapseDuplicateDerivedRules missing"
fi
if grep -q 'PARTITION BY user_id, device_id, exit_node_id, target_type, target_value' "$OWNER"; then
  ok "D.2 the dedup partitions on the natural key (parent_domain is metadata, B183)"
else
  bad "D.2 the dedup must partition on (user_id, device_id, exit_node_id, target_type, target_value)"
fi
if grep -q "LIKE 'cdn:%' THEN 0 ELSE 1 END, id" "$OWNER"; then
  ok "D.3 the cdn:-prefixed parent wins (B183's stated preference)"
else
  bad "D.3 the dedup must prefer the cdn:-prefixed parent_domain"
fi
if grep -q 's.CollapseDuplicateDerivedRules()' "$SYNC"; then
  ok "D.4 the domain autoupdater runs the dedup every tick"
else
  bad "D.4 DomainAutoUpdater must call CollapseDuplicateDerivedRules"
fi

# --- E: fixture cleanup -----------------------------------------------------
if [ -f "$CLEAN" ]; then
  ok "E.1 $CLEAN exists"
else
  bad "E.1 $CLEAN missing"
fi
# E.1b: an IGNORED file passes every local check and then never reaches the VM.
# v1.5.19 shipped exactly that way — `.gitignore` line 78 is `cleanup_*.sh`, so
# `git add -A` silently skipped the script and the operator's `--apply` run on a
# freshly pulled host failed with "No such file or directory". The contract
# therefore asserts git TRACKS the file, not merely that it exists on this disk.
if git ls-files --error-unmatch "$CLEAN" >/dev/null 2>&1; then
  ok "E.1b git tracks $CLEAN (not silently ignored)"
else
  bad "E.1b $CLEAN is not tracked by git — check .gitignore (cleanup_*.sh was the trap)"
fi
if bash -n "$CLEAN" 2>/dev/null; then
  ok "E.2 the cleanup script parses"
else
  bad "E.2 $CLEAN does not parse"
fi
if grep -q "DRY-RUN" "$CLEAN" && grep -q 'APPLY=1' "$CLEAN"; then
  ok "E.3 the cleanup script is dry-run by default (--apply required)"
else
  bad "E.3 the cleanup script must be dry-run by default"
fi
if grep -q "FIXTURE_IPS=\"'5.5.5.5/32','6.7.8.9/32','1.2.3.0/24','1.2.99.0/24'\"" "$CLEAN"; then
  ok "E.4 the fixture predicate is a closed allow-list"
else
  bad "E.4 the fixture predicate must be an explicit allow-list, never a pattern over real destinations"
fi

# --- F: Go behaviour tests --------------------------------------------------
if command -v go >/dev/null 2>&1; then
  out="$(go test ./internal/feature/exit_rules/ -run 'PrefixOwnership|PrefixLosers|OwnedPrefixes' 2>&1)"
  if grep -q '^ok' <<< "$out"; then
    ok "F.1 ownership / tie-break / losers / owned-filter tests pass"
  else
    bad "F.1 go test ./internal/feature/exit_rules/ failed: $(printf '%s' "$out" | tail -n 5)"
  fi
  if grep -q 'TestPrefixOwnership_TieBreakIsDeterministic' "$TEST"; then
    ok "F.2 the oscillation guard test is present (20 iterations)"
  else
    bad "F.2 $TEST must keep TestPrefixOwnership_TieBreakIsDeterministic"
  fi
else
  skip "F go toolchain not on PATH"
fi

# --- G: live state ----------------------------------------------------------
# A prefix advertised by two relays is exactly what B274 removes; after a
# sync pass the live tailnet must not show any overlap.
HS_JSON=""
if command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -q '^headscale$'; then
  HS_JSON="$(docker exec headscale headscale nodes list --output json 2>/dev/null || true)"
fi
if [ -z "$HS_JSON" ]; then
  skip "G headscale is not reachable from this host (live overlap check)"
else
  overlap="$(printf '%s' "$HS_JSON" | python3 -c '
import json,sys,collections
d=json.load(sys.stdin)
m=collections.defaultdict(set)
for n in d:
    for r in (n.get("subnet_routes") or []):
        r=str(r)
        if r in ("0.0.0.0/0","::/0"): continue
        m[r].add(n.get("given_name"))
print(",".join(sorted(k for k,v in m.items() if len(v)>1)))
' 2>/dev/null)"
  if [ -z "$overlap" ]; then
    ok "G.1 no prefix is served by more than one relay"
  else
    bad "G.1 prefixes served by more than one relay (primary cannot be stable): $overlap"
  fi
fi

printf '\n\033[1mB274: %d PASS, %d FAIL, %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
