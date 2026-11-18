#!/usr/bin/env bash
# live_gate.sh — run the LIVE-STATE contracts against the installation THIS host serves.
#
# WHY THIS EXISTS (TD-36 / B494)
#
# `scripts/verify_pre_deploy.sh` is a BUILD-TIME catalog: every contract that needs a live installation
# answers SKIP there, and CI has no deployment at all. So the contracts that verify the tailnet's real
# state had to be started by hand, one at a time, on the host that serves it — and nobody did it
# regularly. This is that missing half: one command, one verdict, on the right host.
#
# It is deliberately NOT part of the build-time gate: a build must not require a deployment, and a live
# run must not pretend to be one.
#
# USAGE
#   bash scripts/live_gate.sh                 # run everything, print the summary
#   bash scripts/live_gate.sh --json          # machine-readable summary
#   bash scripts/live_gate.sh --strict        # a SKIP is a failure (cron / nightly use)
#   bash scripts/live_gate.sh --with-post-deploy
#   bash scripts/live_gate.sh --only dup-users,orphans
#
# EXIT
#   0  everything passed (SKIPs allowed unless --strict)
#   1  at least one FAIL (or a SKIP under --strict)
#   2  no live installation on this host — nothing was checked (never a false green)
#
# Each contract runs in its own `bash` (they exit on SKIP, and one SKIP must not end the run); its
# output goes to a per-run directory (B340: never a fixed /tmp path) so a failure can be read back.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
cd "$ROOT" || exit 2

JSON=0
STRICT=0
WITH_POST=0
ONLY=""

while [ $# -gt 0 ]; do
    case "$1" in
        --json)             JSON=1; shift ;;
        --strict)           STRICT=1; shift ;;
        --with-post-deploy) WITH_POST=1; shift ;;
        --only)             ONLY="${2:-}"; shift 2 ;;
        -h|--help)          sed -n '2,30p' "$HERE/live_gate.sh" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "live_gate: unknown flag: $1 (see --help)" >&2; exit 2 ;;
    esac
done

# id <TAB> script <TAB> one-line purpose. Read line by line: no array-of-tuples to mis-quote.
CONTRACTS_LIST="dup-users	scripts/check_b_duplicate_users.sh	headscale user names must be unique (the B243 reconcile relinks by name)
reconcile-audit	scripts/check_b_reconcile_audit_writes.sh	the reconcile cron must leave an audit row (restart-based: docker only)
orphans	scripts/check_b_node_owner_map_orphans.sh	every node_owner_map row must point at a LIVE headscale user
tag-owners	scripts/check_b_tag_owners.sh	the standard tags must exist in the live policy tagOwners
attribution	scripts/check_b_node_attribution.sh	every node must be attributed by node_owner_map (tagged-devices is by design)
b188.2	scripts/check_b188_2.sh	per-CIDR via= must still resolve in the live policy (needs a checkout)"

# shellcheck source=lib/live_db.sh
. "$HERE/lib/live_db.sh"
KIND="$(live_kind)"
if [ "$KIND" = "none" ]; then
    if [ "$JSON" -eq 1 ]; then
        printf '{"kind":"none","verdict":"no-installation","contracts":[]}\n'
    else
        echo "live_gate: no live installation on this host"
        echo "  (no running skygate+headscale container pair and no /etc/skygate/skygate.env)"
        echo "  This runner inspects the host that SERVES the tailnet — run it there."
    fi
    exit 2
fi

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-live.XXXXXX")" || { echo "live_gate: cannot create a scratch dir" >&2; exit 2; }
trap 'rm -rf "$SKY_TMP"' EXIT

PASS_N=0
FAIL_N=0
SKIP_N=0
MISSING_N=0
FAILED_LIST=""
ROWS=""
JSON_ROWS=""

wanted() {
    [ -z "$ONLY" ] && return 0
    case ",$ONLY," in *",$1,"*) return 0 ;; esac
    return 1
}

while IFS='	' read -r id script purpose; do
    [ -n "${id:-}" ] || continue
    wanted "$id" || continue

    if [ "$id" = "b188.2" ] && [ ! -d internal/acl ]; then
        # its sections A..L grep the SOURCE, so outside a checkout it can only report missing files
        SKIP_N=$((SKIP_N + 1))
        ROWS="$ROWS$(printf '%-16s SKIP   %s' "$id" "$purpose")
                        no checkout here (the ACL packages are absent) — run this one from the repository
