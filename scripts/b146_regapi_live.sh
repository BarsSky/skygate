#!/usr/bin/env bash
# scripts/b146_regapi_live.sh — B146 (v1.5.0) Phase 2 e2e live test
# against the reg.ru API.
#
# Calls the v2 /zone/get_resource_records endpoint with the
# auth pattern that B145 + B161.4 confirmed works against
# the live API on 2026-08-18 (top-level form fields +
# mTLS cert, password NOT inside input_data JSON — the
# pre-fix code that put creds inside input_data returned
# NO_AUTH, this is the discovered-working pattern).
#
# The script is OPTIONAL — it only runs when the
# operator's credentials are in env vars. CI / verify-pre
# runs the check_b146.sh wrapper which SKIPs the live call
# when the creds are missing. The operator runs the script
# directly to verify the integration end-to-end on the
# live VM.
#
# Usage:
#   # 1. The cert + key must be on disk (the operator's
#   #    standard location):
#   ls /home/skyadmin/skygate-secrets/regapi/{cert,key}.pem
#
#   # 2. Set the 3 required env vars (the operator's login +
#   #    alternative password, NOT the cert password):
#   export SKYGATE_DNS_REGAPI_USER=kanagaenko@mail.ru
#   export SKYGATE_DNS_REGAPI_PASSWORD='<the alternative password>'
#   export SKYGATE_DNS_REGAPI_ZONE=skynas.ru
#
#   # 3. Run the script:
#   bash scripts/b146_regapi_live.sh
#
# Output (3-line summary, machine-greppable):
#   PASS: <zone>/<name> -> <ip>   (record found, IP matches)
#   PASS: <zone>/<name> -> <ip>   (record found, IP differs — expected for a failover drill)
#   SKIP: <reason>                (one of: missing creds, missing cert, etc.)
#   FAIL: <reason>                (one of: NO_AUTH, ACCESS_DENIED_FROM_IP, network error, etc.)
#
# Exit codes:
#   0 = PASS (record found, IP either matches or differs as expected)
#   1 = FAIL (any other case)
#   2 = SKIP (one of the prerequisites is missing — the caller
#       probably forgot to set env vars; the script printed
#       which one)

set -u

# --- 0. Preflight: 4 prerequisites ---

# P1: cert + key files on disk
CERT_PATH="${SKYGATE_REGAPI_CERT_PATH:-/home/skyadmin/skygate-secrets/regapi/cert.pem}"
KEY_PATH="${SKYGATE_REGAPI_KEY_PATH:-/home/skyadmin/skygate-secrets/regapi/key.pem}"
if [ ! -f "$CERT_PATH" ]; then
    echo "SKIP: cert file not found at $CERT_PATH (set SKYGATE_REGAPI_CERT_PATH or run from the operator VM)" >&2
    exit 2
fi
if [ ! -f "$KEY_PATH" ]; then
    echo "SKIP: key file not found at $KEY_PATH (set SKYGATE_REGAPI_KEY_PATH)" >&2
    exit 2
fi

# P2: env vars
if [ -z "${SKYGATE_DNS_REGAPI_USER:-}" ]; then
    echo "SKIP: SKYGATE_DNS_REGAPI_USER not set" >&2
    exit 2
fi
if [ -z "${SKYGATE_DNS_REGAPI_PASSWORD:-}" ]; then
    echo "SKIP: SKYGATE_DNS_REGAPI_PASSWORD not set" >&2
    exit 2
fi
if [ -z "${SKYGATE_DNS_REGAPI_ZONE:-}" ]; then
    echo "SKIP: SKYGATE_DNS_REGAPI_ZONE not set (the zone name, e.g. 'skynas.ru')" >&2
    exit 2
fi

# P3: the name of the record to fetch. Defaults to
# "skygate" (the operator's documented hostname — see
# Decision #2 in ha-v1.5.0-execution.md §1: "skygate
# is the active node"). Operators can override to test
# other A-records.
RECORD_NAME="${SKYGATE_DNS_REGAPI_RECORD:-skygate}"

# P4: a record MUST exist for the test to be meaningful.
# We don't auto-create one (that's a separate operator
# action); if the test says "no such record" the
# operator needs to add it first.

# --- 1. Build the form ---
# Top-level form fields + input_data (NOT inside
# input_data — see B161.4 NO_AUTH discovery).
# The provider-v2-path is part of the reg.ru URL:
#   https://api.<your-domain>/api/<provider-v2-path>/zone/get_resource_records
# We use the generic "regru2" path (the operator's
# domain) — see B161.4 status log for the URL shape.
API_BASE="https://api.${SKYGATE_DNS_REGAPI_ZONE}/api/regru2"
API_PATH="/zone/get_resource_records"

# JSON body (input_data) is the domain list with
# optional subdomain filter
INPUT_DATA=$(printf '{"domains":[{"dname":"%s"}]}' "$SKYGATE_DNS_REGAPI_ZONE")

# --- 2. Send the request with curl ---
# curl is preferred over a Go program for the live test
# because:
#   1. The operator can run this from any host with curl
#      + openssl (no need to build Go)
#   2. The exact request shape is visible in the script
#      (operator can audit + tweak)
#   3. The B145 + B161.4 status logs have the curl
#      command literally — this script is the
#      "productionized" version of those
#      one-off invocations
# We use --silent + --show-error so a non-zero HTTP
# code still surfaces the error in the log.
RESP=$(curl --silent --show-error \
    --cert "$CERT_PATH" --key "$KEY_PATH" \
    --max-time 15 \
    --data-urlencode "username=${SKYGATE_DNS_REGAPI_USER}" \
    --data-urlencode "password=${SKYGATE_DNS_REGAPI_PASSWORD}" \
    --data-urlencode "output_content_type=plain" \
    --data-urlencode "input_format=json" \
    --data-urlencode "input_data=${INPUT_DATA}" \
    --data-urlencode "subdomain=${RECORD_NAME}" \
    "${API_BASE}${API_PATH}" 2>&1) || {
    echo "FAIL: curl exit non-zero (network error, TLS handshake failure, or DNS resolution failure)" >&2
    echo "  response: ${RESP:0:200}" >&2
    exit 1
}

