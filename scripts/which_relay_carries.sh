#!/usr/bin/env bash
# which_relay_carries.sh — B509 (2026-11-25)
#
# WHICH RELAY ACTUALLY CARRIES THE TRAFFIC OF *THIS* MACHINE, RIGHT NOW.
#
# WHY THIS EXISTS. Asked "which exit node does device X go through?", we answered three
# times and each answer was different, because each time we measured a single echo
# endpoint and read the answer as a property of the machine:
#
#   * an endpoint hosted behind a CDN whose address range a relay advertises resolves
#     into that relay's PREFIX, and a specific prefix always beats the selected
#     exit node — so the traffic left through a relay the person did not choose;
#   * an endpoint with no covering prefix followed the selected exit node;
#   * the peer byte counters the first two answers were built on count ALL traffic to a
#     peer (prefix routes AND exit traffic), so they cannot separate the two.
#
# Measured on one host, with ONE exit node selected, seven endpoints, five answers:
# three left through the selected relay and three through a different one. The
# machine was fine; the question was under-specified.
#
# WHAT IT DOES. For every endpoint it reports:
#   dest      — the address the name resolves to,
#   carrier   — the relay PREDICTED from the tailnet's own routes: the most specific
#               advertised prefix that contains `dest` (a prefix route wins over the
#               exit node), otherwise the selected exit node,
#   kind      — `prefix` or `exit` or `direct`,
#   observed  — the address the endpoint actually saw,
#   match     — whether `observed` belongs to the predicted relay.
#
# The prediction comes from `tailscale status --json` (each peer's PrimaryRoutes) and
# `tailscale debug prefs` (the selected exit node + accept-routes). The observation is
# a real request from this machine. `match` needs to know the relays' PUBLIC addresses;
# pass them in SKYGATE_RELAY_PUBLIC_IPS (`relay-a=203.0.113.10,relay-b=203.0.113.20`)
# and the tool names the relay instead of printing a bare address. Without that map it
# still shows the observed address — the reader can compare it by eye, and `?` says
# honestly that the tool did not attribute it itself.
#
# THE MODEL IT PREDICTS WITH (both halves measured on the reference tailnet):
#   * exit node selected — the packet first reaches the exit relay, and that relay
#     forwards it to the prefix's owner INSIDE the tailnet, so the most specific
#     covering prefix decides the final egress address even with accept-routes OFF;
#   * no exit node, accept-routes ON — the client installs the peer routes itself, so
#     the covering prefix carries the traffic;
#   * no exit node, accept-routes OFF — the client installs no peer routes: the prefix
#     EXISTS in the netmap but is not in effect and the egress is direct (the tool prints
#     that as a note rather than pretending the prefix applied).
# Consequence worth stating out loud: on this kind of tailnet the honest answer to
# "which relay does this machine go through" is a PER-DESTINATION answer, and the
# selected exit node only governs the destinations that no advertised prefix covers.
#
# HONESTY RULES BAKED IN:
#   * READ-ONLY. It never runs `tailscale set`, never touches routes, prefs or policy.
#     Setting an exit node on a host that someone is SSH'd into moves that host's egress
#     and can kill the session — a diagnostic must not be able to do that.
#   * IT MEASURES THE MACHINE IT RUNS ON. There is no way to observe another device's
#     egress from here; run it there (or over the tailnet). `--device` exists only to
#     remind the caller of that.
#   * A MISMATCH IS A FINDING, NOT AN ERROR: exit status stays 0 unless `--fail-on-mismatch`.
#   * It names no host, address or tailnet of its own: everything comes from the live
#     tailnet or from the fixtures.
#
# OFFLINE TESTING (used by check_b509_which_relay_carries.sh):
#   --status-file FILE    use this `tailscale status --json` document instead of the live one
#   --prefs-file FILE     use this `tailscale debug prefs` document instead of the live one
#   --observed-file FILE  TSV `URL<TAB>dest-ip<TAB>observed-ip` instead of probing the network
#   --no-probe            resolve the names but do not make requests
#
# REQUIRES: bash, python3 (JSON + CIDR arithmetic). curl is needed only for probing.
#
# USAGE
#   bash scripts/which_relay_carries.sh
#   SKYGATE_RELAY_PUBLIC_IPS='relay-a=203.0.113.10' bash scripts/which_relay_carries.sh
#   bash scripts/which_relay_carries.sh --endpoint https://example.com/ip --json
#   bash scripts/which_relay_carries.sh --device my-laptop      # reminder: run it THERE

