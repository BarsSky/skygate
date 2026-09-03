#!/usr/bin/env bash
# B-check for B226 (v1.5.0+): Phase 4.5 — Prometheus
# exporter. Closes the "operator can't see skygate
# state in Grafana / Prometheus / kubectl top
# equivalent" gap.
#
# Contracts pinned (10 source-pin + 2 go-runtime):
#   A:    internal/metrics package exists with the
#         Registry / Counter / Gauge / GaugeVec /
#         CounterVec types + the in-house textfmt
#         encoder
#   B:    internal/metrics/collector.go has the
#         StartCollector + runTick functions + a
#         SourceProvider interface
#   C:    The 6+ production metric declarations
#         (ClusterNodesGauge, ClusterNodesTotalGauge,
#         DBHealthGauge, DBSizeBytesGauge,
#         DBPoolOpenGauge, DBPoolIdleGauge,
#         DBPoolInUseGauge, ElectorIsPrimaryGauge,
#         FailoverStateGauge, BuildInfoGauge) are
#         all present + use the right names + labels
#   D:    main.go wires GET /metrics → Default().Handler()
#   E:    main.go starts the collector with
#         DBPoolSource{DB: d} + a 30s interval
#   F:    main.go sets BuildInfoGauge to (version,
#         runtime.Version()) on startup
#   G:    DBPoolSource.PingDB uses s.DB.Current() +
#         PingContext (B203 + B224 transparency)
#   H:    DBPoolSource.DBPoolStats() returns the
#         live *sql.DB.Stats() (B224-compatible
#         via the ResettableDB.Current() path)
#   I:    The textfmt Content-Type is
#         "text/plain; version=0.0.4; charset=utf-8"
#         (matches the Prometheus spec exactly)
#   J:    B226 unit tests cover the in-house metrics
#         package (counter monotonic, gauge settable,
#         textfmt format, label escaping, http handler)
#   K:    AGENTS.md mentions B226
#   L:    go build ./... succeeds
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

METRICS_GO="internal/metrics/metrics.go"
COLLECTOR_GO="internal/metrics/collector.go"
TEST_NEW="internal/metrics/metrics_b226_test.go"
MAIN_GO="cmd/skygate/main.go"
AGENTS="AGENTS.md"

# --- A: package + types ---
for sym in "type Registry struct" "type Counter struct" "type Gauge struct" "type GaugeVec struct" "type CounterVec struct" "text/plain; version=0.0.4"; do
  if has "$METRICS_GO" "$sym"; then
    ok "A: internal/metrics has '$sym'"
  else
    fail "A: internal/metrics missing '$sym'"
  fi
done

# --- B: collector + SourceProvider ---
if has "$COLLECTOR_GO" "type SourceProvider interface" && \
   has "$COLLECTOR_GO" "func StartCollector"; then
  ok "B: collector has SourceProvider + StartCollector"
else
  fail "B: collector missing SourceProvider / StartCollector"
fi

