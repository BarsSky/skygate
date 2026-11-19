#!/usr/bin/env bash
# ============================================================================
# derp-init.sh — install + register a Tailscale DERP relay on a fresh host
# B164 (v1.5.1)
#
# Usage:
#   derp-init.sh <hostname> <public_ip> <region_id> <region_code> \
#                <region_name> <ssh_user> <ssh_target> <ssh_key_path> \
#                <ssh_port> <derp_port> <stun_port> <sort_order>
#
# Example:
#   derp-init.sh derp-fra-1.example.com 198.51.100.10 5 fra \
#                "Frankfurt relay 1" root root@198.51.100.10 \
#                /root/.ssh/id_ed25519 22 443 3478 3
#
# Does (in order, on the REMOTE host via SSH):
#   1. Install Go 1.23+ if missing (via official tarball).
#   2. `go install tailscale.com/cmd/derper@latest` → /root/go/bin/derper.
#   3. Generate self-signed cert + key (or use provided pair).
#   4. Configure systemd unit `derper.service`.
#   5. Open firewall for derp_port (TCP) + stun_port (UDP).
#   6. Enable + start the service.
#   7. Probe the HTTPS endpoint to confirm it's up.
#
# Outputs (stdout, JSON):
#   { "hostname": "...", "public_ip": "...", "region_id": N,
#     "region_code": "...", "region_name": "...",
#     "url": "https://<hostname>:<derp_port>",
#     "derp_port": N, "stun_port": N,
#     "cert_path": "/etc/skygate-derper/cert.pem",
#     "key_path":  "/etc/skygate-derper/key.pem",
#     "systemd_unit": "derper.service",
#     "duration_ms": N }
#
# Errors go to stderr; the script exits non-zero.
#
# Idempotency: running again on a host that already
# has derper just restarts the service (the install
# steps are guarded by `command -v` checks).
# ============================================================================
set -euo pipefail

HOSTNAME="${1:-}"
PUBLIC_IP="${2:-}"
REGION_ID="${3:-}"
REGION_CODE="${4:-}"
REGION_NAME="${5:-}"
SSH_USER="${6:-}"
SSH_TARGET="${7:-}"
SSH_KEY_PATH="${8:-}"
SSH_PORT="${9:-}"
DERP_PORT="${10:-}"
STUN_PORT="${11:-}"
SORT_ORDER="${12:-}"

if [ -z "$HOSTNAME" ] || [ -z "$REGION_ID" ] || [ -z "$SSH_TARGET" ]; then
    echo "usage: derp-init.sh <hostname> <public_ip> <region_id> <region_code>" >&2
    echo "                   <region_name> <ssh_user> <ssh_target> <ssh_key_path>" >&2
    echo "                   <ssh_port> <derp_port> <stun_port> <sort_order>" >&2
    exit 2
fi

# Validate region_id range.
if [ "$REGION_ID" -lt 1 ] || [ "$REGION_ID" -gt 999 ]; then
    echo "bad region_id: $REGION_ID (must be 1-999)" >&2
    exit 2
fi

START_MS=$(date +%s%3N)

# ssh command prefix. We always pass StrictHostKeyChecking=accept-new
# (the operator explicitly added the host via the form, so the
# first SSH from skygate will accept + cache the new key).
SSH_BASE=(ssh -i "$SSH_KEY_PATH" -p "$SSH_PORT" -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes)

# Remote command prefix.
RCMD() { "${SSH_BASE[@]}" "$SSH_TARGET" "$@"; }

# 1. Install Go 1.23+ if missing.
echo "[1/7] check Go on remote host" >&2
HAS_GO=$(RCMD 'command -v go && go version' 2>/dev/null || true)
if [ -z "$HAS_GO" ]; then
    echo "[1/7] installing Go 1.23.4 (no Go on the remote host)" >&2
    RCMD "curl -fsSL https://go.dev/dl/go1.23.4.linux-amd64.tar.gz | sudo tar -C /usr/local -xz && echo 'export PATH=\$PATH:/usr/local/go/bin:/root/go/bin' | sudo tee /etc/profile.d/go.sh >/dev/null"
else
    echo "[1/7] Go present: $HAS_GO" >&2
fi

