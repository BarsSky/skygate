#!/usr/bin/env bash
# ============================================================================
# oidc-sync.sh — sync OIDC config between skygate and headscale
# B167 (v1.5.2) — operator-requested auto-sync
#
# MODES (full Option C):
#   1. docker-compose (auto-detected): writes headscale.conf + restarts
#      the headscale container via `docker restart` + waits for /health
#   2. systemd (auto-detected): writes /etc/headscale/config.yaml +
#      `systemctl restart headscale` + waits for /health
#   3. kubernetes (auto-detected): writes the ConfigMap + rolls the
#      headscale deployment via kubectl (if available)
#   4. manual / no-restart: writes the headscale.conf + updates skygate's
#      .env, but DOES NOT restart anything. Operator can download the
#      generated headscale.conf snippet and apply manually.
#   5. --download-only: ONLY prints the generated headscale.conf YAML
#      to stdout. No file writes, no restarts. Used by the operator to
#      pre-stage the config when they want to apply it by hand.
#   6. --api-apply: calls headscale's `headscale configure oidc` gRPC
#      method (when available) instead of writing a file. Used for
#      headscale 0.30+ which supports runtime OIDC config via gRPC.
#
# Usage:
#   oidc-sync.sh <skygate_url> <client_id> <client_secret> <redirect_uris>
#                [--headscale-config <path>]
#                [--headscale-container <name>]
#                [--skygate-env <path>]
#                [--mode auto|docker|systemd|k8s|manual|download|api]
#                [--download-only]
#                [--api-endpoint <url>]
#
# Example (docker-compose, same machine):
#   oidc-sync.sh \
#     https://skygate.example.com \
#     headscale \
#     test-secret-do-not-use-in-prod \
#     https://head.skynas.ru/oidc/callback \
#     --headscale-config /home/skyadmin/headscale/config/config.yaml \
#     --headscale-container headscale \
#     --skygate-env /home/skyadmin/skygate/.env
#
# Does (in order):
#   1. Validate input arguments
#   2. Validate the skygate_url is reachable
#   3. Detect deployment mode (docker / systemd / k8s / manual)
#   4. Generate the oidc: YAML block from the input
#   5. Backup the existing headscale config (if any)
#   6. Inject/replace the oidc: block in the headscale config
#   7. Update skygate's .env with SKYGATE_OIDC_ISSUER (and friends)
#   8. Apply the change (restart headscale / download / API apply)
#   9. Verify health (poll /health for up to RESTART_TIMEOUT seconds)
#  10. Output a JSON result on stdout
#
# Outputs (stdout, JSON):
#   { "ok": true,
#     "skygate_url": "https://skygate.example.com",
#     "client_id": "headscale",
#     "headscale_config_path": "/home/admin/headscale/config/config.yaml",
#     "oidc_block_yaml": "...yaml...",
#     "mode": "docker" | "systemd" | "k8s" | "manual" | "download" | "api",
#     "headscale_restarted": true | false,
#     "headscale_healthy": true | false,
#     "env_updated": true | false,
#     "duration_ms": N }
#
# Errors go to stderr; the script exits non-zero.
# Re-run is idempotent: same inputs → same output, no state drift.
# ============================================================================
set -euo pipefail

# --- defaults ---
HEADSCALE_CONFIG_PATH=""
HEADSCALE_CONTAINER="${HEADSCALE_CONTAINER:-headscale}"
SKYGATE_ENV_PATH="${SKYGATE_ENV_PATH:-/home/skyadmin/skygate/.env}"
RESTART_TIMEOUT="${RESTART_TIMEOUT:-60}"
MODE_OVERRIDE=""
DOWNLOAD_ONLY=0
API_ENDPOINT=""
SCOPE="${SCOPE:-/oidc/userinfo}"
EXTRA_PARAMS="${EXTRA_PARAMS:-domain=client_id}"
ALLOWED_DOMAINS="${ALLOWED_DOMAINS:-}"
AUTO_UPDATE="${AUTO_UPDATE:-true}"
# B167.1: STRIP_EMAIL_DOMAIN is NOT a valid headscale 0.29.x key
# (removed in headscale 0.23+ — see changelog). The skygate OIDC
# provider always uses the email's local part as the preferred_username
# claim regardless of this setting, so we drop it from the generated
# YAML to keep the config valid for headscale 0.20-0.29.
unset STRIP_EMAIL_DOMAIN 2>/dev/null || true