# --- 3. Parse the response ---
# reg.ru's response shape (from the B161.4 working
# pattern):
#   {
#     "status": "success",
#     "answer": {
#       "domains": [
#         { "dname": "skynas.ru",
#           "records": [
#             { "type": "A", "name": "skygate", "content": "95.165.170.190" }
#           ]
#         }
#       ]
#     }
#   }
#
# Failure shapes (any of):
#   { "status": "error", "error_code": "NO_AUTH", ... }
#   { "status": "error", "error_code": "ACCESS_DENIED_FROM_IP", ... }
#   { "status": "error", "error_code": "DOMAIN_NOT_FOUND", ... }
#   { "status": "error", "error_code": "RECORD_NOT_FOUND", ... }
#   HTTP 502 / 504 (the API gateway is down)
#
# We use a tiny Python parser (more reliable than jq +
# grep) because the response is JSON with a consistent
# shape, and Python is available on every skygate VM.

# We invoke Python via "python3" (the canonical name on
# the operator's VM) and fall back to "python" if
# python3 isn't there. The -c script is one line.
PYTHON_BIN="$(command -v python3 2>/dev/null || command -v python 2>/dev/null || true)"
if [ -z "$PYTHON_BIN" ]; then
    echo "FAIL: neither python3 nor python is on PATH (install python3 to run this live test)" >&2
    exit 1
fi

PARSED=$(printf '%s' "$RESP" | "$PYTHON_BIN" -c "
import json, sys
try:
    d = json.loads(sys.stdin.read())
except Exception as e:
    print('PARSE_ERROR:' + str(e))
    sys.exit(0)

# Top-level error?
if d.get('status') == 'error':
    code = d.get('error_code', 'UNKNOWN')
    text = d.get('error_text', '')
    print('ERROR:' + code + ':' + text)
    sys.exit(0)

# Walk the answer for the A-record.
ans = d.get('answer', {})
for dom in ans.get('domains', []):
    for rec in dom.get('records', []):
        if rec.get('type') == 'A' and rec.get('name') == '${RECORD_NAME}':
            print('FOUND:' + rec.get('content', ''))
            sys.exit(0)

# No error, no record — empty result.
print('NO_RECORD')
" 2>&1)

# --- 4. Interpret the parse result ---

case "$PARSED" in
    FOUND:*)
        IP="${PARSED#FOUND:}"
        echo "PASS: ${SKYGATE_DNS_REGAPI_ZONE}/${RECORD_NAME} -> ${IP}"
        # Optional: compare against the operator's
        # current public IP (if SKYGATE_PUBLIC_IP is
        # set). A mismatch is informational, not a
        # failure — the operator may have just
        # changed the record and the skygate
        # process hasn't re-applied yet.
        if [ -n "${SKYGATE_PUBLIC_IP:-}" ] && [ "$IP" != "$SKYGATE_PUBLIC_IP" ]; then
            echo "  (note: ${IP} != SKYGATE_PUBLIC_IP=${SKYGATE_PUBLIC_IP}; this is normal during a failover drill)"
        fi
        exit 0
        ;;
    NO_RECORD)
        echo "FAIL: no A-record found for ${SKYGATE_DNS_REGAPI_ZONE}/${RECORD_NAME} (record doesn't exist in the live DNS — add it via the registrar's web UI before retrying)" >&2
        exit 1
        ;;
    ERROR:NO_AUTH:*)
        echo "FAIL: reg.ru returned NO_AUTH — the auth pattern is wrong, or the cert is not registered in reg.ru, or the alternative password is wrong" >&2
        echo "  detail: ${PARSED#ERROR:NO_AUTH:}" >&2
        exit 1
        ;;
    ERROR:ACCESS_DENIED_FROM_IP:*)
        echo "FAIL: reg.ru returned ACCESS_DENIED_FROM_IP — the source IP isn't in the API IP whitelist" >&2
        echo "  detail: ${PARSED#ERROR:ACCESS_DENIED_FROM_IP:}" >&2
        echo "  fix: add this VM's public IP to the reg.ru 'API IP whitelist' (Настройки → Безопасность)" >&2
        exit 1
        ;;
    ERROR:DOMAIN_NOT_FOUND:*)
        echo "FAIL: reg.ru doesn't manage ${SKYGATE_DNS_REGAPI_ZONE} under this account — the zone field is wrong, or the account doesn't own the domain" >&2
        echo "  detail: ${PARSED#ERROR:DOMAIN_NOT_FOUND:}" >&2
        exit 1
        ;;
    ERROR:*)
        echo "FAIL: reg.ru returned error ${PARSED#ERROR:}" >&2
        exit 1
        ;;
    PARSE_ERROR:*)
        echo "FAIL: response is not valid JSON: ${PARSED#PARSE_ERROR:}" >&2
        echo "  raw response: ${RESP:0:500}" >&2
        exit 1
        ;;
    *)
        echo "FAIL: unknown parse result: ${PARSED}" >&2
        echo "  raw response: ${RESP:0:500}" >&2
        exit 1
        ;;
esac