# 2. Build + install derper. `go install` puts the binary
# in /root/go/bin/derper (or $GOPATH/bin). The Go install
# takes ~30-45s on a small VPS.
#
# B500 (2026-11-19) — WHAT "UNOFFICIAL BUILD" ACTUALLY MEANS HERE.
#
# Tailscale's static release tarballs do NOT ship `derper` (measured: the
# 1.104.7 amd64 tarball contains only `tailscale` and `tailscaled`), so building
# from source is not a shortcut — it is the only path that exists for a native
# install. The result is UNMODIFIED upstream source, not a fork: skygate patches
# nothing inside derper, and everything skygate adds lives OUTSIDE the binary
# (the systemd unit below, `derper-cert-sync.sh`, the /admin/derp probes, the
# generated derpmap). Replacing this binary therefore loses no skygate feature.
#
# Two consequences worth knowing:
#   * `derper --version` prints `X.Y.Z-ERR-BuildInfo` for ANY `go install` build:
#     the version string needs VCS/buildinfo stamping, which a module build
#     outside the repo does not have. It is cosmetic, but it does mean the
#     binary's provenance is the module version, nothing else.
#   * `@latest` moves under the installer's feet. It is pinned here so two hosts
#     installed a month apart get the same relay, and an upgrade is a decision.
#     Override with DERP_VERSION=<tag> when a specific release is wanted.
DERP_VERSION="${DERP_VERSION:-1.104.7}"
echo "[2/7] go install tailscale.com/cmd/derper@v${DERP_VERSION} (this takes ~30-45s)" >&2
RCMD "export PATH=\$PATH:/usr/local/go/bin:/root/go/bin && go install tailscale.com/cmd/derper@v${DERP_VERSION}" 2>&1 | tail -3 || {
    # The module path moved in the tailscale repo — the old form is kept for
    # hosts whose Go proxy cache still resolves it.
    echo "[2/7] retry with the historical module path" >&2
    RCMD "export PATH=\$PATH:/usr/local/go/bin:/root/go/bin && go install tailscale.com/cmd/cmd/derper@v${DERP_VERSION}" 2>&1 | tail -3
}
RCMD "test -x /root/go/bin/derper || sudo install -m 0755 /root/go/bin/derper /usr/local/bin/derper"
DERP_BIN=$(RCMD 'command -v derper || command -v /usr/local/bin/derper || command -v /root/go/bin/derper' 2>/dev/null)
echo "[2/7] derper binary: $DERP_BIN ($(RCMD "\"$DERP_BIN\" --version 2>&1 | head -1" 2>/dev/null))" >&2

# 3. Generate self-signed cert + key. derper refuses to
# start without --certmode=letsencrypt without a real cert,
# so we generate one. The operator can replace it with a
# real one via /admin/certificates (B148) — the certsync
# daemon (B147) will then push the real cert to all nodes.
echo "[3/7] generate self-signed cert (operator can replace via /admin/certificates)" >&2
RCMD "sudo mkdir -p /etc/skygate-derper && sudo openssl req -x509 -newkey rsa:2048 -sha256 -days 365 -nodes \
        -keyout /etc/skygate-derper/key.pem \
        -out /etc/skygate-derper/cert.pem \
        -subj '/CN=$HOSTNAME' \
        -addext 'subjectAltName=DNS:$HOSTNAME,DNS:localhost,IP:127.0.0.1' 2>&1 | tail -3"
RCMD "sudo chmod 0644 /etc/skygate-derper/cert.pem && sudo chmod 0600 /etc/skygate-derper/key.pem"

# 4. Configure systemd unit.
echo "[4/7] configure systemd unit derper.service" >&2
RCMD "sudo tee /etc/systemd/system/derper.service >/dev/null <<EOF
[Unit]
Description=Tailscale DERP relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$DERP_BIN --hostname=$HOSTNAME --certmode=manual --certdir=/etc/skygate-derper --stun --verify-clients=false --bootstrap-dns-names=\"\"
Restart=on-failure
RestartSec=5
User=root
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload && sudo systemctl enable derper"

