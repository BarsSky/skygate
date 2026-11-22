#!/usr/bin/env bash
# derp_path_audit.sh — B510 (2026-11-25)
#
# WHY THIS EXISTS. Operator: «проблема именно в скачках, и это в основном у мобильных
# устройств по мобильной сети, на вайфае таких проблем нет». That difference is the
# diagnosis: a Wi-Fi client gets a DIRECT path, so DERP is not in its traffic; a cellular
# client is relayed, so every relay re-selection is felt as a stalled stream.
#
# On the reference host the relay re-selection had a concrete cause, invisible everywhere:
#   * the three public names had an A record but NO AAAA;
#   * the derpmap we publish carried no IPv4 and no IPv6 for our own region 900;
#   * tailscaled answered that with an endless stream of
#     `netcheck.runProbe: named node "mow-1" has no v6 address`,
#     ~1224 netcheck probes and 96 DERP re-connections in 6 hours.
# Cellular networks here are IPv6-first; the default Tailscale regions carry IPv6 and ours
# did not, so an IPv6-preferring phone kept re-probing and sometimes preferring another
# region. Nothing in the panel said so, and three rounds of "which relay carries me" never
# looked at the DERP map at all.
#
# WHAT IT DOES (read-only, no state is changed):
#   1. DNS: the A and AAAA records of the relay name.
#   2. MAP: what the published derpmap.json actually advertises for that node.
#   3. VERDICT: whether the map carries what DNS says, and the exact record to add when it
#      does not.
#   4. REACHABILITY: a real STUN probe (the client's own packet shape, scripts/lib/stun_probe.py)
#      over IPv4 and, when an AAAA exists, over IPv6.
#   5. TRACE: how often this host re-established DERP and re-ran netcheck in the last hour
#      — the shape of the "spikes" as the local client sees it.
#
# USAGE
#   bash scripts/derp_path_audit.sh                 # name from the panel's DB, else arg
#   bash scripts/derp_path_audit.sh --host relay.example.com
#   bash scripts/derp_path_audit.sh --host relay.example.com --hours 6 --json
#
# EXIT CODES: 0 = the audit ran (findings or not — a finding is not a tool error),
#             2 = the audit could not run (no name, no python3 for the probes).

set -uo pipefail

HOST=""
HOURS=1
JSON_OUT=0
MAP_URL=""
STUN_PORT=""

usage() { sed -n '2,/^set -uo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --host)      shift; HOST="${1:-}" ;;
    --hours)     shift; HOURS="${1:-1}" ;;
    --json)      JSON_OUT=1 ;;
    --map-url)   shift; MAP_URL="${1:-}" ;;
    --stun-port) shift; STUN_PORT="${1:-}" ;;
    -h|--help)   usage; exit 0 ;;
    *) echo "unknown argument: $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done

command -v python3 >/dev/null 2>&1 || {
  echo "derp_path_audit: python3 is required (DNS + STUN probe)" >&2; exit 2; }

# The name to audit: the argument, else the panel's own DB, else SKYGATE_DERP_HOSTNAME.
if [ -z "$HOST" ]; then
  HOST="$(grep -hoE '^SKYGATE_DERP_HOSTNAME=.*' /etc/skygate/skygate.env 2>/dev/null | head -1 | cut -d= -f2- | tr -d '\42\47')"
fi
if [ -z "$HOST" ]; then
  DB="$(grep -hoE '^SKYGATE_DB=.*' /etc/skygate/skygate.env 2>/dev/null | head -1 | cut -d= -f2- | tr -d '\42\47')"
  DB="${DB#sqlite:}"
  if [ -n "$DB" ] && [ -r "$DB" ]; then
    HOST="$(python3 - "$DB" <<'PY'
import sqlite3, sys
try:
    c = sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True)
    row = c.execute("select hostname from derp_relays where is_bundled=1 and enabled=1 order by id asc limit 1").fetchone()
    print(row[0] if row else "")
except Exception:
    print("")
PY
)"
  fi
fi
[ -n "$HOST" ] || { echo "derp_path_audit: no relay name — pass --host <name>" >&2; exit 2; }

