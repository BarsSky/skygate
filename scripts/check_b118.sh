#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.19 (B118) — tag-owner-from-name enforcement check
#
# Pins the B118 design (AGENTS.md "Tag ownership rules"):
#   - `infra` is the technical user for all exit-nodes/hosts
#   - `skyadmin` (envAdminIdentity) is the operator's personal account
#   - The headscale policy's `tagOwners` section must reflect the
#     actual owner, NOT hardcode skyadmin@ for every via tag.
#
# Pre-fix bugs that B118 catches:
#   1. The via loop in GenerateACLWithViaForPlane used
#      `envAdminIdentity()@domain` for every via tag. With the
#      first-write-wins dedup at the top of the tagOwners block,
#      the via path always won, so per-user-owned infra tags
#      (e.g. tag:dev-infra-emilia) showed as skyadmin@ in the
#      live policy even though the DB had infra@.
#   2. tag:exit-node was hardcoded to skyadmin@. The DESIGN
#      requires infra@ (the tag identifies infrastructure, not
#      admin's personal devices).
#
# What this script verifies (live, on the VM):
#   A. Source: acl.go does NOT have envAdminIdentity for
#      tag:dev- via tags. The via loop MUST parse the owner
#      from the tag name.
#   B. Source: BOTH GenerateACLForPlane and
#      GenerateACLWithViaForPlane emit tag:exit-node with
#      infra@.
#   C. Live: the most recent acl_snapshots row has
#      tag:dev-infra-emilia (and the other 4 infra tags)
#      with `infra@` as owner.
#   D. Live: tag:exit-node is owned by `infra@`.
#   E. DB: every tag:dev-infra-* row in node_owner_map is
#      owned by the portal user `infra`.
#   F. Live: tagOwners does NOT contain any
#      tag:dev-skyadmin-svyatoslava-legacy entry
#      (svyatoslava legacy node was deleted in B118 cleanup).
#
# Exit codes:
#   0 = all contracts hold
#   1 = one or more contracts failed
#===============================================================================

set -uo pipefail
PASS=0; FAIL=0; WARN=0
ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn(){ echo "  WARN  $*"; WARN=$((WARN+1)); }

# Allow override so this script works from /tmp on the VM
: "${SKYGATE_DIR:=$(cd "$(dirname "$0")/.." && pwd)}"
cd "${SKYGATE_DIR}" || exit 1
echo "skygate root: ${SKYGATE_DIR}"

ACL_GO="internal/acl/acl.go"
[ -f "${ACL_GO}" ] || { bad "source file not found: ${ACL_GO}"; exit 1; }

# ------------------------------------------------------------------------------
# Contract A: source — via loop does NOT use envAdminIdentity for tag:dev-
# ------------------------------------------------------------------------------
echo
echo "=== A. via loop owner-from-name (source) ==="
# The via loop in GenerateACLWithViaForPlane must parse
# the owner from the tag name (tag:dev-<user>-<device>).
#
# B118 logic:
#   owner := envAdminIdentity() + "@" + baseDomain  // fallback
#   if strings.HasPrefix(tag, "tag:dev-") {
#       rest := tag[len("tag:dev-"):]
#       if idx := strings.Index(rest, "-"); idx > 0 {
#           owner = rest[:idx] + "@" + baseDomain
#       }
#   }
#
# Check: BOTH the fallback (envAdminIdentity) AND the
# owner-from-name logic (rest[:idx]) must be present.
# Pre-fix code only had the fallback.
awk '/for _, tag := range exitNodeTags/,/^	}/' "${ACL_GO}" > /tmp/viablock.txt
has_fallback=$(grep -c 'envAdminIdentity()' /tmp/viablock.txt || true)
has_parse=$(grep -cE 'rest\[:idx\]' /tmp/viablock.txt || true)
if [ "${has_fallback}" -ge 1 ] && [ "${has_parse}" -ge 1 ]; then
    ok "via loop has fallback (envAdminIdentity) AND owner-from-name (rest[:idx]) — B118 applied"
else
    bad "via loop missing either fallback (envAdminIdentity=${has_fallback}) or owner-from-name (rest[:idx]=${has_parse})"
fi

