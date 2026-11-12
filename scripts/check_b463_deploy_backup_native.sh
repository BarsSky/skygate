#!/usr/bin/env bash
# ============================================================================
# check_b463_deploy_backup_native.sh — B463
#
# THE *DOCUMENTED* BACKUP SCRIPT MUST NOT BE NATIVE-BLIND EITHER.
#
# B462 fixed scripts/backup.sh + scripts/restore.sh — the ROUTINE path (cron at
# 03:00 → scripts/backup_cron.sh → `skygate backup-run` → internal/backup.RunBackup
# → scripts/backup.sh). A SECOND script, deploy/backup.sh, is what docs/deploy.md
# §Backup, docs/derp.md and docs/disaster-recovery.md ("Backed up by
# deploy/backup.sh (cron daily at 03:00)") tell the operator to run — and it was
# left native-blind by B459/B462:
#
#   * deploy/lib/env.sh did not know /etc/skygate/skygate.env, so on a native host
#     SKYGATE_DB fell back to the CONTAINER default /data/skygate.db;
#   * worse, env.sh REQUIRES the docker stack's HEADPLANE_*/HEADSCALE_SERVER_URL and
#     exits 1 without them — a valid native env file has none, so the script died
#     before doing any work;
#   * the env copy archived ${PROJECT_DIR}/.env, a file the service never reads;
#   * headscale's database was docker-volume-only and its config came only from the
#     docker bind dir — so headscale's state was silently absent from the archive.
#
# Contracts:
#   A. lib/env.sh knows the native env file, records which one it loaded, and keeps
#      the docker-era validation ON by default (the opt-out is explicit)
#   B. deploy/backup.sh opts out, copies the file that was LOADED, and captures
#      /etc/skygate
#   C. deploy/backup.sh prefers the native headscale config and database, and names
#      why when neither exists
#   D. headscale_db_path is byte-identical in ALL THREE scripts (no drift)
#   E. BEHAVIOURAL: the resolver extracted from deploy/backup.sh behaves like the
#      other two (override, block form, inline form, PostgreSQL → nothing)
#   F. BEHAVIOURAL: env.sh still REFUSES an incomplete env file unless the opt-out
#      is set — the relaxation must not silently disable validation
#   G. the detector can fail on the pre-B463 shape
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b463.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

ENVLIB=deploy/lib/env.sh
DBACKUP=deploy/backup.sh
SBACKUP=scripts/backup.sh
SRESTORE=scripts/restore.sh
PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }

for f in "$ENVLIB" "$DBACKUP" "$SBACKUP" "$SRESTORE"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

echo "=== B463: the documented backup script speaks native too ==="

# --- A: lib/env.sh ---
if has "$ENVLIB" '/etc/skygate/skygate.env'; then
  ok "A1: lib/env.sh knows the native env file"
else
  bad "A1: lib/env.sh still only knows the checkout"
fi
if has "$ENVLIB" 'SKYGATE_ENV_FILE_USED="${_CANDIDATE}"'; then
  ok "A2: lib/env.sh records WHICH file it loaded"
else
  bad "A2: lib/env.sh does not record the loaded file"
fi
if has "$ENVLIB" 'SKYGATE_ENV_SKIP_REQUIRED'; then
  ok "A3: the docker-era validation has an explicit opt-out"
else
  bad "A3: there is no opt-out, so a native env file dies at the required-var check"
fi

# --- B: deploy/backup.sh ---
if has "$DBACKUP" 'SKYGATE_ENV_SKIP_REQUIRED=1'; then
  ok "B1: deploy/backup.sh opts out of the docker-stack validation"
else
  bad "B1: deploy/backup.sh would abort on a native env file"
fi
if has "$DBACKUP" 'ENV_SRC="${SKYGATE_ENV_FILE_USED:-${PROJECT_DIR}/.env}"'; then
  ok "B2: the env copy uses the file that was actually loaded"
else
  bad "B2: the env copy still archives the checkout .env"
fi
if has "$DBACKUP" 'for _f in cluster-state.json dbs.env'; then
  ok "B3: deploy/backup.sh captures /etc/skygate"
else
  bad "B3: deploy/backup.sh does not capture /etc/skygate"
fi

# --- C: headscale ---
if has "$DBACKUP" 'headscale_db_path()'; then
  ok "C1: deploy/backup.sh has the native headscale resolver"
else
  bad "C1: deploy/backup.sh has no native headscale resolver"
fi
if has "$DBACKUP" 'HS_DB_NATIVE="$(headscale_db_path'; then
  ok "C2: the native headscale database is preferred over the docker volume"
else
  bad "C2: the headscale database is still docker-volume-only"
fi
if has "$DBACKUP" 'for _d in /etc/headscale "${DEPLOY_HEADSCALE_DIR}/config"'; then
  ok "C3: the headscale config is captured from the native dir too"
else
  bad "C3: the headscale config is still docker-bind-only"
