#!/usr/bin/env bash
# check_b327_prod_health_skip.sh
#
# 2026-09-25 (B327, v1.5.93) — a live-state check must SKIP when its state is
# unavailable, never FAIL, and it must never redden a commit for something the
# operator did to a host.
#
# OPERATOR-REPORTED SYMPTOM (measured, not inferred):
#
#   CI run 36165848857 on a DOCS-ONLY commit failed with
#
#       FAIL  /healthz returned 502 (want 200) — DO NOT auto-deploy
#
#   The operator was updating skygate to v1.5.92 at that moment, so nginx was up
#   while the container was being recreated and the proxy answered 502.
#   Re-running the SAME commit: "catalog clean: 376 PASS, 1 SKIP, 0 FAIL".
#   The verdict tracked the operator's deploy, not the code — and since
#   release.yml's preflight refuses a commit whose CI is not green, a red commit
#   also blocks the NEXT tag.
#
# THE RULE THIS ENFORCES (AGENTS.md §1, rule 1):
#
#   "A check that needs live state (docker daemon, a DB, the VM) must report SKIP,
#    never FAIL, when that state is unavailable."
#
# `000` (no connection) and `502/503/504` (a proxy up while its upstream is
# unavailable) are both "unavailable". A `4xx/5xx` from the application itself, or
# a reachable 200 whose build is `dev`, are real defects and must stay FAIL.
#
# CONTRACTS
#   A. the tri-state classifier exists, reads the strict knob, and the script exits
#      non-zero only on FAIL (source contracts)
#   B. the classifier's decision table for every status, tolerant AND strict,
#      exercised through `--classify` (pure, no network)
#   C. THE BEHAVIOURAL HALF: the real script, driven against a local HTTP stub,
#      must SKIP+exit 0 on 502 and on a dead port, FAIL on 500 and on build=dev,
#      PASS cleanly on a healthy 200, and FAIL on 502 in strict mode
#   D. the strict mode is documented where the pre-deploy flow is described
#   E. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B327: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SCRIPT="scripts/check_b_prod_health.sh"
TMP="$(mktemp -d)"
cleanup() {
  [ -n "${STUB_PID:-}" ] && kill "$STUB_PID" 2>/dev/null
  rm -rf "$TMP"
}
trap cleanup EXIT

hdr "B327 — a live-state check must SKIP when the state is unavailable"

# --- A: source contracts -----------------------------------------------------------------
if [ -f "$SCRIPT" ]; then
  ok "A1: $SCRIPT exists"
else
  bad "A1: $SCRIPT is missing"
fi

if grep -qF 'prod_classify()' "$SCRIPT" && grep -qF 'prod_build_classify()' "$SCRIPT"; then
  ok "A2: the tri-state classifiers exist (prod_classify / prod_build_classify)"
else
  bad "A2: the classifiers are gone — the reachability verdict is not centralised"
fi

if grep -qF '502|503|504)' "$SCRIPT"; then
  ok "A3: 502/503/504 are classified as their own case (a proxy up, its upstream unavailable)"
else
  bad "A3: the 502/503/504 case is missing — a proxy restart can redden the gate again"
fi

if grep -qF '000)' "$SCRIPT"; then
  ok "A4: 000 (no connection at all) is classified as its own case"
else
  bad "A4: the 000 (no connection) case is missing"
fi

if grep -qF 'SKYGATE_PROD_REQUIRE_HEALTHY' "$SCRIPT"; then
  ok "A5: the strict knob SKYGATE_PROD_REQUIRE_HEALTHY is read"
else
  bad "A5: SKYGATE_PROD_REQUIRE_HEALTHY is not read — the pre-deploy gate lost its strictness"
fi

if grep -qE '\[ "\$FAIL" -gt 0 \]' "$SCRIPT" && grep -qF 'exit 1' "$SCRIPT"; then
  ok "A6: the script exits non-zero on FAIL (and only on FAIL)"
else
  bad "A6: the script does not key its exit status on its own FAIL counter"
fi

# The 000/`|| echo` trap: `curl -w '%{http_code}'` already prints 000 on a
# transport failure and ALSO exits non-zero, so `|| echo "000"` produced "000000"
# — an unknown status, i.e. a FAIL. Pin both the normalisation variable and the
# absence of the double-000 idiom (contract C re-proves it behaviourally: the dead
# port case would emit 'unexpected status 000000' without the fix).
if grep -qF 'code:-000' "$SCRIPT" && ! grep -vE '^[[:space:]]*#' "$SCRIPT" | grep -qF '|| echo "000"'; then
  ok "A9: the http status is normalised to one three-digit code (no 000000 from a double 000)"
