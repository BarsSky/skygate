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
#   J..N) the later B261.x / B262 follow-ups (the installer's `sqlite:` DSN
#      must really open, OpenRC wiring, the published SHA256SUMS asset, the
#      SQLite DDL chokepoint, the minimal-host secret fallback)
#   O) B263 — the single-file release-notes pipeline: release.yml extracts the
#      "## vX.Y.Z" section of RELEASE-NOTES.md, always sets notes_path, and
#      falls back to a commit list (v1.5.8 shipped an EMPTY release body)
#
# The root-only parts (real binary swap, real unit restart) cannot be
# tested unprivileged — they are covered by the live canary run recorded
# in docs/ROADMAP.md §12.16.

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

# --- C5: the migrate step uses the SUBCOMMAND -------------------------
# Live canary finding 3: cmd/skygate dispatches `migrate-only` as a
# subcommand (`case "migrate-only":`), NOT as a --migrate-only flag —
# that prints `unknown command "--migrate-only"`. The applier and the
# operator-facing manual steps (internal/update/manual.go) both used the
# flag form, so the migration step failed on every attempt.
grep -q '"\$NEW_BIN" migrate-only' "$HELPER" || fail "C5: applier does not call the migrate-only subcommand"
if grep -qE '"\$NEW_BIN" --migrate-only|skygate --migrate-only|skygate-skygate:latest \\' "$HELPER" >/dev/null 2>&1; then
  fail "C5: applier still uses the nonexistent --migrate-only flag"
fi
grep -q '+ " migrate-only"' internal/update/manual.go || fail "C5: bare manual steps still use --migrate-only"
grep -q '/app/skygate migrate-only' internal/update/manual.go || fail "C5: docker manual steps still use --migrate-only"
ok "C5: migrate step (applier + manual steps) uses the migrate-only subcommand"

# --- C6: mirror base for air-gapped installs --------------------------
# SKYGATE_UPDATE_BASE_URL is root-owned config (a compromised skygate
# still cannot choose where code comes from). A custom base must be
# https:// (loopback http allowed for a same-host mirror) and MUST ship
# SHA256SUMS, since GitHub's digest only describes the official asset.
grep -q 'SKYGATE_UPDATE_BASE_URL' "$HELPER" || fail "C6: applier has no mirror base override"
grep -q 'MIRROR_MODE=1' "$HELPER" || fail "C6: applier does not track mirror mode"
grep -q 'so the mirror MUST publish SHA256SUMS' "$HELPER" || fail "C6: mirror mode must fail closed without SHA256SUMS"
grep -q 'plain http:// is allowed for loopback mirrors only' "$HELPER" || fail "C6: mirror base must be https:// (loopback http only)"
ok "C6: mirror base is root-owned, https-only (loopback exempt) and always checksum-verified"

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
for inst in deploy/install-debian.sh deploy/install-rh.sh deploy/install-bare.sh deploy/install-alpine.sh; do
  grep -q 'write_update_helper' "$inst" || fail "D: $inst does not call write_update_helper"
  grep -q 'resolve_install_kind' "$inst" || fail "D: $inst does not call resolve_install_kind"
done
grep -q 'NOPASSWD: /bin/sh \${helper}' "$COMMON" || fail "D: bare mode needs a narrowly-scoped sudoers drop-in for the applier"
ok "D: installers install the applier + path unit (systemd) / sudoers drop-in (bare, openrc)"

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
COUNT=$(grep -c 'case update.InstallSystemd, update.InstallOpenRC, update.InstallBare:' "$ADMIN_GO" || true)
if [ "$COUNT" -ge 3 ]; then
  ok "F: all native entry points wired (apply/push/rollback: $COUNT switches, incl. openrc)"
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

# --- K: OpenRC / Alpine (B262) ----------------------------------------
# Pre-B262 InstallKind had no OpenRC at all: an Alpine host (a first-class
# installer, deploy/install-alpine.sh) detected as InstallUnknown, so
# /admin/update refused the update with "could not detect install kind", and
# the applier had no way to restart an OpenRC service.
if grep -q 'InstallOpenRC' internal/update/install.go \
   && grep -q '"/run/openrc"' internal/update/install.go \
   && grep -q 'case "openrc", "rc-service", "alpine":' internal/update/install.go; then
  ok "K: InstallOpenRC kind exists with the /run/openrc marker + env override"