set -uo pipefail

# Default endpoint set, deliberately HETEROGENEOUS: the first three are not in the
# CDN ranges this project's rules pin (so they follow the selected exit node), the next
# three are, so on a tailnet with pinned CDN prefixes the two groups answer differently —
# which is the whole point. Keep them in this order: a reader who stops after the first
# row must not be misled by a CDN endpoint.
DEFAULT_ENDPOINTS=(
  "https://checkip.amazonaws.com"
  "https://ipinfo.io/ip"
  "https://ip.seeip.org"
  "https://api.ipify.org"
  "https://ifconfig.co"
  "https://icanhazip.com"
  "https://api.myip.com"
)

TIMEOUT=8
JSON_OUT=0
FAIL_ON_MISMATCH=0
NO_PROBE=0
STATUS_FILE=""
PREFS_FILE=""
OBSERVED_FILE=""
DEVICE=""
ENDPOINTS=()

usage() {
  sed -n '2,/^set -uo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --endpoint)      shift; [ $# -gt 0 ] || { echo "--endpoint needs a URL" >&2; exit 2; }; ENDPOINTS+=("$1") ;;
    --timeout)       shift; TIMEOUT="${1:-8}" ;;
    --json)          JSON_OUT=1 ;;
    --fail-on-mismatch) FAIL_ON_MISMATCH=1 ;;
    --no-probe)      NO_PROBE=1 ;;
    --status-file)   shift; STATUS_FILE="${1:-}" ;;
    --prefs-file)    shift; PREFS_FILE="${1:-}" ;;
    --observed-file) shift; OBSERVED_FILE="${1:-}" ;;
    --device)        shift; DEVICE="${1:-}" ;;
    -h|--help)       usage; exit 0 ;;
    *) echo "unknown argument: $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done