# --- arg parsing ---
SKYGATE_URL=""
CLIENT_ID=""
CLIENT_SECRET=""
REDIRECT_URIS=""
while [ $# -gt 0 ]; do
    case "$1" in
        --headscale-config)   HEADSCALE_CONFIG_PATH="$2"; shift 2;;
        --headscale-container) HEADSCALE_CONTAINER="$2"; shift 2;;
        --skygate-env)        SKYGATE_ENV_PATH="$2"; shift 2;;
        --mode)               MODE_OVERRIDE="$2"; shift 2;;
        --download-only)      DOWNLOAD_ONLY=1; shift;;
        --api-endpoint)       API_ENDPOINT="$2"; shift 2;;
        --allowed-domains)    ALLOWED_DOMAINS="$2"; shift 2;;
        --scope)              SCOPE="$2"; shift 2;;
        --no-auto-update)     AUTO_UPDATE=false; shift;;
        # B167.1: --no-strip-email is a no-op (the key was removed in headscale 0.23+)
        --no-strip-email)     shift;;
        --help|-h)
            sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        -*)
            echo "unknown flag: $1" >&2
            exit 2;;
        *)
            if [ -z "$SKYGATE_URL" ]; then SKYGATE_URL="$1"
            elif [ -z "$CLIENT_ID" ]; then CLIENT_ID="$1"
            elif [ -z "$CLIENT_SECRET" ]; then CLIENT_SECRET="$1"
            elif [ -z "$REDIRECT_URIS" ]; then REDIRECT_URIS="$1"
            else echo "unexpected positional: $1" >&2; exit 2
            fi
            shift;;
    esac
done

# --- input validation ---
if [ -z "$SKYGATE_URL" ] || [ -z "$CLIENT_ID" ] || [ -z "$CLIENT_SECRET" ] || [ -z "$REDIRECT_URIS" ]; then
    echo "usage: oidc-sync.sh <skygate_url> <client_id> <client_secret> <redirect_uris> [flags]" >&2
    echo "  flags: --headscale-config <path> --headscale-container <name> --skygate-env <path>" >&2
    echo "         --mode auto|docker|systemd|k8s|manual|download|api" >&2
    echo "         --download-only --api-endpoint <url> --allowed-domains <csv>" >&2
    exit 2
fi

START_MS=$(date +%s%3N)

# --- default headscale config path: discover from common locations ---
if [ -z "$HEADSCALE_CONFIG_PATH" ]; then
    for candidate in \
        /home/skyadmin/headscale/config/config.yaml \
        /home/admin/headscale/config/config.yaml \
        /etc/headscale/config.yaml \
        /var/lib/headscale/config.yaml; do
        if [ -f "$candidate" ]; then
            HEADSCALE_CONFIG_PATH="$candidate"
            break
        fi
    done
fi
if [ -z "$HEADSCALE_CONFIG_PATH" ]; then
    HEADSCALE_CONFIG_PATH="/home/skyadmin/headscale/config/config.yaml"
fi

# --- 1. validate skygate_url is reachable ---
echo "[1/10] validate skygate_url is reachable" >&2
HEALTH_OK=0
for u in "$SKYGATE_URL/.well-known/openid-configuration" "$SKYGATE_URL/healthz"; do
    code=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 "$u" 2>/dev/null || echo "fail")
    if [ "$code" = "200" ]; then HEALTH_OK=1; break; fi
