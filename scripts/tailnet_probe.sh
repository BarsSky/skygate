#!/usr/bin/env bash
# tailnet_probe.sh — per-pair tailnet speed + reachability
# diagnostics. Operator can run this from ANY Tailscale node
# to verify the operator's true question: "can I reach
# <other_node> and how fast?". Unlike the Go system tests
# (which run inside skygate-host-1 and only probe from one
# perspective), this script can run from karolina, skybars,
# any VPS, or the operator's laptop.
#
# Usage
# =====
#   # Probe all known peers from the current node
#   ./scripts/tailnet_probe.sh
#
#   # Probe a specific peer
#   ./scripts/tailnet_probe.sh --to 100.64.0.5
#
#   # Probe all known peers using iperf3 (bandwidth) instead
#   # of TCP:22 (latency). Requires iperf3 server on peer.
#   ./scripts/tailnet_probe.sh --iperf3
#
#   # JSON output for automation
#   ./scripts/tailnet_probe.sh --json
#
# What it does
# ============
# 1. Lists peers via `tailscale status --json` (works on
#    every Tailscale node; doesn't require headscale API).
# 2. For each online peer:
#    - TCP-connect to port 22 (SSH) with 2s timeout.
#    - Optional: iperf3 -c <peer> for 5s (bandwidth).
#    - Optional: ping -c 3 -W 2 (ICMP latency).
# 3. Reports a per-peer table and a summary.
#
# TAILNET SPLIT detection
# =======================
# If the operator runs this on 2 different nodes and the
# peer lists differ, that's a tailnet split. The script
# outputs a "DIAGNOSIS" line at the end if it detects
# that the local peer list is small (< 50% of expected).
# The expected count is hardcoded for the operator's
# deployment; update it after each new node is added.
#
# Hardcoded operator fleet (2026-08-13):
#   VPS:     emilia, karolina, sharlotta, skygate-host-1, svyatoslava-1
#   Home:    skybars, skyworker, a71, olesya, svyatoslava-legacy,
#            basic, base, skybars-1, cyborg, nothing-phone-2,
#            desktop-cuo0tfb, msi
#   Total:   17 known Tailscale nodes (10 online as of 2026-08-13)
#
# Dependencies
# ============
#   tailscale (1.32+, for `tailscale status --json`)
#   bash 4+ (uses arrays)
#   ping (optional, for ICMP latency)
#   iperf3 (optional, for bandwidth — operator-installed)
#   nc OR /dev/tcp fallback (for TCP:22 probe)
#
# Exit codes
# ==========
#   0 = all peers reachable
#   1 = 1+ peers unreachable (TAILNET SPLIT suspected if
#       unreachable > 1 and total < 50% of expected)
#   2 = script-level error (missing tailscale, bad args)
#
# Why this script exists separately from the Go tests
# ==================================================
# The Go tests in internal/feature/admin/system_tests_tailnet.go
# run INSIDE the skygate container on skygate-host-1 — they
# only show "what skygate-host-1 can see". When the operator
# runs into "karolina can't see skybars" they need to run a
# probe FROM karolina to confirm. This script is the answer.
# Bash + tailscale status --json works on every Tailscale
# node without needing Go + skygate + PostgreSQL.

set -u
# set -e intentionally NOT enabled — we want to keep probing
# even if one peer fails (don't bail on first error).

PROG=$(basename "$0")
JSON=0
IPERF3=0
PING=0
TO=""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Operator-specific expected peer count. Update when nodes are
# added/removed. The "split" warning fires when the local
# `tailscale status` shows fewer than half of these.
EXPECTED_PEERS=16  # 17 total − self

usage() {
    cat <<EOF
$PROG — per-pair tailnet speed + reachability diagnostics

Usage: $PROG [options]

Options:
  --to IP         Probe only the given peer (Tailscale IP).
  --iperf3        Add iperf3 bandwidth test (requires iperf3 on peer).
  --ping          Add ICMP ping latency (3 packets, 2s timeout).
  --json          Emit JSON instead of human-readable table.
  --help          This help.

Examples:
  $PROG                              # Probe all peers (TCP:22 only)
  $PROG --to 100.64.0.5              # Probe only skybars
  $PROG --iperf3 --to 100.64.0.3     # Bandwidth to emilia
  $PROG --json                       # Machine-readable

Exit codes:
  0  all peers reachable
  1  1+ peers unreachable (split suspected)
  2  script error
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --to)    TO="${2:-}"; shift 2 ;;
        --iperf3) IPERF3=1; shift ;;
        --ping)  PING=1; shift ;;
        --json)  JSON=1; shift ;;
        --help|-h) usage; exit 0 ;;
        *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
    esac
done

# --- preflight: tailscale available? ----------------------------

if ! command -v tailscale >/dev/null 2>&1; then
    echo "ERROR: tailscale not in PATH. Run on a Tailscale node." >&2
    exit 2
