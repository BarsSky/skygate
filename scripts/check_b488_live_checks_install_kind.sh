#!/usr/bin/env bash
# B488 (TD-37) — a LIVE-STATE CHECK must ask whether THIS host can answer, not whether a container
# exists. Five contracts were docker-shaped (dup users, reconcile audit writes, node_owner_map
# orphans, tagOwners coverage, B188.2's live policy read): they ran `docker exec headscale headscale …`
# and `docker exec skygate-pg-local psql …` directly, so after the 2026-11-15 migration to a NATIVE
# (systemd) install every one of them answered SKIP on the production host — the host that actually
# serves the tailnet. `scripts/lib/live_db.sh` is now the single place that knows the shape
# (live_kind → docker | native | none) and the transport (live_headscale, live_db_sql, live_db_dsn).
#
# Contracts:
#   A. the library exists, is LINE-FEED clean shell, and git TRACKS it
#      (AGENTS trap #11: a new script that .gitignore eats passes every local contract and is
#      absent from the pushed tag — the operator then runs a file that is not there)
#   B. it exposes the four transport entry points plus the env-file override used below
#   C. every check that inspects live state SOURCES it
#   D. none of them RUNS `docker exec` any more (mentions in prose remediation text are allowed —
#      the debt was the executed transport, not the word)
#   E. the B479 guard asks live_kind instead of inspecting docker by hand, and its SKIP message no
#      longer claims the check is docker-shaped
#   F. the NATIVE transport really answers: a throwaway env file naming a sqlite DSN is read through
#      the library and reaches BOTH the headscale CLI and the DB (stub CLI/sqlite3/sudo — no /etc
#      write, no root, no live host needed)
#   G. the DOCKER transport really answers, decided from a stub daemon (the shape that must keep
#      working unchanged for the compose install)
#   H. a host with NEITHER reports `none`, and the transport functions fail rather than inventing
#      an empty answer (an empty answer would be read as "no duplicates" / "no orphans")
#   I. the guard's own verdict follows the same decision, in both directions
#   J. the guard and the library stay in step — the guard must not re-implement the shape question
#      (two detectors drift, and the drift shows up as a silent SKIP)
#
# The behavioural half (F–J) is the point: the previous contracts only ever proved the WORDING of
# the shape question, which is exactly how a docker-only assumption survived onto a native host.

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LIB="$ROOT/scripts/lib/live_db.sh"
GUARD="$ROOT/scripts/lib/skip_if_no_live_stack.sh"

# The contracts that inspect live state through the library (B188.3's live leg is a Go test against a
# PG DSN, so it has no transport of its own and is not in this list).
TRANSPORT_CHECKS="check_b_duplicate_users.sh check_b_reconcile_audit_writes.sh check_b_node_owner_map_orphans.sh check_b_tag_owners.sh check_b188_2.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-check.XXXXXX")" || { echo "  FAIL  cannot create a scratch dir"; exit 1; }
trap 'rm -rf "$SKY_TMP"' EXIT

# ═══════════════ A. the library exists, parses, and is tracked ═══════════════
if [ -f "$LIB" ]; then
    ok "A1: scripts/lib/live_db.sh exists"
else
    bad "A1: scripts/lib/live_db.sh is missing — the five live checks have nowhere to ask"
fi
if bash -n "$LIB" 2>/dev/null; then
    ok "A2: the library parses (bash -n)"
else
    bad "A2: the library does not parse — every check that sources it would break"
fi
if git -C "$ROOT" ls-files --error-unmatch scripts/lib/live_db.sh >/dev/null 2>&1; then
    ok "A3: git tracks the library (trap #11 — a contract guarding an untracked file proves nothing)"
else
    bad "A3: scripts/lib/live_db.sh is NOT tracked by git — it would be absent from the released tag"
fi
# CR count through awk, not grep: MSYS grep translates a CR pattern into a line break and counts
# every line (measured here), so a local run would report a phantom FAIL while Linux answers the
# truth — the same Windows-only quirk AGENTS trap #10 records for B144.
cr_lines="$(awk 'index($0, "\r") { n++ } END { print n + 0 }' "$LIB" 2>/dev/null)"
if [ "${cr_lines:-1}" = "0" ]; then
    ok "A4: the library has LF line endings (a CRLF shell script dies with a cryptic bad interpreter)"
else
    bad "A4: the library carries $cr_lines CR line(s) — normalise it to LF before committing"
fi

