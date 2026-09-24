#!/usr/bin/env bash
# check_b315_derp_metrics_endpoint.sh
#
# 2026-09-24 (B315, v1.5.80) — WHERE /admin/derp reads derper's metrics from, and
# why the page must never draw a zero it did not measure.
#
# OPERATOR REPORT (verbatim, on the agent VM):
#
#   «по DERP все еще не доступно на VM agent он заявляет публичный адрес - адрес
#    локальной машины а не публичный адрес домена derp.example.com, и также висит
#    предупреждение о падаваемых метриках что никак не управляется и не
#    объясняется для администратора. Найди причину и предложи варианты»
#
# Two independent defects, both measured live:
#
#   1. derper serves /debug/* to loopback and tailnet sources only (upstream
#      tsweb.AllowDebugAccess) — the same request answered 200 with ~6.3 KB from
#      the host's loopback and 403 "debug access denied" from the container. The
#      verdict is made on the SOURCE address, so the B296 probe-host knob (which
#      makes probes REACH the relay) could never make them ADMITTED. The page then
#      drew `0` in every traffic tile — a number that looks measured — next to one
#      banner that named neither a cause nor a fix.
#   2. the «Публичный IP» row fell back to `detectEgressIP()`, i.e. the container's
#      OWN outbound address (172.18.0.x on the docker bridge), and printed it under
#      a label that promises a public address.
#
# CONTRACTS
#   A. the metrics endpoint is a knob with two layers (DB > .env > none) that is
#      re-resolved per render, validated with a specific code per refusal, and
#      defaults to "none" rather than to a guessed address
#   B. /admin/derp prefers the endpoint for the rich metrics, keeps the relay for
#      the liveness probes, and records WHICH base served the render
#   C. the metrics verdict is explicit (available / named failure / no body size),
#      so the tiles can render "—" instead of zeros
#   D. the STUN verdict names its vantage point and prefers the relay's own
#      counters when they are readable
#   E. the public-address row never presents an egress fallback as the answer, and
#      the container's dial address is a separate row
#   F. the bridge ships inside the binary (subcommand + closed path allow-list +
#      client allow-list), so the operator installs nothing new
#   G. the form exists (save / clear / test-without-saving), is routed, and is
#      audited; refusals are flashes, never a raw error page
#   H. i18n RU+EN pairs for every new key
#   I. tests exist and pass; this script is tracked by git
#
# The whole block is Docker-free and offline-safe: every check is a structural
# grep or a Go test, so it SKIPs nothing on a machine without the VM.

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B315: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

CFG=internal/derpcfg/derpcfg.go
MET=internal/feature/admin/derp_metrics.go
DERP=internal/feature/admin/derp.go
PROXY=internal/derpmetricsproxy/proxy.go
CMD=cmd/skygate/derp_metrics_proxy.go
TPL=internal/handlers/templates/admin/derp.html
I18N=internal/i18n/catalog_derp.go
ROUTE=cmd/skygate/main.go

hdr "B315 — the metrics endpoint, and a page that admits what it did not measure"

# --- A: the knob ------------------------------------------------------------------
if grep -q 'DebugSettingKey = "derp.debug_url"' "$CFG" \
   && grep -q 'DebugEnvKey = "SKYGATE_DERP_DEBUG_URL"' "$CFG"; then
  ok "A1: the metrics endpoint owns its own global_settings row and env var"
else
  bad "A1: the derp.debug_url / SKYGATE_DERP_DEBUG_URL key pair is missing"
fi
if grep -q 'func ResolveDebug(d \*sql.DB) Resolution' "$CFG" \
   && grep -q 'func DebugBaseURL(d \*sql.DB) string' "$CFG" \
   && grep -q 'func SaveDebug(d \*sql.DB, raw string) error' "$CFG"; then
  ok "A2: resolve / base-URL / save are the package's public surface"
else
  bad "A2: ResolveDebug / DebugBaseURL / SaveDebug are missing"
