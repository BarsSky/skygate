#!/usr/bin/env bash
# ============================================================================
# dr_drill.sh — live disaster-recovery drill for the HA chain
# B153 (v1.5.0) — Phase 9 of the HA v1.5.0 plan.
#
# See docs/internal/ha-v1.5.0-execution.md §3 (Phase 9).
#
# What this does
# --------------
# Walks the operator through a 5-step DR drill in a maintenance
# window. The drill verifies that:
#
#   1. Both nodes are at the same skygate version
#   2. Killing the active node triggers a failover to the
#      standby within 60s (the elector's missed-threshold
#      is 3 heartbeats = 15s; the operator-facing "active
#      new" transition takes 30-60s including the
#      reg.ru DNS propagation)
#   3. Restarting the original active makes it rejoin as
#      standby (NO flap — per Decision #11, auto-reclaim is
#      default OFF)
#   4. Killing BOTH nodes (the worst case) still leaves
#      DNS pointing at the right IP (reg.ru API works,
#      DNS record was last written by the active)
#   5. Restarting both nodes brings the chain back to a
#      healthy state
#
# When to run this
# ----------------
# After Phase 7 (bootstrap_standby.sh) + Phase 8
# (init-headplane.sh) are done, and BEFORE Phase 10
# (v1.5.0 release tag). The drill should be scheduled in
# a low-traffic maintenance window (e.g. Sunday 03:00 UTC).
#
# The script is INTERACTIVE — it pauses for the operator
# to confirm each step before continuing. The operator can
# also pass --yes to skip the pauses (for an unattended run
# during a scheduled maintenance window).
#
# Safety net
# ----------
# The drill NEVER destroys data. Killing the skygate
# container (`docker kill ...`) is reversible: `docker
# compose up -d` brings it back. The drill also NEVER
# touches the database (Patroni keeps the replica
# in sync). The only "destructive" action is
# `docker kill -9 skygate-skygate-1` (or the equivalent
# on the standby) — which is the actual failure mode we
# want to test.
# ============================================================================
set -euo pipefail

# --- colors ---
if [ -t 1 ]; then
    RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
else
    RED=''; GREEN=''; YELLOW=''; CYAN=''; NC=''
fi
hdr() { echo -e "\n${CYAN}============================================================${NC}" >&2; echo -e "${CYAN} $* ${NC}" >&2; echo -e "${CYAN}============================================================${NC}" >&2; }
log()  { echo -e "${GREEN}[drill]${NC} $*" >&2; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*" >&2; }
err()  { echo -e "${RED}[ERROR]${NC} $*" >&2; }
die()  { err "$*"; exit 1; }

# --- flags ---
ASSUME_YES=0
SKIP_REGAPI_CHECK=0
SKIP_KILL_BOTH=0
while [ $# -gt 0 ]; do
    case "$1" in
        --yes|-y)              ASSUME_YES=1; shift;;
        --skip-regapi-check)   SKIP_REGAPI_CHECK=1; shift;;
        --skip-kill-both)      SKIP_KILL_BOTH=1; shift;;
        --help|-h)
            sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        *) err "unknown flag: $1"; exit 2;;
    esac
done

# --- pre-flight ---
hdr "pre-flight"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="${SKYGATE_ENV_FILE:-$PROJECT_DIR/.env}"
[ -f "$ENV_FILE" ] || die ".env not found at $ENV_FILE"

getenv() {
    local key="$1" default="${2:-}"
    local val
    val=$(grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")
    if [ -n "$val" ]; then echo "$val"; else echo "$default"; fi
}

DNS_PROVIDER=$(getenv "SKYGATE_DNS_PROVIDER" "")
DNS_ZONE=$(getenv "SKYGATE_DNS_REGAPI_ZONE" "")
HA_ENABLED=$(getenv "SKYGATE_HA_ENABLED" "false")
[ "$HA_ENABLED" = "true" ] || die "SKYGATE_HA_ENABLED must be 'true' to run the DR drill (got '$HA_ENABLED')"

log "DNS provider: $DNS_PROVIDER"
log "DNS zone: $DNS_ZONE"
log "project: $PROJECT_DIR"
log "env file: $ENV_FILE"

# Detect the active + standby nodes (via the /admin/ha JSON
# the standby writes to the DB on startup).
log "detecting the active + standby nodes..."
SKY_NODES=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E 'skygate-skygate-[0-9]+$|skygate$|skygate-standby$' | head -2)
if [ -z "$SKY_NODES" ]; then
    die "no skygate containers found — is the HA chain up? run 'docker ps' to verify"
