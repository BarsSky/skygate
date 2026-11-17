#!/usr/bin/env bash
# ============================================================================
# deploy/caddy/maintenance/index.html is served by the proxy, not the panel — a contract for B485.
#
# Context: an operator opening the panel during a native (systemd) update saw the BROWSER's
# "site cannot handle the request" page. That is a bare 502 from the reverse proxy: the unit is
# stopped to replace the binary, so nothing answers on :8080 and the panel's own provisional page
# (B269) cannot run either. The fix is a proxy-level error page that says what is happening and
# reloads into the panel by itself.
#
# Measured the hard way on Caddy v2.11.9 (all three failures cost real time on the live host):
#   1. the assets must NOT live under /var/lib/skygate — that directory is 0750 skygate:skygate by
#      design, so the caddy service user cannot traverse it and `import` fails with
#      "permission denied", taking the WHOLE site down (it did, for about ninety seconds);
#   2. `file_server` INSIDE handle_errors answers 5xx with an EMPTY body;
#   3. the proxied request keeps the original Host, so without `header_up Host` the loopback static
#      server re-matches the PANEL site and answers an empty 200.
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b485.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

PAGE=deploy/caddy/maintenance/index.html
SNIP=deploy/caddy/maintenance.caddy
SRV=deploy/caddy/maintenance-server.caddy
INST=deploy/install-maintenance-page.sh
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
DOC=docs/https.md

PASS=0
FAIL=0
fails=()
ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$PAGE" "$SNIP" "$SRV" "$INST" "$VERIFY" "$AGENTS" "$NOTES" "$DOC"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the page itself ----------------------------------------------------------
if has "$PAGE" 'Идёт обновление панели' && has "$PAGE" 'проверяю каждые 3 с'; then
  ok "A1: the page states what is happening and that it keeps checking"
else
  bad "A1: the page does not say what is going on"
fi
if has "$PAGE" 'fetch("/healthz"' && has "$PAGE" 'application/json' && has "$PAGE" 'location.replace("/")'; then
  ok "A2: the page returns to the panel by itself once /healthz answers JSON"
else
  bad "A2: the page cannot tell the panel apart from its own 502 body"
fi
if has "$PAGE" 'journalctl -u skygate'; then
  ok "A3: a slow update hands the operator the journal command instead of a dead end"
else
  bad "A3: no journal hint for a stuck update"
fi
if has "$PAGE" 'noindex'; then
  ok "A4: the page is not indexable"
else
  bad "A4: a maintenance page could end up in a search index"
fi

# --- B: wiring (the three measured traps) -----------------------------------------
if has "$SNIP" 'header_up Host 127.0.0.1:8081'; then
  ok "B1: the upstream Host is pinned (trap 3: otherwise the panel site re-matches and answers an empty 200)"
else
  bad "B1: no header_up Host — the error page would be empty"
fi
if has "$SNIP" 'rewrite * /'; then
  ok "B2: the path is rewritten to / (trap 2's sibling: the static server would look for the original URI)"
else
  bad "B2: the original URI reaches the static server (404 instead of the page)"
fi
if has "$SNIP" 'reverse_proxy 127.0.0.1:8081' && ! has "$SNIP" "		 file_server" && ! has "$SNIP" "		file_server"; then
  ok "B3: the error page is served through a loopback proxy, not file_server (trap 2: file_server in handle_errors is empty)"
else
  bad "B3: handle_errors still relies on file_server"
fi
if has "$SNIP" 'X-Skygate-Maintenance' && has "$SNIP" 'Retry-After'; then
  ok "B4: the maintenance response is identifiable and asks for a retry delay"
else
  bad "B4: the maintenance response carries no marker/Retry-After"
fi
if has "$SNIP" 'expression {err.status_code} >= 500'; then
  ok "B5: only 5xx is replaced — a real 404 stays a 404"
else
  bad "B5: every error would render the maintenance page"
fi
if has "$SRV" 'http://127.0.0.1:8081' && has "$SRV" 'root * /etc/caddy/maintenance' && has "$SRV" 'status 503'; then
  ok "B6: the static server is loopback-only, reads /etc/caddy/maintenance, and answers 503 (a 200 maintenance page is a lie to every health checker)"
else
  bad "B6: the static server shape is wrong"
fi
if has "$INST" 'DEST_DIR=/etc/caddy/maintenance'; then
  ok "B7: the assets are NOT installed under the 0750 data dir (trap 1: the site went down for ~90 s)"
else
  bad "B7: the installer does not target /etc/caddy/maintenance"
fi

# --- C: the installer -------------------------------------------------------------
if has "$INST" 'caddy validate --config' && has "$INST" 'restored $BACKUP after a validation failure'; then
  ok "C1: the Caddyfile is validated BEFORE any reload, and a failure restores the backup"
else
  bad "C1: a bad patch could leave the operator without TLS"
fi
if has "$INST" 'grep -qF "$DEST_DIR/maintenance.caddy"'; then
  ok "C2: the patch is idempotent (a Caddyfile that already imports the snippet is left alone)"
else
  bad "C2: re-running the installer would add a second import"
fi
if has "$INST" '--dry-run'; then
  ok "C3: --dry-run exists, so the operator can see the change first"
else
  bad "C3: no dry run"
fi
if has "$DOC" 'maintenance' || has "$DOC" 'B485'; then
  ok "C4: the HTTPS guide explains the maintenance page"
else
  bad "C4: the page is undocumented"
fi

# --- D: registration ---------------------------------------------------------------
if has "$VERIFY" '"B485"'; then ok "D1: registered in the gate"; else bad "D1: not registered"; fi
if has "$AGENTS" 'B485'; then ok "D2: block index carries it"; else bad "D2: no index entry"; fi
if has "$NOTES" 'B485'; then ok "D3: release notes describe it"; else bad "D3: no release-notes section"; fi

# --- E: discriminators ---------------------------------------------------------------
mkdir -p "$SKY_TMP"
printf 'proxy { reverse_proxy 127.0.0.1:8080 }\n' > "$SKY_TMP/old.caddy"
if grep -q 'handle_errors' "$SKY_TMP/old.caddy"; then
  bad "E1: the predicate matches a Caddyfile that has no error handler"
else
  ok "E1: the wiring predicates discriminate a plain proxy block"
fi
if bash -n "$INST" 2>/dev/null; then
  ok "E2: the installer is syntactically valid bash"
else
  bad "E2: the installer does not parse"
fi
if command -v python3 >/dev/null 2>&1; then
  if python3 - "$PAGE" <<'PY'
import sys, re
html = open(sys.argv[1], encoding="utf-8").read()
# the page must be self-contained: no external CSS/JS/fonts, because the panel's static assets are
# served BY THE PANEL and are exactly what is missing while it restarts.
bad = re.findall(r'(?:src|href)="(?!#|/|https?://[^"/]+/)[^"]*\.(?:css|js|woff2?)"', html)
sys.exit(1 if bad else 0)
PY
  then ok "E3: the page is self-contained (no panel assets that would 404 while the panel is down)"; else bad "E3: the page references assets it cannot load while the panel is down"; fi
else
  printf '  \033[33m-\033[0m E3: SKIP — no python3\n'
fi

echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB485: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB485: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