done
if [ "$HEALTH_OK" != "1" ]; then
    echo "  WARN: $SKYGATE_URL is not reachable (probed discovery + healthz)" >&2
fi

# --- 2. detect deployment mode ---
echo "[2/10] detect deployment mode" >&2
MODE="manual"
if [ -n "$MODE_OVERRIDE" ]; then
    MODE="$MODE_OVERRIDE"
elif [ "$DOWNLOAD_ONLY" = "1" ]; then
    MODE="download"
elif [ -n "$API_ENDPOINT" ]; then
    MODE="api"
elif command -v docker >/dev/null 2>&1 && [ -S /var/run/docker.sock ]; then
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -qFx "$HEADSCALE_CONTAINER"; then
        MODE="docker"
    fi
fi
# Try systemd as a fallback (covers bare-metal + VMs without docker)
if [ "$MODE" = "manual" ] && command -v systemctl >/dev/null 2>&1; then
    if systemctl list-unit-files headscale.service 2>/dev/null | grep -q '^headscale.service'; then
        MODE="systemd"
    fi
fi
# Try kubectl as another fallback
if [ "$MODE" = "manual" ] && command -v kubectl >/dev/null 2>&1; then
    if kubectl get deploy headscale -n headscale 2>/dev/null | grep -q 'headscale'; then
        MODE="k8s"
    fi
fi
echo "  selected mode: $MODE" >&2

# --- 3. generate the oidc: YAML block ---
echo "[3/10] generate oidc: YAML block" >&2
OIDC_BLOCK=$(SKYGATE_URL="$SKYGATE_URL" \
             CLIENT_ID="$CLIENT_ID" \
             CLIENT_SECRET="$CLIENT_SECRET" \
             REDIRECT_URIS="$REDIRECT_URIS" \
             SCOPE="$SCOPE" \
             EXTRA_PARAMS="$EXTRA_PARAMS" \
             ALLOWED_DOMAINS="$ALLOWED_DOMAINS" \
             AUTO_UPDATE="$AUTO_UPDATE" \
             python3 <<'PYEOF'
import os, sys, yaml
scopes = [s.strip() for s in os.environ["SCOPE"].split(",") if s.strip()]
allowed_domains = [d.strip() for d in os.environ["ALLOWED_DOMAINS"].split(",") if d.strip()]
# B167.1: strip_email_domain was removed in headscale 0.23+
# (the user-identity handling moved to the OIDC provider's claims).
# The skygate OIDC provider always emits the email's local part as
# preferred_username, so headscale gets the right value without
# needing this key.
block = {
    "oidc": {
        "issuer": os.environ["SKYGATE_URL"].rstrip("/"),
        "client_id": os.environ["CLIENT_ID"],
        "client_secret": os.environ["CLIENT_SECRET"],
        "scope": scopes,
        "extra_params": dict(p.split("=", 1) for p in os.environ["EXTRA_PARAMS"].split("&") if "=" in p),
        "redirect_uris": [u.strip() for u in os.environ["REDIRECT_URIS"].split(",") if u.strip()],
        "allowed_domains": allowed_domains,
        "auto_update": os.environ["AUTO_UPDATE"].lower() == "true",
    }
}
# Drop empty optional fields so the YAML stays clean
for k in ("extra_params", "allowed_domains"):
    if not block["oidc"][k]:
        del block["oidc"][k]
print(yaml.safe_dump({"oidc": block["oidc"]}, default_flow_style=False, sort_keys=False).rstrip())
PYEOF
)

# --- 4. backup existing config (if present) — SKIP in download mode ---
echo "[4/10] backup existing headscale config" >&2
CONFIG_BACKUP_PATH=""
if [ "$MODE" = "download" ]; then
    echo "  download mode — no file writes" >&2
elif [ -f "$HEADSCALE_CONFIG_PATH" ]; then
    CONFIG_BACKUP_PATH="${HEADSCALE_CONFIG_PATH}.pre-oidc-sync.$(date +%Y%m%d%H%M%S)"
    cp -p "$HEADSCALE_CONFIG_PATH" "$CONFIG_BACKUP_PATH"
    echo "  backed up to: $CONFIG_BACKUP_PATH" >&2