# The map endpoint the panel publishes (headscale fetches it): prefer the local panel.
if [ -z "$MAP_URL" ]; then
  PORT="$(grep -hoE '^SKYGATE_PORT=.*' /etc/skygate/skygate.env 2>/dev/null | head -1 | cut -d= -f2- | tr -d '\42\47')"
  [ -n "$PORT" ] || PORT="8080"
  MAP_URL="http://127.0.0.1:${PORT}/admin/derp/relays/derpmap.json"
fi
HELPER="$(dirname "$0")/lib/stun_probe.py"
# B510 follow-up: when the audit is piped (`ssh host bash -s < derp_path_audit.sh`) there is
# no script directory, so $0 is just "bash" and the helper looked missing — the audit then
# printed "STUN: not probed" instead of the probe it exists for. Fall back to the caller's
# working directory and to SKYGATE_REPO before giving up.
if [ ! -f "$HELPER" ]; then
  HELPER="$PWD/scripts/lib/stun_probe.py"
fi
if [ ! -f "$HELPER" ] && [ -n "${SKYGATE_REPO:-}" ]; then
  HELPER="$SKYGATE_REPO/scripts/lib/stun_probe.py"
fi

[ -n "$STUN_PORT" ] || STUN_PORT=3478

# --- the audit itself ---------------------------------------------------------------
python3 - "$HOST" "$HOURS" "$MAP_URL" "$STUN_PORT" "$HELPER" "$JSON_OUT" <<'PY'
import json, os, socket, subprocess, sys
import urllib.request

host, hours, map_url, stun_port_arg, helper, json_out = sys.argv[1:7]
hours = int(hours or 1)
stun_port = int(stun_port_arg or 3478)
findings = []
facts = {"host": host}

# 1. DNS, both families.
def resolve(family):
    try:
        infos = socket.getaddrinfo(host, None, family, socket.SOCK_DGRAM)
    except Exception:
        return []
    out = []
    for info in infos:
        a = info[4][0]
        if a not in out:
            out.append(a)
    return out

a4 = resolve(socket.AF_INET)
a6 = resolve(socket.AF_INET6)
facts["dns_v4"] = a4
facts["dns_v6"] = a6
if not a4 and not a6:
    findings.append("DNS: %s does not resolve at all" % host)
elif not a6:
    findings.append("DNS: %s has A but NO AAAA — IPv6-preferring clients (cellular, often "
                    "v6-only behind 464XLAT) cannot reach this relay natively; add an AAAA "
                    "record for %s pointing at the host's public IPv6 (if you just added it, a "
                    "NEGATIVE DNS CACHE can still hide it: ipconfig /flushdns on Windows, "
                    "resolvectl flush-caches on Linux)" % (host, host))

# 2. The published map.
node = None
try:
    with urllib.request.urlopen(map_url, timeout=5) as r:
        doc = json.load(r)
    for rid, reg in (doc.get("Regions") or {}).items():
        for n in reg.get("Nodes") or []:
            if (n.get("HostName") or "").lower() == host.lower():
                node = (rid, reg.get("RegionCode"), n)
                break
        if node:
            break
except Exception as e:
    findings.append("MAP: could not read %s (%s) — is this the panel host?" % (map_url, e))

if node:
    rid, code, n = node
    facts["map"] = {"region": rid, "code": code, "name": n.get("Name"),
                    "ipv4": n.get("IPv4") or "", "ipv6": n.get("IPv6") or "",
                    "derp_port": n.get("DERPPort"), "stun_port": n.get("STUNPort")}
    if not n.get("IPv4") and a4:
        findings.append("MAP: node %s carries no IPv4 hint although DNS has %s" % (n.get("Name"), a4[0]))
    if not n.get("IPv6"):
        if a6:
            findings.append("MAP: node %s carries no IPv6 hint although DNS has %s (a client "
                            "then has to guess)" % (n.get("Name"), a6[0]))
        else:
            findings.append("MAP: node %s carries no IPv6 hint — expected, because DNS has no "
                            "AAAA; this is the cellular case, fix DNS first" % n.get("Name"))

# 3. Reachability: the client's own STUN packet shape, per family.
if not helper or not os.path.exists(helper):
    findings.append("STUN: not probed — %s is missing (run this from a checkout, not through a "
                    "pipe: the helper lives beside the script)" % (helper or "scripts/lib/stun_probe.py"))

