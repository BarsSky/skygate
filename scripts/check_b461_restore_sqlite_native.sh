#!/usr/bin/env bash
# ============================================================================
# check_b461_restore_sqlite_native.sh — B461
#
# THE RESTORE MUST SPEAK THE DATABASE'S DIALECT TOO.
#
# B459 gave scripts/backup.sh a SQLite branch, so a native SQLite install produces
# an archive whose database is `skygate.db` — and the same script prints
# `./scripts/restore.sh <archive>` as the next step. The restore half was still
# docker-only: do_skygate_db() copied the file into the docker named volume
# `skygate-data` and labelled it "v0.32.x legacy", so on a native install (no
# docker, the database at an absolute path) the documented command could not put
# the database back. A backup you cannot restore is not a backup.
#
# Contracts:
#   A. the resolver exists and reads env -> restored .env -> the archive's own env
#   B. a native target is written, the previous database is KEPT, stale -wal/-shm
#      are removed, and failure to back up the live file REFUSES to overwrite it
#   C. an empty or non-SQLite archive file is refused BEFORE anything is touched
#   D. the docker branch is the fallback, and its failure is an ERROR (not a warn
#      that lets a silent no-op look like a restore)
#   E. BEHAVIOURAL: the functions are extracted from the real script and executed
#   F. the detector can fail on the pre-B461 shape
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b461.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

SCRIPT=scripts/restore.sh
PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }

[ -f "$SCRIPT" ] || { echo "FAIL: $SCRIPT not found (run from the repository root)" >&2; exit 1; }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

echo "=== B461: the restore speaks the database's dialect ==="

# --- A: the resolver ---
if has "$SCRIPT" 'sqlite_target_path()'; then
  ok "A1: sqlite_target_path() exists"
else
  bad "A1: no sqlite_target_path() in restore.sh"
fi
for probe in '"${SKYGATE_DB:-}"' '"${TARGET_DIR}/.env"' '"skygate.env"'; do
  if has "$SCRIPT" "$probe"; then
    ok "A2: the resolver consults $probe"
  else
    bad "A2: the resolver does not consult $probe"
  fi
done

# --- B/C/D: the shape of the branch ---
if has "$SCRIPT" 'do_skygate_db_sqlite()'; then
  ok "B1: the SQLite restore is its own function"
else
  bad "B1: no dedicated SQLite restore function"
fi
if has "$SCRIPT" '.pre-restore-'; then
  ok "B2: the previous database is kept before it is overwritten"
else
  bad "B2: the live database would be overwritten with no copy kept"
fi
if has "$SCRIPT" 'rm -f "${target}-wal" "${target}-shm"'; then
  ok "B3: a stale -wal/-shm is removed next to the new file"
else
  bad "B3: a stale -wal/-shm would be replayed against different data"
fi
if has "$SCRIPT" 'refusing to touch the live database'; then
  ok "C1: an empty archive file is refused"
else
  bad "C1: an empty archive file is not refused"
fi
if has "$SCRIPT" 'does not carry the SQLite header'; then
  ok "C2: a non-SQLite archive file is refused"
else
  bad "C2: a non-SQLite archive file is not refused"
fi
if has "$SCRIPT" 'the database was NOT restored'; then
  ok "D1: a docker-branch failure is an ERROR that says the database was not restored"
else
  bad "D1: a docker-branch failure is still only a warning"
fi
# D2 — the stale wording must not survive in anything the OPERATOR sees. A comment
# that quotes the old wording (to explain what was wrong) is legitimate, so the
# predicate looks at non-comment lines only.
legacy_hits=$(grep -n 'v0\.32\.x legacy' "$SCRIPT" 2>/dev/null | grep -vE ':[[:space:]]*#' || true)
if [ -n "$legacy_hits" ]; then
  bad "D2: the script still calls a current SQLite archive 'v0.32.x legacy' in user-visible output"
else
  ok "D2: no user-visible 'v0.32.x legacy' wording"
fi

# --- E: BEHAVIOURAL — run the real functions ---
extract_fn() {
  awk -v name="$1" '
    index($0, name "() {") == 1 { on = 1 }
    on { print }
    on && /^}$/ { exit }
  ' "$SCRIPT"
}

{
  echo 'log()  { printf "[ok] %s\n" "$1"; }'
  echo 'warn() { printf "[warn] %s\n" "$1"; }'
  echo 'err()  { printf "[err] %s\n" "$1"; exit 1; }'
  echo 'TARGET_DIR="${TARGET_DIR:-}"'
  extract_fn sqlite_target_path
  extract_fn do_skygate_db_sqlite
  extract_fn do_skygate_db
} > "$SKY_TMP/funcs.sh"

if [ -s "$SKY_TMP/funcs.sh" ] && bash -n "$SKY_TMP/funcs.sh" 2>/dev/null; then
  ok "E0: the restore functions extract cleanly and parse"