else
  fail "K: InstallKind has no OpenRC support (Alpine hosts detect as unknown and cannot update)"
fi
grep -q 'func GenerateOpenRCSteps' internal/update/manual.go || fail "K2: no OpenRC manual steps"
grep -q 'case InstallOpenRC:' internal/update/manual.go || fail "K2: OpenRC steps are not dispatched from GenerateManualSteps"
grep -q 'rc-service "\$SERVICE" restart' "$HELPER" || fail "K2: applier cannot restart an OpenRC service (MODE=openrc)"
grep -q 'systemd|openrc|bare)' "$HELPER" || fail "K2: applier does not accept MODE=openrc"
grep -q 'SKYGATE_INSTALL_KIND="openrc"' deploy/install-alpine.sh || fail "K2: install-alpine.sh does not pin SKYGATE_INSTALL_KIND=openrc"
grep -q 'SKYGATE_UPDATE_MODE' "$COMMON" || fail "K2: helper config does not carry the restart mode"
ok "K2: OpenRC wired end-to-end (kind → steps → rc-service restart → alpine installer)"

# --- L: the release actually publishes SHA256SUMS (B262) --------------
# v1.5.6 … v1.5.8 shipped NO SHA256SUMS asset: release.yml downloaded the
# SHA256SUMS artifact into dist/SHA256SUMS (a DIRECTORY named like the file
# inside it) and the Flatten step then moved the file into that directory —
# `mv file dir/` is a no-op — so the release attached a directory. The
# applier's digest fallback masked it, but a mirror cannot use it.
if grep -q 'path: dist/checksums' .github/workflows/release.yml; then
  ok "L: release.yml downloads SHA256SUMS into dist/checksums (so flatten yields a FILE)"
else
  fail "L: release.yml still downloads SHA256SUMS into dist/SHA256SUMS — the next release will attach a directory again"
fi
if command -v find >/dev/null 2>&1 && command -v mktemp >/dev/null 2>&1; then
  SIM="$(mktemp -d)"
  mkdir -p "$SIM/dist/skygate-linux-amd64" "$SIM/dist/checksums"
  : > "$SIM/dist/skygate-linux-amd64/skygate-vX-linux-amd64.tar.gz"
  : > "$SIM/dist/checksums/SHA256SUMS"
  ( cd "$SIM/dist" && find . -mindepth 2 -type f -exec mv '{}' . \; >/dev/null 2>&1; find . -mindepth 1 -type d -empty -delete >/dev/null 2>&1 )
  if [ -f "$SIM/dist/SHA256SUMS" ]; then
    ok "L2: the workflow's own flatten step produces dist/SHA256SUMS as a file"
  else
    fail "L2: flatten step does not produce a dist/SHA256SUMS file (simulation in $SIM)"
  fi
  rm -rf "$SIM"
else
  ok "L2: skipped flatten simulation (no find/mktemp)"
fi

# --- M: the SQLite DDL loops no longer swallow errors (§12.13 follow-up) ---
# migrations_sqlite.go is a script-generated reverse-port of the PostgreSQL
# chain, and nine of its ~18 DDL loops ran statements through
#   if _, err := d.Exec(q); err != nil { continue }
#   _, _ = d.Exec(q)
# so an `ADD COLUMN IF NOT EXISTS` (a SYNTAX ERROR in SQLite, copied verbatim
# from PG) was read as "the column already exists" — exit_servers.{ssh_target,
# ssh_key_path,accept_routes} and device_rules.device_ip silently never
# appeared on a fresh database. Every migration now runs its statements
# through execSQLiteDDL, which is idempotent for ADD COLUMN (PRAGMA
# table_info first) and RETURNS errors for everything else.
if grep -q 'func execSQLiteDDL' internal/db/sqlite_ddl.go \
   && [ -f internal/db/sqlite_ddl_exec_test.go ]; then
  ok "M: execSQLiteDDL helper + its idempotency tests exist"
else
  fail "M: execSQLiteDDL (or internal/db/sqlite_ddl_exec_test.go) missing — the SQLite chain can swallow errors again"
fi
if grep -qE 'for _, [a-z]+ := range (stmts|queries|query)' internal/db/migrations_sqlite.go; then
  fail "M2: migrations_sqlite.go still has ad-hoc DDL loops — route them through execSQLiteDDL"