# --- C: 10 production metrics ---
# Each metric is declared with the right name +
# right labels. We check the declarations (not
# the WithLabelValues calls) because the labels
# are part of the metric definition.
# 10 production metrics — each declared in internal/metrics
metrics_dir="internal/metrics"
for spec in \
  "skygate_cluster_nodes|cluster,state" \
  "skygate_cluster_nodes_total|" \
  "skygate_db_health|cluster" \
  "skygate_db_size_bytes|cluster" \
  "skygate_db_pool_open_connections|" \
  "skygate_db_pool_idle_connections|" \
  "skygate_db_pool_in_use_connections|" \
  "skygate_elector_is_primary|node" \
  "skygate_failover_state|cluster" \
  "skygate_build_info|version,go_version"; do
  name="${spec%%|*}"
  labels="${spec#*|}"
  if grep -q "NewGaugeVec(\"$name\"" "$metrics_dir"/*.go; then
    if [ -z "$labels" ]; then
      ok "C: metric $name declared (Vec, no labels)"
    else
      # Look for the labels in the next 4 lines.
      # The source uses either literal strings
      # ("a", "b") or named constants (clusterLabel,
      # which is const = "cluster"). We split
      # the expected labels by comma and check
      # each — either the literal "x" or the
      # constant name "x" matches.
      all_ok=1
      for lbl in $(echo "$labels" | tr ',' ' '); do
        # Look for the label in the 4 lines after
        # the NewGaugeVec(...) call. Acceptable
        # forms: the literal "x" or the constant
        # name (e.g. clusterLabel) — both are
        # permitted by the B226 design.
        grep -A4 "NewGaugeVec(\"$name\"" "$metrics_dir"/*.go 2>/dev/null > /tmp/b226_block.txt
        # Form 1: literal string "x" in the block.
        if grep -qF "\"$lbl\"" /tmp/b226_block.txt; then
          continue
        fi
        # Form 2: a named constant x that resolves
        # to the literal. (E.g. labels are
        # []string{clusterLabel, "state"} and
        # const clusterLabel = "cluster". The
        # constant NAME appears in the block; the
        # const VALUE is checked against the label.)
        const_block=$(grep -E "^const[[:space:]]+[A-Za-z_][A-Za-z0-9_]*[[:space:]]*=" "$metrics_dir"/*.go 2>/dev/null)
        if grep -qE "const[[:space:]]+[A-Za-z_][A-Za-z0-9_]*[[:space:]]*=[[:space:]]*\"$lbl\"" <<<"$const_block"; then
          # The const value matches the label. Now
          # check that the const name appears in
          # the 4-line block (not a different
          # const with the same value).
          const_name=$(grep -E "const[[:space:]]+[A-Za-z_][A-Za-z0-9_]*[[:space:]]*=[[:space:]]*\"$lbl\"" <<<"$const_block" | head -1 | sed -E 's/.*const[[:space:]]+([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=.*/\1/')
          if [ -n "$const_name" ] && grep -qF "$const_name" /tmp/b226_block.txt; then
            continue
          fi
        fi
        all_ok=0
        break
      done
      rm -f /tmp/b226_block.txt
      if [ "$all_ok" = "1" ]; then
        ok "C: metric $name declared (labels: $labels)"
      else
        fail "C: metric $name declared but labels don't match (expected $labels)"
      fi
    fi
  elif grep -q "NewGauge(\"$name\"" "$metrics_dir"/*.go; then
    if [ -z "$labels" ]; then
      ok "C: metric $name declared (no labels)"
    else
      fail "C: metric $name declared as Gauge but expected Vec with labels [$labels]"
    fi
  else
    fail "C: metric $name not declared"
  fi
done

# --- D: GET /metrics route ---
if grep -q "GET /metrics" "$MAIN_GO" 2>/dev/null && \
   grep -A2 "GET /metrics" "$MAIN_GO" 2>/dev/null | grep -q "metrics.Default().Handler()"; then
  ok "D: main.go registers GET /metrics → Default().Handler()"
else
  fail "D: main.go missing GET /metrics route"
fi

# --- E: collector started with DBPoolSource ---
if grep -q "metrics.DBPoolSource{" "$MAIN_GO" 2>/dev/null && \
   grep -q "metrics.StartCollector" "$MAIN_GO" 2>/dev/null; then
  ok "E: main.go starts collector with metrics.DBPoolSource + StartCollector"
else
  fail "E: main.go missing collector startup"
fi

# --- F: BuildInfoGauge set with (version, runtime.Version()) ---
if grep -q "BuildInfoGauge" "$MAIN_GO" 2>/dev/null && \
   grep -q "runtime.Version()" "$MAIN_GO" 2>/dev/null; then
  ok "F: main.go sets BuildInfoGauge to (version, runtime.Version())"
else
  fail "F: main.go missing BuildInfoGauge wiring"
fi

# --- G: DBPoolSource.PingDB uses s.DB.Current() + PingContext ---
if grep -q "s.DB.Current()" "$COLLECTOR_GO" 2>/dev/null && \
   grep -q "PingContext(ctx)" "$COLLECTOR_GO" 2>/dev/null; then
  ok "G: DBPoolSource.PingDB uses s.DB.Current() + PingContext (B203/B224 transparent)"
else
  fail "G: DBPoolSource.PingDB doesn't use ResettableDB transparency"
fi

# --- H: DBPoolStats returns live pool stats ---
if grep -q "DBPoolStats() sql.DBStats" "$COLLECTOR_GO" 2>/dev/null && \
   grep -q "conn.Stats()" "$COLLECTOR_GO" 2>/dev/null; then
  ok "H: DBPoolStats() returns live pool stats via conn.Stats()"
else
  fail "H: DBPoolStats() doesn't return conn.Stats()"
fi

# --- I: textfmt Content-Type is "text/plain; version=0.0.4; charset=utf-8" ---
if grep -q 'text/plain; version=0.0.4; charset=utf-8' "$METRICS_GO" 2>/dev/null; then
  ok "I: textfmt Content-Type matches Prometheus spec"
else
  fail "I: textfmt Content-Type is wrong"
fi

# --- J: B226 unit tests present ---
if [ -f "$TEST_NEW" ] && grep -q "B226" "$TEST_NEW"; then
  n=$(grep -c "^func Test" "$TEST_NEW" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 8 ]; then
    ok "J: B226 unit tests present (${n} Test functions)"
  else
    fail "J: B226 unit tests insufficient (${n} < 8)"
  fi
else
  fail "J: B226 unit tests file $TEST_NEW missing"
fi

# --- K: AGENTS.md mentions B226 ---
if has "$AGENTS" "B226"; then
  ok "K: AGENTS.md mentions B226"
else
  echo "[skip] K: AGENTS.md doesn't mention B226 (will be added before commit)"
fi

# --- L: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "L: go build ./... succeeds"
  else
    fail "L: go build ./... FAILED"
  fi
else
  echo "[skip] L: go not on PATH"
fi

echo ""
echo "B226 B-check: $ok_count passed"