fi
if grep -q 'return Resolution{Host: "", Source: SourceDefault}' "$CFG" \
   && grep -q 'func ValidateDebugURL(raw string) (string, error)' "$CFG"; then
  ok "A3: no endpoint is the DEFAULT state (never a guessed address), and the value is validated"
else
  bad "A3: the default layer or the validator is missing"
fi
for code in spaces scheme port invalid; do
  if grep -q "Code: \"$code\"" "$CFG"; then
    ok "A4.$code: a refusal carries its own code so the page can say WHY"
  else
    bad "A4.$code: ValidateDebugURL does not distinguish the '$code' refusal"
  fi
done
if grep -q 'DebugSettingKey, v' "$CFG" && grep -q 'db.GetGlobalSetting(d, DebugSettingKey' "$CFG"; then
  ok "A5: the DB row is read per resolution (an edit applies to the next render)"
else
  bad "A5: the DB layer is not read through GetGlobalSetting"
fi

# --- B: the read path --------------------------------------------------------------
if grep -q 'resolveMetricsEndpoint(s.dbc(), derpURL)' "$DERP" \
   && grep -q 'mep := resolveMetricsEndpoint' "$DERP"; then
  ok "B1: collectDerpStatus resolves the endpoint for THIS render"
else
  bad "B1: collectDerpStatus does not resolve a metrics endpoint"
fi
if grep -q 'metricsBase, metricsDial := derpURL, dialAddr' "$DERP" \
   && grep -q 'metricsBase, metricsDial = mep.Base, ""' "$DERP"; then
  ok "B2: the endpoint replaces the base for the metrics paths, the relay stays the fallback"
else
  bad "B2: the endpoint is not wired as the metrics base"
fi
if grep -q 'mget("/debug/vars")' "$DERP" && grep -q 'mget("/active-conn")' "$DERP" \
   && grep -q 'mget("/all-recent")' "$DERP" && grep -q 'mget("/debug/")' "$DERP"; then
  ok "B3: all four rich-metrics paths go through the endpoint"
else
  bad "B3: a rich-metrics path still bypasses the endpoint"
fi
if grep -q 'if _, err := get("/"); err == nil {' "$DERP"; then
  ok "B4: the liveness probe still hits the RELAY (a bridge would 404 there)"
else
  bad "B4: the liveness probe was moved onto the metrics base"
fi
if grep -q 'MetricsVia = "proxy"' "$DERP" && grep -q 'MetricsVia = "relay"' "$DERP"; then
  ok "B5: the render records which base served it"
else
  bad "B5: MetricsVia is not set on both branches"
fi

# --- C: the verdict ----------------------------------------------------------------
for f in MetricsAvailable MetricsErrCode MetricsErrDetail MetricsHTTPStatus MetricsBytes; do
  if grep -q "$f" "$DERP"; then
    ok "C.$f: the status carries $f"
  else
    bad "C.$f: $f is not on DerpStatus"
  fi
done
if grep -q 'func parseDerperVars(st \*DerpStatus, body \[\]byte) bool' "$DERP" \
   && grep -q 'if _, ok := probe\["derp"\]; !ok {' "$DERP"; then
  ok "C6: a 200 without a derp block is NOT counted as measured metrics"
else
  bad "C6: parseDerperVars still accepts any JSON object as metrics"
fi
if grep -q 'if v.DERP.Accepts >= 0 {' "$DERP"; then
  bad "C7: the pre-B315 'Accepts >= 0' tautology is still there (it marks {} as Running)"
else
  ok "C7: the tautological presence test is gone"
fi
if grep -q 'st.MetricsErrCode = "unconfigured"' "$DERP" \
   && grep -q 'st.MetricsErrCode = "denied"' "$DERP" \
   && grep -q 'st.MetricsErrCode = "badstatus"' "$DERP" \
   && grep -q 'st.MetricsErrCode = "badbody"' "$DERP"; then
  ok "C8: every failure shape gets its own code (unconfigured / denied / badstatus / badbody)"
else
  bad "C8: one of the failure codes is missing"
fi
if grep -q 'func httpGetViaStatus' "$DERP"; then
  ok "C9: the status code survives the read (a 502 from a broken bridge is not a 403)"
