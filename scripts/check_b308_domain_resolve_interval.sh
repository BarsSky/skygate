#!/usr/bin/env bash
# check_b308_domain_resolve_interval.sh
#
# 2026-09-23 (B308, v1.5.73) — a domain rule may be re-resolved only once per
# interval, which is what stopped the permanent ACL drift.
#
# LIVE CAUSE (agent VM, measured):
#
#   /admin/exit-nodes kept showing the red «политика headscale УСТАРЕЛА» banner on
#   both hosts and the policy was rewritten over and over although the operator
#   changed nothing. The journal (39 drift/defer lines in two hours):
#
#     17:41:02 acl-drift: auto-updater tick changed 38 rule(s) (added=19 removed=19) — deferring (throttle 30m0s)
#     17:46:02 acl-drift: auto-updater tick changed 34 rule(s) (added=17 removed=17) — deferring
#     18:11:16 acl-drift: auto-updater tick changed 50 rule(s) (added=18 removed=32) — deferring
#     18:16:00 acl-drift: auto-updater tick changed 50 rule(s) (added=32 removed=18) — deferring
#     18:51:04 acl-drift: ACL re-applied (snapshot v1655, generated=50420 bytes)
#
#   `DomainAutoUpdater` re-resolved EVERY domain rule on EVERY five-minute tick, so
#   a domain whose A records rotate (ghcr.io 29 rows, quay.io 17, minimax.io 33)
#   produced ±20 derived /32 rows per tick: the generated ACL never stopped
#   changing, the drift banner was effectively permanent, and on a policy.mode=file
#   host every throttled re-apply restarted headscale.
#
# CONTRACTS
#   A. the interval policy exists: default 6h, floor 5m, ceiling 7d, "0" = every tick
#   B. a domain that was never resolved is always due, and a failed lookup does not
#      mark it resolved (an unreachable resolver is retried next tick)
#   C. the updater consults the gate before resolving and marks only after success
#   D. the operator can change it from the panel (same key on both sides)
#   E. the tests exist and pass
#   F. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B308: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

POLICY=internal/feature/exit_rules/domain_interval_b308.go
SYNC=internal/feature/exit_rules/sync.go
HANDLER=internal/feature/admin/settings_dns_autoupdate.go
ROUTE=cmd/skygate/main.go
TPL=internal/handlers/templates/admin/system_tests.html
I18N=internal/i18n/catalog_admin.go
TEST1=internal/feature/exit_rules/domain_interval_b308_test.go
TEST2=internal/feature/admin/settings_dns_interval_b308_test.go

hdr "B308 — one domain re-resolve per interval (stops the permanent ACL drift)"

# --- A: the policy -------------------------------------------------------------
if grep -q 'DefaultDomainResolveInterval = 6 \* time.Hour' "$POLICY" \
   && grep -q 'MinDomainResolveInterval = 5 \* time.Minute' "$POLICY" \
   && grep -q 'MaxDomainResolveInterval = 7 \* 24 \* time.Hour' "$POLICY"; then
  ok "A1: default 6h, floor 5m, ceiling 7d"
else
  bad "A1: the interval bounds are missing or changed silently"
fi
if grep -q 'if secs <= 0 {' "$POLICY" && grep -q 'return 0 // explicitly unlimited' "$POLICY"; then
  ok "A2: 0 disables the gate (the pre-B308 behaviour stays available)"
else
  bad "A2: there is no way to disable the gate"
fi
if grep -q '"dns_domain_resolve_interval_sec"' "$POLICY"; then
  ok "A3: the setting key is the documented one"
else
  bad "A3: the setting key changed (the page and the updater would disagree)"
fi
if grep -q 'SettingDomainResolveAtPrefix = "domain_resolve_at:"' "$POLICY"; then
  ok "A4: the per-domain last-resolve key prefix is namespaced"
else
  bad "A4: the per-domain key prefix is missing"
fi

# --- B: the due rule ----------------------------------------------------------
if grep -q 'if last <= 0 {' "$POLICY" && grep -q 'return true' "$POLICY"; then
  ok "B1: a never-resolved domain is always due"