fi
if has "$DBACKUP" 'If headscale runs on PostgreSQL there is no file to copy'; then
  ok "C4: an uncaptured headscale database is named"
else
  bad "C4: a missing headscale database has no named reason"
fi

# --- D: no drift between the three copies ---
extract_fn() {
  awk -v name="$1" '
    index($0, name "() {") == 1 { on = 1 }
    on { print }
    on && /^}$/ { exit }
  ' "$2"
}
extract_fn headscale_db_path "$SBACKUP"  > "$SKY_TMP/hs_scripts_backup.sh"
extract_fn headscale_db_path "$SRESTORE" > "$SKY_TMP/hs_scripts_restore.sh"
extract_fn headscale_db_path "$DBACKUP"  > "$SKY_TMP/hs_deploy_backup.sh"
if [ -s "$SKY_TMP/hs_deploy_backup.sh" ] \
   && diff -q "$SKY_TMP/hs_scripts_backup.sh" "$SKY_TMP/hs_scripts_restore.sh" >/dev/null 2>&1 \
   && diff -q "$SKY_TMP/hs_scripts_backup.sh" "$SKY_TMP/hs_deploy_backup.sh"  >/dev/null 2>&1; then
  ok "D1: headscale_db_path is byte-identical in scripts/backup.sh, scripts/restore.sh AND deploy/backup.sh"
else
  bad "D1: headscale_db_path DRIFTED between the three scripts"
fi

# --- E: BEHAVIOURAL — the deploy/backup.sh copy behaves ---
{
  echo 'log()  { printf "[ok] %s\n" "$1"; }'
  echo 'warn() { printf "[warn] %s\n" "$1"; }'
  echo 'die()  { printf "[err] %s\n" "$1"; exit 1; }'
  echo 'SKYGATE_DB="${SKYGATE_DB:-}"'
  echo 'PROJECT_DIR="${PROJECT_DIR:-}"'
  extract_fn sqlite_db_path "$DBACKUP"
  extract_fn headscale_db_path "$DBACKUP"
} > "$SKY_TMP/funcs.sh"
if [ -s "$SKY_TMP/funcs.sh" ] && bash -n "$SKY_TMP/funcs.sh" 2>/dev/null; then
  ok "E0: the deploy/backup.sh resolvers extract cleanly and parse"
else
  bad "E0: the deploy/backup.sh resolvers could not be extracted"
fi

mkdir -p "$SKY_TMP/e1"
printf 'x' > "$SKY_TMP/e1/db.sqlite"
got=$(SKYGATE_HEADSCALE_DB_PATH="$SKY_TMP/e1/db.sqlite" SKYGATE_HEADSCALE_CONFIG="" \
      bash -c '. "$1/funcs.sh"; headscale_db_path' _ "$SKY_TMP" 2>&1)
[ "$got" = "$SKY_TMP/e1/db.sqlite" ] && ok "E1: the explicit override is honoured" \
  || bad "E1: the explicit override is ignored (got '${got}')"

mkdir -p "$SKY_TMP/e2/data"; printf 'x' > "$SKY_TMP/e2/data/db.sqlite"
cat > "$SKY_TMP/e2/config.yaml" <<EOF
database:
  type: sqlite
  sqlite:
    path: $SKY_TMP/e2/data/db.sqlite
noise:
  private_key_path: /etc/headscale/noise_private.key
EOF
got=$(SKYGATE_HEADSCALE_DB_PATH="" SKYGATE_HEADSCALE_CONFIG="$SKY_TMP/e2/config.yaml" \
      bash -c '. "$1/funcs.sh"; headscale_db_path' _ "$SKY_TMP" 2>&1)
[ "$got" = "$SKY_TMP/e2/data/db.sqlite" ] && ok "E2: the block form is parsed" \
  || bad "E2: block-form parsing failed (got '${got}')"

mkdir -p "$SKY_TMP/e3"; printf 'x' > "$SKY_TMP/e3/db.sqlite"
printf 'database:\n  type: sqlite\n  sqlite: %s\n' "$SKY_TMP/e3/db.sqlite" > "$SKY_TMP/e3/config.yaml"
got=$(SKYGATE_HEADSCALE_DB_PATH="" SKYGATE_HEADSCALE_CONFIG="$SKY_TMP/e3/config.yaml" \
      bash -c '. "$1/funcs.sh"; headscale_db_path' _ "$SKY_TMP" 2>&1)
[ "$got" = "$SKY_TMP/e3/db.sqlite" ] && ok "E3: the inline form is parsed" \
  || bad "E3: inline-form parsing failed (got '${got}')"

mkdir -p "$SKY_TMP/e4"; printf 'x' > "$SKY_TMP/e4/key"
printf 'noise:\n  private_key_path: %s\ndatabase:\n  type: postgres\n' "$SKY_TMP/e4/key" > "$SKY_TMP/e4/config.yaml"
got=$(SKYGATE_HEADSCALE_DB_PATH="" SKYGATE_HEADSCALE_CONFIG="$SKY_TMP/e4/config.yaml" \
      bash -c '. "$1/funcs.sh"; headscale_db_path' _ "$SKY_TMP" 2>&1)
