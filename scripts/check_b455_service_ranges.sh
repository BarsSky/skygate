#!/usr/bin/env bash
# check_b455_service_ranges.sh — B455 (2026-10-30)
#
# «посмотри каких адресов не хватает для discord не все покрыто текущими правилами»
#
# MEASURED on the reference install: every address the Discord DOMAINS resolve to (162.159.128-138.232/233,
# 162.159.129-135.233, 162.159.128-134.232, 162.159.130-136.234) is already covered by the
# 162.158.0.0/15 row. What was NOT covered is Discord's VOICE/MEDIA range, 66.22.192.0/18: the
# client is HANDED a voice server as IP:port by the gateway, so no DNS name exists to write a domain
# rule against and no domain rule can ever cover it — chat and attachments worked, voice/созвоны
# could not. That is a CLASS of services (RTC/voice), not a Discord quirk, so the ranges belong in a
# registry next to the CDN one: the expansion appends a service's ranges for a matching domain, and
# from there the normal prefix-owner path announces them, so the operator still sees who carries them.
#
# CONTRACTS: 11.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

REG=internal/feature/exit_rules/service_ranges_b455.go
REG_T=internal/feature/exit_rules/service_ranges_b455_test.go
SYNC=internal/feature/exit_rules/sync_domain.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$REG" "$REG_T" "$SYNC" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b455.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the registry carries what DNS cannot"
GOFMT_OUT="$(gofmt -l "$REG" "$REG_T" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$REG" '66.22.192.0/18'; then
  ok "A2: Discord's measured voice range is in the registry"
else
  bad "A2: the range the operator asked about is missing"
fi
if has "$REG" 'func ServiceExtraRanges(domain string) (service string, prefixes []string, why string)'; then
  ok "A3: one lookup answers service + ranges + WHY, so the panel can explain it"
else
  bad "A3: no lookup with a reason"
fi
if has "$REG" 'strings.HasSuffix(d, "."+want)'; then
  ok "A4: matching is a dot-boundary suffix (mydiscord.com must not announce 16k addresses)"
else
  bad "A4: a substring match would over-reach onto unrelated sites"
fi
if has "$REG" 'What was NOT covered' || has "$REG" '66.22.192.0/18'; then
  ok "A5: the measurement behind the range is written down where the range lives"
else
  bad "A5: a range nobody can justify is how a registry rots"
fi
if has "$SYNC" 'ServiceExtraRanges(d.domain)' && has "$SYNC" 'merged = append(merged, cdnCIDRs...)'; then
  ok "A6: the expansion actually ADDS them, into a COPY of the CDN list (a registry nobody calls fixes nothing, and appending into knownCDNs would write into a shared array)"
else
  bad "A6: the registry is not wired into the derived set, or it appends into the shared CDN list"
fi
if has "$SYNC" 'also needs %d range(s) that DNS cannot name'; then
  ok "A7: the journal names the service and the ranges it added"
else
  bad "A7: a silent 16k-address addition is not acceptable"
fi
if has "$REG" 'Name:     "Telegram"'; then
  bad "A8: Telegram's call ranges are in the registry WITHOUT the same measurement"
else
  ok "A8: nothing unmeasured was added (Telegram is named as the next candidate, not shipped)"
fi

hdr "B. pinned by running the tests"
if ! command -v go >/dev/null 2>&1; then
  skip "B1: no go toolchain on PATH — CI is authoritative"
else
  go test ./internal/feature/exit_rules -run 'B455' -count=1 >"$WORK/t.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "B1: TestB455 green (matching, over-reach, CIDR validity, copy-on-return)"
  else
    bad "B1: TestB455 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
  fi
fi

hdr "C. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b455_service_ranges.sh >/dev/null 2>&1; then
  ok "C1: this script is tracked by git"
else
  bad "C1: this script is not tracked by git"
fi
if grep -qF 'check_b455_service_ranges' "$GATE" 2>/dev/null; then
  ok "C2: registered in scripts/verify_pre_deploy.sh"
else
  bad "C2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B455"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "C3: the B455 catalog description contains a backtick (TD-15)" ;;
  "")    bad "C3: the B455 catalog entry was not found" ;;
  *)     ok "C3: the B455 catalog description has no backtick (TD-15)" ;;
esac

printf '\n\033[1mB455 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
