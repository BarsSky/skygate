#!/usr/bin/env bash
# Per-run scratch directory (B340). A FIXED /tmp path is not writable by the
# next run under a different user, which made eight checks report phantom FAILs
# on 2026-10-01. See AGENTS.md trap #13.
SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)" || SKY_TMP="/tmp/skygate-check.$$"
trap 'rm -rf "$SKY_TMP"' EXIT

. "$(dirname "$0")/lib/db_credentials.sh"
SKYGATE_DB_PASSWORD="${SKYGATE_DB_PASSWORD:-$(skygate_db_password)}"
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

# The admin ACL SURFACE, not one file: internal/acl/acl.go was split into
# seven on 2026-10-01 (refactor Phase D) and a contract that greps one path turns
# a pure code move into a false FAIL — and is the weaker contract even while it
# is green. See scripts/lib/gosurface.sh and B339.
. scripts/lib/gosurface.sh
gosurface ACL_GO internal/acl/acl.go internal/acl/acl_apply.go internal/acl/acl_generate.go internal/acl/acl_generate_via.go internal/acl/acl_ownership.go internal/acl/acl_set.go internal/acl/acl_tags.go
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
awk '/for _, tag := range exitNodeTags/,/^	}/' "${ACL_GO}" > ${SKY_TMP}/viablock.txt
has_fallback=$(grep -c 'envAdminIdentity()' ${SKY_TMP}/viablock.txt || true)
# RENEGOTIATED for B288 (2026-09-22): the owner-from-name parsing moved out of
# this loop into the shared helpers `db.PerDeviceTagUser` + `db.TagOwnersForUser`
# (internal/db/device_tag.go), because THREE writers of the same tagOwners entry
# derived three different owner sets — which made the headscale policy drift
# permanent on the live host (the «политика УСТАРЕЛА» banner). The B118 property
# is unchanged: a `tag:dev-*` via tag must NOT be owned by the hardcoded admin
# identity, the owner comes from the tag NAME. Only the parsing location moved,
# so either form satisfies the contract.
has_parse=$(grep -cE 'rest\[:idx\]|db\.PerDeviceTagUser\(tag\)' ${SKY_TMP}/viablock.txt || true)
if [ "${has_fallback}" -ge 1 ] && [ "${has_parse}" -ge 1 ]; then
    ok "via loop has fallback (envAdminIdentity) AND owner-from-name (rest[:idx] or db.PerDeviceTagUser) — B118 applied"
else
    bad "via loop missing either fallback (envAdminIdentity=${has_fallback}) or owner-from-name (rest[:idx]|PerDeviceTagUser=${has_parse})"
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
# Contract C–G: live policy + database (B336 connection)
# ------------------------------------------------------------------------------
# The live contracts below all read the operator's live PostgreSQL and the
# latest ACL snapshot. They used to build a host-side psql call from the DSN
# host, which on the reference deployment is a docker DNS name by design (B278
# removed the rotating bridge IP from it) and therefore does not resolve from
# the host. stderr went to /dev/null, the queries returned nothing, and the
# contracts reported fabricated facts — six FAILs in the 2026-10-01 baseline,
# including `FAIL live policy:  references to svyatoslava-legacy` with an EMPTY
# count. Measured through the helper below, the same deployment answers
# `svyatoslava refs = 0`, `tag:dev-infra-* owners = 4`, `node_owner_map = 4` —
# i.e. every one of those contracts holds.
#
# The helper runs psql INSIDE the postgres container and PROBES first, so an
# unreachable database can never be read as "zero rows" again (AGENTS rule 1).
. "${SKYGATE_DIR}/scripts/lib/db_credentials.sh"
LIVE_OK=0
live_q()      { skygate_live_db_query "$1"; }
live_q_pipe() { skygate_live_db_query "$1" '|'; }

echo
if skygate_live_db_probe; then
    LIVE_OK=1
    echo "  (live DB via ${SKYGATE_LIVE_DB_DESC})"
else
    echo "  SKIP  C-G live contracts — $(skygate_live_db_reason)"
    echo "        these measure the LIVE policy and database; with no connection"
    echo "        there is nothing they can honestly assert (AGENTS rule 1)"
fi