else
  bad "E0: the restore functions could not be extracted"
fi

# A real SQLite header (16 bytes) plus padding.
mkdb() {
  printf 'SQLite format 3\000' > "$1"
  head -c 256 /dev/zero >> "$1"
}

# E1: native target — file replaced, previous kept, stale WAL removed.
mkdir -p "$SKY_TMP/e1/work" "$SKY_TMP/e1/data"
mkdb "$SKY_TMP/e1/work/skygate.db"
printf 'ARCHIVED-PAYLOAD' >> "$SKY_TMP/e1/work/skygate.db"
mkdb "$SKY_TMP/e1/data/skygate.db"
printf 'OLD-DATA' > "$SKY_TMP/e1/data/skygate.db.keepme"
printf 'stale-wal' > "$SKY_TMP/e1/data/skygate.db-wal"
printf 'stale-shm' > "$SKY_TMP/e1/data/skygate.db-shm"
target="$SKY_TMP/e1/data/skygate.db"
out=$( cd "$SKY_TMP/e1/work" && TARGET_DIR="$SKY_TMP/e1/data" SKYGATE_DB="sqlite:$target" \
        bash -c '. "$1/funcs.sh"; do_skygate_db' _ "$SKY_TMP" 2>&1 ); rc=$?
if [ "$rc" -eq 0 ] \
   && grep -q 'ARCHIVED-PAYLOAD' "$target" 2>/dev/null \
   && ls "$SKY_TMP/e1/data/skygate.db.pre-restore-"* >/dev/null 2>&1 \
   && [ ! -f "${target}-wal" ] && [ ! -f "${target}-shm" ]; then
  ok "E1: a native target is restored, the previous file kept, stale -wal/-shm removed"
else
  bad "E1: native restore wrong (rc=$rc); out: $(printf '%s' "$out" | tr '\n' ' ')"
fi

# The kept copy must be the OLD bytes, not the new ones.
kept=$(ls "$SKY_TMP/e1/data/skygate.db.pre-restore-"* 2>/dev/null | head -1)
if [ -n "$kept" ] && ! grep -q 'ARCHIVED-PAYLOAD' "$kept"; then
  ok "E2: the kept copy holds the PREVIOUS data, not the restored one"
else
  bad "E2: the kept copy is not a real rollback point"
fi

# E3: an empty archive database must be refused and the live file left alone.
mkdir -p "$SKY_TMP/e3/work" "$SKY_TMP/e3/data"
: > "$SKY_TMP/e3/work/skygate.db"
mkdb "$SKY_TMP/e3/data/skygate.db"
printf 'LIVE-DATA' >> "$SKY_TMP/e3/data/skygate.db"
target3="$SKY_TMP/e3/data/skygate.db"
out3=$( cd "$SKY_TMP/e3/work" && TARGET_DIR="$SKY_TMP/e3/data" SKYGATE_DB="sqlite:$target3" \
        bash -c '. "$1/funcs.sh"; do_skygate_db' _ "$SKY_TMP" 2>&1 ); rc3=$?
if [ "$rc3" -ne 0 ] && grep -q 'LIVE-DATA' "$target3" && ! ls "$SKY_TMP/e3/data"/skygate.db.pre-restore-* >/dev/null 2>&1; then
  ok "E3: an empty archive file is refused and the live database is untouched"
else
  bad "E3: empty archive handling wrong (rc=$rc3)"
fi

# E4: a non-SQLite archive file must be refused too.
mkdir -p "$SKY_TMP/e4/work" "$SKY_TMP/e4/data"
printf 'not a database at all' > "$SKY_TMP/e4/work/skygate.db"
mkdb "$SKY_TMP/e4/data/skygate.db"
printf 'LIVE-DATA' >> "$SKY_TMP/e4/data/skygate.db"
target4="$SKY_TMP/e4/data/skygate.db"
out4=$( cd "$SKY_TMP/e4/work" && TARGET_DIR="$SKY_TMP/e4/data" SKYGATE_DB="sqlite:$target4" \
        bash -c '. "$1/funcs.sh"; do_skygate_db' _ "$SKY_TMP" 2>&1 ); rc4=$?
if [ "$rc4" -ne 0 ] && grep -q 'LIVE-DATA' "$target4"; then
  ok "E4: a non-SQLite archive file is refused and the live database is untouched"
else
  bad "E4: non-SQLite archive handling wrong (rc=$rc4)"
fi