else
  bad "B1: a fresh rule could be starved by the gate"
fi
if grep -q 'if now < last {' "$POLICY"; then
  ok "B2: a backwards clock does not freeze a domain for hours"
else
  bad "B2: a clock step could park a domain"
fi
if grep -q 'cannot tell → resolve (never leave a rule unrefreshed)' "$POLICY"; then
  ok "B3: an unreadable last-resolve value resolves instead of skipping"
else
  bad "B3: an unreadable value would skip the domain"
fi

# --- C: the updater consults it ------------------------------------------------
if grep -q 'resolveInterval := s.DomainResolveInterval()' "$SYNC" \
   && grep -q 'if !s.domainResolveDueFor(d.domain, nowUnix, resolveInterval) {' "$SYNC"; then
  ok "C1: DomainAutoUpdater gates every domain before resolving"
else
  bad "C1: the updater still resolves every domain on every tick"
fi
if [ "$(grep -c 's.markDomainResolved(d.domain, nowUnix)' "$SYNC")" -eq 2 ]; then
  ok "C2: both resolution paths (CDN + per-IP) mark the domain resolved"
else
  bad "C2: a resolution path does not mark the domain (it would re-resolve forever)"
fi
if grep -q 'domain(s) skipped (re-resolve interval' "$SYNC"; then
  ok "C3: the skip is logged with the effective interval (visible in the journal)"
else
  bad "C3: skipping is silent"
fi
if grep -B2 'markDomainResolved' "$SYNC" | grep -q 'failed lookup returned early'; then
  ok "C4: a failed lookup does NOT mark the domain (retried next tick)"
else
  # The comment is the contract here: verify the early return still exists.
  if grep -q 's.logAutoUpdate(d.id, d.domain, 0, 0, "lookup failed: "' "$SYNC"; then
    ok "C4: a failed lookup still returns early (no mark, retried next tick)"
  else
    bad "C4: a failed lookup may now be marked as resolved"
  fi
fi

# --- D: the panel --------------------------------------------------------------
if grep -q 'func (s \*Service) PostAdminSystemTestsDNSInterval' "$HANDLER" \
   && grep -q 'exit_rules.SettingDomainResolveIntervalSec' "$HANDLER"; then
  ok "D1: the handler writes the SAME key the updater reads"
else
  bad "D1: the panel writes a different key than the updater reads"
fi
if grep -q 'POST /admin/system_tests/dns-interval' "$ROUTE"; then
  ok "D2: the route is registered"
else
  bad "D2: the route is missing"
fi
if grep -q 'dns-interval' "$TPL" && grep -q 'DNSResolveIntervalSec' "$TPL"; then
  ok "D3: the page renders the field with the effective value"
else
  bad "D3: the field is not rendered"
fi
for k in interval_label interval_help interval_saved; do
  cnt="$(grep -cF "\"dns_autoupdater.$k\"" "$I18N" 2>/dev/null || echo 0)"
  if [ "$cnt" -eq 2 ]; then
    ok "D4: i18n key present exactly once per map: dns_autoupdater.$k"
  else
    bad "D4: i18n key dns_autoupdater.$k appears $cnt time(s), want 2 (RU+EN)"
  fi
done

# --- E: tests ------------------------------------------------------------------
for f in "$TEST1" "$TEST2"; do
  if [ -f "$f" ]; then ok "E1: $f exists"; else bad "E1: $f is missing"; fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/feature/exit_rules/ ./internal/feature/admin/ -run 'B308' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "E2: the B308 tests pass"
  else
    bad "E2: the B308 tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "E2: go not on PATH — run the B308 tests on the VM"
fi

# --- F: git --------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b308_domain_resolve_interval.sh >/dev/null 2>&1; then
  ok "F1: scripts/check_b308_domain_resolve_interval.sh is tracked by git"
else
  bad "F1: scripts/check_b308_domain_resolve_interval.sh is NOT tracked"
fi

printf '\n\033[1mB308 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
