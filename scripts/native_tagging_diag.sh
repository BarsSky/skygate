#!/usr/bin/env bash
# diag_native_tagging.sh — why are devices not tagged on a NATIVE (systemd) host?
#
# Run ON the host that runs skygate (e.g. the `aro` box):
#     bash scripts/diag_native_tagging.sh
#
# It answers, in order, the four questions that decide whether a dev-tag can
# reach headscale on a native install:
#   1. is the tag reconciler even running?
#   2. can skygate write the policy (tagOwners) — is the root helper armed?
#   3. does the policy actually carry the tagOwners entry for this device?
#   4. what does headscale report, and what did the last attempts log?
#
# Read-only: nothing is written, no policy is applied.

set -uo pipefail
hdr() { printf '\n\033[1m=== %s\033[0m\n' "$*"; }

DB="${SKYGATE_DB_PATH:-/var/lib/skygate/skygate.db}"
POLICY="${SKYGATE_HEADSCALE_POLICY_PATH:-/etc/headscale/policy.hujson}"
UPDATE_DIR="${SKYGATE_UPDATE_DIR:-/var/lib/skygate}"

command -v sqlite3 >/dev/null 2>&1 || echo "WARN: sqlite3 not installed — DB sections will be empty"
command -v headscale >/dev/null 2>&1 || echo "WARN: headscale binary not in PATH"

hdr "1. units (is the reconciler running, is the policy helper armed?)"
systemctl is-active skygate 2>/dev/null | sed 's/^/  skygate: /'
systemctl status skygate --no-pager 2>/dev/null | grep -E 'Loaded:|Active:|ExecStart' | sed 's/^/  /'
for u in skygate-policy.path skygate-policy.service; do
  printf '  %s: %s\n' "$u" "$(systemctl is-active "$u" 2>/dev/null || echo missing)"
done
echo "  --- env of the running service"
systemctl show skygate -p Environment 2>/dev/null | tr ' ' '\n' | grep -E 'SKYGATE_(BASE_DOMAIN|DB|HEADSCALE|UPDATE_DIR|PORT)' | sed 's/^/  /'

hdr "2. policy write path (B272.3 root helper)"
ls -la "$UPDATE_DIR"/policy.request.props 2>/dev/null && echo "  ^ a QUEUED request that nobody consumed => the path unit is missing/not started" \
  || echo "  no queued policy request"
ls -la /etc/systemd/system/skygate-policy.path /etc/systemd/system/skygate-policy.service 2>/dev/null | sed 's/^/  /'
ls -la /usr/local/bin/skygate-apply-policy.sh 2>/dev/null | sed 's/^/  /'
echo "  policy file:"; ls -la "$POLICY" 2>/dev/null | sed 's/^/  /'

hdr "3. does the policy carry tagOwners for the devices?"
if [ -r "$POLICY" ]; then
  grep -c 'tag:dev-' "$POLICY" 2>/dev/null | sed 's/^/  tag:dev- entries: /'
  grep -o '"tag:dev-[^"]*"' "$POLICY" 2>/dev/null | head -10 | sed 's/^/  /'
else
  echo "  cannot read $POLICY"
fi

hdr "4. headscale's view"
if command -v headscale >/dev/null 2>&1; then
  headscale nodes list 2>/dev/null | sed -n '1,20p' | sed 's/^/  /'
  echo "  --- users"
  headscale users list 2>/dev/null | sed 's/^/  /'
fi

hdr "5. skygate's DB (what it THINKS the tag is)"
if [ -r "$DB" ] && command -v sqlite3 >/dev/null 2>&1; then
  echo "  node_owner_map:"; sqlite3 -header "$DB" "select node_id,hostname,username,tag from node_owner_map;" 2>&1 | sed 's/^/  /'
  echo "  last tag-related audit rows:"; sqlite3 "$DB" \
    "select id||' | '||action||' | '||substr(COALESCE(detail,''),1,120) from audit_log where action like '%tag%' or action like '%adopt%' order by id desc limit 12;" 2>&1 | sed 's/^/  /'
else
  echo "  cannot read $DB"
fi

hdr "6. what the service logged about tags"
journalctl -u skygate --no-pager -n 4000 2>/dev/null \
  | grep -iE 'tag-reconcile|tag_missing|not permitted|ensure_tag|tag.*owner|policy|autoupdate' \
  | tail -25 | sed 's/^/  /'

hdr "7. metrics"
curl -s -m 5 "http://127.0.0.1:${SKYGATE_PORT:-8082}/metrics" 2>/dev/null \
  | grep -E 'skygate_tag_(unmatched|autoupdate)' | sed 's/^/  /' || echo "  /metrics not reachable on :${SKYGATE_PORT:-8082}"

hdr "summary"
cat <<'EOT'
  How to read this:
  * section 1 shows whether the reconciler can run at all (skygate active, SKYGATE_BASE_DOMAIN set —
    the base domain is what turns a username into a tagOwner like daniil@<baseDomain>);
  * section 2: a queued policy.request.props with no skygate-policy.path unit means the root helper
    was never installed, so the tagOwners entry can NEVER be written on a ProtectSystem=strict unit;
  * section 3: no tag:dev-* entries in the policy => headscale rejects every tag write with
    '400 requested tags [...] are invalid or not permitted' (that is the live aro symptom);
  * section 4: compare the Tags column with section 5's node_owner_map — the difference IS the drift;
  * section 6: the reconciler reports each refusal with a reason; the reason names the fix.
EOT