# ═══════════════ B. the entry points ═══════════════
for fn in live_kind live_headscale live_db_sql live_db_dsn live_env_file; do
    if grep -q "^${fn}()" "$LIB" 2>/dev/null || grep -q "^${fn} ()" "$LIB" 2>/dev/null; then
        ok "B: $fn() is defined by the library"
    else
        bad "B: $fn() is missing from the library"
    fi
done

# ═══════════════ C. every live check sources it ═══════════════
missing_src=""
for f in $TRANSPORT_CHECKS; do
    p="$ROOT/scripts/$f"
    if [ ! -f "$p" ]; then
        missing_src="$missing_src $f(absent)"
        continue
    fi
    case "$(cat "$p")" in
        *lib/live_db.sh*) : ;;
        *) missing_src="$missing_src $f" ;;
    esac
done
case "$missing_src" in
    '') ok "C1: all five live checks source scripts/lib/live_db.sh" ;;
    *)  bad "C1: these checks still do not source the library:$missing_src" ;;
esac

# ═══════════════ D. no check EXECUTES docker any more ═══════════════
still_running=""
for f in $TRANSPORT_CHECKS; do
    p="$ROOT/scripts/$f"
    [ -f "$p" ] || continue
    n=0
    while IFS= read -r line; do
        case "$line" in
            ''|'#'*) continue ;;
            *'docker exec'*)
                # prose remediation lines start with echo/printf; anything else is a command
                case "${line#"${line%%[![:space:]]*}"}" in
                    echo\ *|printf\ *|*\ \#\ *docker\ exec*) continue ;;
                    *) n=$((n + 1)) ;;
                esac
                ;;
        esac
    done < "$p"
    [ "$n" -gt 0 ] && still_running="$still_running $f($n)"
done
case "$still_running" in
    '') ok "D1: no live check runs a docker command — the transport lives in the library only" ;;
    *)  bad "D1: these checks still EXECUTE docker:$still_running" ;;
esac

# ═══════════════ E. the guard delegates the shape question ═══════════════
guard_text="$(cat "$GUARD" 2>/dev/null || true)"
case "$guard_text" in
    *'live_kind'*) ok "E1: the B479 guard asks live_kind" ;;
    *) bad "E1: the guard does not ask live_kind — a native host would still skip" ;;
esac
case "$guard_text" in
    *'live_db.sh'*) ok "E2: the guard sources the library" ;;
    *) bad "E2: the guard does not source the library" ;;
esac
# E3/E4 inspect the MESSAGE, not the whole file: the header comment legitimately records that the
# checks USED to be docker-shaped, and a contract that forbids the phrase would punish the history
# that explains the fix.
guard_skip_line="$(grep 'SKIP:' "$GUARD" 2>/dev/null || true)"
guard_inspects="$(grep -n 'docker inspect' "$GUARD" 2>/dev/null | grep -v '^ *[0-9]*: *#' || true)"
case "$guard_inspects" in
    '') ok "E3: the guard no longer executes a docker inspection — one detector, no drift" ;;
    *) bad "E3: the guard still inspects docker by hand: $guard_inspects" ;;
esac
case "$guard_skip_line" in
    *'docker-shaped'*) bad "E4: the SKIP message still calls the check docker-shaped" ;;
    *SKIP*) ok "E4: the SKIP message describes the installation, not a container" ;;
    *) bad "E4: the guard has no SKIP message — an absent live host must say why" ;;
esac

# ═══════════════ F–I. behavioural: the three shapes, decided for real ═══════════════
# One stub directory per shape. The NATIVE one deliberately carries a docker that FAILS its daemon
# probe: the production host is native, and a stray real docker on the machine under test must not
# decide the answer (this contract first failed F1–F5 for exactly that reason).
mkdir -p "$SKY_TMP/native" "$SKY_TMP/dockerstub" "$SKY_TMP/nodocker"

cat > "$SKY_TMP/native/sudo" <<'STUB'
#!/bin/sh
[ "${1:-}" = "-n" ] && shift
exec "$@"
STUB

cat > "$SKY_TMP/native/docker" <<'STUB'
#!/bin/sh
exit 1
STUB

cat > "$SKY_TMP/native/headscale" <<'STUB'
#!/bin/sh
echo '{"users":[{"id":1,"name":"native-user"}]}'
STUB

cat > "$SKY_TMP/native/sqlite3" <<'STUB'
#!/bin/sh
# live_db_sql calls: sqlite3 -readonly <path> <sql> — answer only for the path it was given.
case "$*" in
    *native.db*) echo 'native-answer' ;;
    *) echo "unexpected sqlite3 argv: $*" >&2; exit 1 ;;
esac
STUB