# Contract C: live policy — tag:dev-infra-* have infra@ owner
echo
echo "=== C. live policy: tag:dev-infra-* owners (live DB) ==="
if [ "${LIVE_OK}" = 1 ]; then
    if [ -n "${SKYGATE_LIVE_DB_DESC}" ]; then
        host=$(echo "${DSN}" | sed -E 's|.*@([^:/]+):.*|\1|')
        port=$(echo "${DSN}" | sed -E 's|.*@[^:/]+:([0-9]+).*|\1|')
        # Use (SELECT max(version) FROM ...) to first get the
        # latest version number, then filter — this avoids
        # evaluating `config::jsonb` on the OLD malformed
        # rows (v<1063 have e.g. "acls": [, which is invalid
        # JSON). ORDER BY version DESC LIMIT 1 in a subquery
        # forces PostgreSQL to materialize the cast on every row
        # before the LIMIT, which fails on the malformed ones.
        out=$(live_q_pipe \
            "SELECT tag, owners FROM (SELECT key as tag, value::text as owners FROM jsonb_each_text((SELECT config::jsonb->'tagOwners' FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots)))) t WHERE tag LIKE 'tag:dev-infra-%';" 2>/dev/null)
        if [ -z "${out}" ]; then
            warn "could not query latest acl_snapshots — is the DB up?"
        else
            bad_infra=0
            while IFS='|' read -r tag owners; do
                [ -z "${tag}" ] && continue
                if [[ $owners == *'"infra@'* ]]; then
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
        warn "SKYGATE_DB_DSN not set — skipping live check"
    fi
fi

