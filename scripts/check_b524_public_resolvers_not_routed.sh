#!/usr/bin/env bash
# check_b524_public_resolvers_not_routed.sh
#
# 2026-11-27 (B524) — «публичный резолвер не должен ехать subnet-маршрутом».
#
# THE MEASURED INCIDENT. Operator, 4G, Android, exit node selected — «тормозит, какой бы
# узел ни выбрал, а на Wi-Fi всё работает». The exit node's data path was healthy; the
# relays were healthy; the ACL permitted the tag. What was broken: skygate carried the
# tailnet's DNS resolvers (1.1.1.1, 8.8.8.8) as SYSTEM prefix_owner rows, so a relay
# advertised each of them as a /32. A /32 is MORE SPECIFIC than the 0.0.0.0/0 an exit node
# installs, so a client that accepts subnet routes («Use Tailscale subnets») sent every DNS
# query to whichever relay owned the prefix — in the live case a relay in the United States
# — regardless of the exit node the operator picked. Each new domain paid a transatlantic
# UDP DNS round trip through DERP, with retries; video to a tailnet peer (no DNS) was fine.
#
# WHY THE CONTRACT IS TWO-SIDED. B458's core must survive: a resolver the internet cannot
# reach (RFC 1918, loopback, link-local, CGNAT, the tailnet's own ULA) is reachable ONLY
# through a route, so it MUST still be carried. The public half was a trap, not a feature:
# it needs no route, and its /32 hijacks the exit node's default route. So the check pins
# both halves, and it pins the RELEASE half too — stopping to offer the prefix is useless if
# the old system row keeps advertising it.
#
# WHERE IT SKIPS: nothing here needs live state; the Go legs are real unit tests.
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B524: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

DNS=internal/headscale/dns_config_b458.go
SYNC=internal/feature/exit_rules/tailnet_dns_b458.go
SYSP=internal/prefixowner/system_prefixes_b458.go
CARD=internal/feature/admin/tailnet_dns_b458.go
PAGE=internal/handlers/templates/admin/exit_nodes.html
I18N=internal/i18n/catalog_exit_nodes.go
B272T=internal/headscale/policy_helper_b272_test.go

hdr "B524 — a public resolver must never override an exit node"

# --- A: the split exists and both halves are named ------------------------------
A_MISS=""
grep -q 'func resolverIsPubliclyRoutable' "$DNS" \
  || A_MISS="${A_MISS}there is no predicate for "is this resolver publicly routable""$'\n'
grep -q 'cgnatRange' "$DNS" \
  || A_MISS="${A_MISS}carrier-grade NAT (100.64.0.0/10) is not treated as tailnet-only — a resolver there would stop being carried"$'\n'
grep -q 'public \[\]string' "$DNS" \
  || A_MISS="${A_MISS}ResolverPrefixes does not report the public half separately (it would be silently dropped)"$'\n'
grep -q 'resolverIsPubliclyRoutable(addr)' "$DNS" \
  || A_MISS="${A_MISS}ResolverPrefixes does not use the predicate"$'\n'
if [ -z "$A_MISS" ]; then
  ok "A1: resolvers are split into carried / skipped / public, with CGNAT and private space carried"
else
  bad "A1: the resolver split is incomplete:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
fi

# --- B: the sync carries only the non-public half and says why ------------------
B_MISS=""
grep -q 'public resolver(s) are NOT carried as subnet routes (B524)' "$SYNC" \
  || B_MISS="${B_MISS}the sync does not journal why public resolvers are skipped (an invisible rule is how this trap returns)"$'\n'
grep -q 'EnsureSystemPrefixesLogged(s.dbc(), carried,' "$SYNC" \
  || B_MISS="${B_MISS}the sync still passes the whole resolver set instead of the carried half"$'\n'
grep -q 'carried = \[\]string{}' "$SYNC" \
  || B_MISS="${B_MISS}an empty carried set is not normalised, so the stale rows would never be released"$'\n'
if [ -z "$B_MISS" ]; then
  ok "B1: only the non-public half is carried, the reason is journaled, and an empty set still prunes"
else
  bad "B1: the DNS sync half is wrong:"
  printf '%s' "$B_MISS" | sed 's/^/       /' >&2
fi

# --- C: releasing a prefix is real (nil is a no-op, empty releases) -------------
C_MISS=""
grep -q 'if d == nil || prefixes == nil' "$SYSP" \
  || C_MISS="${C_MISS}EnsureSystemPrefixes no longer distinguishes "do not manage" (nil) from "carry nothing" (empty)"$'\n'
