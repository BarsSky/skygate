#!/usr/bin/env bash
# check_b268_applier_failure_diagnostics.sh
#
# 2026-09-19 (B268) — the privileged update applier must explain WHY a
# swap failed, and must never swap in a binary that cannot run.
#
# Live case: a native/systemd self-update on a remote VM ended with
#
#   ROLLBACK: restoring the previous binary …
#   verdict: rolled_back (healthz did not report build 'v1.5.11' within
#           90s (last build: none))
#
# and nothing else. That single sentence is equally true for "the unit is
# masked", "the new binary crashes on startup", "the port is already held
# by another process" and "the service listens on another port" — the
# operator had no way to tell which, and the answer was not in the UI.
#
# B268 makes the applier:
#   * probe the extracted binary with `--version` BEFORE the swap
#     (`binary_smoke_test`), refusing to install one that cannot execute;
#   * log `service_diagnostics` (unit state, port owner, binary on disk,
#     journal tail) before any rollback;
#   * distinguish "the service did not come up" from "the service came up
#     but reports the wrong build", and append the unit state to both.
#
# CONTRACTS
#   A  source contracts (smoke test before swap, diagnostics, verdicts)
#   B  behavioural: healthy body → done, stale body → rolled_back +
#      diagnostics, dead service → distinct verdict + diagnostics
#      B6/B7 (B269): a body with "ready":false (correct build, still
#      booting) must NOT be accepted as a successful update, and the
#      applier must name the boot phase while it waits
#   C  go test ./internal/update/ -run B268
#
# Section B needs a Linux userland (systemctl/ss stubs + /tmp). It SKIPs
# elsewhere — never FAILs — per the project's live-state rule.

set -uo pipefail
# Resolve the repo root the way the rest of the suite does (relative to the
# script), with fallbacks for the case where the check is copied elsewhere
# (e.g. /tmp on a Linux host) — then verify the applier is actually visible,
# so a wrong CWD fails loudly instead of silently reporting seven FAILs.
if [ -f "$(dirname "$0")/../deploy/skygate-apply-update.sh" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/deploy/skygate-apply-update.sh" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f deploy/skygate-apply-update.sh ] || {
  printf 'B268: cannot locate deploy/skygate-apply-update.sh from %s (run this check from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

APPLIER=deploy/skygate-apply-update.sh

hdr "B268 — applier failure diagnostics"

# --- A: source contracts -----------------------------------------------------
if grep -q 'binary_smoke_test()' "$APPLIER" \
   && grep -q 'binary_smoke_test "\$NEW_BIN"' "$APPLIER" \
   && grep -q -- '--version' "$APPLIER"; then
  ok "A: the extracted binary is smoke-tested before the swap"
else
  bad "A: no pre-swap smoke test — an unrunnable artifact would still replace the live binary"
fi
if grep -q 'service_diagnostics()' "$APPLIER" \
   && grep -q 'DIAG: unit state:' "$APPLIER" \
   && grep -q 'DIAG: listener on port' "$APPLIER" \
   && grep -q 'DIAG: last journal lines for' "$APPLIER"; then
  ok "A2: service_diagnostics covers unit state, port owner and journal tail"
else
  bad "A2: service_diagnostics is incomplete"
fi
if grep -q 'the service did not come up:' "$APPLIER" \
   && grep -q 'unit state: \$(service_state)' "$APPLIER"; then
  ok "A3: verdicts distinguish 'never came up' from 'wrong build' and carry the unit state"
else
  bad "A3: verdicts are still ambiguous"
fi
if grep -q 'pre-swap health baseline:' "$APPLIER" \
   && grep -q 'a SECOND skygate instance (container/other unit) may own that port' "$APPLIER"; then
  ok "A4: the health endpoint is baselined BEFORE the swap (catches a port owned by another instance)"
else
  bad "A4: no pre-swap health baseline"
fi

# --- C: Go contracts --------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  if go test ./internal/update/ -run 'B268' -count=1 >/dev/null 2>&1; then
    ok "C: go test ./internal/update/ -run B268 passes"
  else
    bad "C: B268 Go contracts failed"
  fi
  if go vet ./internal/update/ >/dev/null 2>&1; then
    ok "C2: go vet ./internal/update clean"
  else
    bad "C2: go vet reported issues"
  fi
else
  skip "C: go not reachable in this bash PATH"
fi

# --- B: behavioural (needs a Linux userland) --------------------------------
if [ "$(uname -s)" = "Linux" ] && command -v python3 >/dev/null 2>&1; then
  ROOT="$(mktemp -d /tmp/b268check.XXXXXX)"
  MIRROR_PID=""
  # B281 (2026-09-22): the mirror server is killed by the trap, using the REAL
  # python pid. Pre-fix the pid came from `( cd … && python3 … & echo $! )`, i.e.
  # the subshell's pid, so the kill at the end of the block missed python and
  # every run leaked a server holding the port. This check ran with hardcoded
  # 18098/18099: a leaked (or otherwise occupied) port made the applier talk to
  # a server whose document root no longer existed, so the download 404'd and
  # the four download-dependent contracts failed with no visible reason
  # (`curl: (22) … 404` was swallowed by run_case's redirect).
  cleanup_b268() {
    [ -n "$MIRROR_PID" ] && kill "$MIRROR_PID" 2>/dev/null
    rm -rf "$ROOT"
  }
  trap cleanup_b268 EXIT
  mkdir -p "$ROOT"/{bin,stubs,update,mirror,state,etc}
  mkdir -p "$ROOT/mirror/v1.5.99"

  # Two FREE ports, chosen by the kernel: no stale listener and no collision with
  # a concurrent check can turn this into a false failure.
  free_port() {
    python3 -c 'import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()'
  }
  MIRROR_PORT="$(free_port)"
  HEALTH_PORT="$(free_port)"

  # stubs: the applier must not be able to see the real service manager
  cat > "$ROOT/stubs/systemctl" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  restart) exit 0 ;;
  is-active) cat "$B268_SERVICE_STATE" 2>/dev/null || echo inactive; exit 0 ;;
