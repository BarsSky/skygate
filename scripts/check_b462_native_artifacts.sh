#!/usr/bin/env bash
# ============================================================================
# check_b462_native_artifacts.sh — B462
#
# NOTHING LOST ON A NATIVE (systemd/OpenRC) INSTALL.
#
# Measured 2026-10-29 by comparing what the native installer CREATES against what
# scripts/backup.sh CAPTURES and scripts/restore.sh PUTS BACK:
#
#   artefact                     native location                     backup      restore
#   ---------------------------  ----------------------------------  ----------  ----------
#   config (DSN + secrets)       /etc/skygate/skygate.env            MISSED      MISSED
#   skygate database             /var/lib/skygate/skygate.db         B459 ok     B461 ok
#   headscale config             /etc/headscale                      MISSED      MISSED
#   headscale database           /var/lib/headscale/db.sqlite        MISSED      MISSED
#   cluster-state.json, dbs.env  /etc/skygate                        MISSED      MISSED
#
# Four of the five were lost: the archive carried the database but no DSN and no
# secrets (a restore that cannot start), and both headscale artefacts were
# docker-only steps that a native host skips. Worse, the env step "succeeded"
# silently on the checkout's .env while the service reads /etc/skygate/skygate.env.
#
# Contracts:
#   A. backup resolves the env file the SERVICE reads, and uses it for BOTH the
#      copy and the DB/DSN lookups
#   B. backup captures /etc/headscale and the native headscale database
#   C. backup captures /etc/skygate (cluster-state.json, dbs.env)
#   D. restore writes the env and both headscale artefacts to their NATIVE targets
#   E. the headscale resolver is byte-identical in both scripts (no drift)
#   F. BEHAVIOURAL: the resolvers are extracted and run against real files
#   G. the detector can fail on the pre-B462 shape
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b462.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

BACKUP=scripts/backup.sh
RESTORE=scripts/restore.sh
PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }

for f in "$BACKUP" "$RESTORE"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

echo "=== B462: nothing lost on a native install ==="

# --- A: the env file the service reads ---
if has "$BACKUP" 'skygate_env_file()'; then
  ok "A1: backup has skygate_env_file()"
else
  bad "A1: backup has no env-file resolver (it would only see the checkout .env)"
fi
for probe in 'SKYGATE_UPDATE_ENV_FILE' '/etc/skygate/skygate.env' '${SKYGATE_DIR}/.env'; do
  if has "$BACKUP" "$probe"; then
    ok "A2: the resolver consults $probe"
  else
    bad "A2: the resolver does not consult $probe"
  fi
done
if has "$BACKUP" 'SKYGATE_ENV_SOURCE="$(skygate_env_file'; then
  ok "A3: the env COPY uses the resolver and records its source"
else
  bad "A3: the env copy does not use the resolver"
fi
if has "$BACKUP" 'f="$(skygate_env_file'; then
  ok "A4: the PostgreSQL DSN lookup uses the resolver"
else
  bad "A4: the DSN lookup still reads only the checkout .env"
fi
if has "$BACKUP" 'skygate_env_file || true'; then
  ok "A5: the SQLite resolution (B459) now reads the same env file"
else
  bad "A5: the SQLite resolution still reads only the checkout .env"
fi

# --- B: headscale, native locations ---
if has "$BACKUP" 'for _d in /etc/headscale /home/admin/headscale/config'; then
  ok "B1: backup captures the headscale config from BOTH locations"
else
  bad "B1: backup captures only the docker headscale config path"
fi
if has "$BACKUP" 'HS_DB_NATIVE="$(headscale_db_path'; then
  ok "B2: backup prefers the NATIVE headscale database file"
else
  bad "B2: backup is still docker-volume-only for headscale's database"
fi
if has "$BACKUP" 'If headscale runs on PostgreSQL there is no file to copy'; then
  ok "B3: an uncaptured headscale database is named, not silently skipped"
else
  bad "B3: a missing headscale database still has no named reason"
fi

# --- C: the rest of /etc/skygate ---
if has "$BACKUP" 'for _f in cluster-state.json dbs.env'; then
  ok "C1: backup captures /etc/skygate (cluster-state.json, dbs.env)"
else
  bad "C1: backup does not capture /etc/skygate"
fi

# --- D: restore targets ---
if has "$RESTORE" 'skygate_env_target()'; then
  ok "D1: restore has an env-target resolver"
else
  bad "D1: restore writes the env into the checkout unconditionally"
fi
if has "$RESTORE" 'dest="$(skygate_env_target)"'; then
  ok "D2: restore writes the env to the resolved target"
else
  bad "D2: restore does not use the env-target resolver"
