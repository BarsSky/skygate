#!/usr/bin/env bash
# check_b270_startup_blockers.sh
#
# 2026-09-19 (B270) — two independent startup blockers found from ONE operator
# report on a native VM. The unit was `active` with nothing listening, and the
# privileged self-updater could only say
#
#   verdict: rolled_back (healthz did not report build 'v1.5.11' within 90s
#            (last build: none))
#
# The operator's own diagnostics showed the real causes:
#
#   ExecStart=/usr/local/bin/skygate        (WorkingDirectory unset in the unit)
#   2026/09/19 17:17:14  Skygate starting on :8082
#   2026/09/19 17:17:15  oidc: SKYGATE_OIDC_ISSUER not set — …
#   2026/09/19 17:17:15  oidc: init failed: oidc: mkdir ./data/oidc-keys:
#                        mkdir ./data: permission denied
#
# 1. The OIDC key store was FATAL. `oidc: init failed` called log.Fatalf, which
#    killed the process BEFORE it bound its HTTP port. OIDC was not even
#    configured (SKYGATE_OIDC_ISSUER unset) — a side feature nobody used took
#    the whole control plane down, and because nothing listened, the updater
#    could never verify a build.
#
# 2. The key dir defaulted to the CWD-relative "./data/oidc-keys". Any unit
#    whose WorkingDirectory is not the data dir (or an older unit that has no
#    WorkingDirectory at all) resolves it against / and MkdirAll fails with
#    permission denied.
#
# Plus the silent trap that made the update loop unwinnable: the applier polls
# http://127.0.0.1:8080/healthz, but this host runs with SKYGATE_PORT=8082 — the
# two numbers were never compared anywhere, so every update rolled back on a
# perfectly healthy service.
#
# CONTRACTS
#   A  the OIDC key store is non-fatal (service keeps running, routes 503)
#   B  config default is an ABSOLUTE, data-dir-anchored path (+ pure helper)
#   C  the installers create the key dir with the service user's ownership
#   D  the applier warns when the health port and SKYGATE_PORT disagree
#   E  Go tests + a live-start probe proving /healthz answers despite a broken
#      key dir (SKIPs when the probe cannot run)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B270: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

MAIN=cmd/skygate/main.go
OIDC_SVC=internal/oidc/service.go
OIDC_KEYS=internal/oidc/keys.go
CONFIG=internal/config/config.go
APPLIER=deploy/skygate-apply-update.sh

hdr "B270 — startup blockers (non-fatal OIDC key store, absolute key dir, port mismatch)"

# --- A: the OIDC key store must not kill the process ------------------------
if ! grep -q 'log.Fatalf("oidc: init failed' "$MAIN"; then
  ok "A: main.go no longer calls log.Fatalf when the OIDC provider cannot initialise"
else
  bad "A: 'log.Fatalf(\"oidc: init failed' is still in main.go — a broken (or unconfigured) OIDC key store kills the process before it listens"
fi
if grep -q 'oidc: init failed: %v (continuing' "$MAIN"; then
  ok "A2: the failure is logged as a warning that names the consequence (503 routes, portal unaffected)"
else
  bad "A2: the non-fatal path is not logged actionably"
fi
if grep -q 'log.Printf("oidc: KEY STORE UNAVAILABLE' "$OIDC_SVC" \
   && grep -q 'KeyStoreErr' "$OIDC_SVC"; then
  ok "A3: NewService degrades to a nil key store and records the reason"
else
  bad "A3: NewService still fails hard on an unusable key dir"
fi
if grep -q 'return nil, err' "$OIDC_SVC"; then
  bad "A4: NewService still returns an error for the key-store failure"
else
  ok "A4: no fatal error return remains on the key-store path"
fi
if grep -q 'func (ks \*KeyStore) Ready() bool {' "$OIDC_KEYS" \
   && grep -q 'if ks == nil {' "$OIDC_KEYS"; then
  ok "A5: KeyStore.Ready() is nil-safe so a nil store answers 503 instead of panicking"
else
  bad "A5: Ready() dereferences a possibly-nil key store"
fi

# --- B: the default key dir is absolute and data-dir anchored ---------------
if grep -q 'func defaultOIDCKeyDir(dsn string) string' "$CONFIG" \
   && grep -q 'func sqlitePathFromDSN(dsn string) (string, bool)' "$CONFIG"; then
  ok "B: the default key dir is derived from the data dir through a pure helper"
else
  bad "B: no data-dir-anchored default — the CWD-relative ./data default can return"
fi
if grep -q 'if os.Getenv("SKYGATE_OIDC_KEY_DIR") == "" {' "$CONFIG"; then
  ok "B2: an explicit SKYGATE_OIDC_KEY_DIR still wins over the derived default"
else
  bad "B2: the derived default would override an operator's explicit setting"
fi
if grep -q 'return "/var/lib/skygate/oidc-keys"' "$CONFIG"; then
  ok "B3: a PostgreSQL install (no local DB file to anchor to) still gets an absolute path"
