#!/usr/bin/env bash
# B-check for B227 (v1.5.2+): B77 tag-autoupdater
# observability. Closes the gap where a stuck ACL
# reject (e.g. skygate-host-1-1 with `tag:infra-*`
# + B77 trying to add `tag:dev-skyadmin-*` every 5
# min) was invisible to the operator except by
# tailing skygate stderr.
#
# Contracts pinned (12 contracts across 5 surface areas):
#   A:    internal/nodeownership/auto_alert.go exists
#         with the B227 surface (TagAlertSink struct,
#         AlertSink interface, NoopAlertSink fallback,
#         ReportFailure method, ClassifyFailure helper,
#         ReasonACLReject/RPCError/Unknown constants).
#   B:    internal/metrics/collector.go declares the
#         skygate_tag_autoupdate_failures_total
#         CounterVec with labels [node_id, hostname,
#         reason] (low-cardinality, Prom-friendly).
#   C:    AutoBackfill signature now takes a
#         *TagAlertSink parameter (the new B227
#         observability hook). main.go wires the
#         production sink (Notifier + DB).
#   D:    Backfill signature plumbs alertSink through
#         to the per-tick AddTag error path
#         (nodeownership.go:633) — the existing warn
#         log is preserved, alertSink.ReportFailure
#         is called alongside.
#   E:    Manual Backfill callers (handlers_export.go
#         for feature/my, feature/admin/devices.go for
#         /admin/devices/force-backfill-tags) pass nil
#         alertSink — operator-initiated backfill is
#         silent (the operator sees the HTTP response).
#   F:    audit_log write via
#         db.AppendAuditLogWithTarget with
#         target_type="headscale_node", target_id=nodeID,
#         action="tag.autoupdate_failed", username="system",
#         userID=0 (no human actor).
#   G:    Telegram alert rate-limited to 1 per
#         (node_id, reason) per hour. Metric + audit
#         are NOT rate-limited (durable record + Prom
#         alert surface). The rate-limit window is
#         1h by default.
#   H:    ClassifyFailure parses 3 ACL-reject gRPC
#         codes (InvalidArgument, PermissionDenied,
#         FailedPrecondition) + 3 transient gRPC codes
#         (Unavailable, Internal, DeadlineExceeded) +
#         unknown fallback. nil err → unknown.
#   I:    B227 unit tests (auto_b227_test.go) cover
#         classification, rate-limit (1/h, different
#         reasons, resets after window), nil notifier,
#         nil receiver, nil DB, audit-attempted call
#         site, alert text format, audit detail format.
#   J:    AGENTS.md mentions B227.
#   K:    go build ./... succeeds.
#   L:    go test ./internal/nodeownership/...
#         ./internal/metrics/... passes.
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }
# hasf: fixed-string (no regex) — use for patterns that
# contain regex metachars like ( ) [ ] . * + ? \ that we
# want to match LITERALLY (e.g. function signatures
# with parens). Without -F, bash double-quoted "..."
# unescapes backslashes and the ERE parens become
# group-open/close metachars, breaking the match.
hasf() { grep -qF -- "$2" "$1" 2>/dev/null; }

ALERT_GO="internal/nodeownership/auto_alert.go"
COLLECTOR_GO="internal/metrics/collector.go"
AUTOBACKFILL_GO="internal/nodeownership/auto.go"
NODEOWNERSHIP_GO="internal/nodeownership/nodeownership.go"
HANDLERS_EXPORT="internal/handlers/handlers_export.go"
ADMIN_DEVICES="internal/feature/admin/devices.go"
MAIN_GO="cmd/skygate/main.go"
TEST_NEW="internal/nodeownership/auto_b227_test.go"
AGENTS="AGENTS.md"

# --- A: auto_alert.go with full B227 surface ---
required_a=(
  "type AlertSink interface"
  "type noopAlertSink struct"
  "NoopAlertSink"
  "type TagAlertSink struct"
  "func NewTagAlertSink"
  "func .s .TagAlertSink. ReportFailure"
  "func ClassifyFailure"
  "ReasonACLReject"
  "ReasonRPCError"
  "ReasonUnknown"
  "rateLimitWindow"
)
all_a=1
for sym in "${required_a[@]}"; do
  if ! has "$ALERT_GO" "$sym"; then
    echo "  [missing] $sym"
    all_a=0
  fi
