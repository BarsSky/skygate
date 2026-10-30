#!/usr/bin/env bash
# check_b453_relay_weights.sh — B453 (2026-10-30)
#
# The last open item of B275.1: «веса нагрузки на relay». The assignment engine spread unclaimed
# prefixes over the healthy relays by LEAST LOAD — an EVEN split, which is right for identical
# relays and wrong for a real tailnet, where a small VPS and a big box should not each carry half of
# 200 prefixes.
#
# The engine now accepts a weight per relay and prefers the least load RELATIVE to it. Two
# properties are deliberate, because both protect live traffic:
#
#   1. STICKY STILL WINS. A prefix with a healthy owner stays; weights only decide where the NEXT
#      prefixes land, so changing a weight never migrates traffic.
#   2. AN EMPTY/INVALID WEIGHT IS 1. Every existing install behaves exactly as before until somebody
#      sets a weight, and a zero/negative/garbage entry can neither divide by zero nor starve a
#      relay the healthy set says must be usable — it is REJECTED and logged with its text, because a
#      typo that silently became an even split would look like the weights were ignored.
#
# CONTRACTS: 12.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

W=internal/prefixowner/weights_b453.go
W_T=internal/prefixowner/weights_b453_test.go
ENGINE=internal/prefixowner/prefixowner.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$W" "$W_T" "$ENGINE" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b453.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the engine takes a weight, and the default is untouched"
GOFMT_OUT="$(gofmt -l "$W" "$W_T" "$ENGINE" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$W" 'type Weights map[string]int'; then
  ok "A2: a weight is a relay→positive-int map with a documented default"
else
  bad "A2: no weights type"
fi
if has "$W" 'const DefaultWeight = 1'; then
  ok "A3: an unset relay weighs 1, so an install with no weights behaves exactly as before"
else
  bad "A3: no explicit default — an empty setting could change behaviour"
fi
if has "$W" 'func ParseWeights(raw string) (w Weights, rejected []string)'; then
  ok "A4: the parser names what it rejected instead of silently dropping it"
else
  bad "A4: an unparseable weight would be invisible"
fi
if has "$W" 'func pickLeastRelative(candidates []string, load map[string]int, w Weights) string'; then
  ok "A5: the choice is least load RELATIVE to the weight (one pure function)"
else
  bad "A5: no weighted choice"
fi
if has "$W" 'lc*wb < lb*wc'; then
  ok "A6: the ratio is cross-multiplied in integers, so equal ratios cannot oscillate"
else
  bad "A6: a float ratio could flip a tie between passes"
fi
if has "$ENGINE" 'as := AssignWithReservationsAndWeights(claims, healthyRelays, existing, prefer, now, cfg, w)'; then
  ok "A7: the reconcile pass uses the weights"
else
  bad "A7: the engine ignores the weights"
fi
if has "$W" 'const SettingWeights = "prefix_owner.weights"'; then
  ok "A8: the setting lives in the database (a panel change needs no restart)"
else
  bad "A8: no setting key"
fi
if has "$ENGINE" 'prefix-owner: prefix_owner.weights: ignoring unparseable'; then
  ok "A9: a typo in the setting is logged with its text"
else
  bad "A9: a typo would look like the weights were ignored"
fi

hdr "B. the sticky promise survives the weights"
if has "$W_T" 'a sticky prefix migrated'; then
  ok "B1: a test asserts a sticky prefix does NOT move when a weight changes"
else
  bad "B1: nothing protects live traffic from a weight edit"
fi
if has "$W_T" 'the heavier relay to take it'; then
  ok "B2: and a reassignment DOES follow the weight (the failover records its source)"
else
  bad "B2: weights are not shown to decide anything"
fi

hdr "C. pinned by running the tests"
if ! command -v go >/dev/null 2>&1; then
  skip "C1: no go toolchain on PATH — cannot run TestB453 here (CI is authoritative)"
else
  go test ./internal/prefixowner -run 'B453' -count=1 >"$WORK/t.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "C1: TestB453 green (parse, distribution, sticky, reassignment, determinism)"
  else
    bad "C1: TestB453 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
  fi
fi

hdr "D. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b453_relay_weights.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git"
else
  bad "D1: this script is not tracked by git"
fi
if grep -qF 'check_b453_relay_weights' "$GATE" 2>/dev/null; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B453"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "D3: the B453 catalog description contains a backtick (TD-15)" ;;
  "")    bad "D3: the B453 catalog entry was not found" ;;
  *)     ok "D3: the B453 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B453**' "$AGENTS" 2>/dev/null; then
  ok "D4: indexed in AGENTS.md"
else
  bad "D4: no one-line index entry in AGENTS.md"
fi

printf '\n\033[1mB453 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