# ------------------------------------------------------------------------------
# Contract B: source — both functions emit tag:exit-node with infra@
# ------------------------------------------------------------------------------
echo
echo "=== B. tag:exit-node owner = infra@ (source) ==="
# Count instances of `infra@` near the tag:exit-node emit
# (BOTH functions). The new code uses `infra@<baseDomain>`
# (no + concat, baseDomain is interpolated at runtime).
exn_lines=$(grep -nE 'tag:exit-node' "${ACL_GO}" | head -5)
if [ -z "${exn_lines}" ]; then
    bad "tag:exit-node not emitted anywhere in ${ACL_GO}"
else
    infra_emits=$(grep -B1 -A1 'tag:exit-node' "${ACL_GO}" | grep -cE '"infra@"\s*\+' || true)
    if [ "${infra_emits}" -ge 2 ]; then
        ok "tag:exit-node is owned by infra@ in >=2 emit sites (GenerateACLForPlane + GenerateACLWithViaForPlane)"
    else
        # Fallback: search for the literal string `infra@` near tag:exit-node
        # even if the exact emit pattern differs.
        if grep -A3 'tag:exit-node' "${ACL_GO}" | grep -q '"infra@'; then
            ok "tag:exit-node is owned by infra@ (literal string found near emit)"
        else
            bad "tag:exit-node still owned by envAdminIdentity — change to infra@ per B118"
        fi
    fi
fi

# ------------------------------------------------------------------------------
# Contract C: live policy — tag:dev-infra-* have infra@ owner
# ------------------------------------------------------------------------------
echo
echo "=== C. live policy: tag:dev-infra-* owners (live DB) ==="
if [ -f /home/skyadmin/skygate/.env ] && command -v psql >/dev/null 2>&1; then
    DSN=$(grep -E '^SKYGATE_DB_DSN=' /home/skyadmin/skygate/.env 2>/dev/null | head -1 | cut -d= -f2-)
    if [ -n "${DSN}" ]; then
        host=$(echo "${DSN}" | sed -E 's|.*@([^:/]+):.*|\1|')
        port=$(echo "${DSN}" | sed -E 's|.*@[^:/]+:([0-9]+).*|\1|')
        # Use (SELECT max(version) FROM ...) to first get the
        # latest version number, then filter — this avoids
        # evaluating `config::jsonb` on the OLD malformed
        # rows (v<1063 have e.g. "acls": [, which is invalid
        # JSON). ORDER BY version DESC LIMIT 1 in a subquery
        # forces PostgreSQL to materialize the cast on every row
        # before the LIMIT, which fails on the malformed ones.
        out=$(PGPASSWORD=skygate_admin_pass psql -h "${host}" -p "${port}" -U admin -d skygate_staging -A -t -F'|' -c \
            "SELECT tag, owners FROM (SELECT key as tag, value::text as owners FROM jsonb_each_text((SELECT config::jsonb->'tagOwners' FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots)))) t WHERE tag LIKE 'tag:dev-infra-%';" 2>/dev/null)
        if [ -z "${out}" ]; then
            warn "could not query latest acl_snapshots — is the DB up?"
        else
            bad_infra=0
            while IFS='|' read -r tag owners; do
                [ -z "${tag}" ] && continue
                if echo "${owners}" | grep -q '"infra@'; then
                    ok "live policy: ${tag} owner = ${owners}"
                else
                    bad "live policy: ${tag} owner = ${owners} (expected infra@)"
                    bad_infra=$((bad_infra+1))
                fi
            done <<< "${out}"
            if [ "${bad_infra}" -eq 0 ] && [ -n "${out}" ]; then
                ok "all tag:dev-infra-* in live policy owned by infra@"
            fi
        fi
    else
        warn "SKYGATE_DB_DSN not set in /home/skyadmin/skygate/.env — skipping live check"
    fi
else
    warn "psql not on PATH or /home/skyadmin/skygate/.env missing — skipping live check"
fi