esac
exit 0
EOF
  printf '#!/usr/bin/env bash\necho "python3 pid=4242"\n' > "$ROOT/stubs/ss"
  printf '#!/usr/bin/env bash\necho "skygate.service: Failed with result '"'"'exit-code'"'"'."\necho "skygate.service: bind: address already in use"\n' > "$ROOT/stubs/journalctl"
  printf '#!/usr/bin/env bash\nshift 2; shift; exec "$@"\n' > "$ROOT/stubs/runuser"
  cat > "$ROOT/stubs/curl" <<'EOF'
#!/usr/bin/env bash
for a in "$@"; do
  case "$a" in
    *"/healthz") if [ -n "${B268_HEALTH_BODY:-}" ]; then printf '%s' "$B268_HEALTH_BODY"; exit 0; fi; exit 7 ;;
  esac
done
exec /usr/bin/curl "$@"
EOF
  # B281 (2026-09-22): `install` is stubbed for the same reason as systemctl —
  # the real one chowns to root:root (deploy/skygate-apply-update.sh
  # atomic_install: `install -m 0755 -o root -g root …`), which only root may do.
  # The applier is DESIGNED to run as root (systemd path unit), so on an
  # unprivileged host (the CI runner, this workstation) every install failed with
  # `changing ownership of …: Operation not permitted` and the four
  # download-dependent contracts reported FAIL — a privilege fact about the
  # runner, not a defect in the diagnostics B268 pins. The stub copies + chmods
  # and RECORDS the argv, so the root:root request is still asserted (B8) and a
  # regression that stops asking for root ownership cannot hide behind the stub.
  cat > "$ROOT/stubs/install" <<'EOF'
#!/usr/bin/env bash
[ -n "${B268_INSTALL_LOG:-}" ] && echo "install $*" >> "$B268_INSTALL_LOG"
mode=0755; src=""; dst=""
while [ $# -gt 0 ]; do
  case "$1" in
    -m) mode="$2"; shift 2 ;;
    -o|-g) shift 2 ;;
    -*) shift ;;
    *) if [ -z "$src" ]; then src="$1"; else dst="$1"; fi; shift ;;
  esac