cat > "$SKY_TMP/dockerstub/sudo" <<'STUB'
#!/bin/sh
[ "${1:-}" = "-n" ] && shift
exec "$@"
STUB

cat > "$SKY_TMP/dockerstub/docker" <<'STUB'
#!/bin/sh
case "${1:-}" in
    info) exit 0 ;;
    inspect) echo true; exit 0 ;;
    exec)
        shift 2
        case "${1:-}" in
            sh)        echo 'sqlite:/data/skygate.db' ;;
            sqlite3)   echo 'docker-answer' ;;
            headscale) shift; echo '{"users":[{"id":2,"name":"docker-user"}]}' ;;
            *)         exit 1 ;;
        esac
        ;;
    *) exit 1 ;;
esac
STUB

cat > "$SKY_TMP/nodocker/docker" <<'STUB'
#!/bin/sh
exit 1
STUB

chmod +x "$SKY_TMP/"*/*

printf 'SKYGATE_DB=sqlite:%s\n' "$SKY_TMP/native.db" > "$SKY_TMP/env"

run_lib() { # run_lib <envfile> <stubdir> <snippet>
    SKYGATE_LIVE_ENV_FILE="$1" PATH="$2:$PATH" bash -c '. "$0"; '"$3" "$LIB" 2>/dev/null
}

# F. native
kind="$(run_lib "$SKY_TMP/env" "$SKY_TMP/native" 'live_kind')"
case "$kind" in
    native) ok "F1: an env file on the host means the NATIVE shape (live_kind=native)" ;;
    *)      bad "F1: live_kind answered '$kind' with an env file present — expected native" ;;
esac
dsn="$(run_lib "$SKY_TMP/env" "$SKY_TMP/native" 'live_db_dsn')"
case "$dsn" in
    "sqlite:$SKY_TMP/native.db") ok "F2: live_db_dsn reads SKYGATE_DB from the env file" ;;
    *) bad "F2: live_db_dsn answered '$dsn' — expected the DSN named by SKYGATE_DB" ;;
esac
hs="$(run_lib "$SKY_TMP/env" "$SKY_TMP/native" 'live_headscale users list -o json')"
case "$hs" in
    *native-user*) ok "F3: live_headscale reaches the local binary on a native install" ;;
    *) bad "F3: live_headscale answered '$hs' — expected the local CLI's JSON" ;;
esac
row="$(run_lib "$SKY_TMP/env" "$SKY_TMP/native" 'live_db_sql "SELECT 1;"')"
case "$row" in
    native-answer) ok "F4: live_db_sql reaches the native sqlite database named by the env file" ;;
    *) bad "F4: live_db_sql answered '$row' — expected the sqlite3 answer" ;;
esac
row2="$(run_lib "$SKY_TMP/env" "$SKY_TMP/native" "live_db_sql -F'|' \"SELECT a, b FROM t;\"")"
case "$row2" in
    native-answer) ok "F5: live_db_sql passes the separator through on the native path" ;;
    *) bad "F5: live_db_sql with -F answered '$row2'" ;;
esac
if command -v sqlite3 >/dev/null 2>&1; then
    real_db="$SKY_TMP/real.db"
    if sqlite3 "$real_db" 'CREATE TABLE t(a INTEGER); INSERT INTO t VALUES (7);' 2>/dev/null; then
        printf 'SKYGATE_DB=sqlite:%s\n' "$real_db" > "$SKY_TMP/env.real"
        # B522 (measured 2026-11-25): the answer and the REASON must be told apart. Before
        # this, the leg compared stdout with 7 only — so on the reference host, where the
        # gate runs as a user without passwordless sudo, the transport returned an empty
        # string and this contract reported the LIBRARY as broken while it was the
        # privilege that was missing. "Could not read" is a fact about the check (SKIP with
        # the reason named); "read the wrong value" is a fact about the product (FAIL).
        real_out="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/env.real" bash -c '. "$0"; live_db_sql "SELECT a FROM t;"' "$LIB" 2>&1)"; real_rc=$?
        if [ "$real_rc" -ne 0 ]; then
            skip "F6: the transport could not answer here (rc=$real_rc: $(printf '%s' "$real_out" | tail -1)) — no sqlite3/sudo for this user, so nothing about the product is proven"
        elif [ "$real_out" = "7" ]; then
            ok "F6: live_db_sql reads a REAL sqlite file end to end"
        else
            bad "F6: live_db_sql answered '$real_out' against a real sqlite DB — expected 7"
        fi
    else
        skip "F6: could not build a real sqlite fixture on this host"
    fi
else
    skip "F6: no sqlite3 on this host — the real-file leg needs it (the run above used a stub)"
fi

# G. docker
kindd="$(run_lib "$SKY_TMP/absent.env" "$SKY_TMP/dockerstub" 'live_kind')"
case "$kindd" in
    docker) ok "G1: a running container pair means the DOCKER shape (unchanged for compose hosts)" ;;
    *)      bad "G1: live_kind answered '$kindd' with a stub daemon — expected docker" ;;
esac
rowd="$(run_lib "$SKY_TMP/absent.env" "$SKY_TMP/dockerstub" 'live_db_sql "SELECT 1;"')"
case "$rowd" in
    docker-answer) ok "G2: live_db_sql reaches the containerised panel database" ;;
    *) bad "G2: live_db_sql answered '$rowd' on the docker path — expected the container answer" ;;
esac
hsd="$(run_lib "$SKY_TMP/absent.env" "$SKY_TMP/dockerstub" 'live_headscale users list -o json')"
case "$hsd" in
    *docker-user*) ok "G3: live_headscale runs the CLI inside the headscale container" ;;
    *) bad "G3: live_headscale answered '$hsd' on the docker path" ;;
esac

# H. neither
kindn="$(run_lib "$SKY_TMP/absent.env" "$SKY_TMP/nodocker" 'live_kind')"
case "$kindn" in
    none) ok "H1: a host with no containers and no env file is 'none'" ;;
    *)    bad "H1: live_kind answered '$kindn' for a dead daemon and no env file — expected none" ;;
esac
if run_lib "$SKY_TMP/absent.env" "$SKY_TMP/nodocker" 'live_db_sql "SELECT 1;"' >/dev/null 2>&1; then
    bad "H2: live_db_sql succeeded with no installation — an empty answer reads as 'nothing wrong'"
else
    ok "H2: live_db_sql FAILS (non-zero) when no installation can answer"
fi
if run_lib "$SKY_TMP/absent.env" "$SKY_TMP/nodocker" 'live_headscale users list' >/dev/null 2>&1; then
    bad "H3: live_headscale succeeded with no installation"
else
    ok "H3: live_headscale FAILS when no installation can answer"
fi
if run_lib "$SKY_TMP/env" "$SKY_TMP/native" 'live_db_sql ""' >/dev/null 2>&1; then
    bad "H4: live_db_sql accepted an empty statement"
else
    ok "H4: live_db_sql refuses an empty statement instead of querying nothing"
fi

# I. the guard's own verdict
mkdir -p "$SKY_TMP/guard"
cp "$LIB" "$SKY_TMP/guard/live_db.sh"
cp "$GUARD" "$SKY_TMP/guard/skip_if_no_live_stack.sh"
cat > "$SKY_TMP/guard/run.sh" <<'EOF'
#!/bin/sh
. "$(dirname "$0")/skip_if_no_live_stack.sh"
echo ALIVE
EOF
chmod +x "$SKY_TMP/guard/run.sh"
guard_native="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/env" PATH="$SKY_TMP/native:$PATH" bash "$SKY_TMP/guard/run.sh" 2>&1)"
case "$guard_native" in
    *ALIVE*) ok "I1: on a native install the guard lets the check run (pre-B488 it skipped)" ;;
    *) bad "I1: the guard exited early on a native install: $guard_native" ;;
esac
guard_none="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/absent.env" PATH="$SKY_TMP/nodocker:$PATH" bash "$SKY_TMP/guard/run.sh" 2>&1)"
case "$guard_none" in
    *ALIVE*) bad "I2: the guard let a check run with no installation at all" ;;
    *SKIP*)  ok "I2: with no installation the guard still reports SKIP (never FAIL)" ;;
    *)       bad "I2: unexpected guard verdict with no installation: $guard_none" ;;
esac

# J. guard and library stay one decision
if [ "$(grep -c 'live_kind' "$GUARD" 2>/dev/null || true)" -ge 1 ] \
   && [ "$(grep -c 'docker inspect' "$GUARD" 2>/dev/null || true)" = "0" ]; then
    ok "J1: exactly one shape detector (the library) — the guard only asks it"
else
    bad "J1: the guard and the library disagree about how the shape is decided"
fi
if grep -q 'SKYGATE_LIVE_ENV_FILE' "$LIB" 2>/dev/null; then
    ok "J2: the env-file override that makes the native path testable is documented in the library"
else
    bad "J2: the library has no SKYGATE_LIVE_ENV_FILE override — the native path is untestable off-host"
fi

echo
echo "  B488: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