else
  bad "B3: no absolute fallback for the PostgreSQL case"
fi
if grep -q 'OIDCKeyDir:         getenv("SKYGATE_OIDC_KEY_DIR", "./data/oidc-keys")' "$CONFIG"; then
  bad "B4: the relative ./data/oidc-keys default is still present in the struct literal"
else
  ok "B4: the relative default is gone from the Config literal"
fi
# The literal must still be produced by NewKeyStore when dir is empty (that is
# the developer-machine path) — pin it so the fix is not "delete the fallback".
if grep -q 'dir = "./data/oidc-keys"' "$OIDC_KEYS"; then
  ok "B5: the package-level empty-dir fallback is preserved for direct callers"
else
  bad "B5: the package-level fallback disappeared (external callers passing \"\" would panic)"
fi

# --- C: the installers create the key dir -----------------------------------
for inst in deploy/install-common.sh deploy/install-alpine.sh; do
  if grep -q 'oidc-keys' "$inst"; then
    ok "C: $inst creates the OIDC key dir"
  else
    bad "C: $inst does not create the OIDC key dir — a fresh native install starts with an unwritable/absent path"
  fi
done
if grep -q 'install -d -m 0700 -o "$user" -g "$user" "$data_dir/oidc-keys"' deploy/install-common.sh; then
  ok "C2: the key dir is 0700 and owned by the service user (it holds a private key)"
else
  bad "C2: the key dir is not created 0700 + service-owned in install-common.sh"
fi

# --- D: the applier compares the health port with SKYGATE_PORT --------------
if grep -q 'port_mismatch_warning()' "$APPLIER" \
   && grep -q 'PORT MISMATCH' "$APPLIER" \
   && grep -q 'configured_port()' "$APPLIER"; then
  ok "D: the applier compares \$HEALTH_URL's port with SKYGATE_PORT from the env file"
else
  bad "D: nothing compares the health URL's port with the configured port — updates roll back forever on a healthy service"
fi
if grep -q 'port_mismatch_warning$' "$APPLIER" || grep -q '^port_mismatch_warning$' "$APPLIER"; then
  ok "D2: the check runs (not just defined) in the pre-swap path"
else
  bad "D2: port_mismatch_warning is defined but never called"
fi

# --- E: unit tests + live probe ---------------------------------------------
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 -run 'B270' ./internal/oidc/ ./internal/config/ 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "E: the B270 Go contracts pass (degradation, nil-safety, JWKS 503, healthy path)"
  else
    bad "E: the B270 Go contracts failed: $OUT"
  fi
else
  skip "E: go not on PATH"
fi

# Live probe: start the real binary with an UNCREATABLE key dir and assert
# /healthz still answers (the exact live failure, now survivable).
if ! command -v go >/dev/null 2>&1; then
  skip "E2: live probe needs go"
elif ! command -v curl >/dev/null 2>&1; then
  skip "E2: live probe needs curl"