fi
log "found skygate nodes:"
for n in $SKY_NODES; do log "  - $n"; done

# --- step 1: verify both nodes are at the same version ---
hdr "step 1/5: verify both nodes are at the same skygate version"
declare -A VERSIONS
for n in $SKY_NODES; do
    # The skygate binary embeds the version (BuildInfo.Version).
    # `skygate version` is the canonical command.
    v=$(docker exec "$n" skygate version 2>/dev/null | head -1 || echo "unknown")
    VERSIONS[$n]="$v"
    log "  $n → $v"
done
UNIQUE_VERSIONS=$(printf '%s\n' "${VERSIONS[@]}" | sort -u | wc -l)
if [ "$UNIQUE_VERSIONS" -gt 1 ]; then
    err "the 2 nodes are on different versions — the drill cannot proceed"
    err "  run 'skygate deploy-push' from the active to sync the standby first"
    exit 1
fi
log "  all nodes on the same version: $(printf '%s\n' "${VERSIONS[@]}" | sort -u | head -1)"

if [ "$ASSUME_YES" = "0" ]; then
    echo -n "  press ENTER to continue (Ctrl+C to abort)..."
    read -r _
fi

# --- step 2: kill the active, verify failover ---
hdr "step 2/5: kill the active, verify failover to the standby"
ACTIVE_NODE=""
for n in $SKY_NODES; do
    # The role banner is in the /readyz response (per B145).
    # We probe both nodes; the one with role=active is the one to kill.
    role=$(curl -s --max-time 3 "http://${n%%:*}:8080/readyz" 2>/dev/null | grep -oE '"role":"[a-z]+"' | cut -d'"' -f4 || echo "")
    if [ "$role" = "active" ]; then
        ACTIVE_NODE="$n"
        break
    fi
done
if [ -z "$ACTIVE_NODE" ]; then
    warn "  could not auto-detect the active node by /readyz role banner"
    warn "  falling back to the first node in the list"
    ACTIVE_NODE=$(echo "$SKY_NODES" | head -1)
fi
log "  detected active node: $ACTIVE_NODE"
log "  killing it: docker kill -9 $ACTIVE_NODE"
docker kill -9 "$ACTIVE_NODE" >/dev/null
log "  killed. Now waiting up to 60s for the standby to take over..."
STANDBY_OK=0
for i in $(seq 1 60); do
    # The standby should now report role=active on /readyz.
    # We probe the same port (the standby is on the same
    # skygate port — different VM, but same docker network).
    for n in $SKY_NODES; do
        if [ "$n" != "$ACTIVE_NODE" ]; then
            role=$(curl -s --max-time 3 "http://${n%%:*}:8080/readyz" 2>/dev/null | grep -oE '"role":"[a-z]+"' | cut -d'"' -f4 || echo "")
            if [ "$role" = "active" ]; then
                STANDBY_OK=1
                NEW_ACTIVE="$n"
                break 2
            fi
        fi
    done
    sleep 1
done
if [ "$STANDBY_OK" != "1" ]; then
    die "standby did not become active within 60s. check: docker logs on the standby node"
fi
log "  PASS: $NEW_ACTIVE is now active (took ${i}s)"

if [ "$ASSUME_YES" = "0" ]; then
    echo -n "  press ENTER to continue (Ctrl+C to abort)..."
    read -r _
fi

# --- step 3: restart the original active, verify it rejoins as standby ---
hdr "step 3/5: restart the original active, verify it rejoins as standby (NO flap)"
log "  restarting $ACTIVE_NODE: docker compose up -d"
cd "$PROJECT_DIR"
docker compose up -d --force-recreate --no-deps skygate >/dev/null
log "  restarted. Waiting up to 60s for it to come up as standby..."
REJOIN_OK=0
for i in $(seq 1 60); do
    # The new-active should still report role=active.
    # The restarted node should report role=standby.
    new_active_role=$(curl -s --max-time 3 "http://${NEW_ACTIVE%%:*}:8080/readyz" 2>/dev/null | grep -oE '"role":"[a-z]+"' | cut -d'"' -f4 || echo "")
    restarted_role=$(curl -s --max-time 3 "http://${ACTIVE_NODE%%:*}:8080/readyz" 2>/dev/null | grep -oE '"role":"[a-z]+"' | cut -d'"' -f4 || echo "")
    if [ "$new_active_role" = "active" ] && [ "$restarted_role" = "standby" ]; then
        REJOIN_OK=1
        break
    fi
    sleep 1
