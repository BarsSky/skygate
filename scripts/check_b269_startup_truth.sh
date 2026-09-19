#!/usr/bin/env bash
# check_b269_startup_truth.sh
#
# 2026-09-19 (B269) — a skygate process must never be "active" with nothing
# listening, and it must say WHERE it is while it boots.
#
# Live case (native/systemd install, remote VM): the unit reported `active`,
# `ss -ltnp` showed no listener on the configured port, and the privileged
# self-updater could only say
#
#   verdict: rolled_back (healthz did not report build 'v1.5.11' within 90s
#            (last build: none))
#
# The process was alive (its DB watchdogs logged normally) but main() had not
# reached ListenAndServe. Nothing anywhere named the blocking phase, and the
# listener — the one thing every downstream check depends on — was created
# LAST, so any early return or panic produced a mute, portless process.
#
# B269 therefore:
#   A  binds the socket at the TOP of main() (config → bind → DB → …), so a
#      port conflict is the first thing reported and the process always has
#      an HTTP face;
#   B  serves a provisional /healthz from that socket (build + phase +
#      timeline) and hands the SAME listener to the real mux once routes are
#      wired — no second bind, no gap, no "active but mute" state;
#   C  logs `startup: phase=<name> +Nms (total=Nms)` on every transition, so
#      the last phase in the journal IS the blocker;
#   D  reports a panic with its stack and its phase, and answers 503 +
#      `error` afterwards instead of a 200 for a dead boot;
#   E  behaviourally proves A+B: with a deliberately broken DB the process
#      stays up, keeps retrying, and still answers /healthz with its build
#      and the `db-open+migrate` phase (SKIPs when the probe cannot run).
#
# The E half needs `go` and a free TCP port; it SKIPs (never FAILs) when the
# probe cannot run, per the project's live-state rule.

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B269: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MAIN=cmd/skygate/main.go
STARTUP=internal/startup/startup.go

hdr "B269 — startup truth (socket first, phase log, provisional healthz)"

# --- A: the package exists with the documented surface ----------------------
if [ -f "$STARTUP" ] \
   && grep -q '^package startup' "$STARTUP" \
   && grep -q 'func StageHandler() http.HandlerFunc' "$STARTUP" \
   && grep -q 'func Enter(name string) func()' "$STARTUP" \
   && grep -q 'func MarkReady()' "$STARTUP" \
   && grep -q 'func SetFatal(reason string)' "$STARTUP" \
   && grep -q 'func Phase() string' "$STARTUP"; then
  ok "A: internal/startup exposes StageHandler/Enter/MarkReady/SetFatal/Phase"
else
  bad "A: internal/startup is missing part of the B269 surface"
fi

# --- A2: the socket is bound BEFORE the database ----------------------------
# Ordering is the whole point: pre-B269 the fatal `listen:` line came last, so
# a broken DB (or a slow migration) produced an `active` unit with no port.
i_bind=$(grep -n 'net.Listen("tcp", addr)' "$MAIN" | head -1 | cut -d: -f1)
i_db=$(grep -n 'db.OpenDSNWithRetry(cfg.DBDSN, 5, 2\*time.Second)' "$MAIN" | head -1 | cut -d: -f1)
if [ -n "$i_bind" ] && [ -n "$i_db" ] && [ "$i_bind" -lt "$i_db" ]; then
  ok "A2: the listener is bound before the DB open (bind@$i_bind < db@$i_db)"
else
  bad "A2: the listener is NOT bound before the DB (bind='${i_bind:-none}' db='${i_db:-none}') — a failed DB open leaves an active unit with no port"
fi

# --- A3: a bind failure is fatal and recorded -------------------------------
if grep -q 'startup.SetFatal(fmt.Sprintf("bind %s: %v", addr, err))' "$MAIN" \
   && grep -q 'func listenAddr(port string) (string, error)' "$MAIN"; then
  ok "A3: a bind failure is validated, recorded as fatal and exits non-zero"
