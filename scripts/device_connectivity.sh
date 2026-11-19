#!/usr/bin/env bash
# B500 (2026-11-19) — «ЭТО УСТРОЙСТВО ДОХОДИТ ДО EXIT-УЗЛА?» — ОДНА КОМАНДА, ОДИН ВЕРДИКТ.
#
# Живой случай: оператор спросил про `olesya` — «в сети, посмотри как с доступом к exit node
# и скорость». Ручное расследование заняло шесть заходов и дало ответ, который ни одна
# страница панели не показывала: устройство ONLINE по контрольной плоскости (сессия на
# публичном DERP `hel`), НО данные не ходят — `tailscale ping` не отвечает, `rx=0` при
# `tx=3120`, а relay `karolina` вообще не видит пути (`-`, 0/0). При этом ACL разрешал
# exit-node, все четыре relay были healthy с одобренным `0.0.0.0/0`.
#
# То есть «нет доступа к exit-узлу» и «нет пути до устройства» — РАЗНЫЕ диагнозы, и панель
# показывала только «online». Этот скрипт печатает все слои и говорит, какой из них сломан.
#
# Usage: bash scripts/device_connectivity.sh <hostname> [--json] [--db PATH] [--verbose]
# Exit: 0 — устройство реально проходит трафик; 1 — слой сломан (см. вывод);
#       2 — измерить нельзя (нет tailscale/DB/устройства).
set -uo pipefail

DEVICE=""
JSON=0
VERBOSE=0
DB="${SKYGATE_DB_PATH:-/var/lib/skygate/skygate.db}"
while [ $# -gt 0 ]; do
    case "$1" in
        --json) JSON=1 ;;
        --verbose|-v) VERBOSE=1 ;;
        --db) shift; DB="${1:-$DB}" ;;
        -h|--help) sed -n '1,30p' "$0"; exit 0 ;;
        -*) echo "device_connectivity: unknown flag $1" >&2; exit 2 ;;
        *) DEVICE="$1" ;;
    esac
    shift
done
[ -n "$DEVICE" ] || { echo "usage: device_connectivity.sh <hostname>" >&2; exit 2; }
DEVICE_LOWER="$(printf '%s' "$DEVICE" | tr '[:upper:]' '[:lower:]')"

command -v tailscale >/dev/null 2>&1 || { echo "device_connectivity: tailscale CLI not found (run on the panel host)" >&2; exit 2; }
command -v python3   >/dev/null 2>&1 || { echo "device_connectivity: python3 not found" >&2; exit 2; }

say() { [ "$JSON" -eq 1 ] || echo "$@"; }

# ── layer 1: the control plane knows the node ────────────────────────────────
ctl="$(headscale nodes list -o json 2>/dev/null | python3 -c '
import json,sys,datetime
want=sys.argv[1]
try:
    ns=json.load(sys.stdin)
except Exception:
    print("unknown|0|0"); raise SystemExit
if isinstance(ns,dict): ns=ns.get("nodes") or []
for n in ns:
    name=(n.get("givenName") or n.get("name") or "").lower()
    if name!=want: continue
    ls=n.get("lastSeen") or ""
    age=-1
    try:
        t=datetime.datetime.fromisoformat(ls.replace("Z","+00:00"))
        age=(datetime.datetime.now(datetime.timezone.utc)-t).total_seconds()/60.0
    except Exception:
        pass
    print("%s|%.1f|%s" % ("online" if n.get("online") else "offline", age if age >= 0 else 0.0, n.get("id","")))
    raise SystemExit
print("absent|0|0")
' "$DEVICE_LOWER")"
state="${ctl%%|*}"; rest="${ctl#*|}"; age_min="${rest%%|*}"; node_id="${rest#*|}"

if [ "$state" = "absent" ]; then
    say "device_connectivity: $DEVICE is not in headscale at all"
    [ "$JSON" -eq 1 ] && printf '{"device":"%s","layer":"control","verdict":"absent","gaps":1}\n' "$DEVICE"
    exit 2
fi

# ── layer 2/3: the local netmap's path and a real disco ping ─────────────────
path_json="$(tailscale status --json 2>/dev/null | python3 -c '
import json,sys
want=sys.argv[1]
d=json.load(sys.stdin)
for k,p in (d.get("Peer") or {}).items():
    if (p.get("HostName") or "").lower()!=want: continue
    print(json.dumps({
        "online": bool(p.get("Online")),
        "relay": p.get("Relay") or "",
        "cur_addr": p.get("CurAddr") or "",
        "rx": p.get("RxBytes") or 0,
        "tx": p.get("TxBytes") or 0,
        "handshake": p.get("LastHandshake") or "",
        "exit_option": bool(p.get("ExitNodeOption")),
    }))
    raise SystemExit
print("{}")
' "$DEVICE_LOWER")"
[ -n "$path_json" ] || path_json="{}"

ping_ok=0
ping_line="$(timeout 30 tailscale ping -c 5 "$DEVICE_LOWER" 2>&1 | tail -1)"
case "$ping_line" in
    *pong*) ping_ok=1 ;;
esac