else
  bad "C9: httpGetViaStatus is missing"
fi

# --- D: the STUN tile tells the truth ----------------------------------------------
if grep -q 'st.STUNSource = "relay"' "$DERP" && grep -q 'st.STUNSource = "container"' "$DERP"; then
  ok "D1: the STUN verdict names its vantage point"
else
  bad "D1: STUNSource is not set on both branches"
fi
if grep -q 'st.STUNListening = st.STUNRequests > 0' "$DERP"; then
  ok "D2: with metrics available the verdict is derper's OWN counters, not the container's UDP egress"
else
  bad "D2: the relay counters do not drive the STUN verdict"
fi
if grep -q 'STUNNotSTUN int' "$DERP" && grep -q 'st.STUNNotSTUN = v.STUN.CounterRequests.NotSTUN' "$DERP"; then
  ok "D3: the not_stun counter is read out, so a red tile can explain itself"
else
  bad "D3: the not_stun counter is not surfaced"
fi
if grep -q 'probeSTUNForStatus(&st, s.dbc(), derpHost, stunPort)' "$DERP"; then
  ok "D4: without metrics the B265 UDP round trip is still the fallback"
else
  bad "D4: the B265 container probe was dropped"
fi
# D5/D6 — the connected-socket defect. A UDP socket CONNECTED to the relay's
# address drops a reply whose source was rewritten on the way (the operator's host
# DNATs :3478 onto the docker bridge gateway), so the probe reported "closed" on a
# relay that answered instantly. Measured live both ways before the fix.
if grep -q 'conn, err := net.ListenUDP(network, nil)' internal/feature/admin/derp_stun.go \
   && ! grep -qE '^[[:space:]]*conn, err := net\.DialTimeout\("udp"' internal/feature/admin/derp_stun.go; then
  ok "D5: the STUN probe uses an UNCONNECTED socket (a connected one silently drops a rewritten reply)"
else
  bad "D5: the STUN probe still dials a connected UDP socket"
fi
if grep -q 'res.ReplyFrom = from.String()' internal/feature/admin/derp_stun.go \
   && grep -q 'st.STUNReplyFrom = res.ReplyFrom' internal/feature/admin/derp_stun.go \
   && grep -q 'res.ReplyFrom != net.JoinHostPort(host, stunPort)' internal/feature/admin/derp_stun.go; then
  ok "D6: the answering address is recorded and reported only when it differs from the address dialled"
else
  bad "D6: STUNReplyFrom is not threaded from the socket to the page"
fi
if grep -q 'TestProbeSTUNAcceptsARewrittenReplySource_B315' internal/feature/admin/derp_stun_b315_test.go \
   && grep -q 'TestProbeSTUNStillRejectsAForeignTransaction_B315' internal/feature/admin/derp_stun_b315_test.go; then
  ok "D7: the rewrite case AND the foreign-transaction refusal are pinned by tests"
else
  bad "D7: the STUN socket change has no regression test"
fi

# --- E: the address rows -----------------------------------------------------------
if grep -q 'WhiteIPFallback = strings.HasPrefix(src, "egress")' "$DERP" \
   && grep -q 'WhiteIPFallback bool' "$DERP"; then
  ok "E1: an egress fallback is flagged as NOT a public address"
else
  bad "E1: the egress fallback is not flagged"
fi
if grep -q '.DerpStatus.WhiteIPFallback' "$TPL" && grep -q 'derp.field_public_ip_egress' "$TPL"; then
  ok "E2: the template refuses to print a fallback under «Публичный IP»"
else
  bad "E2: the public-IP row can still show the egress address unlabelled"
fi
if grep -q 'ProbeDialAddr   string' "$DERP" && grep -q 'derp.field_probe_dial' "$TPL"; then
  ok "E3: the container's dial address is its OWN row, separate from the clients' address"
else
  bad "E3: the two addresses are still conflated on the page"
fi
if grep -q 'st.ProbeDialSource = string(derpcfg.Resolve(s.dbc()).Source)' "$DERP"; then
  ok "E4: the dial row shows which layer supplied the address"
