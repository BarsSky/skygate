#!/usr/bin/env python3
"""A STUN probe that speaks the shape derper actually answers.

WHY THIS FILE EXISTS, AND WHY IT IS A CORRECTION.
-----------------------------------------------
The first version of the shell audit (and the first diagnosis handed to the operator) sent
a bare 20-byte Binding Request and read the silence as «STUN is broken, the relay binary is
at fault». That was WRONG — and skygate's own Go probe in `internal/feature/admin/derp_stun.go`
had already been fixed for exactly this in B265, with the reason written in its comment
(`ErrNoFingerprint` → `not_stun`, «the tile showed STUN UDP :3478 closed on a relay whose
STUN answers real clients»). Two implementations of one protocol in one product, and only
the shell one repeated the bug. The measurement that settled it — derper's own
`/debug/vars` counters plus a tcpdump of a real client:

    SOFTWARE "tailnode" + FINGERPRINT  -> reply 0x0101, counter `success`    +1   (3/3)
    SOFTWARE "skygate"  + FINGERPRINT  -> TIMEOUT,    counter `not_stun`     +1   (3/3)
    bare 20-byte Binding Request       -> TIMEOUT,    counter `not_stun`     +1

So the relay is healthy (`success` was already 600+ when we looked, and one
`tailscale netcheck` on the host moved it 608 → 616), and Tailscale's STUN server serves
requests that carry the standard client SOFTWARE value, counting every other packet as
`not_stun`. A prober that does not mimic a client measures nothing about the server.

Exit codes: 0 answered · 1 not a Binding Success · 2 no reply · 3 probe error.
"""
import binascii
import os
import socket
import struct
import sys

MAGIC = 0x2112A442
BINDING_REQUEST = 0x0001
BINDING_SUCCESS = 0x0101
ATTR_SOFTWARE = 0x8022
ATTR_FINGERPRINT = 0x8028
FINGERPRINT_XOR = 0x5354554E

# The value a real Tailscale client sends. Measured: the server answers THIS and counts
# anything else as `not_stun`, so it is part of the probe, not decoration.
CLIENT_SOFTWARE = b"tailnode"


def build_request(software=CLIENT_SOFTWARE):
    tid = os.urandom(12)
    pad = (4 - len(software) % 4) % 4
    attrs = struct.pack("!HH", ATTR_SOFTWARE, len(software)) + software + b"\x00" * pad
    total = len(attrs) + 8  # + FINGERPRINT: header (4) + value (4)
    hdr = struct.pack("!HHI", BINDING_REQUEST, total, MAGIC) + tid
    pre = hdr + attrs
    crc = (binascii.crc32(pre) & 0xFFFFFFFF) ^ FINGERPRINT_XOR
    return pre + struct.pack("!HH", ATTR_FINGERPRINT, 4) + struct.pack("!I", crc), tid


def probe(host, port=3478, timeout=4.0):
    pkt, tid = build_request()
    # B510: the family comes from the TARGET, not from an assumption. Pre-B510 this was a
    # hardcoded AF_INET socket, so probing an IPv6 address failed with "Address family for
    # hostname not supported" and the audit reported a healthy relay as unreachable over v6
    # - the same class of false negative this file was written to end (a prober that cannot
    # speak the target's family measures nothing about the server).
    fam, family_err = socket_family_for(host)
    if family_err is not None:
        return 3, family_err
    s = socket.socket(fam, socket.SOCK_DGRAM)
    s.settimeout(timeout)
    try:
        s.sendto(pkt, (host, port))
        while True:
            data, addr = s.recvfrom(2048)
            if len(data) < 20:
                continue
            mtype, _mlen, mmagic = struct.unpack("!HHI", data[:8])
            if mmagic != MAGIC or data[8:20] != tid:
                continue
            if mtype == BINDING_SUCCESS:
                return 0, "STUN answers on %s:%d (%d bytes, Binding Success from %s)" % (
                    host, port, len(data), addr[0])
            return 1, "a reply arrived but it is type 0x%04x, not a Binding Success" % mtype
    except socket.timeout:
        return 2, "no reply on %s:%d within %.0fs (the port may be bound but silent, " \
                  "or the packet was classified `not_stun`)" % (host, port, timeout)
    except Exception as e:  # noqa: BLE001 - the audit needs the reason, not a traceback
        return 3, "probe error: %s" % e
    finally:
        s.close()


def socket_family_for(host):
    """AF_INET for a v4 target, AF_INET6 for a v6 one, resolved by the usual rules."""
    try:
        infos = socket.getaddrinfo(host, None, type=socket.SOCK_DGRAM)
    except socket.gaierror as e:
        return None, "cannot resolve %s: %s" % (host, e)
    for info in infos:
        if info[0] in (socket.AF_INET, socket.AF_INET6):
            return info[0], None
    return None, "no usable address family for %s" % host


if __name__ == "__main__":
    h = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
    p = int(sys.argv[2]) if len(sys.argv) > 2 else 3478
    rc, msg = probe(h, p)
    print(msg)
    sys.exit(rc)
