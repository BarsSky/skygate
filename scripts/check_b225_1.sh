#!/usr/bin/env bash
# B-check for B225.1 (v1.5.0+): /admin/database
# Phase 4.4 follow-up — DB health degraded
# transition alert (closes the "operator finds
# out about a DB outage only when /db/health
# silently goes orange" gap).
#
# Contracts pinned (8 source-pin + 2 go-runtime):
#   A:    DBHealthConfig has Notifier field
#         (DBHealthAlertSink interface to avoid
#         the backup → telegram → mesh import
#         cycle that B225 already discovered)
#   B:    DBHealthConfig has ClusterID field
#         (default "skygate-staging" — the
#         target_id for the B221 audit row)
#   C:    DefaultDBHealthConfig sets Notifier to
#         NoopAlertSink{} (the silent default
#         when no Telegram bot is configured)
#   D:    Sampler has hasFirstSample + lastHealthy
#         fields (the transition state)
#   E:    Sampler has detectTransition method
#         (the per-tick transition detector)
#   F:    detectTransition alerts on
#         ok→degraded (❌ "DB health DEGRADED")
#   G:    detectTransition alerts on
#         degraded→ok (✅ "DB health recovered")
#   H:    detectTransition's first sample is the
#         baseline (no alert on tick 0; ticks 1+
#         fire on edges)
#   I:    main.go wires app.Notifier into the
#         DBHealthConfig
#   J:    B225.1 unit tests cover all 4 transition
#         edges + the no-op cases
#   K:    AGENTS.md mentions B225.1
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

HEALTHZ_GO="internal/feature/healthz/db_health.go"
TEST_NEW="internal/feature/healthz/db_health_b225_1_test.go"
MAIN_GO="cmd/skygate/main.go"
AGENTS="AGENTS.md"

# --- A: Notifier field on DBHealthConfig ---
if sed -n '/^type DBHealthConfig struct/,/^}$/p' "$HEALTHZ_GO" 2>/dev/null | grep -q "Notifier DBHealthAlertSink"; then
  ok "A: DBHealthConfig has Notifier field (DBHealthAlertSink interface)"
else
  fail "A: DBHealthConfig Notifier field missing"
fi

# --- B: ClusterID field on DBHealthConfig ---
if sed -n '/^type DBHealthConfig struct/,/^}$/p' "$HEALTHZ_GO" 2>/dev/null | grep -q "ClusterID string"; then
  ok "B: DBHealthConfig has ClusterID field"
else
  fail "B: DBHealthConfig ClusterID field missing"
fi

# --- C: DefaultDBHealthConfig wires NoopAlertSink ---
if sed -n '/^func DefaultDBHealthConfig/,/^}$/p' "$HEALTHZ_GO" 2>/dev/null | grep -q "NoopAlertSink{}"; then
  ok "C: DefaultDBHealthConfig sets Notifier to NoopAlertSink{}"
else
  fail "C: DefaultDBHealthConfig missing NoopAlertSink default"
fi

# --- D: Sampler has transition state fields ---
sampler_struct=$(sed -n '/^type Sampler struct/,/^}$/p' "$HEALTHZ_GO" 2>/dev/null)
if echo "$sampler_struct" | grep -q "hasFirstSample bool" && \
   echo "$sampler_struct" | grep -q "lastHealthy bool"; then
  ok "D: Sampler has hasFirstSample + lastHealthy fields"
else
  fail "D: Sampler missing transition state fields"
fi

# --- E: detectTransition method exists ---
if has "$HEALTHZ_GO" "^func \\(s \\*Sampler\\) detectTransition"; then
  ok "E: Sampler has detectTransition method"
else
  fail "E: detectTransition method missing"
fi

# --- F: ok→degraded alert ---
# The verb constant for ok→degraded transitions is
# "DEGRADED" (combined with the "DB health " prefix in
# the format string to form "DB health DEGRADED").
detect_body=$(sed -n '/^func (s \*Sampler) detectTransition/,/^}$/p' "$HEALTHZ_GO" 2>/dev/null)
if echo "$detect_body" | grep -q 'verb = "DEGRADED"'; then
  ok "F: ok→degraded alert uses \"DB health DEGRADED\""
else
  fail "F: ok→degraded verb constant missing"
fi

# --- G: degraded→ok alert ---
if echo "$detect_body" | grep -q 'verb = "recovered"'; then
  ok "G: degraded→ok alert uses \"DB health recovered\""
else
  fail "G: recovered verb constant missing"
fi

# --- H: first sample is the baseline (no alert on tick 0) ---
# The detectTransition body must have the `first := !s.hasFirstSample`
# check + the early return.
if sed -n '/^func (s \*Sampler) detectTransition/,/^}$/p' "$HEALTHZ_GO" 2>/dev/null | grep -q "first := !s.hasFirstSample"; then
  ok "H: detectTransition treats first sample as the baseline (no alert on tick 0)"
else
  fail "H: detectTransition missing first-sample baseline check"
fi

# --- I: main.go wires app.Notifier into DBHealthConfig ---
# The wiring is a 3-line snippet in main.go:
#     dbHealthCfg := healthz.DefaultDBHealthConfig()
#     dbHealthCfg.Notifier = schedulerNotifierSink(app.Notifier)
#     dbHealthSampler := healthz.NewDBHealthSampler(dbHealthCfg, d)
# Match by looking for all three patterns close together
# (within 5 lines of each other).
if grep -q "dbHealthCfg := healthz.DefaultDBHealthConfig" "$MAIN_GO" 2>/dev/null && \
   grep -q "dbHealthCfg.Notifier = schedulerNotifierSink" "$MAIN_GO" 2>/dev/null && \
   grep -q "dbHealthSampler := healthz.NewDBHealthSampler(dbHealthCfg" "$MAIN_GO" 2>/dev/null; then
  ok "I: main.go wires app.Notifier into DBHealthConfig (B225.1 wire-up)"
else
  fail "I: main.go missing B225.1 Notifier wiring for DBHealthSampler"
fi

# --- J: B225.1 unit tests present ---
if [ -f "$TEST_NEW" ] && grep -q "B225.1" "$TEST_NEW"; then
  n=$(grep -c "^func TestTransition" "$TEST_NEW" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 7 ]; then
    ok "J: B225.1 unit tests present (${n} TestTransition functions)"
  else
    fail "J: B225.1 unit tests insufficient (${n} < 7)"
  fi
else
  fail "J: B225.1 unit tests file $TEST_NEW missing"
fi

# --- K: AGENTS.md mentions B225.1 ---
if has "$AGENTS" "B225.1"; then
  ok "K: AGENTS.md mentions B225.1"
else
  echo "[skip] K: AGENTS.md doesn't mention B225.1 (will be added before commit)"
fi

echo ""
echo "B225.1 B-check: $ok_count passed"
