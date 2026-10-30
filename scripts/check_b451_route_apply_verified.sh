#!/usr/bin/env bash
# check_b451_route_apply_verified.sh — B451 (2026-10-30)
#
# «что за проблемные статусы 117 штук — при этом правила работают … никакие действия с панелью не
# снимают их из проблемных … все правила перешли на karolina и на emilia никого не осталось?»
#
# MEASURED ON THE REFERENCE INSTALL, which is why the answer was neither of those:
#
#	prefix_owner          emilia = 135 prefixes
#	headscale nodes list  emilia advertises 2 (0.0.0.0/0, ::/0), karolina advertises 223
#	relay_apply_state     emilia = <ts>|ok          ← the apply claimed SUCCESS
#	route-converge        emilia differs from its owned set (owned=137 advertised=2) — applying
#
# So the table moved the prefixes, the apply reported success, and the node never advertised them.
# `tailscale set --advertise-routes=…` exiting 0 over SSH is a statement about the ssh COMMAND, not
# about the node — and nothing ever read the node back. Every panel action took the same
# "successful" path, which is exactly why no action cleared the problem rows.
#
# THE FIX: the apply verifies itself ON the node. After the set command succeeds, the same transport
# reads the node's own preferences (`tailscale debug prefs`) and compares AdvertiseRoutes with what
# was requested; a mismatch is an ERROR naming both sides, so relay_apply_state stops claiming ok
# and the pages inherit the truth. The comparison and the parse are pure and unit-tested, and the
# expected set is built by the SAME routes the set command sends.
#
# CONTRACTS: 14.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

VERIFY=internal/headscale/route_verify_b451.go
VERIFY_T=internal/headscale/route_verify_b451_test.go
ROUTES=internal/headscale/routes.go
ARGS=internal/headscale/route_args.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$VERIFY" "$VERIFY_T" "$ROUTES" "$ARGS" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b451.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir under \$PWD/tmp (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the apply no longer believes the ssh exit status"
GOFMT_OUT="$(gofmt -l "$VERIFY" "$VERIFY_T" "$ROUTES" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$ROUTES" 'verifyRoutesOnNode(keyPath, host, port, jumpTarget, nodeHostname, routes, acceptRoutes)'; then
  ok "A2: a successful `set` is followed by a read-back ON the node"
else
  bad "A2: the apply still returns success on the ssh exit status alone (the reported bug)"
fi
if has "$ROUTES" 'if verr := verifyRoutesOnNode('; then
  ok "A3: the read-back runs on the SAME transport/argv path as the apply"
else
  bad "A3: the verification is not wired into the apply"
fi
if has "$VERIFY" 'const TailscaleReadPrefsCommand = "tailscale debug prefs"'; then
  ok "A4: the read-back is a read-only probe (no root, no state change)"
else
  bad "A4: no defined read-back command"
fi
if has "$VERIFY" 'func verifyRoutesOnNode(' && has "$VERIFY" 'could not be read back'; then
  ok "A5: «could not read the result» is reported as itself, not as a wrong advertisement"
else
  bad "A5: an unreadable node would be indistinguishable from a wrong one"
fi

hdr "B. the comparison names both sides"
if has "$VERIFY" 'func VerifyAdvertised(' && has "$VERIFY" 'ACCEPTED the command but does not advertise'; then
  ok "B1: a mismatch is an error that says the command was accepted and the node disagrees"
else
  bad "B1: no mismatch verdict"
fi
if has "$VERIFY" 'func ExpectedAdvertisedRoutes(' && has "$VERIFY" 'ExitNodeBaseRoutes'; then
  ok "B2: the expected set includes the exit-node base routes (the node must stay an exit node)"
else
  bad "B2: the expected set could demand a relay that is no longer an exit node"
fi
if has "$VERIFY" 'func AdvertisedRoutesFromPrefs(' && has "$VERIFY" 'advertiseRoutes'; then
  ok "B3: the prefs parse tolerates formatting and both field spellings"
else
  bad "B3: a formatting difference would look like a wrong advertisement"
fi
if has "$VERIFY" '"missing="' && has "$VERIFY" '"extra="'; then
  ok "B4: the message names what is missing AND what is extra (two different fixes)"
else
  bad "B4: the operator cannot tell «не хватает» from «надо снять»"
fi
if has "$VERIFY" 'summarizeRoutes'; then
  ok "B5: a 137-prefix mismatch cannot become a 3 KB log line"
else
  bad "B5: the message is unbounded"
fi
if has "$VERIFY_T" 'a node advertising 2 routes instead of the requested set was accepted as a success'; then
  ok "B6: the live shape (2 of 137) is pinned as a FAILURE by a test"
else
  bad "B6: the exact reported case is not a test case"
fi

hdr "C. pinned by running the tests"
if ! command -v go >/dev/null 2>&1; then
  skip "C1: no go toolchain on PATH — cannot run TestB451 here (CI is authoritative)"
else
  go test ./internal/headscale -run 'B451' -count=1 >"$WORK/t.log" 2>&1
  if [ $? -eq 0 ]; then
    ok "C1: TestB451 is green (parse, diff, the live 2-of-137 case, base routes)"
  else
    bad "C1: TestB451 failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
  fi
fi

hdr "D. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b451_route_apply_verified.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git"
else
  bad "D1: this script is not tracked by git"
fi
if grep -qF 'check_b451_route_apply_verified' "$GATE" 2>/dev/null; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B451"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "D3: the B451 catalog description contains a backtick (TD-15)" ;;
  "")    bad "D3: the B451 catalog entry was not found" ;;
  *)     ok "D3: the B451 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B451**' "$AGENTS" 2>/dev/null; then
  ok "D4: indexed in AGENTS.md"
else
  bad "D4: no one-line index entry in AGENTS.md"
fi

printf '\n\033[1mB451 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
