#!/usr/bin/env bash
# ============================================================================
# deploy/install-firewall.sh — the inbound ports a Skygate host actually needs
#
# WHY THIS EXISTS (TD-38)
#
# The 2026-11-15 migration opened the firewall BY HAND on the production panel
# host (`ufw allow 8443/tcp` was one of the recorded fixes). A rebuilt host has
# no such memory: the panel would come up healthy on loopback while the relay
# stayed unfetchable from outside, and no B-check would notice.
#
# WHAT IT OPENS (and what it deliberately does NOT)
#
#   80/tcp    ACME HTTP-01 (Caddy's certificate challenge)
#   443/tcp   HTTPS — panel, headscale, DERP-over-443
#   443/udp   HTTP/3 (QUIC) — Caddy listens on udp/443, and DERP-over-QUIC uses it.
#             B500 (2026-11-19): this one was MISSING from the plan, and the reference
#             host had it closed: `port_audit.sh` reported «listening, but ufw does NOT
#             allow it». A browser on H3 and a phone using QUIC both fall back to TCP,
#             so the symptom is not an outage — it is a slower/less reliable path that
#             nobody can see. `--no-http3` skips it for hosts that pin HTTP/2.
#   3478/udp  STUN for the bundled DERP relay
#   8443/tcp  DERP's own TLS listener (derper --a=:8443)
#
# It does NOT open 8080 / 50444 / 50443: those are loopback services behind
# Caddy, and exposing them would bypass TLS and the ACL boundary. `--extra`
# exists for the rare case (e.g. a subnet-router's own exposure).
#
# USAGE
#   sudo bash deploy/install-firewall.sh                 # show what WOULD change
#   sudo bash deploy/install-firewall.sh --apply         # actually add the rules
#   sudo bash deploy/install-firewall.sh --ssh-port 22 --apply
#   bash deploy/install-firewall.sh --dry-run --json
#
# FLAGS
#   --apply          add the rules (WITHOUT it the script only reports)
#   --ssh-port N     also ensure SSH stays allowed (recommended: you are using it)
#   --derp-port N    DERP TLS port (default 8443; 0 = host serves no relay)
#   --stun-port N    STUN UDP port (default 3478; 0 = skip)
#   --extra SPEC     additional "port/proto" rule, repeatable (e.g. 51820/udp)
#   --no-http        do not open 80/tcp (e.g. DNS-01 only, port 80 blocked upstream)
#   --no-http3       do not open 443/udp (HTTP/3 disabled, e.g. an upstream that drops UDP)
#   --dry-run        report only (the default; stated explicitly for scripts)
#   --json           machine-readable summary
#
# It is idempotent: ufw itself refuses duplicates ("Skipping adding existing
# rule"), and the printed plan always names the state it found.
#
# EXIT: 0 ok / unchanged · 1 usage error · 2 ufw missing or inactive · 3 a
#       rule could not be added
# ============================================================================

set -uo pipefail

APPLY=0
DRY=1
JSON=0
SSH_PORT=""
DERP_PORT=8443
STUN_PORT=3478
ENABLE_HTTP=1
ENABLE_HTTP3=1
EXTRA=()

say()  { [ "$JSON" -eq 1 ] || echo "[firewall] $*"; }
die()  { local rc="$1"; shift; echo "[firewall] ERROR: $*" >&2; exit "$rc"; }
usage() { sed -n '2,50p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
    case "$1" in
        --apply)      APPLY=1; DRY=0; shift ;;
        --dry-run)    DRY=1; APPLY=0; shift ;;
        --json)       JSON=1; shift ;;
        --ssh-port)   SSH_PORT="${2:-}"; shift 2 ;;
        --derp-port)  DERP_PORT="${2:-}"; shift 2 ;;
        --stun-port)  STUN_PORT="${2:-}"; shift 2 ;;
        --extra)      EXTRA+=("${2:-}"); shift 2 ;;
        --no-http)    ENABLE_HTTP=0; shift ;;
        --no-http3)   ENABLE_HTTP3=0; shift ;;
        -h|--help)    usage; exit 0 ;;
        *) die 1 "unknown flag: $1 (see --help)" ;;
    esac
done

HAVE_UFW=0
command -v ufw >/dev/null 2>&1 && HAVE_UFW=1
if [ "$HAVE_UFW" -eq 0 ] && [ "$DRY" -eq 0 ]; then
    die 2 "ufw is not installed (install it, or manage the firewall elsewhere and read the PLAN above for the required ports)"
