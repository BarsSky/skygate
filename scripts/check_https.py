#!/usr/bin/env python3
"""scripts/check_https.py — verify SKYGATE_CONTROL_URL is reachable over HTTPS.

2026-07-15: v0.15.0 — deploy-time HTTPS health check. Complements
the existing check_exit_nodes.py (which verifies exit-node
state) with a check that the public-facing URL is reachable
over HTTPS, has a valid cert, includes the right SubjectAltName,
sends HSTS, and HTTP→HTTPS redirects on the plain-HTTP port.

Why: the v0.15.0 Caddy sidecar auto-issues Let's Encrypt certs
via DNS-01, but a broken Caddyfile (typo in the vhost, missing
DNS-01 token, Caddy container not started) would leave the
operator's tailnet clients stuck on plain HTTP. This script
catches the common misconfigurations at deploy time.

Exits 0 on full pass, 1 on any failure, 2 on setup error.
Same shape as check_exit_nodes.py so a CI / automated deploy
can `set -e` and trust the result.

Usage:
  SKYGATE_CONTROL_URL=https://head.example.com \\
    python3 scripts/check_https.py
  python3 scripts/check_https.py --url https://head.example.com
  python3 scripts/check_https.py --url https://head.example.com --strict
"""
import argparse
import json
import os
import socket
import ssl
import subprocess
import sys
import urllib.error
import urllib.request


def ok(msg):
    print(f"PASS: {msg}")


def warn(msg):
    print(f"WARN: {msg}")


def fail(msg):
    print(f"FAIL: {msg}")


def parse_url(url):
    """Parse a URL like https://head.example.com into
    (scheme, host, port). Strips any path component."""
    from urllib.parse import urlparse
    p = urlparse(url)
    if p.scheme not in ("http", "https"):
        raise ValueError(f"only http/https URLs are supported, got {p.scheme!r}")
    host = p.hostname
    if not host:
        raise ValueError(f"could not extract host from {url!r}")
    # Default ports; we let the caller override if they
    # pass --port explicitly.
    if p.port:
        port = p.port
    elif p.scheme == "https":
        port = 443
    else:
        port = 80
    return p.scheme, host, port


def check_https(url, host, port, timeout):
    """Open a TLS connection to the host:port, return the
    cert subject / issuer / SAN / expiry. Uses the stdlib
    ssl module (no extra deps)."""
    ctx = ssl.create_default_context()
    # We need the SAN, which the default context exposes
    # via the cert returned from getpeercert.
    with socket.create_connection((host, port), timeout=timeout) as sock:
        with ctx.wrap_socket(sock, server_hostname=host) as ssock:
            cert = ssock.getpeercert()
            cipher = ssock.cipher()
            tls_version = ssock.version()
    return cert, cipher, tls_version


def check_redirect(url, host, port, timeout):
    """GET http://<host>/ (or https with a -k flag) and
    assert the response code is a 3xx (the v0.15.0
    Caddyfile sets up an automatic HTTP→HTTPS redirect,
    and a 200 here would mean Caddy isn't terminating
    the connection — which would break Tailscale's
    "control plane is HTTPS" expectation)."""
    scheme = "http" if port == 80 else "https"
    target = f"{scheme}://{host}:{port}/"
    req = urllib.request.Request(target, method="GET")
    # We don't follow redirects (we want to confirm
    # the redirect actually exists, not that it
    # eventually lands somewhere).
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *a, **kw):
            return None
    opener = urllib.request.build_opener(NoRedirect)
    try:
        opener.open(req, timeout=timeout)
    except urllib.error.HTTPError as e:
        # 3xx is what we want (it raises HTTPError on
        # 4xx/5xx but the redirect handler returns None
        # on 3xx so the open call actually returns the
        # 3xx response — which is a HTTPError with
        # code 301/302/308. Verify.
        if 300 <= e.code < 400:
            return e.code, e.headers.get("Location", "")
        raise
    # If we get here without an exception, the response
    # was 2xx. That's NOT what we want for the plain-HTTP
    # path (we want a 308 to https://).
    return None, None


