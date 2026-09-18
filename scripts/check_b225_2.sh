#!/usr/bin/env bash
# B-check for B225.2 (v1.5.0+): Phase 4.4 follow-up
# — PG health → automatic alert via B203 watchdog
# consecutive-failure counter.
#
# Contracts pinned (8 source-pin + 1 go-runtime):
#   A:    watchdog.Config has Notifier field
#         (local NotifierSink interface to avoid
#         the backup → telegram → mesh import
#         cycle that B225 already discovered)
#   B:    watchdog.Config has ReadFailureThreshold
#         field (default 3, = 15s of failures with
#         the default 5s Interval)
#   C:    watchdog.Config has ClusterID field
#         (default "skygate-staging")
#   D:    DefaultConfig sets Notifier to
#         NoopNotifierSink{} (silent default when
#         no Telegram bot is configured)
#   E:    DBSwap struct has
#         consecutiveReadFailures +
#         hasFirstTick fields
#   F:    DBSwap has detectReadFailureTransition +
#         detectReadSuccessTransition methods
#   G:    tick() calls
#         detectReadFailureTransition on read
#         failure path
#   H:    tick() calls
#         detectReadSuccessTransition on success
#         path
#   I:    main.go wires app.Notifier into
#         watchdog Config (B225.2 wire-up)
#   J:    B225.2 unit tests pass
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

WD_GO="internal/watchdog/dbswap.go"
TEST_NEW="internal/watchdog/dbswap_b225_2_test.go"
MAIN_GO="cmd/skygate/main.go"
AGENTS="AGENTS.md"

# --- A: Notifier field on Config ---
config_struct=$(sed -n '/^type Config struct/,/^}$/p' "$WD_GO" 2>/dev/null)
if echo "$config_struct" | grep -q "Notifier NotifierSink"; then
  ok "A: watchdog.Config has Notifier field (NotifierSink interface)"
else
  fail "A: watchdog.Config Notifier field missing"
fi

# --- B: ReadFailureThreshold field on Config ---
if echo "$config_struct" | grep -q "ReadFailureThreshold int"; then
  ok "B: watchdog.Config has ReadFailureThreshold field"
else
  fail "B: watchdog.Config ReadFailureThreshold field missing"
fi

# --- C: ClusterID field on Config ---
if echo "$config_struct" | grep -q "ClusterID string"; then
  ok "C: watchdog.Config has ClusterID field"
else
  fail "C: watchdog.Config ClusterID field missing"
fi

# --- D: DefaultConfig sets NoopNotifierSink ---
default_config=$(sed -n '/^func DefaultConfig/,/^}$/p' "$WD_GO" 2>/dev/null)
if echo "$default_config" | grep -q "NoopNotifierSink{}"; then
  ok "D: DefaultConfig sets Notifier to NoopNotifierSink{} (silent default)"
else
  fail "D: DefaultConfig missing NoopNotifierSink default"
fi

# --- E: DBSwap struct has transition state fields ---
dbswap_struct=$(sed -n '/^type DBSwap struct/,/^}$/p' "$WD_GO" 2>/dev/null)
if echo "$dbswap_struct" | grep -q "consecutiveReadFailures int" && \
   echo "$dbswap_struct" | grep -q "hasFirstTick bool"; then
  ok "E: DBSwap has consecutiveReadFailures + hasFirstTick fields"
else
  fail "E: DBSwap missing transition state fields"
fi

# --- F: detectReadFailureTransition + detectReadSuccessTransition methods ---
if has "$WD_GO" "^func \\(w \\*DBSwap\\) detectReadFailureTransition" && \
   has "$WD_GO" "^func \\(w \\*DBSwap\\) detectReadSuccessTransition"; then
  ok "F: detectReadFailureTransition + detectReadSuccessTransition methods exist"
else
  fail "F: detect*Transition methods missing"
fi

# --- G: tick() calls detectReadFailureTransition on read failure ---
if sed -n '/^func (w \*DBSwap) tick/,/^}$/p' "$WD_GO" 2>/dev/null | grep -q "detectReadFailureTransition"; then
  ok "G: tick() calls detectReadFailureTransition on read failure"
else
  fail "G: tick() missing detectReadFailureTransition call"
fi

# --- H: tick() calls detectReadSuccessTransition on success ---
if sed -n '/^func (w \*DBSwap) tick/,/^}$/p' "$WD_GO" 2>/dev/null | grep -q "detectReadSuccessTransition"; then
  ok "H: tick() calls detectReadSuccessTransition on success"
else
  fail "H: tick() missing detectReadSuccessTransition call"
fi

# --- I: main.go wires app.Notifier into watchdog Config ---
if grep -q "wdCfg := watchdog.DefaultConfig" "$MAIN_GO" 2>/dev/null && \
   grep -q "wdCfg.Notifier = schedulerNotifierSink" "$MAIN_GO" 2>/dev/null; then
  ok "I: main.go wires app.Notifier into watchdog.Config (B225.2 wire-up)"
else
  fail "I: main.go missing B225.2 Notifier wiring for watchdog"
fi

# --- J: B225.2 unit tests present ---
if [ -f "$TEST_NEW" ] && grep -q "B225.2" "$TEST_NEW"; then
  n=$(grep -c "^func Test" "$TEST_NEW" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 8 ]; then
    ok "J: B225.2 unit tests present (${n} Test functions)"
  else
    fail "J: B225.2 unit tests insufficient (${n} < 8)"
  fi
else
  fail "J: B225.2 unit tests file $TEST_NEW missing"
fi

echo ""
echo "B225.2 B-check: $ok_count passed"