# ------------------------------------------------------------------------------
# Contract D: live policy — tag:exit-node has infra@ owner
# ------------------------------------------------------------------------------
echo
echo "=== D. live policy: tag:exit-node owner (live DB) ==="
if [ -n "${DSN:-}" ]; then
    # Use (SELECT max(version) FROM ...) to first get the
    # latest version number, then filter — this avoids
    # evaluating `config::jsonb` on the OLD malformed
    # rows (v<1063 have e.g. "acls": [, which is invalid
    # JSON). ORDER BY version DESC LIMIT 1 in a subquery
    # forces PostgreSQL to materialize the cast on every row
    # before the LIMIT, which fails on the malformed ones.
    out=$(PGPASSWORD=skygate_admin_pass psql -h "${host}" -p "${port}" -U admin -d skygate_staging -A -t -F'|' -c \
        "SELECT value FROM jsonb_each_text((SELECT config::jsonb->'tagOwners' FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots))) WHERE key = 'tag:exit-node';" 2>/dev/null)
    if [ -z "${out}" ]; then
        warn "tag:exit-node not in live policy tagOwners — is the policy applied?"
    elif echo "${out}" | grep -q '"infra@'; then
        ok "live policy: tag:exit-node owner = ${out}"
    else
        bad "live policy: tag:exit-node owner = ${out} (expected infra@)"
    fi
fi

# ------------------------------------------------------------------------------
# Contract E: DB — every tag:dev-infra-* row in node_owner_map
#              is owned by the portal user `infra`.
# ------------------------------------------------------------------------------
echo
echo "=== E. node_owner_map: tag:dev-infra-* owned by 'infra' (live DB) ==="
if [ -n "${DSN:-}" ]; then
    out=$(PGPASSWORD=skygate_admin_pass psql -h "${host}" -p "${port}" -U admin -d skygate_staging -A -t -F'|' -c \
        "SELECT tag, username FROM node_owner_map WHERE tag LIKE 'tag:dev-infra-%' ORDER BY tag;" 2>/dev/null)
    if [ -z "${out}" ]; then
        warn "no tag:dev-infra-* rows in node_owner_map"
    else
        bad_nom=0
        while IFS='|' read -r tag user; do
            [ -z "${tag}" ] && continue
            if [ "${user}" = "infra" ]; then
                ok "nom: ${tag} → ${user}"
            else
                bad "nom: ${tag} → ${user} (expected infra)"
                bad_nom=$((bad_nom+1))
            fi
        done <<< "${out}"
        if [ "${bad_nom}" -eq 0 ]; then
            ok "all tag:dev-infra-* in node_owner_map owned by 'infra'"
        fi
    fi
fi

# ------------------------------------------------------------------------------
# Contract F: live policy — NO tag:dev-skyadmin-svyatoslava-legacy entry
# (svyatoslava legacy node 27 was deleted in the B118 cleanup;
#  if it's back, the cleanup didn't take)
# ------------------------------------------------------------------------------
echo
echo "=== F. live policy: svyatoslava-legacy is GONE (B118 cleanup) ==="
if [ -n "${DSN:-}" ]; then
    # Use text-search (LIKE) on the policy text directly, instead
    # of jsonb_object_keys. Reason: some OLD snapshots (v<1063)
    # have malformed JSON (e.g. "acls": [,), and the jsonb cast
    # fails on those, even with ORDER BY ... LIMIT 1. The text
    # search is safe — we only care whether the substring
    # "svyatoslava-legacy" appears in the LATEST policy.
    out=$(PGPASSWORD=skygate_admin_pass psql -h "${host}" -p "${port}" -U admin -d skygate_staging -A -t -F'|' -c \
        "SELECT count(*) FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots) AND config LIKE '%svyatoslava-legacy%';" 2>/dev/null)
    cnt=$(echo "${out}" | tr -d '[:space:]')
    if [ "${cnt}" = "0" ]; then
        ok "live policy: 0 references to svyatoslava-legacy (B118 cleanup held)"
    else
        bad "live policy: ${cnt} references to svyatoslava-legacy in latest snapshot (run cleanup again)"
    fi
fi

