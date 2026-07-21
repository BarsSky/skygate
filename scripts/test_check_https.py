#!/usr/bin/env python3
"""scripts/test_check_https.py — unit tests for check_https.py.

2026-07-17: v0.18.1 — regression test for the HSTS
fallback logic. The VM uses openresty (not Caddy as
the docs say) and /login returns 404 there. The
check must fall back to / (which sets HSTS globally)
and report a PASS, not a 404 failure.

Run: python3 scripts/test_check_https.py
Exits 0 on full pass, 1 on any failure.
"""
import http.server
import os
import socket
import ssl
import sys
import tempfile
import threading

sys.path.insert(0, os.path.join(os.path.dirname(__file__)))
import check_https  # noqa: E402


def _free_port():
    """Find a free localhost port for the test server."""
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


HSTS = "max-age=15552000; includeSubDomains; preload"


class _OpenrestyLikeHandler(http.server.BaseHTTPRequestHandler):
    """Mimics the VM's openresty config:
    - /login returns 404 (openresty doesn't route it to skygate)
    - / returns 405 with HSTS (openresty adds HSTS globally)
    - /api/v1/apikey returns 401 with HSTS (headscale rejects without auth)
    """

    def log_message(self, format, *args):
        pass  # silence

    def do_GET(self):
        if self.path == "/login":
            self.send_response(404)
            self.send_header("Server", "openresty")
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.end_headers()
            self.wfile.write(b"not found")
            return
        if self.path == "/":
            self.send_response(405)
            self.send_header("Server", "openresty")
            self.send_header("Strict-Transport-Security", HSTS)
            self.end_headers()
            return
        if self.path == "/api/v1/apikey":
            self.send_response(401)
            self.send_header("Server", "openresty")
            self.send_header("Strict-Transport-Security", HSTS)
            self.send_header("Content-Type", "text/plain; charset=utf-8")
            self.end_headers()
            self.wfile.write(b"unauthorized")
            return
        self.send_response(500)
        self.end_headers()


class _CaddyLikeHandler(http.server.BaseHTTPRequestHandler):
    """Mimics the docs' Caddy config:
    - /login returns 200 with HSTS (Caddy routes everything except API paths to skygate)
    """

    def log_message(self, format, *args):
        pass

    def do_GET(self):
        if self.path == "/login":
            self.send_response(200)
            self.send_header("Server", "Caddy")
            self.send_header("Strict-Transport-Security", HSTS)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.end_headers()
            self.wfile.write(b"<form>login</form>")
            return
        self.send_response(404)
        self.end_headers()


def _make_https(handler_cls):
    """Spin up a local HTTPS server with the given handler.
    Returns (host, port, server_thread)."""
    port = _free_port()

    # Generate a self-signed cert for 127.0.0.1
    import subprocess
    tmpdir = tempfile.mkdtemp()
    cert_path = os.path.join(tmpdir, "cert.pem")
    key_path = os.path.join(tmpdir, "key.pem")
    subprocess.run(
        ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
         "-keyout", key_path, "-out", cert_path, "-days", "1",
         "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1"],
        check=True, capture_output=True,
    )

    httpd = http.server.HTTPServer(("127.0.0.1", port), handler_cls)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(cert_path, key_path)
    httpd.socket = ctx.wrap_socket(httpd.socket, server_side=True)

    t = threading.Thread(target=httpd.serve_forever, daemon=True)
    t.start()
    return "127.0.0.1", port, httpd


def _patched_urlopen():
    """Replace urllib.request.urlopen with a version that
    doesn't verify TLS certs (the test server uses a
    self-signed cert for 127.0.0.1). We patch the module
    attribute on the imported check_https namespace."""
    import urllib.request as _ur
    real_open = _ur.urlopen

    def open_no_verify(req, **kw):
        # Build a context that doesn't verify
        ctx = ssl.create_default_context()
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
        # urlopen accepts context= kwarg in 3.10+; for 3.6+
        # we wrap with opener that uses HTTPSHandler
        from urllib.request import build_opener, HTTPSHandler
        opener = build_opener(HTTPSHandler(context=ctx))
        return opener.open(req, **kw)

    check_https.urllib.request.urlopen = open_no_verify
    return lambda: setattr(check_https.urllib.request, "urlopen", real_open)


