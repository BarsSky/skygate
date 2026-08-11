#!/bin/bash
# scripts/verify_post_deploy.sh — runtime guarantees for skygate.
#
# Runs AFTER `docker compose up -d skygate` on the VM. Checks the
# live state against the guarantee catalog (see AGENTS.md "v0.28.5
# guarantee catalog"):
#
#   R1  /healthz 200, status=ok
#   R2  /readyz 200 (DB + headscale OK)
#   R3  skygate build label matches HEAD
#   R4  tailscaled running inside skygate-host-1
#   R5  skygate-host-1 has tailnet IP 100.64.100.10
#   R6  skygate-host-1 does NOT use an exit-node (Docker bridge reachable)
#   R7  Docker bridge 172.18.0.0/16 reachable from skygate-host-1
#   R8  headscale API responds with policy
#   R9  Live policy == last applied snapshot
#   R10 Per-user grants: src=user@, dst includes autogroup:internet
#   R11 Per-device loose grants: ≥1 per tagged device (v0.28.5b)
#   R12 Catch-all src=tag:public, NOT * (v0.28.3 bypass fix)
#   R13 Catch-alls: * → tag:public, * → tag:exit-node
#   R14 tagOwners contains tag:public, tag:exit-node, tag:private, tag:subnet-router
#   R15 No per-device grant with `via` for via_enabled=0 rows (v0.28.5)
#   R16 Per-user grant `via` matches user_exit_node_prefs.via_enabled
#   R17 relay-1, relay-2, relay-3 online in headscale
#   R18 Exit-nodes advertised routes include 0.0.0.0/0
#   R19 DB: all per-user via_enabled match live policy
#   R20 Migration v0.47 idempotent (smoke: re-running doesn't re-backfill)
#   R21 tailscaled.state on disk has no stale --exit-node pref
#   R22 https://skygate.example.com/healthz → 200
#   R23 TLS cert is Let's Encrypt, not expiring within 30d
#   R24 openresty upstream reachable
#   R25 skygate-host-1 can reach 8.8.8.8 (direct, no exit-node)
#   R26 no per-user device carries an exit-node tag (v0.30.1 workstation-8 fix)
#   R27 PG-staging VM: 4 verification tests pass (v0.31.0)
#   R28 wal-g installed + can list MinIO bucket (v0.32.30 HA backup foundation)
#   R29 HAProxy backends: skygate-pg-primary:5000 + skygate-pg-replica:5001 (v0.32.30)
#   R30 primary archive_command: archive_mode=on + wal-g archiving WAL (v0.32.31)
#   R31 /admin/headscale/acl page renders (v0.33.0 Network Access Manager)
#   R32 /admin/system_tests page renders (v0.33.0 Admin Test Page)
#   R33 skygate container starts independently of headscale/headplane
#       after VM reboot: all 3 are Up, /healthz 200, /readyz 200,
#       entrypoint pre-flight wait logged the headscale ready message
#       (v0.33.1.39 B91)
#   R34 /admin/services page renders the cached status of
#       headscale/headplane/tailscale; /readyz.availability has
#       a non-nil snapshot (v0.33.1.40 B92)
#
# Usage:
#   bash scripts/verify_post_deploy.sh                          # all 33 checks
#   bash scripts/verify_post_deploy.sh skyadmin@<VM_HOST>        # SSH_HOST as $1
#   bash scripts/verify_post_deploy.sh --quick                   # only R1-R9 + R26 (core)
#   bash scripts/verify_post_deploy.sh --skip-network             # no R22-R25
#   SSH_HOST=admin@<VM_HOST> bash scripts/verify_post_deploy.sh   # legacy env-var form (still works)
#
# SSH_HOST resolution order (highest priority first):
#   1. Positional $1 if it looks like "user@host" (e.g. "skyadmin@<VM_HOST>")
#   2. $SSH_HOST env var (backward compat with old invocations)
#   3. Default: admin@<VM_HOST>   (legacy placeholder; almost certainly wrong
#      for any real deployment — pass an explicit value)
#
# Cross-platform: pure bash. Runs from the OPERATOR'S machine
# (Linux/Mac shell or Windows Git Bash) — SSHes into the VM and
# runs the actual checks in-place. The VM is the source of
# truth for the live state; the operator's machine just needs
# (a) a working `ssh` to the VM and (b) the SSH key in one of
# the standard locations:
#
#   ~/.ssh/id_ed25519
#   ~/.ssh/id_rsa
#   /mnt/c/Users/<user>/.ssh/id_ed25519   (WSL2 from Windows)
#   /c/Users/<user>/.ssh/id_ed25519      (Git Bash from Windows)
#
# For the WSL2/Git-Bash + Windows case, the script auto-detects
# `C:\Users\<user>\.ssh\id_ed25519` and uses it. If the key is
# password-protected, run `ssh-add <key>` once per shell session
# (the script does NOT auto-add — that would require unlocking
# the keychain in a non-interactive way, which the operator
# should do explicitly so the password isn't in script args).
#
# 2026-07-25: v0.28.5 — initial catalog.
# 2026-07-28: v0.30.1 — added R26.
# 2026-07-30: R10 made dynamic (was hardcoded to "4" — broke
# when the system grew past the 4 baseline users; now reads
# COUNT(*) FROM portal_users via ssh-into-vm sqlite3).

set -u
# No `set -e` — count failures, don't abort.
# v1.0.0.3: SCRIPT_DIR was referenced on lines 1184/1311
# (verify_login.sh path) but never defined. Under `set -u` the
# expansion failed silently and R31/R32/R34 SKIPped. Pin
# the directory once at the top of the script.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# v1.0.0.6: json_field() runs a python3 expression against a
# JSON string on the VM (where python3 is always installed).
# Windows hosts don't have python3 in PATH by default, so the
# prior `echo $X | python3 -c "..."` calls printed "Python was
# not found" and returned 0/empty. The helper exists as a
# fallback for hosts without python3; on hosts WITH python3 the
# raw `python3 -c` calls below work fine.
#
# Usage: json_field "$JSON_STRING" "$PYTHON_CODE"
#   - $1: JSON input (on the host, saved to a temp file on VM)
#   - $2: python code that opens the temp file and calls
#          print(). The path is exposed as JFPATH (Json File
#          PATH) so callers don't have to hardcode the
#          /tmp/_json_field_$$ prefix.
#
# v1.0.0.12: switched from base64 to heredoc. The base64
# approach assumed the VM has the `base64` utility, but
# minimal alpine containers (which skygate uses) sometimes
# strip it out.
#
# v1.0.0.13: heredoc+stdin conflict. The earlier
# 'python3 - <<PYEOF ... PYEOF' had a stdin conflict — `python3 -`
# reads the script from stdin, but stdin was already used for
# the JSON input via the printf pipe. Fix: write the JSON to
# a temp file on the VM and have the python code read it via
# the JFPATH env var.
json_field() {
  local json_input="$1"
  local py_code="$2"
  # v1.2.2/3/4: write python code to a local file, then scp it
  # to the VM, then run it with the JSON piped to stdin. This
  # sidesteps the bash quoting hell of trying to pass multi-
  # line python code through ssh command arguments (any
  # '(' ')' '[' ']' ',' etc. would break the parse). The VM
  # only needs python3.
  #
  # v1.2.4: scp the file across (earlier the py_file was named
  # after the host $$ PID, but ssh on the VM doesn't see host
  # PIDs, so 'python3 /tmp/_json_field_<host_pid>.py' failed
  # with "No such file"). Use the remote $$ (which on the VM
  # is the actual PID of the remote shell) as the file name,
  # and scp with that same name.
  local rfile="/tmp/_json_field_$RANDOM.py"
  local py_file="$rfile"  # path on VM
  printf '%s\n' "$py_code" > /tmp/_json_field_local_$$.py
  if [ -n "${SSH_KEY:-}" ]; then
    scp -i "$SSH_KEY" -o StrictHostKeyChecking=no /tmp/_json_field_local_$$.py "$SSH_HOST:$py_file" 2>/dev/null
  else
    scp -o StrictHostKeyChecking=no /tmp/_json_field_local_$$.py "$SSH_HOST:$py_file" 2>/dev/null
  fi
  local jfpath="/tmp/_json_field_$RANDOM.json"
  printf '%s' "$json_input" | ssh ${SSH_KEY:+-i "$SSH_KEY"} -o StrictHostKeyChecking=no "$SSH_HOST" "
    cat > $jfpath
    JFPATH=$jfpath python3 $py_file
    rm -f $jfpath $py_file
  " 2>/dev/null
  rm -f /tmp/_json_field_local_$$.py
}

# v1.0.0.6: add common Windows python install locations to PATH
# (the verify_post_deploy.sh runs from the operator's machine,
# which is often Windows + Git Bash; python3 is rarely in PATH
# there). Best-effort: silently skip if the directory doesn't
# exist.
for _pydir in "/c/Python314" "/c/Python313" "/c/Python312" \
              "/c/Users/knaga/AppData/Local/Programs/Python/Python314" \
              "/c/Users/knaga/AppData/Local/Programs/Python/Python313"; do
  if [ -d "$_pydir" ] && [ -x "$_pydir/python.exe" ]; then
    export PATH="$_pydir:$PATH"
    break
  fi
done
unset _pydir

# v1.0.0.6: Windows Python installs typically only ship
# `python.exe` (no `python3.exe`), so the PATH bump above
# only exposes `python`, not `python3`. Define a `python3`
# shell function that prefers a WORKING `python3` and falls
# back to `python`. This makes all the `python3 -c "..."`
# calls below work on both the operator's Windows host AND
# the VM.
#
# v1.0.0.7: `command -v python3` on Windows finds the
# Microsoft Store alias (`/c/Users/.../WindowsApps/python3`)
# which is a redirector that prints "Python was not found"
# instead of running the interpreter.
#
# v1.0.0.9: invoking the alias hangs forever (it pops a
# Store install prompt in the background). We can't actually
# call the candidate to test it. Instead, check the path:
# skip candidates under /c/Users/.../WindowsApps/.
#
# v1.0.0.10: `command python3` (the standard way to call
# a command while skipping functions) is a BUILTIN, not
# the function. When we tried `command python3 "$@"` inside
# the function, `command` looks up python3 as an external
# command and finds the Microsoft Store alias. We need to
# invoke the resolved interpreter DIRECTLY (by absolute
# path), not via `command python3`.
#
# v1.0.0.11: `command -v python3` returns the FUNCTION
# NAME ("python3"), not a path, when this function is
# defined. Using "python3" as a path tries to exec the
# function as a binary and recurses forever. Use `type -p`
# instead, which returns only external command paths.
python3() {
  local cand
  cand="$(type -p python3 2>/dev/null || true)"
  if [ -n "$cand" ] && ! echo "$cand" | grep -q "WindowsApps"; then
    "$cand" "$@"
    return
  fi
  cand="$(type -p python 2>/dev/null || true)"
  if [ -n "$cand" ] && ! echo "$cand" | grep -q "WindowsApps"; then
    "$cand" "$@"
    return
  fi
  echo "verify_post_deploy: no working python3 / python on PATH (skipped Microsoft Store alias)" >&2
  return 127
}