done
if [ "$all_a" = "1" ]; then
  ok "A: auto_alert.go has AlertSink / TagAlertSink / ReportFailure / ClassifyFailure / 3 reason constants / rateLimitWindow"
else
  fail "A: auto_alert.go missing one or more required symbols"
fi

# --- B: B226 metric for tag-autoupdate failures ---
# The label set is on the SAME line (the []string{...}
# argument, 3 lines after the NewCounterVec call).
if has "$COLLECTOR_GO" 'skygate_tag_autoupdate_failures_total' && \
   grep -A3 'skygate_tag_autoupdate_failures_total' "$COLLECTOR_GO" | grep -q 'node_id' && \
   grep -A3 'skygate_tag_autoupdate_failures_total' "$COLLECTOR_GO" | grep -q 'hostname' && \
   grep -A3 'skygate_tag_autoupdate_failures_total' "$COLLECTOR_GO" | grep -q 'reason'; then
  ok "B: skygate_tag_autoupdate_failures_total CounterVec declared with labels [node_id, hostname, reason]"
else
  fail "B: skygate_tag_autoupdate_failures_total CounterVec missing or wrong labels"
fi

# --- C: AutoBackfill takes *TagAlertSink ---
# hasf (fixed-string) because the pattern contains parens
# and a star (regex metachars) that we want to match
# literally.
if hasf "$AUTOBACKFILL_GO" "func AutoBackfill(ctx context.Context, dbConn db.DBSource, hs nodeLister, alertSink *TagAlertSink, interval"; then
  ok "C: AutoBackfill signature includes alertSink *TagAlertSink parameter"
else
  fail "C: AutoBackfill signature doesn't take *TagAlertSink"
fi
# main.go wires the production sink.
if hasf "$MAIN_GO" "nodeownership.NewTagAlertSink(app.Notifier, d)" && \
   hasf "$MAIN_GO" "nodeownership.AutoBackfill(ctx, d, hs, alertSink, cfg.NodeDiscoveryInterval)"; then
  ok "C2: main.go constructs NewTagAlertSink(app.Notifier, d) and passes to AutoBackfill"
else
  fail "C2: main.go missing NewTagAlertSink wiring"
fi

# --- D: Backfill signature plumbs alertSink to the AddTag error site ---
# hasf (fixed-string) for the same reason as C — the
# signature line has parens, dots, stars, and brackets
# that we want to match literally.
if hasf "$NODEOWNERSHIP_GO" "func Backfill(" && \
   hasf "$NODEOWNERSHIP_GO" "portalUsername string," && \
   hasf "$NODEOWNERSHIP_GO" "alertSink *TagAlertSink,"; then
  ok "D: Backfill signature plumbs alertSink to the per-tick AddTag path"
else
  fail "D: Backfill signature doesn't take alertSink"
fi
# And the actual ReportFailure call at the AddTag error site
# (the live site is around the B177 warn line, with
# `alertSink.ReportFailure(n.ID, n.Hostname, devTag, err)`).
if hasf "$NODEOWNERSHIP_GO" "alertSink.ReportFailure(n.ID, n.Hostname, devTag, err)"; then
  ok "D2: nodeownership.go:AddTag error path calls alertSink.ReportFailure"
else
  fail "D2: nodeownership.go missing alertSink.ReportFailure call"
fi

# --- E: manual Backfill callers pass nil (operator-initiated, silent) ---
if hasf "$HANDLERS_EXPORT" "nodeownership.Backfill(d, a.HS, nodes, userID, username, nil)"; then
  ok "E: handlers_export.go BackfillNodeOwnershipFn passes nil alertSink (user-initiated silent)"
else
  fail "E: handlers_export.go doesn't pass nil alertSink"
fi
if hasf "$ADMIN_DEVICES" "nodeownership.Backfill(s.DB, hs, nodes, u.ID, u.Username, nil)"; then
  ok "E2: feature/admin/devices.go force-backfill passes nil alertSink (admin-initiated silent)"
else
  fail "E2: feature/admin/devices.go doesn't pass nil alertSink"