else
  ok "M2: no ad-hoc DDL loops left in migrations_sqlite.go"
fi
if grep -nE '^[[:space:]]*_, _ = d\.Exec\(' internal/db/migrations_sqlite.go >/dev/null 2>&1; then
  fail "M3: migrations_sqlite.go still has an unconditional error-ignoring Exec"
else
  ok "M3: no unconditional error-ignoring Exec left in migrations_sqlite.go"
fi

# --- N: the installer survives a MINIMAL host (pre-release acceptance) ---
# `xxd` ships in the xxd/vim-common package, which is in NONE of the
# installer dependency lists (apt/dnf/apk). On a clean debian:12 container
# write_env_file died at the secret-generation line, and because the
# installers run under `set -euo pipefail` the install ABORTED after
# "installed: /usr/local/bin/skygate" — leaving a binary, no env file, no
# systemd unit and no update helper. The secret must come from a tool the
# distro actually guarantees (openssl if present, `od` from coreutils
# otherwise).
if grep -q 'command -v od' deploy/install-common.sh \
   && grep -q 'command -v openssl' deploy/install-common.sh; then
  ok "N: secret generation has od/openssl fallbacks (xxd is not guaranteed on minimal hosts)"
else
  fail "N: install-common.sh still relies on xxd alone — a minimal Debian/RHEL install aborts after the binary is installed"
fi
# N2 pins the ORDER: openssl first (best), then od (coreutils, guaranteed),
# and only then xxd as a last resort. A bare/unguarded xxd pipeline would sit
# before the od fallback, so the ordering check catches it without trying to
# parse shell control flow.
n_openssl=$(grep -n 'command -v openssl' deploy/install-common.sh | head -1 | cut -d: -f1)
n_od=$(grep -n 'command -v od' deploy/install-common.sh | head -1 | cut -d: -f1)
n_xxd=$(grep -n 'xxd -p' deploy/install-common.sh | grep -vE '^[0-9]+:[[:space:]]*#' | head -1 | cut -d: -f1)
if [ -n "$n_openssl" ] && [ -n "$n_od" ] && [ -n "$n_xxd" ] \
   && [ "$n_openssl" -lt "$n_od" ] && [ "$n_od" -lt "$n_xxd" ]; then
  ok "N2: secret fallback order is openssl → od (coreutils) → xxd"
else
  fail "N2: secret generation must try openssl, then od (coreutils), and only then xxd"
fi

# --- O: ONE canonical release-notes file (B263) ------------------------
# v1.5.8 was published with an EMPTY GitHub release body: release.yml read
# RELEASE-NOTES-v${VERSION}.md (a per-version file that no longer existed) and
# assigned an empty body_path, while generate_release_notes was never set — so
# the release carried no notes at all. The notes now live in ONE file
# (RELEASE-NOTES.md, newest section first); the workflow extracts the
# "## vX.Y.Z" section and falls back to a generated commit list, so the body
# can never be empty again.
REL=.github/workflows/release.yml
grep -q 'NOTES_FILE="RELEASE-NOTES.md"' "$REL" \
  || fail "O: release.yml does not read the single RELEASE-NOTES.md"
ok "O: release.yml reads RELEASE-NOTES.md (one canonical file)"
if grep -q 'RELEASE-NOTES-v\${VERSION}\.md' "$REL"; then
  fail "O2: release.yml still looks for the deprecated per-version note file"
fi
ok "O2: no per-version RELEASE-NOTES-vX.Y.Z.md lookup left in release.yml"
grep -q 'notes_path=\$OUT' "$REL" \
  || fail "O3: release.yml does not always set notes_path (an empty body_path publishes no notes)"
grep -q 'falling back to the commit list' "$REL" \
  || fail "O3: release.yml has no non-empty fallback for a missing section"
ok "O3: notes_path always points at a file; a missing section falls back to the commit list"

# O4: run the workflow's OWN awk extraction against the real file. This is the
# contract that would have caught the v1.5.8 empty body.
NEWEST="$(grep -m1 -E '^## v[0-9]' RELEASE-NOTES.md | sed -E 's/^## (v[0-9][^ ]*).*/\1/')"
[ -n "$NEWEST" ] || fail "O4: RELEASE-NOTES.md has no '## vX.Y.Z' section heading"
EXTRACT="$(mktemp)"
awk -v ver="$NEWEST" '
  !inside && $0 ~ ("^## " ver "([^0-9.]|$)") { inside = 1 }
  inside && $0 ~ "^## " && $0 !~ ("^## " ver "([^0-9.]|$)") { exit }
  inside { print }