else
  bad "E4: the dial address has no source annotation"
fi

# --- F: the bridge ships in the binary ---------------------------------------------
if grep -q 'case "derp-metrics-proxy"' "$ROUTE" && grep -q 'runDerpMetricsProxy' "$ROUTE"; then
  ok "F1: the bridge is a subcommand of the binary the operator already deploys"
else
  bad "F1: the derp-metrics-proxy subcommand is not wired"
fi
if grep -q 'func Run(ctx context.Context, cfg Config) error' "$PROXY" \
   && grep -q 'func ParseArgs(args \[\]string) (Config, error)' "$PROXY"; then
  ok "F2: the proxy has a testable config surface and a runnable server"
else
  bad "F2: the proxy entry points are missing"
fi
if grep -q 'var AllowedPaths = \[\]string{' "$PROXY" && grep -q 'func PathAllowed(p string) bool' "$PROXY"; then
  ok "F3: the forwarded paths are a closed allow-list"
else
  bad "F3: the path allow-list is missing"
fi
if grep -q 'func ipAllowed(ip net.IP, allow \[\]\*net.IPNet) bool' "$PROXY" \
   && grep -q 'DefaultAllowedNets = \[\]string{' "$PROXY"; then
  ok "F4: a client allow-list refuses public sources even on a 0.0.0.0 bind"
else
  bad "F4: the client allow-list is missing"
fi
if grep -q 'upstreamHint' "$PROXY" && grep -q '\-\-server-name' "$PROXY"; then
  ok "F5: an upstream TLS failure names the flag that fixes it"
else
  bad "F5: a bridge failure reaches the page without a next step"
fi
if grep -q 'X-Skygate-Metrics-Via' "$PROXY"; then
  ok "F6: the bridge marks the responses it served (debuggable from the container)"
else
  bad "F6: the bridge does not identify its own responses"
fi

# --- G: the operator's controls ----------------------------------------------------
if grep -q 'func (s \*Service) PostAdminDerpMetricsEndpoint' "$MET"; then
  ok "G1: the save/clear/test handler exists"
else
  bad "G1: PostAdminDerpMetricsEndpoint is missing"
fi
if grep -q 'action == "test"' "$MET" && grep -q 'action == "clear"' "$MET"; then
  ok "G2: test-without-saving and clear are distinct actions"
else
  bad "G2: the test or clear action is missing"
fi
if grep -q 'POST /admin/derp/metrics-endpoint' "$ROUTE"; then
  ok "G3: the form is routed on the page that shows the warning"
else
  bad "G3: the route is not registered"
fi
if grep -q '"derp.metrics_endpoint"' "$MET"; then
  ok "G4: every outcome (save / clear / test / refusal) is audited"
else
  bad "G4: the handler does not audit"
fi
if grep -q 'action="/admin/derp/metrics-endpoint"' "$TPL" \
   && grep -q 'name="action" value="save"' "$TPL" \
   && grep -q 'name="action" value="test"' "$TPL" \
   && grep -q 'name="action" value="clear"' "$TPL"; then
  ok "G5: the page renders the three controls"
else
  bad "G5: the form is incomplete on the page"
fi
if grep -q 'Skygate derp-metrics-proxy\|skygate derp-metrics-proxy' "$TPL" \
   && grep -q 'docker run -d --name skygate-derp-metrics' "$TPL"; then
  ok "G6: the page prints the exact host commands for both install kinds"
else
  bad "G6: the page does not show how to start the bridge"
fi
if grep -q 'derp.metrics_unavailable' "$TPL"; then
  n=$(grep -c 'derp.metrics_unavailable' "$TPL")
  if [ "$n" -ge 4 ]; then
    ok "G7: all four traffic tiles fall back to «—» instead of 0 ($n references)"
  else
    bad "G7: only $n tile(s) were guarded — the rest still draw an unmeasured zero"
  fi
else
  bad "G7: the unavailable marker is missing from the template"