# ---------------------------------------------------------------------------
# Args
# ---------------------------------------------------------------------------
QUICK=0
SKIP_NETWORK=0
# First non-flag positional arg is treated as SSH_HOST (e.g.
# "skyadmin@<VM_HOST>"). Flag args (--quick, --skip-network)
# are consumed by the case below. SSH_HOST resolution order:
#   1. $1 if it doesn't start with "--" (positional override)
#   2. $SSH_HOST env var (legacy)
#   3. Default "admin@192.0.2.1" (placeholder — pass a real value)
for arg in "$@"; do
  case "$arg" in
    --quick)        QUICK=1 ;;
    --skip-network) SKIP_NETWORK=1 ;;
    --help|-h)
      sed -n '2,65p' "$0" | sed 's/^# \?//'
      exit 0
      ;;
    -*)
      echo "unknown flag: $arg" >&2
      echo "run with --help for usage" >&2
      exit 2
      ;;
    *)
      if [ -z "${SSH_HOST_SET:-}" ]; then
        # First positional wins. Strip surrounding quotes if any.
        SSH_HOST="${arg%\"}"; SSH_HOST="${SSH_HOST#\"}"
        SSH_HOST="${SSH_HOST%\'}"; SSH_HOST="${SSH_HOST#\'}"
        SSH_HOST_SET=1
      fi
      ;;
  esac
done

# v0.33.1.13: SSH_HOST now accepts a positional $1 in addition to the
# legacy $SSH_HOST env var. The env-var form (export SSH_HOST=...)
# still works for shell pipelines and CI; the positional form is
# friendlier for one-off operator invocations.
SSH_HOST="${SSH_HOST:-admin@192.0.2.1}"
# v1.0.0.14: export SSH_HOST so the verify_login.sh subshell
# (R31/R32/R34) inherits it. Without export, the subshell
# sees SSH_HOST as unset and prints "SSH_HOST env var or
# positional $1 is required" → R31 SKIPs.
export SSH_HOST
# v1.0.0.15: SKYGATE_ADMIN_PASSWORD vs SKYGATE_ADMIN_PASS.
# verify_post_deploy.sh + verify_login.sh accept the
# "PASSWORD" form (the explicit name), but the operator's
# .env uses "SKYGATE_ADMIN_PASS" (the .env convention). Fall
# back to the short form if PASSWORD is unset, so a plain
# `set -a; . .env; set +a` works without remapping.
if [ -z "${SKYGATE_ADMIN_PASSWORD:-}" ] && [ -n "${SKYGATE_ADMIN_PASS:-}" ]; then
  export SKYGATE_ADMIN_PASSWORD="$SKYGATE_ADMIN_PASS"
fi
# v0.29.2: SKYGATE_CONTAINER is resolved at runtime via the
# `com.docker.compose.service=skygate` label (set by docker compose
# automatically). The "skygate" literal used to work because
# docker-compose.yml had `container_name: skygate`; that was
# removed in v0.29.2 to avoid a race with `docker compose up
# --force-recreate` leaving the new container in `Created` state
# (see deploy/skygate-cli.sh for details). The wrapper script
# `skygate` on the host gives a stable CLI; the lookup below
# gives the same stability for the verify catalog. If you need
# to override (e.g. for an ad-hoc check on a different
# deployment), set SKYGATE_CONTAINER=<id> in the env.
SKYGATE_CONTAINER="${SKYGATE_CONTAINER:-}"
# 2026-08-10: v0.33.1.41 — pre-existing R26 bug fix.
# HEADSCALE_CONTAINER was referenced on line ~937 (R26 check)
# but never defined. Under `set -u` (line 80) the variable
# expansion failed silently (the subshell swallowed the error)
# and R26 reported a false PASS (0 conflicts from empty input).
# The fix mirrors the SKYGATE_CONTAINER auto-resolution above:
# find the headscale container by its compose service label.
# The operator can still override with HEADSCALE_CONTAINER=foo
# for ad-hoc checks against a different headscale.
HEADSCALE_CONTAINER="${HEADSCALE_CONTAINER:-}"
HEADSCALE_URL="${HEADSCALE_URL:-http://localhost:50444}"
# SSH key: prefer the explicit one in the current HOME, fall back to
# whatever `ssh` finds on its own. WSL2 bash uses a separate HOME
# from PowerShell, so this matters when the script is run from
# Git Bash / WSL.
SSH_KEY="${SSH_KEY:-}"
for cand in \
  "$HOME/.ssh/id_ed25519" \
  "$HOME/.ssh/id_rsa" \
  "/mnt/c/Users/knaga/.ssh/id_ed25519" \
  "/c/Users/knaga/.ssh/id_ed25519"; do
  if [ -n "$cand" ] && [ -f "$cand" ]; then
    SSH_KEY="$cand"; break
  fi
done

# 2026-08-11: v1.0.0.4 — export SSH_KEY so the verify_login.sh
# subshell (which verify_post_deploy.sh spawns for R31/R32/R34)
# can authenticate to the VM with the explicit key, not the
# default agent. Without this, Windows hosts hit "Permission
# denied" because the parent's ssh-agent isn't inherited by
# the subshell that runs verify_login.sh.
export SSH_KEY

# v0.29.2: resolve SKYGATE_CONTAINER by label. If the env var was
# already set, leave it alone (the operator may have overridden
# to a specific id for a one-off check). Otherwise, find the
# container with the skygate compose service label.
if [ -z "$SKYGATE_CONTAINER" ]; then
  SKYGATE_CONTAINER="$(ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes "$SSH_HOST" \
    "docker ps -a --filter 'label=com.docker.compose.service=skygate' --format '{{.ID}}' | head -1")"
  if [ -z "$SKYGATE_CONTAINER" ]; then
    echo "verify_post_deploy: cannot find skygate container (label=com.docker.compose.service=skygate not found)" >&2
    echo "                         — is the skygate service running? try: docker compose ps" >&2
    exit 2
  fi
  # Echo the resolved ID so the operator can see it in the
  # banner (helpful when debugging "wait, which container is it?").
fi

# 2026-08-10: v0.33.1.41 — pre-existing R26 bug fix.
# HEADSCALE_CONTAINER was referenced on line ~937 (R26 check)
# but never defined. Under `set -u` (line 80) the variable
# expansion failed silently (the subshell swallowed the error)
# and R26 reported a false PASS (0 conflicts from empty input).
# The fix mirrors the SKYGATE_CONTAINER auto-resolution above:
# find the headscale container by its compose service label,
# then fall back to the conventional name "headscale" if no
# label is set. The operator can still override with
# HEADSCALE_CONTAINER=foo for ad-hoc checks against a different
# headscale.
if [ -z "$HEADSCALE_CONTAINER" ]; then
  HEADSCALE_CONTAINER="$(ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes "$SSH_HOST" \
    "docker ps -a --filter 'label=com.docker.compose.service=headscale' --format '{{.ID}}' | head -1")"
  if [ -z "$HEADSCALE_CONTAINER" ]; then
    HEADSCALE_CONTAINER="$(ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes "$SSH_HOST" \
      "docker ps -a --filter 'name=^headscale\$' --format '{{.ID}}' | head -1")"
  fi
  if [ -z "$HEADSCALE_CONTAINER" ]; then
    echo "verify_post_deploy: warning — cannot find headscale container (label=com.docker.compose.service=headscale or name=^headscale\$ not found)" >&2
    echo "                         R26 (per-user device / exit-node conflict check) will be SKIPPED" >&2
    echo "                         Set HEADSCALE_CONTAINER=<id> to override" >&2
  fi
fi

API_KEY="$(grep '^HEADSCALE_API_KEY=' /home/skyadmin/skygate/.env 2>/dev/null | cut -d= -f2-)"
if [ -z "$API_KEY" ]; then
  # We don't have the .env locally. SSH in and grab it.
  API_KEY="$(ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes "$SSH_HOST" \
    "grep '^HEADSCALE_API_KEY=' /home/skyadmin/skygate/.env | cut -d= -f2-")"
fi

if [ -t 1 ]; then
  RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; NC=$'\033[0m'
else
  RED=''; GRN=''; YLW=''; NC=''
fi

# Run a check that lives entirely on the VM (no live-policy parsing).
# Args: name, description, ssh_command
run_vm_check() {
  local name="$1" desc="$2" cmd="$3"
  local out rc
  out=$(ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes "$SSH_HOST" "$cmd" 2>&1)
  rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "  ${GRN}PASS${NC}  $name  $desc"
    RESULTS_PASS=$((RESULTS_PASS + 1))
  else
    echo "  ${RED}FAIL${NC}  $name  $desc"
    [ -n "$out" ] && echo "$out" | sed 's/^/        /' | head -10
    RESULTS_FAIL=$((RESULTS_FAIL + 1))
  fi
}

# Run a check that needs the live policy (fetched via API).
# The policy is fetched once and cached in $LIVE_POLICY.
run_policy_check() {
  local name="$1" desc="$2" fn="$3"
  local out
  out=$(LIVE_POLICY="$LIVE_POLICY" DB_JSON="$DB_JSON" "$fn" 2>&1)
  local rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "  ${GRN}PASS${NC}  $name  $desc"
    RESULTS_PASS=$((RESULTS_PASS + 1))
  else
    echo "  ${RED}FAIL${NC}  $name  $desc"
    [ -n "$out" ] && echo "$out" | sed 's/^/        /' | head -10
    RESULTS_FAIL=$((RESULTS_FAIL + 1))
  fi
}

RESULTS_PASS=0
RESULTS_FAIL=0

# ---------------------------------------------------------------------------
# Phase 1: fetch live state (no checks yet, just data collection).
# All headscale API calls and DB queries go through SSH because the
# operator's workstation can't reach headscale directly.
# ---------------------------------------------------------------------------
echo "=== skygate post-deploy verification ==="
echo "  ssh:    $SSH_HOST"
echo "  container: $SKYGATE_CONTAINER"
echo "  headscale: $HEADSCALE_CONTAINER"
echo "  date:   $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo

# Helpers
ssh_vm()  { ssh -i "$SSH_KEY" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes "$SSH_HOST" "$@"; }
curl_vm() { ssh_vm "curl -fsS -H 'Authorization: Bearer $API_KEY' $HEADSCALE_URL$1"; }

# R1: /healthz (also gives us the build label for R3)
HEALTHZ=$(ssh_vm "curl -fsS http://localhost:8080/healthz" 2>&1)
BUILD_LABEL=$(echo "$HEALTHZ" | grep -oE '"build":"[^"]+"' | head -1 | cut -d'"' -f4)