def test_openresty_fallback_to_root():
    """The VM's openresty config: /login 404s but HSTS is set on /.
    check_hsts must fall back and return HSTS via /."""
    restore = _patched_urlopen()
    host, port, httpd = _make_https(_OpenrestyLikeHandler)
    try:
        hsts, status, path = check_https.check_hsts(
            f"https://{host}:{port}", host, port, timeout=3,
        )
        assert "max-age=" in hsts, f"expected HSTS, got {hsts!r}"
        assert path in ("/", "/login", "/api/v1/apikey"), f"unexpected fallback path: {path}"
        # The first probe (/) returns 405 with HSTS — that's the
        # openresty behavior we expect.
        assert status in (200, 401, 405), f"unexpected status: {status}"
        print(f"PASS: openresty fallback (status={status}, path={path})")
    finally:
        httpd.shutdown()
        restore()


def test_openresty_fallback_to_apikey():
    """Variant: /login and / both 404 (some configs). The /api/v1/apikey
    fallback is the last resort — it always returns 401 with HSTS when
    headscale is reachable behind the proxy."""
    class OnlyApikeyHandler(_OpenrestyLikeHandler):
        def do_GET(self):
            if self.path == "/api/v1/apikey":
                self.send_response(401)
                self.send_header("Strict-Transport-Security", HSTS)
                self.end_headers()
                return
            self.send_response(404)
            self.end_headers()

    restore = _patched_urlopen()
    host, port, httpd = _make_https(OnlyApikeyHandler)
    try:
        hsts, status, path = check_https.check_hsts(
            f"https://{host}:{port}", host, port, timeout=3,
        )
        assert "max-age=" in hsts, f"expected HSTS, got {hsts!r}"
        assert path == "/api/v1/apikey", f"expected /api/v1/apikey fallback, got {path}"
        print(f"PASS: /api/v1/apikey fallback (status={status})")
    finally:
        httpd.shutdown()
        restore()


def test_caddy_login_works():
    """The docs' Caddy config: /login returns 200 with HSTS.
    check_hsts should hit /login on the first try and return it."""
    restore = _patched_urlopen()
    host, port, httpd = _make_https(_CaddyLikeHandler)
    try:
        hsts, status, path = check_https.check_hsts(
            f"https://{host}:{port}", host, port, timeout=3,
        )
        assert "max-age=" in hsts, f"expected HSTS, got {hsts!r}"
        assert path == "/login", f"expected /login primary, got {path}"
        assert status == 200
        print(f"PASS: caddy /login primary (status={status}, path={path})")
    finally:
        httpd.shutdown()
        restore()


def test_no_hsts_anywhere():
    """A TLS terminator that doesn't set HSTS at all. check_hsts
    should return empty HSTS so the caller can WARN."""
    class NoHSTSHandler(_CaddyLikeHandler):
        def do_GET(self):
            if self.path == "/login":
                self.send_response(200)
                self.end_headers()
                return
            self.send_response(404)
            self.end_headers()

    restore = _patched_urlopen()
    host, port, httpd = _make_https(NoHSTSHandler)
    try:
        hsts, status, path = check_https.check_hsts(
            f"https://{host}:{port}", host, port, timeout=3,
        )
        assert hsts == "", f"expected empty HSTS, got {hsts!r}"
        print(f"PASS: no HSTS detected (correctly returns empty)")
    finally:
        httpd.shutdown()
        restore()


def main():
    tests = [
        test_openresty_fallback_to_root,
        test_openresty_fallback_to_apikey,
        test_caddy_login_works,
        test_no_hsts_anywhere,
    ]
    fail = 0
    for t in tests:
        try:
            t()
        except AssertionError as e:
            print(f"FAIL: {t.__name__}: {e}")
            fail += 1
        except Exception as e:
            print(f"ERROR: {t.__name__}: {e!r}")
            fail += 1
    if fail:
        print(f"--- {fail} of {len(tests)} tests failed")
        return 1
    print(f"--- {len(tests)} of {len(tests)} tests passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
