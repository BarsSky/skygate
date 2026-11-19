#!/usr/bin/env bash
# B500 (2026-11-19) — «ПОРТЫ ОТКРЫТЫ?» — ПРОВЕРКА, А НЕ ДОГАДКА.
#
# Эталонный хост, 2026-11-19. Оператор: «посмотри может сама svyatoslava не открыла
# порты нужные». На фактах нашлось ровно две вещи, и обе выглядят одинаково — «не
# работает»:
#
#   1. ufw разрешал 443/tcp, 3478/udp и 8443/tcp, но НЕ 443/udp, хотя Caddy слушает
#      udp/443 (HTTP/3 — им же ходит DERP-over-QUIC). Порт был закрыт, и в плане
#      установщика (`deploy/install-firewall.sh`) его тоже не было. Исправлено в обоих
#      местах: правило добавлено, установщик планирует порт.
#   2. «Слушается» ≠ «разрешён» ≠ «отвечает». Первая версия этой проверки отправляла
#      «голый» 20-байтный Binding Request, получала тишину и объявляла relay сломанным —
#      И ЭТО БЫЛО ОШИБКОЙ ПРОВЕРКИ, а не продукта. Счётчики самого derper
#      (`/debug/vars`) показали: запрос формы реального клиента (`SOFTWARE "tailnode"` +
#      `FINGERPRINT`) получает Binding Success и увеличивает `success`, а любая другая
#      форма считается `not_stun` и остаётся без ответа. Поэтому STUN-проба теперь живёт
#      в `scripts/lib/stun_probe.py` и воспроизводит форму клиента: молчание такой пробы
#      означает настоящую поломку, а не свойство пробника.
#
# Отсюда и проверки: этот скрипт сверяет ОЖИДАЕМЫЙ список портов с тем, что реально
# слушает, с тем, что разрешено, и — отдельно — отвечает ли сервис. Только чтение.
#
# Usage:
#   bash scripts/port_audit.sh [--json] [--host <hostname>]
# Exit: 0 — всё сходится; 1 — есть разрыв (не слушается / не разрешён / не отвечает);
#       2 — проверить нельзя (нет ss, или ни один сервис Skygate здесь не активен:
#           «осмотрено 0 портов» — не зелёный результат, урок B494).
set -uo pipefail

JSON=0
HOSTNAME_OPT=""
while [ $# -gt 0 ]; do
    case "$1" in
        --json) JSON=1 ;;
        --host) shift; HOSTNAME_OPT="${1:-}" ;;
        -h|--help) sed -n '1,40p' "$0"; exit 0 ;;
        *) echo "port_audit: unknown argument $1" >&2; exit 2 ;;
    esac
    shift
done

say() { [ "$JSON" -eq 1 ] || echo "$@"; }

# ── can we inspect at all? ───────────────────────────────────────────────────
if ! command -v ss >/dev/null 2>&1; then
    echo "port_audit: ss not found — cannot list listeners" >&2
    exit 2
fi

HAVE_UFW=0
command -v ufw >/dev/null 2>&1 && HAVE_UFW=1
UFW_ACTIVE=0
if [ "$HAVE_UFW" -eq 1 ]; then
    case "$(ufw status 2>/dev/null | head -1)" in
        *active*) UFW_ACTIVE=1 ;;
    esac
fi

LISTEN_TCP="$(ss -lntH 2>/dev/null | awk '{print $4}')"
LISTEN_UDP="$(ss -lnuH 2>/dev/null | awk '{print $4}')"

# ── the expected list, and WHY each entry exists ─────────────────────────────
# owner = the unit that must be active for the port to be required.
EXPECTED=(
    "80/tcp|optional|caddy|ACME HTTP-01 + the maintenance page"
    "443/tcp|required|caddy|HTTPS: panel, headscale control, DERP-over-443"
    "443/udp|required|caddy|HTTP/3 (QUIC): the panel when the browser uses H3, and DERP-over-QUIC"
    "8443/tcp|required|derper|DERP TLS listener (derper --a=:8443)"
    "3478/udp|required|derper|STUN for the bundled DERP — used for NAT traversal"
)

unit_active() {
    command -v systemctl >/dev/null 2>&1 || return 1
    systemctl is-active --quiet "$1" 2>/dev/null
}

listening_on() { # proto port
    local port="$1" proto="$2" list
    [ "$proto" = "tcp" ] && list="$LISTEN_TCP" || list="$LISTEN_UDP"
    case "$list" in
        *":$port"*) return 0 ;;
        *) return 1 ;;
    esac
}

ufw_allows() { # port proto
    local port="$1" proto="$2"
    [ "$UFW_ACTIVE" -eq 1 ] || return 1
    ufw status 2>/dev/null | awk -v p="$port/$proto" '$1 == p { found = 1 } END { exit(found ? 0 : 1) }'
}

