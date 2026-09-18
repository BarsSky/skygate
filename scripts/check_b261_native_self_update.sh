#!/usr/bin/env bash
# check_b261_native_self_update.sh
#
# 2026-09-18 (B261 / plan §12.15) — native (systemd / bare-binary)
# self-update.
#
# Pre-fix state: only the Docker install kind had an auto-updater.
# /admin/update's "Update now" / "Push update" buttons on a native
# install fell into a `default:` branch that failed the job with
# "auto-updater for systemd not yet implemented (v0.29.0 Phase 2 covers
# Docker only)" and told the operator to run the manual steps.
#
# The reason it was never implemented is a privilege problem, not a
# coding one: the native unit runs skygate as an unprivileged user
# under ProtectSystem=strict (install-common.sh:write_systemd_unit), so
# the process cannot write /usr/local/bin/skygate and cannot
# `systemctl restart skygate` (no polkit rule, no sudo). Restarting the
# unit from inside the unit additionally kills any setsid'd child,
# because systemd kills the whole cgroup.
#
# What this check pins:
#
#   A) internal/update/native.go exists with the NativeUpgrader,
#      ConfirmNativeSwap and NativeReleaseTagFor API
#   B) the request file is DATA ONLY: the helper never sources it, and
#      the Go side never puts paths into it
#   C) the privileged applier (deploy/skygate-apply-update.sh) does the
#      full safe sequence: backup → download → SHA256SUMS verify →
#      migrate BEFORE the swap → atomic rename swap (never `install`
#      onto a running binary, which is ETXTBSY) → restart → healthz
#      BUILD-STRING check → rollback from skygate.prev
#   D) install-common.sh installs the helper + the path/service units
#      and enables the path unit; the paths live in the root-owned
#      config, not in the request
#   E) the unit carries SKYGATE_INSTALL_KIND + the native state/staging
#      paths (item 3+4 of §12.15)
#   F) admin/update.go wires both the Apply and Push paths to the
#      native upgrader and no longer contains the "not yet implemented"
#      stub
#   G) the helper script is valid POSIX sh, and its request validation
#      actually rejects a hostile request (smoke run, no root needed)
#   H) go test ./internal/update/... passes
#   I) AGENTS.md + verify_pre_deploy.sh mention B261
#
# The root-only parts (real binary swap, real unit restart) cannot be
# tested unprivileged — they are covered by the live canary run recorded
# in docs/plans/2026-09-17-sqlite-pg-and-headscale-hardening.md §12.16.

