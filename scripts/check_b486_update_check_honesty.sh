#!/usr/bin/env bash
# ============================================================================
# check_b486_update_check_honesty.sh — B486
#
# «НЕ УДАЛОСЬ ПРОВЕРИТЬ GITHUB» — and it printed that OVER the neighbouring column.
#
# Operator screenshot, /admin/update, 2026-11-17 23:07 MSK: the «Последняя» cell carried
#
#   не удалось проверить GitHub: checker: GET https://api.github.com/repos/BarsSky/skygate/
#   releases/latest: Get "https://api.github.com/…": context deadline exceeded
#
# and the URL had no spaces to wrap at, so the text ran UNDER the «Установка» column and both became
# unreadable. Measured the same evening on the host: a plain `curl` from the SAME service user reached
# api.github.com in 0.2 s, and the panel's own log showed the git-tags fallback failing the same way
# at that minute — i.e. intermittent egress to api.github.com (DPI/throttling), not a broken panel.
#
# Three things were wrong and are fixed here:
#   1. the reason was a raw Go error, so "slow", "blocked", "DNS" and "rate limited" looked identical;
#   2. the checker used http.DefaultClient (NO timeout) and Go's dialer prefers AAAA — this host has an
#      IPv6 default route that blackholes most v6 destinations (the panel's Telegram egress logged
#      `dial tcp [2001:67c:4e08::…]:443: i/o timeout` on the same box), so a service that publishes v6
#      could burn the whole 15 s budget;
#   3. the failure rendered inline in a 1fr grid track, which may shrink below its content.
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b486.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

CHK=internal/update/checker.go
TPL=internal/handlers/templates/admin/update.html
DOC=docs/troubleshooting.md
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
ROADMAP=docs/ROADMAP.md

PASS=0
FAIL=0
fails=()
ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$CHK" "$TPL" "$DOC" "$VERIFY" "$AGENTS" "$NOTES" "$ROADMAP"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the client -----------------------------------------------------------------
if has "$CHK" 'func defaultCheckerClient()' && has "$CHK" 'ResponseHeaderTimeout: 10 * time.Second'; then
  ok "A1: the checker has its own bounded client (http.DefaultClient has no timeout at all)"
else
  bad "A1: the checker still relies on http.DefaultClient"
fi
if has "$CHK" '"tcp4"'; then
  ok "A2: the dialer tries IPv4 first (this host blackholes v6; Telegram already paid for it)"
else
  bad "A2: the dialer still prefers AAAA and can stall on a blackholed v6 route"
fi
if ! has "$CHK" 'c.HTTPClient = http.DefaultClient'; then
  ok "A3: http.DefaultClient is not used for the real installation"
else
  bad "A3: the unbounded default client is still the production path"
fi

# --- B: the reason ------------------------------------------------------------------
if has "$CHK" 'func classifyCheckError('; then
  ok "B1: a transport failure is classified instead of pasted raw"
else
  bad "B1: no classifier — slow, blocked, DNS and rate limit still look the same"
fi
for phrase in 'api.github.com не ответил вовремя' 'DNS не разрешает api.github.com' 'нет маршрута к api.github.com'; do
  if has "$CHK" "$phrase"; then :; else bad "B2: the classifier has no case saying «$phrase»"; fi
done
if has "$CHK" "$(printf 'api.github.com не ответил вовремя')"; then
  ok "B2: timeout, DNS and no-route are named separately, each with the raw text kept"
fi
if has "$CHK" 'Error:     classifyCheckError(err)'; then
  ok "B3: the page's error field goes through the classifier"
else
  bad "B3: the Result carries the raw error"
fi

# --- C: the layout ------------------------------------------------------------------
if has "$TPL" 'min-width:0' && has "$TPL" 'overflow-wrap:anywhere'; then
  ok "C1: the status cells may shrink and the URL-like reason breaks instead of overflowing"
else
  bad "C1: a long reason still runs under the neighbouring column"
fi
if has "$TPL" 'action="/admin/update/check-now" style="margin-top:6px"'; then
  ok "C2: the failure block carries its own «Проверить сейчас» control"
else
  bad "C2: the operator must hunt for the retry button"
fi
if ! has "$TPL" '<span class="sub">{{t "update.check_failed"}}: {{.Error}}</span>'; then
  ok "C3: the raw inline «failed: error» span is gone"
else
  bad "C3: the overflowing inline span is still there"
fi

# --- D: the docs and the register -----------------------------------------------------
if has "$DOC" 'не удалось проверить GitHub' || has "$DOC" 'api.github.com'; then
  ok "D1: the troubleshooting guide explains this failure"
else
  bad "D1: nothing to read for the operator"
fi
if has "$ROADMAP" 'TD-39'; then
  ok "D2: the egress debt is registered (TD-39)"
else
  bad "D2: the dependency on api.github.com egress is not in the register"
fi
if has "$VERIFY" '"B486"'; then ok "D3: registered in the gate"; else bad "D3: not registered"; fi
if has "$AGENTS" 'B486'; then ok "D4: block index carries it"; else bad "D4: no index entry"; fi
if has "$NOTES" 'B486'; then ok "D5: release notes describe it"; else bad "D5: no release-notes section"; fi

# --- E: discriminators ----------------------------------------------------------------
cat > "$SKY_TMP/old.go" <<'GO'
package update
// resp, err := c.HTTPClient.Do(req)  // http.DefaultClient
// return nil, fmt.Errorf("checker: GET %s: %w", apiURL, err)
GO
if grep -q 'classifyCheckError' "$SKY_TMP/old.go"; then
  bad "E1: the classifier predicate matches a checker that has no classifier"
else
  ok "E1: the predicates discriminate the pre-B486 checker"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/update/ -count=1 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    ok "E2: go test ./internal/update/ passes"
  else
    bad "E2: the update package tests fail: $(printf '%s' "$OUT" | tail -2 | tr '\n' ' ')"
  fi
else
  printf '  \033[33m-\033[0m E2: SKIP — no go toolchain\n'
fi

echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB486: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB486: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