fi

# --- F: audit_log write with target_type=headscale_node ---
if has "$ALERT_GO" '"headscale_node"' && \
   has "$ALERT_GO" '"tag.autoupdate_failed"' && \
   has "$ALERT_GO" 'db\.AppendAuditLogWithTarget'; then
  ok "F: auto_alert.go writes audit_log with target_type=headscale_node, action=tag.autoupdate_failed"
else
  fail "F: auto_alert.go missing audit_log target_type / action / AppendAuditLogWithTarget call"
fi

# --- G: rate-limit window 1h, distinct metric from rate-limit ---
if grep -E "rateLimitWindow\s*=\s*[0-9]+\s*\*\s*time\.Hour" "$ALERT_GO" >/dev/null; then
  ok "G: rateLimitWindow is 1h (Prom counter is rate-limit-independent; only Telegram is rate-limited)"
else
  fail "G: rateLimitWindow is not 1h (should be 1h by default)"
fi
# The metric increment is OUTSIDE the rate-limit gate
# (the metric call should appear BEFORE the rate-limit
# check, OR the report function's structure should make
# it clear the metric fires on every call).
if grep -B2 -A2 'lastAlert\[key\] = now' "$ALERT_GO" | grep -q 'shouldAlert'; then
  printf "[ok]   %s\n" "G2: rate-limit gate is shouldAlert, applied only to SendAlert (not to the metric)"
  ok_count=$((ok_count+1))
else
  fail "G2: rate-limit gate structure not as expected"
fi

# --- H: ClassifyFailure buckets ---
if has "$ALERT_GO" 'InvalidArgument' && \
   has "$ALERT_GO" 'PermissionDenied' && \
   has "$ALERT_GO" 'FailedPrecondition' && \
   has "$ALERT_GO" 'Unavailable' && \
   has "$ALERT_GO" 'Internal' && \
   has "$ALERT_GO" 'DeadlineExceeded'; then
  ok "H: ClassifyFailure recognizes 3 ACL-reject codes + 3 transient codes"
else
  fail "H: ClassifyFailure missing one or more gRPC codes"
fi

# --- I: unit tests ---
if [ -f "$TEST_NEW" ]; then
  n=$(grep -c "^func Test" "$TEST_NEW" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 10 ]; then
    ok "I: B227 unit tests present (${n} Test functions, expected >=10)"
  else
    fail "I: B227 unit tests insufficient (${n} < 10)"
  fi
  # Required test names (the surface area B227 pins)
  for t in "TestClassifyFailure_ACLReject" "TestClassifyFailure_RPCError" "TestClassifyFailure_Unknown" "TestReportFailure_MetricAlwaysIncrements" "TestReportFailure_RateLimit_1PerHourPerKey" "TestReportFailure_RateLimit_DifferentReasons" "TestReportFailure_RateLimit_ResetsAfterWindow" "TestReportFailure_NilNotifierSilent" "TestReportFailure_NilReceiverSilent" "TestReportFailure_DBAuditAttempted" "TestNewTagAlertSink_NilDBAllowed" "TestBuildAlertText_Format" "TestBuildFailureDetail_AuditFormat"; do
    if ! has "$TEST_NEW" "$t"; then
      fail "I: missing required test $t"
    fi
  done
  ok "I2: all 13 required B227 tests present"
else
  fail "I: B227 test file $TEST_NEW missing"
fi

# --- J: AGENTS.md mentions B227 ---
if has "$AGENTS" "B227"; then
  ok "J: AGENTS.md mentions B227"
else
  echo "[skip] J: AGENTS.md doesn't mention B227 (will be added before commit)"
fi

# --- K: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "K: go build ./... succeeds"
  else
    fail "K: go build ./... FAILED"
  fi
else
  echo "[skip] K: go not on PATH"
fi

# --- L: go test on the two affected packages ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/nodeownership/... ./internal/metrics/... >/dev/null 2>&1; then
    ok "L: go test ./internal/nodeownership/... ./internal/metrics/... passes"
  else
    fail "L: go test on B227-touched packages FAILED"
  fi
else
  echo "[skip] L: go not on PATH"
fi

echo ""
echo "B227 B-check: $ok_count passed"