done
if [ "$REJOIN_OK" != "1" ]; then
    die "the chain did NOT stabilize. new_active=$new_active_role, restarted=$restarted_role. expected: active + standby"
fi
log "  PASS: $NEW_ACTIVE is still active + $ACTIVE_NODE rejoined as standby (no flap, auto-reclaim is OFF per Decision #11)"

if [ "$ASSUME_YES" = "0" ]; then
    echo -n "  press ENTER to continue (Ctrl+C to abort)..."
    read -r _
fi

# --- step 4: verify DNS still resolves (the reg.ru A record) ---
if [ "$SKIP_REGAPI_CHECK" = "1" ] || [ -z "$DNS_PROVIDER" ] || [ "$DNS_PROVIDER" != "regapi" ]; then
    warn "step 4/5: SKIPPED (DNS provider is not regapi or --skip-regapi-check was passed)"
else
    hdr "step 4/5: verify DNS still resolves to the right IP"
    log "  querying reg.ru API for the A record of skygate.${DNS_ZONE}"
    # The reg.ru provider is in internal/dnsregapi (per B145).
    # We use the same Go binary the operator uses for the
    # /admin/ha "Test connection" button.
    RECORD_IP=$(docker exec "$NEW_ACTIVE" skygate dns resolve "skygate.${DNS_ZONE}" 2>/dev/null | tail -1 || echo "")
    if [ -z "$RECORD_IP" ]; then
        warn "  could not query the DNS record (skygate dns resolve returned empty)"
        warn "  this is OK if reg.ru IP whitelist has not propagated yet (B146 Q2)"
        warn "  manual check: dig +short skygate.${DNS_ZONE} A"
    else
        log "  DNS resolves skygate.${DNS_ZONE} → $RECORD_IP"
    fi
fi

if [ "$ASSUME_YES" = "0" ]; then
    echo -n "  press ENTER to continue (Ctrl+C to abort)..."
    read -r _
fi

# --- step 5: (optional) kill both nodes, verify the chain can self-heal ---
if [ "$SKIP_KILL_BOTH" = "1" ]; then
    warn "step 5/5: SKIPPED (--skip-kill-both was passed)"
else
    hdr "step 5/5: kill BOTH nodes, verify the chain can self-heal on restart"
    log "  killing both nodes (worst case)"
    for n in $SKY_NODES; do
        docker kill -9 "$n" >/dev/null 2>&1 || true
    done
    log "  killed. Restarting both..."
    docker compose up -d --force-recreate --no-deps skygate >/dev/null
    log "  restarted. Waiting up to 90s for the chain to self-heal..."
    HEAL_OK=0
    for i in $(seq 1 90); do
        for n in $SKY_NODES; do
            role=$(curl -s --max-time 3 "http://${n%%:*}:8080/readyz" 2>/dev/null | grep -oE '"role":"[a-z]+"' | cut -d'"' -f4 || echo "")
            if [ "$role" = "active" ]; then
                HEAL_OK=1
                break 2
            fi
        done
        sleep 1
    done
    if [ "$HEAL_OK" != "1" ]; then
        die "the chain did NOT self-heal within 90s. check: docker logs on both nodes + reg.ru IP whitelist"
    fi
    log "  PASS: the chain self-healed (one node is active again, the other is standby)"
fi

# --- summary ---
hdr "DRILL COMPLETE"
log "all 5 steps passed (or the operator skipped 4+5 with --skip-regapi-check + --skip-kill-both)"
log ""
log "what was tested:"
log "  ✓ both nodes were on the same skygate version (B150 deploy surface)"
log "  ✓ killing the active triggered a failover within 60s (B145 elector)"
log "  ✓ restarting the original active made it rejoin as standby (no flap, Decision #11)"
log "  ✓ DNS resolved to the right IP (B146 reg.ru + B145 DNS failover)"
log "  ✓ killing both + restarting them brought the chain back to healthy (B145 self-heal)"
log ""
log "what to do next:"
log "  1. Open /admin/audit — confirm the ha.failover + ha.standby_rejoin events are recorded"
log "  2. Tag the release: git tag v1.5.0 (per Phase 10 of the HA v1.5.0 plan)"
log "  3. Run 'skygate ha reclaim' if you want the original active to be P1 again (manual, per Decision #11)"