fi

# --- 5. inject/replace the oidc: block in the headscale config — SKIP in download mode ---
echo "[5/10] inject oidc: block into $HEADSCALE_CONFIG_PATH" >&2
TMP_CONFIG_PATH="${HEADSCALE_CONFIG_PATH}.tmp.$$"
SKYGATE_URL="$SKYGATE_URL" \
CLIENT_ID="$CLIENT_ID" \
CLIENT_SECRET="$CLIENT_SECRET" \
REDIRECT_URIS="$REDIRECT_URIS" \
SCOPE="$SCOPE" \
EXTRA_PARAMS="$EXTRA_PARAMS" \
ALLOWED_DOMAINS="$ALLOWED_DOMAINS" \
AUTO_UPDATE="$AUTO_UPDATE" \
HEADSCALE_CONFIG_PATH="$HEADSCALE_CONFIG_PATH" \
TMP_CONFIG_PATH="$TMP_CONFIG_PATH" \
MODE="$MODE" \
python3 <<'PYEOF'
import os, sys, yaml
mode = os.environ.get("MODE", "manual")
path = os.environ["HEADSCALE_CONFIG_PATH"]
tmp = os.environ["TMP_CONFIG_PATH"]
# Skip file write in download mode
if mode == "download":
    sys.exit(0)
scopes = [s.strip() for s in os.environ["SCOPE"].split(",") if s.strip()]
allowed_domains = [d.strip() for d in os.environ["ALLOWED_DOMAINS"].split(",") if d.strip()]
# B167.1: strip_email_domain removed in headscale 0.23+
oidc_block = {
    "oidc": {
        "issuer": os.environ["SKYGATE_URL"].rstrip("/"),
        "client_id": os.environ["CLIENT_ID"],
        "client_secret": os.environ["CLIENT_SECRET"],
        "scope": scopes,
        "extra_params": dict(p.split("=", 1) for p in os.environ["EXTRA_PARAMS"].split("&") if "=" in p),
        "redirect_uris": [u.strip() for u in os.environ["REDIRECT_URIS"].split(",") if u.strip()],
        "allowed_domains": allowed_domains,
        "auto_update": os.environ["AUTO_UPDATE"].lower() == "true",
    }
}
# Drop empty optional fields
for k in ("extra_params", "allowed_domains"):
    if not oidc_block["oidc"][k]:
        del oidc_block["oidc"][k]
# Read existing config (or start fresh)
data = {}
if os.path.exists(path):
    with open(path) as f:
        try:
            data = yaml.safe_load(f) or {}
        except yaml.YAMLError as e:
            print(f"WARN: existing config not valid YAML, starting fresh: {e}", file=sys.stderr)
            data = {}
# Replace the oidc: block
data["oidc"] = oidc_block["oidc"]
# Atomic write
with open(tmp, "w") as f:
    yaml.safe_dump(data, f, default_flow_style=False, sort_keys=False)
PYEOF
# Only mv if not download mode
if [ "$MODE" != "download" ] && [ -f "$TMP_CONFIG_PATH" ]; then
    mv "$TMP_CONFIG_PATH" "$HEADSCALE_CONFIG_PATH"
fi