"
        JSON_ROWS="$JSON_ROWS${JSON_ROWS:+,}{\"id\":\"$id\",\"verdict\":\"skip\",\"reason\":\"no-checkout\"}"
        continue
    fi
    if [ ! -f "$script" ]; then
        MISSING_N=$((MISSING_N + 1))
        ROWS="$ROWS$(printf '%-16s MISSING  %s' "$id" "$purpose")
"
        JSON_ROWS="$JSON_ROWS${JSON_ROWS:+,}{\"id\":\"$id\",\"verdict\":\"missing\"}"
        continue
    fi

    log="$SKY_TMP/live_$(printf '%s' "$id" | tr -c 'a-z0-9' '_').log"
    bash "$script" >"$log" 2>&1
    rc=$?

    # The EXIT STATUS is the authority for FAILURE; "answered" is a separate question. A check that
    # printed a PASS row and exited 0 answered; a check that printed ONLY a SKIP row and exited 0 did
    # not answer (measured: reconcile-audit on a native host) and must not be counted as green here.
    verdict="FAIL"
    if grep -qE '^  FAIL' "$log"; then
        verdict="FAIL"
    elif grep -qE '^  PASS' "$log"; then
        verdict="PASS"
    elif [ "$rc" -eq 0 ] && grep -qE '^  SKIP' "$log"; then
        verdict="SKIP"
    elif [ "$rc" -eq 0 ]; then
        verdict="PASS"
    fi

    detail="$(grep -E '^  (PASS|FAIL|SKIP)' "$log" | tail -1 | sed 's/^  //')"
    case "$verdict" in
        PASS) PASS_N=$((PASS_N + 1)) ;;
        SKIP) SKIP_N=$((SKIP_N + 1)) ;;
        *)    FAIL_N=$((FAIL_N + 1)); FAILED_LIST="$FAILED_LIST $id" ;;
    esac
    ROWS="$ROWS$(printf '%-16s %-5s  %s' "$id" "$verdict" "$purpose")
"
    [ -n "$detail" ] && ROWS="$ROWS$(printf '                        %s' "$detail")
"
    lower="$(printf '%s' "$verdict" | tr 'A-Z' 'a-z')"
    JSON_ROWS="$JSON_ROWS${JSON_ROWS:+,}{\"id\":\"$id\",\"verdict\":\"$lower\",\"rc\":$rc}"
done <<< "$CONTRACTS_LIST"

if [ "$WITH_POST" -eq 1 ] && [ -f scripts/verify_post_deploy.sh ]; then
    log="$SKY_TMP/live_post_deploy.log"
    bash scripts/verify_post_deploy.sh >"$log" 2>&1
    rc=$?
    if [ "$rc" -eq 0 ]; then
        verdict="PASS"; PASS_N=$((PASS_N + 1))
    else
        verdict="FAIL"; FAIL_N=$((FAIL_N + 1)); FAILED_LIST="$FAILED_LIST post-deploy"
    fi
    ROWS="$ROWS$(printf '%-16s %-5s  %s' "post-deploy" "$verdict" "runtime probes against the running panel (/healthz, /readyz)")
"
    lower="$(printf '%s' "$verdict" | tr 'A-Z' 'a-z')"
    JSON_ROWS="$JSON_ROWS${JSON_ROWS:+,}{\"id\":\"post-deploy\",\"verdict\":\"$lower\",\"rc\":$rc}"
fi

verdict="pass"
[ "$FAIL_N" -gt 0 ] && verdict="fail"
[ "$STRICT" -eq 1 ] && [ "$SKIP_N" -gt 0 ] && verdict="fail"

if [ "$JSON" -eq 1 ]; then
    printf '{"kind":"%s","verdict":"%s","passed":%d,"failed":%d,"skipped":%d,"missing":%d,"contracts":[%s]}\n' \
        "$KIND" "$verdict" "$PASS_N" "$FAIL_N" "$SKIP_N" "$MISSING_N" "$JSON_ROWS"
else
    echo "live contracts — installation kind: $KIND"
    echo
    printf '%s' "$ROWS"
    echo
    printf 'verdict: %s   (%d passed, %d failed, %d skipped, %d missing)\n' \
        "$verdict" "$PASS_N" "$FAIL_N" "$SKIP_N" "$MISSING_N"
    if [ "$FAIL_N" -gt 0 ]; then
        echo "failing:$FAILED_LIST"
        echo "  re-run a single check to read its full output (the scratch dir is removed on exit)"
    fi
    if [ "$STRICT" -eq 1 ] && [ "$SKIP_N" -gt 0 ]; then
        echo "strict mode: a SKIP counts as a failure (use --strict only where every check must answer)"
    fi
fi

[ "$verdict" = "pass" ] || exit 1
exit 0