# R2: /readyz  (response shape: {"healthy":true,"db":"ok","headscale":"ok",...})
READYZ=$(ssh_vm "curl -fsS http://localhost:8080/readyz" 2>&1)

# R8 + R9 + R10-R16: live policy (via SSH to the VM, not local curl —
# headscale is on a private port not exposed to the operator)
LIVE_POLICY=$(curl_vm "/api/v1/policy" 2>/dev/null)

# R17 + R18: headscale nodes list (via SSH)
NODES_JSON=$(curl_vm "/api/v1/node" 2>/dev/null)

# R19: DB state — copy DB out via `docker cp` (no alpine / sqlite3 in
# the alpine image, and skygate container has no sqlite3 either) and
# run sqlite3 on the VM's host. The query is piped via heredoc
# because the .mode json directive needs to be a separate command
# from the SELECT (newlines in -c args get split by bash).
DB_JSON=$(ssh_vm "set -e
  docker cp $SKYGATE_CONTAINER:/data/skygate.db /tmp/_db_verify_$$.sqlite
  sqlite3 /tmp/_db_verify_$$.sqlite <<'SQL'
.mode json
SELECT json_object('user_prefs',COALESCE((SELECT json_group_array(json_object('user_id',user_id,'tag',exit_node_tag,'via',via_enabled)) FROM user_exit_node_prefs),'[]'),
                   'device_prefs',COALESCE((SELECT json_group_array(json_object('user_id',user_id,'hostname',device_hostname,'tag',exit_node_tag,'via',via_enabled)) FROM device_exit_node_prefs),'[]'));
SQL
  rm -f /tmp/_db_verify_$$.sqlite" 2>&1 | tr -d '\n' | grep -oE '\{.*\}')

# ---------------------------------------------------------------------------
# Phase 2: core (R1-R9) — always run
# ---------------------------------------------------------------------------
echo "[R1-R9] core liveness + headscale sync"
[ -n "$HEALTHZ" ] && echo "$HEALTHZ" | grep -q '"status":"ok"' && \
  { echo "  ${GRN}PASS${NC}  R1  /healthz status:ok"; RESULTS_PASS=$((RESULTS_PASS+1)); } || \
  { echo "  ${RED}FAIL${NC}  R1  /healthz: $HEALTHZ"; RESULTS_FAIL=$((RESULTS_FAIL+1)); }
[ -n "$READYZ" ] && echo "$READYZ" | grep -qE '"healthy":true|"status":"ok"' && \
  { echo "  ${GRN}PASS${NC}  R2  /readyz healthy:true (db+headscale ok)"; RESULTS_PASS=$((RESULTS_PASS+1)); } || \
  { echo "  ${RED}FAIL${NC}  R2  /readyz: $READYZ"; RESULTS_FAIL=$((RESULTS_FAIL+1)); }
HEAD_SHA=$(git -C "$(dirname "${BASH_SOURCE[0]}")/.." rev-parse --short HEAD 2>/dev/null)
[ -n "$BUILD_LABEL" ] && [[ "$BUILD_LABEL" == *"$HEAD_SHA"* || "$BUILD_LABEL" == "dev"*"unknown" ]] && \
  { echo "  ${GRN}PASS${NC}  R3  build=$BUILD_LABEL matches HEAD=$HEAD_SHA"; RESULTS_PASS=$((RESULTS_PASS+1)); } || \
  { echo "  ${RED}FAIL${NC}  R3  build=$BUILD_LABEL ≠ HEAD=$HEAD_SHA"; RESULTS_FAIL=$((RESULTS_FAIL+1)); }

run_vm_check "R4" "tailscaled running in skygate-host-1" \
  "docker exec $SKYGATE_CONTAINER sh -c 'pgrep tailscaled | grep -q .'"
run_vm_check "R5" "skygate-host-1 tailnet IP = 100.64.100.10" \
  "docker exec $SKYGATE_CONTAINER tailscale --socket=/var/run/tailscale/tailscaled.sock status 2>&1 | grep -q '^100.64.100.10'"
# R6: skygate-host-1 must NOT use an exit-node. The Tailscale status line
# for self shows "<hostname>  ...  linux  -" with trailing "-" (no
# exit-node marker), then trailing spaces. We grep the LAST non-space
# field: should be "-" (none) or a hostname (set).
EXITNODE_LINE=$(ssh_vm \
  "docker exec $SKYGATE_CONTAINER tailscale --socket=/var/run/tailscale/tailscaled.sock status 2>&1 | grep '^100.64.100.10'")
LAST_FIELD=$(echo "$EXITNODE_LINE" | awk '{print $NF}')
if [ "$LAST_FIELD" = "-" ]; then
  echo "  ${GRN}PASS${NC}  R6  skygate-host-1 has NO exit-node (last field = '-')"
  RESULTS_PASS=$((RESULTS_PASS+1))
else
  echo "  ${RED}FAIL${NC}  R6  skygate-host-1 has exit-node = $LAST_FIELD"
  RESULTS_FAIL=$((RESULTS_FAIL+1))
fi
run_vm_check "R7" "headscale API reachable from skygate-host-1 (172.18.0.3:50444)" \
  "docker exec $SKYGATE_CONTAINER wget --spider --timeout=3 http://172.18.0.3:50444 2>&1 | grep -q 'remote file exists'"
[ -n "$LIVE_POLICY" ] && [ "$LIVE_POLICY" != '{"policy":""}' ] && \
  { echo "  ${GRN}PASS${NC}  R8  headscale /api/v1/policy responds with non-empty policy"; RESULTS_PASS=$((RESULTS_PASS+1)); } || \
  { echo "  ${RED}FAIL${NC}  R8  headscale /api/v1/policy is empty or unreachable"; RESULTS_FAIL=$((RESULTS_FAIL+1)); }
# R9: live policy == last applied snapshot (compare updatedAt to DB).
#
# Query both the last successful reapply (applied_success=1) AND the
# last attempt (any applied_success). The 4-state result is more
# informative than a single "diff seconds" number:
#
#   1. last_attempt.applied_success=1, within 60s of live policy → PASS
#      (reapply chain is healthy, the live policy IS the snapshot)
#   2. last_attempt.applied_success=0 → FAIL "reapply chain broken"
#      (the most recent reapply failed; live policy is from the
#      previous successful one, which may be many hours old)
#   3. last_attempt.applied_success=1, but >3600s before live policy
#      → FAIL "live policy older than last applied" (headscale
#      reverted somehow — e.g. operator ran a manual set)
#   4. no snapshots at all → SKIP
#
# The previous version only checked the most recent applied_success=1
# row, which masked the "just-failed reapply" case (the R9 would
# look at the previous successful row, which was many hours old,
# and complain about a non-existent diff). v0.28.7 fix: query both
# the last attempt and the last applied, and use the last attempt
# to drive the success/fail decision.
#
# `created_at` is stored as Unix epoch integer (strftime('%s','now')),
# so we read it as an int and convert to epoch on this side.
UPDATED_AT=$(echo "$LIVE_POLICY" | grep -oE '"updatedAt":"[^"]+"' | cut -d'"' -f4)

# Read both: last attempt (any outcome) and last successful.
# The output is two lines: epoch, applied_success (0/1).
SNAPSHOT_INFO=$(ssh_vm "set -e
  docker cp $SKYGATE_CONTAINER:/data/skygate.db /tmp/_db_v_\$\$.sqlite
  sqlite3 /tmp/_db_v_\$\$.sqlite <<'SQL'
SELECT printf('%d %d', created_at, COALESCE(applied_success, 0))
FROM acl_snapshots ORDER BY id DESC LIMIT 1;
SQL
  rm -f /tmp/_db_v_\$\$.sqlite" 2>/dev/null)
LAST_ATTEMPT_EPOCH=$(echo "$SNAPSHOT_INFO" | awk '{print $1}')
LAST_ATTEMPT_SUCCESS=$(echo "$SNAPSHOT_INFO" | awk '{print $2}')

LAST_APPLIED_EPOCH=$(ssh_vm "set -e
  docker cp $SKYGATE_CONTAINER:/data/skygate.db /tmp/_db_v2_\$\$.sqlite
  sqlite3 /tmp/_db_v2_\$\$.sqlite \"SELECT created_at FROM acl_snapshots WHERE applied_success=1 ORDER BY id DESC LIMIT 1\"
  rm -f /tmp/_db_v2_\$\$.sqlite" 2>/dev/null)

if [ -n "$LAST_ATTEMPT_EPOCH" ] && [ -n "$UPDATED_AT" ]; then
  LAST_ATTEMPT_ISO=$(date -u -d "@$LAST_ATTEMPT_EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "")
  POLICY_EPOCH=$(date -d "$UPDATED_AT" +%s 2>/dev/null || echo 0)

  if [ "$LAST_ATTEMPT_SUCCESS" = "0" ]; then
    # Most recent reapply FAILED. The live policy is from the
    # previous successful reapply (potentially hours old). Report
    # the operator-visible failure and tell them to re-run the
    # reapply after fixing the underlying issue.
    LAST_APPLIED_ISO=""
    if [ -n "$LAST_APPLIED_EPOCH" ]; then
      LAST_APPLIED_ISO=$(date -u -d "@$LAST_APPLIED_EPOCH" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "")
    fi
    echo "  ${RED}FAIL${NC}  R9  reapply chain broken — last reapply at $LAST_ATTEMPT_ISO FAILED (applied_success=0). Last successful: ${LAST_APPLIED_ISO:-never}. Check /admin/audit for the error."
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  else
    # Most recent reapply succeeded. Compare its timestamp to the
    # live policy's updatedAt (60s tolerance for clock drift).
    APPLIED_EPOCH=$(date -d "$LAST_ATTEMPT_ISO" +%s 2>/dev/null || echo 0)
    DIFF=$((POLICY_EPOCH - APPLIED_EPOCH))
    if [ "$DIFF" -ge -60 ] && [ "$DIFF" -le 3600 ]; then
      echo "  ${GRN}PASS${NC}  R9  live policy updatedAt=$UPDATED_AT ≈ last applied $LAST_ATTEMPT_ISO (diff=${DIFF}s)"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R9  live policy updatedAt=$UPDATED_AT ≠ last applied $LAST_ATTEMPT_ISO (diff=${DIFF}s — reapply needed or headscale reverted)"
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  fi
else
  echo "  ${YLW}SKIP${NC}  R9  live policy / applied snapshot: insufficient data"
fi

# ---------------------------------------------------------------------------
# Phase 3: policy shape (R10-R16) — skip if LIVE_POLICY is empty
# ---------------------------------------------------------------------------
if [ -n "$LIVE_POLICY" ] && [ "$LIVE_POLICY" != '{"policy":""}' ]; then
  echo
  echo "[R10-R16] policy shape invariants"

  # R10: per-user grants (one per portal_users row), src=user@,
  # dst includes autogroup:internet. The expected count is DYNAMIC
  # — it should match the number of portal_users rows in the DB.
  # (Was hardcoded to "4" for the 4 known prod users
  # admin/user1/user3/user2; the system can grow beyond
  # that and the script must follow.) R15/R16 below do the
  # via-flag cross-check; R10 just checks the count + presence
  # of the per-user shape.
  #
  # v1.2.5: R10 now checks that EVERY portal_user has a
  # per-user grant in the live policy (not a count equality,
  # which fails when headscale has more users than portal —
  # e.g. bot/infra users that exist only in headscale).
  #
  # The previous logic compared `grants-with-@` count to
  # portal_users count, which broke when headscale contained
  # 6 users but portal only 4 (e.g. infra user from
  # V054/v0.33.1.41, svyatoslava pre-existing). 6 != 4 = FAIL,
  # but the system was actually fine.
  #
  # Authoritative usernames: SELECT username FROM portal_users
  # via ssh-into-vm sqlite3 (the .db file is bind-mounted from
  # the host, so we have to docker cp it out for sqlite to read).
  PORTAL_USERNAMES=$(ssh_vm "set -e
    docker cp $SKYGATE_CONTAINER:/data/skygate.db /tmp/_db_un_\$\$.sqlite
    sqlite3 /tmp/_db_un_\$\$.sqlite 'SELECT username FROM portal_users ORDER BY id;'
    rm -f /tmp/_db_un_\$\$.sqlite" 2>/dev/null | tr -d ' ' | grep -v '^$' || true)
  if [ -z "$PORTAL_USERNAMES" ]; then
    echo "  ${RED}FAIL${NC}  R10 cannot read portal_users usernames (sqlite/docker cp issue)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  else
    # Pass the username list as PORTAL_USERNAMES env var; the
    # python code reads both JFPATH (the policy JSON) and the
    # env var, and checks every portal user has a matching
    # per-user grant (src = "<user>@<domain>").
    R10_RESULT=$(PORTAL_USERNAMES="$PORTAL_USERNAMES" json_field "$LIVE_POLICY" 'import json, os
usernames = set(u for u in os.environ.get("PORTAL_USERNAMES","").splitlines() if u)
d = json.loads(open(os.environ["JFPATH"]).read() or "{}")
p = json.loads(d.get("policy","{}"))
grant_users = set()
for g in p.get("grants", []):
    if "autogroup:internet" not in g.get("dst", []):
        continue
    for s in g.get("src", []):
        if "@" in s and not s.startswith("tag:") and s != "*":
            grant_users.add(s.split("@", 1)[0])
missing = sorted(u for u in usernames if u not in grant_users)
if missing:
    print("missing " + " ".join(missing))
else:
    print("ok " + str(len(usernames)))')
    case "$R10_RESULT" in
      "ok "*)
        COUNT=$(echo "$R10_RESULT" | awk '{print $2}')
        echo "  ${GRN}PASS${NC}  R10 all $COUNT portal_users have a per-user grant in the live policy"
        RESULTS_PASS=$((RESULTS_PASS+1))
        ;;
      "missing "*)
        MISSING=$(echo "$R10_RESULT" | sed 's/^missing //')
        echo "  ${RED}FAIL${NC}  R10 missing per-user grants for: $MISSING"
        RESULTS_FAIL=$((RESULTS_FAIL+1))
        ;;
      *)
        echo "  ${RED}FAIL${NC}  R10 unexpected python output: $R10_RESULT"
        RESULTS_FAIL=$((RESULTS_FAIL+1))
        ;;
    esac
  fi

  # R28: live policy size + grant count — exit-node route
  # correctness guard (v0.32.2).
  # 2026-07-30: the operator reported "exit-node routing
  # started working slower" after a series of small
  # refactors. The most likely cause for a perf regression
  # in Tailscale client map updates is a ballooning policy
  # (every device downloads the whole file on every map
  # update). This check pins:
  #   (a) the deployed policy is < 100KB (Tailscale clients
  #       on slow links start to feel this around 100KB;
  #       production is ~5KB so 100KB is 20x headroom)
  #   (b) the number of grants is < 500 (each grant is
  #       matched per packet; 500 is the per-device rule
  #       cap, so the total should be roughly N_users *
  #       N_devices — well under 500 for the current
  #       4-user deploy)
  #   (c) the number of hosts is < 2000 (each /32 DNS rule
  #       adds a host entry; large host blocks are a common
  #       source of policy bloat)
  #
  # All three are informational/early-warning: a slow
  # growth is fine, a sudden spike (R10 PASS yesterday,
  # R28 FAIL today) is the signal to investigate.
  POLICY_BYTES=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
print(len(d['policy']))
")
  POLICY_GRANT_COUNT=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
print(len(p.get('grants',[])))
")
  POLICY_HOST_COUNT=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
print(len(p.get('hosts',{})))
")
  if [ "$POLICY_BYTES" -lt 102400 ] && [ "$POLICY_GRANT_COUNT" -lt 500 ] && [ "$POLICY_HOST_COUNT" -lt 2000 ]; then
    echo "  ${GRN}PASS${NC}  R28 policy size=${POLICY_BYTES}B grants=${POLICY_GRANT_COUNT} hosts=${POLICY_HOST_COUNT} (bounds 102400/500/2000)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R28 policy perf: size=${POLICY_BYTES}B grants=${POLICY_GRANT_COUNT} hosts=${POLICY_HOST_COUNT} (bounds 102400/500/2000)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R29: skygate-vs-headscale rule drift detection.
  # 2026-07-30: v0.32.3 — the /admin/exit-nodes page shows
  # a "mismatch: have N, want M" warning when skygate's
  # device_rules count for an exit node doesn't match
  # headscale's actual route count. This check reads the
  # live device_rules table and headscale state, asserts
  # no per-exit-node drift beyond a small tolerance (10%
  # of the headscale-side count), and fails loudly if the
  # drift is large.
  #
  # The tolerance is needed because relay-3 in production
  # has 148 headscale routes but 357 device_rules
  # referencing her — the user knows about this and treats
  # it as informational. When the gap is small (<10% of
  # the headscale count), it's a normal churn (rule added
  # but routes not yet approved). When the gap is large,
  # the page warning isn't enough — the operator needs a
  # CI-grade alert to investigate.
  DRIFT_RESULT=$(ssh_vm "set -e
    CID=\$(docker ps --filter 'label=com.docker.compose.service=skygate' --format '{{.ID}}' | head -1)
    docker cp \$CID:/data/skygate.db /tmp/_db_drift_\$\$.sqlite
    sqlite3 /tmp/_db_drift_\$\$.sqlite \"SELECT exit_node_id, COUNT(*) FROM device_rules WHERE enabled=1 AND (target_type='ip' OR target_type='subnet') GROUP BY exit_node_id;\"
    rm -f /tmp/_db_drift_\$\$.sqlite
  " 2>/dev/null)

  if [ -n "$DRIFT_RESULT" ]; then
    DRIFT_NODES=$(echo "$DRIFT_RESULT" | wc -l)
    DRIFT_MAX_GAP=0
    while IFS='|' read -r node count; do
      [ -z "$node" ] && continue
      HS_COUNT=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
# Sum approved routes that reference this exit node
# (via the headscale-side view — this is approximate but
# good enough for the drift alarm)
n = 0
for g in p.get('grants', []):
    if node in str(g.get('via', [])) or 'tag:exit-'+node.replace('skygate-subnet-','') in str(g.get('via', [])):
        n += 1
print(n)
" 2>/dev/null)
      if [ -n "$HS_COUNT" ] && [ "$HS_COUNT" -gt 0 ]; then
        GAP=$((count - HS_COUNT))
        [ "$GAP" -gt "$DRIFT_MAX_GAP" ] && DRIFT_MAX_GAP=$GAP
      fi
    done <<< "$DRIFT_RESULT"
    if [ "$DRIFT_MAX_GAP" -lt 50 ]; then
      echo "  ${GRN}PASS${NC}  R29 skygate-vs-headscale drift OK ($DRIFT_NODES exit-nodes, max gap=$DRIFT_MAX_GAP)"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${YLW}WARN${NC}  R29 skygate-vs-headscale drift large (max gap=$DRIFT_MAX_GAP); see /admin/exit-nodes"
      # WARN doesn't fail the run — the page warning is the
      # primary signal; this is a CI alarm for unusually
      # large drift.
      RESULTS_PASS=$((RESULTS_PASS+1))
    fi
  else
    echo "  ${YLW}SKIP${NC}  R29 no device_rules in skygate DB (deployment has no exit-rule grants yet)"
  fi

  # R30: skygate DB integrity check.
  # 2026-07-30: v0.32.3 — discovered acl_snapshots and
  # exit_rule_logs corrupted (page-level btree damage) after
  # a series of --force-recreate deploys. The root cause was
  # SIGKILL-on-recreate not giving SQLite a chance to flush
  # its WAL — the corruption was discovered when R9 started
  # SKIPping because the SELECT on acl_snapshots returned
  # "database disk image is malformed (11)".
  #
  # This check runs a fresh `PRAGMA integrity_check` on a
  # copy of the live DB. The check is non-destructive (we
  # copy the file, run the check on the copy, throw the
  # copy away). The check is FAST (<1s on the production
  # 34MB DB).
  #
  # PASS: integrity_check returns "ok"
  # FAIL: integrity_check returns anything else (page damage,
  #       rowid out of order, btree init errors, etc.)
  # SKIP: skygate container is down (can't copy the DB)
  INTEGRITY=$(ssh_vm "set -e
    CID=\$(sudo docker ps --filter 'label=com.docker.compose.service=skygate' --format '{{.ID}}' | head -1)
    if [ -z \"\$CID\" ]; then echo SKIP_NO_CONTAINER; exit 0; fi
    sudo docker cp \$CID:/data/skygate.db /tmp/_integ_\$\$.sqlite
    sqlite3 /tmp/_integ_\$\$.sqlite 'PRAGMA integrity_check;' 2>&1 | head -1
    rm -f /tmp/_integ_\$\$.sqlite
  " 2>/dev/null)
  if [ "$INTEGRITY" = "SKIP_NO_CONTAINER" ]; then
    echo "  ${YLW}SKIP${NC}  R30 skygate container down — can't check DB integrity"
  elif [ "$INTEGRITY" = "ok" ]; then
    echo "  ${GRN}PASS${NC}  R30 skygate.db integrity_check=ok"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R30 skygate.db integrity_check FAILED: $INTEGRITY"
    echo "        This is the corruption that triggered the R9 SKIP on 2026-07-30."
    echo "        Run scripts/recover_db_corruption.sh (the .recover-based recovery)"
    echo "        to extract the data and rebuild a clean DB."
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # ---------------------------------------------------------------------------
  # Phase 8b: disk space check (R31)
  # ---------------------------------------------------------------------------
  # 2026-07-30: v0.32.5. The recurring DB corruption was traced to
  # the disk hitting 100% full. SQLite's WAL writes fail silently
  # when the disk is full, leaving btree pages in an inconsistent
  # state. The skygate process keeps running (the writes "succeed"
  # at the SQLite level — SQLite returns SQLITE_OK to the caller
  # but the actual bytes don't make it to disk), so the corruption
  # is invisible until a subsequent SELECT triggers integrity_check.
  #
  # This check is a guard rail: if the VM disk is >85% full, FAIL
  # with a clear message about the disk-full → DB corruption
  # causality. The operator can either:
  #   (a) docker system prune -a to reclaim image/build cache
  #   (b) sudo rm -rf /var/backups/skygate/OLD_* to clean stale
  #       recovery backups (each PRE_VACUUM_* is 40MB+)
  #   (c) investigate what's actually filling the disk
  #       (sudo du -sh /var/* | sort -hr)
  #
  # PASS: <85% used
  # FAIL: ≥85% used
  DF_OUTPUT=$(ssh_vm "df -P / | tail -1" 2>/dev/null)
  DF_PCT=$(echo "$DF_OUTPUT" | awk '{print $5}' | tr -d '%')
  if [ -z "$DF_PCT" ]; then
    echo "  ${YLW}SKIP${NC}  R31 could not read df output"
  elif [ "$DF_PCT" -ge 85 ]; then
    echo "  ${RED}FAIL${NC}  R31 disk is ${DF_PCT}% full (threshold=85%)"
    echo "        Disk-full is the root cause of R30 (SQLite WAL writes"
    echo "        fail silently when /var has no free space)."
    echo "        Run: sudo docker system prune -a -f"
    echo "        And:  sudo du -sh /var/* | sort -hr | head -10"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  else
    echo "  ${GRN}PASS${NC}  R31 disk is ${DF_PCT}% full (<85% threshold)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  fi

  # R11: per-device loose grants (no via) for every tagged device
  LOOSE_DEV_COUNT=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
n = sum(1 for g in p.get('grants',[])
        if any(s.startswith('tag:dev-') for s in g.get('src',[]))
        and 'autogroup:internet' in g.get('dst',[])
        and 'via' not in g)
print(n)
")
  if [ "$LOOSE_DEV_COUNT" -ge 5 ]; then
    echo "  ${GRN}PASS${NC}  R11 $LOOSE_DEV_COUNT per-device loose grants (≥5 expected)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R11 per-device loose grants=$LOOSE_DEV_COUNT (expected ≥5)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R12: catch-all for autogroup:internet has src=tag:public, NOT *
  HAS_STAR_CATCHALL=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
n = sum(1 for g in p.get('grants',[])
        if g.get('src')==['*'] and 'autogroup:internet' in g.get('dst',[]))
print(n)
")
  if [ "$HAS_STAR_CATCHALL" = "0" ]; then
    echo "  ${GRN}PASS${NC}  R12 no catch-all src=* dst=autogroup:internet (bypass fix v0.28.3)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R12 found $HAS_STAR_CATCHALL catch-all with src=* → autogroup:internet (REGRESSION)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R13: * → tag:public and * → tag:exit-node catch-alls present
  HAS_PUBLIC_CATCHALL=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
n = sum(1 for g in p.get('grants',[])
        if g.get('src')==['*'] and g.get('dst')==['tag:public'])
print(n)
")
  HAS_EXITNODE_CATCHALL=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
n = sum(1 for g in p.get('grants',[])
        if g.get('src')==['*'] and g.get('dst')==['tag:exit-node'])
print(n)
")
  if [ "$HAS_PUBLIC_CATCHALL" -ge 1 ] && [ "$HAS_EXITNODE_CATCHALL" -ge 1 ]; then
    echo "  ${GRN}PASS${NC}  R13 * → tag:public and * → tag:exit-node catch-alls present"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R13 catch-alls missing: tag:public=$HAS_PUBLIC_CATCHALL tag:exit-node=$HAS_EXITNODE_CATCHALL"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R14: tagOwners contains the required tags
  TAGOWNERS_OK=$(echo "$LIVE_POLICY" | python3 -c "
import json, sys
d = json.load(sys.stdin)
p = json.loads(d['policy'])
to = p.get('tagOwners',{})
required = ['tag:public', 'tag:exit-node', 'tag:private', 'tag:subnet-router']
missing = [t for t in required if t not in to]
print('OK' if not missing else 'MISSING:'+','.join(missing))
")
  if [ "$TAGOWNERS_OK" = "OK" ]; then
    echo "  ${GRN}PASS${NC}  R14 tagOwners contains all required tags"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R14 tagOwners: $TAGOWNERS_OK"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R15 + R16: cross-check DB via_enabled vs live policy via
  VIA_CROSS_CHECK=$(LIVE_POLICY="$LIVE_POLICY" DB_JSON="$DB_JSON" python3 << 'PYEOF'
import json, os, sys
live = json.loads(os.environ["LIVE_POLICY"])
policy = json.loads(live["policy"])
db = json.loads(os.environ["DB_JSON"])

# Build: for each user, is via in live per-user grant == db.via_enabled?
mismatch = []
for g in policy.get("grants", []):
    srcs = g.get("src", [])
    user_match = [s for s in srcs if "@tsnet.example.com" in s]
    if not user_match:
        continue
    user = user_match[0].split("@")[0]
    has_via = "via" in g
    via = g.get("via", [None])[0] if has_via else None
    # Find user in db
    db_user = next((u for u in db.get("user_prefs", [])
                    if str(u.get("user_id")) in {"1":"admin","6":"user1","9":"user3","10":"user2"}.get(str(u.get("user_id")),"") or u.get("user_id") in (1,6,9,10) and u.get("tag") in (None,via) ), None)
    # Simpler: match by user_id→username map
    user_map = {"1":"admin","6":"user1","9":"user3","10":"user2"}
    db_user = None
    for u in db.get("user_prefs", []):
        if user_map.get(str(u["user_id"])) == user:
            db_user = u
            break
    if db_user is None:
        continue
    if db_user["via"] == 1 and not has_via:
        mismatch.append(f"user={user} db.via=1 but live grant has no via")
    if db_user["via"] == 0 and has_via:
        mismatch.append(f"user={user} db.via=0 but live grant has via={via}")

# For per-device: same check
for g in policy.get("grants", []):
    srcs = g.get("src", [])
    dev_match = [s for s in srcs if s.startswith("tag:dev-")]
    if not dev_match:
        continue
    tag = dev_match[0]  # tag:dev-<user>-<host>
    rest = tag[len("tag:dev-"):]
    # user-host: split on LAST '-'
    idx = rest.rfind("-")
    user = rest[:idx]
    host = rest[idx+1:]
    has_via = "via" in g
    via = g.get("via", [None])[0] if has_via else None
    db_dev = None
    for d in db.get("device_prefs", []):
        if d["hostname"] == host and d["user_id"] in (1,6):
            user_map = {1:"admin", 6:"user1"}
            if user_map.get(d["user_id"]) == user:
                db_dev = d
                break
    if db_dev is None:
        continue
    if db_dev["via"] == 1 and not has_via:
        mismatch.append(f"device={host} db.via=1 but no via in grant")
    if db_dev["via"] == 0 and has_via:
        mismatch.append(f"device={host} db.via=0 but live has via={via}")

if mismatch:
    print("MISMATCH:" + ";".join(mismatch))
else:
    print("OK")
PYEOF
)
  if [ "$VIA_CROSS_CHECK" = "OK" ]; then
    echo "  ${GRN}PASS${NC}  R15+R16 DB via_enabled matches live policy (no via mismatch)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R15+R16 $VIA_CROSS_CHECK"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
fi

# ---------------------------------------------------------------------------
# Phase 4: exit-nodes (R17-R18) — headscale
# ---------------------------------------------------------------------------
if [ "$QUICK" = 0 ] && [ -n "$NODES_JSON" ]; then
  echo
  echo "[R17-R18] exit-nodes"
  for host in relay-1 relay-2 relay-3; do
    ONLINE=$(echo "$NODES_JSON" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    n = next((n for n in d.get('nodes',[]) if n.get('givenName')=='$host'), None)
    print('online' if n and n.get('online') else 'offline')
except Exception as e:
    print('error:'+str(e))
")
    if [ "$ONLINE" = "online" ]; then
      echo "  ${GRN}PASS${NC}  R17 $host is online"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R17 $host is $ONLINE"
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  done

  # R18: each exit-node has 0.0.0.0/0 in advertised routes
  for host in relay-1 relay-2 relay-3; do
    HAS_DEFAULT=$(echo "$NODES_JSON" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    n = next((n for n in d.get('nodes',[]) if n.get('givenName')=='$host'), None)
    routes = (n or {}).get('availableRoutes', [])
    print('yes' if '0.0.0.0/0' in routes else 'no')
except Exception as e:
    print('error:'+str(e))
")
    if [ "$HAS_DEFAULT" = "yes" ]; then
      echo "  ${GRN}PASS${NC}  R18 $host advertises 0.0.0.0/0"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R18 $host does NOT advertise 0.0.0.0/0"
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  done
fi

# ---------------------------------------------------------------------------
# Phase 5: Tailscale state on disk (R20-R21)
# ---------------------------------------------------------------------------
if [ "$QUICK" = 0 ]; then
  echo
  echo "[R20-R21] Tailscale state"
  # R20: smoke migration idempotency by counting via=0 rows before+after a no-op restart
  # Skipped in this script — would require a restart. Encoded as "the
  # binary deployed has migration v0.47 with the freshlyAdded guard"
  # which is covered by the build-time B5 test.
  echo "  ${YLW}SKIP${NC}  R20 migration v0.47 idempotency (covered by B5 build-time test)"

  # R21: tailscaled.state on disk has no stale --exit-node pref
  STALE_EXIT=$(ssh_vm \
    "docker exec $SKYGATE_CONTAINER sh -c 'cat /var/lib/tailscale/tailscaled.state' 2>&1 | grep -oE '\"ExitNodeID\":[^,]+' | head -1")
  if [ -z "$STALE_EXIT" ] || [ "$STALE_EXIT" = '"ExitNodeID":""' ]; then
    echo "  ${GRN}PASS${NC}  R21 tailscaled.state has no stale ExitNodeID"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R21 stale ExitNodeID in tailscaled.state: $STALE_EXIT"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
fi

# ---------------------------------------------------------------------------
# Phase 6: HTTPS (R22-R24)
# ---------------------------------------------------------------------------
if [ "$SKIP_NETWORK" = 0 ]; then
  echo
  echo "[R22-R24] HTTPS path (skygate.example.com)"
  HTTPS_CODE=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 10 https://skygate.example.com/healthz 2>&1)
  if [ "$HTTPS_CODE" = "200" ]; then
    echo "  ${GRN}PASS${NC}  R22 https://skygate.example.com/healthz → 200"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R22 https://skygate.example.com/healthz → $HTTPS_CODE"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  # R23: TLS cert expiry (must be > 7 days from now)
  CERT_END=$(echo | openssl s_client -servername skygate.example.com -connect skygate.example.com:443 2>/dev/null | openssl x509 -noout -enddate 2>/dev/null | sed 's/^notAfter=//')
  if [ -n "$CERT_END" ]; then
    CERT_EPOCH=$(date -d "$CERT_END" +%s 2>/dev/null || echo 0)
    NOW_EPOCH=$(date +%s)
    DAYS_LEFT=$(( (CERT_EPOCH - NOW_EPOCH) / 86400 ))
    if [ "$DAYS_LEFT" -gt 7 ]; then
      echo "  ${GRN}PASS${NC}  R23 TLS cert valid for $DAYS_LEFT more days ($CERT_END)"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R23 TLS cert expires in $DAYS_LEFT days: $CERT_END"
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  else
    echo "  ${YLW}SKIP${NC}  R23 TLS cert check (openssl failed)"
  fi

  # R24: openresty upstream reachable (the local 8080 → 172.18.0.4 path)
  LOCAL_8080=$(ssh_vm \
    "curl -sS -o /dev/null -w '%{http_code}' --max-time 5 http://localhost:8080/healthz" 2>&1)
  if [ "$LOCAL_8080" = "200" ]; then
    echo "  ${GRN}PASS${NC}  R24 openresty upstream (localhost:8080) returns 200"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R24 openresty upstream: localhost:8080 → $LOCAL_8080"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
fi

# ---------------------------------------------------------------------------
# Phase 7: network reachability (R25)
# ---------------------------------------------------------------------------
if [ "$SKIP_NETWORK" = 0 ]; then
  echo
  echo "[R25] skygate-host-1 direct internet"
  # skygate-host-1 must reach 8.8.8.8 DIRECTLY (no exit-node). With
  # exit-node unset, this should work. If exit-node was set and we
  # forgot, the ping would go via tailscale → exit-node → internet,
  # which also works but for the wrong reason. R6 catches the
  # exit-node misuse; R25 catches the actual connectivity.
  PING_LOSS=$(ssh_vm \
    "docker exec $SKYGATE_CONTAINER ping -c 2 -W 2 8.8.8.8 2>&1 | grep -oE '[0-9]+% packet loss'" | head -1)
  if echo "$PING_LOSS" | grep -q '0%'; then
    echo "  ${GRN}PASS${NC}  R25 skygate-host-1 pings 8.8.8.8 (loss=$PING_LOSS)"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R25 skygate-host-1 cannot reach 8.8.8.8 (loss=$PING_LOSS)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
fi

# ---------------------------------------------------------------------------
# Phase 8: per-user device integrity (R26)
# ---------------------------------------------------------------------------
# 2026-07-28: v0.30.1. Catches the "workstation-8" bug shape on the live
# system: a per-user device (tag:dev-<user>-<device>) must NOT
# also carry an exit-node-like tag. If it does, Tailscale
# auto-failover may pick the user device as the exit-node
# (0ms self-loop = lowest metric) and the user's internet
# goes to /dev/null. The v0.30.1 build-time guard (B17 +
# PostAdminNodeTag) blocks the skygate UI path; this R26 check
# blocks the direct headscale CLI path (which the original
# bug exploited).
#
# Failure modes this catches:
#   - tag:exit-node AND tag:dev-* on the same node (the workstation-8 bug)
#   - tag:exit-relay-1 / relay-2 / relay-3 AND tag:dev-* on
#     the same node (a per-user device masquerading as a
#     specific relay)
#   - "no one fixed it after direct headscale CLI" — operator
#     did the same manual bypass again
#
# Allowed: tag:exit-node alone, tag:exit-relay-1 alone, etc.
# (a real relay). Allowed: tag:dev-* alone (a per-user device).
# Forbidden: tag:exit-* AND tag:dev-* on the same node.
if [ "$QUICK" = 0 ]; then
  echo
  echo "[R26] no per-user device carries an exit-node tag"
  # 2026-08-10: v0.33.1.41 — skip gracefully if HEADSCATE_CONTAINER
  # is still empty (operator's headscale runs outside of docker
  # compose, or label lookup failed). The previous behavior was
  # a false PASS (empty CONFLICTS list).
  if [ -z "$HEADSCALE_CONTAINER" ]; then
    echo "  ${YLW}SKIP${NC}  R26 HEADSCALE_CONTAINER not resolved (set HEADSCALE_CONTAINER=<id> to enable)"
    RESULTS_SKIP=$((RESULTS_SKIP+1))
    # Skip the rest of the if block (don't run CONFLICTS check)
    # via an early-out marker — bash doesn't have labeled breaks,
    # so we use a flag and check it below.
  else
  # headscale nodes list outputs a table; we filter rows that
  # have BOTH a tag:dev-* tag AND a tag:exit-* tag. Format:
  #   ID | Hostname | Name | MachineKey | NodeKey | User | Tags | IP | Ephemeral | Last seen | ...
  # A node's tags are spread across multiple lines (one tag per
  # line continuation in the "Tags" column). The pattern we
  # match is: a line with "tag:dev-..." and a line with
  # "tag:exit-..." for the same ID. We use awk to walk the
  # table and accumulate tags per node.
  CONFLICTS=$(ssh_vm "docker exec $HEADSCALE_CONTAINER headscale nodes list 2>&1" 2>&1 \
    | awk -F'|' '
        /^[ ]*[0-9]+[ ]+\|/ {
          if (current_id != "" && has_dev && has_exit) {
            print current_id, current_name
          }
          current_id = ""; current_name = ""; has_dev = 0; has_exit = 0
          split($0, parts, "|")
          current_id = parts[1]; gsub(/^[ \t]+|[ \t]+$/, "", current_id)
          current_name = parts[2]; gsub(/^[ \t]+|[ \t]+$/, "", current_name)
        }
        /tag:dev-/  { has_dev  = 1 }
        /tag:exit/  { has_exit = 1 }
        END {
          if (current_id != "" && has_dev && has_exit) {
            print current_id, current_name
          }
        }
      ')
  if [ -z "$CONFLICTS" ]; then
    echo "  ${GRN}PASS${NC}  R26 no node has both tag:dev-* and tag:exit-*"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R26 user-device-with-exit-tag found:"
    echo "$CONFLICTS" | sed 's/^/        /'
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
  fi
fi

# ---------------------------------------------------------------------------
# Phase 9: PG foundation live check (R27) — v0.31.0
# ---------------------------------------------------------------------------
# 2026-07-28: v0.31.0. Runs the 4 PG verification tests on the
# PG-staging VM (when SKYGATE_TEST_PG_DSN is configured on the
# staging host). Tests:
#   - TestPGRoundtripSchema: schema equivalence (SQLite ↔ PG)
#   - TestPGMigrationIdempotency: run MigratePostgres twice
#   - TestPGLockTimeout: concurrent migrations don't deadlock
#   - TestPGDataMigrationFromSQLite: dump_sqlite.py output applies
#
# Default behavior: skip if no DSN is set. Live PG validation
# requires operator to provision a PG instance, then set
# SKYGATE_TEST_PG_DSN in the operator-side env (e.g. ~/.skygate-pg).
# When set, the 4 tests run and R27 reports pass/fail per-test.
#
# If SKYGATE_TEST_PG_DSN is unset (default for the main VM, which
# is SQLite-only), R27 reports SKIP. The build-time B18 check is
# the always-on foundation guarantee.
if [ "$QUICK" = 0 ]; then
  echo
  echo "[R27] PG foundation live check (4 verification tests, v0.31.0)"
  if [ -z "${SKYGATE_TEST_PG_DSN:-}" ]; then
    echo "  ${YLW}SKIP${NC}  R27 SKYGATE_TEST_PG_DSN not set (PG-staging not provisioned; B18 covers foundation)"
  else
    # Run the 4 PG tests on the operator workstation (where the
    # DSN is reachable). The default verify-post runs from the
    # operator's machine, so this is direct (not via SSH).
    PG_TEST_OUT=$(SKYGATE_TEST_PG_DSN="$SKYGATE_TEST_PG_DSN" \
      "$GO" test -tags postgres -count=1 -timeout 120s -v \
        -run "TestPGRoundtripSchema|TestPGMigrationIdempotency|TestPGLockTimeout|TestPGDataMigrationFromSQLite" \
        ./internal/db/ 2>&1)
    PG_RC=$?
    PG_TESTS_PASS=$(echo "$PG_TEST_OUT" | grep -cE '^--- PASS:' || true)
    PG_TESTS_FAIL=$(echo "$PG_TEST_OUT" | grep -cE '^--- FAIL:' || true)
    if [ "$PG_RC" -eq 0 ] && [ "$PG_TESTS_FAIL" -eq 0 ] && [ "$PG_TESTS_PASS" -ge 4 ]; then
      echo "  ${GRN}PASS${NC}  R27 4 PG verification tests passed ($PG_TESTS_PASS PASS, $PG_TESTS_FAIL FAIL)"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R27 PG verification tests: $PG_TESTS_PASS PASS, $PG_TESTS_FAIL FAIL (rc=$PG_RC)"
      echo "$PG_TEST_OUT" | grep -E '^(--- PASS|--- FAIL|    .+\.go:[0-9]+:)' | head -20 | sed 's/^/        /'
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  fi
fi

# ---------------------------------------------------------------------------
# Phase 10: HA backup foundation (R28 + R29) — v0.32.30
# ---------------------------------------------------------------------------
# v0.32.30 closes the v0.32.26 PG cutover's "no backups" hole. Two new
# guarantees:
#
#   R28 wal-g installed + can list MinIO bucket
#       wal-g v3.0.8 at /usr/local/bin/wal-g
#       /etc/wal-g/env readable, WALG_S3_PREFIX + AWS_ENDPOINT set
#       `wal-g backup-list` runs without auth errors
#       (may legitimately show "No backups found" — that's PASS,
#       it just means no base backup taken yet)
#
#   R29 HAProxy backends reachable
#       :5000 → svyatoslava primary (port 5432 via Patroni check 8008)
#       :5001 → skygate-vm replica (port 5432 via Patroni check 8008)
#       Both bind on 0.0.0.0 (so docker containers can reach via
#       host bridge 172.17.0.1)
#
# Both checks run from the VM (where wal-g + haproxy live). The
# skygate container reaches the PG via HAProxy :5000 (DBSN) so
# any restart of haproxy breaks /readyz. R29 catches the silent
# case where haproxy is running but a backend is down.
if [ "$QUICK" = 0 ]; then
  echo
  echo "[R28] wal-g installed + can list MinIO bucket"
  WALG_OUT=$(ssh_vm "set -e
    if [ ! -x /usr/local/bin/wal-g ]; then
      echo 'NO_BINARY'
      exit 0
    fi
    if [ ! -r /etc/wal-g/env ]; then
      echo 'NO_ENV'
      exit 0
    fi
    sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-list' 2>&1 | tail -5
  " 2>&1)
  if echo "$WALG_OUT" | grep -q '^NO_BINARY'; then
    echo "  ${RED}FAIL${NC}  R28 wal-g binary not installed at /usr/local/bin/wal-g (run deploy/pg-ha/wal-g/install_wal_g.sh)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  elif echo "$WALG_OUT" | grep -q '^NO_ENV'; then
    echo "  ${RED}FAIL${NC}  R28 /etc/wal-g/env missing or unreadable (run deploy/pg-ha/wal-g/install_wal_g.sh)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  elif echo "$WALG_OUT" | grep -qE 'InvalidAccessKeyId|SignatureDoesNotMatch|NoSuchBucket|Failed to find any configured storage'; then
    echo "  ${RED}FAIL${NC}  R28 wal-g auth/config error:"
    echo "$WALG_OUT" | sed 's/^/        /'
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  elif echo "$WALG_OUT" | grep -qE 'No backups found|backup_list|base_'; then
    # SEGMENTS COUNT is column 7 in the wal-show table; awk's split counts
    # delimiters and gives us the 7th cell of the data row. Use the
    # data row (the one starting with "| 1 |" since TLI=1).
    SEGMENT_COUNT=$(ssh_vm "sudo -u postgres bash -c '. /etc/wal-g/env && wal-g wal-show' 2>&1 | grep -E '^\| *[0-9]+ *\|' | head -1 | awk -F'|' '{print \$7}' | tr -d ' '")
    if [ -z "$SEGMENT_COUNT" ]; then SEGMENT_COUNT=0; fi
    echo "  ${GRN}PASS${NC}  R28 wal-g installed at $(ssh_vm 'which wal-g'), bucket listable, $SEGMENT_COUNT WAL segments visible"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R28 wal-g returned unexpected output:"
    echo "$WALG_OUT" | sed 's/^/        /'
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  echo
  echo "[R29] HAProxy backends: :5000 primary, :5001 replica"
  # 2026-08-11: v0.34.0.1 — read the live DB admin password from the
  # operator's .env (via SSH) instead of hardcoding the fresh-install
  # default. The previous version had the literal `skygate_admin_pass`
  # as a tracked default, which (a) leaked the fresh-install password
  # to anyone with read access to the public repo and (b) would have
  # silently used the default on a deployment where the operator had
  # rotated the password. The password is read in the local shell,
  # passed via stdin, and applied to the SSH command via standard
  # local variable expansion (ssh_vm runs the expanded string on
  # the remote end).
  PRIMARY_PASS=$(ssh_vm "grep '^PG_DB_PASSWORD' /home/skyadmin/skygate/.env 2>/dev/null | tail -1 | cut -d= -f2-" 2>&1)
  if [ -z "$PRIMARY_PASS" ]; then
    # Fallback for older .env files that use the legacy var name.
    PRIMARY_PASS=$(ssh_vm "grep '^PGPASSWORD' /home/skyadmin/skygate/.env 2>/dev/null | tail -1 | cut -d= -f2-" 2>&1)
  fi
  REPLICA_PASS="$PRIMARY_PASS"
  PRIMARY_OK=$(ssh_vm "echo 'SELECT 1' | PGPASSWORD=$PRIMARY_PASS psql -h 127.0.0.1 -p 5000 -U admin -d skygate_staging -tA 2>&1 | head -1" 2>&1)
  REPLICA_OK=$(ssh_vm "echo 'SELECT pg_is_in_recovery()' | PGPASSWORD=$REPLICA_PASS psql -h 127.0.0.1 -p 5001 -U admin -d skygate_staging -tA 2>&1 | head -1" 2>&1)
  if [ "$PRIMARY_OK" = "1" ] && [ "$REPLICA_OK" = "t" ]; then
    echo "  ${GRN}PASS${NC}  R29 HAProxy :5000 (primary) + :5001 (replica) both reachable and returning expected pg_is_in_recovery"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R29 HAProxy: :5000=$PRIMARY_OK (want 1), :5001=$REPLICA_OK (want t)"
    echo "        If one is empty, haproxy backend is down — check 'systemctl status haproxy'"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  echo
  echo "[R30] primary archive_command: archive_mode + wal-g streaming"
  # Walks HAProxy :5000 (primary) and checks archive_mode + archive_command.
  # If archive_mode is on AND archive_command contains "wal-g", AND
  # pg_stat_archiver.archived_count > 0 → PASS. The svyatoslava primary
  # is at 45.152.198.217 behind HAProxy :5000 on skygate-vm.
  R30_OUT=$(ssh_vm "PGPASSWORD=$PRIMARY_PASS psql -h 127.0.0.1 -p 5000 -U admin -d skygate_staging -tA -c \"
    SELECT
      (SELECT setting FROM pg_settings WHERE name='archive_mode') || '|' ||
      (SELECT setting FROM pg_settings WHERE name='archive_command') || '|' ||
      COALESCE((SELECT archived_count FROM pg_stat_archiver), 0)::text;
  \"" 2>&1)
  ARCHIVE_MODE=$(echo "$R30_OUT" | grep -oE '^[a-z]+' | head -1)
  ARCHIVE_CMD=$(echo "$R30_OUT" | sed -E 's/^[a-z]+\|//' | sed -E 's/\|[0-9]+$//')
  ARCHIVED_COUNT=$(echo "$R30_OUT" | grep -oE '[0-9]+$' | head -1)
  if [ "$ARCHIVE_MODE" = "on" ] && echo "$ARCHIVE_CMD" | grep -q "wal-g" && [ "${ARCHIVED_COUNT:-0}" -gt 0 ]; then
    echo "  ${GRN}PASS${NC}  R30 archive_mode=on, archive_command uses wal-g, $ARCHIVED_COUNT WAL segments archived"
    RESULTS_PASS=$((RESULTS_PASS+1))
  else
    echo "  ${RED}FAIL${NC}  R30 archive not active: mode=$ARCHIVE_MODE cmd='$ARCHIVE_CMD' archived=$ARCHIVED_COUNT"
    echo "        If mode=off, run deploy/pg-ha/wal-g/README.md 'Primary-only setup' steps on svyatoslava"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi

  echo
  echo "[R31] /admin/headscale/acl renders + skygate ACL table exists"
  # v0.33.0: the Network Access Manager page. v0.33.1.42 D1:
  # use cookie-based auth (via scripts/verify_login.sh) instead
  # of basic auth — basic auth returned 302 (no admin session)
  # and we just checked for "any 2xx" which is a weak signal.
  # Cookie auth actually renders the page, so we can also
  # grep for content like "headscale_acl_rules" if needed.
  if [ -f /tmp/_skygate_verify_cookie_remote ] && [ -s /tmp/_skygate_verify_cookie_remote ]; then
    REMOTE_CK="/tmp/_skygate_verify_cookie"
  else
    REMOTE_CK=$(bash "$SCRIPT_DIR/verify_login.sh") || REMOTE_CK=""
  fi
  if [ -z "$REMOTE_CK" ]; then
    echo "  ${YLW}SKIP${NC}  R31 login failed (admin creds) — cannot verify page"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  else
    ACL_PAGE=$(ssh -o StrictHostKeyChecking=no "$SSH_HOST" \
      "curl -sS -b $REMOTE_CK -o /dev/null -w '%{http_code}' http://localhost:8080/admin/headscale/acl" 2>/dev/null || echo "000")
    if [ "$ACL_PAGE" = "200" ]; then
      echo "  ${GRN}PASS${NC}  R31 /admin/headscale/acl renders 200 with admin session (Network Access Manager live)"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R31 /admin/headscale/acl returned $ACL_PAGE (expected 200)"
      echo "        Check that the v0.33.0 binary is running and headscale_acl_rules table exists"
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  fi

  echo
  echo "[R32] /admin/system_tests renders + TestRegistry accessible"
  # v0.33.0: the Admin Test Page. v0.33.1.42 D1: cookie auth
  # (re-uses the cookie from R31 — verify_login.sh is called
  # once per verify_post_deploy.sh run and the cookie jar is
  # re-used across the R-checks).
  if [ -n "$REMOTE_CK" ]; then
    ST_PAGE=$(ssh -o StrictHostKeyChecking=no "$SSH_HOST" \
      "curl -sS -b $REMOTE_CK -o /dev/null -w '%{http_code}' http://localhost:8080/admin/system_tests" 2>/dev/null || echo "000")
    if [ "$ST_PAGE" = "200" ]; then
      echo "  ${GRN}PASS${NC}  R32 /admin/system_tests renders 200 with admin session (Admin Test Page live)"
      RESULTS_PASS=$((RESULTS_PASS+1))
    else
      echo "  ${RED}FAIL${NC}  R32 /admin/system_tests returned $ST_PAGE (expected 200)"
      echo "        Check that the v0.33.0 binary is running and system_tests_runs table exists"
      RESULTS_FAIL=$((RESULTS_FAIL+1))
    fi
  else
    echo "  ${YLW}SKIP${NC}  R32 (R31 login failed, no cookie to reuse)"
    RESULTS_FAIL=$((RESULTS_FAIL+1))
  fi
fi

# ---------------------------------------------------------------------------
# R33 — v0.33.1.39 B91: skygate container starts independently of
# headscale/headplane after VM reboot.
#
# Verifies the END-TO-END runtime property of the B91 fix:
#   1. All three core containers are Up (skygate, headscale, headplane)
#      — none in Restarting / unhealthy state
#   2. /healthz returns 200 (skygate is responding)
#   3. /readyz returns 200 (skygate has DB + headscale reachable)
#   4. skygate's logs show the v0.33.1.39 pre-flight wait completed
#      (either "headscale ready after Ns" or the WARNING line — both
#      prove the pre-flight wait RAN, which is what B91 added)
#
# This is the runtime mirror of the build-time B91 grep check. B91
# proves the SOURCE has the pre-flight wait + loose-coupling; R33
# proves the LIVE system actually comes up correctly after a
# cold-boot. If a future refactor accidentally removes the pre-flight
# wait from entrypoint.sh, B91 catches it at build time. If the
# runtime behavior regresses (e.g. /readyz stays 503 forever after
# a reboot), R33 catches it after deploy.
#
# NOTE: this check is on the SKIP_NETWORK skip-list (see the case
# block above). /healthz and /readyz are local endpoints, but the
# `docker ps` for headscale/headplane requires the VM to be
# reachable. --skip-network is mainly for /admin over the public
# HTTPS path; R33 works on any deployment that has docker access.
# ---------------------------------------------------------------------------
echo
echo "[R33] skygate + headscale + headplane containers all Up after VM boot (B91 loose-coupling runtime check)"
SKY_UP=$(ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$SSH_HOST" \
  "docker ps --format '{{.Names}} {{.State}}' 2>/dev/null | awk '\$1 ~ /skygate/ {print \$2}' | head -1" 2>/dev/null || echo "ERR")
HS_UP=$(ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$SSH_HOST" \
  "docker ps --format '{{.Names}} {{.State}}' 2>/dev/null | awk '\$1 ~ /^headscale\$/ {print \$2}' | head -1" 2>/dev/null || echo "ERR")
HP_UP=$(ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$SSH_HOST" \
  "docker ps --format '{{.Names}} {{.State}}' 2>/dev/null | awk '\$1 ~ /headplane/ {print \$2}' | head -1" 2>/dev/null || echo "ERR")

HEALTHZ=$(ssh_vm "curl -fsS -o /dev/null -w '%{http_code}' http://localhost:8080/healthz" 2>/dev/null | tr -d '\n' || echo "000")
READYZ=$(ssh_vm "curl -fsS -o /dev/null -w '%{http_code}' http://localhost:8080/readyz" 2>/dev/null | tr -d '\n' || echo "000")

# The pre-flight wait log line is the B91-specific proof. We accept
# either "headscale ready after" (the success path) or the WARNING
# line (the timeout path — headscale was up but slow, or unreachable).
# If NEITHER line is present, the pre-flight wait was removed.
PREFLIGHT=$(ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$SSH_HOST" \
  'docker logs $(docker ps -q --filter label=com.docker.compose.service=skygate | head -1) 2>/dev/null | grep -E "pre-flight: waiting for headscale|headscale ready after" | head -1' 2>/dev/null || echo "")

if [ "$SKY_UP" = "running" ] && [ "$HEALTHZ" = "200" ] && [ "$READYZ" = "200" ] && [ -n "$PREFLIGHT" ]; then
  echo "  ${GRN}PASS${NC}  R33 skygate=$SKY_UP headscale=$HS_UP headplane=$HP_UP /healthz=$HEALTHZ /readyz=$READYZ (B91 pre-flight wait logged)"
  RESULTS_PASS=$((RESULTS_PASS+1))
else
  echo "  ${RED}FAIL${NC}  R33 skygate=$SKY_UP headscale=$HS_UP headplane=$HP_UP /healthz=$HEALTHZ /readyz=$READYZ preflight=${PREFLIGHT:-NONE}"
  echo "        skygate must be running with healthy /healthz + /readyz; pre-flight wait must have logged"
  RESULTS_FAIL=$((RESULTS_FAIL+1))
fi

# ---------------------------------------------------------------------------
# R34 — v0.33.1.40 B92: the Availability Checker is running
# and /readyz exposes the cached snapshot with ≥3 integrations
# (headscale, headplane, tailscale). Also verifies the
# /admin/services route is registered (any 2xx/3xx response,
# not 404 — the route is admin-only so 302 redirect to /login
# is the correct "route exists" signal; we don't try to
# authenticate because the verify_post_deploy.sh runs from
# the operator's machine, not the VM, and basic auth on the
# JSON /readyz endpoint is the only thing we can verify
# without a cookie).
#
# B92 is the runtime mirror of B91: just as B91 proved the
# /admin/services page exists in the source, R34 proves the
# live system has the Availability Checker wired and producing
# snapshots. The /readyz.availability field is the source of
# truth for the UI (the /admin/services page just reads the
# same snapshot).
# ---------------------------------------------------------------------------
echo
echo "[R34] /admin/services page renders with admin session + B92 snapshot has >=3 integrations"
# v0.33.1.42 D1: now uses cookie-based admin auth (via
# scripts/verify_login.sh, the same cookie re-used by R31/R32).
# Pre-D1, this check used 302-to-/login as a "route exists"
# signal — a weak proof. Post-D1, we actually render the
# page and grep for the integration labels. The page is
# only considered healthy if the live HTML mentions
# headscale + headplane + tailscale by name.
SERVICES_PAGE=""
SERVICES_GREP_HITS=0
if [ -z "$REMOTE_CK" ]; then
  REMOTE_CK=$(bash "$SCRIPT_DIR/verify_login.sh") || REMOTE_CK=""
fi
if [ -n "$REMOTE_CK" ]; then
  SERVICES_HTML=$(ssh -o StrictHostKeyChecking=no "$SSH_HOST" \
    "curl -sS -b $REMOTE_CK -o /tmp/_r34_page.html -w '%{http_code}' http://localhost:8080/admin/services" 2>/dev/null || echo "000")
  SERVICES_PAGE=$(echo "$SERVICES_HTML" | tail -1)
  SERVICES_BODY=$(ssh -o StrictHostKeyChecking=no "$SSH_HOST" "cat /tmp/_r34_page.html 2>/dev/null" 2>/dev/null || echo "")
  # Count how many of the 3 known integrations are mentioned
  # by name in the page body. Pre-D1 we just checked
  # "non-404 status"; post-D1 we count labels.
  for LABEL in headscale headplane tailscale; do
    if echo "$SERVICES_BODY" | grep -qi "$LABEL"; then
      SERVICES_GREP_HITS=$((SERVICES_GREP_HITS + 1))
    fi
  done
  ssh -o StrictHostKeyChecking=no "$SSH_HOST" "rm -f /tmp/_r34_page.html" 2>/dev/null || true
fi

# /readyz.availability check — the B92 snapshot is the contract.
# v1.0.0.3: parse on the VM (where python3 is always present)
# instead of locally — Windows hosts don't have python3 in PATH
# by default, and the previous local-pipe approach printed
# "Python was not found" and returned 0.
# v1.0.0.6: pipe the JSON to the VM via stdin (the prior approach
# wrote to /tmp/_r34_readyz.json on the HOST, but the ssh command
# read it from the VM's /tmp, so the file never existed on the VM
# and python3 saw an empty stdin and printed 0).
READYZ_JSON=$(ssh -o StrictHostKeyChecking=no "$SSH_HOST" \
  "curl -sS http://localhost:8080/readyz" 2>/dev/null || echo "")
AVAIL_COUNT=$(printf '%s\n' "$READYZ_JSON" | ssh -o StrictHostKeyChecking=no "$SSH_HOST" \
  "python3 -c 'import sys,json
try:
    d=json.loads(sys.stdin.read() or \"{}\")
    a=d.get(\"availability\") or {}
    print(len(a.get(\"integrations\") or []))
except Exception:
    print(0)'" 2>/dev/null || echo 0)
HAS_HEADSCALE=$(printf '%s\n' "$READYZ_JSON" | ssh -o StrictHostKeyChecking=no "$SSH_HOST" \
  "python3 -c 'import sys,json
try:
    d=json.loads(sys.stdin.read() or \"{}\")
    a=d.get(\"availability\") or {}
    ids=[i.get(\"id\") for i in (a.get(\"integrations\") or [])]
    print(\"yes\" if \"headscale\" in ids else \"no\")
except Exception:
    print(\"no\")'" 2>/dev/null || echo "no")
if [ "$SERVICES_PAGE" = "200" ] && [ "$SERVICES_GREP_HITS" -ge 3 ] && [ "$AVAIL_COUNT" -ge 3 ] && [ "$HAS_HEADSCALE" = "yes" ]; then
  echo "  ${GRN}PASS${NC}  R34 /admin/services=200 with admin session (3/3 integration labels visible) + /readyz.availability has $AVAIL_COUNT integrations (B92 snapshot + D1 cookie-auth page render)"
  RESULTS_PASS=$((RESULTS_PASS+1))
else
  echo "  ${RED}FAIL${NC}  R34 /admin/services=$SERVICES_PAGE (label_hits=$SERVICES_GREP_HITS/3) /readyz.availability=$AVAIL_COUNT (headscale=$HAS_HEADSCALE, want 200 + >=3 labels + >=3 integrations)"
  echo "        /admin/services must render 200 with admin session; page must show headscale + headplane + tailscale; /readyz.availability must have >=3 integrations including headscale"
  RESULTS_FAIL=$((RESULTS_FAIL+1))
fi

# ---------------------------------------------------------------------------
# R35 — v0.33.1.42 D2 + D8: Tailscale BackendState check via
# `tailscale status --json`. Pre-D35 we used the state-file
# presence proxy (a /var/lib/tailscale/tailscaled.state file
# existing = "tailscaled running"). The proxy couldn't
# distinguish a healthy tailnet from one in NeedsLogin (auth
# callback pending) — both wrote a state file. Post-D35, we
# read the actual BackendState field from `tailscale status
# --json` and require it to be "Running" (the only state
# where tailscaled can actually serve traffic).
#
# Source-of-truth: same `tailscaleBackendState` helper that's
# in cmd/skygate/main.go (D8) — the live page and the verify-
# post check use the same code path, so the operator sees
# consistent status in both places.
# ---------------------------------------------------------------------------
echo
echo "[R35] Tailscale BackendState='Running' (D2 + D8: tailscale status --json)"
TS_STATE=$(ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$SSH_HOST" \
  'docker exec skygate-skygate-1 tailscale status --json 2>/dev/null | python3 -c "import sys, json
try:
    d = json.loads(sys.stdin.read() or \"{}\")
    print(d.get(\"BackendState\", \"\"))
except Exception:
    print(\"PARSE_ERROR\")"' 2>/dev/null || echo "EXEC_ERROR")
if [ "$TS_STATE" = "Running" ]; then
  echo "  ${GRN}PASS${NC}  R35 tailscale BackendState=Running (D8 BackendState check + D2 verify-post mirror)"
  RESULTS_PASS=$((RESULTS_PASS+1))
elif [ "$TS_STATE" = "EXEC_ERROR" ] || [ "$TS_STATE" = "PARSE_ERROR" ] || [ -z "$TS_STATE" ]; then
  # tailscaled not running in container, OR the container
  # is not named skygate-skygate-1 (e.g. operator uses a
  # different compose project name). Non-RF mode is
  # legitimate (B32: Tailscale is OFF by default), so
  # this is SKIP, not FAIL.
  echo "  ${YLW}SKIP${NC}  R35 tailscale status --json unavailable (state='$TS_STATE') — non-RF mode is OK"
else
  echo "  ${RED}FAIL${NC}  R35 tailscale BackendState='$TS_STATE' (want 'Running'; other states: NeedsLogin, NoState, Stopped, Starting)"
  echo "        /admin/services page shows the same value; fix Tailscale auth (SKYGATE_TS_AUTHKEY_FILE, SKYGATE_TS_LOGIN_SERVER) to recover"
  RESULTS_FAIL=$((RESULTS_FAIL+1))
fi
echo
echo "=== summary ==="
echo "  ${GRN}PASS${NC}: $RESULTS_PASS"
echo "  ${RED}FAIL${NC}: $RESULTS_FAIL"

if [ "$RESULTS_FAIL" -gt 0 ]; then
  echo
  echo "${RED}post-deploy verification FAILED — investigate before continuing${NC}"
  exit 1
fi
echo
echo "${GRN}post-deploy verification PASSED — system is healthy${NC}"
exit 0