# ------------------------------------------------------------------------------
# Contract D: live policy — tag:exit-node has infra@ owner
# ------------------------------------------------------------------------------
echo
echo "=== D. live policy: tag:exit-node owner (live DB) ==="
if [ "${LIVE_OK}" = 1 ]; then
    # Use (SELECT max(version) FROM ...) to first get the
    # latest version number, then filter — this avoids
    # evaluating `config::jsonb` on the OLD malformed
    # rows (v<1063 have e.g. "acls": [, which is invalid
    # JSON). ORDER BY version DESC LIMIT 1 in a subquery
    # forces PostgreSQL to materialize the cast on every row
    # before the LIMIT, which fails on the malformed ones.
    out=$(live_q_pipe \
        "SELECT value FROM jsonb_each_text((SELECT config::jsonb->'tagOwners' FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots))) WHERE key = 'tag:exit-node';" 2>/dev/null)
    if [ -z "${out}" ]; then
        warn "tag:exit-node not in live policy tagOwners — is the policy applied?"
    elif [[ $out == *'"infra@'* ]]; then
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
if [ "${LIVE_OK}" = 1 ]; then
    out=$(live_q_pipe \
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
if [ "${LIVE_OK}" = 1 ]; then
    # Use text-search (LIKE) on the policy text directly, instead
    # of jsonb_object_keys. Reason: some OLD snapshots (v<1063)
    # have malformed JSON (e.g. "acls": [,), and the jsonb cast
    # fails on those, even with ORDER BY ... LIMIT 1. The text
    # search is safe — we only care whether the substring
    # "svyatoslava-legacy" appears in the LATEST policy.
    out=$(live_q_pipe \
        "SELECT count(*) FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots) AND config LIKE '%svyatoslava-legacy%';" 2>/dev/null)
    cnt=$(echo "${out}" | tr -d '[:space:]')
    if [ "${cnt}" = "0" ]; then
        ok "live policy: 0 references to svyatoslava-legacy (B118 cleanup held)"
    else
        bad "live policy: ${cnt} references to svyatoslava-legacy in latest snapshot (run cleanup again)"
    fi
fi

# ------------------------------------------------------------------------------
# Contract G: v1.3.19.1 — no UNSANCTIONED tag:dev-infra-* survives (retired HA mirror)
#
# The retired mirror was a real hostname, and AGENTS rule 6 forbids committing
# one, so an earlier redaction pass replaced it with the literal placeholder
# `<polygon-vm-hostname>` — INSIDE THE SQL STRINGS. That made three of these
# contracts vacuous: they searched for a string that cannot exist in the
# database, so "0 references found" was guaranteed and could never fail.
#
# The rewrite that followed froze the reference deployment's FOUR relay names
# (`emilia, karolina, sharlotta, skygate-host`) plus "exactly 4" counts. That was
# honest on the day it was written and wrong a week later: two more real relays
# (`aisha`, `svyatoslava`) joined the tailnet, every one of the six is a genuine
# infra node owned by `infra@`, and the gate went red on a healthy system —
# measured 2026-10-28, four FAIL rows in this section alone. A contract that
# encodes one install's inventory cannot be a gate.
#
# B437 re-expresses it against a source of truth that moves with the deployment:
# the SANCTIONED infra tags are exactly the ones whose node exists in headscale
# right now (`headscale nodes list -o json`). That keeps the original regression
# covered — the retired mirror's node is gone, so its tag is sanctioned by
# nothing — while a new relay needs no code change. The counts become an
# agreement between the two writers (policy and database) instead of a literal.
# ------------------------------------------------------------------------------
echo
echo "=== G. v1.3.19.1/B437: every tag:dev-infra-* names a REAL node (retired HA mirror) ==="
if [ "${LIVE_OK}" = 1 ]; then
    # Live headscale hostnames, lower-cased: the sanctioned universe of this run.
    HS_CTR="${HEADSCALE_CONTAINER:-headscale}"
    HS_HOSTS="$(_skygate_docker exec "$HS_CTR" \
        headscale nodes list -o json 2>/dev/null | python3 -c '
import json, sys
try:
    nodes = json.load(sys.stdin)
except Exception:
    sys.exit(1)
print("\n".join(sorted({(n.get("name") or "").strip().lower() for n in nodes if n.get("name")})))
' 2>/dev/null)"
    if [ -z "${HS_HOSTS}" ]; then
        echo "  SKIP  [G] headscale did not answer \"nodes list\" — the sanctioned set cannot be derived"
        echo "        (a failed read must not become a verdict: AGENTS rule 1)"
    else
        # A quoted SQL IN-list of the live hostnames.
        HS_SQL="$(printf '%s\n' "$HS_HOSTS" | sed "s/^/'/; s/\$/'/" | paste -sd, -)"
        # 1. Policy tagOwners: no infra tag whose node is gone from headscale
        unsanctioned_policy=$(live_q \
            "SELECT coalesce(string_agg(key, ','), '') FROM jsonb_each_text((SELECT config::jsonb->'tagOwners' FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots)))
              WHERE key LIKE 'tag:dev-infra-%'
                AND substring(key from 15) NOT IN (${HS_SQL});" | tr -d '[:space:]')
        if [ -z "${unsanctioned_policy}" ]; then
            ok "tagOwners: every tag:dev-infra-* names a node headscale actually has"
        else
            bad "tagOwners: infra tag(s) whose node is GONE from headscale: ${unsanctioned_policy} — the retired HA mirror (or a deleted relay) is back in the policy"
        fi
        # 2. node_owner_map: the same rule
        unsanctioned_nom=$(live_q \
            "SELECT coalesce(string_agg(DISTINCT tag, ','), '') FROM node_owner_map
              WHERE tag LIKE 'tag:dev-infra-%'
                AND substring(tag from 15) NOT IN (${HS_SQL});" | tr -d '[:space:]')
        if [ -z "${unsanctioned_nom}" ]; then
            ok "nom: every tag:dev-infra-* row names a node headscale actually has"
        else
            bad "nom: infra tag(s) whose node is GONE from headscale: ${unsanctioned_nom} — BackfillInfra kept a deleted relay, check sync.go"
        fi
        # 3. The two writers must agree. A tag present in one and not the other is
        #    JSON drift (the policy was written from an older snapshot) and is worth a
        #    red row; the pre-B437 count of "exactly 4" was this same fact, frozen.
        only_policy=$(live_q \
            "SELECT coalesce(string_agg(k, ','), '') FROM (
                SELECT key AS k FROM jsonb_each_text((SELECT config::jsonb->'tagOwners' FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots))) WHERE key LIKE 'tag:dev-infra-%') p
             WHERE p.k NOT IN (SELECT tag FROM node_owner_map WHERE tag LIKE 'tag:dev-infra-%')" | tr -d '[:space:]')
        only_db=$(live_q \
            "SELECT coalesce(string_agg(tag, ','), '') FROM node_owner_map
              WHERE tag LIKE 'tag:dev-infra-%'
                AND tag NOT IN (SELECT key FROM jsonb_each_text((SELECT config::jsonb->'tagOwners' FROM acl_snapshots WHERE version=(SELECT max(version) FROM acl_snapshots))) WHERE key LIKE 'tag:dev-infra-%')" | tr -d '[:space:]')
        if [ -z "${only_policy}${only_db}" ]; then
            n_infra="$(live_q "SELECT count(*) FROM node_owner_map WHERE tag LIKE 'tag:dev-infra-%';" | tr -d '[:space:]')"
            ok "policy and node_owner_map list the same ${n_infra:-0} infra relay(s) — no drift between the two writers"
        else
            bad "infra drift: policy-only=[${only_policy}] db-only=[${only_db}] — the applied policy and the database disagree"
        fi
        # 4. A legacy/mirror MARKER is never legitimate, whatever the node list says.
        marked=$(live_q \
            "SELECT coalesce(string_agg(tag, ','), '') FROM node_owner_map
              WHERE tag LIKE 'tag:dev-infra-%'
                AND (tag LIKE '%-legacy%' OR tag LIKE '%-mirror%' OR tag LIKE '%-retired%')" | tr -d '[:space:]')
        if [ -z "${marked}" ]; then
            ok "no tag:dev-infra-*-legacy/-mirror/-retired marker anywhere (B118 cleanup still holds)"
        else
            bad "infra tag(s) carrying a legacy/mirror marker: ${marked}"
        fi
    fi
fi

echo
echo "=== summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