ROWS=""
GAPS=0
SKIPS=0
INSPECTED=0
for spec in "${EXPECTED[@]}"; do
    IFS='|' read -r portproto requirement owner why <<<"$spec"
    port="${portproto%%/*}"
    proto="${portproto##*/}"

    req=1
    case "$requirement" in
        optional)
            if ! unit_active "$owner"; then
                ROWS="${ROWS}${portproto}|skip|${why}|${owner} is not active on this host"$'\n'
                SKIPS=$((SKIPS + 1))
                continue
            fi
            ;;
        required)
            if ! unit_active "$owner"; then
                # A packed single-host install may run everything; a host that does not
                # serve DERP does not need STUN/8443. Absence of the unit is not a gap.
                ROWS="${ROWS}${portproto}|skip|${why}|${owner} is not active here (the port is not required)"$'\n'
                SKIPS=$((SKIPS + 1))
                continue
            fi
            ;;
    esac

    if listening_on "$port" "$proto"; then
        INSPECTED=$((INSPECTED + 1))
        if [ "$UFW_ACTIVE" -eq 1 ] && ! ufw_allows "$port" "$proto"; then
            ROWS="${ROWS}${portproto}|fail|${why}|listening, but ufw does NOT allow it"$'\n'
            GAPS=$((GAPS + 1))
        else
            ROWS="${ROWS}${portproto}|ok|${why}|listening and allowed"$'\n'
        fi
    else
        INSPECTED=$((INSPECTED + 1))
        ROWS="${ROWS}${portproto}|fail|${why}|${owner} is active but nothing listens on this port"$'\n'
        GAPS=$((GAPS + 1))
    fi
done

# ── STUN: a port that listens is not a service that answers ──────────────────
STUN_VERDICT="skip"
STUN_DETAIL="derper is not active on this host"
if unit_active derper; then
    HELPER="$(dirname "$0")/lib/stun_probe.py"
    if [ -f "$HELPER" ] && command -v python3 >/dev/null 2>&1; then
        STUN_DETAIL="$(python3 "$HELPER" 127.0.0.1 3478 2>&1)"; stun_rc=$?
        case "$stun_rc" in
            0) STUN_VERDICT="ok" ;;
            1) STUN_VERDICT="fail" ;;
            # No reply is NOT proof of a broken relay: derper counts a packet it does not
            # accept as its own `not_stun` counter and stays silent. The helper mimics a
            # real client (SOFTWARE "tailnode" + FINGERPRINT), which is what the server
            # answers; anything else measures the prober, not the service.
            *) STUN_VERDICT="fail" ;;
        esac
        [ "$STUN_VERDICT" = "fail" ] && GAPS=$((GAPS + 1))
    else
        STUN_DETAIL="python3 or $HELPER is missing — STUN liveness was not tested"
    fi
fi

# ── output ───────────────────────────────────────────────────────────────────
if [ "$JSON" -eq 1 ]; then
    json_rows=""
    while IFS='|' read -r portproto status why extra; do
        [ -n "${portproto:-}" ] || continue
        json_rows="${json_rows}${json_rows:+,}{\"port\":\"${portproto}\",\"status\":\"${status}\",\"why\":\"${why}\",\"detail\":\"${extra}\"}"
    done <<<"$ROWS"
    printf '{"host":"%s","ufw_active":%s,"stun":"%s","stun_detail":"%s","rows":[%s],"gaps":%d,"skips":%d}\n' \
        "${HOSTNAME_OPT:-$(hostname 2>/dev/null || echo unknown)}" "$UFW_ACTIVE" \
        "$STUN_VERDICT" "$STUN_DETAIL" "$json_rows" "$GAPS" "$SKIPS"
else
    echo "port audit on ${HOSTNAME_OPT:-$(hostname 2>/dev/null || echo this host)} (ufw active: $UFW_ACTIVE)"
    printf '  %-10s %-6s %s\n' "PORT" "STATE" "DETAIL"
    while IFS='|' read -r portproto status why extra; do
        [ -n "${portproto:-}" ] || continue
        printf '  %-10s %-6s %s — %s\n' "$portproto" "$status" "$why" "$extra"
    done <<<"$ROWS"
    printf '  %-10s %-6s %s\n' "3478/udp" "$STUN_VERDICT" "$STUN_DETAIL"
fi

if [ "$GAPS" -gt 0 ]; then
    [ "$JSON" -eq 1 ] || echo "port_audit: $GAPS gap(s) — see the table above"
    exit 1
fi
# B494's lesson applied here: a run that inspected NOTHING must not look green. If no
# owning unit is active, this is not a Skygate host and the answer is «cannot inspect»,
# not «all ports fine».
if [ "$INSPECTED" -eq 0 ]; then
    [ "$JSON" -eq 1 ] || echo "port_audit: no Skygate service is active here — nothing was inspected (exit 2, not a pass)"
    exit 2
fi
exit 0