[ -z "$got" ] && ok "E4: a PostgreSQL headscale config yields NO path (no false hit on private_key_path)" \
  || bad "E4: the resolver invented '${got}'"

# --- F: BEHAVIOURAL — env.sh must still validate by default ---
# The path has to be absolute BEFORE the `cd` below: `$PWD` inside the subshell is
# the temp dir, and using it silently sourced nothing and reported rc=0 (the first
# version of this leg did exactly that — the check has to be able to fail).
REPO_ABS="$(pwd)"
ENVLIB_ABS="${REPO_ABS}/${ENVLIB}"
mkdir -p "$SKY_TMP/f"
printf 'SKYGATE_DB=sqlite:/srv/f/skygate.db\n' > "$SKY_TMP/f/native.env"

out=$( cd "$SKY_TMP/f" && SKYGATE_ENV="$SKY_TMP/f/native.env" DEPLOY_SKYGATE_DIR="$SKY_TMP/f" \
        bash -c "source \"${ENVLIB_ABS}\"; echo LOADED-\$SKYGATE_DB" 2>&1 ); rc=$?
if [ "$rc" -ne 0 ] && [[ "$out" == *"Required variables missing"* ]]; then
  ok "F1: an incomplete env file is STILL refused by default (validation not disabled)"
else
  bad "F1: env.sh no longer validates by default (rc=$rc; out: $(printf '%s' "$out" | tr '\n' ' '))"
fi

out=$( cd "$SKY_TMP/f" && SKYGATE_ENV="$SKY_TMP/f/native.env" DEPLOY_SKYGATE_DIR="$SKY_TMP/f" \
        SKYGATE_ENV_SKIP_REQUIRED=1 \
        bash -c "source \"${ENVLIB_ABS}\"; echo LOADED-\$SKYGATE_DB-\$SKYGATE_ENV_FILE_USED" 2>&1 ); rc=$?
if [ "$rc" -eq 0 ] && [[ "$out" == *'LOADED-sqlite:/srv/f/skygate.db-'* ]]; then
  ok "F2: with the opt-out a native env file loads and SKYGATE_DB comes from it"
else
  bad "F2: the opt-out does not load the native env file (rc=$rc; out: $(printf '%s' "$out" | tr '\n' ' '))"
fi

# --- G: the detector must fail on the pre-B463 shape ---
cat > "$SKY_TMP/old_env.sh" <<'EOF'
_LOADED=false
for _CANDIDATE in "${SKYGATE_ENV:-}" "${DEPLOY_SKYGATE_DIR:-/home/admin/skygate}/.env" .env; do
    if [ -f "${_CANDIDATE}" ]; then set -a; source "${_CANDIDATE}"; set +a; _LOADED=true; break; fi
done
_required_vars=(SKYGATE_JWT_SECRET HEADPLANE_SERVER__COOKIE_SECRET)
EOF
cat > "$SKY_TMP/old_deploy_backup.sh" <<'EOF'
log "Backing up .env..."
[ -f "${PROJECT_DIR}/.env" ] && cp "${PROJECT_DIR}/.env" "${BACKUP_PATH}/.env"
log "Backing up Headscale database..."
${DOCKER_CMD} run --rm -v headscale_headscale_data:/data alpine sh -c "cp /data/db.sqlite /backup/x"
if [ -d "${DEPLOY_HEADSCALE_DIR}/config" ]; then cp -r "${DEPLOY_HEADSCALE_DIR}/config/"* x; fi
EOF
discriminates=1
for _pair in "$ENVLIB:/etc/skygate/skygate.env" "$ENVLIB:SKYGATE_ENV_FILE_USED" \
             "$DBACKUP:SKYGATE_ENV_SKIP_REQUIRED=1" "$DBACKUP:headscale_db_path" \
             "$DBACKUP:for _d in /etc/headscale" "$DBACKUP:for _f in cluster-state.json dbs.env"; do
  _file="${_pair%%:*}"; _marker="${_pair#*:}"
  has "$_file" "$_marker" || discriminates=0
done
for _marker in '/etc/skygate/skygate.env' 'SKYGATE_ENV_FILE_USED' 'headscale_db_path' \
               'SKYGATE_ENV_SKIP_REQUIRED' 'for _d in /etc/headscale' 'cluster-state.json'; do
  has "$SKY_TMP/old_env.sh" "$_marker" && discriminates=0
  has "$SKY_TMP/old_deploy_backup.sh" "$_marker" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "G1: every predicate is present in the fixed files and absent from the pre-B463 shape"
else
  bad "G1: the predicates do not discriminate the pre-B463 native-blind shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB463: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB463: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