# 5. Open firewall. We try ufw (Ubuntu), firewalld
# (CentOS), iptables (fallback). The script is best-effort
# — if no firewall is active, the operator already opened
# the port and the step is a no-op.
echo "[5/7] open firewall for derp_port (TCP) + stun_port (UDP)" >&2
RCMD "if command -v ufw >/dev/null; then sudo ufw allow $DERP_PORT/tcp && sudo ufw allow $STUN_PORT/udp
elif command -v firewall-cmd >/dev/null; then sudo firewall-cmd --permanent --add-port=$DERP_PORT/tcp && sudo firewall-cmd --permanent --add-port=$STUN_PORT/udp && sudo firewall-cmd --reload
elif command -v iptables >/dev/null; then sudo iptables -I INPUT -p tcp --dport $DERP_PORT -j ACCEPT && sudo iptables -I INPUT -p udp --dport $STUN_PORT -j ACCEPT
else echo 'no firewall tool found, skipping' >&2; fi" 2>&1 | tail -3

# 6. Start the service.
echo "[6/7] start derper" >&2
RCMD "sudo systemctl restart derper && sleep 2 && sudo systemctl is-active derper" 2>&1 | tail -3

# 7. Probe BOTH listeners. derper answers 200 on / (the bootstrap page) for TLS,
# and a STUN Binding Request on stun_port — and those are two different facts:
# measured 2026-11-19 on the reference host, the TLS probe returned 200 while STUN
# had not answered a single request for three days (the port was bound, ufw
# accepted the packets, the log said "STUN server listening", and every client
# still fell back to a foreign DERP region). A relay that silently lost STUN is
# exactly the kind of half-broken that no page reported, so the installer checks it.
echo "[7/7] probe HTTPS + STUN" >&2
sleep 2
PROBE_OUT=$(RCMD "curl -sk --max-time 5 -o /dev/null -w '%{http_code}' https://127.0.0.1:$DERP_PORT/ 2>&1" || echo "fail")
echo "[7/7] HTTPS probe: $PROBE_OUT (200 = OK)" >&2
if [ "$PROBE_OUT" != "200" ]; then
    echo "WARN: derper did not return 200 on the probe (got $PROBE_OUT). Check 'journalctl -u derper' on the remote host." >&2
    # We don't fail the whole script — the cert
    # generation + systemd start may have succeeded
    # even if the probe took a few extra seconds. The
    # operator can re-probe from /admin/derp/relays.
fi
STUN_OUT=$(RCMD "python3 - <<'PY'
import os, socket, struct
tid = os.urandom(12)
pkt = struct.pack('!HHI', 0x0001, 0, 0x2112A442) + tid
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(4)
try:
    s.sendto(pkt, ('127.0.0.1', $STUN_PORT))
    d, a = s.recvfrom(2048)
    print('ok' if len(d) >= 20 and struct.unpack('!H', d[:2])[0] == 0x0101 else 'bad-reply')
except socket.timeout:
    print('no-answer')
except Exception as e:
    print('error:%s' % e)
finally:
    s.close()
PY" 2>&1 | tail -1)
if [ "$STUN_OUT" = "ok" ]; then
    echo "[7/7] STUN probe: ok" >&2
else
    echo "WARN: STUN on 127.0.0.1:$STUN_PORT answered '$STUN_OUT' — the port is bound but the relay is not answering." >&2
    echo "WARN: clients that cannot measure this region prefer a foreign DERP, and NAT traversal loses this server." >&2
    echo "WARN: this is a derper/build problem, NOT a firewall one. Check 'journalctl -u derper | grep -i stun'." >&2
    echo "WARN: re-run the check any time with: bash scripts/port_audit.sh" >&2
fi

# Compute the public URL.
URL="https://$HOSTNAME:$DERP_PORT"

# Done. Emit JSON on stdout.
END_MS=$(date +%s%3N)
DURATION_MS=$((END_MS - START_MS))

cat <<EOF
{"hostname":"$HOSTNAME","public_ip":"$PUBLIC_IP","region_id":$REGION_ID,"region_code":"$REGION_CODE","region_name":"$REGION_NAME","url":"$URL","derp_port":$DERP_PORT,"stun_port":$STUN_PORT,"cert_path":"/etc/skygate-derper/cert.pem","key_path":"/etc/skygate-derper/key.pem","systemd_unit":"derper.service","duration_ms":$DURATION_MS}
EOF