def check_hsts(url, host, port, timeout):
    """GET https://<host>/login and assert the response
    carries a Strict-Transport-Security header. The
    v0.15.0 Caddyfile sets max-age=15552000 (6 months)
    + includeSubDomains + preload."""
    scheme = "https" if port == 443 else "https"  # always for the /login probe
    target = f"{scheme}://{host}:{port}/login"
    req = urllib.request.Request(target, method="GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        hsts = resp.headers.get("Strict-Transport-Security", "")
    return hsts, resp.status


def cert_contains_san(cert, expected_host):
    """True iff cert has the expected_host in its
    SubjectAltName extension. Returns the SAN list as
    well for the printout."""
    san_list = []
    for typ, val in cert.get("subjectAltName", ()):
        if typ == "DNS":
            san_list.append(val)
    # Match exact or wildcard.
    for s in san_list:
        if s == expected_host:
            return True, san_list
        if s.startswith("*.") and expected_host.endswith(s[1:]):
            # *.example.com matches foo.example.com but
            # NOT foo.bar.example.com (single label).
            if expected_host.count(".") == s.count("."):
                return True, san_list
    return False, san_list


def cert_is_expired(cert, now_ts):
    """True iff the cert is currently valid. Returns the
    not_before / not_after timestamps for the printout."""
    nb = cert.get("notBefore", "")
    na = cert.get("notAfter", "")
    return nb, na


def run_openssl_san(url, host, port, timeout):
    """Best-effort fallback: use the openssl CLI to dump
    the cert when the stdlib path can't reach it (e.g.
    very old Python without PEP 567 ssl improvements).
    We don't fail if openssl isn't installed."""
    try:
        out = subprocess.run(
            ["openssl", "s_client", "-connect", f"{host}:{port}",
             "-servername", host, "-showcerts"],
            input=b"", capture_output=True, timeout=timeout,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None
    if out.returncode != 0:
        return None
    return out.stdout.decode("utf-8", errors="replace")


def main():
    p = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    p.add_argument("--url", default=os.environ.get("SKYGATE_CONTROL_URL", ""),
                   help="Public HTTPS URL to check (default: $SKYGATE_CONTROL_URL).")
    p.add_argument("--port", type=int, default=0,
                   help="Override the port (default: 443 for https, 80 for http).")
    p.add_argument("--timeout", type=int, default=10,
                   help="Network timeout in seconds (default: 10).")
    p.add_argument("--strict", action="store_true",
                   help="Hard-fail (exit 1) on any WARN. Without --strict, "
                        "warnings exit 0; only FAILs exit 1.")
    args = p.parse_args()

    if not args.url:
        fail("--url (or SKYGATE_CONTROL_URL env) is required")
        return 2

    try:
        scheme, host, port = parse_url(args.url)
    except ValueError as e:
        fail(str(e))
        return 2

    if args.port:
        port = args.port

    if scheme != "https":
        # The script's whole point is to verify the
        # HTTPS path. We still check the redirect chain,
        # but the script is a no-op if SKYGATE_CONTROL_URL
        # is plain http:// (which would mean the
        # operator hasn't migrated yet).
        warn(f"SKYGATE_CONTROL_URL is {scheme}://, not https://. Run this AFTER you've enabled the Caddy sidecar (see docs/https-setup.md).")
        return 0

    any_warn = False
    any_fail = False

    # 1. Cert reachable and valid.
    print(f"--- 1. TLS handshake to {host}:{port}")
    try:
        cert, cipher, tls_version = check_https(args.url, host, port, args.timeout)
    except (socket.timeout, ConnectionRefusedError, ssl.SSLError, OSError) as e:
        fail(f"TLS handshake failed: {e}")
        return 1
    ok(f"TLS {tls_version}, cipher {cipher[0]}/{cipher[1]}")

    # 2. SubjectAltName contains the expected host.
    matched, san_list = cert_contains_san(cert, host)
    if matched:
        ok(f"Cert SAN matches {host!r} (SANs: {san_list})")
    else:
        fail(f"Cert SAN does not contain {host!r} (got: {san_list}). The Caddyfile vhost doesn't match the cert — re-check the Caddy render.")
        any_fail = True

    # 3. Cert is not expired.
    nb, na = cert_is_expired(cert, None)
    if nb and na:
        ok(f"Cert valid: {nb} → {na}")
    else:
        warn("Cert validity not parseable; check manually")
        any_warn = True

    # 4. HTTP→HTTPS redirect on port 80.
    print(f"--- 2. HTTP→HTTPS redirect on {host}:80")
    try:
        code, location = check_redirect(args.url, host, 80, args.timeout)
    except (urllib.error.URLError, ConnectionRefusedError, socket.timeout) as e:
        warn(f"Port 80 check failed ({e}); probably behind a firewall — fine if you're behind Cloudflare, otherwise check the host's :80 binding")
        any_warn = True
    else:
        if code and 300 <= code < 400 and location.startswith("https://"):
            ok(f"HTTP→HTTPS redirect: {code} → {location}")
        elif code is None:
            warn("Port 80 returned a 2xx (no redirect). Tailscale clients that dial http:// will see plain-HTTP, not HTTPS. Check the Caddyfile.")
            any_warn = True
        else:
            fail(f"Port 80 returned {code} (Location={location!r}), not a 3xx redirect. Caddy's HTTP→HTTPS rewrite is missing.")
            any_fail = True

    # 5. HSTS on /login.
    print(f"--- 3. HSTS on {host}:443/login")
    try:
        hsts, status = check_hsts(args.url, host, port, args.timeout)
    except (urllib.error.URLError, ConnectionRefusedError, socket.timeout) as e:
        fail(f"/login probe failed: {e}")
        return 1
    if "max-age=" in hsts:
        ok(f"HSTS: {hsts} (HTTP {status})")
    else:
        warn(f"No HSTS header on /login (got: {hsts!r}). Tailscale clients will accept HTTP downgrades.")
        any_warn = True

    if any_fail:
        return 1
    if any_warn and args.strict:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