fi
if has "$RESTORE" 'that is the RUNTIME config file'; then
  ok "D3: restore says that a runtime-config write needs a restart"
else
  bad "D3: restore does not mention the restart"
fi
if has "$RESTORE" 'if [ -d /etc/headscale ]'; then
  ok "D4: restore targets /etc/headscale on a native host"
else
  bad "D4: restore only knows the docker headscale config path"
fi
if has "$RESTORE" 'hs_target="$(headscale_db_path'; then
  ok "D5: restore puts headscale's database back at its NATIVE path"
else
  bad "D5: restore is still docker-volume-only for headscale's database"
fi
if has "$RESTORE" 'if [ -d etc-skygate ]'; then
  ok "D6: restore puts /etc/skygate back"
else
  bad "D6: restore ignores the etc-skygate/ member"
fi

# --- E: the resolver must not drift between the two scripts ---
extract_fn() {
  awk -v name="$1" '
    index($0, name "() {") == 1 { on = 1 }
    on { print }
    on && /^}$/ { exit }
  ' "$2"
}
extract_fn headscale_db_path "$BACKUP"  > "$SKY_TMP/hs_backup.sh"
extract_fn headscale_db_path "$RESTORE" > "$SKY_TMP/hs_restore.sh"
if [ -s "$SKY_TMP/hs_backup.sh" ] && diff -q "$SKY_TMP/hs_backup.sh" "$SKY_TMP/hs_restore.sh" >/dev/null 2>&1; then
  ok "E1: headscale_db_path is byte-identical in backup.sh and restore.sh"
else
  bad "E1: headscale_db_path DRIFTED between backup.sh and restore.sh"
fi

# --- F: BEHAVIOURAL ---
{
  echo 'log()  { printf "[ok] %s\n" "$1"; }'
  echo 'warn() { printf "[warn] %s\n" "$1"; }'
  echo 'err()  { printf "[err] %s\n" "$1"; exit 1; }'
  echo 'SKYGATE_DIR="${SKYGATE_DIR:-}"'
  extract_fn skygate_env_file "$BACKUP"
  extract_fn skygate_db_value "$BACKUP"
  extract_fn headscale_db_path "$BACKUP"
} > "$SKY_TMP/funcs.sh"
if [ -s "$SKY_TMP/funcs.sh" ] && bash -n "$SKY_TMP/funcs.sh" 2>/dev/null; then
  ok "F0: the resolvers extract cleanly and parse"
else
  bad "F0: the resolvers could not be extracted"
fi

# F1: an explicit env file wins.
mkdir -p "$SKY_TMP/f1"
printf 'SKYGATE_DB=sqlite:/srv/f1/skygate.db\n' > "$SKY_TMP/f1/service.env"
printf 'SKYGATE_DB=sqlite:/srv/f1/checkout.db\n' > "$SKY_TMP/f1/checkout.env"
got=$(TARGET_DIR="$SKY_TMP/f1" SKYGATE_DIR="$SKY_TMP/f1" SKYGATE_ENV_FILE="$SKY_TMP/f1/service.env" \
      bash -c '. "$1/funcs.sh"; skygate_env_file' _ "$SKY_TMP" 2>&1)
if [ "$got" = "$SKY_TMP/f1/service.env" ]; then
  ok "F1: the service env file wins (checked first)"
else
  bad "F1: env-file precedence wrong (got '${got}')"
fi

# F2: the DB value is read OUT OF that file — the native case, where the checkout
# has no .env at all.
got=$(TARGET_DIR="$SKY_TMP/f1" SKYGATE_DIR="$SKY_TMP/f1" SKYGATE_ENV_FILE="$SKY_TMP/f1/service.env" SKYGATE_DB="" \
      bash -c '. "$1/funcs.sh"; skygate_db_value' _ "$SKY_TMP" 2>&1)
if [ "$got" = "sqlite:/srv/f1/skygate.db" ]; then
  ok "F2: SKYGATE_DB is resolved from the service env file, not the checkout"
else
  bad "F2: SKYGATE_DB resolution wrong (got '${got}')"
fi

# F3: the explicit headscale DB override wins.
mkdir -p "$SKY_TMP/f3"
printf 'x' > "$SKY_TMP/f3/db.sqlite"
got=$(SKYGATE_HEADSCALE_DB_PATH="$SKY_TMP/f3/db.sqlite" SKYGATE_HEADSCALE_CONFIG="" \
      bash -c '. "$1/funcs.sh"; headscale_db_path' _ "$SKY_TMP" 2>&1)
if [ "$got" = "$SKY_TMP/f3/db.sqlite" ]; then
  ok "F3: SKYGATE_HEADSCALE_DB_PATH is honoured"