done
[ -n "$src" ] && [ -n "$dst" ] || { echo "install stub: missing operand" >&2; exit 1; }
cp -f "$src" "$dst" && chmod "$mode" "$dst"
EOF
  chmod +x "$ROOT/stubs"/*
  cat > "$ROOT/etc/update.conf" <<EOF
SKYGATE_UPDATE_DIR="$ROOT/update"
SKYGATE_UPDATE_MODE="systemd"
SKYGATE_UPDATE_SERVICE="skygate"
SKYGATE_UPDATE_BINARY="$ROOT/bin/skygate"
SKYGATE_UPDATE_RUN_USER="$(id -un)"
SKYGATE_UPDATE_ENV_FILE="$ROOT/etc/skygate.env"
SKYGATE_UPDATE_HEALTH_URL="http://127.0.0.1:$HEALTH_PORT/healthz"
SKYGATE_UPDATE_BASE_URL="http://127.0.0.1:$MIRROR_PORT"
SKYGATE_UPDATE_HEALTH_TIMEOUT="4"
SKYGATE_UPDATE_HEALTH_POLL="1"
EOF
  cat > "$ROOT/etc/skygate.env" <<EOF
SKYGATE_DB=sqlite:$ROOT/state/test.db
SKYGATE_JWT_SECRET=0123456789abcdef0123456789abcdef
HEADSCALE_API_KEY=test
HEADSCALE_URL=http://127.0.0.1:1
EOF

  # a fake "current" binary: a script that reports a build, so the
  # download can be a trivial tarball (the smoke test then also passes)
  cat > "$ROOT/build.sh" <<'EOF'
#!/usr/bin/env bash
[ "$1" = "--version" ] && { echo "skygate v1.5.99 (test)"; exit 0; }
echo '{"build":"v1.5.99+test","status":"ok"}'
exit 0
EOF
  chmod +x "$ROOT/build.sh"
  mkdir -p "$ROOT/build"; cp "$ROOT/build.sh" "$ROOT/build/skygate"
  tar -C "$ROOT/build" -czf "$ROOT/mirror/v1.5.99/skygate-v1.5.99-linux-amd64.tar.gz" skygate
  ( cd "$ROOT/mirror/v1.5.99" && sha256sum skygate-v1.5.99-linux-amd64.tar.gz > SHA256SUMS )
  # `--directory` makes the served root explicit (no `cd`-in-a-background-list
  # subtlety) and `$!` is python's own pid, so the trap can actually kill it.
  python3 -m http.server "$MIRROR_PORT" --bind 127.0.0.1 \
    --directory "$ROOT/mirror" >"$ROOT/mirror.out" 2>&1 &
  MIRROR_PID=$!
  # Wait for READINESS instead of `sleep 1`: the download-dependent contracts
  # (B, B2, B3, B5) failed on a loaded runner because the server was not
  # answering yet, and the only symptom was a 404 swallowed by run_case.
  MIRROR_READY=0
  for _ in $(seq 1 50); do
    if /usr/bin/curl -fsS -o /dev/null "http://127.0.0.1:$MIRROR_PORT/v1.5.99/SHA256SUMS" 2>/dev/null; then
      MIRROR_READY=1
      break
    fi
    sleep 0.2
  done
  if [ "$MIRROR_READY" != "1" ]; then
    skip "B: the local artifact mirror never became ready on 127.0.0.1:$MIRROR_PORT (see $ROOT/mirror.out) — behavioural half needs it"
    kill "$MIRROR_PID" 2>/dev/null || true
    MIRROR_PID=""
    printf '\n\033[1mB268 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
    exit 0
  fi

  run_case() { # name health_body unit_state
    rm -rf "$ROOT/update"; mkdir -p "$ROOT/update"
    rm -f "$ROOT/install.argv"
    cp "$ROOT/build.sh" "$ROOT/bin/skygate"; chmod 0755 "$ROOT/bin/skygate"
    printf 'TARGET=v1.5.99\nFROM_VERSION=v1.4.0\nJOB_ID=deadbeefcafe\nRUNTIME_PID=\nREQUESTED_AT=2026-09-19T15:01:42Z\n' > "$ROOT/update/request.props"
    B268_HEALTH_BODY="$2" B268_SERVICE_STATE="$3" B268_INSTALL_LOG="$ROOT/install.argv" \
      PATH="$ROOT/stubs:$PATH" \
      SKYGATE_HELPER_CONF="$ROOT/etc/update.conf" \
      bash "$APPLIER" >/dev/null 2>&1
    cat "$ROOT/update/result.status" 2>/dev/null
  }

  NEW='{"build":"v1.5.99+test","instance_id":"x","status":"ok","timestamp":"t"}'
  OLD='{"build":"v1.4.0+old","instance_id":"x","status":"ok","timestamp":"t"}'

  if [ "$(run_case healthy "$NEW" active)" = "done" ]; then
    ok "B: healthy service reporting the target build → done"
  else
    bad "B: healthy path did not finish with 'done'"
  fi
  if [ "$(run_case stale "$OLD" active)" = "rolled_back" ]; then
    ok "B2: service up but reporting the old build → rolled_back"
  else
    bad "B2: stale-build path did not roll back"
  fi
  # B281: the `install` stub must not be able to hide the root:root request —
  # a stubbed privileged step is only honest if we still assert WHAT was asked
  # for (deploy/skygate-apply-update.sh atomic_install).
  if [ -f "$ROOT/install.argv" ] && grep -q -- '-o root -g root' "$ROOT/install.argv"; then
    ok "B8: the applier still installs the binary with -o root -g root (stub recorded the argv)"
  else
    bad "B8: no 'install … -o root -g root' invocation recorded (got: $(head -c 200 "$ROOT/install.argv" 2>/dev/null))"
  fi
  # run_case rewrites apply.log for every case, so the diagnostics of this
  # case are inspected immediately after it.
  if grep -q 'DIAG: unit state:' "$ROOT/update/apply.log" 2>/dev/null \
     && grep -q 'DIAG: listener on port' "$ROOT/update/apply.log" 2>/dev/null; then
    ok "B3: diagnostics were written for the stale-build failure"
  else
    bad "B3: no DIAG lines were written for the stale-build failure"
  fi
  dead_verdict="$(run_case dead "" failed)"
  # A service that never comes up can end as rolled_back (the rollback
  # itself restored a healthy service) OR as failed (even the rollback
  # could not bring the port back — the reference host's case, where the
  # old binary was healthy but was not the one holding :8080). Both are
  # honest; what matters is that the message names the real failure.
  case "$dead_verdict" in
    rolled_back|failed) ok "B4: service that never came up → $dead_verdict (honest verdict)" ;;
    *) bad "B4: dead-service path produced verdict '$dead_verdict'" ;;
  esac
  if grep -q 'the service did not come up' "$ROOT/update/apply.log" 2>/dev/null \
     && grep -q 'DIAG: listener on port' "$ROOT/update/apply.log" 2>/dev/null \
     && grep -q 'DIAG: last journal lines for' "$ROOT/update/apply.log" 2>/dev/null; then
    ok "B5: the dead-service run names the real failure and logs unit/port/journal diagnostics"
  else
    bad "B5: the dead-service run is still ambiguous (no 'did not come up' verdict or no DIAG lines)"
  fi

  # B269: since the socket is bound at the top of main(), /healthz answers
  # with the NEW build string while the DB and the services are still being
  # constructed. That body carries "ready":false and MUST NOT be enough to
  # declare the swap successful (otherwise B269 would have made the updater
  # blind to exactly the class of failure B268 was written for).
  printf '  .. B269 contract: a booting body (\"ready\":false, correct build) must not count as healthy\n'
  sed -i 's/^SKYGATE_UPDATE_HEALTH_TIMEOUT=.*/SKYGATE_UPDATE_HEALTH_TIMEOUT="9"/' "$ROOT/etc/update.conf"
  boot_verdict="$(run_case booting '{"status":"ok","build":"v1.5.99+test","ready":false,"phase":"db-open+migrate","stage":"boot"}' active)"
  case "$boot_verdict" in
    rolled_back|failed) ok "B6: healthz reporting the target build but still booting → $boot_verdict (not done)" ;;
    done) bad "B6: a still-booting body was accepted as a successful update (regression of the B269 readiness guard)" ;;
    *) bad "B6: booting-body case produced verdict '$boot_verdict'" ;;
  esac
  if grep -q "still starting" "$ROOT/update/apply.log" 2>/dev/null; then
    ok "B7: the applier names the boot phase while it waits (phase=db-open+migrate)"
  else
    bad "B7: the applier did not log the boot phase — the operator cannot see WHERE the new binary is stuck"
  fi
  sed -i 's/^SKYGATE_UPDATE_HEALTH_TIMEOUT=.*/SKYGATE_UPDATE_HEALTH_TIMEOUT="4"/' "$ROOT/etc/update.conf"

  kill "$MIRROR_PID" 2>/dev/null || true
  MIRROR_PID=""
else
  skip "B: behavioural applier test needs Linux + python3 (run this check on the VM/CI)"
fi

printf '\n\033[1mB268 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