# ------------------------------------------------------------------------------
# Contract G: v1.3.19.1 — svyatoslava-1 is REMOVED entirely
# (svyatoslava-1 / headscale node id=30 was the HA mirror, but the
#  operator declared it offline + not working 2026-08-17. The
#  cleanup deleted the headscale node + the node_owner_map row +
#  re-applied policy. Live state should have ZERO references.)
# ------------------------------------------------------------------------------
echo
echo "=== G. v1.3.19.1: svyatoslava-1 is REMOVED (HA mirror retired) ==="
if [ -n "${DSN:-}" ]; then
    # 1. Policy: 0 references to svyatoslava-1
    out=$(PGPASSWORD=skygate_admin_pass psql -h "${host}" -p "${port}" -U admin -d skygate_staging -A -t -c \
        "SELECT count(*) FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots) AND config::text ILIKE '%svyatoslava-1%';" 2>/dev/null)
    cnt=$(echo "${out}" | tr -d '[:space:]')
    if [ "${cnt}" = "0" ]; then
        ok "live policy: 0 references to svyatoslava-1 (v1.3.19.1 cleanup held)"
    else
        bad "live policy: ${cnt} references to svyatoslava-1 (re-apply or check sync.go auto-add)"
    fi
    # 2. node_owner_map: 0 rows for svyatoslava-1
    out=$(PGPASSWORD=skygate_admin_pass psql -h "${host}" -p "${port}" -U admin -d skygate_staging -A -t -c \
        "SELECT count(*) FROM node_owner_map WHERE tag = 'tag:dev-infra-svyatoslava-1';" 2>/dev/null)
    cnt=$(echo "${out}" | tr -d '[:space:]')
    if [ "${cnt}" = "0" ]; then
        ok "nom: 0 rows for tag:dev-infra-svyatoslava-1 (v1.3.19.1 cleanup held)"
    else
        bad "nom: ${cnt} rows for tag:dev-infra-svyatoslava-1 (BackfillInfra re-added — check sync.go)"
    fi
    # 3. tagOwners: 0 entries for tag:dev-infra-svyatoslava-1
    out=$(PGPASSWORD=skygate_admin_pass psql -h "${host}" -p "${port}" -U admin -d skygate_staging -A -t -c \
        "SELECT count(*) FROM jsonb_each_text((SELECT config::jsonb->'tagOwners' FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots))) WHERE key = 'tag:dev-infra-svyatoslava-1';" 2>/dev/null)
    cnt=$(echo "${out}" | tr -d '[:space:]')
    if [ "${cnt}" = "0" ]; then
        ok "tagOwners: 0 entries for tag:dev-infra-svyatoslava-1 (v1.3.19.1 cleanup held)"
    else
        bad "tagOwners: ${cnt} entries for tag:dev-infra-svyatoslava-1"
    fi
    # 4. tag:dev-infra-* count: should be exactly 4 (was 5 pre-cleanup)
    out=$(PGPASSWORD=skygate_admin_pass psql -h "${host}" -p "${port}" -U admin -d skygate_staging -A -t -c \
        "SELECT count(*) FROM jsonb_each_text((SELECT config::jsonb->'tagOwners' FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots))) WHERE key LIKE 'tag:dev-infra-%';" 2>/dev/null)
    cnt=$(echo "${out}" | tr -d '[:space:]')
    if [ "${cnt}" = "4" ]; then
        ok "tagOwners: exactly 4 tag:dev-infra-* entries (emilia, karolina, sharlotta, skygate-host-1)"
    else
        bad "tagOwners: ${cnt} tag:dev-infra-* entries (expected 4 after svyatoslava-1 removal)"
    fi
    # 5. node_owner_map count: should be exactly 4
    out=$(PGPASSWORD=skygate_admin_pass psql -h "${host}" -p "${port}" -U admin -d skygate_staging -A -t -c \
        "SELECT count(*) FROM node_owner_map WHERE tag LIKE 'tag:dev-infra-%';" 2>/dev/null)
    cnt=$(echo "${out}" | tr -d '[:space:]')
    if [ "${cnt}" = "4" ]; then
        ok "nom: exactly 4 tag:dev-infra-* rows (emilia, karolina, sharlotta, skygate-host-1)"
    else
        bad "nom: ${cnt} tag:dev-infra-* rows (expected 4 after svyatoslava-1 removal)"
    fi
fi

echo
echo "=== summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