# E5: PostgreSQL archives still go to the pg path, not the SQLite one.
mkdir -p "$SKY_TMP/e5/work"
: > "$SKY_TMP/e5/work/skygate-pg.sql"
out5=$( cd "$SKY_TMP/e5/work" && bash -c '
  . "$1/funcs.sh"
  do_pg_restore() { echo PG-RESTORE-CALLED; }
  do_skygate_db' _ "$SKY_TMP" 2>&1 )
if [[ "$out5" == *PG-RESTORE-CALLED* ]]; then
  ok "E5: an archive with skygate-pg.sql still takes the PostgreSQL path"
else
  bad "E5: the PostgreSQL path was disturbed (out: $(printf '%s' "$out5" | tr '\n' ' '))"
fi

# E6: with no resolvable native path, the docker branch runs — and its failure must
# be an ERROR naming the knob. `docker` is stubbed so the case is deterministic on
# every host (an unstubbed one would either be missing on Windows or really try to
# pull alpine on a host that has docker).
mkdir -p "$SKY_TMP/e6/work" "$SKY_TMP/e6/bin"
mkdb "$SKY_TMP/e6/work/skygate.db"
printf '#!/bin/sh\nexit 1\n' > "$SKY_TMP/e6/bin/docker"
chmod +x "$SKY_TMP/e6/bin/docker"
out6=$( cd "$SKY_TMP/e6/work" && TARGET_DIR="$SKY_TMP/e6/none" SKYGATE_DB="" \
        PATH="$SKY_TMP/e6/bin:$PATH" \
        bash -c '. "$1/funcs.sh"; do_skygate_db' _ "$SKY_TMP" 2>&1 ); rc6=$?
if [ "$rc6" -ne 0 ] && [[ "$out6" == *SKYGATE_DB* ]]; then
  ok "E6: the docker fallback failure is an error that names SKYGATE_DB"
else
  bad "E6: docker fallback failure handling wrong (rc=$rc6); out: $(printf '%s' "$out6" | tr '\n' ' ')"
fi

# E7: THE BUG THE BEHAVIOURAL LEG FOUND (2026-10-29). An earlier version of the
# resolver guessed "value vs file path" from the SHAPE of the string, so the
# candidate `${TARGET_DIR}/.env` — an absolute path that does NOT exist — was
# taken as the database path, and the restore wrote skygate.db OVER that .env.
# Pin the correct answer directly: with no SKYGATE_DB and no reachable env file,
# the resolver must print NOTHING.
mkdir -p "$SKY_TMP/e7/work"
resolved7=$( cd "$SKY_TMP/e7/work" && TARGET_DIR="$SKY_TMP/e7/does-not-exist" SKYGATE_DB="" \
              bash -c '. "$1/funcs.sh"; sqlite_target_path' _ "$SKY_TMP" 2>&1 )
if [ -z "$resolved7" ]; then
  ok "E7: a missing \${TARGET_DIR}/.env is not mistaken for the database path"
else
  bad "E7: the resolver returned '${resolved7}' for a non-existent .env — it would overwrite that file"
fi

# E8: but a REAL restored .env IS read, and its sqlite: value is resolved.
mkdir -p "$SKY_TMP/e8/work" "$SKY_TMP/e8/data"
printf 'SKYGATE_DB=sqlite:%s/skygate.db\n' "$SKY_TMP/e8/data" > "$SKY_TMP/e8/.env"
resolved8=$( cd "$SKY_TMP/e8/work" && TARGET_DIR="$SKY_TMP/e8" SKYGATE_DB="" \
              bash -c '. "$1/funcs.sh"; sqlite_target_path' _ "$SKY_TMP" 2>&1 )
if [ "$resolved8" = "$SKY_TMP/e8/data/skygate.db" ]; then
  ok "E8: a real \${TARGET_DIR}/.env IS read and its sqlite: value resolved"
else
  bad "E8: the .env source is broken (got '${resolved8}')"
fi

# --- F: prove the static detector can fail on the pre-B461 shape ---
cat > "$SKY_TMP/old_shape.sh" <<'EOF'
do_skygate_db() {
    if [ -f skygate-pg.sql ]; then
        do_pg_restore
    elif [ -f skygate.db ]; then
        log "Restoring Skygate SQLite database (skygate.db, v0.32.x legacy)..."
        docker run --rm -v skygate-data:/data -v "$(pwd):/restore" alpine \
            sh -c "cp /restore/skygate.db /data/skygate.db" 2>/dev/null && \
            log "  skygate.db restored to Docker volume" || \
            warn "  DB restore failed (is container running?)"
    fi
}
EOF
old_ok=1
has "$SKY_TMP/old_shape.sh" 'sqlite_target_path()' && old_ok=0
has "$SKY_TMP/old_shape.sh" 'v0.32.x legacy' && old_ok=0
has "$SKY_TMP/old_shape.sh" 'the database was NOT restored' && old_ok=0
has "$SKY_TMP/old_shape.sh" 'DB restore failed' && old_ok=0
if [ "$old_ok" -eq 1 ]; then
  bad "F1: the static predicates cannot see the pre-B461 docker-only shape"
else
  ok "F1: the static predicates reject the pre-B461 docker-only shape"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB461: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB461: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