' RELEASE-NOTES.md > "$EXTRACT"
if [ -s "$EXTRACT" ] && head -1 "$EXTRACT" | grep -q "^## ${NEWEST}"; then
  ok "O4: the workflow's extraction yields $(wc -l < "$EXTRACT") non-empty lines for the newest section (${NEWEST})"
else
  fail "O4: the workflow's awk extraction produced no section for ${NEWEST} — that release body would be empty"
fi
HEADINGS=$(grep -c '^## ' "$EXTRACT" || true)
if [ "${HEADINGS:-0}" = "1" ]; then
  ok "O4b: extraction stops at the next '## ' heading (exactly 1 heading in the block)"
else
  fail "O4b: the extracted block contains ${HEADINGS} '## ' headings — it did not stop at the next section"
fi
rm -f "$EXTRACT"

# O5: version-exact matching. A naive '^## v1.5.9' pattern also matches a
# '## v1.5.90' heading, and an UNKNOWN version must yield nothing at all — that
# empty result is exactly what routes the workflow into its fallback branch.
BOUND="$(mktemp)"
printf '## v1.5.90 — synthetic boundary probe\n\nmust not be extracted for v1.5.9\n' > "$BOUND"
if awk -v ver="v1.5.9" '
  !inside && $0 ~ ("^## " ver "([^0-9.]|$)") { inside = 1 }
  inside && $0 ~ "^## " && $0 !~ ("^## " ver "([^0-9.]|$)") { exit }
  inside { print }
' "$BOUND" | grep -q 'must not be extracted'; then
  fail "O5: '## v1.5.9' also matches '## v1.5.90' — the version-boundary guard is broken"
fi
MISSING="$(awk -v ver="v9.9.999" '
  !inside && $0 ~ ("^## " ver "([^0-9.]|$)") { inside = 1 }
  inside && $0 ~ "^## " && $0 !~ ("^## " ver "([^0-9.]|$)") { exit }
  inside { print }
' RELEASE-NOTES.md)"
[ -z "$MISSING" ] || fail "O5: an unknown version extracted content instead of nothing"
rm -f "$BOUND"
ok "O5: version-exact matching; an unknown version extracts nothing (→ workflow fallback)"

# O6: the release cycle being prepared has its own section in the file.
grep -qE '^## v1\.5\.9 ' RELEASE-NOTES.md \
  || fail "O6: RELEASE-NOTES.md has no '## v1.5.9' section for this release cycle"
ok "O6: the current release cycle (v1.5.9) has a section in RELEASE-NOTES.md"

# O7: the operator-facing procedure and the repo root follow the single-file
# rule. `git ls-files` (not a shell glob) because this script runs under `set -f`.
if grep -q 'RELEASE-NOTES-vX\.Y\.Z\.md' docs/operations.md; then
  fail "O7: docs/operations.md still instructs operators to write RELEASE-NOTES-vX.Y.Z.md"
fi
if [ -n "$(git ls-files 'RELEASE-NOTES-v*.md' 2>/dev/null)" ]; then
  fail "O7: per-version RELEASE-NOTES-v*.md files are tracked at the repo root"
fi
ok "O7: docs/operations.md + the repo root follow the single-file rule"

# O8: the release job must actually HAVE the file. The real reason v1.5.8's body
# was empty is that the `release` job had no `actions/checkout` step at all — the
# file could not be found no matter how it was named.
CHECKS=$(grep -c 'uses: actions/checkout@' "$REL" || true)
if [ "${CHECKS:-0}" -ge 3 ]; then
  ok "O8: every release job that needs the tree checks it out (CHECKS=$CHECKS)"
else
  fail "O8: only ${CHECKS:-0} checkout step(s) in release.yml — the release job needs its own, or RELEASE-NOTES.md is absent and the body comes out empty"
fi
grep -q 'sparse-checkout' "$REL" \
  || fail "O8: the release job's checkout is not sparse (it would fetch the whole tree for one file)"
ok "O8: the release job checks RELEASE-NOTES.md out sparsely"

hdr "B261: all contracts pass"