grep -q 'RELEASES every system row' "$SYSP" \
  || C_MISS="${C_MISS}the release semantics are not documented where the next reader will look"$'\n'
if [ -z "$C_MISS" ]; then
  ok "C1: an empty set releases the system rows; nil leaves them alone"
else
  bad "C1: the release contract is missing:"
  printf '%s' "$C_MISS" | sed 's/^/       /' >&2
fi

# --- D: the panel shows the public half and the subnet-routing warning ----------
D_MISS=""
grep -q 'Public \[\]string' "$CARD" \
  || D_MISS="${D_MISS}the exit-nodes card does not carry the public list (the operator cannot see what is deliberately not routed)"$'\n'
grep -q '\.Public' "$PAGE" \
  || D_MISS="${D_MISS}the page does not render the public list"$'\n'
grep -q 'exit_nodes.tailnet_dns.subnet_warning' "$PAGE" \
  || D_MISS="${D_MISS}the page does not warn that accepted subnet routes override an exit node"$'\n'
for k in exit_nodes.tailnet_dns.public_not_carried exit_nodes.tailnet_dns.subnet_warning; do
  n=$(grep -c "\"$k\"" "$I18N" 2>/dev/null || true)
  [ "${n:-0}" -ge 2 ] || D_MISS="${D_MISS}$k is not a RU+EN pair (found ${n:-0})"$'\n'
done
if [ -z "$D_MISS" ]; then
  ok "D1: the panel names the public resolvers, warns about subnet routing, and both texts are RU+EN"
else
  bad "D1: the panel half is incomplete:"
  printf '%s' "$D_MISS" | sed 's/^/       /' >&2
fi

# --- E: the behaviour is pinned by tests ---------------------------------------
E_MISS=""
for t in TestB524_ResolverPrefixesSplitPublicFromPrivate TestB524_EmptySetReleasesSystemRows TestB524_NilSetIsANoOp; do
  grep -rq "func $t" internal/ || E_MISS="${E_MISS}missing test $t"$'\n'
done
if [ -z "$E_MISS" ]; then
  if command -v go >/dev/null 2>&1; then
    OUT="$(go test ./internal/headscale/ ./internal/prefixowner/ -run 'B524|B458' -count=1 2>&1)"; RC=$?
    if [ "$RC" -eq 0 ]; then
      ok "E1: the split and the release are pinned by Go tests (and they pass)"
    else
      bad "E1: the B524 tests fail:"
      printf '%s\n' "$OUT" | tail -12 | sed 's/^/       /' >&2
    fi
  else
    skip "E1: no Go on PATH — the B524 tests exist but cannot run here"
  fi
else
  bad "E1: the behaviour is not pinned:"
  printf '%s' "$E_MISS" | sed 's/^/       /' >&2
fi

# --- F: the gate must not leave a bogus applier status on the host -------------
F_MISS=""
grep -q 'SKYGATE_UPDATE_DIR' "$B272T" \
  || F_MISS="${F_MISS}the B272 applier tests do not pin SKYGATE_UPDATE_DIR, so a gate run on a live host writes the REAL policy-apply.status"$'\n'
if [ -z "$F_MISS" ]; then
  ok "F1: the applier tests keep their status file in a temp dir (no bogus 'write did not land' on a live host)"
else
  bad "F1: the gate can poison the host's policy applier status:"
  printf '%s' "$F_MISS" | sed 's/^/       /' >&2
fi

# --- G: bookkeeping ------------------------------------------------------------
G_MISS=""
for f in "$DNS" "$SYNC" "$SYSP" "$CARD" "$PAGE" "$I18N" internal/prefixowner/system_prefixes_b524_test.go; do
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || G_MISS="${G_MISS}$f is NOT tracked by git"$'\n'
done
git ls-files --error-unmatch scripts/check_b524_public_resolvers_not_routed.sh >/dev/null 2>&1 \
  || G_MISS="${G_MISS}this script is NOT tracked by git"$'\n'
grep -q 'check_b524_public_resolvers_not_routed.sh' scripts/verify_pre_deploy.sh \
  || G_MISS="${G_MISS}verify_pre_deploy.sh does not register B524"$'\n'
grep -q '\*\*B524\*\*' AGENTS.md \
  || G_MISS="${G_MISS}AGENTS.md's block index has no B524 entry"$'\n'
if [ -z "$G_MISS" ]; then
  ok "G1: tracked, registered and indexed"
else
  bad "G1: the block bookkeeping is incomplete:"
  printf '%s' "$G_MISS" | sed 's/^/       /' >&2
fi

hdr "B524 summary"
printf 'B524: %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