else
  bad "A3: a failed bind is not recorded/reported as the first startup failure"
fi

# --- B: handover to the real mux on the SAME listener -----------------------
if grep -q 'handler.Store(http.Handler(mux))' "$MAIN" \
   && grep -q 'startup.MarkReady()' "$MAIN"; then
  ok "B: the real mux takes over the bound listener and the process is marked ready"
else
  bad "B: no handover of the bound listener to the real mux (would re-bind and race the provisional server)"
fi
if grep -q 'srv.Serve(ln)' "$MAIN"; then
  ok "B2: the real server serves the pre-bound listener"
else
  bad "B2: the real server does not use the listener bound at startup — the 'no gap' property is lost"
fi
if grep -q 'srv.ListenAndServe()' "$MAIN"; then
  bad "B3: main() still calls ListenAndServe — that is a SECOND bind on a port we already own"
else
  ok "B3: no second bind (ListenAndServe removed from the boot path)"
fi

# --- C: the phase log --------------------------------------------------------
if grep -q 'logf("startup: phase=%s +%dms (total=%dms)"' "$STARTUP"; then
  ok "C: every phase transition is logged with its duration"
else
  bad "C: the phase log line is missing — the journal cannot say where boot stalled"
fi
nphases=$(grep -c 'startup\.Enter("' "$MAIN")
if [ "${nphases:-0}" -ge 5 ]; then
  ok "C2: the boot sequence announces $nphases phases (config, DB, headscale, crons, services, routes, handover)"
else
  bad "C2: only ${nphases:-0} phase transitions are instrumented (want >= 5)"
fi
missing=""
for p in 'config' 'db-open+migrate' 'headscale-client' 'background-crons' 'services+telegram' 'routes' 'handover'; do
  grep -q "startup.Enter(\"$p\")" "$MAIN" || missing="$missing $p"
done
if [ -z "$missing" ]; then
  ok "C3: config/db/headscale/crons/services/routes/handover are all announced"
else
  bad "C3: the boot sequence does not announce:$missing"
fi

# --- D: panic + fatal surface ------------------------------------------------
if grep -q 'startup.SetFatal(fmt.Sprintf("panic in phase %q: %v", startup.Phase(), r))' "$MAIN" \
   && grep -q 'debug.Stack()' "$MAIN"; then
  ok "D: a panic records its phase and logs its stack"
else
  bad "D: a panic is not attributed to a startup phase"
fi
if grep -q 'os.Exit(3)' "$MAIN"; then
  ok "D2: a startup panic exits non-zero so the unit is not reported as active"
else
  bad "D2: a startup panic does not exit non-zero (systemd would keep an active, mute unit)"
fi
if grep -q '"status":        status' "$STARTUP" \
   && grep -q 'st.fatal = ""' "$STARTUP" \
   && grep -q 'http.StatusServiceUnavailable' "$STARTUP"; then
  ok "D3: a recorded fatal answers 503 on the provisional /healthz"
else
  bad "D3: the provisional handler cannot report a fatal startup"
fi

# --- D4: the applier must not accept a still-booting body --------------------
APPLIER=deploy/skygate-apply-update.sh
if grep -qF '"ready":false' "$APPLIER" \
   && grep -q 'boot_phase()' "$APPLIER" \
   && grep -q 'still starting after' "$APPLIER"; then
  ok "D4: the applier refuses a booting body (ready:false) and logs the boot phase"
else
  bad "D4: the applier would accept a still-booting process as a successful deployment"
fi