def stun(addr, fam):
    if not addr or not helper or not os.path.exists(helper):
        return None
    if fam == socket.AF_INET6:
        target = addr
    else:
        target = addr
    try:
        p = subprocess.run([sys.executable, helper, target, str(stun_port)],
                           capture_output=True, text=True, timeout=10)
        return {"addr": addr, "rc": p.returncode, "detail": (p.stdout or p.stderr).strip()[:160]}
    except Exception as e:
        return {"addr": addr, "rc": -1, "detail": str(e)[:160]}

reach = {}
v4probe = stun(a4[0] if a4 else "", socket.AF_INET)
if v4probe is not None:
    reach["v4"] = v4probe
    if v4probe["rc"] != 0:
        findings.append("STUN: no Binding Success over IPv4 to %s:%d (rc=%s) — the relay may be "
                        "down or the port blocked" % (v4probe["addr"], stun_port, v4probe["rc"]))
if a6:
    v6probe = stun(a6[0], socket.AF_INET6)
    if v6probe is not None:
        reach["v6"] = v6probe
        if v6probe["rc"] != 0:
            findings.append("STUN: no Binding Success over IPv6 to [%s]:%d (rc=%s) — an AAAA that "
                            "does not answer is worse than no AAAA: v6-preferring clients will try "
                            "it and stall" % (v6probe["addr"], stun_port, v6probe["rc"]))
facts["reachability"] = reach

# 4. The local trace: DERP re-connections and netcheck runs.
def journal_count(pattern, unit="tailscaled"):
    try:
        p = subprocess.run(["journalctl", "-u", unit, "--since", "%d hours ago" % hours,
                            "--no-pager", "-o", "cat"], capture_output=True, text=True, timeout=30)
        text = p.stdout or ""
    except Exception:
        return None, ""
    return text.count(pattern), text

derp_reconnects, _ = journal_count("connected; connGen")
netcheck_probes, _ = journal_count("runProbe")
facts["trace"] = {"hours": hours, "derp_connected_lines": derp_reconnects, "netcheck_probe_lines": netcheck_probes}
if derp_reconnects is None:
    findings.append("TRACE: journalctl unavailable — cannot count relay re-connections on this host")
elif derp_reconnects >= 12 * hours:
    findings.append("TRACE: this host re-established DERP %d time(s) in %dh (>= %d/h) — the relay path "
                    "is being re-selected often; on a cellular client that is felt as a stalled "
                    "stream" % (derp_reconnects, hours, 12))

if json_out == "1":
    facts["findings"] = findings
    print(json.dumps(facts, indent=2, ensure_ascii=False))
else:
    print("derp_path_audit — %s (last %dh)" % (host, hours))
    print("read-only: it changes no DNS record, no map and no tailscale state")
    print()
    print("  DNS      A=%s" % (", ".join(a4) or "(none)"))
    print("           AAAA=%s" % (", ".join(a6) or "(none)"))
    if node:
        rid, code, n = node
        print("  MAP      region %s (%s) node %s" % (rid, code, n.get("Name")))
        print("           IPv4=%s IPv6=%s DERP=%s STUN=%s" % (
            n.get("IPv4") or "(absent)", n.get("IPv6") or "(absent)",
            n.get("DERPPort"), n.get("STUNPort")))
    print("  STUN     %s" % (" ".join("%s:%s" % (k, v["rc"]) for k, v in reach.items()) or "(not probed)"))
    if facts.get("trace"):
        t = facts["trace"]
        print("  TRACE    derp re-connects=%s netcheck probes=%s in %dh" % (
            t["derp_connected_lines"], t["netcheck_probe_lines"], t["hours"]))
    print()
    if findings:
        print("%d finding(s):" % len(findings))
        for f in findings:
            print("  ! " + f)
    else:
        print("No findings: DNS, the published map and reachability agree.")
    print()
    print("NOTE: only IPv6-preferring clients are affected by a missing AAAA, and those are")
    print("      mostly on cellular. Wi-Fi clients take a direct path and never see the DERP")
    print("      map at all — which is exactly why this class goes unnoticed until someone")
    print("      uses mobile data.")
PY
rc=$?
case "$rc" in
  0|141) exit 0 ;;   # 141 = reader closed the pipe; not a failure of the audit
  *) exit "$rc" ;;
esac