fi
if ! tailscale status --json >/dev/null 2>&1; then
    echo "ERROR: tailscaled not running on this node. Start with: systemctl start tailscaled" >&2
    exit 2
fi

# --- gather peers via tailscale status --json --------------------

TS_JSON=$(tailscale status --json 2>/dev/null)
if [ -z "$TS_JSON" ]; then
    echo "ERROR: tailscale status --json returned empty" >&2
    exit 2
fi

# Use python3 for JSON parsing (no jq dependency).
# python3 is available on every Tailscale-supported platform.
parse_peers() {
    python3 - <<PYEOF
import json, sys
data = json.loads('''$TS_JSON''')
self_ip = data.get("TailscaleIPs", [""])[0]
out = []
for peer in data.get("Peer", {}).values():
    addrs = peer.get("TailscaleIPs", [])
    if not addrs:
        continue
    ip = next((a for a in addrs if "." in a), "")
    if not ip:
        continue
    out.append({
        "ip": ip,
        "hostname": peer.get("HostName", "?"),
        "online": peer.get("Online", False),
        "exit": peer.get("ExitNode", False),
    })
print(f"SELF_IP={self_ip}")
for p in out:
    print(f"PEER\t{p['ip']}\t{p['hostname']}\t{int(p['online'])}\t{int(p['exit'])}")
PYEOF
}

PARSED=$(parse_peers 2>/dev/null)
if [ -z "$PARSED" ]; then
    echo "ERROR: failed to parse tailscale status JSON" >&2
    exit 2
fi

SELF_IP=$(echo "$PARSED" | grep '^SELF_IP=' | cut -d= -f2)
PEERS=$(echo "$PARSED" | grep '^PEER	' | cut -f2-5)

# --- probe function: TCP:22 with 2s timeout ---------------------
# Uses bash's /dev/tcp feature (built-in, no nc dependency).
# Returns: "OK <ms>" or "FAIL <reason>".
#
# Latency measurement uses $EPOCHREALTIME (bash 4+, available
# in the skygate container at 5.2.21 and on every modern
# Linux/macOS). Returns seconds.microseconds (e.g.
# "1786618205.317110"). We multiply by 1000 to get ms.
# This is portable: works on Alpine busybox, glibc Linux,
# macOS, WSL — no `date +%s%N` dependency (which doesn't
# work on busybox).

tcp_probe() {
    local ip="$1"
    local s e
    # Bash 5+ has $EPOCHREALTIME; bash 4 has it as well
    # (4.0+, but rarely in 3.x). Default to 0 if unset.
    s=${EPOCHREALTIME:-0}
    # 2-second timeout via `timeout` if available; else rely
    # on bash's /dev/tcp which doesn't have a timeout. Most
    # Linux distros have `timeout` from coreutils.
    if command -v timeout >/dev/null 2>&1; then
        if timeout 2 bash -c "echo > /dev/tcp/$ip/22" 2>/dev/null; then
            e=${EPOCHREALTIME:-0}
            # awk for the float math (busybox awk works).
            local ms
            ms=$(awk -v s="$s" -v e="$e" 'BEGIN { printf "%d", (e - s) * 1000 }')
            echo "OK $ms"
            return 0
        else
            echo "FAIL timeout/closed"
            return 1
        fi
    else
        # Fallback: bash /dev/tcp with no timeout (may hang).
        if (echo > /dev/tcp/$ip/22) 2>/dev/null; then
            e=${EPOCHREALTIME:-0}
            local ms
            ms=$(awk -v s="$s" -v e="$e" 'BEGIN { printf "%d", (e - s) * 1000 }')
            echo "OK $ms"
            return 0
        else
            echo "FAIL refused"
            return 1
        fi
    fi
}

ping_probe() {
    local ip="$1"
    if ! command -v ping >/dev/null 2>&1; then
        echo "N/A"
        return 0
    fi
    # 3 packets, 2s per-packet timeout. macOS uses -t, Linux uses -W.
    local out
    if ping -c 3 -W 2 "$ip" >/dev/null 2>&1; then
        out=$(ping -c 3 -W 2 "$ip" 2>&1 | tail -1)
        # Extract avg from "rtt min/avg/max/mdev = ..."
        echo "$out" | grep -oE 'min/avg/max[^=]+= [0-9.]+/[0-9.]+/[0-9.]+' | head -1 | sed 's/.*= //' | cut -d/ -f2
    else
        echo "100% loss"
    fi
}