else
  bad "F3: the explicit headscale DB path is ignored (got '${got}')"
fi

# F4: the path is parsed out of headscale's own config (block form).
mkdir -p "$SKY_TMP/f4/data"
printf 'x' > "$SKY_TMP/f4/data/db.sqlite"
cat > "$SKY_TMP/f4/config.yaml" <<EOF
server_url: https://hs.example.com
database:
  type: sqlite
  sqlite:
    path: $SKY_TMP/f4/data/db.sqlite
noise:
  private_key_path: /etc/headscale/noise_private.key
EOF
got=$(SKYGATE_HEADSCALE_DB_PATH="" SKYGATE_HEADSCALE_CONFIG="$SKY_TMP/f4/config.yaml" \
      bash -c '. "$1/funcs.sh"; headscale_db_path' _ "$SKY_TMP" 2>&1)
if [ "$got" = "$SKY_TMP/f4/data/db.sqlite" ]; then
  ok "F4: the database path is parsed from headscale's config"
else
  bad "F4: config parsing failed (got '${got}')"
fi

# F5: an unrelated `path:` (noise_private_key_path) must NOT be mistaken for the DB.
mkdir -p "$SKY_TMP/f5"
printf 'x' > "$SKY_TMP/f5/noise.key"
cat > "$SKY_TMP/f5/config.yaml" <<EOF
noise:
  private_key_path: $SKY_TMP/f5/noise.key
database:
  type: postgres
  postgres:
    host: db
EOF
got=$(SKYGATE_HEADSCALE_DB_PATH="" SKYGATE_HEADSCALE_CONFIG="$SKY_TMP/f5/config.yaml" \
      bash -c '. "$1/funcs.sh"; headscale_db_path' _ "$SKY_TMP" 2>&1)
if [ -z "$got" ]; then
  ok "F5: a PostgreSQL headscale config yields NO file path (and no false hit on private_key_path)"
else
  bad "F5: the resolver invented a database path '${got}'"
fi

# F6: an inline `sqlite: /path` form is understood too.
mkdir -p "$SKY_TMP/f6"
printf 'x' > "$SKY_TMP/f6/db.sqlite"
cat > "$SKY_TMP/f6/config.yaml" <<EOF
database:
  type: sqlite
  sqlite: $SKY_TMP/f6/db.sqlite
EOF
got=$(SKYGATE_HEADSCALE_DB_PATH="" SKYGATE_HEADSCALE_CONFIG="$SKY_TMP/f6/config.yaml" \
      bash -c '. "$1/funcs.sh"; headscale_db_path' _ "$SKY_TMP" 2>&1)
if [ "$got" = "$SKY_TMP/f6/db.sqlite" ]; then
  ok "F6: the inline sqlite: form is understood"
else
  bad "F6: the inline sqlite: form is not understood (got '${got}')"
fi

# --- G: the static detector must fail on the pre-B462 shape ---
cat > "$SKY_TMP/old_backup.sh" <<'EOF'
skygate_db_value() {
  local v="${SKYGATE_DB:-}"
  if [[ -z "${v}" ]] && [[ -f "${SKYGATE_DIR}/.env" ]]; then
    v=$(grep -E '^SKYGATE_DB=' "${SKYGATE_DIR}/.env" 2>/dev/null | head -1 | cut -d= -f2-)
  fi
  printf '%s\n' "${v}"
}
if [ -f "${SKYGATE_DIR}/.env" ]; then cp "${SKYGATE_DIR}/.env" x; fi
if [ -d /home/admin/headscale/config ]; then cp -r /home/admin/headscale/config x; fi
if command -v docker >/dev/null 2>&1; then docker run --rm -v headscale_headscale_data:/data x; fi
EOF
old_ok=1
# The proof must show the predicates DISCRIMINATE: each marker is present in the
# real backup.sh and ABSENT from the pre-B462 shape. Merely asserting "the old file
# does not contain the new code" would be trivially true and would prove nothing.
# (The first version of this leg got the polarity wrong and said "cannot see" while
# the detectors were in fact fine — caught by running it.)
for _marker in 'skygate_env_file()' '/etc/skygate/skygate.env' 'headscale_db_path' \
               'for _d in /etc/headscale' 'for _f in cluster-state.json dbs.env'; do
  if ! has "$BACKUP" "$_marker"; then old_ok=0; fi
  if has "$SKY_TMP/old_backup.sh" "$_marker"; then old_ok=0; fi
done
if [ "$old_ok" -eq 1 ]; then
  ok "G1: every predicate is present in backup.sh and absent from the pre-B462 shape (the detector discriminates)"
else
  bad "G1: the predicates do not discriminate the pre-B462 native-blind shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB462: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB462: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