else
  bad "A9: http_get_status can still emit '000000' (curl's own 000 plus an echoed one), which classifies as an unknown status and FAILs"
fi

if grep -qF 'INCONCLUSIVE' "$SCRIPT"; then
  ok "A7: a tolerant run that SKIPped says so (INCONCLUSIVE, not a clean bill of health)"
else
  bad "A7: a run with SKIP rows could be mistaken for a healthy production"
fi

# The B322 lesson, applied here: a check that counts FAIL must not be able to
# print "Production is healthy" on the FAIL path.
if grep -qE 'if \[ "\$FAIL" -gt 0 \]; then' "$SCRIPT"; then
  ok "A8: the healthy/not-healthy sentence is inside the FAIL branch, not after it"
else
  bad "A8: 'Production is healthy' is printed unconditionally"
fi

# --- B: the pure decision table ----------------------------------------------------------
# expected "<verdict> <exit>" for: status, strict
table_check() {
  local status="$1" strict="$2" want_verdict="$3" want_rc="$4" out rc
  out="$(bash "$SCRIPT" --classify "$status" "$strict" 2>&1)"; rc=$?
  if [ "${out%% *}" = "$want_verdict" ] && [ "$rc" = "$want_rc" ]; then
    ok "B: --classify $status strict=$strict → ${want_verdict} (exit ${want_rc})"
  else
    bad "B: --classify $status strict=$strict → '${out%% *}' exit $rc, want ${want_verdict} exit ${want_rc}"
  fi
}
if [ -x "$SCRIPT" ] || [ -f "$SCRIPT" ]; then
  table_check 200 0 OK   0
  table_check 502 0 SKIP 0
  table_check 503 0 SKIP 0
  table_check 504 0 SKIP 0
  table_check 000 0 SKIP 0
  table_check 500 0 FAIL 1
  table_check 404 0 FAIL 1
  table_check 502 1 FAIL 1
  table_check 000 1 FAIL 1
  table_check 200 1 OK   0
  # `--classify` must agree with the env var, not only with a positional override: an
  # operator who has SKYGATE_PROD_REQUIRE_HEALTHY=1 set and asks about a 502 must not be
  # told SKIP. (This was a real slip: `--classify` read the strict flag only from argv, so
  # it answered for the tolerant run while the env var said otherwise.)
  env_out="$(SKYGATE_PROD_REQUIRE_HEALTHY=1 bash "$SCRIPT" --classify 502 2>&1)"; env_rc=$?
  if [ "${env_out%% *}" = "FAIL" ] && [ "$env_rc" = "1" ]; then
    ok "B: --classify honours SKYGATE_PROD_REQUIRE_HEALTHY (502 with the env var set → FAIL exit 1)"
  else
    bad "B: --classify ignored SKYGATE_PROD_REQUIRE_HEALTHY (502 → '${env_out%% *}' exit $env_rc, want FAIL exit 1) — the inspect mode disagreed with the run"
  fi
else
  bad "B: cannot exercise --classify without $SCRIPT"
fi

# --- C: behavioural, against a local stub -------------------------------------------------
# The whole point of B327 is what the REAL script does with a REAL 502, so this
# half starts an HTTP stub and runs the script against it. It never touches the
# operator's production host: SKYGATE_PROD_URL points at 127.0.0.1.
if ! command -v python3 >/dev/null 2>&1; then
  skip "C: python3 not on PATH — the behavioural half needs a local HTTP stub; run this check on the VM"
else
  cat > "$TMP/stub.py" <<'PY'
import sys, http.server, socketserver
status = int(sys.argv[1])
body = sys.argv[2].encode()
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a):
        pass