else
  TMPD="$(mktemp -d 2>/dev/null || mktemp -d -t b270)"
  KERNEL="$(uname -s)"
  # The binary is a NATIVE process: on Windows/MSYS a /c/... path handed to the
  # Go sqlite driver does not resolve (the parent dir "does not exist"), the
  # DB open retries and the boot never reaches 'ready'. Convert once, here.
  DSNROOT="$TMPD"
  case "$KERNEL" in
    MINGW*|MSYS*|CYGWIN*)
      if command -v cygpath >/dev/null 2>&1; then DSNROOT="$(cygpath -w "$TMPD")"; fi
      ;;
  esac
  BIN="$TMPD/skygate-probe"
  [ "$KERNEL" = "Linux" ] || BIN="$BIN.exe"
  PORT=$(( 40200 + (RANDOM % 500) ))
  # SQLite cannot create the database file if its directory is missing, and it
  # reports that as "unable to open database file: out of memory (14)" — the
  # probe would then die of a broken DB instead of exercising the OIDC path.
  mkdir -p "$TMPD/state"
  # The blocker is a regular FILE where the key dir's parent should be, so
  # MkdirAll fails identically on Linux and Windows.
  printf 'x' > "$TMPD/blocker"
  if go build -o "$BIN" -ldflags "-X main.version=v0.0.0-b270probe" ./cmd/skygate > "$TMPD/build.log" 2>&1; then
    SKYGATE_PORT="$PORT" \
    SKYGATE_DB="sqlite:$DSNROOT/state/skygate.db" \
    SKYGATE_OIDC_KEY_DIR="$DSNROOT/blocker/oidc-keys" \
    SKYGATE_ADMIN_PASS="b270-probe-not-a-secret" \
    HEADSCALE_API_KEY="b270-probe-not-a-key" \
    SKYGATE_JWT_SECRET="b270-probe-not-a-jwt-secret" \
      "$BIN" > "$TMPD/out.log" 2>&1 &
    PROBE_PID=$!
    BODY=""
    i=0
    while [ "$i" -lt 30 ]; do
      i=$((i + 1))
      BODY="$(curl -fsS --max-time 2 "http://127.0.0.1:$PORT/healthz" 2>/dev/null || true)"
      [ -n "$BODY" ] && break
      sleep 1
    done
    if [ -z "$BODY" ]; then
      bad "E2: the process never answered /healthz with an unusable OIDC key dir (the pre-B270 fatal path)"
      sed -n '1,10p' "$TMPD/out.log" >&2 2>/dev/null || true
    else
      case "$BODY" in
        *'"status":"ok"'*) ok "E2: /healthz answers 200 even though the OIDC key dir cannot be created — OIDC no longer blocks the portal" ;;
        *) bad "E2: /healthz body is not healthy — $BODY" ;;
      esac
      # Wait (bounded) for the OIDC block to be reached. That is where the
      # pre-B270 process died, so seeing the log line AND a live process
      # afterwards is the whole contract. We deliberately do NOT require
      # 'startup: ready': later phases (headscale client warm-up) touch the
      # network and their duration depends on the probe host's DNS, which has
      # nothing to do with the OIDC blocker under test.
      j=0
      while [ "$j" -lt 40 ]; do
        j=$((j + 1))
        grep -q 'KEY STORE UNAVAILABLE' "$TMPD/out.log" 2>/dev/null && break
        sleep 1
      done
      if grep -q 'KEY STORE UNAVAILABLE' "$TMPD/out.log" 2>/dev/null; then
        ok "E3: the journal names the real reason (KEY STORE UNAVAILABLE + the path)"
      else
        bad "E3: the key-store failure was not logged actionably"
        printf '        --- probe log (%s) ---\n' "$TMPD/out.log" >&2
        tail -12 "$TMPD/out.log" 2>/dev/null | sed 's/^/        /' >&2
      fi
      if kill -0 "$PROBE_PID" 2>/dev/null; then
        ok "E4: the process is STILL ALIVE after the OIDC failure (pre-B270 it exited here, before ever listening)"
      else
        bad "E4: the process died when the OIDC key store failed — the degradation contract is broken"
      fi
      BODY2="$(curl -fsS --max-time 3 "http://127.0.0.1:$PORT/healthz" 2>/dev/null || true)"
      case "$BODY2" in
        *'"status":"ok"'*) ok "E4b: /healthz still answers after the OIDC failure" ;;
        *) bad "E4b: /healthz stopped answering after the OIDC failure (body: ${BODY2:-empty})" ;;
      esac
      if grep -q 'fatal error:' "$TMPD/out.log" 2>/dev/null || grep -q 'PANIC' "$TMPD/out.log" 2>/dev/null; then
        bad "E5: the process logged a fatal error/panic despite the degradation contract"
      else
        ok "E5: no fatal error and no panic in the whole boot"
      fi
    fi
    kill "$PROBE_PID" 2>/dev/null || true
    wait "$PROBE_PID" 2>/dev/null || true
  else
    skip "E2: could not build the probe binary (see $TMPD/build.log)"
  fi
  rm -rf "$TMPD" 2>/dev/null || true
fi

# --- F: the DB-health sampler must speak the DB's dialect (B271) ------------
# The operator's journal showed, every 30 s: "db_health: tick: db_health: 4 query
# error(s): [server: SQL logic error: no such function: pg_is_in_recovery …]"
# — PostgreSQL catalog SQL running against SQLite.
HEALTHZ_GO=internal/feature/healthz/db_health.go
if grep -q 'Dialect string' "$HEALTHZ_GO" \
   && grep -q 'func (c DBHealthConfig) isSQLite() bool' "$HEALTHZ_GO" \
   && grep -q 'func (s \*Sampler) collectSQLite(' "$HEALTHZ_GO"; then
  ok "F: the sampler has a dialect switch and a SQLite collector"
else
  bad "F: the sampler is still PostgreSQL-only (SQLite installs log 5 query errors per tick)"
fi
if grep -q 'Dialect:      "postgres"' "$HEALTHZ_GO"; then
  ok "F2: the default dialect stays postgres (no caller changes behaviour by omission)"
else
  bad "F2: the zero/default dialect is not pinned to postgres"
fi
if grep -q 'PRAGMA page_count' "$HEALTHZ_GO" && grep -q 'PRAGMA quick_check' "$HEALTHZ_GO"; then
  ok "F3: the SQLite branch collects the DB size and runs an integrity check"
else
  bad "F3: the SQLite branch does not collect size/integrity"
fi
if grep -q 'dbHealthCfg.Dialect = dialectKind.String()' "$MAIN"; then
  ok "F4: main.go passes the detected dialect into the sampler"
else
  bad "F4: the detected dialect never reaches the sampler (the switch is dead code)"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 -run 'B271' ./internal/feature/healthz/ 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F5: the dialect Go contracts pass (SQLite branch succeeds, PG branch is provably not what runs)"
  else
    bad "F5: the B271 Go contracts failed: $OUT"
  fi
else
  skip "F5: go not on PATH"
fi

printf '\n\033[1mB270 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
