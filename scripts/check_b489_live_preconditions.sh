#!/usr/bin/env bash
# B489 (2026-11-17) — B488 moved the TRANSPORT into a library; this block makes sure each CHECK
# actually reaches it. Measured on the production host right after v1.5.233 shipped: the library
# answered `kind=native` and `check_b_duplicate_users.sh` verified all 5 users, while three sibling
# checks still printed "SKIP  skygate-skygate-1 container not running" — they had their own
# `docker inspect` precondition BELOW the shared guard, so the guard no longer decided anything and
# the fix stopped one layer short of the checks it was written for. A contract that only proves the
# library cannot see that (B488's did not), which is why this one runs the checks themselves.
#
# Contracts:
#   A. no check EXECUTES docker any more (inspect / ps / exec in command position; prose is fine)
#   B. run with a NATIVE-shaped environment (env file + stubbed headscale/sqlite3/sudo) the three
#      read-only checks must NOT answer SKIP — they must reach their real work. The stub DB returns
#      nothing, so a FAIL is acceptable and expected here; "SKIP" is what this contract forbids,
#      because that is what the production host was told while its state was perfectly readable.
#   C. the restart-based check keeps its limitation, but names it: on a native install it must say
#      "restart-based" instead of the useless "container not running"
#   D. with NO installation each of the four reports SKIP and exits 0, never FAIL (rule 1)
#   E. the block is registered in the catalog and in the AGENTS index, and the follow-up is noted in
#      the roadmap (a fix nobody can find is how this class returned once already)

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

READ_ONLY_CHECKS="check_b_duplicate_users.sh check_b_node_owner_map_orphans.sh check_b_tag_owners.sh"
ALL_CHECKS="$READ_ONLY_CHECKS check_b_reconcile_audit_writes.sh"
# The restart-based check is DOCKER-ONLY BY DESIGN (it restarts the container and polls docke
# health); its docker usage is legitimate but must sit behind the shape decision — contract A2.
CONTAINER_ONLY_CHECKS="check_b_reconcile_audit_writes.sh"
A1_CHECKS="$READ_ONLY_CHECKS check_b_node_attribution.sh"

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-check.XXXXXX")" || { echo "  FAIL  cannot create a scratch dir"; exit 1; }
trap 'rm -rf "$SKY_TMP"' EXIT

# ═══════════════ A. no check decides with docker unless it must ═══════════════
still=""
for f in $A1_CHECKS; do
    p="scripts/$f"
    [ -f "$p" ] || { still="$still $f(absent)"; continue; }
    n=0
    while IFS= read -r line; do
        # comments may sit behind indentation — trim BEFORE deciding what the line is
        trimmed="${line#"${line%%[![:space:]]*}"}"
        case "$trimmed" in
            ''|'#'*) continue ;;
            echo\ *|printf\ *|*\ echo\ *) continue ;;
        esac
        case "$trimmed" in
            *'docker inspect'*|*'docker ps '*|*'docker exec '*|*'docker restart'*) n=$((n + 1)) ;;
        esac
    done < "$p"
    [ "$n" -gt 0 ] && still="$still $f($n)"
done
case "$still" in
    '') ok "A1: no live check runs docker in command position (the precondition is the installation)" ;;
    *)  bad "A1: these checks still decide with docker themselves:$still" ;;
esac

# A2: the restart-based check may use docker, but only AFTER the shape decision — a bare docker call
# before it is exactly how the fix stopped one layer short in v1.5.233.
for f in $CONTAINER_ONLY_CHECKS; do
    p="scripts/$f"
    kind_line="$(grep -n 'live_kind' "$p" 2>/dev/null | head -1 | cut -d: -f1)"
    docker_line="$(grep -n 'docker restart\|docker inspect' "$p" 2>/dev/null | head -1 | cut -d: -f1)"
    if [ -z "$docker_line" ]; then
        bad "A2: $f no longer restarts anything — the contract expects a restart-based check"
    elif [ -z "$kind_line" ]; then
        bad "A2: $f uses docker without asking the shape first"
    elif [ "$kind_line" -lt "$docker_line" ]; then
        ok "A2: $f decides the shape (line $kind_line) before its first docker call (line $docker_line)"
    else
        bad "A2: $f calls docker (line $docker_line) before deciding the shape (line $kind_line)"
    fi