fi
if [ "$(id -u)" -ne 0 ]; then
    [ "$DRY" -eq 1 ] || die 1 "run as root (or pass --dry-run to just see the plan)"
fi

# ── build the plan ───────────────────────────────────────────────────────────
PLAN=()
[ "$ENABLE_HTTP" -eq 1 ] && PLAN+=("80/tcp:ACME HTTP-01")
PLAN+=("443/tcp:HTTPS (panel + headscale + DERP-over-443)")
[ "$ENABLE_HTTP3" -eq 1 ] && PLAN+=("443/udp:HTTP/3 (QUIC) — Caddy listens on udp/443")
case "$STUN_PORT" in ''|0) : ;; *) PLAN+=("${STUN_PORT}/udp:STUN for the bundled DERP relay") ;; esac
case "$DERP_PORT" in ''|0) : ;; *) PLAN+=("${DERP_PORT}/tcp:DERP TLS listener") ;; esac
[ -n "$SSH_PORT" ] && PLAN+=("${SSH_PORT}/tcp:SSH (keep your own access)")
for spec in "${EXTRA[@]:-}"; do
    [ -n "$spec" ] && PLAN+=("${spec}:operator-supplied")
done

# ── report the state we found ────────────────────────────────────────────────
if [ "$HAVE_UFW" -eq 1 ]; then
    status="$(ufw status 2>/dev/null | head -1)"
else
    status="ufw not installed"
fi
active=0
case "$status" in
    *active*) active=1 ;;
esac

if [ "$JSON" -eq 1 ]; then
    json_plan=""
    for p in "${PLAN[@]}"; do
        json_plan="${json_plan}${json_plan:+,}\"${p%%:*}\""
    done
    printf '{"apply":%s,"ufw_active":%s,"rules":[%s]}\n' \
        "$([ "$APPLY" -eq 1 ] && echo true || echo false)" \
        "$([ "$active" -eq 1 ] && echo true || echo false)" \
        "$json_plan"
else
    echo "[firewall] ufw status: ${status:-unknown}"
    for p in "${PLAN[@]}"; do
        echo "[firewall]   need ${p%%:*}  (${p#*:})"
    done
    echo "[firewall] NOT opened on purpose: 8080, 50443, 50444 (loopback behind Caddy)"
fi

if [ "$DRY" -eq 1 ]; then
    [ "$JSON" -eq 1 ] || say "dry run — nothing changed (re-run with --apply)"
    exit 0
fi

[ "$active" -eq 1 ] || die 2 "ufw is installed but not active — enable it deliberately (ufw enable) before opening ports, otherwise the plan would silently apply to a disabled firewall"

# ── apply ────────────────────────────────────────────────────────────────────
fail=0
add_rule() { # add_rule <port>/<proto> <label>
    local rule="$1" label="$2"
    local out
    out="$(ufw allow "$rule" 2>&1)"; local rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "[firewall] FAILED $rule ($label): $out" >&2
        fail=$((fail + 1))
    else
        case "$out" in
            *"Skipping"*|*"existing"*) say "already allowed: $rule" ;;
            *) say "allowed: $rule ($label)" ;;
        esac
    fi
}

[ "$ENABLE_HTTP" -eq 1 ] && add_rule "80/tcp" "ACME HTTP-01"
add_rule "443/tcp" "HTTPS"
# B500: Caddy listens on udp/443 as well (HTTP/3), and DERP-over-QUIC uses it. Without
# this rule the port is bound and the rule set simply never let it in — the exact gap
# `scripts/port_audit.sh` reports as «listening, but ufw does NOT allow it».
[ "$ENABLE_HTTP3" -eq 1 ] && add_rule "443/udp" "HTTP/3 (QUIC)"
case "$STUN_PORT" in ''|0) : ;; *) add_rule "${STUN_PORT}/udp" "STUN" ;; esac
case "$DERP_PORT" in ''|0) : ;; *) add_rule "${DERP_PORT}/tcp" "DERP TLS" ;; esac
[ -n "$SSH_PORT" ] && add_rule "${SSH_PORT}/tcp" "SSH"
for spec in "${EXTRA[@]:-}"; do
    [ -n "$spec" ] && add_rule "$spec" "operator-supplied"
done

[ "$fail" -eq 0 ] || die 3 "$fail rule(s) could not be added"
say "done — verify from outside with: bash scripts/port_audit.sh (or ss -ltnup | grep -E ':(80|443|3478|8443)')"