fi
if grep -q 'derp.debug_access_denied' "$TPL"; then
  ok "G8: the B265 debug-denied caveat is still rendered (contract H of check_b265 stays true)"
else
  bad "G8: the debug-denied caveat disappeared from the page"
fi

# --- H: i18n parity -----------------------------------------------------------------
MISSING=""
for k in derp.metrics_title derp.metrics_help derp.metrics_current derp.metrics_none \
         derp.metrics_src_db derp.metrics_src_env derp.metrics_src_default \
         derp.metrics_field derp.metrics_field_help derp.metrics_save derp.metrics_clear \
         derp.metrics_test derp.metrics_saved derp.metrics_cleared derp.metrics_env \
         derp.metrics_test_empty derp.metrics_test_ok derp.metrics_test_fail \
         derp.metrics_test_status derp.metrics_err_unconfigured derp.metrics_err_denied \
         derp.metrics_err_unreachable derp.metrics_err_badstatus derp.metrics_err_badbody \
         derp.metrics_err_form derp.metrics_err_spaces derp.metrics_err_scheme \
         derp.metrics_err_port derp.metrics_err_invalid derp.metrics_via_proxy \
         derp.metrics_via_relay derp.metrics_setup_title derp.metrics_setup_hint \
         derp.metrics_setup_docker derp.metrics_setup_native derp.metrics_setup_after \
         derp.metrics_unavailable derp.metrics_tiles_hint derp.stun_source_relay \
         derp.stun_source_container derp.stun_counters derp.field_probe_dial \
         derp.field_probe_dial_help derp.field_public_ip_unknown derp.field_public_ip_egress \
         derp.stun_reply_from derp.stun_reply_from_help; do
  n=$(grep -c "\"$k\":" "$I18N")
  if [ "$n" -lt 2 ]; then MISSING="$MISSING $k($n)"; fi
done
if [ -z "$MISSING" ]; then
  ok "H1: every B315 key is defined in BOTH catalogues (rule 10)"
else
  bad "H1: keys missing from one side (count in parentheses):$MISSING"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/i18n/ -run TestCatalogsParity -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "H2: the i18n parity test passes"
  else
    bad "H2: i18n parity failed:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "H2: go not on PATH — run the i18n parity test on the VM"
fi

# --- I: tests + git -----------------------------------------------------------------
for f in internal/derpcfg/derpcfg_b315_test.go internal/derpmetricsproxy/proxy_b315_test.go \
         internal/feature/admin/derp_metrics_b315_test.go internal/feature/admin/derp_stun_b315_test.go; do
  if [ -f "$f" ]; then ok "I1: $f exists"; else bad "I1: $f is missing"; fi
done
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/derpcfg/ ./internal/derpmetricsproxy/ ./internal/feature/admin/ -run 'B315' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT" && ! grep -q 'FAIL' <<< "$OUT"; then
    ok "I2: the B315 Go tests pass"
  else
    bad "I2: the B315 Go tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
  OUT="$(go vet ./internal/derpcfg/ ./internal/derpmetricsproxy/ ./internal/feature/admin/ ./cmd/skygate/ 2>&1)"
  if [ -z "$OUT" ]; then
    ok "I3: go vet is clean on every package this block touches"
  else
    bad "I3: go vet reported:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "I2/I3: go not on PATH — run the B315 tests on the VM"
fi
if git ls-files --error-unmatch scripts/check_b315_derp_metrics_endpoint.sh >/dev/null 2>&1; then
  ok "I4: scripts/check_b315_derp_metrics_endpoint.sh is tracked by git"
else
  bad "I4: scripts/check_b315_derp_metrics_endpoint.sh is NOT tracked (AGENTS trap #11)"
fi
# The bridge must not be published on a public address by default in the docs the
# operator copies: the two printed commands must bind a bridge/loopback address.
if grep -q 'listen 172.18.0.1:8767' "$TPL"; then
  ok "I5: the printed commands bind the docker bridge address, not 0.0.0.0"
else
  bad "I5: the printed commands do not pin a bridge address"
fi

printf '\n\033[1mB315 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