srv = socketserver.TCPServer(('127.0.0.1', 0), H)
print(srv.server_address[1], flush=True)
srv.serve_forever()
PY

  start_stub() {
    STUB_PID=""
    STUB_PORT=""
    python3 "$TMP/stub.py" "$1" "$2" > "$TMP/stub.out" 2>"$TMP/stub.err" &
    STUB_PID=$!
    for _ in $(seq 1 50); do
      STUB_PORT="$(head -1 "$TMP/stub.out" 2>/dev/null)"
      [ -n "$STUB_PORT" ] && break
      sleep 0.1
    done
    [ -n "$STUB_PORT" ]
  }
  stop_stub() {
    [ -n "${STUB_PID:-}" ] && kill "$STUB_PID" 2>/dev/null
    wait "$STUB_PID" 2>/dev/null
    STUB_PID=""
  }
  # run_case <name> <base-url> <strict> <want-rc> <want-grep> <want-absent>
  run_case() {
    local name="$1" url="$2" strict="$3" want_rc="$4" want="$5" absent="$6" out rc
    out="$(SKYGATE_PROD_URL="$url" SKYGATE_PROD_REQUIRE_HEALTHY="$strict" \
           bash "$SCRIPT" 2>&1)"; rc=$?
    if [ "$rc" != "$want_rc" ]; then
      bad "C: $name → exit $rc, want $want_rc"
      printf '%s\n' "$out" | sed 's/^/       /' >&2
      return
    fi
    if ! grep -qF "$want" <<< "$out"; then
      bad "C: $name → exit $want_rc as expected, but the output does not contain '$want'"
      printf '%s\n' "$out" | tail -8 | sed 's/^/       /' >&2
      return
    fi
    if [ -n "$absent" ] && grep -qF "$absent" <<< "$out"; then
      bad "C: $name → the output still claims '$absent'"
      return
    fi
    ok "C: $name"
  }

  HEALTHY='{"build":"v1.5.92+abc1234","status":"ok","ready":true}'
  DEVBUILD='{"build":"dev","status":"ok"}'

  if start_stub 502 'nginx: 502 Bad Gateway'; then
    run_case "a 502 from the proxy SKIPs and exits 0 (the CI flapping case)" \
      "http://127.0.0.1:$STUB_PORT" 0 0 "the proxy answered 502, not the application" "All 4 mandatory contracts PASS"
    run_case "a 502 under SKYGATE_PROD_REQUIRE_HEALTHY=1 is a FAIL and exits 1" \
      "http://127.0.0.1:$STUB_PORT" 1 1 "the upstream is unavailable (pre-deploy strict mode)" "All 4 mandatory contracts PASS"
    stop_stub
  else
    bad "C: could not start the 502 stub"
  fi

  if start_stub 200 "$HEALTHY"; then
    run_case "a healthy 200 PASSes all four contracts and exits 0" \
      "http://127.0.0.1:$STUB_PORT" 0 0 "All 4 mandatory contracts PASS" "INCONCLUSIVE"
    stop_stub
  else
    bad "C: could not start the 200 stub"
  fi

  if start_stub 200 "$DEVBUILD"; then
    run_case "a reachable build=dev still FAILs (that IS a property of the deployment)" \
      "http://127.0.0.1:$STUB_PORT" 0 1 "build=dev detected" ""
    stop_stub
  else
    bad "C: could not start the dev stub"
  fi

  if start_stub 500 'internal server error'; then
    run_case "a 500 from the application FAILs (a wrong answer from a reachable stack)" \
      "http://127.0.0.1:$STUB_PORT" 0 1 "the application answered 500" ""
    stop_stub
  else
    bad "C: could not start the 500 stub"
  fi

  # A dead port: bind one, close it, then use it. 000 means "no connection at all".
  DEAD_PORT="$(python3 - <<'PY'
import socket
s = socket.socket(); s.bind(('127.0.0.1', 0)); p = s.getsockname()[1]; s.close(); print(p)
PY
)"
  run_case "no connection at all (000) SKIPs and exits 0" \
    "http://127.0.0.1:${DEAD_PORT}" 0 0 "no connection at all" ""
  run_case "no connection at all (000) under strict mode is a FAIL" \
    "http://127.0.0.1:${DEAD_PORT}" 1 1 "no connection at all" ""
fi

# --- D: the strict mode is documented where the pre-deploy flow lives --------------------
if [ -f docs/operations.md ]; then
  if grep -qF 'SKYGATE_PROD_REQUIRE_HEALTHY' docs/operations.md; then
    ok "D1: docs/operations.md documents the strict knob and where it belongs"
  else
    bad "D1: docs/operations.md does not mention SKYGATE_PROD_REQUIRE_HEALTHY — the pre-deploy flow has no written strictness"
  fi
  if grep -qF '36165848857' docs/operations.md; then
    ok "D2: docs/operations.md records the measured CI run that motivated this"
  else
    bad "D2: docs/operations.md does not carry the evidence for the default"
  fi
else
  bad "D1/D2: docs/operations.md is missing"
fi

# --- E: git ------------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b327_prod_health_skip.sh >/dev/null 2>&1; then
  ok "E1: this script is TRACKED by git (AGENTS trap #11)"
else
  bad "E1: this script is NOT tracked by git"
fi
if git ls-files --error-unmatch "$SCRIPT" >/dev/null 2>&1; then
  ok "E2: $SCRIPT is TRACKED by git"
else
  bad "E2: $SCRIPT is NOT tracked by git"
fi

printf '\n\033[1mB327 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