set -euo pipefail
# Disable pathname expansion (grep patterns contain shell globs).
set -f

ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
fail(){ printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

if ! command -v go >/dev/null 2>&1; then
  for cand in '/mnt/c/Program Files/Go/bin' '/c/Program Files/Go/bin'; do
    if [ -f "$cand/go.exe" ] && "$cand/go.exe" version >/dev/null 2>&1; then
      export PATH="$cand:$PATH"
      break
    fi
  done
fi

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

NATIVE_GO="internal/update/native.go"
PLATFORM_GO="internal/update/platform.go"
NATIVE_TEST="internal/update/native_test.go"
ADMIN_GO="internal/feature/admin/update.go"
HELPER="deploy/skygate-apply-update.sh"
COMMON="deploy/install-common.sh"

hdr "B261 — native (systemd / bare) self-update"

# --- A: the Go API exists -------------------------------------------
if grep -q 'func NewNativeUpgrader' "$NATIVE_GO" \
   && grep -q 'func (u \*NativeUpgrader) Run' "$NATIVE_GO" \
   && grep -q 'func ConfirmNativeSwap' "$NATIVE_GO" \
   && grep -q 'func NativeReleaseTagFor' "$NATIVE_GO"; then
  ok "A: NativeUpgrader + ConfirmNativeSwap + NativeReleaseTagFor present in $NATIVE_GO"
else
  fail "A: native.go is missing part of the API (NewNativeUpgrader / Run / ConfirmNativeSwap / NativeReleaseTagFor)"
fi

# --- B: request file is data-only ------------------------------------
if grep -qE '^\s*\.\s+"\$REQ"|source\s+"\$REQ"|^\s*\.\s+\$REQ' "$HELPER"; then
  fail "B: $HELPER SOURCES request.props — the file is written by an unprivileged process, sourcing it as root is a local root escalation"
fi
if grep -q 'read_prop()' "$HELPER" && grep -q 'valid_target()' "$HELPER"; then
  ok "B: helper parses request.props as data (read_prop + strict validation), never sources it"
else
  fail "B: helper is missing read_prop/valid_target — the request must be parsed, not trusted"
fi
# The Go side must not hand paths to the privileged side.
if grep -q 'BINARY=\|SERVICE=\|HEALTH_URL=\|RUN_USER=' <(sed -n '/func (u \*NativeUpgrader) renderRequest/,/^}/p' "$NATIVE_GO"); then
  fail "B2: renderRequest puts paths into the request file — those must come from the root-owned helper config"
fi
if grep -q 'request.props' "$NATIVE_GO" && grep -q 'NativeRequestFile = "request.props"' "$NATIVE_GO"; then
  ok "B2: request file name is request.props (deliberately not .sh/.env) and carries no paths"
else
  fail "B2: request.props constant missing in native.go"
fi

# --- C: the privileged sequence --------------------------------------
grep -q 'SHA256SUMS' "$HELPER" || fail "C: helper does not verify against SHA256SUMS — an unverified binary must never be installed"
grep -q 'sha256sum' "$HELPER" || fail "C: helper does not compute sha256sum"
# C4 (live canary finding, 2026-09-18): the reference repo's published
# releases (v1.5.6 … v1.5.8) carry NO SHA256SUMS asset, so a
# SHA256SUMS-only gate made the feature dead on arrival while looking
# secure. The helper must fall back to GitHub's per-asset
# `digest: sha256:<hex>` (same trust root: same owner/repo/tag) and
# still refuse when neither is available.
grep -q 'releases/tags/' "$HELPER" || fail "C4: helper has no Releases API fallback for the asset digest"
grep -q 'sha256:\[0-9a-f\]' "$HELPER" || fail "C4: helper does not extract the GitHub asset digest"
grep -q 'no SHA256SUMS asset and no GitHub asset digest' "$HELPER" || fail "C4: helper does not fail closed when no checksum source exists"
grep -q -- '--migrate-only' "$HELPER" || fail "C: helper does not run --migrate-only before the swap"
grep -q 'atomic_install()' "$HELPER" || fail "C: helper has no atomic_install helper"
if grep -qE 'install -m 0755 -o root -g root "\$NEW_BIN" "\$BINARY_PATH"' "$HELPER"; then
  fail "C: helper installs directly onto \$BINARY_PATH — writing a running executable fails with ETXTBSY"
fi
grep -q 'mv -f "$_tmp" "$BINARY_PATH"' "$HELPER" || fail "C: helper does not swap the binary via rename"
grep -q 'systemctl restart "\$SERVICE"' "$HELPER" || fail "C: helper does not restart the unit via systemctl (MODE=systemd)"
grep -q 'build_matches()' "$HELPER" || fail "C: helper has no build_matches — a bare HTTP 200 must not count as success"
grep -q 'PREV_BINARY' "$HELPER" || fail "C: helper keeps no previous-binary backup for rollback"
grep -q 'finish rolled_back' "$HELPER" || fail "C: helper never reports rolled_back"
ok "C: applier does backup → SHA256 verify (SHA256SUMS or GitHub asset digest) → migrate → atomic swap → restart → build-string healthz → rollback"

# --- C2: bare mode restarts as the service user ----------------------
if grep -q 'setsid runuser -u "\$RUN_USER"' "$HELPER"; then
  ok "C2: bare mode restarts the process as \$RUN_USER (never as root)"
else
  fail "C2: bare mode must restart via runuser -u \$RUN_USER"
fi

# --- C3: the env file is parsed, not sourced -------------------------
if grep -qE '^\s*\.\s+"\$ENV_FILE"|source\s+"\$ENV_FILE"' "$HELPER"; then
  fail "C3: helper sources \$ENV_FILE — /etc/skygate/skygate.env is owned by the service user (local root escalation)"
fi
grep -q 'runuser -u "\$RUN_USER" -- env "\$@"' "$HELPER" || fail "C3: helper must pass the parsed env file to \`runuser ... env\`"
ok "C3: /etc/skygate/skygate.env is parsed as data and passed via env(1)"

# --- D: installer wiring ---------------------------------------------
grep -q 'write_update_helper()' "$COMMON" || fail "D: install-common.sh has no write_update_helper()"
grep -q 'skygate-update.service' "$COMMON" || fail "D: install-common.sh does not write skygate-update.service"
grep -q 'skygate-update.path' "$COMMON" || fail "D: install-common.sh does not write skygate-update.path"
grep -q 'systemctl enable --now skygate-update.path' "$COMMON" || fail "D: install-common.sh does not enable the path unit"
grep -q 'update-helper.conf' "$COMMON" || fail "D: install-common.sh does not write the root-owned helper config"
grep -q 'SKYGATE_UPDATE_BINARY=' "$COMMON" || fail "D: helper config does not pin the binary path"
for inst in deploy/install-debian.sh deploy/install-rh.sh deploy/install-bare.sh; do
  grep -q 'write_update_helper' "$inst" || fail "D: $inst does not call write_update_helper"
  grep -q 'resolve_install_kind' "$inst" || fail "D: $inst does not call resolve_install_kind"
done
grep -q 'NOPASSWD: /bin/sh \${helper}' "$COMMON" || fail "D: bare mode needs a narrowly-scoped sudoers drop-in for the applier"
ok "D: installers install the applier + path unit (systemd) / sudoers drop-in (bare)"

# --- E: unit env (items 3+4 of §12.15) -------------------------------
grep -q 'Environment=SKYGATE_INSTALL_KIND=' "$COMMON" || fail "E: unit does not pin SKYGATE_INSTALL_KIND (item 4)"
grep -q 'Environment=SKYGATE_UPDATE_STATE_PATH=' "$COMMON" || fail "E: unit does not set the native update state path (default /data/... does not exist natively)"
grep -q 'Environment=SKYGATE_UPDATE_DIR=' "$COMMON" || fail "E: unit does not set the native update staging dir"
ok "E: unit carries SKYGATE_INSTALL_KIND + native state/staging paths"

# --- F: handler wiring -----------------------------------------------
grep -q 'runNativeUpdater' "$ADMIN_GO" || fail "F: admin/update.go never calls runNativeUpdater"
if grep -q 'not yet implemented' "$ADMIN_GO"; then
  fail "F: admin/update.go still contains a 'not yet implemented' stub for native installs"
fi
COUNT=$(grep -c 'case update.InstallSystemd, update.InstallBare:' "$ADMIN_GO" || true)
if [ "$COUNT" -ge 3 ]; then
  ok "F: all native entry points wired (apply/push/rollback: $COUNT switches)"
else
  fail "F: expected the native case in apply + push + rollback switches, found $COUNT"
fi
grep -q 'update.ConfirmNativeSwap' "$ADMIN_GO" || fail "F: renderUpdatePage does not fold the helper verdict"
grep -q '"Platform":' "$ADMIN_GO" || fail "F: the page does not render the platform info (item 3)"
grep -q 'func DetectPlatform' "$PLATFORM_GO" || fail "F: platform.go has no DetectPlatform"
ok "F: apply/push/rollback wired + verdict folding + platform rendering"

# --- G: syntax + hostile-request smoke --------------------------------
if command -v sh >/dev/null 2>&1; then
  sh -n "$HELPER" || fail "G: $HELPER is not valid POSIX sh"
  ok "G: $HELPER passes sh -n"
else
  ok "G: skipped sh -n (no sh on PATH)"
fi

if command -v sh >/dev/null 2>&1; then
  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT
  cat > "$TMP/conf" <<EOF
SKYGATE_UPDATE_DIR="$TMP/upd"
SKYGATE_UPDATE_MODE="systemd"
SKYGATE_UPDATE_SERVICE="skygate"
SKYGATE_UPDATE_BINARY="$TMP/skygate"
SKYGATE_UPDATE_RUN_USER="nobody"
SKYGATE_UPDATE_ENV_FILE="$TMP/env"
SKYGATE_UPDATE_HEALTH_URL="http://127.0.0.1:1/healthz"
EOF
  mkdir -p "$TMP/upd"
  # A hostile target must be refused BEFORE anything else happens.
  printf "JOB_ID='deadbeef'\nTARGET='v1.5.9'; rm -rf /\nFROM_VERSION='v1.5.8'\n" > "$TMP/upd/request.props"
  SKYGATE_HELPER_CONF="$TMP/conf" sh "$HELPER" >/dev/null 2>&1 || true
  if [ -f "$TMP/upd/result.status" ] && grep -q '^failed$' "$TMP/upd/result.status"; then
    if grep -q 'refusing request' "$TMP/upd/apply.log"; then
      ok "G2: hostile TARGET is refused as data (verdict=failed, no shell execution)"
    else
      fail "G2: request was rejected but apply.log does not show the validation path"
    fi
  else
    fail "G2: hostile TARGET did not produce a failed verdict (helper may have executed the request)"
  fi
  # A missing request must be a no-op, not a failure loop.
  rm -f "$TMP/upd/request.props" "$TMP/upd/result.status"
  if SKYGATE_HELPER_CONF="$TMP/conf" sh "$HELPER" >/dev/null 2>&1; then
    fail "G3: helper should exit non-zero (3) when there is no request"
  else
    rc=$?
    if [ "$rc" -eq 3 ]; then
      ok "G3: no request → exit 3 (path unit re-arms without a failed unit)"
    else
      fail "G3: expected exit 3 for a missing request, got $rc"
    fi
  fi
fi

# --- H: Go tests ------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  go test ./internal/update/... -count=1 >/dev/null 2>&1 || fail "H: go test ./internal/update/... failed"
  go test ./internal/db/ -run 'TestOpenSQLite|TestConfigDSNFormsOpen' -count=1 >/dev/null 2>&1 \
    || fail "H2: the SQLite DSN open regression tests failed"
  ok "H: go test ./internal/update/... + the SQLite DSN open tests pass"
else
  ok "H: skipped (no go on PATH)"
fi

# --- I: trackers ------------------------------------------------------
grep -q 'B261' AGENTS.md || fail "I: AGENTS.md does not mention B261"
grep -q 'check_b261' scripts/verify_pre_deploy.sh || fail "I: verify_pre_deploy.sh does not run check_b261"
ok "I: AGENTS.md + verify_pre_deploy.sh track B261"

# --- J: the installer's SQLite DSN form actually opens (B261.1) --------
# Live canary finding: deploy/install-*.sh write
# SKYGATE_DB=sqlite:/path/to/skygate.db, and openSQLite passed the
# "sqlite:" scheme straight to modernc.org/sqlite, which does not know
# it — SQLite tried a relative filename containing a colon, failed with
# SQLITE_CANTOPEN, and the driver reported "unable to open database
# file: out of memory (14)". A native install could therefore never
# start (5 retries over ~40s, then log.Fatalf). Every SQLite test used
# ":memory:", so the suite stayed green.
if grep -q 'HasPrefix(strings.ToLower(dsn), "sqlite:")' internal/db/open_sqlite.go; then
  ok "J: openSQLite strips the 'sqlite:' scheme prefix (the installer DSN form opens)"
else
  fail "J: openSQLite no longer strips 'sqlite:' — native SQLite installs will fail with a misleading CANTOPEN/OOM error"
fi
if [ -f internal/db/open_sqlite_b261_test.go ] \
   && grep -q 'TestOpenSQLiteInstallerDSNForms' internal/db/open_sqlite_b261_test.go; then
  ok "J2: a real-open regression test covers the installer DSN forms"
else
  fail "J2: internal/db/open_sqlite_b261_test.go is missing the installer-DSN open test"
fi

hdr "B261: all contracts pass"