if [ ${#ENDPOINTS[@]} -eq 0 ]; then
  ENDPOINTS=("${DEFAULT_ENDPOINTS[@]}")
fi

command -v python3 >/dev/null 2>&1 || {
  echo "which_relay_carries: python3 is required (JSON + CIDR arithmetic)" >&2; exit 2; }

if [ -n "$DEVICE" ]; then
  echo "which_relay_carries: measuring THIS machine, not '$DEVICE'." >&2
  echo "  A device's egress can only be observed from that device: run this script there" >&2
  echo "  (or over the tailnet) instead of asking the panel to guess." >&2
fi

# Per-run scratch dir (never a fixed /tmp path — AGENTS trap #13).
SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/which-relay.XXXXXX")" || exit 2
cleanup() { rm -rf "$SKY_TMP"; }
trap cleanup EXIT

# --- inputs: status + prefs ---------------------------------------------------------
if [ -z "$STATUS_FILE" ]; then
  STATUS_FILE="$SKY_TMP/status.json"
  if ! tailscale status --json >"$STATUS_FILE" 2>"$SKY_TMP/status.err"; then
    echo "which_relay_carries: 'tailscale status --json' failed:" >&2
    sed 's/^/  /' "$SKY_TMP/status.err" >&2
    echo "  (run this on a machine that is logged into the tailnet)" >&2
    exit 2
  fi
fi
[ -s "$STATUS_FILE" ] || { echo "which_relay_carries: empty status document: $STATUS_FILE" >&2; exit 2; }

if [ -z "$PREFS_FILE" ]; then
  PREFS_FILE="$SKY_TMP/prefs.json"
  tailscale debug prefs >"$PREFS_FILE" 2>/dev/null || : >"$PREFS_FILE"
fi

# --- resolve a name to an IPv4 address (probe mode only) ----------------------------
resolve4() {
  local host="$1"
  getent ahostsv4 "$host" 2>/dev/null | awk 'NR==1{print $1; exit}' && return 0
  python3 - "$host" <<'PY' 2>/dev/null
import socket, sys
try:
    print(socket.getaddrinfo(sys.argv[1], None, socket.AF_INET)[0][4][0])
except Exception:
    pass
PY
}

first_ipv4() {
  grep -oE '\b([0-9]{1,3}\.){3}[0-9]{1,3}\b' | head -n1
}

# --- observations -------------------------------------------------------------------
OBS="$SKY_TMP/observed.tsv"
: >"$OBS"

if [ -n "$OBSERVED_FILE" ]; then
  cat "$OBSERVED_FILE" >>"$OBS"
else
  HAVE_CURL=0
  command -v curl >/dev/null 2>&1 && HAVE_CURL=1
  for url in "${ENDPOINTS[@]}"; do
    host="${url#*://}"; host="${host%%/*}"
    dest="$(resolve4 "$host")"
    seen=""
    if [ "$NO_PROBE" -eq 0 ] && [ "$HAVE_CURL" -eq 1 ]; then
      body="$(curl -4 -sS --max-time "$TIMEOUT" "$url" 2>/dev/null | tr -d '\r' | head -c 400)"
      seen="$(printf '%s' "$body" | first_ipv4)"
    fi
    printf '%s\t%s\t%s\n' "$url" "$dest" "$seen" >>"$OBS"
  done
fi

# --- join + render ------------------------------------------------------------------
SUMMARY="$SKY_TMP/summary.txt"
: >"$SUMMARY"
python3 - "$STATUS_FILE" "$PREFS_FILE" "$OBS" "$JSON_OUT" "$SUMMARY" <<'PY'
import ipaddress, json, os, sys

status_path, prefs_path, obs_path, json_out, summary_path = (
    sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4] == "1", sys.argv[5])

def die(msg):
    print("which_relay_carries: " + msg, file=sys.stderr)
    sys.exit(2)

try:
    with open(status_path, encoding="utf-8") as f:
        st = json.load(f)
except Exception as e:
    die("cannot parse the status document: %s" % e)

prefs = {}
try:
    if os.path.getsize(prefs_path) > 0:
        with open(prefs_path, encoding="utf-8") as f:
            prefs = json.load(f) or {}
except Exception:
    prefs = {}

peers = st.get("Peer") or {}
if not isinstance(peers, dict):
    peers = {}

def hostname(p):
    h = p.get("HostName") or ""
    if not h:
        dns = p.get("DNSName") or ""
        h = dns.split(".")[0]
    return h or "?"

exit_id = str(prefs.get("ExitNodeID") or "").strip()
exit_name = ""
if exit_id:
    p = peers.get(exit_id)
    exit_name = hostname(p) if p else "node-%s" % exit_id
route_all = bool(prefs.get("RouteAll"))

# prefix -> relay, most specific wins at match time
prefixes = []
for p in peers.values():
    if not isinstance(p, dict):
        continue
    hn = hostname(p)
    for r in (p.get("PrimaryRoutes") or []):
        try:
            prefixes.append((ipaddress.ip_network(str(r), strict=False), str(r), hn))
        except Exception:
            continue

# observed address -> relay, from SKYGATE_RELAY_PUBLIC_IPS
relay_by_ip = {}
for part in (os.environ.get("SKYGATE_RELAY_PUBLIC_IPS") or "").split(","):
    part = part.strip()
    if "=" in part:
        name, addr = part.split("=", 1)
        relay_by_ip[addr.strip()] = name.strip()

rows = []
with open(obs_path, encoding="utf-8") as f:
    for line in f:
        line = line.rstrip("\n")
        if not line:
            continue
        parts = line.split("\t")
        url = parts[0]
        dest = parts[1].strip() if len(parts) > 1 else ""
        seen = parts[2].strip() if len(parts) > 2 else ""

        carrier, kind, cover, note = "", "", "", ""
        try:
            ip = ipaddress.ip_address(dest)
        except Exception:
            ip = None
        best = None
        if ip is not None:
            for net, rstr, hn in prefixes:
                if net.version == ip.version and ip in net:
                    if best is None or net.prefixlen > best[0].prefixlen:
                        best = (net, rstr, hn)
        if best is not None:
            cover = best[1]
        # THE MODEL (measured, see the header):
        #   no exit node + accept-routes off -> the client installs NO peer routes, so a
        #     covering prefix exists but is NOT in effect: egress is direct;
        #   no exit node + accept-routes on  -> the covering prefix carries the traffic;
        #   exit node selected -> the packet first reaches the exit relay, which itself
        #     forwards it to the prefix's owner inside the tailnet, so the most specific
        #     covering prefix decides the FINAL egress even with accept-routes off.
        use_prefix = best is not None and (bool(exit_name) or route_all)
        if use_prefix:
            carrier, kind = best[2], "prefix"
        elif exit_name:
            carrier, kind = exit_name, "exit"
        else:
            carrier, kind = "(no exit node)", "direct"
            if best is not None:
                note = "prefix %s not in effect (accept-routes off)" % best[1]

        who = relay_by_ip.get(seen, "") if seen else ""
        if not seen or not who:
            match = "?"
        elif kind == "direct":
            # A direct row means "this machine's own egress". Name that address `self`
            # (or `direct`) in the map and it is confirmed; an address that belongs to
            # some relay is a real contradiction, because nothing predicted it.
            match = "yes" if who in ("self", "direct", "this-host") else "NO"
        else:
            match = "yes" if who == carrier else "NO"

        rows.append({
            "endpoint": url, "dest": dest, "covering_prefix": cover,
            "predicted": carrier, "kind": kind, "note": note,
            "observed": seen, "observed_relay": who, "match": match,
        })

if json_out:
    print(json.dumps({
        "selected_exit_node": exit_name,
        "accept_routes": route_all,
        "prefix_routes": len(prefixes),
        "relays_with_prefixes": len({h for _, _, h in prefixes}),
        "endpoints": rows,
    }, indent=2))
    sys.exit(0)

print("which_relay_carries — what actually carries this machine's traffic, right now")
print("read-only: it changes no prefs, no routes and no policy")
print()
print("  selected exit node : %s" % (exit_name or "(none — direct egress)"))
print("  accept-routes      : %s" % ("on" if route_all else "off"))
print("  advertised routes  : %d prefix(es) across %d relay(s)" % (
    len(prefixes), len({h for _, _, h in prefixes})))
if not relay_by_ip:
    print("  relay public addrs : not supplied — set SKYGATE_RELAY_PUBLIC_IPS to name the")
    print("                       observed address by relay (match stays '?' otherwise)")
print()
hdr = ("ENDPOINT", "DEST", "CARRIER", "KIND", "COVERING PREFIX", "OBSERVED", "MATCH")
data = [hdr] + [(r["endpoint"], r["dest"] or "-", r["predicted"], r["kind"],
                 r["covering_prefix"] or "-", r["observed"] or "-", r["match"]) for r in rows]
widths = [max(len(str(row[i])) for row in data) for i in range(len(hdr))]
for n, row in enumerate(data):
    cells = [str(c).ljust(widths[i]) for i, c in enumerate(row)]
    line = "  ".join(cells).rstrip()
    if n == 0:
        print(line)
        print("  ".join("-" * w for w in widths))
    else:
        print(line)

mism = [r for r in rows if r["match"] == "NO"]
unknown = [r for r in rows if r["match"] == "?"]
try:
    with open(summary_path, "w", encoding="utf-8") as f:
        f.write("mismatches=%d\nunknown=%d\nrows=%d\n" % (len(mism), len(unknown), len(rows)))
except Exception:
    pass

notes = [r for r in rows if r["note"]]
if notes:
    print()
    for r in notes:
        print("  note: %s — %s" % (r["endpoint"], r["note"]))
print()
if mism:
    print("%d endpoint(s) LEFT THROUGH A DIFFERENT RELAY than the routes predict:" % len(mism))
    for r in mism:
        print("  %s -> %s (predicted %s, observed %s)" % (
            r["endpoint"], r["observed_relay"] or r["observed"], r["predicted"], r["observed"]))
    print("  That is a finding about the tailnet, not a bug in this script.")
elif unknown:
    print("No contradiction measured, but %d row(s) could not be attributed: pass"
          " SKYGATE_RELAY_PUBLIC_IPS to turn '?' into a verdict." % len(unknown))
else:
    print("Every endpoint left through the relay the routes predict.")

if len({r["kind"] for r in rows}) > 1:
    print()
    print("NOTE: rows disagree BY DESIGN on a tailnet with pinned prefixes — a specific")
    print("      advertised prefix (typically a CDN range from a rule) always beats the")
    print("      selected exit node. Read the KIND column before concluding anything.")
PY

rc=$?
# 141 = SIGPIPE: the reader closed the pipe (`| head`, `| less`). A truncated read of a
# diagnostic is not a failure of the diagnostic (AGENTS trap #9 is the same trap in the
# check harness), so only a real error propagates.
case "$rc" in
  0|141) ;;
  *) exit "$rc" ;;
esac

if [ "$FAIL_ON_MISMATCH" -eq 1 ] && [ -s "$SUMMARY" ]; then
  mismatches="$(sed -n 's/^mismatches=//p' "$SUMMARY" | head -n1)"
  if [ "${mismatches:-0}" -gt 0 ]; then
    echo "which_relay_carries: $mismatches endpoint(s) contradicted the prediction" >&2
    exit 1
  fi
fi
exit 0
