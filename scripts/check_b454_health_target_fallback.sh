#!/usr/bin/env bash
# check_b454_health_target_fallback.sh — B454 (2026-10-30)
#
# «Почему проблемы на двух релеях» — the remaining half. B449 fixed the PARSING of the target; what
# was still wrong is the RESOLUTION: the health probe read `exit_servers.ssh_target` directly and, on
# an empty value, answered «поле ssh_target пустое» (live: `aisha`) — even though skygate itself
# already knew where that relay is (`exit_servers.tailscale_ip`) and the ROUTE APPLY path has used
# that fallback since B292 (`db.LookupExitServerSSHTarget`: operator override → root@<tailnet IP> →
# none).
#
# Two resolvers for one question is how a panel and a sync come to disagree: the apply reaches the
# relay over the tailnet and the probe says the row is unreadable. The probe now resolves its target
# through the SAME helper, so an empty ssh_target is an OPERATOR OMISSION the panel fills in, not a
# broken relay.
#
# CONTRACTS: 8.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

H=internal/feature/admin/exit_nodes_health_b417_1.go
DB=internal/db/exit_servers.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$H" "$DB" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the probe resolves its target like the apply does"
GOFMT_OUT="$(gofmt -l "$H" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go file is gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$H" 'db.LookupExitServerSSHTarget(s.dbc(), row.Hostname)'; then
  ok "A2: an empty ssh_target falls back to the relay's tailnet address (the B292 helper)"
else
  bad "A2: the probe still answers «поле пустое» about a relay skygate can reach"
fi
if has "$H" 'target := row.SSHTarget'; then
  ok "A3: the operator's own value still wins when it is set"
else
  bad "A3: the fallback would override a configured target"
fi
if has "$DB" 'func LookupExitServerSSHTarget('; then
  ok "A4: ONE resolver serves both paths (no second opinion)"
else
  bad "A4: the shared resolver moved or disappeared — update this contract deliberately"
fi
if has "$H" 'B454'; then
  ok "A5: the reason is written down where the code is"
else
  bad "A5: no note explaining why the fallback exists"
fi

hdr "B. the package still passes"
if ! command -v go >/dev/null 2>&1; then
  skip "B1: no go toolchain on PATH — CI is authoritative"
else
  mkdir -p "$PWD/tmp" 2>/dev/null || true
  W="$(mktemp -d "$PWD/tmp/b454.XXXXXX" 2>/dev/null || true)"
  [ -n "${W:-}" ] && [ -d "$W" ] || { bad "cannot create a scratch dir (B340)"; exit 1; }
  if go test ./internal/feature/admin -count=1 >"$W/t.log" 2>&1; then
    ok "B1: the admin package is green with the shared resolver"
  else
    bad "B1: admin tests failed — $(tail -3 "$W/t.log" | tr '\n' ' ')"
  fi
  rm -rf "$W"
fi

hdr "C. registered, tracked, and free of the TD-15 trap"
if git ls-files --error-unmatch scripts/check_b454_health_target_fallback.sh >/dev/null 2>&1; then
  ok "C1: this script is tracked by git"
else
  bad "C1: this script is not tracked by git"
fi
if grep -qF 'check_b454_health_target_fallback' "$GATE" 2>/dev/null; then
  ok "C2: registered in scripts/verify_pre_deploy.sh"
else
  bad "C2: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B454"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "C3: the B454 catalog description contains a backtick (TD-15)" ;;
  "")    bad "C3: the B454 catalog entry was not found" ;;
  *)     ok "C3: the B454 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B454**' "$AGENTS" 2>/dev/null; then
  echo "  (index present)"
fi

printf '\n\033[1mB454 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