# --- E: behavioural — probe the real binary ---------------------------------
# With an unopenable DB the process must NOT die quietly: it must hold the
# port, keep retrying, and answer /healthz with the build and the phase. This
# is the exact live signature B269 was written for (active unit, no listener).
# The probe drives the REAL binary and fetches /healthz with curl or python3;
# with neither available it SKIPs (never FAILs), per the live-state rule.
probe_fetch_py() { # port
  python3 - "$1" <<'PY' 2>/dev/null || true
import sys, urllib.request
try:
    print(urllib.request.urlopen("http://127.0.0.1:%s/healthz" % sys.argv[1], timeout=2).read().decode())
except Exception:
    pass
PY
}

if ! command -v go >/dev/null 2>&1; then
  skip "E: behavioural probe needs the go toolchain on PATH"
elif ! command -v curl >/dev/null 2>&1 && ! command -v python3 >/dev/null 2>&1; then
  skip "E: behavioural probe needs curl or python3 to fetch /healthz"
else
  TMPD="$(mktemp -d 2>/dev/null || mktemp -d -t b269)"
  KERNEL="$(uname -s)"
  BIN="$TMPD/skygate-probe"
  [ "$KERNEL" = "Linux" ] || BIN="$BIN.exe"
  PORT=$(( 39000 + (RANDOM % 900) ))
  if go build -o "$BIN" -ldflags "-X main.version=v0.0.0-b269probe" ./cmd/skygate > "$TMPD/build.log" 2>&1; then
    if command -v curl >/dev/null 2>&1; then
      fetch() { curl -fsS --max-time 3 "http://127.0.0.1:$PORT/healthz" 2>/dev/null || true; }
    else
      fetch() { probe_fetch_py "$PORT"; }
    fi
    SKYGATE_PORT="$PORT" \
    SKYGATE_DB="sqlite:$TMPD/no-such-dir/skygate.db" \
    SKYGATE_ADMIN_PASS="b269-probe-not-a-secret" \
    HEADSCALE_API_KEY="b269-probe-not-a-key" \
    SKYGATE_JWT_SECRET="b269-probe-not-a-jwt-secret" \
      "$BIN" > "$TMPD/out.log" 2>&1 &
    PROBE_PID=$!

    BODY=""
    i=0
    while [ "$i" -lt 15 ]; do
      i=$((i + 1))
      BODY="$(fetch)"
      [ -n "$BODY" ] && break
      sleep 1
    done
    if [ -z "$BODY" ]; then
      bad "E: the process never answered /healthz while its DB was unreachable (unit would be active + mute — the live B269 failure)"
      sed -n '1,6p' "$TMPD/out.log" >&2 2>/dev/null || true
    else
      case "$BODY" in
        *'"status":"ok"'*) ok "E: /healthz answers 200 + status ok while the DB is still unreachable" ;;
        *) bad "E: /healthz body has no status:ok — $BODY" ;;
      esac
      case "$BODY" in
        *'"build":"v0.0.0-b269probe"'*) ok "E2: the build string is reported from the first moment (the applier's build check can pass)" ;;
        *) bad "E2: /healthz does not report the build — $BODY" ;;
      esac
      case "$BODY" in
        *'"phase":"db-open+migrate"'*) ok "E3: the body names the blocking phase (db-open+migrate)" ;;
        *) bad "E3: the body does not name the blocking phase — $BODY" ;;
      esac
      case "$BODY" in
        *'"ready":false'*) ok "E4: an unfinished boot is marked ready:false so the applier keeps waiting" ;;
        *) bad "E4: the boot marker is missing — a still-starting process could be mistaken for a deployed one" ;;
      esac
      if kill -0 "$PROBE_PID" 2>/dev/null; then
        ok "E5: the process stays alive through the DB retry budget (it does not die mute)"
      else
        bad "E5: the process died instead of holding the port and reporting its phase"
      fi
    fi
    kill "$PROBE_PID" 2>/dev/null || true
    wait "$PROBE_PID" 2>/dev/null || true
  else
    skip "E: could not build the probe binary (see $TMPD/build.log)"
  fi
  rm -rf "$TMPD" 2>/dev/null || true
fi

printf '\n\033[1mB269 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