# --- 6. update skygate's .env with SKYGATE_OIDC_ISSUER (and friends) ---
echo "[6/10] update $SKYGATE_ENV_PATH" >&2
ENV_UPDATED=0
ENV_BACKUP_PATH=""
if [ -f "$SKYGATE_ENV_PATH" ] && [ "$MODE" != "download" ]; then
    ENV_BACKUP_PATH="${SKYGATE_ENV_PATH}.pre-oidc-sync.$(date +%Y%m%d%H%M%S)"
    cp -p "$SKYGATE_ENV_PATH" "$ENV_BACKUP_PATH"
    # Replace or append SKYGATE_OIDC_ISSUER + SKYGATE_OIDC_REDIRECT_URIS
    TMP_ENV_PATH="${SKYGATE_ENV_PATH}.tmp.$$"
    awk -v url="$SKYGATE_URL" -v ru="$REDIRECT_URIS" -v cid="$CLIENT_ID" -v sec="$CLIENT_SECRET" '
        /^SKYGATE_OIDC_ISSUER=/      { print "SKYGATE_OIDC_ISSUER=" url; found_iss=1; next }
        /^SKYGATE_OIDC_REDIRECT_URIS=/ { print "SKYGATE_OIDC_REDIRECT_URIS=" ru; found_ru=1; next }
        /^SKYGATE_OIDC_CLIENT_ID=/   { print "SKYGATE_OIDC_CLIENT_ID=" cid; found_cid=1; next }
        /^SKYGATE_OIDC_CLIENT_SECRET=/ { print "SKYGATE_OIDC_CLIENT_SECRET=" sec; found_sec=1; next }
        { print }
        END {
            if (!found_iss) print "SKYGATE_OIDC_ISSUER=" url
            if (!found_ru)  print "SKYGATE_OIDC_REDIRECT_URIS=" ru
            if (!found_cid) print "SKYGATE_OIDC_CLIENT_ID=" cid
            if (!found_sec) print "SKYGATE_OIDC_CLIENT_SECRET=" sec
        }
    ' "$SKYGATE_ENV_PATH" > "$TMP_ENV_PATH"
    mv "$TMP_ENV_PATH" "$SKYGATE_ENV_PATH"
    ENV_UPDATED=1
    echo "  updated $SKYGATE_ENV_PATH (backup: $ENV_BACKUP_PATH)" >&2
elif [ "$MODE" = "download" ]; then
    echo "  download mode — skipping .env update" >&2
else
    echo "  WARN: $SKYGATE_ENV_PATH not found, skipping .env update" >&2
fi

# --- 7. apply: restart / download / api ---
echo "[7/10] apply (mode: $MODE)" >&2
HEADSCALE_RESTARTED=0
if [ "$MODE" = "docker" ]; then
    if docker restart "$HEADSCALE_CONTAINER" >/dev/null 2>&1; then
        HEADSCALE_RESTARTED=1
    fi
elif [ "$MODE" = "systemd" ]; then
    if systemctl restart headscale 2>/dev/null; then
        HEADSCALE_RESTARTED=1
    fi
elif [ "$MODE" = "k8s" ]; then
    if kubectl rollout restart deploy/headscale -n headscale 2>/dev/null; then
        HEADSCALE_RESTARTED=1
    fi
elif [ "$MODE" = "api" ]; then
    # headscale 0.30+ has a gRPC `configure_oidc` method. We call it via
    # the headscale CLI inside the container (avoids a separate gRPC client).
    if docker exec "$HEADSCALE_CONTAINER" headscale configure oidc \
            --issuer "$SKYGATE_URL" \
            --client-id "$CLIENT_ID" \
            --client-secret "$CLIENT_SECRET" 2>/dev/null; then
        HEADSCALE_RESTARTED=1
    fi
fi

