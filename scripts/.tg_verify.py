#!/usr/bin/env python3
"""
Skygate Telegram delivery harness — used by scripts/notify.sh and a smoke
check script that does NOT need real credentials.

Modes:
  --mode=client     act as HTTP client to api.telegram.org (real or mock)
  --mode=mock-server  start a tiny localhost HTTP server that mimics the
                     bot API's POST /bot<token>/sendMessage. The dry-run
                     smoke test in scripts/notify.sh --dry-run-mode=mock
                     points at this server via TELEGRAM_API=http://127.0.0.1:8123
                     and prints the received payload. This is the
                     way to verify end-to-end wiring without a token.

The notify.sh script is intentionally simple bash. The harness exists to
give operators a way to *prove* that what would have been sent is what
the channel expects (JSON shape, severity icons, truncation behavior).

For real delivery, use scripts/notify.sh directly with TELEGRAM_BOT_TOKEN
and TELEGRAM_CHAT_ID set in .env.
"""
import argparse
import http.server
import json
import sys
import threading
import time


def run_mock_server(host: str, port: int) -> None:
    received = []

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_POST(self):  # noqa: N802 (BaseHTTPRequestHandler API)
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length).decode("utf-8", errors="replace")
            try:
                payload = json.loads(body)
            except json.JSONDecodeError:
                payload = {"_raw": body}
            received.append({"path": self.path, "payload": payload})
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"ok":true,"result":{"message_id":1}}')
            print(f"MOCK recv: path={self.path} payload={payload}", flush=True)

        def log_message(self, *_a, **_k):
            return

    server = http.server.HTTPServer((host, port), Handler)
    print(f"MOCK listening on http://{host}:{port}", flush=True)
    server.serve_forever()


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--mode", choices=("client", "mock-server"), required=True)
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", type=int, default=8123)
    args = p.parse_args()

    if args.mode == "mock-server":
        run_mock_server(args.host, args.port)
        return 0

    # client mode — dry-run a request shape without actually talking to TG
    sample = {
        "chat_id": "REDACTED",
        "text": "[skyagent] (refactor v0.6.0) harness preview\n"
                "2026-07-09T07:48:00Z  severity=ok\n"
                "this is what notify.sh would POST when token is configured",
        "disable_web_page_preview": True,
    }
    print(json.dumps(sample, indent=2, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