# ── layer 4: may this device use an exit node at all? (live policy) ──────────
acl="$(python3 - "$DB" "$DEVICE_LOWER" <<'PY'
import json, sqlite3, sys, re
db_path, want = sys.argv[1], sys.argv[2]
tag = None
try:
    con = sqlite3.connect("file:%s?mode=ro" % db_path, uri=True)
    row = con.execute("SELECT tag FROM node_owner_map WHERE lower(hostname)=?", (want,)).fetchone()
    tag = (row or [None])[0]
except Exception as e:
    print("unknown||%s" % e); raise SystemExit
if not tag:
    print("no-tag||this device has no per-device tag, so no per-device grant can name it")
    raise SystemExit
# the live policy is stored in headscale's own DB
pol = ""
try:
    hdb = sqlite3.connect("file:/var/lib/headscale/db.sqlite?mode=ro", uri=True)
    r = hdb.execute("SELECT data FROM policies ORDER BY id DESC LIMIT 1").fetchone()
    pol = (r or [""])[0] or ""
except Exception as e:
    print("unknown||cannot read the live policy: %s" % e); raise SystemExit
try:
    doc = json.loads(re.sub(r"//.*", "", pol if not pol.startswith('"') else json.loads(pol)))
except Exception as e:
    print("unknown||live policy is not parseable: %s" % e); raise SystemExit
internet = [g for g in doc.get("grants", []) if "autogroup:internet" in (g.get("dst") or [])]
mine = [g for g in internet if tag in (g.get("src") or [])]
if not mine:
    print("denied||no autogroup:internet grant names %s" % tag); raise SystemExit
pinned = [g for g in mine if g.get("via")]
if pinned:
    print("pinned||%s" % ",".join(" ".join(g["via"]) for g in pinned)); raise SystemExit
print("allowed||unpinned grant: the device may choose any relay")
PY
)"
acl_state="${acl%%|*}"; acl_rest="${acl#*|}"; acl_detail="${acl_rest#*|}"

# ── layer 5: are the relays able to carry it? ────────────────────────────────
relays="$(python3 - "$DB" <<'PY'
import sqlite3, sys
try:
    con = sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True)
    rows = con.execute("SELECT hostname, state, COALESCE(healthy,0), COALESCE(advertised_routes_ok,0) FROM exit_node_health ORDER BY hostname").fetchall()
except Exception as e:
    print("unknown|%s" % e); raise SystemExit
ok = [r for r in rows if r[2] and r[3]]
print("%d/%d|%s" % (len(ok), len(rows), ",".join(r[0] for r in ok)))
PY
)"
relay_ok="${relays%%|*}"; relay_list="${relays#*|}"

# ── verdict ─────────────────────────────────────────────────────────────────
gaps=0
problems=""
add_gap() { gaps=$((gaps + 1)); problems="${problems}${problems:+, }$1"; }

[ "$state" = "online" ] || { add_gap "control: offline"; }
# An "online" control session with a stale lastSeen is the classic half-dead client.
if [ "$state" = "online" ] && [ "${age_min%%.*}" -gt 5 ] 2>/dev/null; then
    add_gap "control: online but silent for ${age_min} min"
fi
[ "$ping_ok" -eq 1 ] || add_gap "path: no disco reply (the device does not answer)"
case "$acl_state" in
    allowed|pinned) : ;;
    *) add_gap "acl: $acl_state" ;;
esac
case "$relay_ok" in
    0/*) add_gap "relays: none healthy with approved routes" ;;
    *) : ;;
esac
rx="$(printf '%s' "$path_json" | python3 -c 'import json,sys; print((json.load(sys.stdin) or {}).get("rx",0))' 2>/dev/null || echo 0)"

if [ "$JSON" -eq 1 ]; then
    printf '{"device":"%s","node_id":"%s","control":{"state":"%s","minutes_since_seen":%s},"path":%s,"disco_ping":"%s","acl":{"state":"%s","detail":"%s"},"relays":{"healthy":"%s","names":"%s"},"gaps":%d}\n' \
        "$DEVICE" "$node_id" "$state" "${age_min:-0}" "$path_json" \
        "$(printf '%s' "$ping_line" | sed 's/"/\\"/g')" "$acl_state" "$acl_detail" "$relay_ok" "$relay_list" "$gaps"
else
    echo "device: $DEVICE (node id $node_id)"
    printf '  %-10s %s\n' "control" "$state, last seen ${age_min} min ago"
    printf '  %-10s %s\n' "path" "$(printf '%s' "$path_json" | python3 -c '
import json,sys
p=json.load(sys.stdin) or {}
if not p: print("not in the local netmap"); raise SystemExit
print("relay=%s curAddr=%s rx=%s tx=%s" % (p.get("relay") or "-", p.get("cur_addr") or "-", p.get("rx"), p.get("tx")))
')"
    printf '  %-10s %s\n' "disco" "$ping_line"
    printf '  %-10s %s\n' "acl" "$acl_state — $acl_detail"
    printf '  %-10s %s\n' "relays" "$relay_ok healthy with approved routes ($relay_list)"
    if [ "$gaps" -gt 0 ]; then
        echo "  verdict  BROKEN: $problems"
    else
        echo "  verdict  OK: the device answers and may use an exit node (rx=$rx bytes from it)"
    fi
fi

[ "$gaps" -eq 0 ] || exit 1
exit 0
