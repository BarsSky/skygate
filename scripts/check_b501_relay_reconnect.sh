#!/usr/bin/env bash
# B501 (2026-11-19) — «ПЕРЕПОДКЛЮЧИТЬ RELAY ИЗ ВЕБИНТЕРФЕЙСА», А НЕ ИЗ SSH-СЕССИИ.
#
# The operator's request, verbatim: «необходим тогда механизм переподключения реализовать в
# вебинтерфейсе для администратора чтобы можно было удаленно инициализировать команду на
# переподключение и использовать в некоторых спорных ситуация по доступу и обновлению если
# это возможно — если же это избыточно то тогда не следует делать функционал а просто
# переподключить».
#
# It is NOT excessive, and the live case is why: relay `karolina` reported `Nearest DERP:
# New York City` while the operator's own region 900 answered — a stale netmap in its
# tailscaled, fixed by a daemon restart. That restart was an SSH session: not repeatable,
# not audited, not verified. The action now exists on /admin/exit-nodes, and its boundary is
# as important as its feature:
#
#   * a RELAY can be reconnected (skygate reaches it over the proven transport ladder);
#   * a DEVICE cannot — skygate never reaches a client's daemon, and the page shows no such
#     button for a phone. `scripts/device_connectivity.sh` is the honest answer there.
#
# Contracts:
#   A. the command is a FIXED constant: no operator input reaches a shell
#   B. the action is wired end to end (handler, route, service hook, boot wiring, button)
#   C. it reports the state it produced, not the exit code (B451's rule for a restart)
#   D. admin-only, audited, and refused for a relay that IS the panel host
#   E. i18n: three keys in BOTH catalogues, and the confirm text exists (no silent restart)
#   F. discoverable: registered, tracked, indexed, roadmap, release notes
#   G. behaviour: the pure half is tested (SKIP when no Go toolchain is on this host)

. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"   # B528: the route table spans routes*.go now
gosurface ROUTES cmd/skygate/*.go || { echo "B501: cannot read the cmd/skygate surface" >&2; exit 2; }

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

EXEC="internal/headscale/relay_exec_b501.go"
SVC="internal/feature/exit_rules/relay_reconnect_b501.go"
HND="internal/feature/admin/exit_nodes_reconnect_b501.go"
TPL="internal/handlers/templates/admin/exit_nodes.html"
CAT="internal/i18n/catalog_exit_nodes.go"

# B339: the boot wiring lives in the cmd/skygate PACKAGE (the assignment itself is
# in exit_rules_wire_b501.go, extracted so main.go — capped by B372 — keeps
# shrinking). Read the surface, never one file: a pure move must not go red.
. scripts/lib/gosurface.sh
gosurface MAIN_SURFACE cmd/skygate/*.go

# ═══════════════ A. the command is fixed ═══════════════
if [ -f "$EXEC" ]; then ok "A1: $EXEC exists"; else bad "A1: $EXEC is missing"; fi
if grep -q 'const RelayReconnectCommand' "$EXEC" 2>/dev/null; then
    ok "A2: the command is a constant, not built from request data"
else
    bad "A2: no constant command — operator input could reach a shell"
fi
# The handler must not read any form value / path value into the command.
if grep -qE 'FormValue|r\.URL\.Query' "$HND" 2>/dev/null; then
    bad "A3: the handler reads request data — the command must be fixed"
else
    ok "A3: the handler passes only the hostname path variable"
fi
if grep -q 'tailscale status --json' "$EXEC" 2>/dev/null; then
    ok "A4: the command reads the relay's own status afterwards"
else
    bad "A4: the command cannot show what state the relay came back in"
fi

# ═══════════════ B. wired end to end ═══════════════
if grep -q 'func (s \*Service) ReconnectRelay(' "$SVC" 2>/dev/null; then
    ok "B1: exit_rules.Service.ReconnectRelay exists"
else
    bad "B1: the service method is missing"
fi
if grep -q 'ReconnectRelayFn' internal/feature/admin/service.go 2>/dev/null; then
    ok "B2: the admin service carries the hook"
else
    bad "B2: the hook is missing from the admin service"
fi
case "$(cat "$MAIN_SURFACE" 2>/dev/null)" in
    *'adminSvc.ReconnectRelayFn = exitRulesSvc.ReconnectRelay'*)
        ok "B3: it is wired at boot" ;;
    *)  bad "B3: nothing wires the hook — the page would show a button that does nothing" ;;
esac
if grep -q 'exit-nodes/{hostname}/reconnect' "$ROUTES" 2>/dev/null; then
    ok "B4: the route is registered"
else
    bad "B4: the route is missing"
fi
case "$(cat "$TPL" 2>/dev/null)" in
    *'/reconnect"'*) ok "B5: the relay row renders the button" ;;
    *) bad "B5: the template has no reconnect control" ;;
esac
if grep -q 'ProbeRelayEndpoint' "$SVC" 2>/dev/null && grep -q 'relaySSHEndpoints\|appendJumpCandidates' "$SVC" 2>/dev/null; then
    ok "B6: it reuses the proven transport ladder rather than a new ssh call"
else
    bad "B6: the action bypasses the transport ladder (it would fail exactly where routes fail)"
fi

# ═══════════════ C. it verifies, not just runs ═══════════════
if grep -q 'ParseRelaySelfState' "$SVC" 2>/dev/null; then
    ok "C1: the result is parsed from the relay's own status"
else
    bad "C1: a successful ssh exit would be reported as «reconnected»"
fi
if grep -q 'res.OK = true' "$SVC" 2>/dev/null; then
    ok "C2: OK is set only after the parse succeeds"
else
    bad "C2: OK is not tied to the read-back"
fi
if grep -q 'func (r RelayReconnectResult) Summary()' "$SVC" 2>/dev/null; then
    ok "C3: one summary line names the transport and the DERP region"
else
    bad "C3: the flash cannot say which region the relay came back on"
fi

# ═══════════════ D. admin-only, audited, safe ═══════════════
if grep -q 'c == nil || !c.IsAdmin' "$HND" 2>/dev/null; then
    ok "D1: admin-only"
else
    bad "D1: the handler does not check IsAdmin"
fi
if grep -q 'exit_relay_reconnected' "$HND" 2>/dev/null; then
    ok "D2: every attempt is audited"
else
    bad "D2: the action leaves no audit trail"
fi
if grep -q 'placement.Local' "$SVC" 2>/dev/null; then
    ok "D3: it refuses a relay that IS the panel host"
else
    bad "D3: it would try to restart the daemon on the panel's own host"
fi
if grep -q 'Переподключить\|Reconnect' "$SVC" "$HND" 2>/dev/null; then
    ok "D4: the result explains itself in the operator's language"
else
    bad "D4: the result is machine-speak only"
fi

# ═══════════════ E. i18n pairs + a confirm ═══════════════
missing=""
for k in reconnect_button reconnect_help reconnect_confirm; do
    n="$(grep -c "\"exit_nodes\.$k\":" "$CAT" 2>/dev/null)"
    [ "${n:-0}" -ge 2 ] || missing="$missing exit_nodes.$k(${n:-0}/2)"
done
if [ -z "$missing" ]; then
    ok "E1: all three keys exist in BOTH catalogues"
else
    bad "E1: keys missing:$missing"
fi
case "$(cat "$TPL" 2>/dev/null)" in
    *'reconnect_confirm'*) ok "E2: the restart is behind a confirm dialog" ;;
    *) bad "E2: one click restarts a relay with no confirmation" ;;
esac

# ═══════════════ F. discoverability ═══════════════
if grep -q 'check_b501_relay_reconnect.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "F1: registered in the catalog"
else
    bad "F1: not registered in scripts/verify_pre_deploy.sh"
fi
if git ls-files --error-unmatch scripts/check_b501_relay_reconnect.sh >/dev/null 2>&1; then
    ok "F2: git tracks this script (trap #11)"
elif git check-ignore -q scripts/check_b501_relay_reconnect.sh 2>/dev/null; then
    bad "F2: this script is IGNORED by .gitignore (trap #11)"
else
    ok "F2: this script is not ignored (untracked until the release commit)"
fi
if grep -q '^\- \*\*B501\*\*' AGENTS.md 2>/dev/null; then ok "F3: the AGENTS index carries its line"; else bad "F3: no AGENTS index line"; fi
if grep -q 'B501' docs/ROADMAP.md 2>/dev/null; then ok "F4: the roadmap records it"; else bad "F4: the roadmap does not mention B501"; fi
if grep -q 'B501' RELEASE-NOTES.md 2>/dev/null; then ok "F5: the release notes describe it"; else bad "F5: no release-notes entry"; fi
if grep -q 'reconnect' docs/operations.md 2>/dev/null; then ok "F6: the procedure is written down for an operator"; else bad "F6: nothing in operations.md explains the action"; fi

# ═══════════════ G. behaviour ═══════════════
GOBIN="$(command -v go 2>/dev/null || true)"
if [ -z "$GOBIN" ] && [ -n "${GO:-}" ] && [ -x "$GO" ]; then GOBIN="$GO"; fi
if [ -z "$GOBIN" ]; then
    skip "G1: no go binary in PATH — the behavioural tests were not run"
else
    if out="$("$GOBIN" test ./internal/headscale/ ./internal/feature/admin/ -run 'B501' -count=1 2>&1)"; then
        ok "G1: the B501 tests are green (fixed command, status parsing)"
    else
        bad "G1: go test failed:
$(printf '%s\n' "$out" | tail -20)"
    fi
fi

echo
echo "  B501: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