iperf3_probe() {
    local ip="$1"
    if ! command -v iperf3 >/dev/null 2>&1; then
        echo "N/A (no iperf3)"
        return 0
    fi
    # 3 second test, JSON output, parse "bits_per_second".
    local out
    out=$(iperf3 -c "$ip" -t 3 -J 2>/dev/null)
    if [ -z "$out" ]; then
        echo "FAIL (no iperf3 server)"
        return 1
    fi
    local bps
    bps=$(echo "$out" | python3 -c 'import json,sys; d=json.load(sys.stdin); e=d.get("end",{}); print(int(e.get("sum_received",{}).get("bits_per_second", 0)))' 2>/dev/null)
    if [ -z "$bps" ] || [ "$bps" = "0" ]; then
        echo "FAIL"
    else
        # Convert to Mbit/s.
        awk -v b="$bps" 'BEGIN { printf "%.1f Mbit/s", b/1e6 }'
    fi
}

# --- main loop --------------------------------------------------

if [ "$JSON" = "1" ]; then
    echo "{"
    echo "  \"self_ip\": \"$SELF_IP\","
    echo "  \"probes\": ["
fi

# Pre-initialize arrays so `set -u` doesn't trip when no
# entries are appended (e.g. all peers OK, RESULTS_FAIL is empty).
RESULTS_OK=()
RESULTS_FAIL=()
FIRST=1

while IFS=$'\t' read -r ip hostname online exit_flag; do
    [ -z "$ip" ] && continue
    if [ -n "$TO" ] && [ "$ip" != "$TO" ]; then
        continue
    fi
    [ "$online" = "0" ] && continue  # skip offline peers

    TCP_R=$(tcp_probe "$ip")
    PING_R=""
    IPERF_R=""
    if [ "$PING" = "1" ]; then
        PING_R=$(ping_probe "$ip")
    fi
    if [ "$IPERF3" = "1" ]; then
        IPERF_R=$(iperf3_probe "$ip")
    fi

    if [[ "$TCP_R" == OK* ]]; then
        # Strip the leading "OK " and trim. The value is
        # "<ms>" from the tcp_probe function. If the script
        # is running on a platform where date +%s%N doesn't
        # work (Alpine busybox, macOS), tcp_probe returns
        # "OK 0" — we still record success, just without
        # a useful latency number.
        LAT="${TCP_R#OK }"
        RESULTS_OK+=("$hostname ($ip): ${LAT}ms")
    else
        RESULTS_FAIL+=("$hostname ($ip): ${TCP_R#FAIL }")
    fi

    if [ "$JSON" = "1" ]; then
        [ "$FIRST" = "0" ] && echo ","
        FIRST=0
        printf '    {"ip":"%s","hostname":"%s","tcp":"%s"' "$ip" "$hostname" "$TCP_R"
        if [ -n "$PING_R" ]; then
            printf ',"ping":"%s"' "$PING_R"
        fi
        if [ -n "$IPERF_R" ]; then
            printf ',"iperf3":"%s"' "$IPERF_R"
        fi
        printf '}'
    else
        printf "%-20s %-15s tcp=%-12s" "$hostname" "$ip" "$TCP_R"
        [ -n "$PING_R" ] && printf " ping=%s" "$PING_R"
        [ -n "$IPERF_R" ] && printf " iperf3=%s" "$IPERF_R"
        echo
    fi
done <<< "$PEERS"

# --- summary -----------------------------------------------------

TOTAL=$(( ${#RESULTS_OK[@]} + ${#RESULTS_FAIL[@]} ))

if [ "$JSON" = "1" ]; then
    echo ""
    echo "  ],"
    echo "  \"summary\": {\"ok\": ${#RESULTS_OK[@]}, \"fail\": ${#RESULTS_FAIL[@]}, \"total\": $TOTAL}"
    echo "}"
else
    echo
    echo "Summary: ${#RESULTS_OK[@]}/$TOTAL peers reachable from $SELF_IP"
    if [ "${#RESULTS_FAIL[@]}" -gt 0 ]; then
        echo "Unreachable:"
        for r in "${RESULTS_FAIL[@]}"; do
            echo "  $r"
        done
    fi
    if [ "$TOTAL" -gt 0 ] && [ "$TOTAL" -lt $((EXPECTED_PEERS / 2)) ]; then
        echo
        echo "DIAGNOSIS: TAILNET SPLIT LIKELY"
        echo "  Local peer count ($TOTAL) is < 50% of expected ($EXPECTED_PEERS)."
        echo "  Other nodes on this tailnet are not visible from $SELF_IP."
        echo "  See docs/tailnet-diagnostics.md for root cause + fix."
    fi
fi

# --- exit code ---------------------------------------------------
# 0 = all OK
# 1 = 1+ failures (split suspected if 2+ failures AND total < 50% of expected)
# 2 = script error (handled above)

if [ "${#RESULTS_FAIL[@]}" -eq 0 ]; then
    exit 0
fi
if [ "${#RESULTS_FAIL[@]}" -ge 2 ] && [ "$TOTAL" -lt $((EXPECTED_PEERS / 2)) ]; then
    exit 1  # split
fi
if [ "${#RESULTS_FAIL[@]}" -ge 1 ]; then
    exit 1  # 1+ failure is enough to alert
fi
exit 0