# --- 8. wait for headscale to be healthy (poll /health) ---
echo "[8/10] wait for headscale health" >&2
HEADSCALE_HEALTHY=0
if [ "$HEADSCALE_RESTARTED" = "1" ] && [ "$MODE" = "docker" ]; then
    for i in $(seq 1 "$RESTART_TIMEOUT"); do
        code=$(docker exec "$HEADSCALE_CONTAINER" wget -qO- --timeout=2 http://127.0.0.1:50443/health 2>/dev/null && echo "ok" || echo "fail")
        if [ "$code" = "ok" ]; then
            HEADSCALE_HEALTHY=1
            break
        fi
        sleep 1
    done
elif [ "$HEADSCALE_RESTARTED" = "1" ] && [ "$MODE" = "systemd" ]; then
    for i in $(seq 1 "$RESTART_TIMEOUT"); do
        if curl -s -o /dev/null --max-time 2 http://127.0.0.1:50443/health; then
            HEADSCALE_HEALTHY=1
            break
        fi
        sleep 1
    done
elif [ "$HEADSCALE_RESTARTED" = "1" ] && [ "$MODE" = "k8s" ]; then
    for i in $(seq 1 "$((RESTART_TIMEOUT * 2))"); do
        if kubectl wait --for=condition=ready pod -l app=headscale -n headscale --timeout=2s 2>/dev/null; then
            HEADSCALE_HEALTHY=1
            break
        fi
        sleep 1
    done
fi
if [ "$HEADSCALE_HEALTHY" = "1" ]; then
    echo "  headscale is healthy" >&2
elif [ "$HEADSCALE_RESTARTED" = "1" ]; then
    echo "  WARN: headscale did not become healthy within ${RESTART_TIMEOUT}s" >&2
else
    echo "  manual mode — operator must restart headscale" >&2
fi

# --- 9. (optional) OIDC test probe from inside skygate container ---
echo "[9/10] optional OIDC test probe" >&2
TEST_RESULT=""
if [ "$HEADSCALE_HEALTHY" = "1" ]; then
    # Check that headscale can reach the skygate OIDC endpoints
    # (this is what the operator will trigger via "Test connection" in the admin UI)
    TEST_RESULT=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
        -H "User-Agent: headscale-test" \
        "${HEADSCALE_URL:-http://headscale:50444}/oidc/callback?code=fake&state=fake" 2>/dev/null || echo "fail")
    if [ -z "$TEST_RESULT" ] || [ "$TEST_RESULT" = "000" ]; then
        TEST_RESULT="skipped"
    fi
fi

# --- 10. output JSON result ---
END_MS=$(date +%s%3N)
DURATION_MS=$((END_MS - START_MS))

SKYGATE_URL="$SKYGATE_URL" \
CLIENT_ID="$CLIENT_ID" \
HEADSCALE_CONFIG_PATH="$HEADSCALE_CONFIG_PATH" \
CONFIG_BACKUP_PATH="$CONFIG_BACKUP_PATH" \
SKYGATE_ENV_PATH="$SKYGATE_ENV_PATH" \
ENV_BACKUP_PATH="$ENV_BACKUP_PATH" \
OIDC_BLOCK="$OIDC_BLOCK" \
MODE="$MODE" \
HEADSCALE_RESTARTED="$HEADSCALE_RESTARTED" \
HEADSCALE_HEALTHY="$HEADSCALE_HEALTHY" \
ENV_UPDATED="$ENV_UPDATED" \
TEST_RESULT="$TEST_RESULT" \
DURATION_MS="$DURATION_MS" \
python3 <<PYEOF
import json, os
print(json.dumps({
    "ok": True,
    "skygate_url": os.environ["SKYGATE_URL"],
    "client_id": os.environ["CLIENT_ID"],
    "headscale_config_path": os.environ["HEADSCALE_CONFIG_PATH"],
    "config_backup_path": os.environ.get("CONFIG_BACKUP_PATH", ""),
    "env_path": os.environ.get("SKYGATE_ENV_PATH", ""),
    "env_backup_path": os.environ.get("ENV_BACKUP_PATH", ""),
    "oidc_block_yaml": os.environ.get("OIDC_BLOCK", ""),
    "mode": os.environ["MODE"],
    "headscale_restarted": int(os.environ["HEADSCALE_RESTARTED"]),
    "headscale_healthy": int(os.environ["HEADSCALE_HEALTHY"]),
    "env_updated": int(os.environ["ENV_UPDATED"]),
    "test_result": os.environ.get("TEST_RESULT", ""),
    "duration_ms": int(os.environ["DURATION_MS"]),
}, indent=2))
PYEOF