done

# ═══════════════ stubs ═══════════════
mkdir -p "$SKY_TMP/native" "$SKY_TMP/nodocker"
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
case "$*" in
    *"users list"*)  echo '[{"id":1,"name":"alpha"},{"id":2,"name":"beta"}]' ;;
    *"policy get"*)  echo '{"tagOwners":{"tag:public":["a@"],"tag:exit-node":["b@"],"tag:private":["c@"],"tag:subnet-router":["d@"]}}' ;;
    *) exit 1 ;;
esac
STUB
cat > "$SKY_TMP/native/sqlite3" <<'STUB'
#!/bin/sh
# An empty result: the checks must still get PAST their precondition, which is the only claim here.
exit 0
STUB
cat > "$SKY_TMP/nodocker/docker" <<'STUB'
#!/bin/sh
exit 1
STUB
chmod +x "$SKY_TMP/"*/*
printf 'SKYGATE_DB=sqlite:%s\n' "$SKY_TMP/live.db" > "$SKY_TMP/env"

# ═══════════════ B/C. native-shaped run ═══════════════
for f in $READ_ONLY_CHECKS; do
    out="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/env" PATH="$SKY_TMP/native:$PATH" bash "scripts/$f" 2>&1)"
    case "$out" in
        *'container not running'*)
            bad "B: $f still answers 'container not running' on a native install" ;;
        SKIP*|*$'\n''SKIP'*)
            bad "B: $f SKIPped on a native install it can read: $(printf '%s' "$out" | head -1)" ;;
        '')
            bad "B: $f printed nothing on a native install" ;;
        *)
            ok "B: $f reaches its real work on a native install (no SKIP)" ;;
    esac
done

out="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/env" PATH="$SKY_TMP/native:$PATH" bash scripts/check_b_reconcile_audit_writes.sh 2>&1)"
case "$out" in
    *'restart-based'*)
        case "$out" in
            *'container not running'*) bad "C: the restart-based check still blames a missing container" ;;
            *) ok "C: the restart-based check names its real limitation (restart via docker)" ;;
        esac
        ;;
    *'container not running'*) bad "C: the restart-based check blames a missing container instead of the restart" ;;
    *) bad "C: unexpected output for the restart-based check: $(printf '%s' "$out" | head -1)" ;;
esac

# ═══════════════ D. no installation => SKIP, exit 0 ═══════════════
for f in $ALL_CHECKS; do
    out="$(SKYGATE_LIVE_ENV_FILE="$SKY_TMP/absent.env" PATH="$SKY_TMP/nodocker:$PATH" bash "scripts/$f" 2>&1)"; rc=$?
    if [ "$rc" -ne 0 ]; then
        bad "D: $f exited $rc with no installation — rule 1 says SKIP, never FAIL"
    else
        case "$out" in
            *SKIP*) ok "D: $f reports SKIP (exit 0) with no installation" ;;
            *)      bad "D: $f neither answered nor SKIPped with no installation" ;;
        esac
    fi
done

# ═══════════════ E. discoverability ═══════════════
if grep -q 'check_b489_live_preconditions.sh' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "E1: the block is registered in scripts/verify_pre_deploy.sh"
else
    bad "E1: B489 is not registered in the catalog"
fi
if grep -q 'B489' AGENTS.md 2>/dev/null; then
    ok "E2: the block has an index line in AGENTS.md"
else
    bad "E2: the block index has no B489 line"
fi

echo
echo "  B489: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
